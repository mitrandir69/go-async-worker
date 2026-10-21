package asyncworker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// TestErrKeepsPercentVerbsLiteral covers the old formatting bug: the
// errors were joined with fmt.Errorf and a non-constant format string,
// so any error message containing a percent verb came back mangled.
func TestErrKeepsPercentVerbsLiteral(t *testing.T) {
	t.Parallel()

	const message = "boom %s and 100% done, %d left"

	pool := NewErrPool()
	pool.Add(Do(func(context.Context) error { return errors.New(message) }))

	got := pool.Run(context.Background()).Err().Error()

	if !strings.Contains(got, message) {
		t.Errorf("Err() = %q, want it to contain %q", got, message)
	}
	if strings.Contains(got, "MISSING") || strings.Contains(got, "%!") {
		t.Errorf("Err() = %q, want no formatting artefacts", got)
	}
}

// TestErrPreservesWrapping covers the old lossy reporting: errors were
// flattened to strings, so errors.Is and errors.As stopped working and
// there was no way to tell which task had failed.
func TestErrPreservesWrapping(t *testing.T) {
	t.Parallel()

	notFound := errors.New("not found")

	pool := New[int](WithLimit(2))
	pool.AddFunc(func(context.Context) (int, error) { return 1, nil })
	pool.AddFunc(func(context.Context) (int, error) {
		return 0, fmt.Errorf("fetch user: %w", notFound)
	})

	err := pool.Run(context.Background()).Err()

	if !errors.Is(err, notFound) {
		t.Errorf("errors.Is(err, notFound) = false, want true (err = %v)", err)
	}

	var taskErr *TaskError
	if !errors.As(err, &taskErr) {
		t.Fatalf("errors.As(err, *TaskError) = false, want true (err = %v)", err)
	}
	if taskErr.Index != 1 {
		t.Errorf("TaskError.Index = %d, want 1", taskErr.Index)
	}
	if !errors.Is(taskErr.Unwrap(), notFound) {
		t.Errorf("TaskError.Unwrap() = %v, want it to wrap notFound", taskErr.Unwrap())
	}
}

func TestErrJoinsEveryFailure(t *testing.T) {
	t.Parallel()

	first, second := errors.New("first"), errors.New("second")

	pool := NewErrPool(WithLimit(1))
	pool.Add(Do(func(context.Context) error { return first }))
	pool.Add(Do(func(context.Context) error { return second }))

	err := pool.Run(context.Background()).Err()

	if !errors.Is(err, first) || !errors.Is(err, second) {
		t.Errorf("Err() = %v, want it to wrap both failures", err)
	}
}

func TestErrIsNilWithoutFailures(t *testing.T) {
	t.Parallel()

	pool := NewErrPool()
	pool.Add(Do(func(context.Context) error { return nil }))

	if err := pool.Run(context.Background()).Err(); err != nil {
		t.Errorf("Err() = %v, want nil", err)
	}
}

func TestTaskErrorMessage(t *testing.T) {
	t.Parallel()

	err := &TaskError{Index: 7, Err: errors.New("disk is full")}

	if got, want := err.Error(), "task 7: disk is full"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestPanicErrorMessage(t *testing.T) {
	t.Parallel()

	err := &PanicError{Value: 42}

	if got, want := err.Error(), "panic: 42"; got != want {
		t.Errorf("Error() = %q, want %q", got, want)
	}
}

func TestValuesAndFailed(t *testing.T) {
	t.Parallel()

	report := Report[string]{
		Succeeded: 2,
		Results: []Result[string]{
			{Index: 0, Value: "a"},
			{Index: 1, Err: &TaskError{Index: 1, Err: errors.New("nope")}},
			{Index: 2, Value: "c"},
		},
	}

	if got, want := report.Values(), []string{"a", "c"}; !slices.Equal(got, want) {
		t.Errorf("Values() = %v, want %v", got, want)
	}
	failed := report.Failed()
	if len(failed) != 1 || failed[0].Index != 1 {
		t.Errorf("Failed() = %v, want just task 1", failed)
	}
}

func TestDuration(t *testing.T) {
	t.Parallel()

	start := time.Now()
	report := Report[int]{StartedAt: start, EndedAt: start.Add(1500 * time.Millisecond)}

	if got, want := report.Duration(), 1500*time.Millisecond; got != want {
		t.Errorf("Duration() = %v, want %v", got, want)
	}
}

// TestReportJSON checks that a report survives encoding/json, which a
// plain error value does not.
func TestReportJSON(t *testing.T) {
	t.Parallel()

	pool := New[int](WithLimit(1), WithLabel("json"))
	pool.AddFunc(func(context.Context) (int, error) { return 11, nil })
	pool.AddFunc(func(context.Context) (int, error) { return 0, errors.New("nope") })

	raw, err := json.Marshal(pool.Run(context.Background()))
	if err != nil {
		t.Fatalf("json.Marshal: %v", err)
	}

	var decoded struct {
		Status    string `json:"status"`
		Label     string `json:"label"`
		Total     int    `json:"total"`
		Succeeded int    `json:"succeeded"`
		Results   []struct {
			Index int    `json:"index"`
			Value int    `json:"value"`
			Error string `json:"error"`
		} `json:"results"`
	}
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("json.Unmarshal: %v", err)
	}

	if decoded.Status != string(StatusFailed) || decoded.Label != "json" {
		t.Errorf("status/label = %q/%q, want %q/%q",
			decoded.Status, decoded.Label, StatusFailed, "json")
	}
	if decoded.Total != 2 || decoded.Succeeded != 1 {
		t.Errorf("total/succeeded = %d/%d, want 2/1", decoded.Total, decoded.Succeeded)
	}
	if decoded.Results[0].Value != 11 || decoded.Results[0].Error != "" {
		t.Errorf("results[0] = %+v, want value 11 and no error", decoded.Results[0])
	}
	if want := "task 1: nope"; decoded.Results[1].Error != want {
		t.Errorf("results[1].error = %q, want %q", decoded.Results[1].Error, want)
	}
}
