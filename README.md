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
| `WithRateLimit(rps)` | Token bucket for task starts, shared by all workers and runs of this pool. Refills at `rps` tokens/sec. Burst is `max(1, min(worker limit, floor(rps)))`. Off by default. Invalid rates disable it. |
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
