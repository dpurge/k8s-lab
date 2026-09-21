package server

import (
	"context"
	"embed"
	"encoding/json"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"gopkg.in/yaml.v3"
	"knowledge/internal/auth"
	"knowledge/internal/chat"
	"knowledge/internal/generate"
	"knowledge/internal/ingest"
	"knowledge/internal/jobs"
	"knowledge/internal/qdrant"
)

// importBodyCap bounds a YAML import request: a whole-collection export can
// be large (unlike a single ingest source, capped much lower at
// ingest.MaxSourceBytes), so this is sized for that, not for one item.
const importBodyCap = 8 << 20

//go:embed static/*
var static embed.FS

const sessionCookieName = "kb_session"

type Server struct {
	kb       *qdrant.Client
	chat     *chat.Service
	auth     *auth.Service
	generate *generate.Service
	jobs     *jobs.Service
	ingest   *ingest.Service
}

func New(kb *qdrant.Client, chatSvc *chat.Service, authSvc *auth.Service, generateSvc *generate.Service, jobsSvc *jobs.Service, ingestSvc *ingest.Service) *Server {
	return &Server{kb: kb, chat: chatSvc, auth: authSvc, generate: generateSvc, jobs: jobsSvc, ingest: ingestSvc}
}

type itemRequest struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
}
type generateRequest struct {
	Body string `json:"body"`
}
type generateTitleResponse struct {
	Title string `json:"title"`
}
type generateSummaryResponse struct {
	Summary string `json:"summary"`
}
type searchRequest struct {
	Query string     `json:"query"`
	Tags  []string   `json:"tags"`
	Start *time.Time `json:"start"`
	End   *time.Time `json:"end"`
	Limit int        `json:"limit"`
}
type listResponse struct {
	Items []qdrant.ListItem `json:"items"`
}
type exportResponse struct {
	Items []qdrant.Item `json:"items" yaml:"items"`
}
type importRequest struct {
	Items []qdrant.ImportItem `json:"items" yaml:"items"`
}

type authRequest struct {
	Email    string `json:"email"`
	Password string `json:"password"`
}

type createChatRequest struct {
	Title string   `json:"title"`
	Tags  []string `json:"tags"`
}
type sendMessageRequest struct {
	Message string `json:"message"`
}
type chatsResponse struct {
	Chats []chat.Chat `json:"chats"`
}
type sendMessageResponse struct {
	AssistantMessage chat.Message  `json:"assistant_message"`
	Sources          []chat.Source `json:"sources"`
}

// ingestURLBodyCap bounds an ingest/url request body — a URL plus tags
// needs kilobytes, not megabytes.
const ingestURLBodyCap = 64 << 10 // 64 KiB

type ingestURLRequest struct {
	URL  string   `json:"url"`
	Tags []string `json:"tags"`
}
type ingestTextRequest struct {
	Filename string   `json:"filename"`
	Content  string   `json:"content"`
	Tags     []string `json:"tags"`
}
type ingestDraftsResponse struct {
	Drafts []ingest.DraftSummary `json:"drafts"`
}
type draftUpdateRequest struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
}
type approveDraftResponse struct {
	ID string `json:"id"`
}

// draftUpdateBodyCap mirrors ingestURLBodyCap's reasoning: a draft edit is
// title/summary/body/tags text, not a file upload, so 256 KiB (per the
// approved spec) is ample headroom without inviting an oversized request.
const draftUpdateBodyCap = 256 << 10 // 256 KiB

