package asyncworker

import (
	"context"
	"errors"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestStreamExecutesBeforeInputCloses(t *testing.T) {
	t.Parallel()
	var progress []int
	pool := New[int](WithLimit(2), WithLabel("live"), WithProgress(func(done, total int) {
		if total != -1 {
			t.Errorf("stream progress total = %d, want -1", total)
		}
		progress = append(progress, done)
	}))
	pool.AddFunc(func(context.Context) (int, error) { return 999, nil })
	input := make(chan Task[int])
	h := pool.Stream(context.Background(), input)
	defer h.Stop()
	select {
	case input <- TaskFunc[int](func(context.Context) (int, error) { return 42, nil }):
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not receive a task")
	}
	res := streamResult(t, h)
	if res.Index != 0 || res.Value != 42 || res.Err != nil {
		t.Fatalf("result = %+v", res)
	}
	select {
	case <-h.Done():
		t.Fatal("open input finished prematurely")
	default:
	}
	close(input)
	for range h.Results() {
		t.Error("unexpected extra result")
	}
	report := streamWait(t, h)
	if report.Status != StatusDone || report.Total != 1 || report.Succeeded != 1 || report.Label != "live" || report.Cause != nil {
		t.Fatalf("summary = %+v", report)
	}
	if pool.Len() != 1 || len(progress) != 1 || progress[0] != 1 {
		t.Errorf("pool or progress changed unexpectedly")
	}
	if again := h.Wait(); again.Total != 1 {
		t.Error("Wait is not idempotent")
	}
	h.Stop()
}

func TestStreamResultsDoNotWaitForEarlierTasks(t *testing.T) {
	t.Parallel()
	gate := make(chan struct{})
	input := make(chan Task[int], 2)
	input <- TaskFunc[int](func(ctx context.Context) (int, error) {
		select {
		case <-gate:
			return 10, nil
		case <-ctx.Done():
			return 0, ctx.Err()
		}
	})
	input <- TaskFunc[int](func(context.Context) (int, error) { return 20, nil })
	close(input)
	h := New[int](WithLimit(2)).Stream(context.Background(), input)
	defer h.Stop()
	if res := streamResult(t, h); res.Index != 1 || res.Value != 20 {
		t.Fatalf("second task was held back: %+v", res)
	}
	close(gate)
	if res := streamResult(t, h); res.Index != 0 || res.Value != 10 {
		t.Errorf("first result = %+v", res)
	}
	streamWait(t, h)
}

func TestStreamRespectsLimitAndInputIndices(t *testing.T) {
	t.Parallel()
	const limit, total = 4, 100
	var active, peak atomic.Int64
	entered := make(chan struct{}, total)
	gate := make(chan struct{})
	input := make(chan Task[int], total)
	for i := 0; i < total; i++ {
		input <- TaskFunc[int](func(ctx context.Context) (int, error) {
			n := active.Add(1)
			defer active.Add(-1)
			for old := peak.Load(); n > old; old = peak.Load() {
				if peak.CompareAndSwap(old, n) {
					break
				}
			}
			entered <- struct{}{}
			select {
			case <-gate:
				return i, nil
			case <-ctx.Done():
				return 0, ctx.Err()
			}
		})
	}
	close(input)
	h := New[int](WithLimit(limit)).Stream(context.Background(), input)
	defer h.Stop()
	for i := 0; i < limit; i++ {
		select {
		case <-entered:
		case <-time.After(3 * time.Second):
			t.Fatal("workers did not start")
		}
	}
	close(gate)
	seen := make(map[int]bool)
	for res := range h.Results() {
		if seen[res.Index] || res.Index != res.Value || res.Err != nil {
			t.Errorf("result = %+v", res)
		}
		seen[res.Index] = true
	}
	report := streamWait(t, h)
	if len(seen) != total || report.Total != total || report.Succeeded != total || report.Failed != 0 || report.Skipped != 0 || report.Dropped != 0 {
		t.Errorf("incomplete results: %+v, received %d", report, len(seen))
	}
	if got := peak.Load(); got != limit {
		t.Errorf("peak = %d, want %d", got, limit)
	}
}

func TestStreamStopUnblocksUndrainedResults(t *testing.T) {
	t.Parallel()
	input := make(chan Task[int], 100)
	for i := 0; i < cap(input); i++ {
		input <- TaskFunc[int](func(context.Context) (int, error) { return i, nil })
	}
	blocked := make(chan struct{})
	pool := New[int](WithLimit(1), WithProgress(func(done, total int) {
		if done == 2 {
			close(blocked)
		}
	}))
	h := pool.Stream(context.Background(), input)
	defer h.Stop()
	select {
	case <-blocked:
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not fill its result buffer")
	}
	h.Stop()
	report := streamWait(t, h)
	delivered := 0
	for range h.Results() {
		delivered++
	}
	if report.Status != StatusCanceled || !errors.Is(report.Cause, ErrStopped) || report.Dropped == 0 || delivered+report.Dropped != report.Total {
		t.Errorf("summary = %+v, delivered %d", report, delivered)
	}
	if len(input) == 0 {
		t.Error("Stop drained input")
	}
	// The pool never owns input: it is still open and can be closed by its owner.
	close(input)
}

func TestStreamEmptyAndCanceledInputs(t *testing.T) {
	t.Parallel()
	t.Run("closed", func(t *testing.T) {
		t.Parallel()
		input := make(chan Task[int])
		close(input)
		h := New[int](WithLimit(2)).Stream(context.Background(), input)
		if r := streamWait(t, h); r.Status != StatusEmpty || r.Total != 0 {
			t.Errorf("summary = %+v", r)
		}
	})
	t.Run("nil input", func(t *testing.T) {
		t.Parallel()
		h := New[int](WithLimit(2)).Stream(context.Background(), nil)
		h.Stop()
		if r := streamWait(t, h); r.Status != StatusCanceled || !errors.Is(r.Cause, ErrStopped) {
			t.Errorf("summary = %+v", r)
		}
	})
	t.Run("already canceled", func(t *testing.T) {
		t.Parallel()
		ctx, cancel := context.WithCancel(context.Background())
		cancel()
		input := make(chan Task[int], 1)
		input <- nil
		h := New[int](WithLimit(2)).Stream(ctx, input)
		if r := streamWait(t, h); r.Total != 0 || !errors.Is(r.Cause, context.Canceled) {
			t.Errorf("summary = %+v", r)
		}
		if len(input) != 1 {
			t.Error("read from input after cancellation")
		}
	})
}

func TestStreamErrorsAndFailFast(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	for _, failFast := range []bool{false, true} {
		name := "collect"
		if failFast {
			name = "fail fast"
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			opts := []Option{WithLimit(1)}
			if failFast {
				opts = append(opts, WithStopOnFirstError())
			}
			input := make(chan Task[int], 4)
			input <- TaskFunc[int](func(context.Context) (int, error) { return 0, boom })
			input <- nil
			input <- TaskFunc[int](func(context.Context) (int, error) { panic("bad task") })
			input <- TaskFunc[int](func(context.Context) (int, error) { return 7, nil })
			close(input)
			h := New[int](opts...).Stream(context.Background(), input)
			defer h.Stop()
			results := make([]Result[int], 0)
			for res := range h.Results() {
				results = append(results, res)
			}
			r := streamWait(t, h)
			if r.Status != StatusFailed || !errors.Is(r.FirstError, boom) {
				t.Fatalf("summary = %+v", r)
			}
			if failFast {
				if r.Total != 1 || r.Failed != 1 || !errors.Is(r.Cause, ErrFailFast) || len(input) != 3 {
					t.Errorf("summary = %+v", r)
				}
			} else {
				var panicErr *PanicError
				if r.Total != 4 || r.Succeeded != 1 || r.Failed != 3 || len(results) != 4 ||
					!errors.Is(results[1].Err, ErrNilTask) || !errors.As(results[2].Err, &panicErr) {
					t.Errorf("summary = %+v, results = %+v", r, results)
				}
			}
		})
	}
}

