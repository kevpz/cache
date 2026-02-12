package store

import (
	"sync"
)

// SyncMapStore is a key-value store using sync.Map.
type SyncMapStore struct {
	m sync.Map
}

// NewSyncMapStore creates a new SyncMapStore.
func NewSyncMapStore() *SyncMapStore {
	return &SyncMapStore{}
}

// Get retrieves the value for the given key.
func (s *SyncMapStore) Get(key string) (string, bool) {
	v, ok := s.m.Load(key)
	if !ok {
		return "", false
	}
	return v.(string), true
}

// Set stores a key-value pair.
func (s *SyncMapStore) Set(key, value string) {
	s.m.Store(key, value)
}

// Delete removes the key from the store.
func (s *SyncMapStore) Delete(key string) {
	s.m.Delete(key)
}
