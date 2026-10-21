package server

import (
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	"phraseforge/internal/auth"
	"phraseforge/internal/llmcalls"
	"phraseforge/internal/roles"
)

// llmCallsAPI is the three llm-calls admin routes behind requireAdmin over a
// real database (PHRASEFORGE_TEST_DSN, skipped without it), as Router mounts
// them; the test user comes from a header instead of a session.
type llmCallsAPI struct {
	router    http.Handler
	srv       *Server
	adminID   int64
	studentID int64
}

func newLLMCallsAPI(t *testing.T) *llmCallsAPI {
	t.Helper()
	pool := newAdminDB(t)
	ctx := context.Background()
	f := &llmCallsAPI{}
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
	f.srv = &Server{db: pool, roles: roles.New(pool), ai: testAI(t, pool), llmCalls: llmcalls.NewStore(pool)}

	r := chi.NewRouter()
	r.Use(func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
			id, _ := strconv.ParseInt(req.Header.Get("X-Test-User"), 10, 64)
			next.ServeHTTP(w, req.WithContext(context.WithValue(req.Context(), userCtxKey, auth.User{ID: id, Locale: "en"})))
		})
	})
	r.Group(func(r chi.Router) {
		r.Use(f.srv.requireAdmin)
		r.Get("/api/v1/admin/llm-aggregates", f.srv.apiAdminLLMAggregates)
		r.Get("/api/v1/admin/llm-calls", f.srv.apiAdminLLMCalls)
		r.Get("/api/v1/admin/llm-usage", f.srv.apiAdminLLMUsage)
		r.Delete("/api/v1/admin/llm-calls", f.srv.apiAdminPurgeLLMCalls)
	})
	f.router = r
	return f
}

func (f *llmCallsAPI) do(userID int64, method, path string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, nil)
	req.Header.Set("X-Test-User", strconv.FormatInt(userID, 10))
	rec := httptest.NewRecorder()
	f.router.ServeHTTP(rec, req)
	return rec
}

func (f *llmCallsAPI) adminGet(t *testing.T, path string, wantStatus int) []byte {
	t.Helper()
	rec := f.do(f.adminID, http.MethodGet, path)
	if rec.Code != wantStatus {
		t.Fatalf("GET %s = %d %s; want %d", path, rec.Code, rec.Body, wantStatus)
	}
	return rec.Body.Bytes()
}

// seed inserts one attempt; fields not set by the test get neutral values.
func (f *llmCallsAPI) seed(t *testing.T, c llmcalls.Call) {
	t.Helper()
	if c.ProviderID == "" {
		c.ProviderID = "ollama"
	}
	if c.ModelName == "" {
		c.ModelName = c.ModelID + "-name"
	}
	if err := f.srv.llmCalls.Insert(context.Background(), c); err != nil {
		t.Fatal(err)
	}
}

func hoursAgo(h float64) time.Time {
	return time.Now().Add(-time.Duration(h * float64(time.Hour))).UTC().Truncate(time.Millisecond)
}

type aggregatesResponse struct {
	Since   string           `json:"since"`
	GroupBy string           `json:"group_by"`
	Items   []map[string]any `json:"items"`
}