func TestStreamSharesRateLimiterWithBatch(t *testing.T) {
	t.Parallel()
	pool := New[int](WithLimit(2), WithRateLimit(0.001), WithJitter(time.Nanosecond))
	pool.AddFunc(func(context.Context) (int, error) { return 1, nil })
	pool.Run(context.Background()) // Consume the only token.
	input := make(chan Task[int])
	h := pool.Stream(context.Background(), input)
	defer h.Stop()
	select {
	case input <- TaskFunc[int](func(context.Context) (int, error) { t.Error("stream got a fresh token"); return 2, nil }):
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not accept input")
	}
	h.Stop()
	res := streamResult(t, h)
	r := streamWait(t, h)
	if !errors.Is(res.Err, ErrNotRun) || !errors.Is(res.Err, ErrStopped) || r.Total != 1 || r.Skipped != 1 || r.Succeeded != 0 {
		t.Errorf("summary = %+v, result = %+v", r, res)
	}
}

func TestStreamWaitsForCanceledTaskReturn(t *testing.T) {
	t.Parallel()
	entered, canceled, release := make(chan struct{}), make(chan struct{}), make(chan struct{})
	input := make(chan Task[int], 1)
	input <- TaskFunc[int](func(ctx context.Context) (int, error) {
		close(entered)
		<-ctx.Done()
		close(canceled)
		<-release
		return 0, ctx.Err()
	})
	close(input)
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	h := New[int](WithLimit(2)).Stream(ctx, input)
	defer h.Stop()
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("task did not start")
	}
	cause := errors.New("producer shutdown")
	cancel(cause)
	select {
	case <-canceled:
	case <-time.After(3 * time.Second):
		t.Fatal("task context was not canceled")
	}
	select {
	case <-h.Done():
		t.Fatal("stream ended before its task returned")
	default:
	}
	close(release)
	for range h.Results() {
	}
	r := streamWait(t, h)
	if r.Total != 1 || r.Failed != 1 || r.Status != StatusCanceled || !errors.Is(r.Cause, cause) || !errors.Is(r.FirstError, context.Canceled) {
		t.Errorf("summary = %+v", r)
	}
}

