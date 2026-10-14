package ai

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"log/slog"
	"slices"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/llm"

	"phraseforge/internal/config"
	"phraseforge/internal/jobs"
)

// KindLLMGenerate is the historical jobs.Service kind for HandleGenerate —
// still registered (see main.go) so a pre-existing pending/failed row (or
// a Retry of one) keeps working. New callers use JobKind's field-specific
// kinds instead — all four are registered against the exact same
// HandleGenerate handler; only the job's own Kind column differs, giving
// the Jobs page real per-field traceability instead of one generic
// bucket, matching the same "same handler, several kind values" pattern
// generate.Service's own KindGenerateVocabFromText/FromDialog pair already
// established.
const KindLLMGenerate = "llm_generate"

// KindGenerateTitle/KindGenerateTranscription/KindGenerateTranslation are
// JobKind's three field-specific kinds — see KindLLMGenerate's doc comment.
const (
	KindGenerateTitle         = "generate_title"
	KindGenerateTranscription = "generate_transcription"
	KindGenerateTranslation   = "generate_translation"
)

// JobKind maps a generatePayload.Kind value ("title"/"transcription"/
// "translation") to the jobs.Service kind a caller should enqueue it
// under — the single place this mapping lives, so every enqueue call site
// (export/import backfill, the View-page Generate buttons, item-level
// generation) stays in sync. Falls back to KindLLMGenerate for anything
// else, preserving this package's existing lenient-unrecognized-kind
// behavior (see purposeDefault's own doc comment).
func JobKind(payloadKind string) string {
	switch payloadKind {
	case "title":
		return KindGenerateTitle
	case "transcription":
		return KindGenerateTranscription
	case "translation":
		return KindGenerateTranslation
	default:
		return KindLLMGenerate
	}
}

// ValidKinds lists every kind Generate supports — the single authoritative
// source for what the llm_prompts.kind CHECK constraint (schema.sql), the
// admin LLM-prompt API validators (server/admin.go's
// apiSetAdminLLMPrompt/apiImportAdminConfig), and the admin UI's kind
// <select> (populated from apiAdminBootstrap, which reads this slice) must
// all agree with — a hardcoded copy in any of those places is exactly how
// they drifted apart before.
var ValidKinds = []string{"translation", "transcription", "title", "process_text", "process_dialog", "generate_vocabulary", "generate_models", "vocabulary_item", "models_item", "check_vocabulary", "review_vocabulary", "review_transcription", "review_translation", "review_models"}

// IsValidKind reports whether kind is one of ValidKinds.
func IsValidKind(kind string) bool {
	return slices.Contains(ValidKinds, kind)
}

// DefaultPrompts maps every kind in ValidKinds to the system prompt it uses
// when no admin row overrides it (config.yaml's, else the embedded default),
// so the admin UI can start a new prompt from it.
func (s *Service) DefaultPrompts() map[string]string {
	defaults := make(map[string]string, len(ValidKinds))
	for _, kind := range ValidKinds {
		defaults[kind] = s.purposeDefault(kind).Prompt
	}
	return defaults
}

// PromptPlaceholders lists the {{placeholders}} a kind's prompt may use: the
// item prompts take the item vars too (a vocabulary item also its usage and
// examples), every other kind only the two snippets.
func PromptPlaceholders(kind string) []string {
	snippets := []string{"grammarPrompt", "transcriptionPrompt", "dictionaryPrompt"}
	switch kind {
	case "vocabulary_item":
		return append([]string{"sourceLanguage", "targetLanguage", "phrase"}, append(snippets, "usage", "examples", "translation")...)
	case "models_item":
		return append([]string{"sourceLanguage", "targetLanguage", "phrase"}, snippets...)
	case "check_vocabulary":
		return []string{"sourceLanguage", "candidates"}
	case "review_transcription":
		return []string{"sourceLanguage", "text", "transcription", "vocabulary", "transcriptionPrompt"}
	case "review_translation":
		return []string{"sourceLanguage", "text", "translations", "vocabulary"}
	case "review_models":
		return []string{"sourceLanguage", "phrase", "transcription", "translations", "transcriptionPrompt"}
	case "review_vocabulary":
		return []string{"sourceLanguage", "phrase", "grammar", "transcription", "examples", "translations", "dictionaryPrompt", "grammarPrompt", "transcriptionPrompt"}
	}
	return snippets
}

