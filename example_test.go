package asyncworker_test

import (
	"context"
	"errors"
	"fmt"
	"time"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

func ExampleNewErrQueue() {
	queue := asyncworker.NewErrQueue(context.Background(), asyncworker.WithLimit(4))
	defer queue.Stop()
	for i := 0; i < 5; i++ {
		if err := queue.AddFunc(func(context.Context) error { return nil }); err != nil {
			fmt.Println(err)
			return
		}
	}
	report := queue.CloseAndWait()
	fmt.Println(report.Status, report.Succeeded, report.Err())
	// Output: done 5 <nil>
}

func ExampleNewQueue() {
	sum := 0
	queue := asyncworker.NewQueue[int](context.Background(), asyncworker.WithLimit(4)).
		OnResult(func(result asyncworker.Result[int]) {
			if result.Err == nil {
				sum += result.Value
			}
		})
	defer queue.Stop()
	for i := 1; i <= 5; i++ {
		if err := queue.AddFunc(func(context.Context) (int, error) { return i * i, nil }); err != nil {
			fmt.Println(err)
			return
		}
	}
	report := queue.CloseAndWait()
	fmt.Println(report.Status, report.Succeeded, sum)
	// Output: done 5 55
}

func ExampleWithJitter() {
	pool := asyncworker.New[int](
		asyncworker.WithLimit(4),
		asyncworker.WithJitter(10*time.Millisecond),
	)
	for i := 0; i < 3; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}
	report := pool.Run(context.Background())
	fmt.Println(report.Status, report.Values())
	// Output: done [0 1 2]
}

func ExampleWithRateLimit() {
	pool := asyncworker.New[int](
		asyncworker.WithLimit(4),
		asyncworker.WithRateLimit(100),
	)
	for i := 0; i < 3; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}
	report := pool.Run(context.Background())
	fmt.Println(report.Status, report.Values())
	// Output: done [0 1 2]
}

func ExamplePool_Stream() {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	pool := asyncworker.New[int](asyncworker.WithLimit(2))
	input := make(chan asyncworker.Task[int])
	handle := pool.Stream(ctx, input)
	defer handle.Stop()
	go func() {
		defer close(input)
		for i := 0; i < 5; i++ {
			task := asyncworker.TaskFunc[int](func(context.Context) (int, error) { return i * i, nil })
			select {
			case input <- task:
			case <-ctx.Done():
				return
			case <-handle.Done():
				return
			}
		}
	}()
	sum := 0
	for result := range handle.Results() {
		if result.Err == nil {
			sum += result.Value
		}
	}
	summary := handle.Wait()
	fmt.Println(summary.Status, summary.Succeeded, sum)
	// Output: done 5 30
}

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

// Running a pool does not consume its tasks. Reset drops them before the next
// batch, without changing an already running batch.
func ExamplePool_Reset() {
	pool := asyncworker.New[int](asyncworker.WithLimit(2))
	pool.AddFunc(func(context.Context) (int, error) { return 10, nil })
	fmt.Println(pool.Run(context.Background()).Values())
	fmt.Println(pool.Run(context.Background()).Values())
	pool.Reset()
	fmt.Println("tasks after reset:", pool.Len())
	pool.AddFunc(func(context.Context) (int, error) { return 20, nil })
	fmt.Println(pool.Run(context.Background()).Values())
	// Output:
	// [10]
	// [10]
	// tasks after reset: 0
	// [20]
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
