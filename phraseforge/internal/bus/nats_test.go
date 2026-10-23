package bus

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	sharedbus "k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
)

// natsURL returns the server the real-NATS test uses, or skips the test.
func natsURL(t *testing.T) string {
	t.Helper()
	url := os.Getenv("NATS_URL")
	if !sharedbus.Enabled(url) {
		t.Skip("NATS_URL is not set: the NATS tests are skipped")
	}
	return url
}

// uniqueApp returns an app name no other test or run shares, so subjects never
// collide on a server other tests use.
func uniqueApp(t *testing.T) string {
	t.Helper()
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	return "t-phraseforge-" + hex.EncodeToString(suffix[:])
}

func TestNATSRoundTripAnswersTaskStatusAndLeavesURLIngestUnanswered(t *testing.T) {
	url := natsURL(t)
	app := uniqueApp(t)
	logger := slog.New(slog.NewTextHandler(&bytes.Buffer{}, nil))
	store := &fakeStore{job: canaryJob()}
	observer := &fakeObserver{}

	service, err := Start(url, app, store, observer, logger)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	t.Cleanup(service.Stop)

	nc, err := sharedbus.Connect(sharedbus.Config{URL: url, Name: app + "-client", Logger: logger})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	t.Cleanup(func() { sharedbus.Close(nc) })
	reg := message.NewRegistry()
	if err := ops.Register(reg); err != nil {
		t.Fatal(err)
	}
	client, err := sharedbus.NewClient(nc, "t-assistant", sharedbus.WithRegistry(reg), sharedbus.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	resp, err := client.Request(ctx, app, ops.TaskStatus, ops.TaskStatusRequest{ID: knownID})
	if err != nil {
		t.Fatalf("task_status: %v", err)
	}
	status, err := ops.ParseTaskStatusResponse(resp.Payload)
	if err != nil {
		t.Fatal(err)
	}
	if status.ID != knownID || status.Kind != "process_text" || status.Status != "running" || status.Step != "clean" || status.Progress != "2/5" {
		t.Errorf("status = %+v", status)
	}
	for _, canary := range []string{canaryPayload, canaryResult, "4242424"} {
		if strings.Contains(string(resp.Payload), canary) {
			t.Errorf("the answer carries %q: %s", canary, resp.Payload)
		}
	}

	_, err = client.Request(ctx, app, ops.TaskStatus, ops.TaskStatusRequest{ID: "11111111-2222-4333-8444-555555555555"})
	var merr *message.Error
	if !errors.As(err, &merr) || merr.Code != message.CodeNotFound {
		t.Errorf("unknown id: error = %v, want not_found", err)
	}

	_, err = client.Request(ctx, app, ops.URLIngest, ops.URLIngestRequest{URL: "https://example.org/page"})
	if !errors.As(err, &merr) || merr.Code != message.CodeUnknownOperation {
		t.Errorf("url_ingest: error = %v, want unknown_operation", err)
	}
	observer.mu.Lock()
	defer observer.mu.Unlock()
	if len(observer.seen) != 2 {
		t.Errorf("observed = %v, want the two task_status requests only", observer.seen)
	}
}
