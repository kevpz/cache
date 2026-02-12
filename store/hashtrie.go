package store

import csync "cache/sync"

// HashTrieStore is a key-value store using a typed HashTrieMap.
// It provides the same concurrency properties as sync.Map but
// avoids interface boxing allocations by using generics.
type HashTrieStore struct {
	m csync.HashTrieMap[string, string]
}

// NewHashTrieStore creates a new HashTrieStore.
func NewHashTrieStore() *HashTrieStore {
	return &HashTrieStore{}
}

// Get retrieves the value for the given key.
func (s *HashTrieStore) Get(key string) (string, bool) {
	return s.m.Load(key)
}

// Set stores a key-value pair.
func (s *HashTrieStore) Set(key, value string) {
	s.m.Store(key, value)
}

// Delete removes the key from the store.
func (s *HashTrieStore) Delete(key string) {
	s.m.Delete(key)
}
