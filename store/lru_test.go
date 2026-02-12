package store

import (
	"fmt"
	"strconv"
	"sync"
	"testing"
)

func TestLRU_BasicGetSetDelete(t *testing.T) {
	s := NewLRUStore(1 << 20) // 1MB

	// Get miss.
	v, ok := s.Get("a")
	if ok || v != "" {
		t.Fatalf("expected miss, got %q %v", v, ok)
	}

	// Set and get.
	s.Set("a", "1")
	v, ok = s.Get("a")
	if !ok || v != "1" {
		t.Fatalf("expected hit 1, got %q %v", v, ok)
	}

	// Overwrite.
	s.Set("a", "2")
	v, ok = s.Get("a")
	if !ok || v != "2" {
		t.Fatalf("expected hit 2, got %q %v", v, ok)
	}

	// Delete.
	s.Delete("a")
	v, ok = s.Get("a")
	if ok || v != "" {
		t.Fatalf("expected miss after delete, got %q %v", v, ok)
	}

	// Delete non-existent is a no-op.
	s.Delete("nonexistent")
}

func TestLRU_EvictionOrder(t *testing.T) {
	// Small cache: 256 shards * 50 bytes = 12800 bytes total.
	// Each entry ~8 bytes (4-byte key + 4-byte value), so ~6 entries per shard.
	// 10000 entries across 256 shards = ~39 per shard, forcing heavy eviction.
	s := NewLRUStore(256 * 50)

	for i := 0; i < 10000; i++ {
		s.Set(fmt.Sprintf("k%04d", i), fmt.Sprintf("v%04d", i))
	}

	stats := s.Stats()
	if stats.Evictions == 0 {
		t.Fatal("expected evictions, got 0")
	}
	// Most recent entry should still be accessible.
	v, ok := s.Get("k9999")
	if !ok {
		t.Fatal("expected most recent entry to exist")
	}
	if v != "v9999" {
		t.Fatalf("expected v9999, got %q", v)
	}
}

func TestLRU_MemoryLimit(t *testing.T) {
	maxBytes := int64(256 * 100) // 100 bytes per shard
	s := NewLRUStore(maxBytes)

	// Fill with data. Each entry ~14 bytes (key8+value6), ~7 per shard.
	// 10000 entries = ~39 per shard, forcing eviction.
	for i := 0; i < 10000; i++ {
		s.Set("key"+strconv.Itoa(i), "value"+strconv.Itoa(i))
	}

	stats := s.Stats()
	// Total live bytes should not exceed maxBytes.
	if stats.Bytes > maxBytes {
		t.Fatalf("bytes %d exceeds max %d", stats.Bytes, maxBytes)
	}
	if stats.Evictions == 0 {
		t.Fatal("expected evictions under memory pressure")
	}
}

func TestLRU_GetPromotesToHead(t *testing.T) {
	// Test that Get promotes an entry so it survives longer than
	// entries that were not accessed. We use a statistical approach:
	// insert N entries, promote half via Get, then insert N more
	// to cause eviction. Promoted entries should survive at a
	// higher rate than non-promoted entries.
	s := NewLRUStore(256 * 100) // small per-shard limit

	n := 2000
	// Phase 1: insert entries.
	for i := 0; i < n; i++ {
		s.Set("k"+strconv.Itoa(i), "v")
	}

	// Phase 2: promote even-numbered entries via Get.
	for i := 0; i < n; i += 2 {
		s.Get("k" + strconv.Itoa(i))
	}

	// Phase 3: insert more entries to cause eviction pressure.
	for i := n; i < n*2; i++ {
		s.Set("k"+strconv.Itoa(i), "v")
	}

	// Phase 4: count survivors.
	promotedAlive := 0
	unpromotedAlive := 0
	for i := 0; i < n; i++ {
		_, ok := s.Get("k" + strconv.Itoa(i))
		if ok {
			if i%2 == 0 {
				promotedAlive++
			} else {
				unpromotedAlive++
			}
		}
	}

	// Promoted entries should survive at a higher rate.
	if promotedAlive <= unpromotedAlive {
		t.Fatalf("promotion did not help: promoted=%d, unpromoted=%d",
			promotedAlive, unpromotedAlive)
	}
}

