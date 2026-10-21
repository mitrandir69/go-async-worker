package asyncworker_test

import (
	"context"
	"errors"
	"fmt"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

// The type parameter is the value a task produces, so results come back
// without a shared slice and without a mutex.
func Example() {
	pool := asyncworker.New[int](asyncworker.WithLimit(4))
	for i := 1; i <= 5; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i * i, nil })
	}

	report := pool.Run(context.Background())

	fmt.Println(report.Status, report.Succeeded, report.Values())
	// Output: done 5 [1 4 9 16 25]
}

// Every failed task keeps its error, so errors.Is still reaches a
// sentinel and the report says which task failed.
func Example_errors() {
	errNotFound := errors.New("not found")

	pool := asyncworker.New[string](asyncworker.WithLimit(2))
	pool.AddFunc(func(context.Context) (string, error) { return "ok", nil })
	pool.AddFunc(func(context.Context) (string, error) {
		return "", fmt.Errorf("load profile: %w", errNotFound)
	})

	report := pool.Run(context.Background())

	fmt.Println(report.Status)
	fmt.Println("is not found:", errors.Is(report.Err(), errNotFound))
	for _, res := range report.Failed() {
		fmt.Println(res.Err)
	}
	// Output:
	// done with errors
	// is not found: true
	// task 1: load profile: not found
}

// A panic in one task is recovered and reported as that task's error,
// and the rest of the run carries on.
func Example_panic() {
	pool := asyncworker.New[int](asyncworker.WithLimit(2))
	pool.AddFunc(func(context.Context) (int, error) { panic("bad input") })
	pool.AddFunc(func(context.Context) (int, error) { return 1, nil })

	report := pool.Run(context.Background())

	var panicErr *asyncworker.PanicError
	errors.As(report.Err(), &panicErr)

	fmt.Println(report.Status, report.Succeeded)
	fmt.Println(panicErr)
	// Output:
	// done with errors 1
	// panic: bad input
}

// Use an ErrPool for work that produces no value.
func ExampleNewErrPool() {
	names := []string{"alice", "bob", "carol"}

	pool := asyncworker.NewErrPool(asyncworker.WithLimit(3))
	for _, name := range names {
		pool.Add(asyncworker.Do(func(context.Context) error {
			if name == "" {
				return errors.New("empty name")
			}
			return nil
		}))
	}

	report := pool.Run(context.Background())

	fmt.Println(report.Status, report.Succeeded, "of", report.Total)
	// Output: done 3 of 3
}

// Start runs the pool in the background and hands back a handle to wait
// on or to stop.
func ExamplePool_Start() {
	pool := asyncworker.New[int](asyncworker.WithLimit(2))
	for i := 0; i < 3; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}

	handle := pool.Start(context.Background())

	// Other work can happen here.
	<-handle.Done()

	fmt.Println(handle.Wait().Status)
	// Output: done
}

// WithStopOnFirstError skips the tasks that have not started once one
// fails. With a single worker the order is deterministic.
func ExampleWithStopOnFirstError() {
	pool := asyncworker.New[int](
		asyncworker.WithLimit(1),
		asyncworker.WithStopOnFirstError(),
	)
	pool.AddFunc(func(context.Context) (int, error) { return 0, errors.New("boom") })
	pool.AddFunc(func(context.Context) (int, error) { return 2, nil })
	pool.AddFunc(func(context.Context) (int, error) { return 3, nil })

	report := pool.Run(context.Background())

	fmt.Println(report.Status, report.Succeeded)
	fmt.Println("second task skipped:", errors.Is(report.Results[1].Err, asyncworker.ErrNotRun))
	// Output:
	// done with errors 0
	// second task skipped: true
}

// WithProgress reports how far along a run is. The pool serializes the
// calls, so the callback needs no lock of its own.
func ExampleWithProgress() {
	pool := asyncworker.New[int](
		asyncworker.WithLimit(1),
		asyncworker.WithProgress(func(done, total int) {
			fmt.Printf("%d/%d\n", done, total)
		}),
	)
	for i := 0; i < 3; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}

	pool.Run(context.Background())
	// Output:
	// 1/3
	// 2/3
	// 3/3
}
