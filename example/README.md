# Runnable examples

Run commands from the repository root with Go 1.22 or later. Every program is
self-contained and uses only the standard library. Both HTTP examples create
their own loopback server; notification examples only print locally.

Start with `queue` for work that returns only an error, or `queue_results` for
typed values. Use a batch when the whole input is known upfront and results
should be kept in input order. `stream` is for applications that already own
a task channel.

| Example | Use case | Run |
| --- | --- | --- |
| [queue](queue/main.go) | Managed error-only queue with minimal setup | `go run ./example/queue` |
| [queue_results](queue_results/main.go) | Typed queue, serialized `OnResult`, individual errors | `go run ./example/queue_results` |
| [queue_http](queue_http/main.go) | HTTP tasks with concurrency, rate and jitter limits | `go run ./example/queue_http` |
| [producers](producers/main.go) | Multiple goroutines submitting to one queue | `go run ./example/producers` |
| [cancellation](cancellation/main.go) | Deadline, submission backpressure, skipped tasks | `go run ./example/cancellation` |
| [simple](simple/main.go) | Typed batch results, progress and JSON report | `go run ./example/simple` |
| [httpcollect](httpcollect/main.go) | Batch HTTP collection with intentional server failures | `go run ./example/httpcollect` |
| [failfast](failfast/main.go) | Custom `Task` type and `WithStopOnFirstError` | `go run ./example/failfast` |
| [background](background/main.go) | Start a batch in the background and stop through its handle | `go run ./example/background` |
| [stream](stream/main.go) | Caller-owned task channel and live result consumption | `go run ./example/stream` |

## Managed queues

### Notifications without results: queue

`NewErrQueue` starts workers internally. Submit ordinary `func(context.Context)
error` functions with `AddFunc`, then call `CloseAndWait`. There is no producer
goroutine or result reader to manage. The program prints five notifications and
a `done: 5/5 notifications` summary. Notification order can vary.

### Typed values and individual failures: queue_results

`NewQueue[int]` parses four strings, including the intentionally invalid `oops`.
`OnResult` adds successful values and records each failure by `Result.Index`.
Callbacks are serialized, so the example needs no mutex; main reads their state
after `CloseAndWait`.

The summary is `succeeded=3 failed=1 sum=44`. The original `strconv.ErrSyntax`
survives error wrapping. A queue summary retains only the first task error;
use `OnResult` to inspect every failure. No callback is required if values and
individual errors are not needed.

### Throttled HTTP requests: queue_http

Twelve items are fetched from a local HTTP server with `WithLimit(4)`,
`WithRateLimit(40)` and `WithJitter(20*time.Millisecond)`. The queue computes an
initial burst of four automatically. Each task makes one request, passes its
context to HTTP, checks the response status and closes the response body.

The callback collects typed items; main sorts them by ID after the queue
finishes. Output lists items 0 through 11 and the total duration. Duration varies
by host: 40 tokens/s controls task admission and allows the initial burst; it
does not promise exactly 40 completed requests in every sliding second.

### Shared queue with several producers: producers

Three producer goroutines each submit five tasks to the same queue. `AddFunc`
is safe for concurrent use and waits when the pending buffer is full. Producer
errors are recorded with `sync.Once`; cancellation unblocks the other producers.

Main waits for all producers before calling `CloseAndWait`, so admission remains
open until everyone has finished submitting. The serialized result handler
counts five jobs for each producer, with 15 successes in total.

### Deadline and backpressure: cancellation

A 100ms context deadline interrupts simulated 40ms operations running with two
workers. Submission eventually waits for capacity and is unblocked by the same
deadline. The example then waits for accepted work to settle.

Success, failure and skip counts vary with scheduling. The important output is
`all accepted tasks accounted for: true` and `deadline reached: true`. Tasks
observe their context; a task that ignores cancellation would delay shutdown.
For an explicit early stop, call a managed queue's `Stop` and then
`CloseAndWait` to await task and callback completion.

## Batches

### Ordered results and progress: simple

A fixed batch squares 100 numbers and returns results in input order. It shows
`WithLabel`, `WithProgress`, `Report.Values` and JSON serialization of a report.
The progress callback knows the batch total, unlike a queue's unknown total
(`-1`). The output includes the complete 100-entry JSON report.

### Collecting all HTTP failures: httpcollect

The batch fetches 50 local items and deliberately receives HTTP 503 for every
seventh item. All tasks are attempted: 43 succeed and seven fail. Every failure
is available in `Report.Failed`, and `errors.Is` reaches the original sentinel
through `Report.Err`. This contrasts with queue summaries, which retain only
the first task error unless individual results are handled.

### Custom tasks and fail-fast validation: failfast

A `check` struct implements `Task[string]` and carries its own name and failure
configuration. The configuration check succeeds, the database check fails, and
the remaining two checks receive `ErrNotRun` wrapping `ErrFailFast`.

One worker makes that order deterministic. With several workers, checks already
running can finish before they observe cancellation. The original database
error remains discoverable with `errors.Is`.

### Background execution and manual stop: background

`Pool.Start` returns while two tasks are active. The caller can do other work,
then call `Handle.Stop` and `Handle.Wait`. The program intentionally stops eight
tasks: two running tasks observe cancellation and six tasks never start.
`Wait` returns after the started tasks have actually returned.

## Existing task channel: stream

The producer owns and closes the input channel. It selects on cancellation and
`handle.Done` when sending, so an early stop cannot strand it. The consumer
reads `Results` while tasks are being produced, then calls `Wait` for counters.

The example prints eight squares with a sum of 204. Result order can vary;
`Result.Index` identifies the input receive position. Always consume results
concurrently with production, or cancel/stop the stream if results are no longer
needed. A managed queue handles that result consumption internally.

## Error handling and verification

`queue_results`, `cancellation`, `failfast`, `background` and `httpcollect`
intentionally demonstrate errors or cancellation and normally exit with code 0.
Unexpected errors in the managed-queue, producer, stream and background programs
exit with code 1 after cleanup. Concurrent output ordering and elapsed times
can differ between runs.

Executable godoc snippets live in [example_test.go](../example_test.go), including
batch reuse with `Pool.Reset`, panic recovery, progress and the queue APIs:

```bash
go test -run '^Example' .
go test -race ./...
```