func (s *Server) Router() http.Handler {
	r := chi.NewRouter()
	r.Get("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	r.Get("/ready", func(w http.ResponseWriter, r *http.Request) {
		if err := s.kb.Health(r.Context()); err != nil {
			http.Error(w, "qdrant unreachable", 503)
			return
		}
		w.WriteHeader(200)
	})

	// Public: the login/signup pages, the shared theme script they (and the
	// main app) load, and the auth API that issues/clears the session
	// cookie. Registered as exact paths so they take precedence over the
	// "/*" static handler below regardless of which group added them.
	r.Get("/login.html", s.serveStatic("login.html"))
	r.Get("/signup.html", s.serveStatic("signup.html"))
	r.Get("/theme.js", s.serveStatic("theme.js"))
	r.Post("/api/v1/auth/signup", s.signup)
	r.Post("/api/v1/auth/login", s.login)
	r.Post("/api/v1/auth/logout", s.logout)

	r.Group(func(r chi.Router) {
		r.Use(s.requireAuth)
		r.Route("/api/v1", func(r chi.Router) {
			r.Get("/auth/me", s.me)
			r.Get("/jobs/current", s.currentJob)
			r.Get("/jobs", s.listJobs)
			r.Post("/jobs/{id}/retry", s.retryJob)
			r.Delete("/jobs/{id}", s.deleteJob)
			r.Get("/knowledge", s.list)
			r.Post("/knowledge", s.create)
			r.Post("/knowledge/search", s.search)
			r.Get("/knowledge/export", s.export)
			r.Post("/knowledge/import", s.importItems)
			r.Post("/knowledge/generate/title", s.generateTitle)
			r.Post("/knowledge/generate/summary", s.generateSummary)
			r.Get("/knowledge/{id}", s.get)
			r.Get("/chats", s.listChats)
			r.Post("/chats", s.createChat)
			r.Get("/chats/{id}", s.getChat)
			r.Delete("/chats/{id}", s.deleteChat)
			r.Post("/chats/{id}/messages", s.sendMessage)
			r.Put("/knowledge/{id}", s.update)
			r.Delete("/knowledge/{id}", s.delete)
			r.Post("/ingest/url", s.ingestURL)
			r.Post("/ingest/text", s.ingestText)
			r.Get("/ingest/drafts", s.ingestDrafts)
			r.Get("/ingest/drafts/{id}", s.getDraft)
			r.Put("/ingest/drafts/{id}", s.updateDraft)
			r.Post("/ingest/drafts/{id}/approve", s.approveDraft)
			r.Delete("/ingest/drafts/{id}", s.discardDraft)
		})
		staticRoot, err := fs.Sub(static, "static")
		if err != nil {
			panic(err)
		}
		r.Handle("/*", http.FileServer(http.FS(staticRoot)))
	})
	return r
}

func (s *Server) serveStatic(name string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		http.ServeFileFS(w, r, static, "static/"+name)
	}
}

type userCtxKey struct{}

func userFromContext(ctx context.Context) auth.User {
	u, _ := ctx.Value(userCtxKey{}).(auth.User)
	return u
}

// requireAuth gates both the JSON API and the static app/assets behind a
// valid session cookie. API requests get a 401; anything else (the app
// shell, any other static asset) is redirected to the login page.
func (s *Server) requireAuth(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var user auth.User
		var err error = auth.ErrNoSession
		if cookie, cerr := r.Cookie(sessionCookieName); cerr == nil {
			user, err = s.auth.UserForToken(r.Context(), cookie.Value)
		}
		if err != nil {
			if strings.HasPrefix(r.URL.Path, "/api/") {
				writeErr(w, 401, "unauthorized", "login required")
				return
			}
			http.Redirect(w, r, "/login.html", http.StatusFound)
			return
		}
		next.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), userCtxKey{}, user)))
	})
}

func (s *Server) me(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, 200, map[string]string{"email": userFromContext(r.Context()).Email})
}

func (s *Server) currentJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.jobs.Current(r.Context())
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	if job == nil {
		w.WriteHeader(204)
		return
	}
	writeJSON(w, 200, job)
}

type jobsResponse struct {
	Jobs []jobs.Job `json:"jobs"`
}

func (s *Server) listJobs(w http.ResponseWriter, r *http.Request) {
	list, err := s.jobs.List(r.Context())
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, jobsResponse{Jobs: list})
}

func (s *Server) retryJob(w http.ResponseWriter, r *http.Request) {
	job, err := s.ingest.Retry(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		writeJSON(w, 202, job)
	case errors.Is(err, ingest.ErrNotFound):
		writeErr(w, 404, "not_found", err.Error())
	case errors.Is(err, ingest.ErrJobRunning):
		writeErr(w, 409, "job_running", err.Error())
	case errors.Is(err, ingest.ErrNotRetryable):
		writeErr(w, 409, "not_retryable", err.Error())
	case errors.Is(err, ingest.ErrValidation):
		writeErr(w, 400, "validation_failed", err.Error())
	default:
		writeErr(w, 500, "internal", err.Error())
	}
}

