package ingest

import (
	"context"
	"errors"
	"testing"
)

// These guard clauses are checked with a zero-value Service (db, jobs,
// translate, generate all nil): the input-shape validation in
// StartURL/StartText runs before any dependency is touched, so a nil
// pointer would panic instead of returning ErrValidation if that ordering
// ever regressed. No live Postgres/Ollama is needed, per the spec's "tests
// cover guard clauses only" scope for this step.

func TestStartURL_EmptyRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	for _, rawURL := range []string{"", "   "} {
		if _, err := svc.StartURL(context.Background(), rawURL, nil); !errors.Is(err, ErrValidation) {
			t.Errorf("StartURL(%q) error = %v, want ErrValidation", rawURL, err)
		}
	}
}

func TestStartURL_BadSchemeOrMissingHostRejectedSynchronously(t *testing.T) {
	svc := &Service{}

	for _, rawURL := range []string{"ftp://example.com/file.txt", "not a url", "http://"} {
		if _, err := svc.StartURL(context.Background(), rawURL, nil); !errors.Is(err, ErrValidation) {
			t.Errorf("StartURL(%q) error = %v, want ErrValidation", rawURL, err)
		}
	}
}

func TestStartText_BadExtensionRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if _, err := svc.StartText(context.Background(), "file.pdf", "some content", nil); !errors.Is(err, ErrValidation) {
		t.Errorf("StartText(bad extension) error = %v, want ErrValidation", err)
	}
}

func TestStartText_EmptyContentRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if _, err := svc.StartText(context.Background(), "file.txt", "", nil); !errors.Is(err, ErrValidation) {
		t.Errorf("StartText(empty content) error = %v, want ErrValidation", err)
	}
}

func TestStartText_InvalidUTF8RejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if _, err := svc.StartText(context.Background(), "file.txt", "bad utf8: \xff\xfe", nil); !errors.Is(err, ErrValidation) {
		t.Errorf("StartText(invalid utf8) error = %v, want ErrValidation", err)
	}
}

func TestUpdateDraft_EmptyFieldsRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if _, err := svc.UpdateDraft(context.Background(), "some-id", "", "summary", "body", nil); !errors.Is(err, ErrValidation) {
		t.Errorf("UpdateDraft(empty title) error = %v, want ErrValidation", err)
	}
}

func TestUpdateDraft_MalformedIDRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if _, err := svc.UpdateDraft(context.Background(), "not-a-uuid", "title", "summary", "body", nil); !errors.Is(err, ErrNotFound) {
		t.Errorf("UpdateDraft(malformed id) error = %v, want ErrNotFound", err)
	}
}

func TestGetDraft_MalformedIDRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if _, err := svc.GetDraft(context.Background(), "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("GetDraft(malformed id) error = %v, want ErrNotFound", err)
	}
}

func TestDiscardDraft_MalformedIDRejectedBeforeAnyDependencyUse(t *testing.T) {
	svc := &Service{}

	if err := svc.DiscardDraft(context.Background(), "not-a-uuid"); err != nil {
		t.Errorf("DiscardDraft(malformed id) error = %v, want nil (idempotent)", err)
	}
}

// fakePromoter stands in for a real Promoter (which would call Qdrant) so
// ApproveDraft's guard clause can be tested without live Postgres or
// Qdrant: called records whether Promote was ever reached, which the test
// below asserts against for a malformed id.
type fakePromoter struct {
	called bool
}

func (f *fakePromoter) Promote(ctx context.Context, title, summary, body string, tags []string) (string, error) {
	f.called = true
	return "fake-id", nil
}

func TestApproveDraft_MalformedIDNeverCallsPromoter(t *testing.T) {
	fp := &fakePromoter{}
	svc := &Service{promoter: fp}

	if _, err := svc.ApproveDraft(context.Background(), "not-a-uuid"); !errors.Is(err, ErrNotFound) {
		t.Errorf("ApproveDraft(malformed id) error = %v, want ErrNotFound", err)
	}
	if fp.called {
		t.Error("ApproveDraft(malformed id) called the promoter; want it never reached")
	}
}

// TestApproveDraft_EmptyFieldsNeverCallsPromoter covers the other half of
// ApproveDraft's guard clauses: a claimed draft with an empty field (e.g.
// one that reached the DB with an empty title because generate.Title
// returned "" on an empty LLM completion) must be rejected by
// validateDraftFields before the promoter is ever reached. This exercises
// approveDraftFields directly against a hand-built Draft value rather than
// going through ApproveDraft/claimDraft: claimDraft's own DELETE...
// RETURNING requires a live Postgres row to produce a Draft in the first
// place, and this codebase's convention (see fakePromoter's own doc
// comment) is not to mock *pgxpool.Pool. approveDraftFields was factored
// out of ApproveDraft specifically so this validate-before-promote
// invariant can still be tested hermetically.
func TestApproveDraft_EmptyFieldsNeverCallsPromoter(t *testing.T) {
	fp := &fakePromoter{}
	svc := &Service{promoter: fp}

	d := Draft{ID: "some-id", Title: "", Summary: "summary", Body: "body"}
	if _, err := svc.approveDraftFields(context.Background(), d); !errors.Is(err, ErrValidation) {
		t.Errorf("approveDraftFields(empty title) error = %v, want ErrValidation", err)
	}
	if fp.called {
		t.Error("approveDraftFields(empty title) called the promoter; want it never reached")
	}
}
