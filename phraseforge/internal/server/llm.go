package server

import (
	"encoding/json"
	"net/http"
	"strings"
)

type llmGenerateRequest struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	ContentType    string `json:"content_type"`
	Content        string `json:"content"`
}

func (s *Server) handleLLMGenerate(w http.ResponseWriter, r *http.Request) {
	var req llmGenerateRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	if req.Kind != "translation" && req.Kind != "transcription" {
		http.Error(w, "kind must be translation or transcription", http.StatusBadRequest)
		return
	}
	if strings.TrimSpace(req.TargetLanguage) == "" {
		req.TargetLanguage = currentUser(r).Locale
	}
	out, err := s.ai.Generate(r.Context(), req.Kind, req.SourceLanguage, req.TargetLanguage, req.ContentType, req.Content)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": strings.TrimSpace(out)}) //nolint:errcheck
}
