package store

import (
	"sync"
	"testing"
)

var implementations = []struct {
	name string
	new  func() Store
}{
	{"MutexStore", func() Store { return NewMutexStore() }},
	{"SyncMapStore", func() Store { return NewSyncMapStore() }},
	{"HashTrieStore", func() Store { return NewHashTrieStore() }},
	{"StaticMap", func() Store { return NewStaticMap(100_000, 16<<20) }},
}

func TestStore_Get(t *testing.T) {
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.new()

			value, exists := s.Get("nonexistent")
			if exists {
				t.Errorf("expected exists=false, got exists=true")
			}
			if value != "" {
				t.Errorf("expected empty value, got %q", value)
			}

			s.Set("test", "value")
			value, exists = s.Get("test")
			if !exists {
				t.Errorf("expected exists=true, got exists=false")
			}
			if value != "value" {
				t.Errorf("expected value=%q, got %q", "value", value)
			}
		})
	}
}

func TestStore_Set(t *testing.T) {
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.new()

			s.Set("key1", "value1")
			value, exists := s.Get("key1")
			if !exists {
				t.Errorf("expected key to exist after Set")
			}
			if value != "value1" {
				t.Errorf("expected value=%q, got %q", "value1", value)
			}

			s.Set("key1", "value2")
			value, _ = s.Get("key1")
			if value != "value2" {
				t.Errorf("expected overwritten value=%q, got %q", "value2", value)
			}
		})
	}
}

func TestStore_Delete(t *testing.T) {
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.new()

			s.Delete("nonexistent") // should not panic

			s.Set("key1", "value1")
			s.Delete("key1")
			_, exists := s.Get("key1")
			if exists {
				t.Errorf("expected key to be deleted")
			}
		})
	}
}

func TestStore_EmptyKey(t *testing.T) {
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.new()

			s.Set("", "empty")
			value, exists := s.Get("")
			if !exists {
				t.Errorf("expected empty key to exist")
			}
			if value != "empty" {
				t.Errorf("expected value=%q, got %q", "empty", value)
			}

			s.Delete("")
			_, exists = s.Get("")
			if exists {
				t.Errorf("expected empty key to be deleted")
			}
		})
	}
}

func TestStore_ConcurrentAccess(t *testing.T) {
	for _, impl := range implementations {
		t.Run(impl.name, func(t *testing.T) {
			s := impl.new()
			const n = 100

			var wg sync.WaitGroup
			wg.Add(n * 3)

			for i := 0; i < n; i++ {
				go func(id int) {
					defer wg.Done()
					s.Set("key"+string(rune('0'+id%10)), "value")
				}(i)
			}
			for i := 0; i < n; i++ {
				go func(id int) {
					defer wg.Done()
					s.Get("key" + string(rune('0'+id%10)))
				}(i)
			}
			for i := 0; i < n; i++ {
				go func(id int) {
					defer wg.Done()
					s.Delete("key" + string(rune('0'+id%10)))
				}(i)
			}

			wg.Wait()
		})
	}
}
