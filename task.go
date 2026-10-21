package asyncworker

import "context"

// Task is a single unit of work that produces a value of type T.
//
// Run should respect ctx and return early once it is done. Cancelling a
// pool stops it from starting further tasks, but a task that ignores ctx
// runs to completion regardless.
//
// Run may be called from any goroutine. A Task that is added to a pool
// more than once, or to several pools, must be safe for concurrent use.
type Task[T any] interface {
	Run(ctx context.Context) (T, error)
}

// TaskFunc adapts an ordinary function to [Task].
type TaskFunc[T any] func(ctx context.Context) (T, error)

// Run implements [Task].
func (f TaskFunc[T]) Run(ctx context.Context) (T, error) { return f(ctx) }

// Void is the value type of tasks that produce no result. It is the type
// parameter of [ErrPool].
type Void = struct{}

// Do adapts a function that only reports an error to a task of an
// [ErrPool].
func Do(f func(ctx context.Context) error) TaskFunc[Void] {
	return func(ctx context.Context) (Void, error) {
		return Void{}, f(ctx)
	}
}
