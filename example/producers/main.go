// Command producers lets independent producers submit work to one managed
// queue. Finish producing before closing admission.
package main

import (
	"context"
	"fmt"
	"os"
	"sync"
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
	const producers, jobsPerProducer = 3, 5
	counts := make([]int, producers)
	queue := asyncworker.NewQueue[int](ctx, asyncworker.WithLimit(4)).
		OnResult(func(result asyncworker.Result[int]) {
			if result.Err == nil {
				counts[result.Value]++
			}
		})
	defer queue.Stop()

	var wg sync.WaitGroup
	var firstError sync.Once
	var submitErr error
	for producer := 0; producer < producers; producer++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for job := 0; job < jobsPerProducer; job++ {
				if err := queue.AddFunc(func(ctx context.Context) (int, error) {
					return producer, ctx.Err()
				}); err != nil {
					firstError.Do(func() { submitErr = err; cancel() })
					return
				}
			}
		}()
	}
	wg.Wait() // All successful submissions happen before CloseAndWait.
	report := queue.CloseAndWait()
	if submitErr != nil {
		return fmt.Errorf("producer: %w", submitErr)
	}
	if err := report.Err(); err != nil {
		return err
	}
	for producer, completed := range counts {
		fmt.Printf("producer %d: %d jobs\n", producer, completed)
	}
	fmt.Printf("%s: %d/%d\n", report.Status, report.Succeeded, report.Total)
	return nil
}
