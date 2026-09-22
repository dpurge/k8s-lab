package translate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"knowledge/internal/config"
)

func TestTranslateRejectsEmptyText(t *testing.T) {
	svc := New(config.Config{KnowledgeLanguage: "English"})
	if _, err := svc.Translate(context.Background(), "   "); !errors.Is(err, ErrEmptyText) {
		t.Errorf("Translate(whitespace) error = %v, want ErrEmptyText", err)
	}
}

// TestTranslateSubstitutesAllLanguageOccurrences exercises the real
// Translate() path against a stub Ollama server, so it covers the actual
// strings.ReplaceAll substitution rather than reimplementing it.
func TestTranslateSubstitutesAllLanguageOccurrences(t *testing.T) {
	var gotSystemPrompt string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		gotSystemPrompt = body.Messages[0].Content
		json.NewEncoder(w).Encode(map[string]any{"message": map[string]string{"content": "stub"}})
	}))
	defer server.Close()

	svc := New(config.Config{
		KnowledgeLanguage: "French",
		TranslateBaseURL:  server.URL,
		TranslatePrompt:   "already in {{language}}, unchanged. Otherwise into {{language}}.",
	})
	if _, err := svc.Translate(context.Background(), "hello"); err != nil {
		t.Fatalf("Translate() error = %v", err)
	}

	want := "already in French, unchanged. Otherwise into French."
	if gotSystemPrompt != want {
		t.Errorf("system prompt sent to model = %q, want %q", gotSystemPrompt, want)
	}
	if strings.Contains(gotSystemPrompt, "{{language}}") {
		t.Errorf("system prompt still contains an unsubstituted {{language}} placeholder: %q", gotSystemPrompt)
	}
}
