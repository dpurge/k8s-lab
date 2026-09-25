package server

import (
	"encoding/json"
	"net/http"
	"strings"

	"phraseforge/internal/ai"
	"phraseforge/internal/jobs"
)

type llmGenerateRequest struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	ContentType    string `json:"content_type"`
	Content        string `json:"content"`
}

type llmGenerateResult struct {
	Text string `json:"text"`
}

// handleLLMGenerate's external HTTP contract is unchanged — same request and
// response shape — but internally it now goes through the job queue
// (jobs.Service.EnqueueAndAwait) instead of calling ai.Service.Generate
// directly, serializing it against any future background job rather than
// running fully concurrently with it (see
// specs/features/phraseforge-job-queue.md).
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
	payload, err := json.Marshal(req)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	raw, err := s.jobs.EnqueueAndAwait(r.Context(), ai.KindLLMGenerate, jobs.PriorityInteractive, payload)
	if err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	var result llmGenerateResult
	if err := json.Unmarshal(raw, &result); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]string{"text": strings.TrimSpace(result.Text)}) //nolint:errcheck
}
