package server

import (
	"encoding/json"
	"testing"
	"time"

	"phraseforge/internal/jobs"
)

func TestJobSummaryCarriesProgressOnlyWhenThereIsSome(t *testing.T) {
	created := time.Date(2026, 10, 5, 12, 30, 0, 0, time.UTC)
	running := jobSummary(jobs.Job{ID: "j1", Kind: "llm_generate", Priority: jobs.PriorityBackground, Status: jobs.StatusRunning, Progress: "3/12", CreatedAt: created})
	if running.Progress != "3/12" || running.Status != "running" || running.CreatedAt != "Oct 5, 2026 · 12:30" {
		t.Errorf("running summary = %+v", running)
	}
	raw, err := json.Marshal(jobSummary(jobs.Job{ID: "j2", Status: jobs.StatusDone, CreatedAt: created}))
	if err != nil {
		t.Fatal(err)
	}
	var fields map[string]any
	if err := json.Unmarshal(raw, &fields); err != nil {
		t.Fatal(err)
	}
	if _, present := fields["progress"]; present {
		t.Errorf("a job without progress still serializes it: %s", raw)
	}
}
