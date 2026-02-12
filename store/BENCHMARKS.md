# Store Benchmark Design

## Motivation

Four `Store` implementations with fundamentally different strategies:

- **MutexStore**: `map[string]string` + `sync.RWMutex`. Single lock, zero allocations.
- **SyncMapStore**: `sync.Map` (untyped). Lock-free reads, 3 allocs/80B per write from `any` boxing.
- **HashTrieStore**: `HashTrieMap[string, string]` (typed, extracted from Go internals). Lock-free reads, 1 alloc/48B per write (entry node). No interface boxing.
- **LRUStore**: Arena-allocated sharded cache with SIEVE eviction (NSDI'24). Pointer-free `map[uint64]uint32`, byte slabs, ~768 GC objects at 32GB. Every read requires a shard lock (to set visited bit) and a string copy from the slab.

The benchmarks answer: **under what conditions does each implementation perform better, and what is the cost of bounded memory (eviction) vs unbounded (no eviction)?**

## Research: Production Cache Workloads

### Operation Distributions

**Facebook SIGMETRICS (2012)**: 284B Memcached requests. GET/SET = 30:1 (~97% reads).

**Twitter OSDI'20**: 153 clusters, 80TB traces. Average GET ~90%. 35%+ write-heavy (>30% writes). DELETE typically 1-5%.

| Workload | Get | Set | Delete | Source |
|---|---|---|---|---|
| Storage cache | 97% | 2% | 1% | Facebook Memcached |
| Mixed | 80% | 15% | 5% | General-purpose |
| Write-heavy | 40% | 40% | 20% | Twitter computation/transient |

### Key Access Distribution

Production caches follow Zipfian popularity (Twitter OSDI'20, Brooker 2023). Using Zipf(s=1.01): top 1% of keys get ~25% of accesses; top 10% get ~60%.

### SIEVE Eviction Algorithm

**Source: NSDI'24, CMU/Emory.** SIEVE uses a queue + visited bit + hand pointer. On cache hit, only the visited bit is set (no list reordering). On eviction, the hand scans: visited entries get a second chance (bit cleared), unvisited entries are evicted.

- Up to 63.2% lower miss ratio than ARC
- Better than 9 state-of-the-art algorithms on 45%+ of 1559 traces
- 2x throughput of optimized 16-thread LRU (no per-read list manipulation)
- Adopted in production: TiDB, DragonFly, PostgREST, DNSCrypt

## Benchmark Categories

### A. Serial Baselines

Per-operation cost with zero contention.

| Benchmark | Operation | Setup |
|---|---|---|
| GetHit | Get | Pre-populated |
| GetMiss | Get | Empty store |
| Set | Set | None |
| DeleteHit | Delete | Set+Delete per iteration |
| DeleteMiss | Delete | Empty store |

### B. Production Workloads (Zipf key distribution)

100,000 keys with Zipf(s=1.01) access skew. Pre-generated 10M Zipf indices.

| Benchmark | Get/Set/Delete |
|---|---|
| ProductionStorage | 97/2/1 |
| ProductionMixed | 80/15/5 |
| ProductionWriteHeavy | 40/40/20 |

### C. Stress Tests (2M keys, 50% hot contention)

2,000,000 keys, 100 hot keys, 50% of operations target hot set (~10,000x traffic per hot key).

| Benchmark | Mix |
|---|---|
| StressRead | 100% Get |
| StressWrite | 100% Set |
| StressMixed | 33/33/33 |
| StressAllOps | 25/50/25 |

### D. Scaling (key space size)

Uniform access at 1k / 100k / 5M keys.

| Benchmark | Operation |
|---|---|
| ScaleRead | Parallel Get |
| ScaleWrite | Parallel Set |
| ScaleMixed | 80/15/5 |

### E. Key Distribution Patterns

| Benchmark | Pattern |
|---|---|
| DisjointKeys | Per-goroutine prefix |
| ContendedKeys | 10 hot keys |

### F. 4GB Scale (30M entries)

Pre-populates 30M entries (~4GB), runs 97/2/1 Zipf workload. Reports throughput, allocations, and GC statistics via `runtime.ReadMemStats`.

Only tests HashTrieStore and LRUStore (the production candidates).

## Key Results

### ProductionStorage (97/2/1, Zipf) -- small scale

| Implementation | ns/op | B/op | allocs/op |
|---|---|---|---|
| MutexStore | 182 | 0 | 0 |
| SyncMapStore | 18 | 1 | 0 |
| HashTrieStore | **14** | **0** | **0** |
| LRUStore (SIEVE) | 76 | 3 | 0 |

### 4GB Scale (30M entries, 97/2/1, Zipf)

| Implementation | ns/op | B/op | allocs/op | GC cycles | GC pause | Heap |
|---|---|---|---|---|---|---|
| HashTrieStore | **55** | 11 | 1 | 0 | 0 ms | 6.1 GB |
| LRUStore (SIEVE) | 123 | 96 | 2 | 1 | 0.2 ms | 9.4 GB |

HashTrieStore wins by 2.2x at 4GB because:
1. GC pressure didn't materialize at 97% reads -- write rate too low to trigger frequent collections
2. LRUStore pays per-read: shard lock + visited bit + string copy from byte slab
3. HashTrieStore reads are lock-free with zero-copy string returns

LRUStore's arena advantage matters more at higher write rates or under tight `GOMEMLIMIT` where GC runs continuously.

### Where LRUStore wins

| Benchmark | HashTrieStore | LRUStore |
|---|---|---|
| StressWrite (2M keys) | 136 ns | **56 ns** |
| ScaleWrite (1k keys) | 63 ns | **45 ns** |
| ContendedKeys (10 hot) | 136 ns | **134 ns** |

LRUStore is the fastest writer: arena allocation avoids per-write heap allocation entirely.

### Where HashTrieStore wins

| Benchmark | HashTrieStore | LRUStore |
|---|---|---|
| ProductionStorage | **14 ns** | 76 ns |
| ScaleRead (1k keys) | **4 ns** | 47 ns |
| StressRead (2M keys) | **21 ns** | 64 ns |

Lock-free reads + zero-copy returns dominate read-heavy workloads.

## Running Benchmarks

```bash
# All benchmarks, all implementations
go test -bench=. -benchmem -count=1 ./store/

# Production workloads only
go test -bench=Production -benchmem -count=3 ./store/

# Stress tests with CPU scaling
go test -bench=Stress -benchmem -count=3 -cpu 1,4,8,16 ./store/

# 4GB scale (requires ~8GB RAM)
go test -bench=Scale4GB -benchmem -benchtime=10s -timeout=600s ./store/

# Serial baselines
go test -bench='Benchmark(Get|Set|Delete)(Hit|Miss)?$' -benchmem -count=3 ./store/
```

## References

1. Yang, Yue, Rashmi. "A large scale analysis of hundreds of in-memory cache clusters at Twitter." OSDI 2020. USENIX.
2. Atikoglu et al. "Workload analysis of a large-scale key-value store." SIGMETRICS 2012. ACM.
3. Nishtala et al. "Scaling Memcache at Facebook." NSDI 2013. USENIX.
4. Brooker. "Hot Keys, Scalability, and the Zipf Distribution." 2023. brooker.co.za.
5. Bendersky. "Common pitfalls in Go benchmarking." 2023. eli.thegreenplace.net.
6. Go standard library. `sync/map_bench_test.go`. github.com/golang/go.
7. Zhang et al. "SIEVE is Simpler than LRU: an Efficient Turn-Key Eviction Algorithm for Web Caches." NSDI 2024. USENIX.
8. Yang et al. "FIFO queues are all you need for cache eviction." SOSP 2023. ACM.
