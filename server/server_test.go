package server

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"cache/store"
)

func TestHandleGet(t *testing.T) {
	s := store.NewMutexStore()
	s.Set("testkey", "testvalue")
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/key/testkey", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if w.Body.String() != "testvalue" {
		t.Errorf("expected body %q, got %q", "testvalue", w.Body.String())
	}
}

func TestHandleGet_NotFound(t *testing.T) {
	s := store.NewMutexStore()
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/key/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusNotFound {
		t.Errorf("expected status %d, got %d", http.StatusNotFound, w.Code)
	}
}

func TestHandlePut(t *testing.T) {
	s := store.NewMutexStore()
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodPut, "/key/testkey", bytes.NewBufferString("testvalue"))
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	value, exists := s.Get("testkey")
	if !exists {
		t.Errorf("expected key to exist after PUT")
	}
	if value != "testvalue" {
		t.Errorf("expected value %q, got %q", "testvalue", value)
	}
}

func TestHandleDelete(t *testing.T) {
	s := store.NewMutexStore()
	s.Set("testkey", "testvalue")
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodDelete, "/key/testkey", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	_, exists := s.Get("testkey")
	if exists {
		t.Errorf("expected key to be deleted")
	}
}

func TestHandleDelete_Idempotent(t *testing.T) {
	s := store.NewMutexStore()
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodDelete, "/key/nonexistent", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
}

func TestHandle_MethodNotAllowed(t *testing.T) {
	s := store.NewMutexStore()
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodPost, "/key/testkey", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusMethodNotAllowed {
		t.Errorf("expected status %d, got %d", http.StatusMethodNotAllowed, w.Code)
	}
}

func TestHandle_EmptyKey(t *testing.T) {
	s := store.NewMutexStore()
	handler := New(s, nil)

	req := httptest.NewRequest(http.MethodGet, "/key/", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusBadRequest {
		t.Errorf("expected status %d, got %d", http.StatusBadRequest, w.Code)
	}
}

func TestHealthEndpoint(t *testing.T) {
	handler := New(store.NewMutexStore(), nil)

	req := httptest.NewRequest(http.MethodGet, "/health", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}
	if ct := w.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("expected application/json, got %q", ct)
	}
}

func TestStatsEndpoint(t *testing.T) {
	lru := store.NewLRUStore(1 << 20)
	lru.Set("a", "1")
	lru.Get("a")
	lru.Get("miss")
	handler := New(lru, lru)

	req := httptest.NewRequest(http.MethodGet, "/stats", nil)
	w := httptest.NewRecorder()
	handler.ServeHTTP(w, req)

	if w.Code != http.StatusOK {
		t.Errorf("expected status %d, got %d", http.StatusOK, w.Code)
	}

	var stats store.Stats
	if err := json.NewDecoder(w.Body).Decode(&stats); err != nil {
		t.Fatalf("failed to decode stats: %v", err)
	}
	if stats.Entries != 1 {
		t.Errorf("expected 1 entry, got %d", stats.Entries)
	}
	if stats.Hits != 1 {
		t.Errorf("expected 1 hit, got %d", stats.Hits)
	}
	if stats.Misses != 1 {
		t.Errorf("expected 1 miss, got %d", stats.Misses)
	}
}
