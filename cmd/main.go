package main

import (
	"context"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"syscall"
	"time"

	"cache/server"
	"cache/store"
)

func main() {
	port := envOr("CACHE_PORT", ":8080")
	maxBytes := envInt64("CACHE_MAX_BYTES", 32<<30) // 32GB default

	cache := store.NewLRUStore(maxBytes)
	handler := server.New(cache, cache)

	srv := &http.Server{
		Addr:    port,
		Handler: handler,
	}

	// Start server in background.
	go func() {
		log.Printf("cache service starting on %s (max_bytes=%d)", port, maxBytes)
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("server failed: %v", err)
		}
	}()

	// Wait for shutdown signal.
	stop := make(chan os.Signal, 1)
	signal.Notify(stop, syscall.SIGTERM, syscall.SIGINT)
	<-stop

	log.Println("shutting down...")
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := srv.Shutdown(ctx); err != nil {
		log.Fatalf("shutdown failed: %v", err)
	}
	log.Println("stopped")
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

func envInt64(key string, fallback int64) int64 {
	v := os.Getenv(key)
	if v == "" {
		return fallback
	}
	n, err := strconv.ParseInt(v, 10, 64)
	if err != nil {
		log.Fatalf("invalid %s: %v", key, err)
	}
	return n
}
