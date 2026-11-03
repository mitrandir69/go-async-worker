package asyncworker

import (
	"context"
	"math"
	"runtime"
	"testing"
	"time"
)

func TestWithRateLimitDisabled(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		rate float64
	}{
		{"zero rate", 0},
		{"negative rate", -1},
		{"nan", math.NaN()},
		{"infinity", math.Inf(1)},
		{"negative infinity", math.Inf(-1)},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := New[int](WithRateLimit(math.SmallestNonzeroFloat64), WithRateLimit(tt.rate))
			for i := 0; i < 5; i++ {
				pool.AddFunc(func(context.Context) (int, error) { return i, nil })
			}
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if report := pool.Run(ctx); report.Status != StatusDone || report.Succeeded != 5 {
				t.Errorf("invalid rate option did not disable limiting: %+v", report)
			}
		})
	}
}

func TestWithRateLimitAutomaticBurst(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name string
		opts []Option
		want int
	}{
		{"worker limit", []Option{WithLimit(4), WithRateLimit(200)}, 4},
		{"rate limit", []Option{WithLimit(16), WithRateLimit(3)}, 3},
		{"equal limits", []Option{WithLimit(4), WithRateLimit(4)}, 4},
		{"reverse option order", []Option{WithRateLimit(200), WithLimit(4)}, 4},
		{"last worker limit", []Option{WithLimit(16), WithRateLimit(200), WithLimit(2)}, 2},
		{"last rate", []Option{WithLimit(16), WithRateLimit(200), WithRateLimit(3)}, 3},
		{"default workers", []Option{WithRateLimit(math.MaxFloat64)}, runtime.NumCPU()},
		{"fractional rate", []Option{WithLimit(16), WithRateLimit(3.9)}, 3},
		{"subsecond rate", []Option{WithLimit(16), WithRateLimit(0.5)}, 1},
		{"tiny rate", []Option{WithLimit(16), WithRateLimit(math.SmallestNonzeroFloat64)}, 1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := New[int](tt.opts...)
			l := pool.limiter
			if l == nil {
				t.Fatal("rate limiter is disabled")
			}
			// Freeze time to test the initial allowance without timer noise.
			now := l.updated
			for i := 0; i < tt.want; i++ {
				if delay := l.take(now); delay != 0 {
					t.Fatalf("token %d: delay = %v", i, delay)
				}
			}
			if delay := l.take(now); delay <= 0 {
				t.Errorf("more than %d initial tokens", tt.want)
			}
		})
	}
}
