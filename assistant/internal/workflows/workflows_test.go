package workflows

import (
	"context"
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"assistant/internal/tasks"
)

// fakeClock records the waits it is asked for and returns at once.
type fakeClock struct {
	slept []time.Duration
	err   error
}

func (f *fakeClock) Sleep(_ context.Context, d time.Duration) error {
	f.slept = append(f.slept, d)
	return f.err
}

func task(workflow, payload string) tasks.Task {
	return tasks.Task{ID: "t1", WorkflowID: workflow, Payload: json.RawMessage(payload)}
}

func TestNoopReturnsThePayloadAsTheResult(t *testing.T) {
	clock := &fakeClock{}
	r := New(clock)
	for _, payload := range []string{`{}`, `{"text":"héllo","n":[1,2,{"a":null}]}`, `{"nested":{"deep":true}}`} {
		got, err := r.Run(context.Background(), task("noop", payload))
		if err != nil || string(got) != payload {
			t.Errorf("payload %s: result %s, err %v", payload, got, err)
		}
	}
	if len(clock.slept) != 0 {
		t.Errorf("without sleep_ms noop must not wait: %v", clock.slept)
	}
}

func TestNoopWaitsForSleepMS(t *testing.T) {
	clock := &fakeClock{}
	r := New(clock)
	got, err := r.Run(context.Background(), task("noop", `{"sleep_ms": 250, "keep": "me"}`))
	if err != nil || !strings.Contains(string(got), `"keep": "me"`) {
		t.Fatalf("result %s, err %v", got, err)
	}
	if len(clock.slept) != 1 || clock.slept[0] != 250*time.Millisecond {
		t.Errorf("slept %v", clock.slept)
	}
}

func TestNoopCapsSleepMS(t *testing.T) {
	for name, payload := range map[string]string{
		"just over": `{"sleep_ms": 10001}`,
		"a minute":  `{"sleep_ms": 60000}`,
		"huge":      `{"sleep_ms": 9223372036854775807}`,
	} {
		clock := &fakeClock{}
		if _, err := New(clock).Run(context.Background(), task("noop", payload)); err != nil {
			t.Errorf("%s: %v", name, err)
		}
		if len(clock.slept) != 1 || clock.slept[0] != MaxSleep {
			t.Errorf("%s: slept %v, want the cap %v", name, clock.slept, MaxSleep)
		}
	}
	clock := &fakeClock{}
	_, _ = New(clock).Run(context.Background(), task("noop", `{"sleep_ms": 10000}`))
	if clock.slept[0] != 10*time.Second {
		t.Errorf("exactly the cap is allowed as asked: %v", clock.slept)
	}
	clock = &fakeClock{}
	_, _ = New(clock).Run(context.Background(), task("noop", `{"sleep_ms": 0}`))
	if len(clock.slept) != 1 || clock.slept[0] != 0 {
		t.Errorf("zero is a valid wait: %v", clock.slept)
	}
}

func TestNoopTreatsNullSleepAsAbsent(t *testing.T) {
	clock := &fakeClock{}
	if _, err := New(clock).Run(context.Background(), task("noop", `{"sleep_ms": null}`)); err != nil || len(clock.slept) != 0 {
		t.Errorf("err %v, slept %v", err, clock.slept)
	}
}

func TestNoopRejectsABadSleepMS(t *testing.T) {
	for _, payload := range []string{`{"sleep_ms": "5"}`, `{"sleep_ms": -1}`, `{"sleep_ms": 1.5}`, `{"sleep_ms": true}`, `{"sleep_ms": [1]}`, `{"sleep_ms": {}}`, `{"sleep_ms": 1e3}`} {
		clock := &fakeClock{}
		_, err := New(clock).Run(context.Background(), task("noop", payload))
		if err == nil || !strings.Contains(err.Error(), "sleep_ms must be a whole number of milliseconds") {
			t.Errorf("%s: err = %v", payload, err)
		}
		if len(clock.slept) != 0 {
			t.Errorf("%s: a rejected payload must not wait", payload)
		}
	}
}

func TestNoopRejectsAPayloadThatIsNotAnObject(t *testing.T) {
	for _, payload := range []string{`[]`, `"x"`, `3`, `not json`, ``} {
		if _, err := New(&fakeClock{}).Run(context.Background(), task("noop", payload)); err == nil {
			t.Errorf("%q should fail", payload)
		}
	}
}

func TestNoopStopsWaitingWhenTheContextEnds(t *testing.T) {
	r := New(&fakeClock{err: context.Canceled})
	_, err := r.Run(context.Background(), task("noop", `{"sleep_ms": 5000}`))
	if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "interrupted") {
		t.Errorf("err = %v", err)
	}
}

func TestUnknownWorkflowFailsWithTheExactMessage(t *testing.T) {
	r := New(&fakeClock{})
	for _, id := range []string{"nope", "NOOP", "noop ", "learn-topic", "../etc"} {
		_, err := r.Run(context.Background(), task(id, `{}`))
		if err == nil || err.Error() != "unknown workflow: "+id {
			t.Errorf("%q: err = %v", id, err)
		}
	}
}

func TestNamesListsTheBuiltIns(t *testing.T) {
	names := New(&fakeClock{}).Names()
	sort.Strings(names)
	if strings.Join(names, ",") != "noop" {
		t.Errorf("names = %v", names)
	}
}

func TestRealClockSleepsAndStopsOnCancel(t *testing.T) {
	start := time.Now()
	if err := (RealClock{}).Sleep(context.Background(), 20*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) < 15*time.Millisecond {
		t.Errorf("returned after %v", time.Since(start))
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	start = time.Now()
	if err := (RealClock{}).Sleep(ctx, time.Hour); !errors.Is(err, context.Canceled) || time.Since(start) > time.Second {
		t.Errorf("err = %v after %v", err, time.Since(start))
	}
}
