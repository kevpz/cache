package server

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"cache/store"
)

type errorResponse struct {
	Error string `json:"error"`
}

// New creates an http.Handler for the KV store.
// If lru is non-nil, /stats serves cache statistics.
func New(s store.Store, lru *store.LRUStore) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/key/", keyHandler(s))
	mux.HandleFunc("/health", healthHandler())
	if lru != nil {
		mux.HandleFunc("/stats", statsHandler(lru))
	}
	return mux
}

func keyHandler(s store.Store) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		key := strings.TrimPrefix(r.URL.Path, "/key/")
		if key == "" {
			respondError(w, http.StatusBadRequest, "key is required")
			return
		}

		switch r.Method {
		case http.MethodGet:
			handleGet(w, s, key)
		case http.MethodPut:
			handlePut(w, r, s, key)
		case http.MethodDelete:
			handleDelete(w, s, key)
		default:
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
		}
	}
}

func handleGet(w http.ResponseWriter, s store.Store, key string) {
	value, exists := s.Get(key)
	if !exists {
		respondError(w, http.StatusNotFound, "key not found")
		return
	}

	w.Header().Set("Content-Type", "text/plain")
	w.WriteHeader(http.StatusOK)
	w.Write([]byte(value))
}

func handlePut(w http.ResponseWriter, r *http.Request, s store.Store, key string) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		respondError(w, http.StatusBadRequest, "failed to read request body")
		return
	}
	defer r.Body.Close()

	s.Set(key, string(body))
	w.WriteHeader(http.StatusOK)
}

func handleDelete(w http.ResponseWriter, s store.Store, key string) {
	s.Delete(key)
	w.WriteHeader(http.StatusOK)
}

func healthHandler() http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
	}
}

func statsHandler(lru *store.LRUStore) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			respondError(w, http.StatusMethodNotAllowed, "method not allowed")
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(lru.Stats())
	}
}

func respondError(w http.ResponseWriter, statusCode int, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(statusCode)
	json.NewEncoder(w).Encode(errorResponse{Error: message})
}
