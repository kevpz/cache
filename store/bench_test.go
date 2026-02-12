package store

import (
	"fmt"
	"math/rand"
	"runtime"
	"strconv"
	"sync/atomic"
	"testing"
)

// sink prevents compiler dead code elimination on serial benchmarks.
var sink string

// --- Infrastructure ---

func makeKeys(n int) []string {
	ks := make([]string, n)
	for i := range ks {
		ks[i] = "key" + strconv.Itoa(i)
	}
	return ks
}

func populateFrom(s Store, keys []string) {
	for _, k := range keys {
		s.Set(k, "value")
	}
}

// makeZipfIndices pre-generates n indices in [0, keyCount) following
// Zipf(s=skew) distribution. Pre-computing avoids RNG overhead in hot loop.
func makeZipfIndices(n, keyCount int, skew float64) []int {
	r := rand.New(rand.NewSource(42))
	z := rand.NewZipf(r, skew, 1, uint64(keyCount-1))
	indices := make([]int, n)
	for i := range indices {
		indices[i] = int(z.Uint64())
	}
	return indices
}

// makeProdKeys generates n keys with namespace prefixes (~25 bytes each),
// matching production key sizes (Twitter OSDI'20: 20-100 bytes with "ns1:ns2:obj").
func makeProdKeys(n int) []string {
	ks := make([]string, n)
	for i := range ks {
		ks[i] = fmt.Sprintf("ns:cache:obj:%08d", i)
	}
	return ks
}

// prodVal is a 200-byte value matching production median object size
// (Twitter OSDI'20: median ~230 bytes).
var prodVal = string(make([]byte, 200))

func populateWithVal(s Store, keys []string, val string) {
	for _, k := range keys {
		s.Set(k, val)
	}
}

var factories = []struct {
	name string
	new  func() Store
}{
	{"MutexStore", func() Store { return NewMutexStore() }},
	{"SyncMapStore", func() Store { return NewSyncMapStore() }},
	{"HashTrieStore", func() Store { return NewHashTrieStore() }},
	{"LRUStore", func() Store { return NewLRUStore(256 * 1 << 20) }},    // 256MB, enough headroom for benchmarks
	{"StaticMap", func() Store { return NewStaticMap(6_000_000, 256<<20) }}, // 6M entries, 256MB data
}

const defaultKeyCount = 1000

// --- A. Serial Baselines ---
// Establish per-operation cost with zero contention.

func BenchmarkGetHit(b *testing.B) {
	keys := makeKeys(defaultKeyCount)
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			populateFrom(s, keys)
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sink, _ = s.Get(keys[i%defaultKeyCount])
			}
		})
	}
}

func BenchmarkGetMiss(b *testing.B) {
	keys := makeKeys(defaultKeyCount)
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				sink, _ = s.Get(keys[i%defaultKeyCount])
			}
		})
	}
}

func BenchmarkSet(b *testing.B) {
	keys := makeKeys(defaultKeyCount)
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.Set(keys[i%defaultKeyCount], "value")
			}
		})
	}
}

// DeleteHit interleaves Set+Delete so every delete targets an existing key.
// The Set cost is overhead, but it is consistent across both implementations.
func BenchmarkDeleteHit(b *testing.B) {
	keys := makeKeys(defaultKeyCount)
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				k := keys[i%defaultKeyCount]
				s.Set(k, "value")
				s.Delete(k)
			}
		})
	}
}

func BenchmarkDeleteMiss(b *testing.B) {
	keys := makeKeys(defaultKeyCount)
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			b.ReportAllocs()
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				s.Delete(keys[i%defaultKeyCount])
			}
		})
	}
}

// --- B. Production Workloads ---
// Model real access skew using Zipf key distribution and operation ratios
// from published production analyses (Twitter OSDI'20, Facebook SIGMETRICS).
//
// Uses production-sized keys (~25 bytes with namespace prefix) and
// values (200 bytes, matching Twitter OSDI'20 median of ~230 bytes).
// This exposes the true cost of slab-copy reads (LRUStore, StaticMap)
// vs zero-copy pointer returns (HashTrieStore, SyncMapStore).

const (
	prodKeyCount   = 100_000
	zipfIndexCount = 10_000_000
	defaultSkew    = 1.01 // conservative; production often 1.0-1.5+
	highSkew       = 1.5  // "very high skew" observed in production (Twitter OSDI'20)
)

