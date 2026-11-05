// Command failfast runs a finite validation checklist and skips later checks
// when one fails. A custom Task type keeps each check's configuration together.
package main

import (
	"context"
	"errors"
	"fmt"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

type check struct {
	name string
	err  error
}

func (c check) Run(ctx context.Context) (string, error) {
	if err := ctx.Err(); err != nil {
		return "", err
	}
	if c.err != nil {
		return "", fmt.Errorf("check %s: %w", c.name, c.err)
	}
	return c.name + " ok", nil
}

func main() {
	unavailable := errors.New("database unavailable")
	pool := asyncworker.New[string](
		asyncworker.WithLimit(1), // Makes the validation order deterministic.
		asyncworker.WithStopOnFirstError(),
	)
	pool.Add(
		check{name: "configuration"},
		check{name: "database", err: unavailable},
		check{name: "cache"},
		check{name: "storage"},
	)
	report := pool.Run(context.Background())
	fmt.Printf("%s: %d/%d succeeded\n", report.Status, report.Succeeded, report.Total)
	for _, result := range report.Results {
		if result.Err == nil {
			fmt.Println(result.Index, result.Value)
			continue
		}
		fmt.Printf("%d: %v; skipped=%v\n", result.Index, result.Err, errors.Is(result.Err, asyncworker.ErrNotRun))
	}
	fmt.Println("original error preserved:", errors.Is(report.Err(), unavailable))
	fmt.Println("fail-fast cause preserved:", errors.Is(report.Err(), asyncworker.ErrFailFast))
}
