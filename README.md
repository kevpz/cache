# Cache

In-memory key-value cache in Go. Five concurrent store implementations benchmarked against production workload research from Facebook and Twitter. Served via HTTP REST.

## Research-Driven Benchmarks

Benchmark parameters are grounded in published production traces, not synthetic assumptions:

| Parameter | Our Benchmark | Source |
|---|---|---|
| Operation mix | 97% Get / 2% Set / 1% Delete | Facebook GET:SET = 30:1 ([SIGMETRICS 2012](https://dl.acm.org/doi/10.1145/2254756.2254766)) |
| Key popularity | Zipf s=1.01 and s=1.5 | Twitter: "approximate Zipfian, sometimes very high skew" ([OSDI 2020](https://www.usenix.org/conference/osdi20/presentation/yang)) |
| Value size | 200 bytes | Twitter: median ~230 bytes |
| Key size | ~25 bytes (namespace prefix) | Twitter: 20-100 bytes |
| Eviction | SIEVE algorithm | [NSDI 2024](https://www.usenix.org/conference/nsdi24/presentation/zhang-yazhuo): better miss ratio than LRU on 45%+ of 1559 traces |

Full methodology, contention analysis, and data gaps documented in [`store/BENCHMARKS.md`](store/BENCHMARKS.md).

### Results: Production Storage Workload (97/2/1 Zipf)

**Default skew (s=1.01):**

| Implementation | ns/op | B/op | Strategy |
|---|---|---|---|
| **HashTrieStore** | **16** | 0 | Lock-free reads, zero-copy returns |
| SyncMapStore | 17 | 1 | Lock-free reads, interface boxing |
| StaticMap | 66 | 144 | DOD open addressing, slab copy |
| LRUStore | 83 | 153 | SIEVE eviction, slab copy |
| MutexStore | 126 | 0 | Single RWMutex |

**High skew (s=1.5) -- closer to many production clusters:**

| Implementation | ns/op | B/op |
|---|---|---|
| **HashTrieStore** | **13** | 0 |
| SyncMapStore | 15 | 1 |
| MutexStore | 93 | 0 |
| StaticMap | 118 | 123 |
| LRUStore | 154 | 134 |

HashTrieStore is **9x faster** than LRUStore under high skew. The `B/op` column shows the cost of copying 200-byte values from byte slabs -- invisible at toy value sizes, dominant at production sizes.

### The 32GB Trade-Off

Per-operation throughput is dwarfed by HTTP latency (~1-5 us per request vs 13-154 ns per cache op). At 32GB, the real question is GC:

- **HashTrieStore**: ~100M pointer-bearing heap objects. GC must scan all of them.
- **LRUStore / StaticMap**: ~768 GC objects. Pointer-free data structures (`map[uint64]uint32`, scalar-only slots, `[]byte` slabs). GC is effectively free.

## Store Implementations

| Implementation | Concurrency | Eviction | GC at 32GB | Best for |
|---|---|---|---|---|
| **LRUStore** | Sharded mutex (256) | SIEVE | ~768 objects | Production: bounded memory |
| **StaticMap** | Sharded mutex (256) | None | ~768 objects | Writes, mixed ops |
| **HashTrieStore** | Lock-free reads | None | ~100M objects | Max read throughput |
| **SyncMapStore** | Lock-free reads | None | ~100M objects | Baseline |
| **MutexStore** | Single RWMutex | None | ~3 objects | Simplicity |

**LRUStore** -- Arena-allocated sharded SIEVE cache. On hit: set a visited bit (no list reordering). On eviction: hand pointer scans, gives visited entries a second chance. All key/value bytes in pre-allocated `[]byte` slabs.

**StaticMap** -- Data Oriented Design. Open addressing with linear probing, backward-shift deletion (no tombstones). SoA layout: `[]uint64` probe array (8 hashes per cache line) separate from `[]entry` payload. All memory pre-allocated.

**HashTrieStore** -- Generic `HashTrieMap[string, string]` extracted from Go's `internal/sync.HashTrieMap`. Lock-free reads via atomic pointer traversal. Zero-copy returns.

## Quick Start

```bash
go run ./cmd/                    # run
go test -race ./...              # test
go test -bench=Production -benchmem ./store/  # benchmark
```

### API

```
PUT    /key/{key}   # store (body = value)
GET    /key/{key}   # retrieve (200 or 404)
DELETE /key/{key}   # delete (idempotent)
GET    /health      # {"status":"ok"}
GET    /stats       # entries, bytes, hits, misses, evictions, hit_rate
```

### Configuration

| Variable | Default | Description |
|---|---|---|
| `CACHE_PORT` | `:8080` | Listen address |
| `CACHE_MAX_BYTES` | `34359738368` (32GB) | Maximum cache size |

### Docker

```bash
docker build -t cache .
docker run -p 8080:8080 cache
```

## Project Structure

```
cmd/main.go           Entry point, config, graceful shutdown
server/server.go      HTTP handlers
store/
  store.go            Store interface (Get, Set, Delete)
  hash.go             Shared: fnv1a, sharding constants
  mutex.go            MutexStore
  syncmap.go          SyncMapStore
  hashtrie.go         HashTrieStore + HashTrieMap[K,V]
  lru.go              LRUStore (SIEVE eviction)
  static.go           StaticMap (DOD open addressing)
  bench_test.go       Benchmarks
  BENCHMARKS.md       Research, methodology, results
```

## References

1. Yang et al. ["In-memory cache clusters at Twitter."](https://www.usenix.org/conference/osdi20/presentation/yang) OSDI 2020. [Traces](https://github.com/twitter/cache-trace).
2. Atikoglu et al. ["Workload analysis of a large-scale key-value store."](https://dl.acm.org/doi/10.1145/2254756.2254766) SIGMETRICS 2012.
3. Nishtala et al. ["Scaling Memcache at Facebook."](https://www.usenix.org/conference/nsdi13/technical-sessions/presentation/nishtala) NSDI 2013.
4. Zhang et al. ["SIEVE is Simpler than LRU."](https://www.usenix.org/conference/nsdi24/presentation/zhang-yazhuo) NSDI 2024.
