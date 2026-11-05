// Command queue_results parses values concurrently and collects successes and
// errors in a serialized OnResult callback, without a mutex.
package main

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strconv"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	inputs := []string{"12", "7", "oops", "25"}
	sum := 0
	failures := make(map[int]error)
	queue := asyncworker.NewQueue[int](context.Background(), asyncworker.WithLimit(3)).
		OnResult(func(result asyncworker.Result[int]) {
			// Only this callback writes these variables; calls are serialized.
			if result.Err != nil {
				failures[result.Index] = result.Err
				return
			}
			sum += result.Value
		})
	defer queue.Stop()
	for _, raw := range inputs {
		if err := queue.AddFunc(func(ctx context.Context) (int, error) {
			if err := ctx.Err(); err != nil {
				return 0, err
			}
			value, err := strconv.Atoi(raw)
			if err != nil {
				return 0, fmt.Errorf("parse %q: %w", raw, err)
			}
			return value, nil
		}); err != nil {
			return err
		}
	}

	report := queue.CloseAndWait() // Callback writes are complete before reads.
	fmt.Printf("%s: succeeded=%d failed=%d sum=%d\n", report.Status, report.Succeeded, report.Failed, sum)
	// Results arrive by completion; Index lets us print failures in input order.
	for i := range inputs {
		if err := failures[i]; err != nil {
			fmt.Printf("input %d: %v; syntax error=%v\n", i, err, errors.Is(err, strconv.ErrSyntax))
		}
	}
	// The malformed input is intentional. Unexpected failures still fail the demo.
	if err := report.Err(); err != nil && !errors.Is(err, strconv.ErrSyntax) {
		return err
	}
	return nil
}
