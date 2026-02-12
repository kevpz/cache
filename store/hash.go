package store

// Shared sharding constants used by LRUStore.
const (
	numShards = 256
	shardMask = numShards - 1
)

// fnv1a computes FNV-1a hash of s.
func fnv1a(s string) uint64 {
	h := uint64(14695981039346656037)
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= 1099511628211
	}
	return h
}

// nextPow2 rounds v up to the next power of 2.
func nextPow2(v uint32) uint32 {
	if v == 0 {
		return 1
	}
	v--
	v |= v >> 1
	v |= v >> 2
	v |= v >> 4
	v |= v >> 8
	v |= v >> 16
	v++
	return v
}
