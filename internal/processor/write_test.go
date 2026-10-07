package processor

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"zoom-checker/internal/model"
	"zoom-checker/internal/zoom"
)

func TestYieldWriteLock(t *testing.T) {
	t.Parallel()
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	cases := []struct {
		name     string
		gate     writeGate
		idle     time.Duration // time since the previous transaction ended
		ctx      context.Context
		wantWait bool
		wantHeld time.Duration // gate.held afterwards (-1: unchanged)
		wantErr  error
	}{
		{name: "first transaction", gate: writeGate{enabled: true}, wantHeld: 0},
		{name: "under budget", gate: writeGate{enabled: true, held: writeBudget - time.Millisecond}, idle: time.Millisecond,
			wantHeld: writeBudget - time.Millisecond},
		{name: "budget used: pause", gate: writeGate{enabled: true, held: writeBudget}, wantWait: true, wantHeld: 0},
		{name: "budget used, already paused long enough", gate: writeGate{enabled: true, held: 10 * writeBudget},
			idle: yieldGap, wantHeld: 0},
		{name: "postgres never pauses", gate: writeGate{held: 10 * writeBudget}, wantHeld: 10 * writeBudget},
		{name: "canceled while pausing", gate: writeGate{enabled: true, held: writeBudget}, ctx: canceled,
			wantHeld: writeBudget, wantErr: context.Canceled},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			p := &Processor{gate: tc.gate}
			if tc.name != "first transaction" {
				p.gate.lastEnd = time.Now().Add(-tc.idle)
			}
			ctx := tc.ctx
			if ctx == nil {
				ctx = context.Background()
			}
			start := time.Now()
			err := p.yieldWriteLock(ctx)
			took := time.Since(start)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err %v, want %v", err, tc.wantErr)
			}
			if tc.wantWait && took < yieldGap-5*time.Millisecond {
				t.Fatalf("paused %v, want about %v", took, yieldGap)
			}
			if !tc.wantWait && took > 50*time.Millisecond {
				t.Fatalf("paused %v, want no pause", took)
			}
			if p.gate.held != tc.wantHeld {
				t.Fatalf("held %v, want %v", p.gate.held, tc.wantHeld)
			}
		})
	}
}

// TestIngestNotStarvedByProcessorLoad reproduces SQLite writer starvation: API
// rebuilds run back to back with no pause while the webhook endpoint keeps
// inserting raw events through the pool (autocommit INSERT, SQLite busy handler).
// Without the processor yielding the write lock an INSERT could poll into a held
// lock until busy_timeout (10 s) and fail with SQLITE_BUSY; Zoom allows 3 s.
func TestIngestNotStarvedByProcessorLoad(t *testing.T) {
	t.Parallel()
	c := newClass(t, hm(9, 0), hm(10, 30))
	lessonA := c.lesson
	lessonB := c.env.lesson(c.g.id, "11122233344", hm(9, 0), hm(10, 30))

	ctx, cancel := context.WithTimeout(c.ctx, 2*time.Minute)
	defer cancel()
	runCtx, stopRun := context.WithCancel(ctx)
	runDone := make(chan error, 1)
	go func() { runDone <- c.p.Run(runCtx) }()

	var stop atomic.Bool
	var wg sync.WaitGroup
	var calls atomic.Int64
	errs := make(chan error, 8)
	for w := range 4 {
		wg.Go(func() {
			for !stop.Load() {
				var err error
				switch w {
				case 0, 1:
					err = c.p.RebuildLesson(ctx, lessonA)
				case 2:
					err = c.p.RelinkLesson(ctx, lessonB)
				default:
					err = c.p.RebuildLesson(ctx, lessonB)
				}
				if err != nil {
					errs <- err
					return
				}
				calls.Add(1)
			}
		})
	}
	defer func() {
		stop.Store(true)
		wg.Wait()
		stopRun()
		<-runDone
	}()

	names := []string{"Иванов Иван", "Петров Пётр", "Сидорова Анна", "Александров Александр"}
	var ids []int64
	var latencies []time.Duration
	for i := range 200 {
		at := hm(9, 0).Add(time.Duration(i) * 10 * time.Second)
		n, round := i%len(names), i/len(names)
		var body []byte
		if round%2 == 0 {
			body = c.m.joined(dev(names[n], fmt.Sprintf("%d-%d", n, round)), at)
		} else {
			body = c.m.left(dev(names[n], fmt.Sprintf("%d-%d", n, round-1)), at)
		}
		c.clock.Set(at.Add(time.Second))
		ev, perr := zoom.Parse(body)
		raw := BuildRawEvent(body, ev, perr, c.clock.Now())
		start := time.Now()
		if _, err := c.st.InsertWebhookEvent(ctx, raw); err != nil {
			t.Fatalf("insert %d after %v: %v", i, time.Since(start), err)
		}
		latencies = append(latencies, time.Since(start))
		ids = append(ids, raw.ID)
		c.p.Notify() // what the webhook handler does
	}
	slices.Sort(latencies)
	worst := latencies[len(latencies)-1]
	t.Logf("ingest INSERT latency under load: p50=%v p99=%v max=%v; %d API calls meanwhile",
		latencies[len(latencies)/2], latencies[len(latencies)*99/100], worst, calls.Load())
	if worst > 3*time.Second {
		t.Fatalf("an ingest INSERT waited %v for the write lock (Zoom allows 3 s)", worst)
	}

	// The worker is not starved by the API calls either.
	deadline := time.Now().Add(60 * time.Second)
	for _, id := range ids {
		for c.event(id).ProcessingStatus == model.EventPending {
			if time.Now().After(deadline) {
				t.Fatalf("event %d still pending under API load", id)
			}
			select {
			case err := <-errs:
				t.Fatalf("API call failed: %v", err)
			case <-time.After(10 * time.Millisecond):
			}
		}
	}
	select {
	case err := <-errs:
		t.Fatalf("API call failed: %v", err)
	default:
	}
	if calls.Load() == 0 {
		t.Fatal("no API calls ran")
	}
}
