package asyncworker

import (
	"math"
	"runtime"
	"time"
)

// Option configures a pool or queue. Pass options to [New], [NewErrPool],
// [NewQueue] or [NewErrQueue].
type Option func(*config)

type config struct {
	limit         int
	jitter        time.Duration
	rate          float64
	label         string
	stopOnErr     bool
	recoverPanics bool
	progress      func(done, total int)
}

func defaultConfig() config {
	return config{
		limit:         runtime.NumCPU(),
		recoverPanics: true,
	}
}

// WithLimit caps how many tasks run at the same time. The default is
// [runtime.NumCPU].
//
// A value below 1 is ignored and the default is kept, so a zero value
// read out of an unset configuration field cannot stall a run or let it
// start one goroutine per task. A pool never starts more goroutines than
// it has tasks, so a limit far above the number of tasks costs nothing.
func WithLimit(n int) Option {
	return func(c *config) {
		if n >= 1 {
			c.limit = n
		}
	}
}

// WithJitter adds an independent, uniformly random delay in [0, maxDelay)
// before every task, including each worker's first task. The delay occupies
// a worker slot and is interrupted when the run is canceled; a task still
// waiting then is reported with [ErrNotRun]. Jitter is disabled by default.
// A non-positive maxDelay disables it, including one set by an earlier option.
//
// Jitter spreads task starts but does not enforce a requests-per-second limit
// or a minimum interval between starts. It does not affect requests or retries
// made within a task.
func WithJitter(maxDelay time.Duration) Option {
	return func(c *config) {
		c.jitter = maxDelay
	}
}

// WithRateLimit limits task starts using a token bucket shared by all workers
// and runs of this pool. The bucket starts full and refills at rps tokens per
// second. Each task start consumes one token. Its burst capacity is computed
// after all options are applied: min(worker limit, floor(rps)), at least 1.
// The worker limit defaults to [runtime.NumCPU]. Option order does not affect
// this calculation. Idle time replenishes the burst, so it is available both
// at startup and after idle periods, rather than only before limiting begins.
//
// Workers wait after jitter, occupying a worker slot. Cancellation interrupts
// the wait and tasks that have not started are reported with [ErrNotRun].
// Requests and retries within a task are not limited. Separate pools have
// independent buckets; [Pool.Reset] does not reset the bucket.
//
// Rate limiting is disabled by default. A non-positive or non-finite rps
// disables it, including a limit set by an earlier option.
func WithRateLimit(rps float64) Option {
	return func(c *config) {
		c.rate = 0
		if rps > 0 && !math.IsInf(rps, 0) && !math.IsNaN(rps) {
			c.rate = rps
		}
	}
}

// WithLabel names the pool. The label is copied into [Report.Label] and
// is only there to tell runs apart in logs.
func WithLabel(label string) Option {
	return func(c *config) { c.label = label }
}

// WithStopOnFirstError ends the run as soon as a task fails: the context
// handed to running tasks is cancelled, and tasks that have not started
// are skipped and reported with [ErrNotRun] wrapping [ErrFailFast].
//
// Without it a run always attempts every task and collects all errors.
func WithStopOnFirstError() Option {
	return func(c *config) { c.stopOnErr = true }
}

// WithProgress registers a callback invoked once per finished task with
// the number of tasks finished so far and the total. Calls are
// serialized, so fn does not need to be safe for concurrent use, and a
// slow fn slows the run down.
// For [Pool.Stream] and queues, total is -1 because the final input size is unknown.
//
// Unlike a panic in a task, a panic in fn is not recovered: it is a bug
// in the callback, and there is no task to blame it on.
func WithProgress(fn func(done, total int)) Option {
	return func(c *config) { c.progress = fn }
}

// WithoutPanicRecovery lets a panicking task crash the process instead of
// being recovered and reported as a [*PanicError].
//
// It is worth setting when a panic means the process state is no longer
// trustworthy and crashing is preferable to carrying on.
func WithoutPanicRecovery() Option {
	return func(c *config) { c.recoverPanics = false }
}
