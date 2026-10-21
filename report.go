package asyncworker

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"
)

// Status describes how a run finished.
type Status string

const (
	// StatusDone means every task ran and none of them failed.
	StatusDone Status = "done"
	// StatusFailed means at least one task failed, or was skipped because
	// another one failed.
	StatusFailed Status = "done with errors"
	// StatusCanceled means the run was cut short by its context or by
	// [Handle.Stop] before every task had run.
	StatusCanceled Status = "canceled"
	// StatusEmpty means the pool held no tasks.
	StatusEmpty Status = "empty"
)

// Sentinel errors reported by a run. Compare with [errors.Is].
var (
	// ErrNotRun marks a task that never started because the run ended
	// early. It wraps the reason the run ended.
	ErrNotRun = errors.New("asyncworker: task did not run")
	// ErrNilTask marks a nil task that was added to a pool.
	ErrNilTask = errors.New("asyncworker: nil task")
	// ErrStopped is the reason a run ended when [Handle.Stop] was called.
	ErrStopped = errors.New("asyncworker: run stopped")
	// ErrFailFast is the reason a run ended under [WithStopOnFirstError].
	ErrFailFast = errors.New("asyncworker: stopped after first error")
)

// TaskError is the error reported for a failed task. It records which
// task failed and wraps the error that task returned, so [errors.Is] and
// [errors.As] keep working through it.
type TaskError struct {
	// Index is the task's position in the order it was added to the pool.
	Index int
	// Err is what the task returned, or a [*PanicError] if it panicked.
	Err error
}

// Error implements the error interface.
func (e *TaskError) Error() string { return fmt.Sprintf("task %d: %s", e.Index, e.Err) }

// Unwrap returns the underlying error.
func (e *TaskError) Unwrap() error { return e.Err }

// PanicError is the error reported for a task that panicked.
type PanicError struct {
	// Value is the value that was passed to panic.
	Value any
	// Stack is the stack trace captured where the panic was recovered.
	Stack []byte
}

// Error implements the error interface.
func (e *PanicError) Error() string { return fmt.Sprintf("panic: %v", e.Value) }

// Result is the outcome of one task.
type Result[T any] struct {
	// Index is the task's position in the order it was added to the pool.
	Index int
	// Value is what the task returned. It is the zero value of T when Err
	// is not nil.
	Value T
	// Err is nil when the task succeeded, and a [*TaskError] otherwise.
	Err error
}

// MarshalJSON renders Err as a string, which a plain error value does
// not survive.
func (r Result[T]) MarshalJSON() ([]byte, error) {
	out := struct {
		Index int    `json:"index"`
		Value T      `json:"value"`
		Error string `json:"error,omitempty"`
	}{Index: r.Index, Value: r.Value}
	if r.Err != nil {
		out.Error = r.Err.Error()
	}
	return json.Marshal(out)
}

// Report describes a finished run.
type Report[T any] struct {
	// Status is how the run finished.
	Status Status `json:"status"`
	// Label is the pool's label, set with [WithLabel].
	Label string `json:"label,omitempty"`
	// Total is how many tasks the run started with.
	Total int `json:"total"`
	// Succeeded is how many tasks returned no error.
	Succeeded int `json:"succeeded"`
	// Results holds one entry per task, in the order the tasks were added.
	Results []Result[T] `json:"results"`
	// StartedAt is when the run began.
	StartedAt time.Time `json:"startedAt"`
	// EndedAt is when the run finished.
	EndedAt time.Time `json:"endedAt"`
}

// Duration is how long the run took.
func (r Report[T]) Duration() time.Duration { return r.EndedAt.Sub(r.StartedAt) }

// Values returns the values of the tasks that succeeded, in the order the
// tasks were added.
func (r Report[T]) Values() []T {
	values := make([]T, 0, r.Succeeded)
	for _, res := range r.Results {
		if res.Err == nil {
			values = append(values, res.Value)
		}
	}
	return values
}

// Failed returns the results of the tasks that did not succeed, in the
// order the tasks were added.
func (r Report[T]) Failed() []Result[T] {
	failed := make([]Result[T], 0, len(r.Results)-r.Succeeded)
	for _, res := range r.Results {
		if res.Err != nil {
			failed = append(failed, res)
		}
	}
	return failed
}

// Err joins the errors of every failed task, and is nil when the run had
// none. Every joined error is a [*TaskError].
func (r Report[T]) Err() error {
	errs := make([]error, 0, len(r.Results))
	for _, res := range r.Results {
		if res.Err != nil {
			errs = append(errs, res.Err)
		}
	}
	return errors.Join(errs...)
}
