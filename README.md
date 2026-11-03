# go-async-worker

[![CI](https://github.com/mitrandir69/go-async-worker/actions/workflows/ci.yml/badge.svg)](https://github.com/mitrandir69/go-async-worker/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mitrandir69/go-async-worker.svg)](https://pkg.go.dev/github.com/mitrandir69/go-async-worker)
[![Go Report Card](https://goreportcard.com/badge/github.com/mitrandir69/go-async-worker)](https://goreportcard.com/report/github.com/mitrandir69/go-async-worker)

Run a set of tasks concurrently with a bounded number of goroutines, and get
back a typed result and a real error for every one of them.

No dependencies outside the standard library.

```bash
go get github.com/mitrandir69/go-async-worker
```

## Why

`errgroup.Group` with `SetLimit` already covers "run N things, stop at the
first error". This package is for the other shape: run everything, keep each
task's **value** and each task's **error**, and tell me afterwards what
happened to each one.

That shape is why the API is generic. The task's return value *is* the result,
so collecting output needs no shared slice and no mutex:

```go
pool := asyncworker.New[payload](asyncworker.WithLimit(10))
for _, url := range urls {
    pool.AddFunc(func(ctx context.Context) (payload, error) {
        return fetch(ctx, url)
    })
}

report := pool.Run(ctx)

fmt.Println(report.Status, report.Succeeded, "of", report.Total)
for _, p := range report.Values() {   // only the ones that came back
    use(p)
}
for _, res := range report.Failed() { // and the ones that did not
    log.Printf("task %d: %v", res.Index, res.Err)
}
```

## Quick start

```go
package main

import (
	"context"
	"fmt"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

func main() {
	pool := asyncworker.New[int](asyncworker.WithLimit(4))
	for i := 1; i <= 5; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i * i, nil })
	}

	report := pool.Run(context.Background())

	fmt.Println(report.Status, report.Succeeded, report.Values())
	// done 5 [1 4 9 16 25]
}
```

For work that produces no value, use an `ErrPool` and wrap the functions with
`Do`:

```go
pool := asyncworker.NewErrPool(asyncworker.WithLimit(4))
for _, msg := range messages {
	pool.Add(asyncworker.Do(func(ctx context.Context) error {
		return send(ctx, msg)
	}))
}

if err := pool.Run(ctx).Err(); err != nil {
	return err
}
```

## Errors keep their type

Every failed task's error is wrapped in a `*TaskError` that records the task's
index, and `Report.Err()` joins them with `errors.Join`. Sentinels and custom
error types survive:

```go
report := pool.Run(ctx)

if errors.Is(report.Err(), sql.ErrNoRows) {
	// still works
}

var taskErr *asyncworker.TaskError
if errors.As(report.Err(), &taskErr) {
	log.Printf("task %d was the first to fail", taskErr.Index)
}
```

A panic in a task is recovered, turned into a `*PanicError` with the stack
trace, and reported as that task's error — one bad task does not take the
process down. Opt out with `WithoutPanicRecovery()` when a panic means the
process should die.

## Cancellation

The context passed to `Run` is handed to every task. When it is done the pool
stops handing out work, and tasks that never started are reported with
`ErrNotRun` wrapping the reason:

```go
ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
defer cancel()

report := pool.Run(ctx)
if report.Status == asyncworker.StatusCanceled {
	// some tasks never ran; report.Results says which
}
```

Cancellation is **cooperative**: a task that ignores its context runs to
completion regardless. `Run` never returns before every task it started has
returned, so there are no goroutines still writing after it hands you the
report.

## Running in the background

```go
handle := pool.Start(ctx)

select {
case <-handle.Done():
case <-somethingElse:
	handle.Stop() // skip what has not started, cancel what has
}

report := handle.Wait() // idempotent
```

## Options

| Option | Effect |
| --- | --- |
| `WithLimit(n)` | Tasks running at once. Default `runtime.NumCPU()`. A value below 1 is ignored, and the pool never starts more goroutines than it has tasks. |
| `WithJitter(maxDelay)` | Independent random delay in `[0, maxDelay)` before every task, including the first per worker. Non-positive values disable it. Off by default. |
| `WithRateLimit(rps, burst)` | Token bucket for task starts, shared by all workers and runs of this pool. Refills at `rps` tokens/sec, with up to `burst` immediate starts. Off by default. Invalid values disable it. |
| `WithLabel(s)` | Names the run; copied into `Report.Label` for logs. |
| `WithStopOnFirstError()` | Cancel the run as soon as a task fails. Off by default: a run attempts every task and collects all errors. |
| `WithProgress(fn)` | Called once per finished task with `(done, total)`. Calls are serialized, so `fn` needs no lock. |
| `WithoutPanicRecovery()` | Let a panicking task crash the process. |

For remote calls, combine a concurrency limit with jitter to spread task starts:

```go
pool := asyncworker.New[payload](
    asyncworker.WithLimit(4),
    asyncworker.WithJitter(500*time.Millisecond),
)
```

Each delay occupies a worker slot and stops promptly on context cancellation,
`Handle.Stop`, or fail-fast. Tasks canceled while waiting are reported with
`ErrNotRun` wrapping the cancellation reason, and do not trigger progress.
Jitter permits starts close together; it does not enforce a requests-per-second
limit or space out requests and retries inside a task.

Use `WithRateLimit` to cap task admissions across workers:

```go
pool := asyncworker.New[payload](
    asyncworker.WithLimit(4),
    asyncworker.WithRateLimit(10, 1), // one immediate start, then 10 starts/sec
    asyncworker.WithJitter(50*time.Millisecond),
)
```

The bucket starts full and refills during idle time up to `burst` tokens.
With `burst = 1`, permissions are spaced by at least `1/rps` seconds; larger
bursts allow short spikes. Workers wait for jitter first, then for a token,
and waiting occupies a worker slot. Task execution order is not guaranteed.
Context cancellation, `Handle.Stop`, and fail-fast interrupt both waits;
skipped tasks retain `ErrNotRun` and the cancellation cause without triggering
progress callbacks.

Repeated and concurrent runs of the same pool share its bucket. `Reset` only
clears tasks; it does not refill the bucket. Separate pools have independent
limits. Non-positive, NaN or infinite rates, or `burst < 1`, disable limiting,
including a limit configured by an earlier option. The pool limits task starts;
apply a limiter inside the task if it sends multiple requests or retries.

## Status values

| Status | Meaning |
| --- | --- |
| `StatusDone` | Every task ran, none failed. |
| `StatusFailed` | A task failed, or was skipped because another one failed. |
| `StatusCanceled` | The run was cut short by its context or `Handle.Stop` before every task ran. |
| `StatusEmpty` | The pool held no tasks. |

## When not to use this

- You want "run N things, abort on the first error" and nothing else —
  [`errgroup`](https://pkg.go.dev/golang.org/x/sync/errgroup) with `SetLimit`
  is smaller and already in most projects.
- Your work arrives as an unbounded stream. A pool's tasks are added up front,
  so memory grows with the number of tasks. Feed a channel to a fixed set of
  workers instead.
- You need retries, scheduling, or request-level rate limiting inside tasks.
  This pool limits task starts and does not manage requests made within them.

## Examples

Two runnable programs, both self-contained:

```bash
go run ./example/simple      # typed results, progress, JSON report
go run ./example/httpcollect # concurrent HTTP against a local test server
```

## Migrating from the pre-1.0 API

The old API has no tagged release, and every name below changed in the same
pass. See [CHANGELOG.md](CHANGELOG.md) for why.

| Before | Now |
| --- | --- |
| `.../src/async_tasks_service/acync_workers` | `github.com/mitrandir69/go-async-worker` |
| `NewDefaultAsyncWorker(ctx, label, n)` | `New[T](WithLabel(label), WithLimit(n))` — the context moves to `Run` |
| `IAsyncAction` with `RunAsync() error` | `Task[T]` with `Run(ctx) (T, error)` |
| `worker.AddAction(a)` | `pool.Add(a)` or `pool.AddFunc(fn)` |
| `worker.StartAndWait()` | `pool.Run(ctx)` |
| `worker.Start()` | `pool.Start(ctx)`, which returns a `*Handle` you can wait on |
| `result.Errors []string` | `report.Results[i].Err` and `report.Err()` |
| `status.Done`, `status.DoneWithErrors` | `StatusDone`, `StatusFailed` |

A shared slice plus a `sync.Mutex` to collect results is no longer needed:
make the value the task's return type.

## Development

```bash
go test -race ./...
go vet ./...
gofmt -l .
```

## License

[MIT](LICENSE)
