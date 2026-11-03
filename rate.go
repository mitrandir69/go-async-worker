package asyncworker

import (
	"context"
	"math"
	"sync"
	"time"
)

// taskRateLimiter grants tokens when they are available instead of reserving
// future tokens, so a canceled waiter cannot leave a reservation behind.
type taskRateLimiter struct {
	mu      sync.Mutex
	rate    float64
	burst   float64
	tokens  float64
	updated time.Time
}

func newTaskRateLimiter(rps float64, burst int) *taskRateLimiter {
	return &taskRateLimiter{rate: rps, burst: float64(burst), tokens: float64(burst), updated: time.Now()}
}

// take consumes one token and returns zero, or returns the time until a token
// may be available. Other waiters may take that token first, so callers retry.
func (l *taskRateLimiter) take(now time.Time) time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	if elapsed := now.Sub(l.updated).Seconds(); elapsed > 0 {
		l.tokens = math.Min(l.burst, l.tokens+elapsed*l.rate)
		l.updated = now
	}
	if l.tokens >= 1 {
		l.tokens--
		return 0
	}
	nanos := math.Ceil((1 - l.tokens) / l.rate * float64(time.Second))
	// Very low rates can exceed time.Duration's range. Sleep in bounded
	// chunks and recalculate, while still allowing prompt cancellation.
	if nanos >= float64(math.MaxInt64) {
		return time.Duration(math.MaxInt64)
	}
	if nanos < 1 {
		return time.Nanosecond
	}
	return time.Duration(nanos)
}

func (l *taskRateLimiter) wait(ctx context.Context) bool {
	if l == nil {
		return ctx.Err() == nil
	}
	for ctx.Err() == nil {
		delay := l.take(time.Now())
		if delay == 0 {
			return ctx.Err() == nil
		}
		if !waitTaskDelay(ctx, delay) {
			return false
		}
	}
	return false
}
