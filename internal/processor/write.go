package processor

import (
	"context"
	"time"

	"zoom-checker/internal/db"
	"zoom-checker/internal/store"
)

// SQLite writer fairness.
//
// SQLite has a single write lock and no queue for it: a connection that finds it
// taken sleeps in the busy handler and polls again after 1, 2, 5 … ms, then every
// 100 ms, until busy_timeout (10 s) and then fails with SQLITE_BUSY. Processor
// transactions instead queue on p.mu, and when several are waiting (worker batches
// plus API rebuilds) each BEGIN follows the previous COMMIT within microseconds. A
// writer outside the processor — the webhook endpoint's INSERT of the raw event,
// handlers' own statements — then keeps polling into a held lock and can be
// starved until busy_timeout. Zoom gives the webhook endpoint 3 s.
//
// So after the processor has held the write lock for writeBudget without a real
// pause, it leaves the lock free for yieldGap, longer than the busy handler's
// longest sleep, before its next transaction: every writer waiting in the busy
// handler polls at least once inside that gap and gets its turn. Under sustained
// load this bounds an outside writer's wait to roughly writeBudget/duty + yieldGap
// (well under a second); light traffic never yields. PostgreSQL queues lock
// waiters itself and needs none of this.
const (
	writeBudget = 250 * time.Millisecond
	yieldGap    = 120 * time.Millisecond // > SQLite's longest busy-handler sleep (100 ms)
)

// writeGate tracks how long the processor has been using the SQLite write lock
// without a pause. Guarded by Processor.mu.
type writeGate struct {
	enabled bool
	held    time.Duration // write-lock time since the last pause of at least yieldGap
	lastEnd time.Time     // when the previous processor transaction finished
}

func newWriteGate(st *store.Store) writeGate {
	return writeGate{enabled: st.Dialect() == db.SQLite}
}

// writeTx runs fn in one transaction, queued behind other processor transactions
// (lock order: p.mu -> transaction; p.mu is never taken inside a transaction).
// Only the tx Store passed to fn may be used inside it: a query on the pool would
// wait for the write lock this transaction holds.
func (p *Processor) writeTx(ctx context.Context, fn func(tx *store.Store) error) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if err := p.yieldWriteLock(ctx); err != nil {
		return err
	}
	start := time.Now()
	defer func() {
		p.gate.lastEnd = time.Now()
		p.gate.held += p.gate.lastEnd.Sub(start)
	}()
	return p.st.InTx(ctx, fn)
}

// yieldWriteLock pauses before the next transaction when the processor has used
// the write lock for writeBudget since the last pause (see writeGate). The caller
// holds p.mu, so no other processor transaction starts meanwhile.
func (p *Processor) yieldWriteLock(ctx context.Context) error {
	g := &p.gate
	if !g.enabled || g.lastEnd.IsZero() {
		return nil
	}
	idle := time.Since(g.lastEnd)
	if idle >= yieldGap {
		g.held = 0 // the lock was free long enough for every waiting writer
		return nil
	}
	if g.held < writeBudget {
		return nil
	}
	t := time.NewTimer(yieldGap - idle)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
	}
	g.held = 0
	return nil
}
