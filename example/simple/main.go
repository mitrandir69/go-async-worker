// Command simple squares a hundred numbers concurrently and prints the
// run's report as JSON.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"os"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

func main() {
	pool := asyncworker.New[int](
		asyncworker.WithLimit(8),
		asyncworker.WithLabel("squares"),
		asyncworker.WithProgress(func(done, total int) {
			if done%25 == 0 {
				fmt.Fprintf(os.Stderr, "%d/%d\n", done, total)
			}
		}),
	)

	for i := 0; i < 100; i++ {
		pool.AddFunc(func(context.Context) (int, error) { return i * i, nil })
	}

	report := pool.Run(context.Background())

	fmt.Printf("%s: %d of %d in %s\n",
		report.Status, report.Succeeded, report.Total, report.Duration())
	fmt.Println("sum of squares:", sum(report.Values()))

	encoded, err := json.MarshalIndent(report, "", "  ")
	if err != nil {
		log.Fatalf("encode report: %v", err)
	}
	fmt.Println(string(encoded))

	if err := report.Err(); err != nil {
		log.Fatalf("run failed: %v", err)
	}
}

func sum(values []int) int {
	var total int
	for _, v := range values {
		total += v
	}
	return total
}
