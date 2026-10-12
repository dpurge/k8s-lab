package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/config"
	"phraseforge/internal/jobs"
)

var aiDBSchemaCounter atomic.Int64

// newAIDB is a pool on a throwaway schema with schema.sql applied
// (PHRASEFORGE_TEST_DSN, skipped without it), for the tests that run
// HandleItemTranslation as a whole: it reads the language table, the language
// sections and the stored prompts.
func newAIDB(t *testing.T) *pgxpool.Pool {
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
	schema := fmt.Sprintf("pf_test_%d_%d", os.Getpid(), aiDBSchemaCounter.Add(1))
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

// runItemJob runs HandleItemTranslation for payload, with the vocabulary item
// prompt template set to template, the fake store answering for the item and a
// fake Ollama giving the scripted replies. It returns the store, Ollama, job
// result and error.
func runItemJob(t *testing.T, template string, source ItemSource, payload string, replies ...string) (*fakeItemStore, *fakeOllama, json.RawMessage, error) {
	t.Helper()
	pool := newAIDB(t)
	ollama := newFakeOllama(t, replies)
	t.Cleanup(ollama.Close)
	store := &fakeItemStore{source: source, translationApplied: true, exampleApplied: true}
	cfg := config.Config{
		Providers:      map[string]config.ProviderConfig{"ollama": {BaseURL: ollama.URL}},
		VocabularyItem: config.PurposeConfig{Provider: "ollama", Model: "test-model", Prompt: template, MaxAttempts: 3},
	}
	result, err := New(pool, cfg, nil, nil, nil, store).HandleItemTranslation(context.Background(), "job-1", json.RawMessage(payload))
	return store, ollama, result, err
}

const itemJobTemplate = "Translate {{phrase}} from {{sourceLanguage}} to {{targetLanguage}}.\nUsed as: {{usage}}\nKnown examples:\n{{examples}}\nEnd."

func TestItemJobShowsTheModelTheUsageAndExamplesAndStoresItsExample(t *testing.T) {
	source := ItemSource{Phrase: "der Koffer", Language: "deu", Examples: []string{"einen Koffer packen", "der große Koffer"}}
	payload := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl","usage":"der Koffer ist schwer"}`
	store, ollama, result, err := runItemJob(t, itemJobTemplate, source, payload,
		`{"phrase":"der Koffer","translation":"walizka","example":"ein schwerer Koffer"}`)
	if err != nil {
		t.Fatal(err)
	}

	want := "Translate der Koffer from German to Polish.\nUsed as: der Koffer ist schwer\nKnown examples:\n- einen Koffer packen\n- der große Koffer\nEnd."
	if len(ollama.lastText) != 1 || ollama.lastText[0] != want {
		t.Fatalf("the model was sent %q, want %q", ollama.lastText, want)
	}
	if got := strings.Join(store.calls, ","); got != "source:vocabulary_item,translation:vocabulary_item:walizka,example:ein schwerer Koffer" {
		t.Errorf("store calls = %s, want source, translation, then the example", got)
	}
	var res itemJobResult
	if err := json.Unmarshal(result, &res); err != nil || !res.Applied["example"] || !res.Applied["translation"] {
		t.Errorf("result = %s (err %v), want translation and example applied", result, err)
	}
}

// A template that does not use the new placeholders (every stored prompt, and
// the defaults) is sent exactly as before, usage and examples or not.
func TestItemJobWithATemplateWithoutTheNewPlaceholdersSendsItAsBefore(t *testing.T) {
	source := ItemSource{Phrase: "der Koffer", Language: "deu", Examples: []string{"einen Koffer packen"}}
	payload := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl","usage":"der Koffer ist schwer"}`
	_, ollama, _, err := runItemJob(t, "Translate {{phrase}} to {{targetLanguage}}.", source, payload,
		`{"phrase":"der Koffer","translation":"walizka"}`)
	if err != nil {
		t.Fatal(err)
	}
	if len(ollama.lastText) != 1 || ollama.lastText[0] != "Translate der Koffer to Polish." {
		t.Fatalf("the model was sent %q, want the template as it always rendered", ollama.lastText)
	}
}

// A reply whose example is blank, null, of the wrong type or too long still
// succeeds on the first call, with no example stored and no correction round.
func TestItemJobIgnoresAnExampleThatIsUnusableWithoutACorrectionRound(t *testing.T) {
	source := ItemSource{Phrase: "der Koffer", Language: "deu"}
	payload := `{"resource_type":"vocabulary_item","item_id":7,"locale":"pl"}`
	for name, example := range map[string]string{
		"blank":      `""`,
		"null":       `null`,
		"wrong type": `["a","b"]`,
		"too long":   `"` + strings.Repeat("x", MaxUsageChars+1) + `"`,
	} {
		store, ollama, result, err := runItemJob(t, itemJobTemplate, source, payload,
			`{"phrase":"der Koffer","translation":"walizka","example":`+example+`}`)
		if err != nil {
			t.Errorf("%s: err = %v, want the job to succeed", name, err)
			continue
		}
		if len(ollama.msgCount) != 1 {
			t.Errorf("%s: the model was called %d times, want 1 (no correction round)", name, len(ollama.msgCount))
		}
		for _, c := range store.calls {
			if strings.HasPrefix(c, "example:") {
				t.Errorf("%s: store calls = %v, want no example write", name, store.calls)
			}
		}
		var res itemJobResult
		if err := json.Unmarshal(result, &res); err != nil || res.Applied["example"] {
			t.Errorf("%s: result = %s (err %v), want example false", name, result, err)
		}
	}
}

func TestItemJobSkipsWhenTheItemIsGoneAtTheExampleWrite(t *testing.T) {
	pool := newAIDB(t)
	ollama := newFakeOllama(t, []string{`{"phrase":"der Koffer","translation":"walizka","example":"ein Koffer"}`})
	t.Cleanup(ollama.Close)
	store := &fakeItemStore{source: ItemSource{Phrase: "der Koffer", Language: "deu"}, translationApplied: true, exampleErr: ErrItemGone}
	cfg := config.Config{
		Providers:      map[string]config.ProviderConfig{"ollama": {BaseURL: ollama.URL}},
		VocabularyItem: config.PurposeConfig{Provider: "ollama", Model: "test-model", Prompt: itemJobTemplate, MaxAttempts: 3},
	}
	_, err := New(pool, cfg, nil, nil, nil, store).HandleItemTranslation(context.Background(), "job-1",
		json.RawMessage(`{"resource_type":"vocabulary_item","item_id":7,"locale":"pl"}`))
	if !errors.Is(err, jobs.ErrSkipped) {
		t.Fatalf("err = %v, want the job skipped (cancelled), not failed", err)
	}
}
