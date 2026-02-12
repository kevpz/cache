# Store Benchmark Design

## Motivation

Five `Store` implementations with fundamentally different strategies:

- **MutexStore**: `map[string]string` + `sync.RWMutex`. Single lock, zero allocations.
- **SyncMapStore**: `sync.Map` (untyped). Lock-free reads, 3 allocs/80B per write from `any` boxing.
- **HashTrieStore**: `HashTrieMap[string, string]` (typed, extracted from Go internals). Lock-free reads, 1 alloc/48B per write (entry node). No interface boxing.
- **LRUStore**: Arena-allocated sharded cache with SIEVE eviction (NSDI'24). Pointer-free `map[uint64]uint32`, byte slabs, ~768 GC objects at 32GB. Every read requires a shard lock (to set visited bit) and a string copy from the slab.
- **StaticMap**: DOD open-addressing hash map with linear probing and backward-shift deletion. SoA layout (separate `[]uint64` probe array, `[]entry` payload array, `[]byte` data slab). 256 shards, pre-allocated, zero steady-state allocation, ~768 GC objects.

The benchmarks answer: **under what conditions does each implementation perform better, and what is the cost of bounded memory (eviction) vs unbounded (no eviction)?**

## Research: Production Cache Workloads

### Sources

1. **Facebook** (Atikoglu et al., [SIGMETRICS 2012](https://dl.acm.org/doi/10.1145/2254756.2254766)): 284 billion Memcached requests, 5 workload types.
2. **Twitter** (Yang et al., [OSDI 2020](https://www.usenix.org/conference/osdi20/presentation/yang)): 153 cache clusters, 700 billion requests, 80 TB of traces. [Public traces](https://github.com/twitter/cache-trace).
3. **Facebook** (Nishtala et al., [NSDI 2013](https://www.usenix.org/conference/nsdi13/technical-sessions/presentation/nishtala)): Scaling Memcache, operational analysis.
4. **SIEVE** (Zhang et al., [NSDI 2024](https://www.usenix.org/conference/nsdi24/presentation/zhang-yazhuo)): Eviction algorithm, 1559 traces from 7 sources.

### Operation Distributions (Get / Set / Delete)

**Facebook SIGMETRICS**: GET:SET = 30:1 (~97% reads). Delete not separately tabulated.

**Twitter OSDI'20**: Average get ratio ~90%. `get` and `set` dominate; `delete`, `incr`, `add`, `cas` are used but in smaller fractions. **35%+ of clusters are write-heavy** (>30% writes). **20%+ have >50% writes.** Write-heavy caches are far more common than previously assumed.

Three production archetypes from Twitter:

| Use Case | % of Clusters | % of QPS | Typical Get/Set/Delete |
|---|---|---|---|
| **Storage** (DB frontend) | 30% | **65%** | ~97/2/1 |
| **Computation** (ML features) | 50% | 26% | ~60-80/15-35/2-5 |
| **Transient** (rate limiters) | 20% | 9% | ~40-80/15-50/5-15 |

Storage caches produce 65% of all QPS despite being only 30% of clusters. The 97/2/1 benchmark models the dominant production pattern.

### Key Popularity Distribution

**Twitter OSDI'20**: "In-memory caching workloads follow approximate Zipfian popularity distribution, sometimes with very high skew. The workloads that show the most deviations tend to be write-heavy workloads." Many workloads are "far more skewed than previously shown."

General literature: CDN/web alpha ≈ 0.8-1.0; in-memory cache alpha ≈ 1.0-1.5+. Our benchmark uses Zipf(s=1.01), at the low end of in-memory cache skew. Real production may be more skewed.

Twitter production mitigates extreme hot keys via **client-side caching**: the server detects hot keys via a sliding-window sampler and signals clients to cache locally, so the server sees a filtered distribution.

### Key and Value Size Distributions

**Twitter OSDI'20**: Median object size ~230 bytes. 25% of clusters have mean <100 bytes. Keys contain namespace prefixes (e.g., `"ns1:ns2:obj"`), typically 20-100 bytes. Size distributions are **dynamic** (diurnal patterns, sudden changes). Range: few bytes to 10s of KB.

**Facebook SIGMETRICS**: ETC workload keys ~20 bytes, values ~200 bytes. Wide variation across workload types.

Our benchmarks use ~10-byte keys and 5-byte values, which underweights real production sizes. The 4GB scale benchmark uses 100-byte values.

### TTL (Time-to-Live)

**Twitter OSDI'20**: 66% of clusters have mean TTL ≤ 12 hours. 25%+ have mean TTL < 20 minutes. TTL is mandatory (GDPR). "Efficiently removing expired objects from cache needs to be prioritized over cache eviction." FIFO performed as well as LRU for many workloads, partly because TTL-driven expiration dominates eviction.

Our benchmarks do not model TTL. This is a gap.

### Contention

**No published study directly measures per-key or per-shard temporal contention** — the probability that two threads/goroutines access the same key within the same nanosecond window. Papers measure popularity (access frequency over time), not contention (temporal overlap within a single process).

**Estimation from published data:**

Facebook reported billions of requests/sec across 800+ servers. At ~10M QPS per server with 8 cores and 256 shards:

```
10M QPS / 256 shards = ~40K QPS per shard
40K QPS × 30ns per operation = 0.12% instantaneous shard occupancy
```

Even with Zipf skew where the hottest shard gets 10x average traffic:

```
400K QPS × 30ns = 1.2% occupancy
```

**Per-shard contention in production is extremely low.** The mutex is held for ~30ns, and even the hottest shard has <2% collision probability. This was confirmed experimentally: Mutex+256 shards and Mutex+2048 shards performed identically on the 97/2/1 Zipf workload (37.0 ns vs 37.4 ns). Switching to `sync.RWMutex` was strictly worse because RWMutex's higher per-call cost (~25 ns for RLock+RUnlock vs ~15 ns for Lock+Unlock) exceeded the negligible contention savings.

### Data We Do Not Have

| Dimension | Status | Notes |
|---|---|---|
| Per-key contention rate | **No data** | Papers measure popularity, not temporal overlap |
| Goroutine parallelism per process | **No data** | Deployment-dependent (GOMAXPROCS) |
| Delete ratio (isolated) | **Partial** | Twitter shows ~2-5% median, high variance |
| Value update frequency per key | **No data** | Write ratio ≠ update ratio |
| Working set vs cache size ratio | **Partial** | Bounded by TTL; varies widely |
| TTL distributions | **Available** | Twitter traces include TTL; not modeled in our benchmarks |

### Benchmark Coverage Assessment

| Dimension | Our Benchmark | Production Data | Match |
|---|---|---|---|
| Operation mix (97/2/1) | Modeled | Facebook 30:1, Twitter storage | Good |
| Operation mix (write-heavy) | Modeled (40/40/20) | Twitter 35% of clusters | Good |
| Key popularity (Zipf s=1.01) | Modeled | Production is 1.0-1.5+ | Conservative |
| Key/value sizes | Fixed ~10B/5B | Production median ~230B | **Underweight** |
| Parallelism (10 goroutines) | Modeled | GOMAXPROCS = 8-64 | Reasonable |
| Contention per shard | <2% | Estimated <2% at 10M QPS | **Good** |
| TTL | Not modeled | Critical in production | **Gap** |
| Size variability over time | Not modeled | Dynamic, diurnal | **Gap** |

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

Only tests HashTrieStore and LRUStore (the large-scale production candidates).

## Key Results

### Serial Baselines (ns/op)

| Benchmark | MutexStore | SyncMap | HashTrie | LRUStore | StaticMap |
|---|---|---|---|---|---|
| GetHit | 33 | **21** | 30 | 61 | 32 |
| GetMiss | 15 | **9** | 14 | 12 | 11 |
| Set | 33 | 88 | 56 | 49 | **28** |
| DeleteHit | 51 | 91 | 65 | 225 | **40** |
| DeleteMiss | 19 | **11** | 15 | 11 | 11 |

StaticMap is the fastest writer (28 ns Set, 40 ns DeleteHit) due to in-place open-addressing mutation with no allocation. SyncMapStore wins serial reads (lock-free).

### ProductionStorage (97/2/1, Zipf, parallel)

| Implementation | ns/op | B/op | allocs/op |
|---|---|---|---|
| MutexStore | 141 | 0 | 0 |
| SyncMapStore | 17 | 1 | 0 |
| HashTrieStore | **15** | **0** | **0** |
| LRUStore (SIEVE) | 65 | 3 | 0 |
| StaticMap | 37 | 3 | 0 |

HashTrieStore wins the production workload by 2.5x over StaticMap. Lock-free reads dominate at 97% Get.

### StaticMap Shard/Lock Experiment

Tested four StaticMap configurations on the production 97/2/1 workload:

| Configuration | ProductionStorage | StressRead | DisjointKeys | ContendedKeys |
|---|---|---|---|---|
| **Mutex + 256** | **37 ns** | 32 ns | 24 ns | 75 ns |
| RWMutex + 256 | 57 ns | **22 ns** | 35 ns | 58 ns |
| Mutex + 2048 | **37 ns** | 31 ns | **12 ns** | 75 ns |
| RWMutex + 2048 | 56 ns | **22 ns** | 14 ns | **53 ns** |

**Findings**: RWMutex adds ~20 ns overhead per read in the uncontended case, outweighing its benefit. More shards helps only for disjoint keys (2048 shards: 12 ns vs 256: 24 ns). For the production Zipf workload, **Mutex + 256 is optimal** — per-shard contention is already <2% so the cheaper lock wins.

RWMutex + more shards wins only under extreme artificial contention (100% reads on 10 shared keys) — a pattern that doesn't occur in production because hot-key detection and client-side caching filter it before reaching the cache server.

### 4GB Scale (30M entries, 97/2/1, Zipf)

| Implementation | ns/op | B/op | allocs/op | GC cycles | GC pause | Heap |
|---|---|---|---|---|---|---|
| HashTrieStore | **55** | 11 | 1 | 0 | 0 ms | 6.1 GB |
| LRUStore (SIEVE) | 123 | 96 | 2 | 1 | 0.2 ms | 9.4 GB |

HashTrieStore wins by 2.2x at 4GB because GC pressure didn't materialize at 97% reads — write rate too low to trigger frequent collections. LRUStore's arena advantage matters more at higher write rates or under tight `GOMEMLIMIT` where GC runs continuously.

### Summary: When To Use Each

| Implementation | Best For | Why |
|---|---|---|
| **HashTrieStore** | Read-dominated parallel (97%+ reads) | Lock-free reads, zero-copy returns |
| **StaticMap** | Writes, mixed ops, bounded GC | DOD open addressing, zero alloc, 768 GC objects |
| **LRUStore** | Bounded memory, 32GB production | SIEVE eviction, arena allocation, ~768 GC objects |
| **SyncMapStore** | Benchmarking baseline | stdlib reference, no tuning |
| **MutexStore** | Simplicity, small datasets | Minimal code, single lock |

At 32GB scale with bounded memory, the choice is between **LRUStore** (eviction, GC-invisible, slower reads) and **HashTrieStore** (no eviction, 100M+ GC pointers, faster reads). The GC impact of 100M pointers at 32GB is the deciding factor — not per-operation throughput, which is dwarfed by HTTP latency (~1-5 μs per request vs 15-55 ns per cache op).

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

1. Yang, Yue, Rashmi. "A large scale analysis of hundreds of in-memory cache clusters at Twitter." OSDI 2020. USENIX. [PDF](https://www.usenix.org/system/files/osdi20-yang.pdf). [Traces](https://github.com/twitter/cache-trace).
2. Atikoglu et al. "Workload analysis of a large-scale key-value store." SIGMETRICS 2012. ACM.
3. Nishtala et al. "Scaling Memcache at Facebook." NSDI 2013. USENIX.
4. Zhang et al. "SIEVE is Simpler than LRU: an Efficient Turn-Key Eviction Algorithm for Web Caches." NSDI 2024. USENIX.
5. Yang et al. "FIFO queues are all you need for cache eviction." SOSP 2023. ACM.
6. Cao et al. "Characterizing, Modeling, and Benchmarking RocksDB Key-Value Workloads at Facebook." FAST 2020. USENIX.
7. Go standard library. `sync/map_bench_test.go`. github.com/golang/go.
8. Bendersky. "Common pitfalls in Go benchmarking." 2023. eli.thegreenplace.net.
