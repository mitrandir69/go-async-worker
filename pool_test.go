package asyncworker

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestRunCollectsValuesInOrder(t *testing.T) {
	t.Parallel()

	pool := New[int](WithLimit(4), WithLabel("squares"))
	for i := 0; i < 50; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i * i, nil })
	}

	report := pool.Run(context.Background())

	if report.Status != StatusDone {
		t.Errorf("Status = %q, want %q", report.Status, StatusDone)
	}
	if report.Label != "squares" {
		t.Errorf("Label = %q, want %q", report.Label, "squares")
	}
	if report.Total != 50 || report.Succeeded != 50 {
		t.Errorf("Total/Succeeded = %d/%d, want 50/50", report.Total, report.Succeeded)
	}
	if err := report.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
	for i, res := range report.Results {
		if res.Index != i {
			t.Errorf("Results[%d].Index = %d", i, res.Index)
		}
		if res.Value != i*i {
			t.Errorf("Results[%d].Value = %d, want %d", i, res.Value, i*i)
		}
	}
	if report.Duration() < 0 {
		t.Errorf("Duration() = %v, want >= 0", report.Duration())
	}
}

// TestRunEmptyPoolReturns covers the old deadlock: a pool with no tasks
// used to block forever.
func TestRunEmptyPoolReturns(t *testing.T) {
	t.Parallel()

	report := New[int]().Run(context.Background())

	if report.Status != StatusEmpty {
		t.Errorf("Status = %q, want %q", report.Status, StatusEmpty)
	}
	if report.Total != 0 || len(report.Results) != 0 {
		t.Errorf("Total/len(Results) = %d/%d, want 0/0", report.Total, len(report.Results))
	}
	if err := report.Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

// TestNonPositiveLimitRuns covers the old deadlock: a limit of zero or
// less used to leave the dispatcher with no worker to hand work to.
func TestNonPositiveLimitRuns(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{0, -1, -1000} {
		t.Run("", func(t *testing.T) {
			t.Parallel()

			pool := New[int](WithLimit(limit))
			pool.AddFunc(func(context.Context) (int, error) { return 7, nil })

			report := pool.Run(context.Background())

			if report.Status != StatusDone {
				t.Errorf("limit %d: Status = %q, want %q", limit, report.Status, StatusDone)
			}
			if got := report.Values(); len(got) != 1 || got[0] != 7 {
				t.Errorf("limit %d: Values() = %v, want [7]", limit, got)
			}
		})
	}
}

// TestRespectsLimit pins both directions: exactly limit tasks can be in
// flight at once, and never more.
func TestRespectsLimit(t *testing.T) {
	t.Parallel()

	const (
		limit = 4
		total = 40
	)

	var inFlight, peak atomic.Int64
	entered := make(chan struct{}, total)
	release := make(chan struct{})

	pool := New[int](WithLimit(limit))
	for i := 0; i < total; i++ {
		pool.AddFunc(func(context.Context) (int, error) {
			n := inFlight.Add(1)
			for {
				old := peak.Load()
				if n <= old || peak.CompareAndSwap(old, n) {
					break
				}
			}
			entered <- struct{}{}
			<-release
			inFlight.Add(-1)
			return 0, nil
		})
	}

	handle := pool.Start(context.Background())

	// Each task sends once and then blocks, so reading limit values proves
	// that limit tasks are in flight at the same moment.
	for i := 0; i < limit; i++ {
		<-entered
	}
	close(release)
	report := handle.Wait()

	if report.Status != StatusDone {
		t.Errorf("Status = %q, want %q", report.Status, StatusDone)
	}
	if got := peak.Load(); got != limit {
		t.Errorf("peak concurrency = %d, want %d", got, limit)
	}
}

// TestWorkersNeverExceedTasks covers the old waste: the pool used to
// start one goroutine per configured limit even with a single task.
func TestWorkersNeverExceedTasks(t *testing.T) {
	before := runtime.NumGoroutine()

	entered := make(chan struct{})
	release := make(chan struct{})

	pool := New[int](WithLimit(5000))
	pool.AddFunc(func(context.Context) (int, error) {
		close(entered)
		<-release
		return 0, nil
	})

	handle := pool.Start(context.Background())
	<-entered
	during := runtime.NumGoroutine()
	close(release)
	handle.Wait()

	if grew := during - before; grew > 10 {
		t.Errorf("one task with limit 5000 grew goroutines by %d, want a handful", grew)
	}
}

func TestRunLeaksNoGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()

	for i := 0; i < 5; i++ {
		pool := New[int](WithLimit(50))
		for j := 0; j < 50; j++ {
			pool.AddFunc(func(context.Context) (int, error) { return j, nil })
		}
		pool.Run(context.Background())
	}

	waitForGoroutines(t, before)
}

