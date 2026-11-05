package asyncworker

import (
	"context"
	"errors"
	"fmt"
	"math/rand/v2"
	"sync"
	"time"
)

// StreamHandle tracks a run fed by a channel. Consume Results while the run
// is active, then call Wait for its summary. A slow consumer applies backpressure.
type StreamHandle[T any] struct {
	results chan Result[T]
	done    chan struct{}
	stop    context.CancelCauseFunc
	report  StreamReport
}

// Results returns results in completion order. Index records input receive
// order. The channel closes after every accepted task has finished or been
// skipped. Cancellation may discard results that cannot be delivered without
// blocking; StreamReport.Dropped counts them.
func (h *StreamHandle[T]) Results() <-chan Result[T] { return h.results }

// Done closes once the stream has stopped and its summary is ready.
func (h *StreamHandle[T]) Done() <-chan struct{} { return h.done }

// Stop cancels running tasks and stops reading input. It also unblocks result
// delivery. The caller owns the input channel; Stop does not close or drain it.
func (h *StreamHandle[T]) Stop() { h.stop(ErrStopped) }

// Wait waits for termination. Consume Results concurrently, or call Stop first:
// a full result buffer otherwise blocks workers. Wait is safe to call repeatedly
// and concurrently. The summary retains no task values or result history.
func (h *StreamHandle[T]) Wait() StreamReport {
	<-h.done
	return h.report
}

// StreamReport summarizes a completed stream without retaining its results.
// For Stream, Total counts tasks received from input, including tasks skipped
// while waiting for jitter or a rate token; tasks left in input are not counted.
// For Queue, Total counts every successful Add, including buffered tasks skipped
// on cancellation. Queue never drops result deliveries to its internal consumer.
type StreamReport struct {
	Label     string
	Status    Status
	Total     int
	Succeeded int
	Failed    int
	Skipped   int
	// Dropped counts results not delivered because the run was canceled.
	// Buffered results remain readable after cancellation.
	Dropped int
	// FirstError is the first task error observed in completion order.
	FirstError error
	// Cause is the cancellation cause, or nil if the stream finished without
	// cancellation. Individual task failures are available in FirstError.
	Cause     error
	StartedAt time.Time
	EndedAt   time.Time
}

// Duration returns the time spent running the stream.
func (r StreamReport) Duration() time.Duration { return r.EndedAt.Sub(r.StartedAt) }

// Err joins the first observed task error and the cancellation cause. Unlike
// Report.Err, it does not retain or aggregate every task error; use result
// delivery to inspect individual failures.
func (r StreamReport) Err() error { return errors.Join(r.FirstError, r.Cause) }

// Stream starts workers that execute tasks as they arrive. It uses the pool's
// options and shared rate limiter, but does not read or modify tasks added by
// Add. Closing input finishes the run after all accepted tasks return. A nil
// input waits until cancellation. The pool never closes or drains input.
//
// There are at most WithLimit workers and WithLimit buffered results, with no
// growing task or result history. Consume Results while producing tasks; both
// input reception and result delivery apply backpressure. Producers should
// select on ctx.Done or the handle's Done when sending, so an early stop does
// not strand them. Cancel or Stop if input or results will no longer be used.
//
// WithProgress is serialized per stream and receives (completed, -1), because
// the final total is unknown. Skipped tasks do not trigger progress. Task
// errors and panics use the same wrapping and recovery as Run.
func (p *Pool[T]) Stream(ctx context.Context, input <-chan Task[T]) *StreamHandle[T] {
	ctx, stop := context.WithCancelCause(ctx)
	return p.startStream(ctx, stop, input, true)
}

func (p *Pool[T]) startStream(ctx context.Context, stop context.CancelCauseFunc, input <-chan Task[T], dropOnCancel bool) *StreamHandle[T] {
	h := &StreamHandle[T]{results: make(chan Result[T], p.cfg.limit), done: make(chan struct{}), stop: stop}
	go func() {
		defer close(h.done)
		defer close(h.results)
		defer stop(nil)
		h.report = p.runStream(ctx, stop, input, h.results, dropOnCancel)
	}()
	return h
}

func (p *Pool[T]) waitTaskStart(ctx context.Context) bool {
	var delay time.Duration
	if p.cfg.jitter > 0 {
		delay = time.Duration(rand.Int64N(int64(p.cfg.jitter)))
	}
	return waitTaskDelay(ctx, delay) && p.limiter.wait(ctx)
}

func (p *Pool[T]) runStream(ctx context.Context, stop context.CancelCauseFunc, input <-chan Task[T], output chan<- Result[T], dropOnCancel bool) StreamReport {
	report := StreamReport{Label: p.cfg.label, StartedAt: time.Now()}
	type outcome struct {
		result  Result[T]
		skipped bool
	}
	completed := make(chan outcome)
	// Serialize input receives and index assignment, so indices follow channel
	// receive order even when a worker is preempted immediately after a receive.
	var receiveMu sync.Mutex
	next := 0
	var wg sync.WaitGroup
	wg.Add(p.cfg.limit)
	for i := 0; i < p.cfg.limit; i++ {
		go func() {
			defer wg.Done()
			for {
				receiveMu.Lock()
				if ctx.Err() != nil {
					receiveMu.Unlock()
					return
				}
				var task Task[T]
				var ok bool
				select {
				case <-ctx.Done():
					receiveMu.Unlock()
					return
				case task, ok = <-input:
				}
				if !ok {
					receiveMu.Unlock()
					return
				}
				idx := next
				next++
				receiveMu.Unlock()
				out := outcome{}
				if !p.waitTaskStart(ctx) {
					out.skipped = true
					out.result = Result[T]{Index: idx, Err: &TaskError{Index: idx, Err: fmt.Errorf("%w: %w", ErrNotRun, context.Cause(ctx))}}
				} else {
					out.result = p.runTask(ctx, idx, task)
					if out.result.Err != nil && p.cfg.stopOnErr {
						stop(ErrFailFast)
					}
				}
				// The collector keeps draining after cancellation, so every
				// accepted task is counted even if its result cannot be delivered.
				completed <- out
			}
		}()
	}
	go func() { wg.Wait(); close(completed) }()
	for out := range completed {
		report.Total++
		switch {
		case out.skipped:
			report.Skipped++
		case out.result.Err == nil:
			report.Succeeded++
		default:
			report.Failed++
			if report.FirstError == nil {
				report.FirstError = out.result.Err
			}
		}
		if !out.skipped && p.cfg.progress != nil {
			p.cfg.progress(report.Succeeded+report.Failed, -1)
		}
		if !dropOnCancel {
			// A managed queue always drains results internally, including on
			// cancellation, so every accepted task can reach its callback.
			output <- out.result
			continue
		}
		// Prefer delivery when buffer space exists, even after cancellation.
		select {
		case output <- out.result:
		default:
			select {
			case output <- out.result:
			case <-ctx.Done():
				report.Dropped++
			}
		}
	}
	report.Cause = context.Cause(ctx)
	switch {
	case errors.Is(report.Cause, ErrFailFast):
		report.Status = StatusFailed
	case report.Cause != nil:
		report.Status = StatusCanceled
	case report.Total == 0:
		report.Status = StatusEmpty
	case report.Failed > 0:
		report.Status = StatusFailed
	default:
		report.Status = StatusDone
	}
	report.EndedAt = time.Now()
	return report
}