func (s *Server) deleteJob(w http.ResponseWriter, r *http.Request) {
	err := s.jobs.Delete(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		w.WriteHeader(204)
	case errors.Is(err, jobs.ErrRunning):
		writeErr(w, 409, "job_running", err.Error())
	default:
		writeErr(w, 500, "internal", err.Error())
	}
}

func (s *Server) signup(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if !decode(w, r, &req) {
		return
	}
	if _, err := s.auth.Signup(r.Context(), req.Email, req.Password); err != nil {
		switch {
		case errors.Is(err, auth.ErrValidation):
			writeErr(w, 400, "validation_failed", err.Error())
		case errors.Is(err, auth.ErrEmailTaken):
			writeErr(w, 409, "email_taken", err.Error())
		default:
			writeErr(w, 500, "internal", err.Error())
		}
		return
	}
	s.startSession(w, r, req.Email, req.Password)
}

func (s *Server) login(w http.ResponseWriter, r *http.Request) {
	var req authRequest
	if !decode(w, r, &req) {
		return
	}
	s.startSession(w, r, req.Email, req.Password)
}

func (s *Server) startSession(w http.ResponseWriter, r *http.Request, email, password string) {
	token, user, err := s.auth.Login(r.Context(), email, password)
	if err != nil {
		if errors.Is(err, auth.ErrInvalidLogin) {
			writeErr(w, 401, "invalid_login", err.Error())
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	// No Secure flag: this app is served over plain HTTP in local/dev
	// clusters (see README) with no TLS termination at the ingress.
	http.SetCookie(w, &http.Cookie{
		Name:     sessionCookieName,
		Value:    token,
		Path:     "/",
		HttpOnly: true,
		SameSite: http.SameSiteLaxMode,
		Expires:  time.Now().Add(auth.SessionTTL),
	})
	writeJSON(w, 200, map[string]string{"email": user.Email})
}

func (s *Server) logout(w http.ResponseWriter, r *http.Request) {
	if cookie, err := r.Cookie(sessionCookieName); err == nil {
		_ = s.auth.Logout(r.Context(), cookie.Value)
	}
	http.SetCookie(w, &http.Cookie{Name: sessionCookieName, Value: "", Path: "/", MaxAge: -1})
	w.WriteHeader(204)
}

func (s *Server) list(w http.ResponseWriter, r *http.Request) {
	search, ok := searchFromQuery(w, r)
	if !ok {
		return
	}
	items, err := s.kb.Search(r.Context(), search)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, listResponse{Items: items})
}
func (s *Server) search(w http.ResponseWriter, r *http.Request) {
	var req searchRequest
	if !decode(w, r, &req) {
		return
	}
	items, err := s.kb.Search(r.Context(), qdrant.Search{Query: req.Query, Tags: req.Tags, Start: req.Start, End: req.End, Limit: req.Limit})
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, listResponse{Items: items})
}
func (s *Server) export(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	tags := append(q["tag"], splitTags(q.Get("tags"))...)
	items, err := s.kb.ExportAll(r.Context(), tags)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	w.Header().Set("Content-Disposition", `attachment; filename="knowledge-export.yaml"`)
	writeYAML(w, 200, exportResponse{Items: items})
}
func (s *Server) importItems(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, importBodyCap)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, 413, "payload_too_large", err.Error())
			return
		}
		writeErr(w, 400, "invalid_yaml", err.Error())
		return
	}
	var req importRequest
	if err := yaml.Unmarshal(body, &req); err != nil {
		writeErr(w, 400, "invalid_yaml", err.Error())
		return
	}
	if len(req.Items) == 0 {
		writeErr(w, 400, "validation_failed", "items must be a non-empty array")
		return
	}
	writeJSON(w, 200, s.kb.Import(r.Context(), req.Items))
}
func (s *Server) create(w http.ResponseWriter, r *http.Request) {
	var req itemRequest
	if !decode(w, r, &req) {
		return
	}
	it, err := s.kb.Create(r.Context(), req.Title, req.Summary, req.Body, req.Tags)
	writeItemOrErr(w, it, err, 201)
}
func (s *Server) get(w http.ResponseWriter, r *http.Request) {
	it, err := s.kb.Get(r.Context(), chi.URLParam(r, "id"))
	writeItemOrErr(w, it, err, 200)
}
func (s *Server) update(w http.ResponseWriter, r *http.Request) {
	var req itemRequest
	if !decode(w, r, &req) {
		return
	}
	it, err := s.kb.Update(r.Context(), chi.URLParam(r, "id"), req.Title, req.Summary, req.Body, req.Tags)
	writeItemOrErr(w, it, err, 200)
}
func (s *Server) delete(w http.ResponseWriter, r *http.Request) {
	if err := s.kb.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	w.WriteHeader(204)
}

