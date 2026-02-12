package store

import (
	"sync"
	"sync/atomic"
	"unsafe"
)

const (
	numShards = 256
	shardMask = numShards - 1
	nullSlot  = ^uint32(0)

	// Per-entry overhead estimate for memory accounting:
	// slot struct size + map bucket share.
	entryOverhead = 64
)

// LRUStore is a sharded cache with arena-allocated storage and SIEVE
// eviction (NSDI'24). On cache hit, only a visited bit is set -- no list
// reordering. On eviction, a hand pointer scans the queue: visited entries
// get a second chance (bit cleared), unvisited entries are evicted.
//
// All key/value data lives in pre-allocated byte slabs. The index map
// uses scalar-only types (uint64 key, uint32 value) so the GC never
// scans cache internals. At 32GB this means ~768 GC objects total
// instead of 100M+.
type LRUStore struct {
	shards   [numShards]shard
	maxBytes int64
}

// NewLRUStore creates a cache with the given memory limit in bytes.
// Memory is pre-allocated across 256 shards.
func NewLRUStore(maxBytes int64) *LRUStore {
	s := &LRUStore{maxBytes: maxBytes}
	perShard := maxBytes / numShards
	estSlots := int(perShard / 128)
	if estSlots < 64 {
		estSlots = 64
	}
	for i := range s.shards {
		sh := &s.shards[i]
		sh.max = perShard
		sh.index = make(map[uint64]uint32, estSlots)
		sh.slots = make([]slot, 0, estSlots)
		sh.data = make([]byte, 0, perShard)
		sh.freeHead = nullSlot
		sh.queueHead = nullSlot
		sh.queueTail = nullSlot
		sh.hand = nullSlot
	}
	return s
}

// Get retrieves the value for the given key, marking it as visited.
func (s *LRUStore) Get(key string) (string, bool) {
	h := fnv1a(key)
	sh := &s.shards[h&shardMask]
	sh.mu.Lock()
	val, ok := sh.get(key, h)
	sh.mu.Unlock()
	return val, ok
}

// Set stores a key-value pair, evicting entries via SIEVE if the
// shard exceeds its memory limit.
func (s *LRUStore) Set(key, value string) {
	h := fnv1a(key)
	sh := &s.shards[h&shardMask]
	sh.mu.Lock()
	sh.set(key, value, h)
	sh.mu.Unlock()
}

// Delete removes the key from the store.
func (s *LRUStore) Delete(key string) {
	h := fnv1a(key)
	sh := &s.shards[h&shardMask]
	sh.mu.Lock()
	sh.del(key, h)
	sh.mu.Unlock()
}

// Stats returns aggregate statistics across all shards.
func (s *LRUStore) Stats() Stats {
	var st Stats
	st.MaxBytes = s.maxBytes
	for i := range s.shards {
		sh := &s.shards[i]
		sh.mu.Lock()
		st.Entries += int64(sh.count)
		st.Bytes += sh.size
		sh.mu.Unlock()
		st.Hits += sh.hits.Load()
		st.Misses += sh.misses.Load()
		st.Evictions += sh.evictions.Load()
	}
	total := st.Hits + st.Misses
	if total > 0 {
		st.HitRate = float64(st.Hits) / float64(total)
	}
	return st
}

// Stats holds aggregate cache statistics.
type Stats struct {
	Entries   int64   `json:"entries"`
	Bytes     int64   `json:"bytes"`
	MaxBytes  int64   `json:"max_bytes"`
	Hits      int64   `json:"hits"`
	Misses    int64   `json:"misses"`
	Evictions int64   `json:"evictions"`
	HitRate   float64 `json:"hit_rate"`
}

// --- shard ---

type shard struct {
	mu        sync.Mutex
	index     map[uint64]uint32 // key hash -> slot index
	slots     []slot
	data      []byte
	dataPos   uint32
	freeHead  uint32
	queueHead uint32 // newest entry (insert here)
	queueTail uint32 // oldest entry
	hand      uint32 // SIEVE eviction scan pointer
	count     int32
	size      int64 // live key+value bytes
	max       int64
	hits      atomic.Int64
	misses    atomic.Int64
	evictions atomic.Int64
}

