package server

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/ollama"

	"phraseforge/internal/ai"
	"phraseforge/internal/auth"
	"phraseforge/internal/config"
	"phraseforge/internal/roles"
)

var adminDBSchemaCounter atomic.Int64

// newAdminDB is a pool on a throwaway schema with schema.sql applied
// (PHRASEFORGE_TEST_DSN, skipped without it).
func newAdminDB(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("pf_test_%d_admin_%d", os.Getpid(), adminDBSchemaCounter.Add(1))
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
	return pool
}

func postLLMPrompt(s *Server, body string) *httptest.ResponseRecorder {
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/admin/llm-prompts", strings.NewReader(body))
	s.apiSetAdminLLMPrompt(rec, req)
	return rec
}

const promptBodyStart = `"kind":"title","sourceLanguage":"deu","targetLanguage":"title"`

// TestSetLLMPromptRejectsBadTagsBeforeTouchingTheDatabase: every validation
// failure is a 400 validation_error that names what is wrong; no database.
func TestSetLLMPromptRejectsBadTagsBeforeTouchingTheDatabase(t *testing.T) {
	s := testAdminServer(t, nil)
	cases := []struct {
		name string
		body string
		want []string
	}{
		{"unknown tag", `{` + promptBodyStart + `,"model_tags":["default","ghost"]}`, []string{"ghost"}},
		{"several unknown tags", `{` + promptBodyStart + `,"model_tags":["ghost","phantom"]}`, []string{"ghost", "phantom"}},
		{"blank tag", `{` + promptBodyStart + `,"model_tags":["default"," "]}`, []string{"blank"}},
		{"tags no single model has", `{` + promptBodyStart + `,"model_tags":["big","vision"]}`, []string{"big", "vision"}},
		{"removed provider key", `{` + promptBodyStart + `,"provider":"ollama"}`, []string{`"provider"`, "model_tags"}},
		{"removed model key", `{` + promptBodyStart + `,"model":"gemma4:12b"}`, []string{`"model"`, "model_tags"}},
		{"both removed keys even with tags", `{` + promptBodyStart + `,"provider":"ollama","model":"m","model_tags":["default"]}`, []string{`"provider" and "model"`}},
		{"old camelCase tags key", `{` + promptBodyStart + `,"modelTags":["default"]}`, []string{`"modelTags"`, "renamed to model_tags"}},
		{"bad timeout", `{` + promptBodyStart + `,"timeoutSeconds":0}`, []string{"timeout"}},
	}
	for _, tc := range cases {
		rec := postLLMPrompt(s, tc.body)
		if rec.Code != http.StatusBadRequest {
			t.Errorf("%s: status %d, want 400 (%s)", tc.name, rec.Code, rec.Body.String())
			continue
		}
		var resp struct {
			Error struct{ Code, Message string }
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if resp.Error.Code != "validation_error" {
			t.Errorf("%s: code %q, want validation_error", tc.name, resp.Error.Code)
		}
		for _, want := range tc.want {
			if !strings.Contains(resp.Error.Message, want) {
				t.Errorf("%s: message %q lacks %q", tc.name, resp.Error.Message, want)
			}
		}
	}
}

func TestSetLLMPromptStoresTagsAndInheritsWhenNoneAreGiven(t *testing.T) {
	pool := newAdminDB(t)
	s := testAdminServer(t, pool)
	ctx := context.Background()
	stored := func(language string) []string {
		t.Helper()
		var tags []string
		if err := pool.QueryRow(ctx, `SELECT model_tags FROM llm_prompts WHERE kind='title' AND source_language=$1`, language).Scan(&tags); err != nil {
			t.Fatal(err)
		}
		return tags
	}
	post := func(language, extra string) {
		t.Helper()
		body := `{"kind":"title","sourceLanguage":"` + language + `","targetLanguage":"title"` + extra + `}`
		if rec := postLLMPrompt(s, body); rec.Code != http.StatusOK {
			t.Fatalf("%s: status %d: %s", language, rec.Code, rec.Body.String())
		}
	}
	post("deu", `,"model_tags":["default","big"],"think":true,"timeoutSeconds":600`)
	post("omitted", ``)
	post("empty", `,"model_tags":[]`)
	if got := stored("deu"); !reflect.DeepEqual(got, []string{"default", "big"}) {
		t.Errorf("deu tags = %v", got)
	}
	for _, language := range []string{"omitted", "empty"} {
		if got := stored(language); got != nil {
			t.Errorf("%s tags = %v, want NULL (inherit)", language, got)
		}
	}
	listed, err := s.ai.ListPrompts(ctx)
	if err != nil {
		t.Fatal(err)
	}
	for _, p := range listed {
		if p.SourceLanguage == "deu" && (!p.Think || p.TimeoutSeconds == nil || *p.TimeoutSeconds != 600) {
			t.Errorf("deu row lost think or timeout: %+v", p)
		}
	}
}

// assertNoLocatingInfo fails if body mentions the test models' base URL, key
// variable and file, provider name or model names that would let a client reach them.
func assertNoLocatingInfo(t *testing.T, body string) {
	t.Helper()
	for _, secret := range []string{testBaseURL, "distinctive-host", testAPIKeyEnv, testAPIKeyFile, "distinctive-secrets-dir", "pf-credential-file", "base_url", "api_key", "big-model-name",
		testHeaderName, testHeaderLiteral, testHeaderEnv, testHeaderFile, testHeaderFileName, testHeaderEnvName, "headers"} {
		if strings.Contains(body, secret) {
			t.Errorf("response contains %q: %s", secret, body)
		}
	}
}

func TestListAdminModelsHasNoBaseURLKeyOrKeyVariable(t *testing.T) {
	s := testAdminServer(t, nil)
	rec := httptest.NewRecorder()
	s.apiListAdminModels(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/models", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d", rec.Code)
	}
	assertNoLocatingInfo(t, rec.Body.String())
	var resp struct {
		Models []map[string]any `json:"models"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if len(resp.Models) != 3 {
		t.Fatalf("%d models, want 3: %s", len(resp.Models), rec.Body.String())
	}
	first := resp.Models[0]
	if first["id"] != "test-model" || first["provider_id"] != "ollama" || first["priority"] != float64(10) || first["available"] != true || !reflect.DeepEqual(first["tags"], []any{"default"}) {
		t.Errorf("first model = %v", first)
	}
	if _, set := first["unavailable_reason"]; set {
		t.Errorf("an available model has no unavailable_reason: %v", first)
	}
	vision := resp.Models[2]
	if vision["id"] != "vision-model" || vision["available"] != false || vision["unavailable_reason"] != "missing_credentials" {
		t.Errorf("vision model on a provider without its credential = %v", vision)
	}
	for _, model := range resp.Models {
		for key := range model {
			if !strings.Contains(" id provider_id tags priority available unavailable_reason ", " "+key+" ") {
				t.Errorf("unexpected key %q in a model", key)
			}
		}
	}
}

func TestListAdminModelsIsAnEmptyArrayWithoutModels(t *testing.T) {
	s := &Server{ai: ai.New(nil, config.Config{}, nil, nil, nil, nil)}
	rec := httptest.NewRecorder()
	s.apiListAdminModels(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/models", nil))
	if got := strings.TrimSpace(rec.Body.String()); got != `{"models":[]}` {
		t.Errorf("body = %s", got)
	}
}

func TestAdminBootstrapCarriesModelsAndTagsInsteadOfProviders(t *testing.T) {
	pool := newAdminDB(t)
	s := testAdminServer(t, pool)
	s.auth = auth.New(pool, "test-session-key")
	s.roles = roles.New(pool)
	rec := httptest.NewRecorder()
	s.apiGetAdminBootstrap(rec, httptest.NewRequest(http.MethodGet, "/api/v1/admin/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d: %s", rec.Code, rec.Body.String())
	}
	assertNoLocatingInfo(t, rec.Body.String())
	var resp map[string]json.RawMessage
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if _, ok := resp["providers"]; ok {
		t.Error("bootstrap still has providers")
	}
	var tags []string
	if err := json.Unmarshal(resp["tags"], &tags); err != nil || !reflect.DeepEqual(tags, []string{"big", "default", "vision"}) {
		t.Errorf("tags = %s (%v)", resp["tags"], err)
	}
	var models []ai.ModelInfo
	if err := json.Unmarshal(resp["models"], &models); err != nil || len(models) != 3 {
		t.Errorf("models = %s (%v)", resp["models"], err)
	} else if models[2].UnavailableReason != "missing_credentials" || models[0].UnavailableReason != "" {
		t.Errorf("bootstrap models lack unavailable_reason: %s", resp["models"])
	}
	var defaults map[string][]string
	if err := json.Unmarshal(resp["llm_default_model_tags"], &defaults); err != nil || len(defaults) != len(ai.ValidKinds) {
		t.Errorf("llm_default_model_tags = %s (%v)", resp["llm_default_model_tags"], err)
	}
}

func rejectedImport(t *testing.T, s *Server, body string) string {
	t.Helper()
	rec := httptest.NewRecorder()
	s.apiImportAdminConfig(rec, importRequest(t, body))
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	return rec.Body.String()
}

func TestImportNamesTheRemovedKeysAndTheOffendingTags(t *testing.T) {
	s := testAdminServer(t, nil)
	prompt := func(extra string) string {
		return `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title"` + extra + `}]}`
	}
	if msg := rejectedImport(t, s, prompt(`,"provider":"ollama","model":"gemma4:12b"`)); !strings.Contains(msg, `\"provider\" and \"model\" were replaced by model_tags`) {
		t.Errorf("removed keys message = %s", msg)
	}
	if msg := rejectedImport(t, s, prompt(`,"model":"x"`)); !strings.Contains(msg, `\"model\" were replaced by model_tags`) {
		t.Errorf("removed key message = %s", msg)
	}
	if msg := rejectedImport(t, s, prompt(`,"model_tags":["default","ghost"]`)); !strings.Contains(msg, "ghost") || !strings.Contains(msg, "title/cmn/title") {
		t.Errorf("unknown tag message = %s", msg)
	}
}

