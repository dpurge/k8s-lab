package server

import (
	"embed"
	"encoding/json"
	"errors"
	"io/fs"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/go-chi/chi/v5"
	"knowledge/internal/chat"
	"knowledge/internal/qdrant"
)

//go:embed static/*
var static embed.FS

type Server struct {
	kb   *qdrant.Client
	chat *chat.Service
}

func New(kb *qdrant.Client, chatSvc *chat.Service) *Server { return &Server{kb: kb, chat: chatSvc} }

type itemRequest struct {
	Title   string   `json:"title"`
	Summary string   `json:"summary"`
	Body    string   `json:"body"`
	Tags    []string `json:"tags"`
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
	r.Route("/api/v1", func(r chi.Router) {
		r.Get("/knowledge", s.list)
		r.Post("/knowledge", s.create)
		r.Post("/knowledge/search", s.search)
		r.Get("/knowledge/{id}", s.get)
		r.Get("/chats", s.listChats)
		r.Post("/chats", s.createChat)
		r.Get("/chats/{id}", s.getChat)
		r.Delete("/chats/{id}", s.deleteChat)
		r.Post("/chats/{id}/messages", s.sendMessage)
		r.Put("/knowledge/{id}", s.update)
		r.Delete("/knowledge/{id}", s.delete)
	})
	staticRoot, err := fs.Sub(static, "static")
	if err != nil {
		panic(err)
	}
	r.Handle("/*", http.FileServer(http.FS(staticRoot)))
	return r
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
func writeErr(w http.ResponseWriter, status int, code, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": code, "message": msg}})
}
