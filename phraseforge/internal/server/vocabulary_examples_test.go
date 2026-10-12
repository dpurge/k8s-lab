package server

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"gopkg.in/yaml.v3"

	"phraseforge/internal/ai"
	"phraseforge/internal/auth"
	"phraseforge/internal/config"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/jobs"
	"phraseforge/internal/resreview"
	"phraseforge/internal/roles"
	"phraseforge/internal/tags"
	"phraseforge/internal/texts"
	"phraseforge/internal/vocabulary"
)

var vocabAPISchemaCounter atomic.Int64

// vocabAPI is the vocabulary item routes over a real database: the real
// stores on a throwaway schema (PHRASEFORGE_TEST_DSN, skipped without it, as
// in the store tests), an admin who may edit and a student who may only view.
type vocabAPI struct {
	router    http.Handler
	srv       *Server
	adminID   int64
	studentID int64
	listID    int64
}

func newVocabAPI(t *testing.T) *vocabAPI {
	t.Helper()
	dsn := os.Getenv("PHRASEFORGE_TEST_DSN")
	if dsn == "" {
		t.Skip("PHRASEFORGE_TEST_DSN not set")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	schema := fmt.Sprintf("pf_test_%d_%d", os.Getpid(), vocabAPISchemaCounter.Add(1))
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		admin.Close()
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(ctx, "DROP SCHEMA "+schema+" CASCADE")
		admin.Close()
	})
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect to schema: %v", err)
	}
	t.Cleanup(pool.Close)
	schemaSQL, err := os.ReadFile("../db/schema.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(schemaSQL)); err != nil {
		t.Fatalf("apply schema.sql: %v", err)
	}

	f := &vocabAPI{}
	insertUser := func(name string) int64 {
		var id int64
		if err := pool.QueryRow(ctx, `INSERT INTO users (username, password_hash) VALUES ($1, 'x') RETURNING id`, name).Scan(&id); err != nil {
			t.Fatal(err)
		}
		return id
	}
	f.adminID, f.studentID = insertUser("admin"), insertUser("student")
	if _, err := pool.Exec(ctx, `INSERT INTO user_role (user_id, role) VALUES ($1, 'admin')`, f.adminID); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO user_role (user_id, role, language) VALUES ($1, 'student', 'arb')`, f.studentID); err != nil {
		t.Fatal(err)
	}

	// ai and jobs are real but nothing runs a worker: an import only enqueues
	// its backfill rows, so no model is ever called.
	aiCfg := config.Config{Providers: map[string]config.ProviderConfig{"ollama": {}}}
	f.srv = &Server{db: pool, vocab: vocabulary.New(pool), roles: roles.New(pool), tags: tags.New(pool),
		ai: ai.New(pool, aiCfg, nil, nil, nil, nil), jobs: jobs.New(pool),
		texts: texts.New(pool), dialogs: dialogs.New(pool), resReviews: resreview.New(pool)}
	if f.listID, err = f.srv.vocab.Create(ctx, f.adminID, "Test", "arb", "arab"); err != nil {
		t.Fatal(err)
	}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			id, _ := strconv.ParseInt(req.Header.Get("X-Test-User"), 10, 64)
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), userCtxKey, auth.User{ID: id, Locale: "en"})))
		})
	})
	r.Get("/api/v1/vocabulary/export", f.srv.apiExportVocabulary)
	r.Post("/api/v1/vocabulary/import", f.srv.apiImportVocabulary)
	r.Get("/api/v1/vocabulary/{id}", f.srv.apiGetVocabList)
	r.Post("/api/v1/vocabulary/{id}/items", f.srv.apiAddVocabItem)
	r.Put("/api/v1/vocabulary/{id}/items/{position}", f.srv.apiUpdateVocabItem)
	r.Post("/api/v1/vocabulary/{id}/review", f.srv.apiReviewVocabList)
	r.Get("/api/v1/vocabulary/{id}/reviews", f.srv.apiListVocabReviews)
	r.Post("/api/v1/vocabulary/{id}/items/{position}/review", f.srv.apiReviewVocabItem)
	r.Get("/api/v1/vocabulary/{id}/items/{position}/review", f.srv.apiGetVocabItemReview)
	r.Post("/api/v1/vocabulary/{id}/items/{position}/review/apply", f.srv.apiApplyVocabItemReview)
	r.Post("/api/v1/vocabulary/{id}/items/{position}/review/dismiss", f.srv.apiDismissVocabItemReview)
	for _, rt := range []struct{ path, resourceType string }{{"texts", "text"}, {"dialogs", "dialog"}} {
		r.Post("/api/v1/"+rt.path+"/{id}/review/{part}", f.srv.reviewResourceHandler(rt.resourceType, "queue"))
		r.Get("/api/v1/"+rt.path+"/{id}/review/{part}", f.srv.reviewResourceHandler(rt.resourceType, "get"))
		r.Post("/api/v1/"+rt.path+"/{id}/review/{part}/apply", f.srv.reviewResourceHandler(rt.resourceType, "apply"))
		r.Post("/api/v1/"+rt.path+"/{id}/review/{part}/dismiss", f.srv.reviewResourceHandler(rt.resourceType, "dismiss"))
	}
	f.router = r
	return f
}

func (f *vocabAPI) do(t *testing.T, userID int64, method, path string, body any) *httptest.ResponseRecorder {
	t.Helper()
	var buf bytes.Buffer
	if body != nil {
		if s, ok := body.(string); ok {
			buf.WriteString(s)
		} else if err := json.NewEncoder(&buf).Encode(body); err != nil {
			t.Fatal(err)
		}
	}
	req := httptest.NewRequest(method, path, &buf)
	req.Header.Set("X-Test-User", strconv.FormatInt(userID, 10))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *vocabAPI) itemsPath() string { return fmt.Sprintf("/api/v1/vocabulary/%d/items", f.listID) }
func (f *vocabAPI) itemPath(pos int) string {
	return fmt.Sprintf("/api/v1/vocabulary/%d/items/%d", f.listID, pos)
}

// detail GETs the list as userID and returns the decoded body and the raw JSON.
func (f *vocabAPI) detail(t *testing.T, userID int64) (apiVocabDetail, string) {
	t.Helper()
	rec := f.do(t, userID, http.MethodGet, fmt.Sprintf("/api/v1/vocabulary/%d", f.listID), nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET list = %d %s", rec.Code, rec.Body)
	}
	var d apiVocabDetail
	if err := json.Unmarshal(rec.Body.Bytes(), &d); err != nil {
		t.Fatal(err)
	}
	return d, rec.Body.String()
}

func TestVocabAPIAddWithExamplesAndGetReturnsThem(t *testing.T) {
	f := newVocabAPI(t)
	examples := []string{"وَحِيدًا فِي الْبَيْتِ", "رَجُلٌ وَحِيدٌ"}

	rec := f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "وَحِيدًا", "examples": examples})
	if rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	d, _ := f.detail(t, f.adminID)
	if len(d.Items) != 1 || !reflect.DeepEqual(d.Items[0].Examples, examples) {
		t.Fatalf("items = %+v, want the examples back in order with their bytes", d.Items)
	}
}

func TestVocabAPIItemWithoutExamplesGetsAnEmptyArrayNotNull(t *testing.T) {
	f := newVocabAPI(t)
	if rec := f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب"}); rec.Code != http.StatusCreated {
		t.Fatalf("POST = %d %s", rec.Code, rec.Body)
	}
	_, raw := f.detail(t, f.adminID)
	if !strings.Contains(raw, `"examples":[]`) {
		t.Fatalf("response = %s, want \"examples\":[] for an item without examples", raw)
	}
}

func TestVocabAPIUpdateAbsentKeepsEmptyClearsAndValidReplaces(t *testing.T) {
	f := newVocabAPI(t)
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب", "examples": []string{"كِتَابٌ جَدِيدٌ"}})

	// Absent: the examples stay.
	if rec := f.do(t, f.adminID, http.MethodPut, f.itemPath(0), map[string]any{"phrase": "كتاب", "notes": "n"}); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if d, _ := f.detail(t, f.adminID); !reflect.DeepEqual(d.Items[0].Examples, []string{"كِتَابٌ جَدِيدٌ"}) {
		t.Fatalf("examples = %q, want unchanged when the key is absent", d.Items[0].Examples)
	}

	// Valid: replaces.
	if rec := f.do(t, f.adminID, http.MethodPut, f.itemPath(0), map[string]any{"phrase": "كتاب", "examples": []string{"قَرَأَ كِتَابًا"}}); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if d, _ := f.detail(t, f.adminID); !reflect.DeepEqual(d.Items[0].Examples, []string{"قَرَأَ كِتَابًا"}) {
		t.Fatalf("examples = %q, want replaced", d.Items[0].Examples)
	}

	// Empty array: clears.
	if rec := f.do(t, f.adminID, http.MethodPut, f.itemPath(0), `{"phrase":"كتاب","examples":[]}`); rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	if d, _ := f.detail(t, f.adminID); len(d.Items[0].Examples) != 0 {
		t.Fatalf("examples = %q, want cleared by an empty array", d.Items[0].Examples)
	}
}

func TestVocabAPIInvalidExamplesAre400AndWriteNothing(t *testing.T) {
	f := newVocabAPI(t)
	tooMany := []string{"a", "b", "c", "d", "e", "f"}
	tooLong := []string{strings.Repeat("x", vocabulary.MaxExampleChars+1)}

	for name, examples := range map[string][]string{"too many": tooMany, "too long": tooLong} {
		rec := f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب", "examples": examples})
		if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "validation_error") || !strings.Contains(rec.Body.String(), "limit") {
			t.Errorf("POST %s = %d %s, want 400 validation_error naming the limit", name, rec.Code, rec.Body)
		}
	}
	if d, _ := f.detail(t, f.adminID); len(d.Items) != 0 {
		t.Fatalf("items = %+v, want no item created by a refused POST", d.Items)
	}

	// PUT: refused before anything is written, the phrase edit included.
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب", "examples": []string{"كِتَابٌ"}})
	rec := f.do(t, f.adminID, http.MethodPut, f.itemPath(0), map[string]any{"phrase": "كتب", "examples": tooMany})
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("PUT = %d %s, want 400", rec.Code, rec.Body)
	}
	d, _ := f.detail(t, f.adminID)
	if d.Items[0].Phrase != "كتاب" || !reflect.DeepEqual(d.Items[0].Examples, []string{"كِتَابٌ"}) {
		t.Fatalf("item = %+v, want the phrase and examples untouched by a refused PUT", d.Items[0])
	}
}

func TestVocabAPIExamplesGoToThePhraseTheItemLinksAfterAnEdit(t *testing.T) {
	f := newVocabAPI(t)
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتب"})

	rec := f.do(t, f.adminID, http.MethodPut, f.itemPath(0), map[string]any{"phrase": "كتاب", "examples": []string{"كِتَابٌ جَدِيدٌ"}})
	if rec.Code != http.StatusOK {
		t.Fatalf("PUT = %d %s", rec.Code, rec.Body)
	}
	d, _ := f.detail(t, f.adminID)
	if d.Items[0].Phrase != "كتاب" || !reflect.DeepEqual(d.Items[0].Examples, []string{"كِتَابٌ جَدِيدٌ"}) {
		t.Fatalf("item = %+v, want the edited phrase carrying the examples", d.Items[0])
	}
}

func TestVocabAPIStudentCanSeeExamplesButNotChangeThem(t *testing.T) {
	f := newVocabAPI(t)
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب", "examples": []string{"كِتَابٌ"}})

	d, _ := f.detail(t, f.studentID)
	if !reflect.DeepEqual(d.Items[0].Examples, []string{"كِتَابٌ"}) || d.CanEdit {
		t.Fatalf("student view = canEdit %v, examples %q, want read-only with the examples", d.CanEdit, d.Items[0].Examples)
	}
	if rec := f.do(t, f.studentID, http.MethodPut, f.itemPath(0), map[string]any{"phrase": "كتاب", "examples": []string{"x"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("student PUT = %d, want 404 like every other edit", rec.Code)
	}
	if rec := f.do(t, f.studentID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "جديد", "examples": []string{"x"}}); rec.Code != http.StatusNotFound {
		t.Fatalf("student POST = %d, want 404", rec.Code)
	}
	if d, _ := f.detail(t, f.adminID); len(d.Items) != 1 || !reflect.DeepEqual(d.Items[0].Examples, []string{"كِتَابٌ"}) {
		t.Fatalf("items = %+v, want nothing changed by the student", d.Items)
	}
}

// exportList GETs the export of the Arabic lists as the admin and returns the
// list the harness created.
func (f *vocabAPI) exportList(t *testing.T) (apiVocabExportList, string) {
	t.Helper()
	rec := f.do(t, f.adminID, http.MethodGet, "/api/v1/vocabulary/export?language=arb&script=arab", nil)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET export = %d %s", rec.Code, rec.Body)
	}
	var out apiVocabExportResponse
	if err := yaml.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("export is not valid YAML: %v\n%s", err, rec.Body)
	}
	for _, l := range out.Lists {
		if l.ID == f.listID {
			return l, rec.Body.String()
		}
	}
	t.Fatalf("export has no list %d: %s", f.listID, rec.Body)
	return apiVocabExportList{}, ""
}

func (f *vocabAPI) importYAML(t *testing.T, body string) importResult {
	t.Helper()
	rec := f.do(t, f.adminID, http.MethodPost, "/api/v1/vocabulary/import", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("POST import = %d %s", rec.Code, rec.Body)
	}
	var res importResult
	if err := json.Unmarshal(rec.Body.Bytes(), &res); err != nil {
		t.Fatal(err)
	}
	return res
}

// importItemYAML is a one-list import that makes the harness list exactly the
// given item lines (each a ready-made YAML mapping body, indented below).
func (f *vocabAPI) importItemYAML(lines ...string) string {
	var b strings.Builder
	fmt.Fprintf(&b, "lists:\n  - id: %d\n    items:\n", f.listID)
	for _, l := range lines {
		b.WriteString("      - " + strings.ReplaceAll(l, "\n", "\n        ") + "\n")
	}
	return b.String()
}

func TestVocabExportCarriesExamplesAndOmitsEmptyOnes(t *testing.T) {
	f := newVocabAPI(t)
	examples := []string{"وَحِيدًا فِي الْبَيْتِ", "رَجُلٌ وَحِيدٌ"}
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "وَحِيدًا", "examples": examples})
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب"})

	list, raw := f.exportList(t)
	if len(list.Items) != 2 || !reflect.DeepEqual(list.Items[0].Examples, examples) || list.Items[1].Examples != nil {
		t.Fatalf("export items = %+v, want the examples on the first item only, with their bytes", list.Items)
	}
	if strings.Count(raw, "examples:") != 1 {
		t.Fatalf("export has %d examples keys, want 1 (omitted for an item without):\n%s", strings.Count(raw, "examples:"), raw)
	}
}

func TestVocabImportExamplesRoundTripReplaceLeaveClearAndRejectInvalid(t *testing.T) {
	f := newVocabAPI(t)
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "وَحِيدًا", "examples": []string{"وَحِيدًا فِي الْبَيْتِ", "رَجُلٌ وَحِيدٌ"}})
	f.do(t, f.adminID, http.MethodPost, f.itemsPath(), map[string]any{"phrase": "كتاب"})
	list, _ := f.exportList(t)
	id0, id1 := list.Items[0].ID, list.Items[1].ID
	item0 := func(examplesYAML string) string {
		return fmt.Sprintf("id: %d\nphrase: \"وَحِيدًا\"\n%s", id0, examplesYAML)
	}
	item1 := fmt.Sprintf("id: %d\nphrase: \"كتاب\"", id1)
	examplesNow := func() []string { d, _ := f.detail(t, f.adminID); return d.Items[0].Examples }

	// Exporting and importing the export unchanged is a no-op.
	_, exported := f.exportList(t)
	if res := f.importYAML(t, exported); res.Unchanged != 1 || res.Imported != 0 || len(res.Errors) != 0 {
		t.Fatalf("re-importing the export = %+v, want one unchanged list", res)
	}

	// Present: replaces — and a change to the examples alone is a change, not skipped as unchanged.
	if res := f.importYAML(t, f.importItemYAML(item0("examples: [\"قَرَأَ كِتَابًا\"]"), item1)); res.Imported != 1 || len(res.Errors) != 0 {
		t.Fatalf("import replacing examples = %+v, want 1 imported", res)
	}
	if got := examplesNow(); !reflect.DeepEqual(got, []string{"قَرَأَ كِتَابًا"}) {
		t.Fatalf("examples = %q, want replaced", got)
	}
	// Importing the same examples again changes nothing.
	if res := f.importYAML(t, f.importItemYAML(item0("examples: [\"قَرَأَ كِتَابًا\"]"), item1)); res.Unchanged != 1 {
		t.Fatalf("import repeating the examples = %+v, want unchanged", res)
	}
	// Absent: left alone, even while another field changes.
	if res := f.importYAML(t, f.importItemYAML(item0("grammar: n"), item1)); res.Imported != 1 {
		t.Fatalf("import changing the grammar = %+v, want 1 imported", res)
	}
	if got := examplesNow(); !reflect.DeepEqual(got, []string{"قَرَأَ كِتَابًا"}) {
		t.Fatalf("examples = %q, want left alone by an item without the key", got)
	}
	// Invalid: reported with the item and nothing is written.
	res := f.importYAML(t, f.importItemYAML(item0("grammar: n\nexamples: [a, b, c, d, e, f]"), item1))
	if len(res.Errors) != 1 || !strings.Contains(res.Errors[0].Message, "item 0") || !strings.Contains(res.Errors[0].Message, "limit") || res.Imported != 0 {
		t.Fatalf("import with 6 examples = %+v, want one error naming item 0 and the limit", res)
	}
	if got := examplesNow(); !reflect.DeepEqual(got, []string{"قَرَأَ كِتَابًا"}) {
		t.Fatalf("examples = %q, want untouched after the refused import", got)
	}
	// Empty list: clears.
	if res := f.importYAML(t, f.importItemYAML(item0("grammar: n\nexamples: []"), item1)); res.Imported != 1 {
		t.Fatalf("import clearing the examples = %+v, want 1 imported", res)
	}
	if got := examplesNow(); len(got) != 0 {
		t.Fatalf("examples = %q, want cleared by an empty list", got)
	}
}

func TestVocabImportCreatesAListWithItsExamples(t *testing.T) {
	f := newVocabAPI(t)
	body := `lists:
  - title: Imported
    language: arb
    script: arab
    items:
      - phrase: "وَحِيدًا"
        examples: ["وَحِيدًا فِي الْبَيْتِ", "رَجُلٌ وَحِيدٌ"]
`
	if res := f.importYAML(t, body); res.Imported != 1 || len(res.Errors) != 0 {
		t.Fatalf("import = %+v, want one list created", res)
	}
	lists, err := f.srv.vocab.ListAll(context.Background(), nil, true)
	if err != nil {
		t.Fatal(err)
	}
	for _, l := range lists {
		if l.Title != "Imported" {
			continue
		}
		items, err := f.srv.vocab.Items(context.Background(), l.ID)
		if err != nil || len(items) != 1 || !reflect.DeepEqual(items[0].Examples, []string{"وَحِيدًا فِي الْبَيْتِ", "رَجُلٌ وَحِيدٌ"}) {
			t.Fatalf("items = %+v (err %v), want the item with both examples", items, err)
		}
		return
	}
	t.Fatal("the imported list was not created")
}