func (s *Server) generateTitle(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if !decode(w, r, &req) {
		return
	}
	title, err := s.generate.Title(r.Context(), req.Body)
	if err != nil {
		if errors.Is(err, generate.ErrEmptyBody) {
			writeErr(w, 400, "validation_failed", err.Error())
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, generateTitleResponse{Title: title})
}

func (s *Server) generateSummary(w http.ResponseWriter, r *http.Request) {
	var req generateRequest
	if !decode(w, r, &req) {
		return
	}
	summary, err := s.generate.Summary(r.Context(), req.Body)
	if err != nil {
		if errors.Is(err, generate.ErrEmptyBody) {
			writeErr(w, 400, "validation_failed", err.Error())
			return
		}
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, generateSummaryResponse{Summary: summary})
}

func (s *Server) createChat(w http.ResponseWriter, r *http.Request) {
	var req createChatRequest
	if !decode(w, r, &req) {
		return
	}
	c, err := s.chat.Create(r.Context(), req.Title, req.Tags)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 201, c)
}
func (s *Server) listChats(w http.ResponseWriter, r *http.Request) {
	chats, err := s.chat.List(r.Context())
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, chatsResponse{Chats: chats})
}
func (s *Server) getChat(w http.ResponseWriter, r *http.Request) {
	c, err := s.chat.Get(r.Context(), chi.URLParam(r, "id"))
	if err != nil {
		writeErr(w, 404, "not_found", err.Error())
		return
	}
	writeJSON(w, 200, c)
}
func (s *Server) deleteChat(w http.ResponseWriter, r *http.Request) {
	if err := s.chat.Delete(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	w.WriteHeader(204)
}
func (s *Server) sendMessage(w http.ResponseWriter, r *http.Request) {
	var req sendMessageRequest
	if !decode(w, r, &req) {
		return
	}
	if strings.TrimSpace(req.Message) == "" {
		writeErr(w, 400, "validation_failed", "message is required")
		return
	}
	msg, sources, err := s.chat.Send(r.Context(), chi.URLParam(r, "id"), req.Message)
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, sendMessageResponse{AssistantMessage: msg, Sources: sources})
}

func (s *Server) ingestURL(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, ingestURLBodyCap)
	var req ingestURLRequest
	if !decode(w, r, &req) {
		return
	}
	job, err := s.ingest.StartURL(r.Context(), req.URL, qdrant.NormalizeTags(req.Tags))
	writeIngestJobOrErr(w, job, err)
}

func (s *Server) ingestText(w http.ResponseWriter, r *http.Request) {
	// Headroom over ingest.MaxSourceBytes: the cap here bounds the whole
	// JSON envelope (structure, filename, tags, and JSON-escaping overhead
	// on the content field — every literal newline in markdown content
	// costs 2 bytes as \n), not just the content field itself. The added
	// 64 KiB covers that overhead so a file at or near the documented
	// content limit can still be accepted; validateText's own size check
	// remains the authoritative content-size limit.
	r.Body = http.MaxBytesReader(w, r.Body, ingest.MaxSourceBytes+(64<<10))
	var req ingestTextRequest
	if !decode(w, r, &req) {
		return
	}
	job, err := s.ingest.StartText(r.Context(), req.Filename, req.Content, qdrant.NormalizeTags(req.Tags))
	writeIngestJobOrErr(w, job, err)
}

func writeIngestJobOrErr(w http.ResponseWriter, job jobs.Job, err error) {
	switch {
	case err == nil:
		writeJSON(w, 202, job)
	case errors.Is(err, ingest.ErrValidation):
		writeErr(w, 400, "validation_failed", err.Error())
	case errors.Is(err, ingest.ErrJobRunning):
		writeErr(w, 409, "job_running", err.Error())
	default:
		writeErr(w, 500, "internal", err.Error())
	}
}

func (s *Server) ingestDrafts(w http.ResponseWriter, r *http.Request) {
	drafts, err := s.ingest.ListDrafts(r.Context())
	if err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	writeJSON(w, 200, ingestDraftsResponse{Drafts: drafts})
}