// TestCancelStopsRunAndReachesTasks covers the old no-op context: the
// context used to be stored and never read, so neither running nor
// pending tasks saw a cancellation.
func TestCancelStopsRunAndReachesTasks(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	entered := make(chan struct{})

	// A single worker, so the first task holds the pool until it returns.
	pool := New[int](WithLimit(1))
	pool.AddFunc(func(taskCtx context.Context) (int, error) {
		close(entered)
		<-taskCtx.Done()
		return 0, taskCtx.Err()
	})
	for i := 1; i < 10; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}

	handle := pool.Start(ctx)
	<-entered
	cancel()
	report := handle.Wait()

	if report.Status != StatusCanceled {
		t.Errorf("Status = %q, want %q", report.Status, StatusCanceled)
	}
	if report.Succeeded != 0 {
		t.Errorf("Succeeded = %d, want 0", report.Succeeded)
	}
	if !errors.Is(report.Results[0].Err, context.Canceled) {
		t.Errorf("Results[0].Err = %v, want it to wrap context.Canceled", report.Results[0].Err)
	}
	for _, res := range report.Results[1:] {
		if !errors.Is(res.Err, ErrNotRun) {
			t.Errorf("Results[%d].Err = %v, want it to wrap ErrNotRun", res.Index, res.Err)
		}
		if !errors.Is(res.Err, context.Canceled) {
			t.Errorf("Results[%d].Err = %v, want it to wrap context.Canceled", res.Index, res.Err)
		}
	}
}

func TestHandleStop(t *testing.T) {
	t.Parallel()

	entered := make(chan struct{})

	pool := New[int](WithLimit(1))
	pool.AddFunc(func(taskCtx context.Context) (int, error) {
		close(entered)
		<-taskCtx.Done()
		return 0, taskCtx.Err()
	})
	for i := 1; i < 5; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}

	handle := pool.Start(context.Background())
	<-entered
	handle.Stop()

	select {
	case <-handle.Done():
	case <-time.After(10 * time.Second):
		t.Fatal("Done() never closed after Stop()")
	}

	report := handle.Wait()
	if report.Status != StatusCanceled {
		t.Errorf("Status = %q, want %q", report.Status, StatusCanceled)
	}
	if !errors.Is(report.Results[4].Err, ErrStopped) {
		t.Errorf("Results[4].Err = %v, want it to wrap ErrStopped", report.Results[4].Err)
	}

	// Wait is idempotent, and Stop after the run is a no-op.
	handle.Stop()
	if again := handle.Wait(); again.Status != report.Status {
		t.Errorf("second Wait() = %q, want %q", again.Status, report.Status)
	}
}

