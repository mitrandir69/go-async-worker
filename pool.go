package asyncworker

import (
	"context"
	"errors"
	"fmt"
	"runtime/debug"
	"sync"
	"time"
)

// Pool runs tasks concurrently with a bounded number of goroutines.
//
// A Pool is safe for concurrent use: [Pool.Add] may be called from
// several goroutines, including while a run is in flight, in which case
// whether the new tasks join that run is undefined. Running a pool does
// not consume its tasks, so a second [Pool.Run] runs them all again; use
// [Pool.Reset] to drop them.
//
// The zero Pool is not usable. Build one with [New].
type Pool[T any] struct {
	cfg config

	mu    sync.Mutex
	tasks []Task[T]
}

// ErrPool is a pool of tasks that produce no value. Build its tasks with
// [Do].
type ErrPool = Pool[Void]

// New returns a pool of tasks producing values of type T.
func New[T any](opts ...Option) *Pool[T] {
	cfg := defaultConfig()
	for _, opt := range opts {
		opt(&cfg)
	}
	return &Pool[T]{cfg: cfg}
}

// NewErrPool returns a pool of tasks that only report an error.
func NewErrPool(opts ...Option) *ErrPool { return New[Void](opts...) }

// Add appends tasks to the pool. A nil task is kept rather than dropped,
// so that indices stay stable, and is reported with [ErrNilTask].
func (p *Pool[T]) Add(tasks ...Task[T]) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks = append(p.tasks, tasks...)
}

// AddFunc appends each fn to the pool as a [TaskFunc].
func (p *Pool[T]) AddFunc(fns ...func(ctx context.Context) (T, error)) {
	p.mu.Lock()
	defer p.mu.Unlock()
	for _, fn := range fns {
		p.tasks = append(p.tasks, TaskFunc[T](fn))
	}
}

// Len reports how many tasks the pool holds.
func (p *Pool[T]) Len() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.tasks)
}

// Reset drops every task the pool holds. It does not affect a run that is
// already in flight.
func (p *Pool[T]) Reset() {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.tasks = nil
}

// Run executes the pool's tasks and blocks until they have finished or
// the run has been cut short. It is [Pool.Start] followed by
// [Handle.Wait], and never returns before every task it started has
// returned.
func (p *Pool[T]) Run(ctx context.Context) Report[T] {
	return p.Start(ctx).Wait()
}

// Start executes the pool's tasks in the background and returns a handle
// to wait on. Which tasks run is fixed when Start is called.
//
// A run finishes and releases its goroutines on its own, so a handle that
// is never waited on leaks nothing. [Handle.Wait] is how the report is
// read, and [Handle.Stop] cuts the run short.
func (p *Pool[T]) Start(ctx context.Context) *Handle[T] {
	p.mu.Lock()
	tasks := make([]Task[T], len(p.tasks))
	copy(tasks, p.tasks)
	p.mu.Unlock()

	runCtx, stop := context.WithCancelCause(ctx)
	h := &Handle[T]{done: make(chan struct{}), stop: stop}

	go func() {
		// Deferred in this order so that report is written, then the
		// context is released, and only then is done closed.
		defer close(h.done)
		defer stop(nil)
		h.report = p.run(runCtx, stop, tasks)
	}()

	return h
}

// Handle tracks a run started by [Pool.Start].
type Handle[T any] struct {
	done chan struct{}
	stop context.CancelCauseFunc

	// report is written once before done is closed, and read only after
	// done is closed, which is what makes the write visible.
	report Report[T]
}

// Done returns a channel that is closed once the run has finished.
func (h *Handle[T]) Done() <-chan struct{} { return h.done }

// Wait blocks until the run has finished and returns its report. It may
// be called from several goroutines and more than once.
func (h *Handle[T]) Wait() Report[T] {
	<-h.done
	return h.report
}

// Stop asks the run to end early: tasks that have not started are
// skipped, and the context handed to running tasks is cancelled. It does
// not wait, so call [Handle.Wait] for the report. Calling it after the
// run has finished does nothing.
func (h *Handle[T]) Stop() { h.stop(ErrStopped) }

// run is the engine. ctx is already derived from the caller's context and
// stop cancels it.
func (p *Pool[T]) run(ctx context.Context, stop context.CancelCauseFunc, tasks []Task[T]) Report[T] {
	report := Report[T]{
		Label:     p.cfg.label,
		Total:     len(tasks),
		Results:   []Result[T]{},
		StartedAt: time.Now(),
	}
	if len(tasks) == 0 {
		report.Status = StatusEmpty
		report.EndedAt = time.Now()
		return report
	}

	// Never start more goroutines than there is work for them to do.
	workers := p.cfg.limit
	if workers > len(tasks) {
		workers = len(tasks)
	}

	// Each worker writes results[i] and ran[i] only for the indices it
	// takes off jobs, so no two of them touch the same element, and the
	// wg.Wait below is what makes those writes visible here.
	results := make([]Result[T], len(tasks))
	ran := make([]bool, len(tasks))
	jobs := make(chan int)

	var (
		progressMu sync.Mutex
		finished   int
	)

	var wg sync.WaitGroup
	wg.Add(workers)
	for i := 0; i < workers; i++ {
		go func() {
			defer wg.Done()
			for idx := range jobs {
				if ctx.Err() != nil {
					return
				}
				results[idx] = p.runTask(ctx, idx, tasks[idx])
				ran[idx] = true

				if results[idx].Err != nil && p.cfg.stopOnErr {
					stop(ErrFailFast)
				}
				if p.cfg.progress != nil {
					progressMu.Lock()
					finished++
					p.cfg.progress(finished, len(tasks))
					progressMu.Unlock()
				}
			}
		}()
	}

	// Hand out work until it runs out or the run is cut short. Closing
	// jobs is what lets idle workers exit.
	go func() {
		defer close(jobs)
		for idx := range tasks {
			select {
			case jobs <- idx:
			case <-ctx.Done():
				return
			}
		}
	}()

	wg.Wait()

	cause := context.Cause(ctx)
	allRan := true
	for idx := range results {
		if ran[idx] {
			if results[idx].Err == nil {
				report.Succeeded++
			}
			continue
		}
		allRan = false
		err := error(ErrNotRun)
		if cause != nil {
			err = fmt.Errorf("%w: %w", ErrNotRun, cause)
		}
		results[idx] = Result[T]{Index: idx, Err: &TaskError{Index: idx, Err: err}}
	}

	switch {
	case allRan && report.Succeeded == report.Total:
		report.Status = StatusDone
	case !allRan && !errors.Is(cause, ErrFailFast):
		report.Status = StatusCanceled
	default:
		report.Status = StatusFailed
	}

	report.Results = results
	report.EndedAt = time.Now()
	return report
}

// runTask runs one task and turns whatever it does, a panic included,
// into a Result.
func (p *Pool[T]) runTask(ctx context.Context, idx int, task Task[T]) (res Result[T]) {
	res.Index = idx
	if task == nil {
		res.Err = &TaskError{Index: idx, Err: ErrNilTask}
		return res
	}

	if p.cfg.recoverPanics {
		defer func() {
			if v := recover(); v != nil {
				res = Result[T]{
					Index: idx,
					Err:   &TaskError{Index: idx, Err: &PanicError{Value: v, Stack: debug.Stack()}},
				}
			}
		}()
	}

	value, err := task.Run(ctx)
	if err != nil {
		res.Err = &TaskError{Index: idx, Err: err}
		return res
	}
	res.Value = value
	return res
}
