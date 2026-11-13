package lua

import (
	"context"
	"runtime/metrics"
	"sync"
	"sync/atomic"
	"time"
)

// budget is a context.Context handed to gopher-lua via SetContext. The VM
// polls ctx.Done() once per executed instruction on the main thread, so Done()
// doubles as the instruction counter. A timer enforces the wall-clock limit
// (which also covers coroutine threads, whose child contexts derive from this
// one but do not call our Done() per instruction).
//
// Not enforced (documented limits): a single long-running Go builtin
// (a pathological string pattern match, huge string.format width) is not
// interruptible until it returns; the instruction counter only ticks in Lua
// code. Wall-clock expiry still marks the call failed afterwards.
type budget struct {
	n        atomic.Int64
	max      int64
	heapMax  int64
	heap0    uint64
	done     chan struct{}
	once     sync.Once
	errMu    sync.Mutex
	err      error
	timer    *time.Timer
	sample   []metrics.Sample
	deadline time.Time
}

func newBudget(o Options) *budget {
	b := &budget{max: o.MaxInstructions, heapMax: o.MaxHeapGrowth, done: make(chan struct{})}
	if o.Timeout > 0 {
		b.deadline = time.Now().Add(o.Timeout)
		b.timer = time.AfterFunc(o.Timeout, func() { b.trip(ErrTimeout) })
	}
	if o.MaxHeapGrowth > 0 {
		b.sample = []metrics.Sample{{Name: "/memory/classes/heap/objects:bytes"}}
		b.heap0 = b.heapBytes()
	}
	return b
}

func (b *budget) heapBytes() uint64 {
	metrics.Read(b.sample)
	if b.sample[0].Value.Kind() == metrics.KindUint64 {
		return b.sample[0].Value.Uint64()
	}
	return 0
}

func (b *budget) trip(err error) {
	b.errMu.Lock()
	if b.err == nil {
		b.err = err
	}
	b.errMu.Unlock()
	b.once.Do(func() { close(b.done) })
}

// tripped returns the budget error, if the call was aborted.
func (b *budget) tripped() error {
	b.errMu.Lock()
	defer b.errMu.Unlock()
	return b.err
}

// stop releases the timer and any goroutines watching Done().
func (b *budget) stop() {
	if b.timer != nil {
		b.timer.Stop()
	}
	b.once.Do(func() { close(b.done) })
}

// Deadline implements context.Context.
func (b *budget) Deadline() (time.Time, bool) { return b.deadline, !b.deadline.IsZero() }

// Done implements context.Context and is the instruction counter.
func (b *budget) Done() <-chan struct{} {
	n := b.n.Add(1)
	if b.max > 0 && n > b.max {
		b.trip(ErrBudget)
	} else if b.heapMax > 0 && n&0xFFFF == 0 {
		if h := b.heapBytes(); h > b.heap0 && int64(h-b.heap0) > b.heapMax {
			b.trip(ErrMemory)
		}
	}
	return b.done
}

// Err implements context.Context.
func (b *budget) Err() error {
	if err := b.tripped(); err != nil {
		return err
	}
	select {
	case <-b.done:
		return context.Canceled
	default:
		return nil
	}
}

// Value implements context.Context.
func (b *budget) Value(any) any { return nil }