// TestPanicBecomesError covers the old crash: a panicking task used to
// take the whole process down.
func TestPanicBecomesError(t *testing.T) {
	t.Parallel()

	pool := New[int](WithLimit(2))
	pool.AddFunc(func(context.Context) (int, error) { panic("task blew up") })
	pool.AddFunc(func(context.Context) (int, error) { return 42, nil })

	report := pool.Run(context.Background())

	if report.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", report.Status, StatusFailed)
	}
	if report.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", report.Succeeded)
	}
	if got := report.Values(); len(got) != 1 || got[0] != 42 {
		t.Errorf("Values() = %v, want [42]", got)
	}

	var panicErr *PanicError
	if !errors.As(report.Results[0].Err, &panicErr) {
		t.Fatalf("Results[0].Err = %v, want a *PanicError", report.Results[0].Err)
	}
	if panicErr.Value != "task blew up" {
		t.Errorf("PanicError.Value = %v, want %q", panicErr.Value, "task blew up")
	}
	if !strings.Contains(string(panicErr.Stack), "runTask") {
		t.Errorf("PanicError.Stack was not captured where the panic was recovered:\n%s", panicErr.Stack)
	}
}

// TestWithoutPanicRecoveryCrashes re-runs itself in a child process,
// because the behaviour under test is the process dying.
func TestWithoutPanicRecoveryCrashes(t *testing.T) {
	const marker = "ASYNCWORKER_PANIC_CHILD"

	if os.Getenv(marker) == "1" {
		pool := New[int](WithoutPanicRecovery())
		pool.AddFunc(func(context.Context) (int, error) { panic("uncaught") })
		pool.Run(context.Background())
		return
	}

	cmd := exec.Command(os.Args[0], "-test.run=^TestWithoutPanicRecoveryCrashes$")
	cmd.Env = append(os.Environ(), marker+"=1")
	out, err := cmd.CombinedOutput()

	if err == nil {
		t.Fatalf("child exited cleanly, want a panic:\n%s", out)
	}
	if !strings.Contains(string(out), "uncaught") {
		t.Errorf("child output does not mention the panic value:\n%s", out)
	}
}

func TestStopOnFirstError(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	var ran atomic.Int64

	// A single worker makes the order the tasks run in deterministic.
	pool := New[int](WithLimit(1), WithStopOnFirstError())
	pool.AddFunc(func(context.Context) (int, error) {
		ran.Add(1)
		return 0, boom
	})
	for i := 1; i < 10; i++ {
		pool.AddFunc(func(context.Context) (int, error) {
			ran.Add(1)
			return i, nil
		})
	}

	report := pool.Run(context.Background())

	if report.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", report.Status, StatusFailed)
	}
	if got := ran.Load(); got != 1 {
		t.Errorf("%d tasks ran, want only the failing one", got)
	}
	if !errors.Is(report.Err(), boom) {
		t.Errorf("Err() = %v, want it to wrap boom", report.Err())
	}
	if !errors.Is(report.Results[9].Err, ErrFailFast) {
		t.Errorf("Results[9].Err = %v, want it to wrap ErrFailFast", report.Results[9].Err)
	}
}

func TestWithoutStopOnFirstErrorAttemptsEveryTask(t *testing.T) {
	t.Parallel()

	pool := New[int](WithLimit(2))
	for i := 0; i < 10; i++ {
		pool.AddFunc(func(context.Context) (int, error) {
			if i%2 == 0 {
				return 0, errors.New("even is not allowed")
			}
			return i, nil
		})
	}

	report := pool.Run(context.Background())

	if report.Status != StatusFailed {
		t.Errorf("Status = %q, want %q", report.Status, StatusFailed)
	}
	if report.Succeeded != 5 {
		t.Errorf("Succeeded = %d, want 5", report.Succeeded)
	}
	if got := len(report.Failed()); got != 5 {
		t.Errorf("len(Failed()) = %d, want 5", got)
	}
}

func TestProgressIsSerializedAndComplete(t *testing.T) {
	t.Parallel()

	const total = 20
	var seen []int

	pool := New[int](
		WithLimit(4),
		// The pool serializes these calls, so appending needs no lock.
		WithProgress(func(done, got int) {
			if got != total {
				t.Errorf("progress total = %d, want %d", got, total)
			}
			seen = append(seen, done)
		}),
	)
	for i := 0; i < total; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i, nil })
	}

	pool.Run(context.Background())

	if len(seen) != total {
		t.Fatalf("progress called %d times, want %d", len(seen), total)
	}
	for i, done := range seen {
		if done != i+1 {
			t.Fatalf("progress sequence = %v, want 1..%d", seen, total)
		}
	}
}

