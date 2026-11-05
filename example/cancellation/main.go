// Command cancellation shows a deadline interrupting tasks and a blocked Add.
// Accepted tasks are counted even when they never start.
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
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	queue := asyncworker.NewErrQueue(ctx, asyncworker.WithLimit(2))
	defer queue.Stop()
	accepted := 0
	for i := 0; i < 30; i++ {
		if err := queue.AddFunc(slowOperation); err != nil {
			fmt.Println("submission stopped:", err)
			break
		}
		accepted++
	}
	report := queue.CloseAndWait()
	fmt.Printf("%s: accepted=%d succeeded=%d failed=%d skipped=%d\n",
		report.Status, accepted, report.Succeeded, report.Failed, report.Skipped)
	fmt.Println("all accepted tasks accounted for:", report.Total == accepted)
	fmt.Println("deadline reached:", errors.Is(report.Err(), context.DeadlineExceeded))
	// The deadline is the scenario being demonstrated, not an unexpected error.
	if err := report.Err(); err != nil && !errors.Is(err, context.DeadlineExceeded) {
		return err
	}
	return nil
}

func slowOperation(ctx context.Context) error {
	// Real network/database operations should receive ctx as well.
	timer := time.NewTimer(40 * time.Millisecond)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
