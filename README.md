# go-async-worker

[![CI](https://github.com/mitrandir69/go-async-worker/actions/workflows/ci.yml/badge.svg)](https://github.com/mitrandir69/go-async-worker/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/mitrandir69/go-async-worker.svg)](https://pkg.go.dev/github.com/mitrandir69/go-async-worker)
[![Go Report Card](https://goreportcard.com/badge/github.com/mitrandir69/go-async-worker)](https://goreportcard.com/report/github.com/mitrandir69/go-async-worker)

Run tasks through a managed queue, a batch, or an existing channel with a
bounded number of goroutines, and get typed results and errors.

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

For tasks arriving over time, let the library manage the queue, channels and
workers. `NewErrQueue` starts immediately and accepts ordinary error-returning
functions:

```go
queue := asyncworker.NewErrQueue(ctx,
    asyncworker.WithLimit(16),
    asyncworker.WithRateLimit(200),
)
defer queue.Stop()

for _, message := range messages {
    if err := queue.AddFunc(func(ctx context.Context) error {
        return send(ctx, message)
    }); err != nil {
        return err
    }
}

return queue.CloseAndWait().Err()
```

There is no channel to create or close, no producer goroutine to start, and no
result channel to drain. `AddFunc` waits when the queue is full. `CloseAndWait`
finishes all accepted work and returns its summary; `Stop` cancels early.

## Batch runs

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

## Managed queues and results

For tasks producing values, use `NewQueue[T]`. An optional `OnResult` handler
receives results one at a time, so it can update local state without a mutex:

```go
sum := 0
queue := asyncworker.NewQueue[int](ctx, asyncworker.WithLimit(4)).
    OnResult(func(result asyncworker.Result[int]) {
        if result.Err == nil {
            sum += result.Value
        }
    })
defer queue.Stop()

for i := 1; i <= 5; i++ {
    if err := queue.AddFunc(func(context.Context) (int, error) {
        return i * i, nil
    }); err != nil {
        return err
    }
}

report := queue.CloseAndWait()
fmt.Println(sum) // 55; CloseAndWait also waits for result handlers
return report.Err()
```

Install `OnResult` before adding tasks to observe every accepted task. Results
arrive in completion order; `Index` records the internal input receive order.
`Add` also accepts a `Task[T]` directly. `NewErrQueue` accepts `func(ctx) error`
through `AddFunc` without requiring a `Do` adapter. It supports `OnResult` too.
All existing worker, rate, jitter, progress, panic and fail-fast options apply.

The pending-task buffer defaults to the final `WithLimit` value, and the result
buffer has the same capacity. Result handling and workers run internally.
No result history is retained: without a handler, values are discarded after
updating counters. Working memory is bounded by the worker limit, in addition
to data retained by the caller or tasks. Slow handlers apply backpressure and
must return; they must not call blocking operations such as `Add` or
`CloseAndWait` on the same queue. Calling `Stop` or `Close` from a handler is
safe. Handler panics are not recovered, like progress callback panics.

`Add`/`AddFunc`, handler replacement and closing are safe for concurrent use:

- `CloseAndWait` closes admission, processes all accepted tasks and callbacks,
  and returns a `StreamReport`. Repeated or concurrent calls return the same
  summary.
- `Stop` cancels tasks and skips queued work. It returns immediately; call
  `CloseAndWait` to wait for running tasks and callbacks to return.
- `Close` stops admission without waiting. `Done` closes when the final summary
  is ready. These are optional when using `CloseAndWait`.

After closing, submissions return `ErrQueueClosed`. Cancellation also unblocks
pending submissions; they may return the cancellation cause instead. Calls
overlapping close/cancellation may be accepted or rejected, and rejected tasks
are not counted. Every successful `Add` is counted in the final `Total`, even
if it is still buffered when cancellation or fail-fast occurs. Skipped tasks
receive `ErrNotRun` wrapping the cancellation cause. A queue's internal result
consumer delivers every accepted task to an installed handler, including skipped
tasks after cancellation; `Dropped` is zero. No external consumer is needed.

The summary contains success/failure/skip counters, label, timing, cancellation
cause and the first observed task error. `report.Err()` joins that first error
with the cancellation cause; it does not retain every task error. Use `OnResult`
to inspect individual failures. `WithProgress` reports `(completed, -1)` because
the eventual total is unknown.

## Using an existing task channel

Use the lower-level `Stream` when the caller already owns a task channel. It starts workers immediately and
executes tasks as they are received, without waiting for the input to close.
Closing input finishes the stream after accepted tasks return. `Stream` uses
the pool's options and shared rate limiter; tasks registered with `Add` belong
to batch runs and are not consumed by the stream.

```go
ctx, cancel := context.WithCancel(context.Background())
defer cancel()

pool := asyncworker.New[int](
    asyncworker.WithLimit(4),
    asyncworker.WithRateLimit(200),
    asyncworker.WithJitter(10*time.Millisecond),
)
tasks := make(chan asyncworker.Task[int], 16)
handle := pool.Stream(ctx, tasks)
defer handle.Stop()

go func() {
    defer close(tasks) // the producer owns the input channel
    for i := 0; i < 100; i++ {
        task := asyncworker.TaskFunc[int](func(ctx context.Context) (int, error) {
            return i * i, nil
        })
        select {
        case tasks <- task:
        case <-ctx.Done():
            return
        case <-handle.Done():
            return
        }
    }
}()

for result := range handle.Results() {
    if result.Err != nil {
        log.Printf("task %d: %v", result.Index, result.Err)
        continue
    }
    fmt.Println(result.Index, result.Value)
}
summary := handle.Wait()
fmt.Println(summary.Status, summary.Total, summary.Succeeded)
```

Results are delivered as tasks finish; `Index` identifies their input receive
order, starting at zero. Read results concurrently with production. A full
result buffer applies backpressure; calling `Wait` before consuming results
can block. `Results` closes when the run finishes. `Done`, `Stop` and repeated
or concurrent `Wait` calls are supported. For an `ErrPool`, send tasks created
with `Do` and consume the resulting `Result[Void]` channel in the same way.

The stream retains no task/result history: worker state and its result buffer
are bounded by `WithLimit`, in addition to the caller's input buffer. The
`StreamReport` returned by `Wait` contains `Total` received tasks, `Succeeded`,
`Failed`, `Skipped`, `Dropped`, the first task error, the cancellation cause,
label, status and timestamps. `Total = Succeeded + Failed + Skipped`.
Results already delivered to a consumer or buffered are not retained in the
summary. `WithProgress` receives `(completed, -1)` once per executed task,
serialized within that stream; skipped tasks do not trigger it.

Context cancellation, `Stop` and fail-fast stop input reception, cancel running
tasks, and interrupt jitter/rate waits. Accepted tasks that have not started
are counted as `Skipped` with `ErrNotRun` wrapping the cause. Tasks remaining
in input are neither consumed nor counted. The pool never closes or drains
input. Producers should select on cancellation or `handle.Done()` when sending.

On cancellation, result delivery stops blocking: results that cannot be sent
are counted as `Dropped`. Already buffered results remain readable. The summary
still counts every accepted task and retains its first task failure. This lets
`Stop` finish even when results are no longer being consumed. Running tasks must
cooperate with cancellation, just as with batch runs. A nil or open empty input
waits for more work until canceled; a normally closed empty input is `StatusEmpty`.

## Options

| Option | Effect |
| --- | --- |
| `WithLimit(n)` | Tasks running at once. Default `runtime.NumCPU()`. A value below 1 is ignored, and the pool never starts more goroutines than it has tasks. |
| `WithJitter(maxDelay)` | Independent random delay in `[0, maxDelay)` before every task, including the first per worker. Non-positive values disable it. Off by default. |
| `WithRateLimit(rps)` | Token bucket for task starts, shared by all workers and runs of this pool. Refills at `rps` tokens/sec. Burst is `max(1, min(worker limit, floor(rps)))`. Off by default. Invalid rates disable it. |
| `WithLabel(s)` | Names the run; copied into `Report.Label` for logs. |
| `WithStopOnFirstError()` | Cancel the run as soon as a task fails. Off by default: a run attempts every task and collects all errors. |
| `WithProgress(fn)` | Called once per executed task with `(done, total)`; stream total is `-1`. Calls are serialized within each run. |
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
    asyncworker.WithRateLimit(10), // 10 tokens/sec, automatic burst of 4
    asyncworker.WithJitter(50*time.Millisecond),
)
```

The bucket starts full and refills during idle time up to its automatic burst
capacity: the smaller of the configured worker limit and the integer part of
`rps`, with a minimum of 1. The default worker limit is `runtime.NumCPU()`.
For example, `WithLimit(16), WithRateLimit(200)` allows a burst of 16;
`WithLimit(16), WithRateLimit(5)` allows a burst of 5. Rates below 1 still allow
one initial start, with slower refill. The calculation uses the final option
values, so option order does not matter.

Burst is an allowance of task starts, not the number of tasks executing
simultaneously and not a one-time bypass of the limiter. It is replenished
after idle time too. The worker limit still caps executing tasks. A full bucket
allows a short spike, so `rps` is the refill rate rather than a strict count
in every sliding one-second window. Workers wait for jitter first, then for a token,
and waiting occupies a worker slot. Task execution order is not guaranteed.
Context cancellation, `Handle.Stop`, and fail-fast interrupt both waits;
skipped tasks retain `ErrNotRun` and the cancellation cause without triggering
progress callbacks.

Repeated and concurrent runs of the same pool share its bucket. `Reset` only
clears tasks; it does not refill the bucket. Separate pools have independent
limits. Non-positive, NaN or infinite rates disable limiting,
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
- You need persistent queues, delivery acknowledgements or automatic retries.
  Queues and `Stream` provide in-memory processing with backpressure; persistence
  and redelivery belong to the caller.
- You need retries, scheduling, or request-level rate limiting inside tasks.
  This pool limits task starts and does not manage requests made within them.

## Examples

Ten runnable programs cover the main APIs. Start with a managed queue; each
example is self-contained, and HTTP requests only target local test servers.

| Example | Shows |
| --- | --- |
| [queue](example/queue/main.go) | Minimal `NewErrQueue`, `AddFunc`, `CloseAndWait` |
| [queue_results](example/queue_results/main.go) | Typed values, serialized `OnResult`, individual errors |
| [queue_http](example/queue_http/main.go) | HTTP with concurrency, rate limiting and jitter |
| [producers](example/producers/main.go) | Several producers sharing a managed queue |
| [cancellation](example/cancellation/main.go) | Deadline, backpressure and accounting for accepted work |
| [simple](example/simple/main.go) | Typed batch results, progress and a JSON report |
| [httpcollect](example/httpcollect/main.go) | Batch HTTP collection and all task failures |
| [failfast](example/failfast/main.go) | Custom `Task` implementation and stop on first error |
| [background](example/background/main.go) | Background batch, `Handle.Stop` and `Wait` |
| [stream](example/stream/main.go) | External task channel with live results |

```bash
go run ./example/queue
go run ./example/queue_results
go run ./example/queue_http
```

See the [example guide](example/README.md) for all commands, expected results and
when to choose each API. [Executable godoc examples](example_test.go) are checked
by `go test` as well.

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

## Load testing and benchmarks

Measured on **2026-11-03**, Go **1.26.5**, Windows/amd64,
AMD Ryzen 5 4600H, `GOMAXPROCS=12`. Each scenario ran five times,
sequentially, without the race detector. Medians are calculated separately
for each metric; ranges show the observed minimum and maximum across five
runs, not confidence intervals. These short local measurements are a baseline
for reproduction, not a production capacity guarantee.

### Local HTTP load

The opt-in [HTTP load test](pool_load_test.go) runs **400 requests per scenario**,
with `WithLimit(16)`, against a fresh loopback HTTP/1.1 server. Each handler
waits 5ms and returns HTTP 204. Connections are reused, the client has no extra
connection limit, and both server and client run in the same process. Server,
client and task setup are outside the measured batch duration; initial
connection establishment is included. No external service receives traffic.

| Options in addition to `WithLimit(16)` | Batch time, median | Requests/s, median [min–max] | HTTP p95, median | Peak handlers, max | Peak requests/100ms, max |
| --- | ---: | ---: | ---: | ---: | ---: |
| None | 146.8ms | 2,725 [1,558–2,845] | 6.47ms | 16 | 304 |
| `WithJitter(50*time.Millisecond)` | 828.6ms | 483 [482–500] | 6.10ms | 10 | 62 |
| `WithRateLimit(200)` (automatic burst 16) | 1,926.2ms | 207.7 [207.6–207.7] | 6.50ms | 16 | 36 |
| Rate limit + jitter above | 1,930.3ms | 207.2 [207.1–207.4] | 6.31ms | 6 | 36 |

All **8,000 requests succeeded**, and server concurrency never exceeded 16.
The test asserts completion, successful responses, the request count and the
concurrency bound. Throughput and latency are recorded without machine-specific
performance pass/fail thresholds.

Requests/s is completed requests divided by the full pool run duration.
HTTP p95 is the nearest-rank 95th percentile of individual request durations,
from task entry through response-body close; it excludes queue, jitter and
rate-limit waiting. Peak requests/100ms counts server arrivals in any rolling,
half-open 100ms window. The last two columns show the largest peak across all
five runs; the raw output also records p50 and each individual run's peaks.

In this workload, jitter reduced the largest short request spike from 304 to
62 requests/100ms. It still allowed roughly 483 requests/s, so it does not
replace a rate limit. The rate-limited cases start with 16 tokens and refill
at 200/s: issuing the remaining 384 permissions takes at least
`(400 - 16) / 200 = 1.92s`. The initial allowance explains the roughly 207
completed requests/s in this short batch; the refill rate stays 200/s.
Over a longer continuously busy run the initial burst contributes less to
the average. Timer wake-ups, scheduling and local HTTP overhead are included.
This experiment does not isolate their individual contributions or establish
a performance benefit from combining jitter with rate limiting.

One baseline run took 256.8ms with an HTTP p95 of 36.75ms; it is retained in
the reported range. This illustrates the variation possible on a shared host.

Raw results: [HTTP load output](benchmarks/http-load.txt).

### 30-second run

A separate longer run uses the same local HTTP fixture, `WithLimit(16)`,
`WithRateLimit(200)` (automatic burst 16) and `WithJitter(50*time.Millisecond)`.
It queues 6,016 requests: 16 initial tokens plus 6,000 refilled tokens keep the
pool busy for approximately 30 seconds. This is one measured run, not a
five-run median.

| Requests | Batch time | Average requests/s | Steady requests/s | HTTP p95 | Errors |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 6,016 | 30.006s | 200.492 | 200.000 | 6.25ms | 0 |

Steady throughput counts server arrivals in complete one-second intervals
anchored at the pool's start, excluding the first second and the final partial
second. All **29 complete intervals contained exactly 200 requests**. These
are fixed measurement intervals, not a guarantee for every sliding window.
The initial burst explains the slightly higher average for the entire run.
Peak server concurrency was 5, and the largest rolling 100ms arrival window
contained 36 requests, including startup.

Raw results: [30-second HTTP load output](benchmarks/http-load-30s.txt).

### Pool overhead

[Throughput benchmarks](pool_bench_test.go) run batches of tasks that immediately
return `1`. They measure `Run`, including the task snapshot, worker startup,
synchronization and report allocation. Pool construction and task registration
are excluded; jitter, rate limiting and progress callbacks are disabled.
One benchmark operation is one complete batch.

| Tasks/batch | Workers | Batch time, median | Million tasks/s, median [min–max] | Bytes/batch, median | Allocs/batch, median |
| ---: | ---: | ---: | ---: | ---: | ---: |
| 1,000 | 1 | 0.558ms | 1.79 [1.62–2.00] | 51,105 | 14 |
| 1,000 | 8 | 0.840ms | 1.19 [1.15–1.34] | 52,298 | 21 |
| 1,000 | 32 | 0.835ms | 1.20 [1.10–1.21] | 56,201 | 45 |
| 10,000 | 1 | 4.991ms | 2.00 [1.82–2.01] | 502,672 | 14 |
| 10,000 | 8 | 7.530ms | 1.33 [1.31–1.37] | 503,792 | 21 |
| 10,000 | 32 | 7.727ms | 1.29 [1.26–1.31] | 507,790 | 46 |

For these near-empty tasks, more workers add coordination cost; they do not
demonstrate scaling for CPU-intensive or remote work. Bytes/batch counts total
heap allocation during `Run`, not peak memory, and excludes the registered task
fixtures. The observed spread, especially for one-worker batches, is a reason
to rerun on the target host before drawing optimization conclusions.

Raw results: [Go benchmark output](benchmarks/pool-throughput.txt).

### Reproduce

Run these commands sequentially from the repository root. The integration test
is excluded from normal `go test ./...` runs. The benchmark uses `b.N` to remain
compatible with the module's minimum Go version, 1.22.

```bash
go test -tags=integration -run '^TestHTTPLoad$' -count=5 -v -timeout=5m .
go test -tags=integration -run '^TestHTTPLoad30Seconds$' -count=1 -v -timeout=1m .
go test -run '^$' -bench '^BenchmarkPoolThroughput$' -benchmem -benchtime=200ms -count=5 -timeout=3m .
```

For longer measurements, increase `-benchtime` and `-count`. Run correctness
checks with the race detector separately; their timings are not comparable
to the tables above. These tests do not simulate WAN latency, TLS, server
throttling or a long-running soak workload.

## Development

```bash
go test -race ./...
go vet ./...
gofmt -l .
```

## License

[MIT](LICENSE)
