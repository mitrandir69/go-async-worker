package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mitrandir69/go-async-worker/src/async_tasks_service/acync_workers"
)

func main() {
	ctx := context.Background()
	worker := acync_workers.NewDefaultAsyncWorker(ctx, "simple-example-action", 100)

	for i := 0; i < 100; i++ {
		worker.AddAction(newExampleAction(i))
	}
	result := worker.StartAndWait()
	res, _ := json.Marshal(result)
	fmt.Println(string(res))
}

type exampleAction struct {
	val int
}

func newExampleAction(val int) *exampleAction {
	return &exampleAction{val: val}
}

func (a *exampleAction) RunAsync() error {
	fmt.Println(a.val)
	return nil
}
