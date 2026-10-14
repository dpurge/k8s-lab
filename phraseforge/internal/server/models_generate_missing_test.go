package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"testing"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/ai"
	"phraseforge/internal/models"
)

func TestModelsAPIGenerateMissingQueuesOnlyWhatIsMissing(t *testing.T) {
	f := newVocabAPI(t) // users, roles, ai, jobs on a throwaway schema; models added here
	ctx := context.Background()
	f.srv.models = models.New(f.srv.db)
	f.router.(*chi.Mux).Post("/api/v1/models/{id}/generate-missing-translations", f.srv.apiGenerateMissingModelsTranslations)
	listID, err := f.srv.models.Create(ctx, f.adminID, "Models", "arb", "arab")
	if err != nil {
		t.Fatal(err)
	}
	path := fmt.Sprintf("/api/v1/models/%d/generate-missing-translations", listID)
	enqueued := func(user int64) (int, int) {
		t.Helper()
		rec := f.do(t, user, http.MethodPost, path, nil)
		var body struct{ Enqueued int }
		_ = json.Unmarshal(rec.Body.Bytes(), &body)
		return rec.Code, body.Enqueued
	}
	queuedJobs := func() int {
		var n int
		if err := f.srv.db.QueryRow(ctx, `SELECT count(*) FROM jobs WHERE kind = $1`, ai.KindGenerateModelsItem).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}

	if code, n := enqueued(f.adminID); code != http.StatusAccepted || n != 0 {
		t.Fatalf("empty list: %d, %d queued; want 202 and none", code, n)
	}
	for _, phrase := range []string{"كتاب", "قلم", "باب"} {
		if _, err := f.srv.models.AddItem(ctx, listID, phrase, ""); err != nil {
			t.Fatal(err)
		}
	}
	if err := f.srv.models.SetTranslation(ctx, listID, 0, "en", "book", 3); err != nil {
		t.Fatal(err)
	}

	if code, n := enqueued(f.adminID); code != http.StatusAccepted || n != 2 {
		t.Errorf("one of three translated: %d, %d queued; want 202 and 2", code, n)
	}
	if got := queuedJobs(); got != 2 {
		t.Errorf("%d item jobs in the queue, want 2", got)
	}
	if code, _ := enqueued(f.studentID); code != http.StatusForbidden {
		t.Errorf("student: status %d, want 403", code)
	}
	if got := queuedJobs(); got != 2 {
		t.Errorf("a refused request queued jobs: %d, want 2", got)
	}
	if rec := f.do(t, f.adminID, http.MethodPost, "/api/v1/models/99999/generate-missing-translations", nil); rec.Code != http.StatusNotFound {
		t.Errorf("unknown list: status %d, want 404", rec.Code)
	}
}
