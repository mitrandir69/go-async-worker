# A simple library for performing async operations.

# Examples

* Simple example
```go
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
```

* Http data collect example
```go
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"github.com/mitrandir69/go-async-worker/src/async_tasks_service/acync_workers"
	"io"
	"net/http"
	"sync"
)

func main() {
	ctx := context.Background()
	worker := acync_workers.NewDefaultAsyncWorker(ctx, "http-data-collect-example-action", 100)

	dataCollectResult := newResult()
	for i := 0; i < 100; i++ {
		worker.AddAction(newHttpDataCollectExampleAction(dataCollectResult, i))
	}
	result := worker.StartAndWait()
	res, _ := json.Marshal(result)
	fmt.Println(string(res))
	fmt.Println(dataCollectResult.res)
}

type result struct {
	mutex *sync.Mutex
	res   []map[string]interface{}
}

func newResult() *result {
	return &result{
		mutex: new(sync.Mutex),
		res:   make([]map[string]interface{}, 0),
	}
}

type httpDataCollectExampleAction struct {
	val  int
	data *result
}

func newHttpDataCollectExampleAction(data *result, val int) *httpDataCollectExampleAction {
	return &httpDataCollectExampleAction{
		val:  val,
		data: data,
	}
}

func (a *httpDataCollectExampleAction) RunAsync() error {
	response, err := http.Get(fmt.Sprintf("http://echo.jsontest.com/key1/%d/key2/%d", a.val, a.val+1))
	if err != nil {
		return err
	}
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("request failed")
	}
	defer response.Body.Close()
	bytes, err := io.ReadAll(response.Body)
	if err != nil {
		return err
	}
	result := make(map[string]interface{})
	err = json.Unmarshal(bytes, &result)
	if err != nil {
		return err
	}
	a.data.mutex.Lock()
	a.data.res = append(a.data.res, result)
	a.data.mutex.Unlock()
	return nil
}

```