func (s *Server) getDraft(w http.ResponseWriter, r *http.Request) {
	d, err := s.ingest.GetDraft(r.Context(), chi.URLParam(r, "id"))
	writeDraftOrErr(w, d, err)
}

func (s *Server) updateDraft(w http.ResponseWriter, r *http.Request) {
	r.Body = http.MaxBytesReader(w, r.Body, draftUpdateBodyCap)
	var req draftUpdateRequest
	if !decode(w, r, &req) {
		return
	}
	d, err := s.ingest.UpdateDraft(r.Context(), chi.URLParam(r, "id"), req.Title, req.Summary, req.Body, qdrant.NormalizeTags(req.Tags))
	writeDraftOrErr(w, d, err)
}

func (s *Server) discardDraft(w http.ResponseWriter, r *http.Request) {
	if err := s.ingest.DiscardDraft(r.Context(), chi.URLParam(r, "id")); err != nil {
		writeErr(w, 500, "internal", err.Error())
		return
	}
	w.WriteHeader(204)
}

func writeDraftOrErr(w http.ResponseWriter, d ingest.Draft, err error) {
	switch {
	case err == nil:
		writeJSON(w, 200, d)
	case errors.Is(err, ingest.ErrNotFound):
		writeErr(w, 404, "not_found", err.Error())
	case errors.Is(err, ingest.ErrValidation):
		writeErr(w, 400, "validation_failed", "title, summary, and body are required")
	default:
		writeErr(w, 500, "internal", err.Error())
	}
}

// approveDraft has no qdrant.ErrValidation case: ApproveDraft's
// validateDraftFields check (see ingest.go) already rejects an empty
// title/summary/body — the only condition qdrant.Create's own validate
// checks — before the promoter is ever called, so that path is
// unreachable here. Any other qdrant.Create failure (an unreachable
// Qdrant, an embeddings error, etc.) falls through to the default 500,
// same as before.
func (s *Server) approveDraft(w http.ResponseWriter, r *http.Request) {
	id, err := s.ingest.ApproveDraft(r.Context(), chi.URLParam(r, "id"))
	switch {
	case err == nil:
		writeJSON(w, 201, approveDraftResponse{ID: id})
	case errors.Is(err, ingest.ErrNotFound):
		writeErr(w, 404, "not_found", err.Error())
	case errors.Is(err, ingest.ErrValidation):
		writeErr(w, 400, "validation_failed", "title, summary, and body are required")
	default:
		writeErr(w, 500, "internal", err.Error())
	}
}

func searchFromQuery(w http.ResponseWriter, r *http.Request) (qdrant.Search, bool) {
	q := r.URL.Query()
	s := qdrant.Search{Query: q.Get("q"), Tags: append(q["tag"], splitTags(q.Get("tags"))...)}
	if v := q.Get("limit"); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 {
			writeErr(w, 400, "validation_failed", "limit must be a positive integer")
			return s, false
		}
		s.Limit = n
	}
	if v := q.Get("start"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, 400, "validation_failed", "start must be RFC3339")
			return s, false
		}
		s.Start = &t
	}
	if v := q.Get("end"); v != "" {
		t, err := time.Parse(time.RFC3339, v)
		if err != nil {
			writeErr(w, 400, "validation_failed", "end must be RFC3339")
			return s, false
		}
		s.End = &t
	}
	return s, true
}
func splitTags(s string) []string {
	if s == "" {
		return nil
	}
	return strings.Split(s, ",")
}
func writeItemOrErr(w http.ResponseWriter, it qdrant.Item, err error, status int) {
	switch {
	case err == nil:
		writeJSON(w, status, it)
	case errors.Is(err, qdrant.ErrNotFound):
		writeErr(w, 404, "not_found", err.Error())
	case errors.Is(err, qdrant.ErrValidation):
		writeErr(w, 400, "validation_failed", "title, summary, and body are required")
	default:
		writeErr(w, 500, "internal", err.Error())
	}
}
func decode(w http.ResponseWriter, r *http.Request, v any) bool {
	if err := json.NewDecoder(r.Body).Decode(v); err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, 413, "payload_too_large", err.Error())
			return false
		}
		writeErr(w, 400, "invalid_json", err.Error())
		return false
	}
	return true
}
func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}
func writeYAML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(status)
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	_ = enc.Encode(v)
	_ = enc.Close()
}
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
