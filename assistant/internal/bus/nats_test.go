package bus

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"

	"assistant/internal/callctx"
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

// uniqueApp returns an app name no other test or run shares, so the subjects
// never collide on a server others use.
func uniqueApp(t *testing.T, role string) string {
	t.Helper()
	var suffix [4]byte
	if _, err := rand.Read(suffix[:]); err != nil {
		t.Fatal(err)
	}
	return "t-assistant-" + role + "-" + hex.EncodeToString(suffix[:])
}

// TestRoundTripThroughRealNATS sends the three operations through real
// envelopes to responders that answer with canned data.
func TestRoundTripThroughRealNATS(t *testing.T) {
	url := natsURL(t)
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	knowledge, phraseforge := uniqueApp(t, "k"), uniqueApp(t, "p")

	nc, err := sharedbus.Connect(sharedbus.Config{URL: url, Name: "assistant-bus-test", Logger: log})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = sharedbus.Close(nc) })

	// seen holds the context of the last request each responder received.
	seen := make(chan *message.Context, 8)
	status := ops.TaskStatusResponse{ID: "t-1", Kind: "url_ingest", Status: "running", Step: "fetch", CreatedAt: "2026-10-23T09:15:00Z", UpdatedAt: "2026-10-23T09:15:30Z"}
	answer := func(app string, names ...string) {
		reg := message.NewRegistry()
		if err := ops.Register(reg, names...); err != nil {
			t.Fatal(err)
		}
		resp, err := sharedbus.NewResponder(nc, app, reg, log)
		if err != nil {
			t.Fatal(err)
		}
		canned := map[string]any{
			ops.DocumentsSearch: ops.DocumentsSearchResponse{Documents: []ops.Document{{ID: "d-1", Title: "Probes", Summary: "s", Tags: []string{"ops"}, Score: 0.5, URL: "/documents/d-1"}}, Total: 1},
			ops.URLIngest:       ops.URLIngestResponse{TaskID: "t-1", Status: "queued"},
			ops.TaskStatus:      status,
		}
		for _, name := range names {
			payload := canned[name]
			if err := resp.Handle(name, func(_ context.Context, req message.Envelope) (any, error) {
				seen <- req.Context
				return payload, nil
			}); err != nil {
				t.Fatal(err)
			}
		}
		if err := resp.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(resp.Stop)
	}
	answer(knowledge, ops.DocumentsSearch, ops.URLIngest, ops.TaskStatus)
	answer(phraseforge, ops.URLIngest, ops.TaskStatus)

	c, err := Dial(url, log, WithDestinations(map[string]string{Knowledge: knowledge, Phraseforge: phraseforge}))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = c.Close() })

	ctx, cancel := context.WithTimeout(callctx.With(context.Background(), callctx.Info{TaskID: "task-9", UserID: "user-3"}), 10*time.Second)
	defer cancel()

	found, err := c.SearchDocuments(ctx, ops.DocumentsSearchRequest{Query: "probes", Limit: 3})
	if err != nil || len(found.Documents) != 1 || found.Documents[0].ID != "d-1" {
		t.Errorf("SearchDocuments: %+v, %v", found, err)
	}
	if got := <-seen; got == nil || got.TaskID != "task-9" || got.UserID != "user-3" {
		t.Errorf("the responder saw context %+v, want task-9 and user-3", got)
	}
	for _, app := range []string{Knowledge, Phraseforge} {
		ingested, err := c.IngestURL(ctx, app, ops.URLIngestRequest{URL: "https://wiki.example/a", Tags: []string{"ops"}})
		if err != nil || ingested.TaskID != "t-1" || ingested.Status != "queued" {
			t.Errorf("IngestURL %s: %+v, %v", app, ingested, err)
		}
		got, err := c.TaskStatus(ctx, app, "t-1")
		if err != nil || got != status {
			t.Errorf("TaskStatus %s: %+v, %v", app, got, err)
		}
	}

	// A request the responder's schema refuses comes back as invalid_payload
	// from the client's own check, before it is sent.
	_, err = c.SearchDocuments(ctx, ops.DocumentsSearchRequest{Query: strings.Repeat("x", ops.MaxQueryChars+1)})
	wantCode(t, "an over-long query", err, message.CodeInvalidPayload)
}
