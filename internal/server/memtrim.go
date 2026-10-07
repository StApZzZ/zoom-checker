package server

import (
	"context"
	"os"
	"runtime/debug"
	"runtime/metrics"
	"time"
)

// Idle memory trimming keeps the resident size of a quiet server close to its
// live heap (docs/architecture.md §0: ~20–40 MB RSS).
//
// Every argon2id password hash allocates 19 MiB (the bootstrap admin at the
// first start, the login timing-equalization hash at every start, each local
// login or password change). An idle Go program collects garbage only every
// two minutes, and the runtime's scavenger keeps freed pages of recently dense
// huge-page chunks until one more cycle has passed, so without trimming a
// freshly started (or just logged-into) server stays at ~70 MB RSS for four
// minutes or more although it needs under 30 MB.
const (
	memTrimEvery = 15 * time.Second
	// memTrimMinGrowth is how far the heap must grow over what remained after
	// the previous trim before an idle server trims again; it keeps a server
	// with a genuinely large live heap from collecting on every tick.
	memTrimMinGrowth = 16 << 20
)

// memSample is what the trimmer needs from runtime/metrics.
type memSample struct {
	gcCycles uint64 // completed GC cycles
	retained uint64 // heap memory held from the OS (objects + unused + free, not released)
}

var memMetricNames = []string{
	"/gc/cycles/total:gc-cycles",
	"/memory/classes/heap/objects:bytes",
	"/memory/classes/heap/unused:bytes",
	"/memory/classes/heap/free:bytes",
}

// readMemSample reads the runtime metrics; ok is false when this Go runtime
// does not provide one of them (then the trimmer does nothing).
func readMemSample() (memSample, bool) {
	s := make([]metrics.Sample, len(memMetricNames))
	for i, n := range memMetricNames {
		s[i].Name = n
	}
	metrics.Read(s)
	var m memSample
	for i := range s {
		if s[i].Value.Kind() != metrics.KindUint64 {
			return memSample{}, false
		}
	}
	m.gcCycles = s[0].Value.Uint64()
	for _, x := range s[1:] {
		m.retained += x.Value.Uint64()
	}
	return m, true
}

// memTrimmer returns freed heap memory to the OS when the server is idle (no GC
// cycle since the previous tick: a busy server's own GC pacing handles memory
// fine) and the heap has grown by memTrimMinGrowth since the last trim.
type memTrimmer struct {
	read func() (memSample, bool)
	free func() // debug.FreeOSMemory: a full GC, then release every free page

	lastCycles uint64
	floor      uint64 // heap retained right after the previous trim
}

func newMemTrimmer() *memTrimmer {
	t := &memTrimmer{read: readMemSample, free: debug.FreeOSMemory}
	if m, ok := t.read(); ok {
		t.lastCycles = m.gcCycles
	}
	return t
}

// tick checks once and reports whether it trimmed.
func (t *memTrimmer) tick() bool {
	m, ok := t.read()
	if !ok {
		return false
	}
	idle := m.gcCycles == t.lastCycles
	t.lastCycles = m.gcCycles
	if !idle || m.retained < t.floor+memTrimMinGrowth {
		return false
	}
	t.free()
	if after, ok := t.read(); ok {
		// The forced collection is not activity; the next tick may trim again
		// only after real growth.
		t.lastCycles, t.floor = after.gcCycles, after.retained
	}
	return true
}

// runMemTrimmer ticks until ctx ends.
func runMemTrimmer(ctx context.Context, every time.Duration) {
	t := newMemTrimmer()
	tk := time.NewTicker(every)
	defer tk.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-tk.C:
			t.tick()
		}
	}
}

// DefaultSoftMemoryLimit is the Go runtime's soft memory limit when GOMEMLIMIT
// is not set. Password hashes (19 MiB each, at most 4 at once) and an XLSX
// import are short-lived garbage; with the default GOGC alone the heap may
// grow to twice the in-flight peak before a collection. The limit makes the
// collector reclaim them sooner on the small servers this service targets. It
// is soft: a larger live heap only costs more frequent collections, capped by
// the runtime at about half the CPU.
const DefaultSoftMemoryLimit = 192 << 20

// softMemoryLimit returns the limit to apply, ok=false when the operator chose
// one with GOMEMLIMIT (the runtime has applied it already).
func softMemoryLimit(getenv func(string) string) (limit int64, ok bool) {
	if v := getenv("GOMEMLIMIT"); v != "" {
		return 0, false
	}
	return DefaultSoftMemoryLimit, true
}

// ApplySoftMemoryLimit sets DefaultSoftMemoryLimit unless GOMEMLIMIT is set,
// and reports the limit in effect (bytes).
func ApplySoftMemoryLimit() int64 {
	if limit, ok := softMemoryLimit(os.Getenv); ok {
		debug.SetMemoryLimit(limit)
		return limit
	}
	return debug.SetMemoryLimit(-1) // -1 only reads the current limit
}
