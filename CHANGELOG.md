# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/), and this project
follows [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

A full rewrite. The old API had no tagged release, so every name was changed
in one pass rather than stacking a compatibility layer on top of it. The
[migration table](README.md#migrating-from-the-pre-10-api) maps the old names
to the new ones.

### Fixed

- **Three runs that hung forever.** A limit of zero or less left the
  dispatcher with no worker to hand work to; an empty pool waited for a result
  that could never arrive. A non-positive limit now falls back to the default,
  and an empty pool returns `StatusEmpty` straight away.
- **The context was ignored.** It was stored in the constructor and never
  read, so neither running nor pending tasks saw a cancellation. It is now
  passed to `Run` and handed to every task, and tasks that never start are
  reported with `ErrNotRun`.
- **A panicking task killed the process.** Panics are now recovered per task
  and reported as a `*PanicError` carrying the stack trace.
- **`Error()` mangled error messages.** Errors were joined into a
  non-constant `fmt.Errorf` format string, so a message containing `%s` came
  back as `%!s(MISSING)`. `Report.Err()` uses `errors.Join`.
- **Error values were flattened to strings**, which broke `errors.Is` and
  `errors.As` and lost track of which task had failed. Each error is now kept
  and wrapped in a `*TaskError` that records the task's index.
- **`Start()` was unusable**: it returned an always-nil error, filled a local
  struct inside a goroutine and threw it away, leaving no way to get the
  result or to know when the work was done. It now returns a `*Handle` with
  `Wait`, `Done` and `Stop`.
- **`go build ./...` failed.** Both example programs declared `package main`
  with a `func main` in the same directory. They are now separate commands
  under `example/simple` and `example/httpcollect`, and both run offline.
- **The pool started one goroutine per configured limit**, so a limit of 5000
  with one task started 5000 goroutines. It now starts
  `min(limit, len(tasks))`.
- **Tasks could not return a value.** `RunAsync() error` meant collecting
  results required a shared slice and a mutex in user code, as the old README
  example showed. Tasks are now `Run(ctx) (T, error)`.
- Removed the atomic operations on variables that only one goroutine ever
  touched, and the `select` with a single case.
- Removed the unread `taskCount` field and the `"do not have actions"` status
  string that was not among the package's own status constants.
- Inverted channel ownership on shutdown: a worker used to close the channel
  the controller sent on. The controller now closes the job channel and the
  workers drain it.

### Added

- Generic `Pool[T]` with `Task[T]`, `TaskFunc[T]`, and `Do` for tasks that
  produce no value.
- `Report[T]` with `Status`, `Total`, `Succeeded`, `Results`, `Duration()`,
  `Values()`, `Failed()` and `Err()`, and a `MarshalJSON` on `Result[T]` so a
  report survives `encoding/json`.
- Functional options: `WithLimit`, `WithLabel`, `WithStopOnFirstError`,
  `WithProgress`, `WithoutPanicRecovery`.
- `Handle[T]` for background runs, with `Wait`, `Done` and `Stop`.
- Sentinel errors `ErrNotRun`, `ErrNilTask`, `ErrStopped` and `ErrFailFast`.
- A test suite covering every fix above, run under `-race` in CI, plus godoc
  examples with verified output. Statement coverage is 98%.
- `LICENSE` (MIT). The project previously had none, which left it under
  default copyright and unusable by anyone else.
- GitHub Actions CI: build, `go vet`, `gofmt`, `staticcheck` and
  `go test -race` on Linux, macOS and Windows.

### Changed

- The package now lives at the module root as `asyncworker`, instead of under
  `src/async_tasks_service/acync_workers` — which also drops the `acync`
  typo from the import path.
- Minimum Go version is 1.22, for per-iteration loop variables. A library
  whose API is built around closures in loops should not ship the old capture
  footgun to its callers.
- `Pool.Add` is safe for concurrent use.
- `WithProgress` callbacks are serialized, so they need no lock of their own.
