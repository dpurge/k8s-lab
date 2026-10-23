package responder

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/bus"
	"k8s-lab/shared/message"
	"k8s-lab/shared/ops"
	"knowledge/internal/jobs"
	"knowledge/internal/qdrant"
)

// TestRoundTripOverRealNATS runs the whole path (Run, the queue-group
// subscription, the envelopes, a shared/bus Client) for the three operations,
// against the services' fakes. It needs a NATS server and is skipped without one.
// The app name is unique per run, so it never meets another listener.
func TestRoundTripOverRealNATS(t *testing.T) {
	url := os.Getenv("NATS_URL")
	if !bus.Enabled(url) {
		t.Skip("NATS_URL is not set: the NATS tests are skipped")
	}
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	app := "t-knowledge-" + hex.EncodeToString(suffix[:])

	score := 0.5
	jobID := "11111111-1111-4111-8111-111111111111"
	svc := Services{
		Search: &fakeSearcher{items: []qdrant.ListItem{{ID: "d-1", Title: "T", Summary: "S", Tags: []string{"a"}, Score: &score}}},
		Ingest: &fakeIngester{job: jobs.Job{ID: jobID, Status: "running"}},
		Jobs: fakeJobs{byID: map[string]jobs.Job{jobID: {
			ID: jobID, Kind: "ingest", Status: "running", Step: "fetch", SourceText: canary,
			CreatedAt: time.Unix(1_700_000_000, 0), UpdatedAt: time.Unix(1_700_000_030, 0),
		}}},
	}
	obs := &observed{}
	stop := Run(url, app, svc, obs.observe, slog.New(slog.DiscardHandler))
	t.Cleanup(stop)

	nc, err := bus.Connect(bus.Config{URL: url, Name: app + "-client", Logger: slog.New(slog.DiscardHandler)})
	if err != nil {
		t.Fatalf("connect the client: %v", err)
	}
	t.Cleanup(func() { bus.Close(nc) })
	reg := message.NewRegistry()
	if err := ops.Register(reg); err != nil {
		t.Fatal(err)
	}
	client, err := bus.NewClient(nc, "assistant", bus.WithRegistry(reg), bus.WithTimeout(5*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()

	env, err := client.Request(ctx, app, ops.DocumentsSearch, ops.DocumentsSearchRequest{Query: "probe"})
	if err != nil {
		t.Fatalf("documents_search: %v", err)
	}
	found, err := ops.ParseDocumentsSearchResponse(env.Payload)
	if err != nil || len(found.Documents) != 1 || found.Documents[0].URL != "/?knowledge=d-1" {
		t.Errorf("documents_search = %+v, %v", found, err)
	}

	env, err = client.Request(ctx, app, ops.URLIngest, ops.URLIngestRequest{URL: "https://wiki.example/a"})
	if err != nil {
		t.Fatalf("url_ingest: %v", err)
	}
	if got, err := ops.ParseURLIngestResponse(env.Payload); err != nil || got.TaskID != jobID || got.Status != "running" {
		t.Errorf("url_ingest = %+v, %v", got, err)
	}

	env, err = client.Request(ctx, app, ops.TaskStatus, ops.TaskStatusRequest{ID: jobID})
	if err != nil {
		t.Fatalf("task_status: %v", err)
	}
	if got, err := ops.ParseTaskStatusResponse(env.Payload); err != nil || got.Step != "fetch" || got.Kind != "ingest" {
		t.Errorf("task_status = %+v, %v", got, err)
	}
	if strings.Contains(string(env.Payload), canary) {
		t.Errorf("task_status payload carries source text: %s", env.Payload)
	}

	_, err = client.Request(ctx, app, ops.TaskStatus, ops.TaskStatusRequest{ID: "unknown"})
	if coded, ok := err.(*message.Error); !ok || coded.Code != message.CodeNotFound {
		t.Errorf("unknown id error = %v, want not_found", err)
	}
	if len(obs.seen) != 4 {
		t.Errorf("observed = %v, want 4 handled requests", obs.seen)
	}
}