type slot struct {
	keyOff  uint32
	keyLen  uint16
	valOff  uint32
	valLen  uint32
	prev    uint32 // queue list (doubly-linked for O(1) removal)
	next    uint32 // queue list
	chain   uint32 // hash collision chain
	hash    uint64
	alive   bool
	visited bool // SIEVE: set on access, cleared during eviction scan
}

// get looks up key in the shard. Caller must hold sh.mu.
// SIEVE: only sets visited bit -- no list reordering.
func (sh *shard) get(key string, h uint64) (string, bool) {
	idx, ok := sh.index[h]
	if !ok {
		sh.misses.Add(1)
		return "", false
	}
	for idx != nullSlot {
		sl := &sh.slots[idx]
		if sl.hash == h && sh.keyEquals(sl, key) {
			sl.visited = true
			sh.hits.Add(1)
			return sh.valAt(sl), true
		}
		idx = sl.chain
	}
	sh.misses.Add(1)
	return "", false
}

// set inserts or updates key in the shard. Caller must hold sh.mu.
func (sh *shard) set(key, value string, h uint64) {
	kLen := len(key)
	vLen := len(value)
	newBytes := int64(kLen + vLen)

	// Check if key already exists.
	idx, exists := sh.index[h]
	if exists {
		for cur := idx; cur != nullSlot; cur = sh.slots[cur].chain {
			sl := &sh.slots[cur]
			if sl.hash == h && sh.keyEquals(sl, key) {
				oldValLen := int64(sl.valLen)
				valOff := sh.allocData(value)
				sl.valOff = valOff
				sl.valLen = uint32(vLen)
				sh.size += int64(vLen) - oldValLen
				sl.visited = true
				sh.evictWhileOverLimit()
				sh.maybeCompact()
				return
			}
		}
	}

	// New entry. Evict if needed to make room.
	for sh.size+newBytes > sh.max && sh.queueTail != nullSlot {
		sh.evict()
	}

	slotIdx := sh.allocSlot()
	sl := &sh.slots[slotIdx]

	keyOff := sh.allocData(key)
	valOff := sh.allocData(value)

	sl.keyOff = keyOff
	sl.keyLen = uint16(kLen)
	sl.valOff = valOff
	sl.valLen = uint32(vLen)
	sl.hash = h
	sl.alive = true
	sl.visited = false
	sl.prev = nullSlot
	sl.next = nullSlot

	if exists {
		sl.chain = idx
	} else {
		sl.chain = nullSlot
	}
	sh.index[h] = slotIdx

	sh.pushHead(slotIdx)
	sh.count++
	sh.size += int64(kLen + vLen)

	sh.maybeCompact()
}

// del removes key from the shard. Caller must hold sh.mu.
func (sh *shard) del(key string, h uint64) {
	idx, ok := sh.index[h]
	if !ok {
		return
	}

	var prevChain uint32 = nullSlot
	for cur := idx; cur != nullSlot; cur = sh.slots[cur].chain {
		sl := &sh.slots[cur]
		if sl.hash == h && sh.keyEquals(sl, key) {
			if prevChain == nullSlot {
				if sl.chain == nullSlot {
					delete(sh.index, h)
				} else {
					sh.index[h] = sl.chain
				}
			} else {
				sh.slots[prevChain].chain = sl.chain
			}
			// Advance hand if it points to the entry being deleted.
			if sh.hand == cur {
				sh.hand = sl.next
			}
			sh.unlink(cur)
			sh.size -= int64(sl.keyLen) + int64(sl.valLen)
			sh.count--
			sh.freeSlot(cur)
			return
		}
		prevChain = cur
	}
}

// --- queue operations (doubly-linked for O(1) removal) ---

func (sh *shard) pushHead(idx uint32) {
	sl := &sh.slots[idx]
	sl.prev = nullSlot
	sl.next = sh.queueHead
	if sh.queueHead != nullSlot {
		sh.slots[sh.queueHead].prev = idx
	}
	sh.queueHead = idx
	if sh.queueTail == nullSlot {
		sh.queueTail = idx
	}
}

func (sh *shard) unlink(idx uint32) {
	sl := &sh.slots[idx]
	if sl.prev != nullSlot {
		sh.slots[sl.prev].next = sl.next
	} else {
		sh.queueHead = sl.next
	}
	if sl.next != nullSlot {
		sh.slots[sl.next].prev = sl.prev
	} else {
		sh.queueTail = sl.prev
	}
	sl.prev = nullSlot
	sl.next = nullSlot
}

