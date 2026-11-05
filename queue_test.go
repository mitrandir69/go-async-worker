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

func TestQueueNeedsNoResultReader(t *testing.T) {
	t.Parallel()
	var progress []int
	q := NewQueue[int](context.Background(), WithLimit(4), WithLabel("managed"), WithProgress(func(done, total int) {
		if total != -1 {
			t.Errorf("total = %d, want -1", total)
		}
		progress = append(progress, done)
	}))
	defer q.Stop()
	for i := 0; i < 1000; i++ {
		if err := q.AddFunc(func(context.Context) (int, error) { return i, nil }); err != nil {
			t.Fatal(err)
		}
	}
	r := queueWait(t, q)
	if r.Status != StatusDone || r.Total != 1000 || r.Succeeded != 1000 || r.Dropped != 0 || r.Err() != nil || r.Label != "managed" {
		t.Errorf("summary = %+v", r)
	}
	if len(progress) != 1000 {
		t.Fatalf("progress calls = %d", len(progress))
	}
	for i, done := range progress {
		if done != i+1 {
			t.Errorf("progress[%d] = %d", i, done)
		}
	}
	if err := q.Add(nil); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("Add after close = %v", err)
	}
	if again := q.CloseAndWait(); again.Total != r.Total || again.EndedAt != r.EndedAt {
		t.Error("summary changed after completion")
	}
}

func TestQueueResultHandlerIsSerialized(t *testing.T) {
	t.Parallel()
	var active atomic.Int64
	var results []Result[int]
	q := NewQueue[int](context.Background(), WithLimit(8)).OnResult(func(r Result[int]) {
		if active.Add(1) != 1 {
			t.Error("concurrent result callbacks")
		}
		defer active.Add(-1)
		results = append(results, r)
	})
	defer q.Stop()
	for i := 0; i < 100; i++ {
		if err := q.AddFunc(func(context.Context) (int, error) { return i * i, nil }); err != nil {
			t.Fatal(err)
		}
	}
	r := queueWait(t, q)
	if r.Succeeded != 100 || len(results) != 100 {
		t.Fatalf("summary = %+v, callbacks = %d", r, len(results))
	}
	seen := make(map[int]bool)
	for _, res := range results {
		if res.Err != nil || seen[res.Index] || res.Value != res.Index*res.Index {
			t.Errorf("result = %+v", res)
		}
		seen[res.Index] = true
	}
}

