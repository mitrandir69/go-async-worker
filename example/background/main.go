// Command background starts a batch without blocking the caller, waits until
// workers are active, then stops it through its handle.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	started := make(chan struct{}, 2)
	pool := asyncworker.NewErrPool(asyncworker.WithLimit(2))
	for i := 0; i < 8; i++ {
		pool.Add(asyncworker.Do(func(ctx context.Context) error {
			started <- struct{}{}
			// Simulate an operation blocked on I/O until canceled.
			<-ctx.Done()
			return ctx.Err()
		}))
	}
	handle := pool.Start(ctx) // Returns while tasks are still running.
	defer handle.Stop()
	for i := 0; i < 2; i++ {
		select {
		case <-started:
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	fmt.Println("two workers active; the caller can do other work here")
	handle.Stop() // Does not wait; running tasks observe context cancellation.
	report := handle.Wait()
	skipped := 0
	for _, result := range report.Results {
		if errors.Is(result.Err, asyncworker.ErrNotRun) {
			skipped++
		}
	}
	fmt.Printf("%s: total=%d skipped=%d\n", report.Status, report.Total, skipped)
	fmt.Println("stopped through handle:", errors.Is(report.Err(), asyncworker.ErrStopped))
	// Stop is intentional. Unexpected cancellation causes fail the demo.
	if !errors.Is(report.Err(), asyncworker.ErrStopped) {
		return fmt.Errorf("expected Stop: %v", report.Err())
	}
	return nil
}
