package asyncworker

import "runtime"

// Option configures a pool. Pass options to [New] or [NewErrPool].
type Option func(*config)

type config struct {
	limit         int
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
