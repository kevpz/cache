# Cache

An in-memory key-value cache microservice with HTTP REST API, written in Go. Designed for deployment in microservice architectures with capacity for 32GB of data.

## Features

- SIEVE eviction algorithm (NSDI'24) -- better miss ratio and throughput than LRU
- Arena-allocated storage with near-zero GC pressure (~768 GC objects for 32GB of data)
- 256-shard design for low lock contention
- Pointer-free data structures (`map[uint64]uint32`, scalar-only slots, byte slabs)
- Health check and cache statistics endpoints
- Graceful shutdown (SIGTERM/SIGINT)
- Configuration via environment variables (12-factor)
- Docker support with `GOMEMLIMIT` tuning
- No external dependencies (standard library only)

## Getting Started

### Prerequisites

- Go 1.24 or later

### Running

```bash
go run ./cmd/
```

### Building

```bash
go build -o cache ./cmd/
./cache
```

### Docker

```bash
docker build -t cache .
docker run -p 8080:8080 -e CACHE_MAX_BYTES=34359738368 cache
```

### Testing

```bash
go test ./...
go test -race ./...
```

### Benchmarks

```bash
# Production workload (97/2/1 Zipf)
go test -bench=Production -benchmem -count=3 ./store/

# All benchmarks
go test -bench=. -benchmem -count=1 ./store/

# 4GB scale benchmark (requires ~8GB RAM, takes several minutes)
go test -bench=Scale4GB -benchmem -benchtime=10s -timeout=600s ./store/
```

## Configuration

| Variable | Default | Description |
|---|---|---|
| `CACHE_PORT` | `:8080` | Listen address |
| `CACHE_MAX_BYTES` | `34359738368` (32GB) | Maximum cache size in bytes |

## API

### PUT /key/{key}

Store a value.

```bash
curl -X PUT http://localhost:8080/key/mykey -d "myvalue"
# 200 OK
```

### GET /key/{key}

Retrieve a value.

```bash
curl http://localhost:8080/key/mykey
# 200 OK: myvalue
# 404 Not Found: {"error":"key not found"}
```

### DELETE /key/{key}

Delete a value. Idempotent (returns 200 even if key doesn't exist).

```bash
curl -X DELETE http://localhost:8080/key/mykey
# 200 OK
```

### GET /health

Health check.

```bash
curl http://localhost:8080/health
# {"status":"ok"}
```

### GET /stats

Cache statistics.

```bash
curl http://localhost:8080/stats
# {"entries":1000,"bytes":128000,"max_bytes":34359738368,"hits":9500,"misses":500,"evictions":100,"hit_rate":0.95}
```

## Architecture

```
.
├── cmd/
│   └── main.go              # Entry point: config, LRUStore, graceful shutdown
├── server/
│   ├── server.go            # HTTP handlers: /key/, /health, /stats
│   └── server_test.go       # HTTP handler tests
├── store/
│   ├── store.go             # Store interface (Get, Set, Delete)
│   ├── lru.go               # Production store: arena-allocated sharded SIEVE cache
│   ├── lru_test.go          # SIEVE cache tests
│   ├── hashtrie.go          # HashTrieStore: typed concurrent hash trie
│   ├── syncmap.go           # SyncMapStore: sync.Map wrapper
│   ├── bench_test.go        # Comprehensive benchmarks (serial, production, stress, scaling)
│   ├── store_test.go        # Store interface conformance tests
│   └── BENCHMARKS.md        # Benchmark design documentation
├── sync/
│   └── map.go               # Generic HashTrieMap[K,V] extracted from Go internals
├── Dockerfile
├── go.mod
└── README.md
```

## Store Implementations

Four implementations of the `Store` interface, each with different trade-offs:

| Implementation | Concurrency | Eviction | GC at 32GB | Best for |
|---|---|---|---|---|
| **LRUStore** | Sharded mutex (256) | SIEVE | ~768 objects | Production (bounded memory) |
| **HashTrieStore** | Lock-free reads | None | ~100M objects | Unbounded caches, max read throughput |
| **SyncMapStore** | Lock-free reads | None | ~100M objects | Benchmarking baseline |
| **MutexStore** | Single RWMutex | None | ~3 objects | Simplicity, low entry counts |

### LRUStore (Production)

Arena-allocated sharded cache with SIEVE eviction (NSDI'24). The index map uses `map[uint64]uint32` (scalar-only, GC-invisible). Slot arrays contain no pointers. All key/value bytes live in pre-allocated `[]byte` slabs. On cache hit, only a `visited` bit is set -- no list reordering. Eviction scans from a hand pointer, giving visited entries a second chance.

### HashTrieStore

Typed generic `HashTrieMap[string, string]` extracted from Go's `internal/sync.HashTrieMap`. Uses `hash/maphash.WriteComparable` for hashing and `reflect` for value equality, replacing internal dependencies (`abi`, `goarch`, `race`) with public equivalents. Lock-free reads, fine-grained locking on writes.

## Design Principles

- Think carefully before writing code
- Model the problem space accurately
- Follow mathematical and engineering principles
- Choose the simplest (not easiest) design
- Avoid accidental complexity
- Every line of code is a liability