type Prompt struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Think          bool   `json:"think"`
	// Prompt is the system prompt in effect for the kind. It is never stored
	// per row: language rules live only in language_llm_sections, the text only
	// in the purpose default.
	Prompt string `json:"-"`
	// TimeoutSeconds is nil when this row has no override (the default for
	// every pre-existing row) — "inherit the purpose default", not "no
	// timeout". See resolveTimeoutSeconds.
	TimeoutSeconds *int `json:"timeout_seconds,omitempty"`
}

// TextWriteback is the consumer-side interface HandleGenerate writes a
// completed title/transcription back through — satisfied structurally by
// both *texts.Store and *dialogs.Store (their SetTitleIfBlank/
// SetTranscriptionIfBlank methods already match this signature), so ai
// never imports either package directly. applied is false (not an error)
// when the target field already had content — background-generate-title-
// transcription-translation's universal blank-check rule.
type TextWriteback interface {
	SetTitleIfBlank(ctx context.Context, id int64, title string) (applied bool, err error)
	SetTranscriptionIfBlank(ctx context.Context, id int64, transcription string) (applied bool, err error)
}

// TranslationWriteback is the consumer-side interface HandleGenerate writes
// a completed translation back through — satisfied structurally by
// *translations.Store (its Set method, used by the interactive edit-form
// save paths, is untouched and unused here). applied follows
// TextWriteback's own blank-check convention (a translation already present
// for that locale is a skip, not a failure).
type TranslationWriteback interface {
	SetIfAbsent(ctx context.Context, resourceType string, resourceID int64, locale, text string) (applied bool, err error)
}

// ErrItemGone and ErrItemChanged are what an ItemStore returns when the item
// an item job targets no longer exists, or no longer has the phrase the job
// started from. They wrap jobs.ErrSkipped, so the job is recorded as cancelled
// with the reason rather than failed: nothing went wrong, the list just
// changed after the job was queued.
var (
	ErrItemGone    = fmt.Errorf("%w: item no longer exists", jobs.ErrSkipped)
	ErrItemChanged = fmt.Errorf("%w: item changed while the job was running", jobs.ErrSkipped)
)

// ItemSource is what an item job reads from its item when it starts.
type ItemSource struct {
	Phrase   string
	Language string   // the source language of the item's list
	Examples []string // the phrase's usage examples in order (vocabulary items only)
	// Translations are the phrase's non-blank translations by site locale
	// code (vocabulary items only).
	Translations map[string]string
}

// ItemStore is the consumer-side interface of everything HandleItemTranslation
// reads and writes for a vocabulary/models item, addressed by the item's id.
// One instance dispatches on resourceType to the store that owns the item's
// rows, so it is satisfied by an adapter built in main.go, where both stores
// are already imported. Every write fills only what is blank (applied is
// false, not an error, when the field already had a value) and only while the
// item still has phrase, the phrase the job started from; otherwise it returns
// ErrItemGone or ErrItemChanged. Models items have no grammar and no notes.
type ItemStore interface {
	ItemSource(ctx context.Context, resourceType string, itemID int64) (ItemSource, error)
	FillTranslation(ctx context.Context, resourceType string, itemID int64, phrase, locale, translation, notes string) (applied bool, err error)
	FillTranscription(ctx context.Context, resourceType string, itemID int64, phrase, transcription string) (applied bool, err error)
	FillGrammar(ctx context.Context, itemID int64, phrase, grammar string) (applied bool, err error)
	// FillExample adds a usage example to a vocabulary item's phrase if there
	// is room and it is new. Only vocabulary items have examples.
	FillExample(ctx context.Context, itemID int64, phrase, example string) (applied bool, err error)
	// RewritePhrase replaces a vocabulary item's phrase, with its grammar and
	// transcription (empty keeps the stored one), if the item still has phrase
	// and newPhrase differs from it. Only vocabulary items have it.
	RewritePhrase(ctx context.Context, itemID int64, phrase, newPhrase, grammar, transcription string) (applied bool, err error)
	// AppendSense adds sense to the end of a vocabulary item's existing
	// translation in locale ("old; sense") if the item still has phrase, the
	// translation is not blank and does not already have that sense. Only
	// vocabulary items have it.
	AppendSense(ctx context.Context, itemID int64, phrase, locale, sense string) (applied bool, err error)
}

