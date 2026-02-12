package store

// Store defines the operations for a key-value store.
type Store interface {
	Get(key string) (string, bool)
	Set(key, value string)
	Delete(key string)
}
