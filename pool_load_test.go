//go:build integration

package asyncworker

import (
	"context"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// TestHTTPLoad is opt-in and intentionally sequential: competing scenarios
// would contaminate timing. All requests target a fresh loopback HTTP server.
func TestHTTPLoad(t *testing.T) {
	runHTTPLoad(t, 400, []httpLoadScenario{
		{"limit", nil},
		{"jitter", []Option{WithJitter(50 * time.Millisecond)}},
		{"rate", []Option{WithRateLimit(200)}},
		{"rate_jitter", []Option{WithRateLimit(200), WithJitter(50 * time.Millisecond)}},
	})
}

// TestHTTPLoad30Seconds issues an initial burst of 16 and 6,000 replenished
// tokens at 200/s, keeping the pool busy for approximately 30 seconds.
func TestHTTPLoad30Seconds(t *testing.T) {
	runHTTPLoad(t, 6016, []httpLoadScenario{
		{"sustained_rate_jitter", []Option{WithRateLimit(200), WithJitter(50 * time.Millisecond)}},
	})
}

type httpLoadScenario struct {
	name string
	opts []Option
}

func runHTTPLoad(t *testing.T, tasks int, scenarios []httpLoadScenario) {
	t.Helper()
	const workers = 16
	const serverDelay = 5 * time.Millisecond
	t.Logf("environment go=%s os=%s arch=%s GOMAXPROCS=%d tasks=%d workers=%d server_delay=%s",
		runtime.Version(), runtime.GOOS, runtime.GOARCH, runtime.GOMAXPROCS(0), tasks, workers, serverDelay)
	for _, tt := range scenarios {
		t.Run(tt.name, func(t *testing.T) {
			var active, peak atomic.Int64
			var mu sync.Mutex
			arrivals := make([]time.Time, 0, tasks)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				n := active.Add(1)
				defer active.Add(-1)
				for old := peak.Load(); n > old; old = peak.Load() {
					if peak.CompareAndSwap(old, n) {
						break
					}
				}
				mu.Lock()
				arrivals = append(arrivals, time.Now())
				mu.Unlock()
				timer := time.NewTimer(serverDelay)
				defer timer.Stop()
				select {
				case <-timer.C:
					w.WriteHeader(http.StatusNoContent)
				case <-r.Context().Done():
				}
			}))
			defer server.Close()
			// Reuse connections without imposing a second concurrency limit.
			transport := &http.Transport{MaxIdleConns: workers, MaxIdleConnsPerHost: workers}
			defer transport.CloseIdleConnections()
			client := &http.Client{Transport: transport, Timeout: 5 * time.Second}
			pool := New[time.Duration](append([]Option{WithLimit(workers)}, tt.opts...)...)
			burst := 0
			if pool.limiter != nil {
				burst = int(pool.limiter.burst)
			}
			t.Logf("settings jitter=%s rate=%g automatic_burst=%d", pool.cfg.jitter, pool.cfg.rate, burst)
			for i := 0; i < tasks; i++ {
				pool.AddFunc(func(ctx context.Context) (time.Duration, error) {
					start := time.Now()
					req, err := http.NewRequestWithContext(ctx, http.MethodGet, server.URL, nil)
					if err != nil {
						return 0, err
					}
					resp, err := client.Do(req)
					if err != nil {
						return 0, err
					}
					if err := resp.Body.Close(); err != nil {
						return 0, err
					}
					if resp.StatusCode != http.StatusNoContent {
						return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
					}
					return time.Since(start), nil
				})
			}
			ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
			defer cancel()
			report := pool.Run(ctx)
			if report.Status != StatusDone || report.Succeeded != tasks {
				t.Fatalf("incomplete load: %s, succeeded %d/%d: %v", report.Status, report.Succeeded, tasks, report.Err())
			}
			latencies := report.Values()
			sort.Slice(latencies, func(i, j int) bool { return latencies[i] < latencies[j] })
			mu.Lock()
			starts := append([]time.Time(nil), arrivals...)
			mu.Unlock()
			sort.Slice(starts, func(i, j int) bool { return starts[i].Before(starts[j]) })
			// Peak arrivals in any half-open rolling 100ms window, not fixed bins.
			peakWindow, left := 0, 0
			for right := range starts {
				for starts[right].Sub(starts[left]) >= 100*time.Millisecond {
					left++
				}
				if n := right - left + 1; n > peakWindow {
					peakWindow = n
				}
			}
			if got := peak.Load(); got > workers {
				t.Errorf("peak handlers = %d, limit = %d", got, workers)
			}
			if len(starts) != tasks {
				t.Errorf("server received %d requests, want %d", len(starts), tasks)
			}
			// Only complete seconds after the initial second are included,
			// so the startup burst and a partial final second do not skew this.
			seconds := make([]int, int(report.Duration()/time.Second))
			for _, arrival := range starts {
				bucket := int(arrival.Sub(report.StartedAt) / time.Second)
				if bucket >= 0 && bucket < len(seconds) {
					seconds[bucket]++
				}
			}
			if len(seconds) > 1 {
				low, high, total := seconds[1], seconds[1], 0
				for _, count := range seconds[1:] {
					low = min(low, count)
					high = max(high, count)
					total += count
				}
				t.Logf("STEADY complete_seconds=%d mean_rps=%.3f min_requests_per_second=%d max_requests_per_second=%d",
					len(seconds)-1, float64(total)/float64(len(seconds)-1), low, high)
			}
			t.Logf("LOAD scenario=%s tasks=%d succeeded=%d errors=%d elapsed_ms=%.3f rps=%.3f http_p50_ms=%.3f http_p95_ms=%.3f peak_handlers=%d peak_100ms=%d",
				tt.name, tasks, report.Succeeded, tasks-report.Succeeded, float64(report.Duration())/float64(time.Millisecond),
				float64(tasks)/report.Duration().Seconds(), float64(latencies[int(math.Ceil(0.50*float64(tasks)))-1])/float64(time.Millisecond),
				float64(latencies[int(math.Ceil(0.95*float64(tasks)))-1])/float64(time.Millisecond), peak.Load(), peakWindow)
		})
	}
}
