// Package asyncworker runs a set of tasks concurrently with a bounded
// number of goroutines and reports what happened to each of them.
//
// A [Pool] is built, filled with tasks and run:
//
//	pool := asyncworker.New[int](asyncworker.WithLimit(8))
//	for i := 0; i < 100; i++ {
//		i := i
//		pool.AddFunc(func(ctx context.Context) (int, error) {
//			return i * i, nil
//		})
//	}
//	report := pool.Run(ctx)
//	fmt.Println(report.Status, report.Succeeded, report.Values())
//
// The type parameter is the value a task produces, so collecting results
// needs no shared slice and no mutex. For work that only reports an error,
// use an [ErrPool] and wrap the functions with [Do]:
//
//	pool := asyncworker.NewErrPool(asyncworker.WithLimit(4))
//	pool.Add(asyncworker.Do(func(ctx context.Context) error {
//		return send(ctx)
//	}))
//
// Three properties are worth knowing before relying on the package.
//
// Cancellation is cooperative. The context passed to [Pool.Run] is handed
// to every task, and when it is done the pool stops handing out work.
// Tasks already in flight keep running until they return, so a task that
// ignores its context cannot be interrupted.
//
// A panic in one task does not take the process down. It is recovered,
// turned into a [PanicError] and reported as that task's error. Opt out
// with [WithoutPanicRecovery].
//
// Errors are kept as values. Each failed task's error is wrapped in a
// [TaskError] that records the task's index, and [Report.Err] joins them
// with [errors.Join], so [errors.Is] and [errors.As] still reach whatever
// the task itself returned.
package asyncworker