type Service struct {
	db  *pgxpool.Pool
	cfg config.Config

	// texts/dialogs/translations back HandleGenerate's writeback dispatch —
	// nil-safe only when ResourceID is always 0 (the interactive-only
	// callers); a background caller must pass real stores via New.
	texts        TextWriteback
	dialogs      TextWriteback
	translations TranslationWriteback
	// items backs HandleItemTranslation.
	items ItemStore
	// modelsReviews backs HandleReviewModelsItem (see SetModelsReviewStore).
	modelsReviews ReviewStore
	// reviews backs HandleReviewItem (see SetReviewStore).
	reviews ReviewStore
	// textReviews backs HandleReviewResource (see SetTextReviewStore).
	textReviews TextReviewStore

	// sleep waits out the backoff between retries of a transient LLM failure;
	// nil means defaultSleep. Tests swap it so they never really wait.
	sleep sleepFunc
}

func New(db *pgxpool.Pool, cfg config.Config, texts, dialogs TextWriteback, translations TranslationWriteback, items ItemStore) *Service {
	return &Service{
		db:           db,
		cfg:          cfg,
		texts:        texts,
		dialogs:      dialogs,
		translations: translations,
		items:        items,
	}
}

func (s *Service) ListPrompts(ctx context.Context) ([]Prompt, error) {
	rows, err := s.db.Query(ctx, `SELECT kind, source_language, target_language, coalesce(provider, ''), coalesce(model, ''), think, timeout_seconds FROM llm_prompts ORDER BY kind, source_language, target_language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.Kind, &p.SourceLanguage, &p.TargetLanguage, &p.Provider, &p.Model, &p.Think, &p.TimeoutSeconds); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) SetPrompt(ctx context.Context, p Prompt) error {
	_, err := s.db.Exec(ctx, `INSERT INTO llm_prompts(kind,source_language,target_language,provider,model,think,timeout_seconds) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(kind,source_language,target_language) DO UPDATE SET provider=excluded.provider, model=excluded.model, think=excluded.think, timeout_seconds=excluded.timeout_seconds, updated_at=now()`, p.Kind, p.SourceLanguage, p.TargetLanguage, strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model), p.Think, p.TimeoutSeconds)
	return err
}

func (s *Service) DeletePrompt(ctx context.Context, kind, sourceLanguage, targetLanguage string) error {
	_, err := s.db.Exec(ctx, `DELETE FROM llm_prompts WHERE kind=$1 AND source_language=$2 AND target_language=$3`, kind, sourceLanguage, targetLanguage)
	return err
}

// purposeDefault returns kind's config.yaml default. Any kind other than
// those Generate supports (see ValidKinds) falls back to Translation,
// preserving the lenient behavior this file always had for an unrecognized
// kind.
func (s *Service) purposeDefault(kind string) config.PurposeConfig {
	switch kind {
	case "transcription":
		return s.cfg.Transcription
	case "title":
		return s.cfg.Title
	case "process_text":
		return s.cfg.ProcessText
	case "process_dialog":
		return s.cfg.ProcessDialog
	case "generate_vocabulary":
		return s.cfg.GenerateVocabulary
	case "generate_models":
		return s.cfg.GenerateModels
	case "vocabulary_item":
		return s.cfg.VocabularyItem
	case "models_item":
		return s.cfg.ModelsItem
	case "check_vocabulary":
		return s.cfg.CheckVocabulary
	case "review_vocabulary":
		return s.cfg.ReviewVocabulary
	case "review_models":
		return s.cfg.ReviewModels
	case "review_transcription":
		return s.cfg.ReviewTranscription
	case "review_translation":
		return s.cfg.ReviewTranslation
	case "translation":
		return s.cfg.Translation
	default:
		return s.cfg.Translation
	}
}

func (s *Service) prompt(ctx context.Context, kind, source, target string) Prompt {
	def := s.purposeDefault(kind)
	p := Prompt{Kind: kind, SourceLanguage: source, TargetLanguage: target, Provider: def.Provider, Model: def.Model, Think: def.Think}
	var provider, model string
	var think bool
	var timeoutSeconds *int
	err := s.db.QueryRow(ctx, `SELECT coalesce(provider, ''), coalesce(model, ''), think, timeout_seconds FROM llm_prompts WHERE kind=$1 AND source_language=$2 AND target_language=$3`, kind, source, target).Scan(&provider, &model, &think, &timeoutSeconds)
	switch {
	case err == nil:
		if strings.TrimSpace(provider) != "" {
			p.Provider = provider
		}
		if strings.TrimSpace(model) != "" {
			p.Model = model
		}
		p.Think = think
		p.TimeoutSeconds = timeoutSeconds
	case errors.Is(err, pgx.ErrNoRows):
		// No admin-configured override for this (kind, source, target) —
		// expected; fall through to config.yaml's defaults below.
	default:
		// Unexpected DB error (connection issue, etc.) — log it so an
		// operator gets a signal, but still fall through to defaults so
		// Generate() doesn't hard-fail on a transient DB problem.
		log.Printf("ai: lookup llm_prompts override for kind=%s source=%s target=%s: %v", kind, source, target, err)
	}
	// The prompt text always comes from the purpose default: a row overrides
	// only provider, model, think and timeout, so language rules have one home
	// (language_llm_sections).
	p.Prompt = def.Prompt
	return p
}

// resolveTimeoutSeconds picks the effective per-call timeout, in seconds:
// an admin llm_prompts.timeout_seconds override (if set) beats the
// purpose's own config.yaml default (if set), which beats shared/llm's own
// built-in default (signaled by returning 0 here — llm.New treats a zero
// Config.Timeout as "apply my own default", so no third fallback value
// needs to be threaded through this function). A pure function, kept next
// to purposeDefault so both are unit-testable without a database.
func resolveTimeoutSeconds(override *int, purposeDefaultSeconds int) int {
	if override != nil {
		return *override
	}
	return purposeDefaultSeconds
}

// userMessageTemplate wraps Generate's caller-supplied content with the
// metadata every LLM call needs alongside it. Named placeholders
// ({{source_language}}, not %s/fmt.Sprintf) — this is prompt text, and the
// user's own stated preference is for prompt/template text to use named
// tokens, matching knowledge/internal/translate/translate.go's existing
// {{language}} convention.
const userMessageTemplate = "Source language: {{source_language}}\nTarget language: {{target_language}}\nContent type: {{content_type}}\n\n{{content}}"

// generateDefault is kind's config default as Generate calls it: with the
// context window explicit, so the window the chunks were sized for is the
// window the call runs under (see defaultNumCtx).
func (s *Service) generateDefault(kind string) config.PurposeConfig {
	def := s.purposeDefault(kind)
	def.NumCtx = effectiveNumCtx(def.NumCtx)
	return def
}

func (s *Service) Generate(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content is required")
	}
	def := s.generateDefault(kind)
	prompt := s.prompt(ctx, kind, sourceLanguage, targetLanguage)
	system, err := s.renderSystemPrompt(ctx, prompt.Prompt, sourceLanguage)
	if err != nil {
		return "", err
	}
	user := strings.NewReplacer(
		"{{source_language}}", sourceLanguage,
		"{{target_language}}", targetLanguage,
		"{{content_type}}", contentType,
		"{{content}}", content,
	).Replace(userMessageTemplate)
	return s.callWithRetry(ctx, kind, prompt, def, []llm.Message{{Role: "system", Content: system}, {Role: "user", Content: user}})
}

// callWithRetry is callLLM with a retry of transient failures (see
// runWithCorrection) within the purpose's MaxAttempts budget. Any reply is
// accepted: Generate's output is free text with nothing to validate.
func (s *Service) callWithRetry(ctx context.Context, kind string, prompt Prompt, def config.PurposeConfig, messages []llm.Message) (string, error) {
	maxAttempts := effectiveMaxAttempts(def.MaxAttempts)
	attempt := 0
	return runWithCorrection(ctx, maxAttempts, s.sleeper(),
		func(ctx context.Context, m []llm.Message) (string, error) {
			attempt++
			return s.callLLM(ctx, kind, prompt, def, m, nil, attempt, maxAttempts)
		},
		nil, messages)
}

// callLLM sends messages with prompt's resolved provider/model/think and
// timeouts (def supplies NumCtx and the purpose timeout), constraining the
// reply to format's JSON schema when non-nil, and logs one telemetry line.
// attempt and maxAttempts (1-based) are only reported in that line.
// Shared by Generate and HandleItemTranslation.
func (s *Service) callLLM(ctx context.Context, kind string, prompt Prompt, def config.PurposeConfig, messages []llm.Message, format json.RawMessage, attempt, maxAttempts int) (string, error) {
	provider, ok := s.cfg.Providers[prompt.Provider]
	if !ok {
		return "", fmt.Errorf("llm provider %q is not in the configured provider registry", prompt.Provider)
	}
	timeoutSeconds := resolveTimeoutSeconds(prompt.TimeoutSeconds, def.TimeoutSeconds)
	clientCfg := llm.Config{
		Provider: prompt.Provider,
		BaseURL:  provider.BaseURL,
		APIKey:   provider.APIKey,
		Model:    prompt.Model,
		NumCtx:   def.NumCtx,
		Think:    prompt.Think,
		Timeout:  time.Duration(timeoutSeconds) * time.Second,

		FirstTokenTimeout: time.Duration(provider.FirstTokenTimeoutSeconds) * time.Second,
		IdleTimeout:       time.Duration(provider.IdleTimeoutSeconds) * time.Second,
		Format:            format,
	}
	start := time.Now()
	resp, err := llm.New(clientCfg).Chat(ctx, messages, nil)
	out := resp.Content
	outcome, limit := "success", ""
	if err == nil {
		if overflow := contextOverflow(resp, def.NumCtx); overflow != nil {
			out, err, outcome = "", overflow, "context_overflow"
		}
	} else {
		outcome = "error"
		var te *llm.TimeoutError
		if errors.As(err, &te) {
			limit = te.Limit
		}
	}
	// Telemetry only — never log content or the response text.
	slog.Info("llm call", "kind", kind, "provider", prompt.Provider, "model", prompt.Model, "timeout_seconds", timeoutSeconds, "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome, "limit", limit, "attempt", attempt, "max_attempts", maxAttempts, "prompt_tokens", resp.PromptTokens, "reply_tokens", resp.ReplyTokens, "num_ctx", def.NumCtx)
	return out, err
}

// generatePayload is the jobs.Service payload shape for KindLLMGenerate —
// the same fields /llm/generate's HTTP request already carries, plus the
// optional resource fields a background caller (e.g. ingest) sets to have
// HandleGenerate write the result back instead of only returning it.
type generatePayload struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	ContentType    string `json:"content_type"`
	Content        string `json:"content"`

	// ResourceType/ResourceID identify the row to write the result back to
	// ("text" or "dialog"); a zero
	// ResourceID means today's interactive path — no writeback, result is
	// only returned. Locale is used for a "translation" writeback only.
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	Locale       string `json:"locale,omitempty"`
}

type generateResult struct {
	Text string `json:"text"`
	// Applied is omitted for the interactive (ResourceID == 0) path, where
	// there's nothing to write back. For a writeback path, false means the
	// target field already had content and this job's result was
	// deliberately not written — still job success, not failure (see
	// writeback's doc comment) — surfaced here so the Jobs page's result
	// view can distinguish "wrote it" from "skipped, already had a value".
	Applied *bool `json:"applied,omitempty"`
}

// HandleGenerate is the jobs.HandlerFunc implementation registered for
// KindLLMGenerate: it wraps Generate so /llm/generate's handler can run it
// through the job queue instead of calling Generate directly, serializing it
// against any future background job (ingest, vocabulary/models generation,
// etc.) rather than running fully concurrently with it. id (its own job's
// id, per jobs.HandlerFunc's signature) is unused here — only
// phraseforge/internal/ingest's handlers need it, for their retry-safety
// step check.
func (s *Service) HandleGenerate(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	var p generatePayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, err
	}
	text, err := s.GenerateChunked(ctx, p.Kind, p.SourceLanguage, p.TargetLanguage, p.ContentType, p.Content)
	if err != nil {
		return nil, err
	}
	result := generateResult{Text: text}
	if p.ResourceID != 0 {
		applied, err := s.writeback(ctx, p, text)
		if err != nil {
			return nil, err
		}
		result.Applied = &applied
	}
	return json.Marshal(result)
}

// writeback stores a completed generation against the resource it was
// generated for, but only if the target field is still blank/absent
// (background-generate-title-transcription-translation's universal rule) —
// "title" writes back only for a text/dialog resource type (vocabulary/
// models items have no title); "process_text"/"process_dialog" are still
// called directly by the ingest package (see phraseforge-ingest-texts-
// dialogs step 6), not routed through this job-queue path with a resource
// already in hand.
//
// applied is false, with a nil error, when the guarded setter found the
// target field already non-blank — that's job success (a no-op skip), not
// failure. translations.SetIfAbsent and the *Store.SetTranscriptionIfBlank
// methods below both treat an empty body as nothing to write — so an empty or whitespace-only
// LLM result reaching any of them here would either silently no-op or (worse,
// for the plain unconditional paths this function no longer uses) destroy a
// previously-good value. Guard against that by treating an empty result as
// this job's failure instead, before any writeback call: the job then
// correctly shows as failed/retryable, and nothing already stored is
// touched.
//
// Only text and dialog resources are written back here. A vocabulary/models
// item is filled by HandleItemTranslation (by item id) instead; a pre-existing
// item-level row of these kinds fails with an error rather than writing
// anywhere (translations.SetIfAbsent would otherwise store it under an item
// resource type it knows nothing about).
func (s *Service) writeback(ctx context.Context, p generatePayload, text string) (applied bool, err error) {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return false, fmt.Errorf("llm returned an empty result for kind %q", p.Kind)
	}
	switch p.Kind {
	case "title":
		store := s.textWriteback(p.ResourceType)
		if store == nil {
			return false, fmt.Errorf("ai: unknown resource_type %q for title writeback", p.ResourceType)
		}
		return store.SetTitleIfBlank(ctx, p.ResourceID, trimmed)
	case "transcription":
		store := s.textWriteback(p.ResourceType)
		if store == nil {
			return false, fmt.Errorf("ai: unknown resource_type %q for transcription writeback", p.ResourceType)
		}
		return store.SetTranscriptionIfBlank(ctx, p.ResourceID, trimmed)
	case "translation":
		if p.ResourceType != "text" && p.ResourceType != "dialog" {
			return false, fmt.Errorf("ai: unknown resource_type %q for translation writeback", p.ResourceType)
		}
		return s.translations.SetIfAbsent(ctx, p.ResourceType, p.ResourceID, p.Locale, trimmed)
	default:
		return false, nil
	}
}

func (s *Service) textWriteback(resourceType string) TextWriteback {
	switch resourceType {
	case "text":
		return s.texts
	case "dialog":
		return s.dialogs
	default:
		return nil
	}
}
