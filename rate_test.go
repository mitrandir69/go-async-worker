package asyncworker

import (
	"context"
	"math"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestRateLimiterTokenBucket(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name     string
		elapsed  time.Duration
		consumed int
		want     time.Duration
	}{
		{"empty bucket", 0, 0, 250 * time.Millisecond},
		{"fractional refill", 125 * time.Millisecond, 0, 125 * time.Millisecond},
		{"full token", 250 * time.Millisecond, 0, 0},
		{"consumed refill", 250 * time.Millisecond, 1, 250 * time.Millisecond},
		{"idle refill", 10 * time.Second, 0, 0},
		{"idle second token", 10 * time.Second, 1, 0},
		{"idle refill capped at burst", 10 * time.Second, 2, 250 * time.Millisecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newTaskRateLimiter(4, 2)
			start := l.updated
			l.take(start)
			l.take(start)
			now := start.Add(tt.elapsed)
			for i := 0; i < tt.consumed; i++ {
				l.take(now)
			}
			if got := l.take(now); got != tt.want {
				t.Errorf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRateLimiterConcurrentBurst(t *testing.T) {
	t.Parallel()
	l := newTaskRateLimiter(1, 3)
	now := l.updated
	var admitted atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if l.take(now) == 0 {
				admitted.Add(1)
			}
		}()
	}
	wg.Wait()
	if got := admitted.Load(); got != 3 {
		t.Errorf("admitted = %d, want burst of 3", got)
	}
}

func TestRateLimiterExtremeRates(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		rate float64
		want time.Duration
	}{
		{"fractional", 0.5, 2 * time.Second},
		{"tiny", math.SmallestNonzeroFloat64, time.Duration(math.MaxInt64)},
		{"huge", math.MaxFloat64, time.Nanosecond},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			l := newTaskRateLimiter(tt.rate, 1)
			now := l.updated
			l.take(now)
			if got := l.take(now); got != tt.want {
				t.Errorf("delay = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestRateLimiterCanceledWaitPreservesTokens(t *testing.T) {
	t.Parallel()
	l := newTaskRateLimiter(1, 1)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if l.wait(ctx) {
		t.Fatal("canceled waiter obtained a token")
	}
	if got := l.take(l.updated); got != 0 {
		t.Errorf("canceled waiter consumed a token: delay = %v", got)
	}
}
