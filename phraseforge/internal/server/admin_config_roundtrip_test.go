package server

import (
	"bytes"
	"context"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/ime"
)

func sampleAdminConfig() adminConfigExport {
	timeout := 600
	prompts := make([]ai.Prompt, 0, len(ai.ValidKinds))
	for _, kind := range ai.ValidKinds {
		prompts = append(prompts, ai.Prompt{Kind: kind, SourceLanguage: "cmn", TargetLanguage: kind})
	}
	prompts[0].TimeoutSeconds = &timeout
	prompts[0].ModelTags = []string{"default", "big"}
	prompts[1].ModelTags = []string{"vision"}
	prompts[1].Think = true
	return adminConfigExport{
		Version:    1,
		IMEConfigs: []ime.Config{{Language: "cmn", Script: "hans", SourceIME: "cmn", TranscriptionIME: "cxs", NeedsTranscription: true}},
		LLMPrompts: prompts,
		LanguageSections: []ai.LanguageSections{
			{Language: "cmn", GrammarPrompt: "N = noun\n", TranscriptionPrompt: "Pinyin with tone marks.\n"},
			{Language: "deu", GrammarPrompt: "N m f n\n", DictionaryPrompt: "Nouns with the article.\n"},
		},
	}
}

// TestAdminConfigExportJSONRoundTrip: what the export writes is what the
// import reads back, field for field, with the key names the file format has.
func TestAdminConfigExportJSONRoundTrip(t *testing.T) {
	want := sampleAdminConfig()
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	var keys map[string]json.RawMessage
	if err := json.Unmarshal(raw, &keys); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"version", "ime_configs", "llm_prompts", "language_sections"} {
		if _, ok := keys[key]; !ok {
			t.Errorf("export has no %q key", key)
		}
	}
	var got adminConfigExport
	if err := json.Unmarshal(raw, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("export JSON did not round-trip:\ngot  %+v\nwant %+v", got, want)
	}
}

func testAdminServer(t testing.TB, db *pgxpool.Pool) *Server {
	return &Server{db: db, ai: testAI(t, db)}
}

func importRequest(t *testing.T, body string) *http.Request {
	t.Helper()
	var buf bytes.Buffer
	w := multipart.NewWriter(&buf)
	part, err := w.CreateFormFile("config_file", "phraseforge-admin-config.json")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write([]byte(body)); err != nil {
		t.Fatal(err)
	}
	if err := w.Close(); err != nil {
		t.Fatal(err)
	}
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/config/import", &buf)
	req.Header.Set("Content-Type", w.FormDataContentType())
	return req
}

// TestImportAdminConfigRejectsBadFilesBeforeTouchingTheDatabase: every check
// the import makes before its transaction, so no database is needed.
func TestImportAdminConfigRejectsBadFilesBeforeTouchingTheDatabase(t *testing.T) {
	s := testAdminServer(t, nil)
	cases := map[string]string{
		"not JSON":              `nope`,
		"unsupported version":   `{"version":2}`,
		"unknown kind":          `{"version":1,"llm_prompts":[{"kind":"tag","source_language":"cmn","target_language":"x","prompt":"p"}]}`,
		"unknown tag":           `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title","model_tags":["ghost"]}]}`,
		"blank tag":             `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title","model_tags":[" "]}]}`,
		"tags no model has":     `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title","model_tags":["big","vision"]}]}`,
		"removed provider key":  `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title","provider":"ollama"}]}`,
		"removed model key":     `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title","model":"gemma4:12b"}]}`,
		"blank source language": `{"version":1,"llm_prompts":[{"kind":"title","source_language":" ","target_language":"title"}]}`,
		"blank section lang":    `{"version":1,"language_sections":[{"language":" ","grammar_prompt":"g"}]}`,
		"IME without language":  `{"version":1,"ime_configs":[{"script":"hans"}]}`,
	}
	for name, body := range cases {
		rec := httptest.NewRecorder()
		s.apiImportAdminConfig(rec, importRequest(t, body))
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (body %s)", name, rec.Code, rec.Body.String())
		}
	}
}

// TestAdminConfigExportImportRoundTripDB: import a known configuration,
// export it, import the export, export again. Import replaces every IME
// config, LLM prompt and language section, so it only runs against a database
// that is safe to wipe: PHRASEFORGE_TEST_DB (schema applied) and
// PHRASEFORGE_TEST_DB_REPLACE_ALL=1.
func TestAdminConfigExportImportRoundTripDB(t *testing.T) {
	url := os.Getenv("PHRASEFORGE_TEST_DB")
	if url == "" || os.Getenv("PHRASEFORGE_TEST_DB_REPLACE_ALL") != "1" {
		t.Skip("PHRASEFORGE_TEST_DB and PHRASEFORGE_TEST_DB_REPLACE_ALL=1 not set")
	}
	pool, err := pgxpool.New(context.Background(), url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	s := testAdminServer(t, pool)

	importConfig := func(body []byte) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.apiImportAdminConfig(rec, importRequest(t, string(body)))
		if rec.Code != http.StatusOK {
			t.Fatalf("import: status %d: %s", rec.Code, rec.Body.String())
		}
	}
	exportConfig := func() ([]byte, adminConfigExport) {
		t.Helper()
		rec := httptest.NewRecorder()
		s.handleAdminExportConfig(rec, httptest.NewRequest(http.MethodGet, "/admin/config/export", nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("export: status %d: %s", rec.Code, rec.Body.String())
		}
		var cfg adminConfigExport
		if err := json.Unmarshal(rec.Body.Bytes(), &cfg); err != nil {
			t.Fatal(err)
		}
		return rec.Body.Bytes(), cfg
	}
	sorted := func(cfg adminConfigExport) adminConfigExport {
		slices.SortFunc(cfg.LLMPrompts, func(a, b ai.Prompt) int {
			return strings.Compare(a.Kind+"|"+a.SourceLanguage+"|"+a.TargetLanguage, b.Kind+"|"+b.SourceLanguage+"|"+b.TargetLanguage)
		})
		slices.SortFunc(cfg.LanguageSections, func(a, b ai.LanguageSections) int { return strings.Compare(a.Language, b.Language) })
		return cfg
	}

	seed, err := json.Marshal(sampleAdminConfig())
	if err != nil {
		t.Fatal(err)
	}
	importConfig(seed)
	exported, first := exportConfig()
	if want := sorted(sampleAdminConfig()); !reflect.DeepEqual(sorted(first), want) {
		t.Errorf("export after import differs from what was imported:\ngot  %+v\nwant %+v", sorted(first), want)
	}
	importConfig(exported)
	_, second := exportConfig()
	if !reflect.DeepEqual(sorted(second), sorted(first)) {
		t.Errorf("second export differs from the first:\ngot  %+v\nwant %+v", sorted(second), sorted(first))
	}
}