// --- SIEVE eviction ---

// evict removes one entry using the SIEVE algorithm: scan from hand,
// give visited entries a second chance (clear bit), evict first unvisited.
func (sh *shard) evict() {
	for {
		if sh.hand == nullSlot {
			sh.hand = sh.queueTail // wrap to oldest
		}
		if sh.hand == nullSlot {
			return // empty queue
		}

		idx := sh.hand
		sl := &sh.slots[idx]

		if sl.visited {
			sl.visited = false      // second chance
			sh.hand = sl.prev       // move toward head (newer entries)
			if sh.hand == nullSlot {
				sh.hand = sh.queueTail // wrap
			}
			continue
		}

		// Evict this entry.
		sh.hand = sl.prev
		sh.unchainSlot(idx, sl.hash)
		sh.unlink(idx)
		sh.size -= int64(sl.keyLen) + int64(sl.valLen)
		sh.count--
		sh.evictions.Add(1)
		sh.freeSlot(idx)
		return
	}
}

func (sh *shard) evictWhileOverLimit() {
	for sh.size > sh.max && sh.queueTail != nullSlot {
		sh.evict()
	}
}

func (sh *shard) unchainSlot(idx uint32, h uint64) {
	head, ok := sh.index[h]
	if !ok {
		return
	}
	if head == idx {
		if sh.slots[idx].chain == nullSlot {
			delete(sh.index, h)
		} else {
			sh.index[h] = sh.slots[idx].chain
		}
		return
	}
	prev := head
	for cur := sh.slots[head].chain; cur != nullSlot; cur = sh.slots[cur].chain {
		if cur == idx {
			sh.slots[prev].chain = sh.slots[cur].chain
			return
		}
		prev = cur
	}
}

// --- slot pool (free-list) ---

func (sh *shard) allocSlot() uint32 {
	if sh.freeHead != nullSlot {
		idx := sh.freeHead
		sh.freeHead = sh.slots[idx].next
		sh.slots[idx] = slot{}
		return idx
	}
	idx := uint32(len(sh.slots))
	sh.slots = append(sh.slots, slot{})
	return idx
}

func (sh *shard) freeSlot(idx uint32) {
	sh.slots[idx].alive = false
	sh.slots[idx].visited = false
	sh.slots[idx].next = sh.freeHead
	sh.freeHead = idx
}

// --- data slab (bump allocator + compaction) ---

func (sh *shard) allocData(s string) uint32 {
	n := uint32(len(s))
	off := sh.dataPos
	needed := off + n
	if int(needed) > cap(sh.data) {
		newCap := cap(sh.data) * 2
		if int(needed) > newCap {
			newCap = int(needed) * 2
		}
		newData := make([]byte, needed, newCap)
		copy(newData, sh.data[:off])
		sh.data = newData
	}
	sh.data = sh.data[:needed]
	copy(sh.data[off:], s)
	sh.dataPos = needed
	return off
}

func (sh *shard) maybeCompact() {
	if sh.dataPos > 0 && int64(sh.dataPos) > 2*sh.size && sh.size > 0 {
		sh.compact()
	}
}

func (sh *shard) compact() {
	newData := make([]byte, 0, sh.dataPos)
	var pos uint32
	for i := range sh.slots {
		sl := &sh.slots[i]
		if !sl.alive {
			continue
		}
		oldKey := sh.data[sl.keyOff : sl.keyOff+uint32(sl.keyLen)]
		newKeyOff := pos
		newData = append(newData, oldKey...)
		pos += uint32(sl.keyLen)

		oldVal := sh.data[sl.valOff : sl.valOff+sl.valLen]
		newValOff := pos
		newData = append(newData, oldVal...)
		pos += sl.valLen

		sl.keyOff = newKeyOff
		sl.valOff = newValOff
	}
	sh.data = newData
	sh.dataPos = pos
}

// --- data slab accessors ---

func (sh *shard) keyEquals(sl *slot, key string) bool {
	k := sh.data[sl.keyOff : sl.keyOff+uint32(sl.keyLen)]
	return *(*string)(unsafe.Pointer(&k)) == key
}

func (sh *shard) valAt(sl *slot) string {
	v := sh.data[sl.valOff : sl.valOff+sl.valLen]
	return string(v)
}

// --- hash ---

func fnv1a(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}