func (f *llmCallsAPI) aggregates(t *testing.T, query string) aggregatesResponse {
	t.Helper()
	var resp aggregatesResponse
	if err := json.Unmarshal(f.adminGet(t, "/api/v1/admin/llm-aggregates"+query, http.StatusOK), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func near(got any, want float64) bool {
	g, ok := got.(float64)
	return ok && math.Abs(g-want) < 1e-6
}

func seedAggregateRows(t *testing.T, f *llmCallsAPI) {
	t.Helper()
	// model "alpha": 4 ok attempts (100, 200, 300, 400 ms; 50, 100, 0, 150 output
	// tokens), one of each failure, in two kinds.
	for i, ms := range []int{100, 200, 300, 400} {
		kind := "translation"
		if i >= 2 {
			kind = "title"
		}
		f.seed(t, llmcalls.Call{ModelID: "alpha", Kind: kind, StartedAt: hoursAgo(1), DurationMS: ms, Outcome: "ok",
			InputTokens: 10, OutputTokens: []int{50, 100, 0, 150}[i], CostUSD: 0.5})
	}
	for _, outcome := range []string{"error", "timeout", "rate_limited", "canceled"} {
		f.seed(t, llmcalls.Call{ModelID: "alpha", Kind: "translation", StartedAt: hoursAgo(2), DurationMS: 9000, Outcome: outcome,
			InputTokens: 1, OutputTokens: 7, CostUSD: 0.25})
	}
	// model "beta": failures only. Old row of "alpha" outside a 24-hour window.
	f.seed(t, llmcalls.Call{ModelID: "beta", ProviderID: "other", Kind: "title", StartedAt: hoursAgo(3), DurationMS: 50, Outcome: "error"})
	f.seed(t, llmcalls.Call{ModelID: "alpha", Kind: "title", StartedAt: hoursAgo(24 * 5), DurationMS: 1000, Outcome: "ok", InputTokens: 1000, OutputTokens: 1000, CostUSD: 2})
}

func TestLLMAggregatesComputePerModelFromOKAttemptsAndAllAttempts(t *testing.T) {
	f := newLLMCallsAPI(t)
	seedAggregateRows(t, f)
	resp := f.aggregates(t, "")
	if resp.GroupBy != "model" || len(resp.Items) != 2 {
		t.Fatalf("response = %+v; want group_by model and 2 items (the 5-day-old row is outside the default 24 hours)", resp)
	}
	if _, err := time.Parse(time.RFC3339, resp.Since); err != nil {
		t.Errorf("since %q is not RFC 3339", resp.Since)
	}
	alpha, beta := resp.Items[0], resp.Items[1]
	if alpha["model_id"] != "alpha" || beta["model_id"] != "beta" || beta["provider_id"] != "other" {
		t.Fatalf("items = %v; want alpha then beta, sorted by model id", resp.Items)
	}
	for key, want := range map[string]float64{
		"calls": 8, "ok_calls": 4, "error_calls": 1, "timeout_calls": 1, "rate_limited_calls": 1, "canceled_calls": 1,
		"success_rate": 0.5, "input_tokens": 44, "output_tokens": 328,
		"mean_duration_ms": 250, "p50_duration_ms": 250, "p95_duration_ms": 385,
		// 300 output tokens over the 3 ok attempts with output tokens (100+200+400 ms).
		"output_tokens_per_second": 300 / 0.7,
		"cost_usd":                 3,
	} {
		if !near(alpha[key], want) {
			t.Errorf("alpha %s = %v, want %v", key, alpha[key], want)
		}
	}
	if _, has := alpha["kind"]; has {
		t.Errorf("a model grouping has a kind: %v", alpha)
	}
	// beta had no ok attempt: its durations and speed are null, its rate 0.
	for _, key := range []string{"mean_duration_ms", "p50_duration_ms", "p95_duration_ms", "output_tokens_per_second"} {
		if v, has := beta[key]; !has || v != nil {
			t.Errorf("beta %s = %v (present %v), want null", key, v, has)
		}
	}
	if !near(beta["success_rate"], 0) || !near(beta["calls"], 1) {
		t.Errorf("beta = %v", beta)
	}
}

func TestLLMAggregatesHonourSinceAndGroupByKindAndModel(t *testing.T) {
	f := newLLMCallsAPI(t)
	seedAggregateRows(t, f)

	all := f.aggregates(t, "?since="+url.QueryEscape("2000-01-01T00:00:00Z"))
	if !near(all.Items[0]["calls"], 9) || !near(all.Items[0]["cost_usd"], 5) {
		t.Errorf("alpha over all time = %v, want 9 calls and cost 5", all.Items[0])
	}
	narrow := f.aggregates(t, "?since="+url.QueryEscape(hoursAgo(1.5).Format(time.RFC3339)))
	if len(narrow.Items) != 1 || !near(narrow.Items[0]["calls"], 4) {
		t.Errorf("last 1.5 hours = %v, want alpha's 4 ok attempts only", narrow.Items)
	}

	byKind := f.aggregates(t, "?group_by=kind_model")
	if byKind.GroupBy != "kind_model" || len(byKind.Items) != 3 {
		t.Fatalf("kind_model = %+v, want 3 items", byKind)
	}
	var got []string
	for _, it := range byKind.Items {
		got = append(got, it["model_id"].(string)+"/"+it["kind"].(string))
	}
	if strings.Join(got, ",") != "alpha/title,alpha/translation,beta/title" {
		t.Errorf("items %v, want sorted by model id then kind", got)
	}
	if !near(byKind.Items[0]["calls"], 2) || !near(byKind.Items[1]["calls"], 6) {
		t.Errorf("per-kind calls = %v, %v; want 2 and 6", byKind.Items[0]["calls"], byKind.Items[1]["calls"])
	}
}

func TestLLMAggregatesOfAnEmptyWindowAreAnEmptyArrayAndConfiguredModelsAreNotListed(t *testing.T) {
	f := newLLMCallsAPI(t) // models test-model, big-model and vision-model are configured; no rows
	body := f.adminGet(t, "/api/v1/admin/llm-aggregates", http.StatusOK)
	var resp struct {
		Items []any `json:"items"`
	}
	if err := json.Unmarshal(body, &resp); err != nil || resp.Items == nil || len(resp.Items) != 0 {
		t.Errorf("body %s, want items as an empty array", body)
	}
}

func TestLLMAggregatesAndCallsRejectBadParameters(t *testing.T) {
	f := newLLMCallsAPI(t)
	for _, path := range []string{
		"/api/v1/admin/llm-aggregates?since=yesterday",
		"/api/v1/admin/llm-aggregates?since=2026-10-20",
		"/api/v1/admin/llm-aggregates?group_by=provider",
		"/api/v1/admin/llm-calls?since=yesterday",
		"/api/v1/admin/llm-calls?outcome=success",
		"/api/v1/admin/llm-calls?limit=201",
		"/api/v1/admin/llm-calls?limit=0",
		"/api/v1/admin/llm-calls?limit=many",
		"/api/v1/admin/llm-calls?offset=-1",
		"/api/v1/admin/llm-calls?offset=x",
		"/api/v1/admin/llm-usage?since=yesterday",
	} {
		var resp struct {
			Error struct{ Code string } `json:"error"`
		}
		if err := json.Unmarshal(f.adminGet(t, path, http.StatusBadRequest), &resp); err != nil || resp.Error.Code != "validation_error" {
			t.Errorf("GET %s: error code %q, want validation_error", path, resp.Error.Code)
		}
	}
}

type callsResponse struct {
	Items []map[string]any `json:"items"`
}

func (f *llmCallsAPI) calls(t *testing.T, query string) callsResponse {
	t.Helper()
	var resp callsResponse
	if err := json.Unmarshal(f.adminGet(t, "/api/v1/admin/llm-calls"+query, http.StatusOK), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLLMCallsAreNewestFirstFilteredAndCapped(t *testing.T) {
	f := newLLMCallsAPI(t)
	jobID := "7b0a7a63-6b8f-4c43-9d0e-3f3b6a1d2c11"
	errText, status, reqID := "upstream said no", 429, "req-1"
	f.seed(t, llmcalls.Call{ModelID: "alpha", Kind: "title", SourceLanguage: "deu", TargetLanguage: "title", StartedAt: hoursAgo(3), Outcome: "ok", DurationMS: 10})
	f.seed(t, llmcalls.Call{ModelID: "beta", Kind: "translation", SourceLanguage: "deu", TargetLanguage: "eng", StartedAt: hoursAgo(2), Outcome: "rate_limited",
		DurationMS: 20, JobID: &jobID, Error: &errText, HTTPStatus: &status, RequestID: &reqID, Tags: []string{"big"}, InputTokens: 5, OutputTokens: 6, CostUSD: 0.123})
	f.seed(t, llmcalls.Call{ModelID: "alpha", Kind: "title", StartedAt: hoursAgo(1), Outcome: "error", DurationMS: 30})

	all := f.calls(t, "")
	var order []string
	for _, it := range all.Items {
		order = append(order, it["model_id"].(string)+":"+it["outcome"].(string))
	}
	if strings.Join(order, ",") != "alpha:error,beta:rate_limited,alpha:ok" {
		t.Fatalf("order %v, want newest first", order)
	}
	beta := all.Items[1]
	if beta["job_id"] != jobID || beta["error"] != errText || beta["http_status"] != float64(429) || beta["request_id"] != reqID ||
		beta["kind"] != "translation" || beta["source_language"] != "deu" || beta["target_language"] != "eng" ||
		beta["provider_id"] != "ollama" || beta["model_name"] != "beta-name" || beta["duration_ms"] != float64(20) ||
		beta["input_tokens"] != float64(5) || beta["output_tokens"] != float64(6) || !near(beta["cost_usd"], 0.123) {
		t.Errorf("beta = %v", beta)
	}
	if tags, _ := beta["tags"].([]any); len(tags) != 1 || tags[0] != "big" {
		t.Errorf("tags = %v", beta["tags"])
	}
	if _, err := time.Parse(time.RFC3339, beta["started_at"].(string)); err != nil || beta["id"] == "" {
		t.Errorf("id/started_at = %v / %v", beta["id"], beta["started_at"])
	}
	// An interactive call: null job id, error, status and request id; tags an array.
	newest := all.Items[0]
	for _, key := range []string{"job_id", "error", "http_status", "request_id"} {
		if v, has := newest[key]; !has || v != nil {
			t.Errorf("newest %s = %v (present %v), want null", key, v, has)
		}
	}
	if tags, ok := newest["tags"].([]any); !ok || len(tags) != 0 {
		t.Errorf("tags of a call without tags = %v, want []", newest["tags"])
	}

	if got := f.calls(t, "?model_id=alpha"); len(got.Items) != 2 {
		t.Errorf("model_id=alpha: %d items, want 2", len(got.Items))
	}
	if got := f.calls(t, "?outcome=rate_limited"); len(got.Items) != 1 {
		t.Errorf("outcome=rate_limited: %d items, want 1", len(got.Items))
	}
	if got := f.calls(t, "?kind=translation&model_id=beta"); len(got.Items) != 1 {
		t.Errorf("kind+model: %d items, want 1", len(got.Items))
	}
	if got := f.calls(t, "?model_id=alpha&outcome=rate_limited"); len(got.Items) != 0 {
		t.Errorf("no match: %d items, want 0", len(got.Items))
	}
	if got := f.calls(t, "?since="+url.QueryEscape(hoursAgo(2.5).Format(time.RFC3339))); len(got.Items) != 2 {
		t.Errorf("since: %d items, want 2", len(got.Items))
	}
	if got := f.calls(t, "?limit=2"); len(got.Items) != 2 || got.Items[0]["outcome"] != "error" {
		t.Errorf("limit=2: %v", got.Items)
	}
}

func TestLLMCallsDefaultLimitIs25AndTheMaximumIs200(t *testing.T) {
	f := newLLMCallsAPI(t)
	if _, err := f.srv.db.Exec(context.Background(), `INSERT INTO llm_calls (model_id, provider_id, model_name, started_at, duration_ms, outcome)
		SELECT 'm', 'p', 'n', now() - (g || ' seconds')::interval, 1, 'ok' FROM generate_series(1, 230) g`); err != nil {
		t.Fatal(err)
	}
	if got := f.calls(t, ""); len(got.Items) != 25 {
		t.Errorf("default: %d items, want 25", len(got.Items))
	}
	if got := f.calls(t, "?limit=200"); len(got.Items) != 200 {
		t.Errorf("limit=200: %d items, want 200", len(got.Items))
	}
}

func TestPurgeLLMCallsRequiresAValidAgeAndDeletesOnlyOlderRows(t *testing.T) {
	f := newLLMCallsAPI(t)
	for _, days := range []float64{45, 31, 29, 1} {
		f.seed(t, llmcalls.Call{ModelID: "m", StartedAt: hoursAgo(days * 24), Outcome: "ok"})
	}
	count := func() int {
		var n int
		if err := f.srv.db.QueryRow(context.Background(), `SELECT count(*) FROM llm_calls`).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	for _, query := range []string{"", "?older_than_days=", "?older_than_days=-1", "?older_than_days=abc", "?older_than_days=1.5", "?older_than_days=99999999999999999999", "?older_than_days=36501"} {
		if rec := f.do(f.adminID, http.MethodDelete, "/api/v1/admin/llm-calls"+query); rec.Code != http.StatusBadRequest {
			t.Errorf("DELETE %q = %d, want 400", query, rec.Code)
		}
	}
	if n := count(); n != 4 {
		t.Fatalf("%d rows after rejected requests, want all 4", n)
	}

	rec := f.do(f.adminID, http.MethodDelete, "/api/v1/admin/llm-calls?older_than_days=30")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deleted":2,"older_than_days":30}` {
		t.Errorf("purge 30 = %d %s, want 200 and 2 deleted", rec.Code, rec.Body)
	}
	if n := count(); n != 2 {
		t.Errorf("%d rows left, want the 2 newer ones", n)
	}
	rec = f.do(f.adminID, http.MethodDelete, "/api/v1/admin/llm-calls?older_than_days=0")
	if rec.Code != http.StatusOK || strings.TrimSpace(rec.Body.String()) != `{"deleted":2,"older_than_days":0}` {
		t.Errorf("purge 0 = %d %s, want 200 and 2 deleted", rec.Code, rec.Body)
	}
	if n := count(); n != 0 {
		t.Errorf("%d rows left, want none", n)
	}
}

func TestLLMCallsRoutesRefuseNonAdminsLikeTheOtherAdminRoutes(t *testing.T) {
	f := newLLMCallsAPI(t)
	f.seed(t, llmcalls.Call{ModelID: "m", StartedAt: hoursAgo(1), Outcome: "ok"})
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/llm-aggregates"},
		{http.MethodGet, "/api/v1/admin/llm-calls"},
		{http.MethodGet, "/api/v1/admin/llm-usage"},
		{http.MethodDelete, "/api/v1/admin/llm-calls?older_than_days=0"},
	} {
		if rec := f.do(f.studentID, c.method, c.path); rec.Code != http.StatusNotFound {
			t.Errorf("%s %s as a non-admin = %d, want 404", c.method, c.path, rec.Code)
		}
	}
	var n int
	if err := f.srv.db.QueryRow(context.Background(), `SELECT count(*) FROM llm_calls`).Scan(&n); err != nil || n != 1 {
		t.Errorf("rows = %d, %v; a non-admin purge must delete nothing", n, err)
	}
}

func TestLLMCallsRoutesAreInTheRealRouterBehindTheLogin(t *testing.T) {
	f := newLLMCallsAPI(t)
	f.srv.auth = auth.New(f.srv.db, "test-session-key")
	h := f.srv.Router()
	for _, c := range []struct{ method, path string }{
		{http.MethodGet, "/api/v1/admin/llm-aggregates"},
		{http.MethodGet, "/api/v1/admin/llm-calls"},
		{http.MethodGet, "/api/v1/admin/llm-usage"},
		{http.MethodDelete, "/api/v1/admin/llm-calls?older_than_days=0"},
	} {
		rec := httptest.NewRecorder()
		h.ServeHTTP(rec, httptest.NewRequest(c.method, c.path, nil))
		if rec.Code != http.StatusFound || rec.Header().Get("Location") != "/login" {
			t.Errorf("anonymous %s %s = %d to %q, want a redirect to /login", c.method, c.path, rec.Code, rec.Header().Get("Location"))
		}
	}
}

func TestLLMCallsResponsesHaveOnlyTheDocumentedFields(t *testing.T) {
	f := newLLMCallsAPI(t)
	seedAggregateRows(t, f)
	aggregateKeys := " model_id provider_id kind calls ok_calls error_calls timeout_calls rate_limited_calls canceled_calls success_rate input_tokens output_tokens mean_duration_ms p50_duration_ms p95_duration_ms output_tokens_per_second cost_usd "
	callKeys := " id started_at user_id username kind source_language target_language job_id model_id provider_id model_name tags input_tokens output_tokens cost_usd duration_ms outcome error http_status request_id "
	for _, c := range []struct {
		path string
		keys string
	}{
		{"/api/v1/admin/llm-aggregates?group_by=kind_model", aggregateKeys},
		{"/api/v1/admin/llm-calls", callKeys},
		{"/api/v1/admin/llm-usage", " user_id username provider_id model_id calls input_tokens output_tokens cost_usd "},
	} {
		body := f.adminGet(t, c.path, http.StatusOK)
		assertNoLocatingInfo(t, string(body))
		var resp struct {
			Items []map[string]any `json:"items"`
		}
		if err := json.Unmarshal(body, &resp); err != nil || len(resp.Items) == 0 {
			t.Fatalf("%s: %v, %d items", c.path, err, len(resp.Items))
		}
		for _, it := range resp.Items {
			for key := range it {
				if !strings.Contains(c.keys, " "+key+" ") {
					t.Errorf("%s: unexpected key %q", c.path, key)
				}
			}
		}
	}
}

func TestLLMCallsArePagedWithATotalAndNameTheUser(t *testing.T) {
	f := newLLMCallsAPI(t)
	for i := 0; i < 5; i++ {
		c := llmcalls.Call{ModelID: "m", StartedAt: hoursAgo(float64(10 - i)), Outcome: "ok", InputTokens: i}
		if i%2 == 0 {
			c.UserID = &f.studentID
		}
		f.seed(t, c)
	}
	var page struct {
		Items  []map[string]any `json:"items"`
		Total  int              `json:"total"`
		Limit  int              `json:"limit"`
		Offset int              `json:"offset"`
	}
	if err := json.Unmarshal(f.adminGet(t, "/api/v1/admin/llm-calls?limit=2&offset=1", http.StatusOK), &page); err != nil {
		t.Fatal(err)
	}
	if page.Total != 5 || page.Limit != 2 || page.Offset != 1 || len(page.Items) != 2 {
		t.Fatalf("page = %+v", page)
	}
	// Newest first: i=4,3,2,1,0; offset 1 starts at i=3 (no user) then i=2 (the student).
	if page.Items[0]["input_tokens"] != float64(3) || page.Items[0]["user_id"] != nil || page.Items[0]["username"] != nil {
		t.Errorf("first = %v", page.Items[0])
	}
	if page.Items[1]["input_tokens"] != float64(2) || page.Items[1]["username"] != "student" || page.Items[1]["user_id"] != float64(f.studentID) {
		t.Errorf("second = %v", page.Items[1])
	}
	if err := json.Unmarshal(f.adminGet(t, "/api/v1/admin/llm-calls?limit=2&offset=40", http.StatusOK), &page); err != nil || len(page.Items) != 0 || page.Total != 5 {
		t.Errorf("a page past the end is empty and the total stays: %+v %v", page, err)
	}
	if got := f.calls(t, "?model_id=nothing"); len(got.Items) != 0 {
		t.Errorf("no match: %d", len(got.Items))
	}
	var none struct{ Total int }
	if err := json.Unmarshal(f.adminGet(t, "/api/v1/admin/llm-calls?model_id=nothing", http.StatusOK), &none); err != nil || none.Total != 0 {
		t.Errorf("total of no match = %d, %v", none.Total, err)
	}
}

type usageResponse struct {
	Items []map[string]any `json:"items"`
}

func (f *llmCallsAPI) usage(t *testing.T, query string) usageResponse {
	t.Helper()
	var resp usageResponse
	if err := json.Unmarshal(f.adminGet(t, "/api/v1/admin/llm-usage"+query, http.StatusOK), &resp); err != nil {
		t.Fatal(err)
	}
	return resp
}

func TestLLMUsageSumsOnlySuccessfulCallsPerUserProviderAndModel(t *testing.T) {
	f := newLLMCallsAPI(t)
	ada, bob := f.adminID, f.studentID
	seed := func(user *int64, provider, model, outcome string, in, out int, cost float64, ago float64) {
		f.seed(t, llmcalls.Call{UserID: user, ProviderID: provider, ModelID: model, Outcome: outcome, InputTokens: in, OutputTokens: out, CostUSD: cost, StartedAt: hoursAgo(ago)})
	}
	seed(&ada, "gpu", "big", "ok", 100, 10, 0.5, 48)
	seed(&ada, "gpu", "big", "ok", 200, 20, 1.0, 1)
	seed(&ada, "gpu", "big", "error", 0, 0, 0, 1)
	seed(&ada, "cloud", "big", "ok", 7, 7, 0.25, 1)
	seed(&bob, "gpu", "small", "ok", 50, 5, 0, 1)
	seed(nil, "gpu", "small", "ok", 5, 1, 0, 1)

	items := f.usage(t, "").Items
	if len(items) != 4 {
		t.Fatalf("items = %v, want 4 (ada gpu/big, ada cloud/big, bob, no user)", items)
	}
	first := items[0]
	if first["username"] != "admin" || first["provider_id"] != "gpu" || first["model_id"] != "big" || first["calls"] != float64(2) ||
		first["input_tokens"] != float64(300) || first["output_tokens"] != float64(30) || !near(first["cost_usd"], 1.5) {
		t.Errorf("first (highest cost, the failed call not counted) = %v", first)
	}
	last := items[len(items)-1]
	if last["user_id"] != nil || last["username"] != nil {
		t.Errorf("a call with no user has no user: %v", last)
	}
	since := f.usage(t, "?since="+url.QueryEscape(hoursAgo(2).Format(time.RFC3339))).Items
	var calls float64
	for _, it := range since {
		calls += it["calls"].(float64)
	}
	if calls != 4 {
		t.Errorf("calls since 2 hours ago = %v, want 4 (the 48-hour-old call is out)", calls)
	}
}

func TestLLMUsageOfNoCallsIsAnEmptyArray(t *testing.T) {
	f := newLLMCallsAPI(t)
	if body := string(f.adminGet(t, "/api/v1/admin/llm-usage", http.StatusOK)); strings.TrimSpace(body) != `{"items":[]}` {
		t.Errorf("body %s", body)
	}
}