func benchProduction(b *testing.B, getPct, setPct, delPct int, skew float64) {
	keys := makeProdKeys(prodKeyCount)
	indices := makeZipfIndices(zipfIndexCount, prodKeyCount, skew)
	total := getPct + setPct + delPct

	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			populateWithVal(s, keys, prodVal)
			// Prime to steady state.
			for i := 0; i < prodKeyCount*2; i++ {
				s.Get(keys[i%prodKeyCount])
			}
			b.ReportAllocs()
			b.ResetTimer()
			var counter atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				id := int(counter.Add(1) - 1)
				offset := id * (zipfIndexCount / 64)
				i := 0
				for pb.Next() {
					idx := indices[(offset+i)%zipfIndexCount]
					k := keys[idx]
					op := i % total
					if op < getPct {
						s.Get(k)
					} else if op < getPct+setPct {
						s.Set(k, prodVal)
					} else {
						s.Delete(k)
					}
					i++
				}
			})
		})
	}
}

// 97% Get, 2% Set, 1% Delete. Facebook storage cache workload.
// Zipf s=1.01 (conservative skew).
func BenchmarkProductionStorage(b *testing.B) {
	benchProduction(b, 97, 2, 1, defaultSkew)
}

// 80% Get, 15% Set, 5% Delete. General-purpose cache.
func BenchmarkProductionMixed(b *testing.B) {
	benchProduction(b, 80, 15, 5, defaultSkew)
}

// 40% Get, 40% Set, 20% Delete. Twitter write-heavy cluster.
func BenchmarkProductionWriteHeavy(b *testing.B) {
	benchProduction(b, 40, 40, 20, defaultSkew)
}

// High-skew variants (Zipf s=1.5). Twitter OSDI'20 reports many clusters
// are "far more skewed than previously shown." Higher skew concentrates
// traffic on fewer keys, increasing shard contention for mutex-based stores
// and widening HashTrieStore's lock-free read advantage.

func BenchmarkProductionStorage_HighSkew(b *testing.B) {
	benchProduction(b, 97, 2, 1, highSkew)
}

func BenchmarkProductionMixed_HighSkew(b *testing.B) {
	benchProduction(b, 80, 15, 5, highSkew)
}

// --- C. Stress Tests ---
// 2M keys, 100 hot keys, 50% of operations target the hot set.
// Each hot key receives ~10,000x more traffic than an average cold key.

const (
	stressKeyCount = 2_000_000
	stressHotSize  = 100
)

func benchStress(b *testing.B, getN, setN, delN int) {
	allKeys := makeKeys(stressKeyCount)
	hotKeys := allKeys[:stressHotSize]
	coldKeys := allKeys[stressHotSize:]
	coldSize := len(coldKeys)
	total := getN + setN + delN

	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			populateFrom(s, allKeys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					// 50% hot, 50% cold.
					var k string
					if i%2 == 0 {
						k = hotKeys[(i/2)%stressHotSize]
					} else {
						k = coldKeys[(i/2)%coldSize]
					}
					op := i % total
					if op < getN {
						s.Get(k)
					} else if op < getN+setN {
						s.Set(k, "value")
					} else {
						s.Delete(k)
					}
					i++
				}
			})
		})
	}
}

// 100% Get with 50% hot contention over 2M keys.
func BenchmarkStressRead(b *testing.B) {
	benchStress(b, 1, 0, 0)
}

// 100% Set with 50% hot contention over 2M keys.
func BenchmarkStressWrite(b *testing.B) {
	benchStress(b, 0, 1, 0)
}

// 33% Get, 33% Set, 33% Delete with 50% hot contention over 2M keys.
func BenchmarkStressMixed(b *testing.B) {
	benchStress(b, 1, 1, 1)
}

// 25% Get, 50% Set, 25% Delete with 50% hot contention. Maximum write pressure.
func BenchmarkStressAllOps(b *testing.B) {
	benchStress(b, 1, 2, 1)
}

// --- D. Scaling ---
// Isolate the effect of key space size on parallel throughput.
// Uniform key access (no Zipf) to avoid confounding with access skew.

var keySizes = []int{1_000, 100_000, 5_000_000}

func BenchmarkScaleRead(b *testing.B) {
	for _, size := range keySizes {
		keys := makeKeys(size)
		for _, f := range factories {
			b.Run(fmt.Sprintf("keys=%d/%s", size, f.name), func(b *testing.B) {
				s := f.new()
				populateFrom(s, keys)
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						s.Get(keys[i%size])
						i++
					}
				})
			})
		}
	}
}

func BenchmarkScaleWrite(b *testing.B) {
	for _, size := range keySizes {
		keys := makeKeys(size)
		for _, f := range factories {
			b.Run(fmt.Sprintf("keys=%d/%s", size, f.name), func(b *testing.B) {
				s := f.new()
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						s.Set(keys[i%size], "value")
						i++
					}
				})
			})
		}
	}
}

