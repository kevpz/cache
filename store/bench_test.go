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
// Zipf(s=1.01) distribution. Pre-computing avoids RNG overhead in hot loop.
func makeZipfIndices(n, keyCount int) []int {
	r := rand.New(rand.NewSource(42))
	z := rand.NewZipf(r, 1.01, 1, uint64(keyCount-1))
	indices := make([]int, n)
	for i := range indices {
		indices[i] = int(z.Uint64())
	}
	return indices
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
// Model real access skew using Zipf(s=1.01) key distribution and
// operation ratios from published production analyses (Twitter OSDI'20,
// Facebook SIGMETRICS).

const (
	prodKeyCount   = 100_000
	zipfIndexCount = 10_000_000
)

func benchProduction(b *testing.B, getPct, setPct, delPct int) {
	keys := makeKeys(prodKeyCount)
	indices := makeZipfIndices(zipfIndexCount, prodKeyCount)
	total := getPct + setPct + delPct

	for _, f := range factories {
		b.Run(f.name, func(b *testing.B) {
			s := f.new()
			populateFrom(s, keys)
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

// 97% Get, 2% Set, 1% Delete. Facebook storage cache workload.
func BenchmarkProductionStorage(b *testing.B) {
	benchProduction(b, 97, 2, 1)
}

// 80% Get, 15% Set, 5% Delete. General-purpose cache.
func BenchmarkProductionMixed(b *testing.B) {
	benchProduction(b, 80, 15, 5)
}

// 40% Get, 40% Set, 20% Delete. Twitter write-heavy cluster.
func BenchmarkProductionWriteHeavy(b *testing.B) {
	benchProduction(b, 40, 40, 20)
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
	indices := makeZipfIndices(zipfIndexCount, scale4GBKeyCount)

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