func TestExportImportKeepsModelTagsAndTreatsEmptyAsInherit(t *testing.T) {
	pool := newAdminDB(t)
	s := testAdminServer(t, pool)
	body := `{"version":1,"llm_prompts":[
		{"kind":"title","source_language":"cmn","target_language":"title","model_tags":["default","big"],"think":true},
		{"kind":"translation","source_language":"cmn","target_language":"deu","model_tags":[]},
		{"kind":"review_models","source_language":"cmn","target_language":"review_models"}]}`
	rec := httptest.NewRecorder()
	s.apiImportAdminConfig(rec, importRequest(t, body))
	if rec.Code != http.StatusOK {
		t.Fatalf("import: status %d: %s", rec.Code, rec.Body.String())
	}
	rec = httptest.NewRecorder()
	s.handleAdminExportConfig(rec, httptest.NewRequest(http.MethodGet, "/admin/config/export", nil))
	var exported adminConfigExport
	if err := json.Unmarshal(rec.Body.Bytes(), &exported); err != nil {
		t.Fatal(err)
	}
	tagsByKind := map[string][]string{}
	for _, p := range exported.LLMPrompts {
		tagsByKind[p.Kind] = p.ModelTags
	}
	if !reflect.DeepEqual(tagsByKind["title"], []string{"default", "big"}) {
		t.Errorf("title tags = %v", tagsByKind["title"])
	}
	if tagsByKind["translation"] != nil || tagsByKind["review_models"] != nil {
		t.Errorf("empty and absent tags did not come back as inherit: %v", tagsByKind)
	}
	if strings.Contains(rec.Body.String(), `"provider"`) || strings.Contains(rec.Body.String(), `"model":`) {
		t.Errorf("export carries a removed key: %s", rec.Body.String())
	}
}

