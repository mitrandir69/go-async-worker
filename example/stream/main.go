// Command stream adapts an existing task channel and consumes results while
// tasks arrive. Use a managed Queue when the application does not own a channel.
package main

import (
	"context"
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
	tasks := make(chan asyncworker.Task[int])
	pool := asyncworker.New[int](asyncworker.WithLimit(3))
	handle := pool.Stream(ctx, tasks)
	defer handle.Stop()

	go func() {
		defer close(tasks) // The producer owns and closes input.
		for i := 1; i <= 8; i++ {
			task := asyncworker.TaskFunc[int](func(ctx context.Context) (int, error) {
				return i * i, ctx.Err()
			})
			select {
			case tasks <- task:
			case <-ctx.Done():
				return
			case <-handle.Done():
				return
			}
		}
	}()

	sum := 0
	// Reading concurrently with production prevents result backpressure from
	// blocking the producer. Completion order may differ from input order.
	for result := range handle.Results() {
		if result.Err != nil {
			fmt.Fprintln(os.Stderr, result.Err)
			continue
		}
		fmt.Printf("input %d: %d\n", result.Index, result.Value)
		sum += result.Value
	}
	report := handle.Wait()
	fmt.Printf("%s: %d tasks, sum=%d\n", report.Status, report.Succeeded, sum)
	return report.Err()
}