func TestStreamMultipleProducers(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	input := make(chan Task[int])
	h := New[int](WithLimit(4)).Stream(ctx, input)
	defer h.Stop()
	var wg sync.WaitGroup
	for producer := 0; producer < 4; producer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := 0; i < 25; i++ {
				task := TaskFunc[int](func(context.Context) (int, error) { return producer*25 + i, nil })
				select {
				case input <- task:
				case <-ctx.Done():
					return
				case <-h.Done():
					return
				}
			}
		}()
	}
	go func() { wg.Wait(); close(input) }()
	values, indices := make(map[int]bool), make(map[int]bool)
	for res := range h.Results() {
		if values[res.Value] || indices[res.Index] || res.Err != nil {
			t.Errorf("duplicate or failed result: %+v", res)
		}
		values[res.Value], indices[res.Index] = true, true
	}
	if r := streamWait(t, h); r.Status != StatusDone || r.Total != 100 || r.Succeeded != 100 {
		t.Errorf("summary = %+v", r)
	}
}

func TestStreamDoesNotLeakGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		input := make(chan Task[int], 1)
		input <- TaskFunc[int](func(context.Context) (int, error) { return 1, nil })
		close(input)
		h := New[int](WithLimit(4)).Stream(context.Background(), input)
		for range h.Results() {
		}
		streamWait(t, h)
	}
	waitForGoroutines(t, before)
}

func streamResult[T any](t *testing.T, h *StreamHandle[T]) Result[T] {
	t.Helper()
	select {
	case res, ok := <-h.Results():
		if !ok {
			t.Fatal("result channel closed prematurely")
		}
		return res
	case <-time.After(3 * time.Second):
		t.Fatal("result did not arrive")
	}
	return Result[T]{}
}

func streamWait[T any](t *testing.T, h *StreamHandle[T]) StreamReport {
	t.Helper()
	select {
	case <-h.Done():
		return h.Wait()
	case <-time.After(3 * time.Second):
		t.Fatal("stream did not terminate")
	}
	return StreamReport{}
}