func TestNilTaskIsReported(t *testing.T) {
	t.Parallel()

	pool := New[int]()
	pool.Add(nil)
	pool.AddFunc(func(context.Context) (int, error) { return 1, nil })

	report := pool.Run(context.Background())

	if report.Succeeded != 1 {
		t.Errorf("Succeeded = %d, want 1", report.Succeeded)
	}
	if !errors.Is(report.Results[0].Err, ErrNilTask) {
		t.Errorf("Results[0].Err = %v, want it to wrap ErrNilTask", report.Results[0].Err)
	}
}

func TestAddLenReset(t *testing.T) {
	t.Parallel()

	pool := New[int]()
	if pool.Len() != 0 {
		t.Errorf("Len() = %d, want 0", pool.Len())
	}

	pool.AddFunc(
		func(context.Context) (int, error) { return 1, nil },
		func(context.Context) (int, error) { return 2, nil },
	)
	pool.Add(TaskFunc[int](func(context.Context) (int, error) { return 3, nil }))
	if pool.Len() != 3 {
		t.Errorf("Len() = %d, want 3", pool.Len())
	}

	// Running does not consume the tasks.
	if got := pool.Run(context.Background()).Succeeded; got != 3 {
		t.Errorf("first run Succeeded = %d, want 3", got)
	}
	if got := pool.Run(context.Background()).Succeeded; got != 3 {
		t.Errorf("second run Succeeded = %d, want 3", got)
	}

	pool.Reset()
	if pool.Len() != 0 {
		t.Errorf("Len() after Reset = %d, want 0", pool.Len())
	}
}

// TestAddIsSafeForConcurrentUse is here for the race detector.
func TestAddIsSafeForConcurrentUse(t *testing.T) {
	t.Parallel()

	pool := New[int](WithLimit(8))
	done := make(chan struct{})

	for g := 0; g < 8; g++ {
		go func() {
			defer func() { done <- struct{}{} }()
			for i := 0; i < 25; i++ {
				pool.AddFunc(func(context.Context) (int, error) { return i, nil })
			}
		}()
	}
	for g := 0; g < 8; g++ {
		<-done
	}

	if pool.Len() != 200 {
		t.Fatalf("Len() = %d, want 200", pool.Len())
	}
	if got := pool.Run(context.Background()).Succeeded; got != 200 {
		t.Errorf("Succeeded = %d, want 200", got)
	}
}

func TestErrPool(t *testing.T) {
	t.Parallel()

	boom := errors.New("boom")
	var calls atomic.Int64

	pool := NewErrPool(WithLimit(3))
	for i := 0; i < 6; i++ {
		pool.Add(Do(func(context.Context) error {
			calls.Add(1)
			if i == 4 {
				return boom
			}
			return nil
		}))
	}

	report := pool.Run(context.Background())

	if got := calls.Load(); got != 6 {
		t.Errorf("%d tasks ran, want 6", got)
	}
	if report.Succeeded != 5 {
		t.Errorf("Succeeded = %d, want 5", report.Succeeded)
	}
	if !errors.Is(report.Err(), boom) {
		t.Errorf("Err() = %v, want it to wrap boom", report.Err())
	}
}

func BenchmarkRun(b *testing.B) {
	for n := 0; n < b.N; n++ {
		pool := New[int](WithLimit(8))
		for i := 0; i < 100; i++ {
			pool.AddFunc(func(context.Context) (int, error) { return i, nil })
		}
		pool.Run(context.Background())
	}
}

// waitForGoroutines gives goroutines that are on their way out a moment
// to finish before reporting a leak.
func waitForGoroutines(t *testing.T, want int) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := runtime.NumGoroutine()
		if got <= want {
			return
		}
		if time.Now().After(deadline) {
			t.Errorf("%d goroutines still running, want at most %d", got, want)
			return
		}
		time.Sleep(10 * time.Millisecond)
	}
}
