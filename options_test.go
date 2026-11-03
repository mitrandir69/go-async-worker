package asyncworker

import (
	"context"
	"math"
	"testing"
	"time"
)

func TestWithRateLimitDisabled(t *testing.T) {
	t.Parallel()
	for _, tt := range []struct {
		name  string
		rate  float64
		burst int
	}{
		{"zero rate", 0, 1},
		{"negative rate", -1, 1},
		{"nan", math.NaN(), 1},
		{"infinity", math.Inf(1), 1},
		{"negative infinity", math.Inf(-1), 1},
		{"zero burst", 10, 0},
		{"negative burst", 10, -1},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			pool := New[int](WithRateLimit(math.SmallestNonzeroFloat64, 1), WithRateLimit(tt.rate, tt.burst))
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
