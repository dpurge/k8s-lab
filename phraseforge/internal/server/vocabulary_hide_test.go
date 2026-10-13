package server

import (
	"context"
	"net/http"
	"strings"
	"testing"
)

func (f *vocabAPI) addPhrases(t *testing.T, phrases ...string) {
	t.Helper()
	for _, p := range phrases {
		if rec := f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": p}); rec.Code != http.StatusCreated {
			t.Fatalf("POST %q = %d %s", p, rec.Code, rec.Body)
		}
	}
}

func hiddenFlags(d apiVocabDetail) map[string]bool {
	out := map[string]bool{}
	for _, it := range d.Items {
		out[it.Phrase] = it.Hidden
	}
	return out
}

func TestVocabAPIHideIsPerUserAndRemovesTheItemFromTheCopyText(t *testing.T) {
	f := newVocabAPI(t)
	f.addPhrases(t, "كتاب", "قلم")
	hide := f.itemPath(0) + "/hidden"

	for i := 0; i < 2; i++ { // the student may hide, twice is fine
		if rec := f.do(t, f.studentID, http.MethodPut, hide, nil); rec.Code != http.StatusNoContent {
			t.Fatalf("PUT hidden #%d = %d %s", i+1, rec.Code, rec.Body)
		}
	}

	student, _ := f.detail(t, f.studentID)
	if len(student.Items) != 2 || !student.Items[0].Hidden || student.Items[1].Hidden {
		t.Errorf("student items = %+v, want both listed with only the first hidden", student.Items)
	}
	if strings.Contains(student.Markdown, student.Items[0].Phrase) || !strings.Contains(student.Markdown, student.Items[1].Phrase) {
		t.Errorf("student markdown = %q, want the hidden phrase left out and the other kept", student.Markdown)
	}
	admin, _ := f.detail(t, f.adminID)
	if admin.Items[0].Hidden || admin.Items[1].Hidden || !strings.Contains(admin.Markdown, admin.Items[0].Phrase) {
		t.Errorf("admin is unaffected by the student's Hide, got items %+v markdown %q", admin.Items, admin.Markdown)
	}

	if rec := f.do(t, f.studentID, http.MethodDelete, hide, nil); rec.Code != http.StatusNoContent {
		t.Fatalf("DELETE hidden = %d %s", rec.Code, rec.Body)
	}
	student, _ = f.detail(t, f.studentID)
	if student.Items[0].Hidden || !strings.Contains(student.Markdown, student.Items[0].Phrase) {
		t.Errorf("after Unhide the item is visible again, got %+v", student.Items)
	}
}

func TestVocabAPIHideNeedsViewAccessAndAnExistingItem(t *testing.T) {
	f := newVocabAPI(t)
	f.addPhrases(t, "كتاب")
	var outsider int64
	if err := f.srv.db.QueryRow(context.Background(), `INSERT INTO users (username, password_hash) VALUES ('outsider', 'x') RETURNING id`).Scan(&outsider); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]struct {
		user   int64
		path   string
		status int
	}{
		"user without access":  {outsider, f.itemPath(0) + "/hidden", http.StatusNotFound},
		"missing item":         {f.studentID, f.itemPath(99) + "/hidden", http.StatusNotFound},
		"non-numeric position": {f.studentID, f.itemPath(0)[:len(f.itemPath(0))-1] + "x/hidden", http.StatusBadRequest},
	} {
		for _, method := range []string{http.MethodPut, http.MethodDelete} {
			if rec := f.do(t, c.user, method, c.path, nil); rec.Code != c.status {
				t.Errorf("%s %s: status %d, want %d (%s)", name, method, rec.Code, c.status, rec.Body)
			}
		}
	}
	if d, _ := f.detail(t, f.studentID); hiddenFlags(d)["كتاب"] {
		t.Error("a rejected request must not hide anything")
	}
}
