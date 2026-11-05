package asyncworker

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"
)

// ErrQueueClosed is returned by Add and AddFunc after queue admission closes.
var ErrQueueClosed = errors.New("asyncworker: queue closed")

// Queue runs tasks as they are added and owns its task channel and result
// consumer. Build one with NewQueue; the zero value is not usable. A Queue is
// safe for concurrent submission and closing, but must not be copied.
//
// Call CloseAndWait to finish normally, or Stop to cancel early. Without an
// OnResult handler, results are discarded after updating the summary. No task
// values or result history are retained. The input and result buffers each
// hold at most the configured worker limit; slow consumers apply backpressure.
type Queue[T any] struct {
	ctx   context.Context
	stop  context.CancelCauseFunc
	tasks chan Task[T]

	mu          sync.Mutex
	closed      bool
	closing     chan struct{}
	inputClosed chan struct{}
	senders     sync.WaitGroup

	handlerMu sync.RWMutex
	handler   func(Result[T])
	done      chan struct{}
	report    StreamReport
}

// NewQueue starts a queue immediately, using the same options as New. Its
// pending-task capacity equals the final worker limit. No producer goroutine,
// channel management, or result reader is required from the caller.
func NewQueue[T any](ctx context.Context, opts ...Option) *Queue[T] {
	p := New[T](opts...)
	ctx, stop := context.WithCancelCause(ctx)
	q := &Queue[T]{
		ctx: ctx, stop: stop, tasks: make(chan Task[T], p.cfg.limit),
		closing: make(chan struct{}), inputClosed: make(chan struct{}), done: make(chan struct{}),
	}
	h := p.startStream(ctx, stop, q.tasks, false)
	go q.consume(h)
	return q
}

// Add submits one task. It blocks when the pending-task buffer is full, until
// space is available, the queue closes, or its context is canceled. A nil
// return means the task was accepted, not that it has completed. A submission
// overlapping Close or cancellation may either be accepted or return an error.
// Rejected tasks are not included in the summary or passed to OnResult.
//
// After Close, Add returns ErrQueueClosed. While cancellation is stopping an
// open queue, it can instead return the context's cancellation cause. Nil tasks
// are accepted and reported as ErrNilTask, just as with Pool.Add.
func (q *Queue[T]) Add(task Task[T]) error {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return ErrQueueClosed
	}
	if q.ctx.Err() != nil {
		q.mu.Unlock()
		return context.Cause(q.ctx)
	}
	// Close prevents future Add calls from registering before waiting for
	// in-flight senders. No sender can race the actual channel close.
	q.senders.Add(1)
	q.mu.Unlock()
	defer q.senders.Done()
	select {
	case q.tasks <- task:
		return nil
	case <-q.closing:
		return ErrQueueClosed
	case <-q.ctx.Done():
		return context.Cause(q.ctx)
	}
}

// AddFunc submits a function as a TaskFunc.
func (q *Queue[T]) AddFunc(fn func(context.Context) (T, error)) error {
	return q.Add(TaskFunc[T](fn))
}

// OnResult installs an optional handler and returns the queue for chaining.
// Set it before submitting tasks to observe every accepted task. Calls are
// serialized, including skipped tasks after cancellation. A nil handler
// discards results. Replacing a handler affects subsequent deliveries; an
// invocation already in progress finishes with its previous handler.
//
// Handlers must return and must not call blocking queue operations such as
// Add or CloseAndWait on this queue. Stop and Close are safe from a handler.
// A handler panic is not recovered, matching the WithProgress contract.
func (q *Queue[T]) OnResult(fn func(Result[T])) *Queue[T] {
	q.handlerMu.Lock()
	q.handler = fn
	q.handlerMu.Unlock()
	return q
}

// Close stops accepting tasks and lets accepted tasks finish. It returns
// without waiting for tasks or callbacks. It is safe to call more than once.
func (q *Queue[T]) Close() {
	q.mu.Lock()
	if q.closed {
		q.mu.Unlock()
		return
	}
	q.closed = true
	close(q.closing)
	q.mu.Unlock()
	go func() {
		q.senders.Wait()
		close(q.tasks)
		close(q.inputClosed)
	}()
}

// CloseAndWait closes admission and waits for every accepted task and result
// callback. It returns counters and the first task error without retaining
// results. Repeated and concurrent calls return the same summary.
func (q *Queue[T]) CloseAndWait() StreamReport {
	q.Close()
	<-q.done
	return q.report
}

// Stop cancels running tasks, skips queued tasks, and unblocks Add calls. It
// returns immediately; CloseAndWait waits for tasks and callbacks to return.
func (q *Queue[T]) Stop() {
	q.stop(ErrStopped)
	q.Close()
}

// Done closes after all accepted tasks have been accounted for and all result
// handlers have returned. Its summary can then be read with CloseAndWait.
func (q *Queue[T]) Done() <-chan struct{} { return q.done }

func (q *Queue[T]) deliver(result Result[T]) {
	q.handlerMu.RLock()
	fn := q.handler
	q.handlerMu.RUnlock()
	if fn != nil {
		fn(result)
	}
}

func (q *Queue[T]) consume(h *StreamHandle[T]) {
	defer close(q.done)
	for result := range h.Results() {
		q.deliver(result)
	}
	report := h.Wait()
	// A canceled stream stops reading input. Because the queue owns input,
	// it can close it and account for every successful Add still buffered.
	q.Close()
	<-q.inputClosed
	for range q.tasks {
		idx := report.Total
		report.Total++
		report.Skipped++
		q.deliver(Result[T]{Index: idx, Err: &TaskError{Index: idx, Err: fmt.Errorf("%w: %w", ErrNotRun, report.Cause)}})
	}
	// Include callback and cancellation-drain time in the queue lifetime.
	report.EndedAt = time.Now()
	q.report = report
}

// ErrQueue runs tasks that return only an error. Use NewErrQueue to create one.
type ErrQueue struct{ *Queue[Void] }

// NewErrQueue starts an error-only queue with no channel or result-reader setup.
func NewErrQueue(ctx context.Context, opts ...Option) *ErrQueue {
	return &ErrQueue{Queue: NewQueue[Void](ctx, opts...)}
}

// AddFunc submits an error-only function without requiring a Do adapter.
func (q *ErrQueue) AddFunc(fn func(context.Context) error) error {
	return q.Queue.Add(Do(fn))
}

// OnResult sets a result handler and preserves the error-only fluent API.
func (q *ErrQueue) OnResult(fn func(Result[Void])) *ErrQueue {
	q.Queue.OnResult(fn)
	return q
}
