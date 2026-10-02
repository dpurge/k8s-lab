package ai

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"k8s-lab/shared/llm"

	"phraseforge/internal/config"
)

// Scripted replies that make fakeOllama answer with an HTTP error instead.
const (
	replyServerError = "<<500>>"
	replyBadRequest  = "<<400>>"
)

// fakeOllama answers each /api/chat request with the next scripted reply
// (streamed as one content chunk plus the done chunk) and records how many
// messages each request carried and the last message's text.
type fakeOllama struct {
	*httptest.Server
	mu       sync.Mutex
	msgCount []int
	lastText []string
}

func newFakeOllama(t *testing.T, replies []string) *fakeOllama {
	t.Helper()
	f := &fakeOllama{}
	f.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Messages []struct{ Role, Content string } `json:"messages"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			t.Errorf("fake ollama: bad request body: %v", err)
		}
		f.mu.Lock()
		i := len(f.msgCount)
		f.msgCount = append(f.msgCount, len(in.Messages))
		f.lastText = append(f.lastText, in.Messages[len(in.Messages)-1].Content)
		f.mu.Unlock()
		switch replies[min(i, len(replies)-1)] {
		case replyServerError:
			http.Error(w, "boom", http.StatusInternalServerError)
			return
		case replyBadRequest:
			http.Error(w, "bad", http.StatusBadRequest)
			return
		}
		if i >= len(replies) {
			http.Error(w, "unexpected extra call", http.StatusInternalServerError)
			return
		}
		fmt.Fprintf(w, `{"message":{"role":"assistant","content":%q},"done":false}`+"\n", replies[i])
		fmt.Fprintln(w, `{"message":{"role":"assistant","content":""},"done":true,"done_reason":"stop"}`)
	}))
	t.Cleanup(f.Close)
	return f
}

func correctionService(baseURL string) *Service {
	return &Service{cfg: config.Config{Providers: map[string]config.ProviderConfig{"ollama": {BaseURL: baseURL}}}}
}

func TestCallItemWithCorrectionFixesMismatchedPhrase(t *testing.T) {
	fake := newFakeOllama(t, []string{
		`{"phrase":"Koffer","translation":"walizka"}`,
		`{"phrase":"der Koffer","translation":"walizka"}`,
	})
	s := correctionService(fake.URL)
	p := vocabPayload("der Koffer")
	prompt := Prompt{Provider: "ollama", Model: "test-model"}
	msgs := []llm.Message{{Role: "user", Content: "translate der Koffer"}}

	raw, err := s.callItemWithCorrection(context.Background(), p, prompt, config.PurposeConfig{MaxAttempts: 3}, VocabularyItemSchema, msgs, deuSections)
	if err != nil {
		t.Fatalf("err = %v, want nil after the corrected reply", err)
	}
	if !strings.Contains(raw, `"der Koffer"`) {
		t.Errorf("raw = %q, want the corrected reply", raw)
	}
	if len(fake.msgCount) != 2 || fake.msgCount[0] != 1 || fake.msgCount[1] != 3 {
		t.Errorf("messages per request = %v, want [1 3] (original, then + assistant + user)", fake.msgCount)
	}
	if !strings.Contains(fake.lastText[1], `does not match the item's phrase "der Koffer"`) {
		t.Errorf("correction turn = %q, want the exact phrase-mismatch error", fake.lastText[1])
	}
}

func TestCallItemWithCorrectionGivesUpAfterBudget(t *testing.T) {
	bad := `{"phrase":"Koffer","translation":"walizka"}`
	fake := newFakeOllama(t, []string{bad, bad, bad, bad})
	s := correctionService(fake.URL)
	prompt := Prompt{Provider: "ollama", Model: "test-model"}

	_, err := s.callItemWithCorrection(context.Background(), vocabPayload("der Koffer"), prompt, config.PurposeConfig{MaxAttempts: 3}, VocabularyItemSchema,
		[]llm.Message{{Role: "user", Content: "translate"}}, deuSections)
	if err == nil || !strings.HasPrefix(err.Error(), "after 3 attempts: ") {
		t.Fatalf("err = %v, want 'after 3 attempts: ...'", err)
	}
	if len(fake.msgCount) != 3 {
		t.Errorf("requests = %d, want exactly 3", len(fake.msgCount))
	}
}