func TestLRU_StatsAccuracy(t *testing.T) {
	s := NewLRUStore(1 << 20)

	s.Set("a", "1")
	s.Set("b", "2")

	s.Get("a")     // hit
	s.Get("b")     // hit
	s.Get("c")     // miss
	s.Get("d")     // miss
	s.Delete("a")

	stats := s.Stats()
	if stats.Hits != 2 {
		t.Fatalf("expected 2 hits, got %d", stats.Hits)
	}
	if stats.Misses != 2 {
		t.Fatalf("expected 2 misses, got %d", stats.Misses)
	}
	if stats.Entries != 1 {
		t.Fatalf("expected 1 entry, got %d", stats.Entries)
	}
}

func TestLRU_ConcurrentAccess(t *testing.T) {
	s := NewLRUStore(1 << 20)
	var wg sync.WaitGroup
	n := 100
	ops := 1000

	wg.Add(n)
	for g := 0; g < n; g++ {
		go func(id int) {
			defer wg.Done()
			prefix := "g" + strconv.Itoa(id) + "_"
			for i := 0; i < ops; i++ {
				k := prefix + strconv.Itoa(i%50)
				switch i % 3 {
				case 0:
					s.Set(k, "v"+strconv.Itoa(i))
				case 1:
					s.Get(k)
				case 2:
					s.Delete(k)
				}
			}
		}(g)
	}
	wg.Wait()

	// Verify stats are consistent.
	stats := s.Stats()
	if stats.Entries < 0 {
		t.Fatalf("negative entry count: %d", stats.Entries)
	}
	if stats.Bytes < 0 {
		t.Fatalf("negative byte count: %d", stats.Bytes)
	}
}

func TestLRU_EmptyKey(t *testing.T) {
	s := NewLRUStore(1 << 20)
	s.Set("", "empty_key_value")
	v, ok := s.Get("")
	if !ok || v != "empty_key_value" {
		t.Fatalf("expected empty key hit, got %q %v", v, ok)
	}
	s.Delete("")
	_, ok = s.Get("")
	if ok {
		t.Fatal("expected miss after delete of empty key")
	}
}

func TestLRU_LargeValues(t *testing.T) {
	s := NewLRUStore(1 << 20) // 1MB

	bigVal := string(make([]byte, 10000))
	s.Set("big", bigVal)
	v, ok := s.Get("big")
	if !ok {
		t.Fatal("expected hit for large value")
	}
	if len(v) != 10000 {
		t.Fatalf("expected 10000 bytes, got %d", len(v))
	}
}

func TestLRU_Compaction(t *testing.T) {
	// Small cache to force compaction.
	s := NewLRUStore(256 * 2000)

	// Insert and delete many entries to create dead space.
	for i := 0; i < 500; i++ {
		s.Set("temp"+strconv.Itoa(i), "value"+strconv.Itoa(i))
	}
	for i := 0; i < 400; i++ {
		s.Delete("temp" + strconv.Itoa(i))
	}

	// Insert more to trigger compaction (dataPos >> size).
	for i := 500; i < 1000; i++ {
		s.Set("temp"+strconv.Itoa(i), "value"+strconv.Itoa(i))
	}

	// Remaining entries should still be readable.
	for i := 400; i < 500; i++ {
		v, ok := s.Get("temp" + strconv.Itoa(i))
		if !ok {
			t.Fatalf("expected temp%d to exist after compaction", i)
		}
		expected := "value" + strconv.Itoa(i)
		if v != expected {
			t.Fatalf("temp%d: expected %q, got %q", i, expected, v)
		}
	}
}

// Verify LRUStore implements the Store interface.
var _ Store = (*LRUStore)(nil)