func TestQueueCloseUnblocksFullBuffer(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	q := NewErrQueue(context.Background(), WithLimit(1))
	defer q.Stop()
	if err := q.AddFunc(func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return nil
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, entered)
	if err := q.AddFunc(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	started, rejected := make(chan struct{}), make(chan error, 1)
	go func() {
		close(started)
		rejected <- q.AddFunc(func(context.Context) error { t.Error("rejected task ran"); return nil })
	}()
	queueSignal(t, started)
	q.Close()
	select {
	case err := <-rejected:
		if !errors.Is(err, ErrQueueClosed) {
			t.Errorf("blocked Add = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("Close did not unblock Add")
	}
	select {
	case <-q.Done():
		t.Fatal("Close did not wait for accepted work")
	default:
	}
	close(release)
	if r := queueWait(t, q); r.Total != 2 || r.Succeeded != 2 || r.Status != StatusDone {
		t.Errorf("summary = %+v", r)
	}
}

func TestQueueStopAccountsForBufferedTasks(t *testing.T) {
	t.Parallel()
	entered := make(chan struct{})
	var results []Result[Void]
	q := NewErrQueue(context.Background(), WithLimit(1)).OnResult(func(r Result[Void]) { results = append(results, r) })
	defer q.Stop()
	if err := q.AddFunc(func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, entered)
	if err := q.AddFunc(func(context.Context) error { t.Error("buffered task started after Stop"); return nil }); err != nil {
		t.Fatal(err)
	}
	q.Stop()
	r := queueWait(t, q)
	if r.Status != StatusCanceled || r.Total != 2 || r.Failed != 1 || r.Skipped != 1 || r.Dropped != 0 || len(results) != 2 || !errors.Is(r.Err(), ErrStopped) {
		t.Fatalf("summary = %+v, callbacks = %d", r, len(results))
	}
	if results[1].Index != 1 || !errors.Is(results[1].Err, ErrNotRun) || !errors.Is(results[1].Err, ErrStopped) {
		t.Errorf("buffered result = %+v", results[1])
	}
}

func TestQueueFailFastClosesAutomatically(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	boom := errors.New("boom")
	var callbacks int
	q := NewErrQueue(context.Background(), WithLimit(1), WithStopOnFirstError()).OnResult(func(Result[Void]) { callbacks++ })
	defer q.Stop()
	if err := q.AddFunc(func(ctx context.Context) error {
		close(entered)
		select {
		case <-release:
			return boom
		case <-ctx.Done():
			return ctx.Err()
		}
	}); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, entered)
	if err := q.AddFunc(func(context.Context) error { t.Error("task ran after fail-fast"); return nil }); err != nil {
		t.Fatal(err)
	}
	close(release)
	queueSignal(t, q.Done()) // The caller has not closed the queue.
	r := q.CloseAndWait()
	if r.Status != StatusFailed || r.Total != 2 || r.Failed != 1 || r.Skipped != 1 || callbacks != 2 || !errors.Is(r.Err(), boom) || !errors.Is(r.Err(), ErrFailFast) {
		t.Errorf("summary = %+v, callbacks = %d", r, callbacks)
	}
	if err := q.Add(nil); !errors.Is(err, ErrQueueClosed) {
		t.Errorf("Add after fail-fast = %v", err)
	}
}

func TestQueueErrorsKeepTheirTypes(t *testing.T) {
	t.Parallel()
	boom := errors.New("boom")
	var results []Result[int]
	q := NewQueue[int](context.Background(), WithLimit(2)).OnResult(func(r Result[int]) { results = append(results, r) })
	defer q.Stop()
	if err := q.Add(nil); err != nil {
		t.Fatal(err)
	}
	for _, fn := range []func(context.Context) (int, error){
		func(context.Context) (int, error) { panic("bad task") },
		func(context.Context) (int, error) { return 0, boom },
		func(context.Context) (int, error) { return 7, nil },
	} {
		if err := q.AddFunc(fn); err != nil {
			t.Fatal(err)
		}
	}
	r := queueWait(t, q)
	if r.Total != 4 || r.Succeeded != 1 || r.Failed != 3 || r.Status != StatusFailed || r.Err() == nil {
		t.Fatalf("summary = %+v", r)
	}
	byIndex := make(map[int]Result[int])
	for _, res := range results {
		byIndex[res.Index] = res
	}
	var panicErr *PanicError
	if !errors.Is(byIndex[0].Err, ErrNilTask) || !errors.As(byIndex[1].Err, &panicErr) || !errors.Is(byIndex[2].Err, boom) || byIndex[3].Value != 7 {
		t.Errorf("results = %+v", byIndex)
	}
}

func TestQueueContextCancellationUnblocksAdd(t *testing.T) {
	t.Parallel()
	ctx, cancel := context.WithCancelCause(context.Background())
	defer cancel(nil)
	q := NewErrQueue(ctx, WithLimit(1))
	defer q.Stop()
	entered := make(chan struct{})
	if err := q.AddFunc(func(ctx context.Context) error { close(entered); <-ctx.Done(); return ctx.Err() }); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, entered)
	if err := q.AddFunc(func(context.Context) error { t.Error("queued task started after cancellation"); return nil }); err != nil {
		t.Fatal(err)
	}
	rejected := make(chan error, 1)
	go func() {
		rejected <- q.AddFunc(func(context.Context) error { t.Error("rejected task ran"); return nil })
	}()
	cause := errors.New("shutdown")
	cancel(cause)
	select {
	case err := <-rejected:
		if !errors.Is(err, cause) && !errors.Is(err, ErrQueueClosed) {
			t.Errorf("blocked Add = %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("cancellation did not unblock Add")
	}
	r := queueWait(t, q)
	if r.Total != 2 || r.Failed != 1 || r.Skipped != 1 || !errors.Is(r.Cause, cause) {
		t.Errorf("summary = %+v", r)
	}
}

func TestQueueRateWaitIsCancelable(t *testing.T) {
	t.Parallel()
	first := make(chan struct{})
	var results []Result[int]
	q := NewQueue[int](context.Background(), WithLimit(1), WithRateLimit(0.001), WithJitter(time.Nanosecond)).OnResult(func(r Result[int]) {
		results = append(results, r)
		if r.Index == 0 {
			close(first)
		}
	})
	defer q.Stop()
	if err := q.AddFunc(func(context.Context) (int, error) { return 1, nil }); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, first)
	if err := q.AddFunc(func(context.Context) (int, error) { t.Error("rate limiter issued another token"); return 2, nil }); err != nil {
		t.Fatal(err)
	}
	q.Stop()
	r := queueWait(t, q)
	if r.Total != 2 || r.Succeeded != 1 || r.Skipped != 1 || len(results) != 2 || r.Dropped != 0 {
		t.Errorf("summary = %+v, callbacks = %d", r, len(results))
	}
}

func TestQueueHandlerReplacementDoesNotHoldLock(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var first, second int
	q := NewErrQueue(context.Background(), WithLimit(1)).OnResult(func(Result[Void]) { first++; close(entered); <-release })
	defer q.Stop()
	if err := q.AddFunc(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, entered)
	changed := make(chan struct{})
	go func() { q.OnResult(func(Result[Void]) { second++ }); close(changed) }()
	queueSignal(t, changed)
	if err := q.AddFunc(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	q.Close()
	select {
	case <-q.Done():
		t.Fatal("queue finished before its callback returned")
	default:
	}
	close(release)
	queueWait(t, q)
	if first != 1 || second != 1 {
		t.Errorf("callback counts = %d/%d", first, second)
	}
}

func TestQueueCancellationKeepsAllCallbacks(t *testing.T) {
	t.Parallel()
	entered, release := make(chan struct{}), make(chan struct{})
	var results []Result[int]
	q := NewQueue[int](context.Background(), WithLimit(1)).OnResult(func(r Result[int]) {
		if r.Index == 0 {
			close(entered)
			<-release
		}
		results = append(results, r)
	})
	defer q.Stop()
	if err := q.AddFunc(func(context.Context) (int, error) { return 0, nil }); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, entered)
	// Fill the output buffer, collector, worker and input buffer while the
	// first callback is paused. Cancellation must not discard any of them.
	for i := 1; i <= 4; i++ {
		if err := q.AddFunc(func(context.Context) (int, error) { return i, nil }); err != nil {
			t.Fatal(err)
		}
	}
	q.Stop()
	select {
	case <-q.Done():
		t.Fatal("queue finished before callback returned")
	default:
	}
	close(release)
	r := queueWait(t, q)
	if r.Total != 5 || len(results) != 5 || r.Dropped != 0 || r.Succeeded+r.Failed+r.Skipped != 5 {
		t.Errorf("summary = %+v, callbacks = %d", r, len(results))
	}
	seen := make(map[int]bool)
	for _, res := range results {
		if seen[res.Index] {
			t.Errorf("duplicate callback for %d", res.Index)
		}
		seen[res.Index] = true
	}
}

func TestQueueCanStopFromResultHandler(t *testing.T) {
	t.Parallel()
	q := NewErrQueue(context.Background(), WithLimit(1))
	q.OnResult(func(Result[Void]) { q.Stop() })
	if err := q.AddFunc(func(context.Context) error { return nil }); err != nil {
		t.Fatal(err)
	}
	queueSignal(t, q.Done())
	r := q.CloseAndWait()
	if r.Total != 1 || r.Succeeded != 1 || !errors.Is(r.Cause, ErrStopped) {
		t.Errorf("summary = %+v", r)
	}
}

func TestQueueConcurrentAddAndClose(t *testing.T) {
	t.Parallel()
	for attempt := 0; attempt < 20; attempt++ {
		q := NewErrQueue(context.Background(), WithLimit(4))
		var accepted, callbacks atomic.Int64
		q.OnResult(func(Result[Void]) { callbacks.Add(1) })
		closeNow := make(chan struct{})
		var producers sync.WaitGroup
		for producer := 0; producer < 8; producer++ {
			producers.Add(1)
			go func() {
				defer producers.Done()
				for i := 0; i < 50; i++ {
					if err := q.AddFunc(func(context.Context) error { return nil }); err != nil {
						if !errors.Is(err, ErrQueueClosed) {
							t.Errorf("Add = %v", err)
						}
						return
					}
					if accepted.Add(1) == 10 {
						close(closeNow)
					}
				}
			}()
		}
		queueSignal(t, closeNow)
		var closers sync.WaitGroup
		for i := 0; i < 4; i++ {
			closers.Add(1)
			go func() { defer closers.Done(); q.CloseAndWait() }()
		}
		producers.Wait()
		closers.Wait()
		r := q.CloseAndWait()
		if r.Total != int(accepted.Load()) || r.Succeeded != r.Total || callbacks.Load() != int64(r.Total) || r.Dropped != 0 {
			t.Errorf("summary = %+v, accepted %d, callbacks %d", r, accepted.Load(), callbacks.Load())
		}
	}
}

func TestQueueEmptyAndAlreadyCanceled(t *testing.T) {
	t.Parallel()
	q := NewErrQueue(context.Background(), WithLimit(1))
	if r := queueWait(t, q); r.Status != StatusEmpty || r.Total != 0 || r.Err() != nil {
		t.Errorf("empty queue = %+v", r)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	q = NewErrQueue(ctx, WithLimit(1))
	if err := q.Add(nil); !errors.Is(err, context.Canceled) && !errors.Is(err, ErrQueueClosed) {
		t.Errorf("Add = %v", err)
	}
	if r := queueWait(t, q); r.Total != 0 || r.Status != StatusCanceled {
		t.Errorf("canceled queue = %+v", r)
	}
}

func TestQueueDoesNotLeakGoroutines(t *testing.T) {
	before := runtime.NumGoroutine()
	for i := 0; i < 20; i++ {
		q := NewErrQueue(context.Background(), WithLimit(4))
		if err := q.AddFunc(func(context.Context) error { return nil }); err != nil {
			t.Fatal(err)
		}
		queueWait(t, q)
		q = NewErrQueue(context.Background(), WithLimit(4))
		q.Stop()
		queueWait(t, q)
	}
	waitForGoroutines(t, before)
}

func queueSignal(t *testing.T, signal <-chan struct{}) {
	t.Helper()
	select {
	case <-signal:
	case <-time.After(3 * time.Second):
		t.Fatal("queue operation did not finish")
	}
}

func queueWait(t *testing.T, q interface {
	Close()
	Done() <-chan struct{}
	CloseAndWait() StreamReport
}) StreamReport {
	t.Helper()
	q.Close()
	queueSignal(t, q.Done())
	return q.CloseAndWait()
}
