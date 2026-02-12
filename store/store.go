package store

import "sync"

// Store defines the operations for a key-value store.
type Store interface {
	Get(key string) (string, bool)
	Set(key, value string)
	Delete(key string)
}

// MutexStore is a key-value store using sync.RWMutex.
type MutexStore struct {
	mu   sync.RWMutex
	data map[string]string
}

// NewMutexStore creates a new MutexStore.
func NewMutexStore() *MutexStore {
	return &MutexStore{
		data: make(map[string]string),
	}
}

// Get retrieves the value for the given key.
func (s *MutexStore) Get(key string) (string, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	v, ok := s.data[key]
	return v, ok
}

// Set stores a key-value pair.
func (s *MutexStore) Set(key, value string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.data[key] = value
}

// Delete removes the key from the store.
func (s *MutexStore) Delete(key string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.data, key)
}
