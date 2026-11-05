// Command queue_http fetches local HTTP data with concurrency, rate and jitter
// limits, using a typed managed queue and a serialized result handler.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	asyncworker "github.com/mitrandir69/go-async-worker"
)

type item struct {
	ID   int    `json:"id"`
	Name string `json:"name"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id, err := strconv.Atoi(strings.TrimPrefix(r.URL.Path, "/items/"))
		if err != nil {
			http.Error(w, "invalid item id", http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(item{ID: id, Name: fmt.Sprintf("item-%02d", id)})
	}))
	defer server.Close()
	client := server.Client()
	client.Timeout = 2 * time.Second
	defer client.CloseIdleConnections()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	var items []item
	queue := asyncworker.NewQueue[item](ctx,
		asyncworker.WithLimit(4),
		asyncworker.WithRateLimit(40), // Automatic burst = min(4, 40) = 4.
		asyncworker.WithJitter(20*time.Millisecond),
	).OnResult(func(result asyncworker.Result[item]) {
		if result.Err == nil {
			items = append(items, result.Value)
		}
	})
	defer queue.Stop()
	for id := 0; id < 12; id++ {
		url := fmt.Sprintf("%s/items/%d", server.URL, id)
		if err := queue.AddFunc(func(ctx context.Context) (item, error) {
			return fetch(ctx, client, url)
		}); err != nil {
			return fmt.Errorf("submit fetch: %w", err)
		}
	}
	report := queue.CloseAndWait()
	if err := report.Err(); err != nil {
		return err
	}
	sort.Slice(items, func(i, j int) bool { return items[i].ID < items[j].ID })
	for _, value := range items {
		fmt.Printf("%d: %s\n", value.ID, value.Name)
	}
	fmt.Printf("%s: %d items in %s (40 tokens/s, initial burst 4)\n", report.Status, report.Succeeded, report.Duration().Round(time.Millisecond))
	return nil
}

func fetch(ctx context.Context, client *http.Client, url string) (item, error) {
	var value item
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return value, err
	}
	resp, err := client.Do(req)
	if err != nil {
		return value, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return value, fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return value, err
	}
	if err := json.Unmarshal(body, &value); err != nil {
		return value, err
	}
	return value, nil
}
