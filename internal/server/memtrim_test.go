package server

import (
	"context"
	"testing"
	"time"
)

func TestReadMemSample(t *testing.T) {
	t.Parallel()
	m, ok := readMemSample()
	if !ok {
		t.Fatal("runtime/metrics lacks a metric the memory trimmer needs")
	}
	if m.retained == 0 {
		t.Fatal("retained heap is 0")
	}
}

func TestMemTrimmerTick(t *testing.T) {
	t.Parallel()
	const mb = 1 << 20
	// tick is one trimmer tick: the runtime state it reads, whether it must
	// trim, and (when it trims) the state right after debug.FreeOSMemory.
	type tick struct {
		cycles, retained uint64
		wantTrim         bool
		after            memSample
	}
	tests := []struct {
		name  string
		ticks []tick
	}{
		{"idle with startup garbage trims once", []tick{
			{cycles: 2, retained: 56 * mb, wantTrim: true, after: memSample{gcCycles: 3, retained: 3 * mb}},
			{cycles: 3, retained: 3 * mb}, // the forced GC is not activity, nothing to trim
			{cycles: 3, retained: 4 * mb},
		}},
		{"busy server is left to the runtime", []tick{
			{cycles: 5, retained: 60 * mb},
			{cycles: 9, retained: 60 * mb},
			{cycles: 9, retained: 60 * mb, wantTrim: true, after: memSample{gcCycles: 10, retained: 5 * mb}}, // quiet again
		}},
		{"small heap is never trimmed", []tick{
			{cycles: 2, retained: 10 * mb},
			{cycles: 2, retained: 15 * mb},
		}},
		{"large live heap trims again only after growth", []tick{
			{cycles: 2, retained: 80 * mb, wantTrim: true, after: memSample{gcCycles: 3, retained: 40 * mb}},
			{cycles: 3, retained: 50 * mb}, // grew less than memTrimMinGrowth
			{cycles: 3, retained: 57 * mb, wantTrim: true, after: memSample{gcCycles: 4, retained: 41 * mb}},
		}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			var reads []memSample
			freed := 0
			tr := &memTrimmer{
				read: func() (memSample, bool) {
					if len(reads) == 0 {
						t.Fatal("unexpected metrics read")
					}
					m := reads[0]
					reads = reads[1:]
					return m, true
				},
				free:       func() { freed++ },
				lastCycles: 2,
			}
			for i, k := range tt.ticks {
				reads = []memSample{{gcCycles: k.cycles, retained: k.retained}}
				if k.wantTrim {
					reads = append(reads, k.after)
				}
				before := freed
				got := tr.tick()
				if got != k.wantTrim || (freed > before) != k.wantTrim {
					t.Fatalf("tick %d: trimmed=%v (free calls %d→%d), want %v", i, got, before, freed, k.wantTrim)
				}
				if len(reads) != 0 {
					t.Fatalf("tick %d: %d metric reads left unused", i, len(reads))
				}
			}
		})
	}
}

func TestMemTrimmerUnavailableMetrics(t *testing.T) {
	t.Parallel()
	tr := &memTrimmer{
		read: func() (memSample, bool) { return memSample{}, false },
		free: func() { t.Fatal("free called without metrics") },
	}
	if tr.tick() {
		t.Fatal("trimmed without metrics")
	}
}

func TestRunMemTrimmerStops(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { runMemTrimmer(ctx, time.Millisecond); close(done) }()
	time.Sleep(5 * time.Millisecond)
	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("runMemTrimmer did not stop")
	}
}

func TestSoftMemoryLimit(t *testing.T) {
	t.Parallel()
	tests := []struct {
		env    string
		want   int64
		wantOK bool
	}{
		{"", DefaultSoftMemoryLimit, true},
		{"512MiB", 0, false},
		{"off", 0, false},
	}
	for _, tc := range tests {
		got, ok := softMemoryLimit(func(k string) string {
			if k == "GOMEMLIMIT" {
				return tc.env
			}
			return ""
		})
		if got != tc.want || ok != tc.wantOK {
			t.Errorf("GOMEMLIMIT=%q: softMemoryLimit = %d, %v; want %d, %v", tc.env, got, ok, tc.want, tc.wantOK)
		}
	}
}
