// Command queue submits notifications to a managed error-only queue.
// No task channel, producer goroutine or result reader is required.
package main

import (
	"context"
	"fmt"
	"os"
	"strings"
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
	queue := asyncworker.NewErrQueue(ctx, asyncworker.WithLimit(3))
	defer queue.Stop()

	for _, recipient := range []string{"alice", "bob", "carol", "dave", "eve"} {
		if err := queue.AddFunc(func(ctx context.Context) error {
			return notify(ctx, recipient)
		}); err != nil {
			return fmt.Errorf("submit notification: %w", err)
		}
	}

	report := queue.CloseAndWait() // Also waits for internal result handling.
	fmt.Printf("%s: %d/%d notifications\n", report.Status, report.Succeeded, report.Total)
	return report.Err()
}

func notify(ctx context.Context, recipient string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	// Print locally instead of contacting a notification service.
	fmt.Println("notification for", strings.ToUpper(recipient))
	return nil
}