func retryingService(baseURL string, rec *sleepRecorder) *Service {
	s := correctionService(baseURL)
	s.sleep = rec.sleep
	return s
}

func TestCallWithRetryRecoversFromServerError(t *testing.T) {
	fake := newFakeOllama(t, []string{replyServerError, "bonjour"})
	rec := &sleepRecorder{}
	s := retryingService(fake.URL, rec)

	got, err := s.callWithRetry(context.Background(), "translation", Prompt{Provider: "ollama", Model: "test-model"}, config.PurposeConfig{MaxAttempts: 3},
		[]llm.Message{{Role: "user", Content: "translate"}})
	if err != nil || got != "bonjour" {
		t.Fatalf("got (%q, %v), want (bonjour, nil)", got, err)
	}
	if len(fake.msgCount) != 2 || len(rec.waits) != 1 || rec.waits[0] != 5*time.Second {
		t.Errorf("requests = %d, waits = %v, want 2 requests after one 5s wait", len(fake.msgCount), rec.waits)
	}
}

func TestCallWithRetryGivesUpAfterBudget(t *testing.T) {
	fake := newFakeOllama(t, []string{replyServerError})
	rec := &sleepRecorder{}
	s := retryingService(fake.URL, rec)

	_, err := s.callWithRetry(context.Background(), "translation", Prompt{Provider: "ollama", Model: "test-model"}, config.PurposeConfig{MaxAttempts: 3},
		[]llm.Message{{Role: "user", Content: "translate"}})
	if err == nil || !strings.HasPrefix(err.Error(), "after 3 attempts: ") || !strings.Contains(err.Error(), "500") {
		t.Fatalf("err = %v, want 'after 3 attempts: ...' naming the 500", err)
	}
	if len(fake.msgCount) != 3 || len(rec.waits) != 2 {
		t.Errorf("requests = %d, waits = %v, want 3 requests and 2 waits", len(fake.msgCount), rec.waits)
	}
}

func TestCallWithRetryDoesNotRetryClientErrors(t *testing.T) {
	fake := newFakeOllama(t, []string{replyBadRequest})
	rec := &sleepRecorder{}
	s := retryingService(fake.URL, rec)

	_, err := s.callWithRetry(context.Background(), "translation", Prompt{Provider: "ollama", Model: "test-model"}, config.PurposeConfig{MaxAttempts: 3},
		[]llm.Message{{Role: "user", Content: "translate"}})
	if err == nil || len(fake.msgCount) != 1 || len(rec.waits) != 0 {
		t.Errorf("err = %v, requests = %d, waits = %v, want a single request and no wait", err, len(fake.msgCount), rec.waits)
	}
}

// TestItemCallSharesBudgetAcrossServerErrorAndBadPhrase: a 500, then a reply
// with the wrong phrase, then the right one — three attempts, one budget.
func TestItemCallSharesBudgetAcrossServerErrorAndBadPhrase(t *testing.T) {
	fake := newFakeOllama(t, []string{
		replyServerError,
		`{"phrase":"Koffer","translation":"walizka"}`,
		`{"phrase":"der Koffer","translation":"walizka"}`,
	})
	rec := &sleepRecorder{}
	s := retryingService(fake.URL, rec)

	_, err := s.callItemWithCorrection(context.Background(), vocabPayload("der Koffer"), Prompt{Provider: "ollama", Model: "test-model"},
		config.PurposeConfig{MaxAttempts: 3}, VocabularyItemSchema, []llm.Message{{Role: "user", Content: "translate der Koffer"}}, deuSections)
	if err != nil {
		t.Fatalf("err = %v, want success on the third attempt", err)
	}
	if want := []int{1, 1, 3}; !reflect.DeepEqual(fake.msgCount, want) {
		t.Errorf("messages per request = %v, want %v", fake.msgCount, want)
	}
}