func BenchmarkScaleMixed(b *testing.B) {
	for _, size := range keySizes {
		keys := makeKeys(size)
		for _, f := range factories {
			b.Run(fmt.Sprintf("keys=%d/%s", size, f.name), func(b *testing.B) {
				s := f.new()
				populateFrom(s, keys)
				b.ReportAllocs()
				b.ResetTimer()
				b.RunParallel(func(pb *testing.PB) {
					i := 0
					for pb.Next() {
						k := keys[i%size]
						// 80% Get, 15% Set, 5% Delete.
						switch i % 20 {
						case 16, 17, 18:
							s.Set(k, "value")
						case 19:
							s.Delete(k)
						default:
							s.Get(k)
						}
						i++
					}
				})
			})
		}
	}
}

// --- E. Key Distribution Patterns ---

// Each goroutine uses its own key space (disjoint access).
// sync.Map is optimized for this case.
func BenchmarkParallelDisjointKeys(b *testing.B) {
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			var counter atomic.Int64
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				id := counter.Add(1)
				prefix := "g" + strconv.FormatInt(id, 10) + "_"
				localKeys := make([]string, 100)
				for j := range localKeys {
					localKeys[j] = prefix + strconv.Itoa(j)
				}
				i := 0
				for pb.Next() {
					k := localKeys[i%len(localKeys)]
					switch i % 3 {
					case 0:
						s.Set(k, "value")
					case 1:
						s.Get(k)
					case 2:
						s.Delete(k)
					}
					i++
				}
			})
		})
	}
}

// All goroutines hit same 10 keys (high contention).
func BenchmarkParallelContendedKeys(b *testing.B) {
	hotKeys := makeKeys(10)
	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			populateFrom(s, hotKeys)
			b.ReportAllocs()
			b.ResetTimer()
			b.RunParallel(func(pb *testing.PB) {
				i := 0
				for pb.Next() {
					k := hotKeys[i%len(hotKeys)]
					switch i % 3 {
					case 0:
						s.Set(k, "value")
					case 1:
						s.Get(k)
					case 2:
						s.Delete(k)
					}
					i++
				}
			})
		})
	}
}

// --- F. 4GB Scale Benchmark ---
// Pre-populates ~30M entries (~4GB), then runs 97/2/1 Zipf workload.
// Reports throughput, allocations, and GC pause statistics.
// Only tests HashTrieStore and LRUStore -- the relevant production candidates.

const (
	scale4GBKeyCount = 30_000_000
	scale4GBValSize  = 100
)

func scaleKey(i int) string {
	return "k" + strconv.Itoa(i)
}

func BenchmarkScale4GB_Production(b *testing.B) {
	val := string(make([]byte, scale4GBValSize))
	indices := makeZipfIndices(zipfIndexCount, scale4GBKeyCount, defaultSkew)

	impls := []struct {
		name string
		new  func() Store
	}{
		{"HashTrieStore", func() Store { return NewHashTrieStore() }},
		{"LRUStore", func() Store { return NewLRUStore(4 << 30) }}, // 4GB
	}

	for _, impl := range impls {
		b.Run(impl.name, func(b *testing.B) {
			s := impl.new()

			// Populate ~30M entries.
			b.Logf("populating %d entries...", scale4GBKeyCount)
			for i := 0; i < scale4GBKeyCount; i++ {
				s.Set(scaleKey(i), val)
			}
			b.Logf("population complete")

			// Force GC to reach steady state before measurement.
			runtime.GC()

			var memBefore runtime.MemStats
			runtime.ReadMemStats(&memBefore)

			b.ReportAllocs()
			b.ResetTimer()

			// 97/2/1 Zipf workload.
			var counter atomic.Int64
			b.RunParallel(func(pb *testing.PB) {
				id := int(counter.Add(1) - 1)
				offset := id * (zipfIndexCount / 64)
				i := 0
				for pb.Next() {
					idx := indices[(offset+i)%zipfIndexCount]
					k := scaleKey(idx)
					op := i % 100
					if op < 97 {
						s.Get(k)
					} else if op < 99 {
						s.Set(k, val)
					} else {
						s.Delete(k)
					}
					i++
				}
			})

			b.StopTimer()

			var memAfter runtime.MemStats
			runtime.ReadMemStats(&memAfter)
			gcCycles := memAfter.NumGC - memBefore.NumGC
			gcTimeNs := memAfter.PauseTotalNs - memBefore.PauseTotalNs
			b.ReportMetric(float64(gcCycles), "gc-cycles")
			b.ReportMetric(float64(gcTimeNs)/1e6, "gc-ms")
			b.ReportMetric(float64(memAfter.HeapAlloc)/(1<<20), "heap-MB")
		})
	}
}