// smallWindowAI has a model with a 1024 token window, below the 4096 every kind
// needs by default, next to one that fits.
func smallWindowAI(t *testing.T) *ai.Service {
	t.Helper()
	models, err := modelcfg.LoadBytes("models.yaml", []byte(`
providers:
  - {id: local, type: ollama, base_url: "http://127.0.0.1:1", api_key_from: ""}
models:
  - {id: roomy-model, provider: local, model: roomy, tags: [default], priority: 10, options: {num_ctx: 32768}}
  - {id: small-model, provider: local, model: small, tags: [default, tiny], priority: 5, options: {num_ctx: 1024}}
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": ollama.New()})
	if err != nil {
		t.Fatal(err)
	}
	svc := ai.New(nil, config.Config{}, nil, nil, nil, nil)
	svc.SetRouter(r, models)
	return svc
}

// An admin tag override can route a kind to a model whose window is smaller
// than the kind's numCtx, which the purposes' own tags are checked for at
// startup; the POST is refused naming the kind and the model, before the database.
func TestSetLLMPromptRejectsATagOverrideRoutedToAModelWithTooSmallAWindow(t *testing.T) {
	s := &Server{ai: smallWindowAI(t)}
	rec := postLLMPrompt(s, `{`+promptBodyStart+`,"model_tags":["tiny"]}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status %d, want 400 (%s)", rec.Code, rec.Body.String())
	}
	for _, want := range []string{"validation_error", "kind title", "model small-model", "num_ctx is 1024", "4096"} {
		if !strings.Contains(rec.Body.String(), want) {
			t.Errorf("body %q lacks %q", rec.Body.String(), want)
		}
	}
	if strings.Contains(rec.Body.String(), "roomy-model") {
		t.Errorf("a model that fits must not be named: %s", rec.Body.String())
	}
}

func TestImportAdminConfigRejectsATagOverrideRoutedToAModelWithTooSmallAWindow(t *testing.T) {
	s := &Server{ai: smallWindowAI(t)}
	rec := httptest.NewRecorder()
	s.apiImportAdminConfig(rec, importRequest(t, `{"version":1,"llm_prompts":[{"kind":"title","source_language":"cmn","target_language":"title","model_tags":["tiny"]}]}`))
	if rec.Code != http.StatusBadRequest || !strings.Contains(rec.Body.String(), "kind title") || !strings.Contains(rec.Body.String(), "small-model") {
		t.Errorf("status %d, body %s", rec.Code, rec.Body.String())
	}
}
