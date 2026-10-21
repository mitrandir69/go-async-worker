// Command httpcollect fetches a set of URLs concurrently and collects
// the decoded payloads.
//
// This is the example that motivated the typed API: the old version of
// this library could only report an error per task, so collecting the
// bodies meant threading a shared slice and a sync.Mutex through every
// action. Here the payload is the task's return value and the pool does
// the collecting.
//
// It serves its own requests from an httptest server so that it runs
// offline and always finishes, including the deliberate failure.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"time"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

// payload is what each request returns.
type payload struct {
	Key   int `json:"key"`
	Value int `json:"value"`
}

func main() {
	server := httptest.NewServer(http.HandlerFunc(handle))
	defer server.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	pool := asyncworker.New[payload](
		asyncworker.WithLimit(10),
		asyncworker.WithLabel("http-collect"),
	)
	for i := 0; i < 50; i++ {
		url := fmt.Sprintf("%s/items/%d", server.URL, i)
		pool.AddFunc(func(ctx context.Context) (payload, error) {
			return fetch(ctx, server.Client(), url)
		})
	}

	report := pool.Run(ctx)

	fmt.Printf("%s: %d of %d in %s\n",
		report.Status, report.Succeeded, report.Total, report.Duration())

	// Values() holds only the payloads that came back, in the order the
	// tasks were added. No shared slice, no mutex.
	total := 0
	for _, p := range report.Values() {
		total += p.Value
	}
	fmt.Println("sum of values:", total)

	for _, res := range report.Failed() {
		fmt.Fprintf(os.Stderr, "failed: %v\n", res.Err)
	}

	// The report keeps error values, so a sentinel still matches.
	if errors.Is(report.Err(), errServerSide) {
		fmt.Fprintln(os.Stderr, "at least one request failed server-side")
	}
}

// errServerSide marks a response the server itself rejected.
var errServerSide = errors.New("server-side failure")

func fetch(ctx context.Context, client *http.Client, url string) (payload, error) {
	var out payload

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return out, fmt.Errorf("build request: %w", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return out, fmt.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return out, fmt.Errorf("get %s: %d: %w", url, resp.StatusCode, errServerSide)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return out, fmt.Errorf("read body: %w", err)
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return out, fmt.Errorf("decode body: %w", err)
	}
	return out, nil
}

// handle answers with a small JSON document, and fails every seventh
// request so that the error path shows up in the output.
func handle(w http.ResponseWriter, r *http.Request) {
	var key int
	if _, err := fmt.Sscanf(r.URL.Path, "/items/%d", &key); err != nil {
		http.Error(w, "bad path", http.StatusBadRequest)
		return
	}
	if key%7 == 6 {
		http.Error(w, "unavailable", http.StatusServiceUnavailable)
		return
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(payload{Key: key, Value: key * 10})
}
