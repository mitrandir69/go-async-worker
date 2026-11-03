package asyncworker

import (
	"context"
	"fmt"
	"testing"
)

// BenchmarkPoolThroughput measures Run, including its task snapshot, worker
// startup and report allocation. Pool construction and Add are excluded.
// Keep b.N loops to support the module's minimum Go version (1.22).
func BenchmarkPoolThroughput(b *testing.B) {
	for _, tasks := range []int{1000, 10000} {
		for _, workers := range []int{1, 8, 32} {
			b.Run(fmt.Sprintf("tasks=%d/workers=%d", tasks, workers), func(b *testing.B) {
				pool := New[int](WithLimit(workers))
				for i := 0; i < tasks; i++ {
					pool.AddFunc(func(context.Context) (int, error) { return 1, nil })
				}
				ctx := context.Background()
				b.ReportAllocs()
				b.ResetTimer()
				for n := 0; n < b.N; n++ {
					report := pool.Run(ctx)
					if report.Status != StatusDone || report.Succeeded != tasks {
						b.Fatalf("incomplete run: %s, succeeded %d/%d", report.Status, report.Succeeded, tasks)
					}
				}
				b.StopTimer()
				b.ReportMetric(float64(tasks)*float64(b.N)/b.Elapsed().Seconds(), "tasks/s")
			})
		}
	}
}
