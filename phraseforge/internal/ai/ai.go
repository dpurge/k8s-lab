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
var ValidKinds = []string{"translation", "transcription", "title", "process_text", "process_dialog", "generate_vocabulary", "generate_models"}

// IsValidKind reports whether kind is one of ValidKinds.
func IsValidKind(kind string) bool {
	return slices.Contains(ValidKinds, kind)
}

type Prompt struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	Provider       string `json:"provider"`
	Model          string `json:"model"`
	Think          bool   `json:"think"`
	Prompt         string `json:"prompt"`
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

// ItemTranscriptionWriteback is the consumer-side interface HandleGenerate
// writes a completed transcription back through for a vocabulary/models
// item — addressed by (listID, position) rather than its own independent
// row id. Satisfied structurally by both *vocabulary.Store and
// *models.Store (their SetItemTranscriptionIfBlank methods already match
// this signature). phrase is the item's phrase as of when the job was
// enqueued (generatePayload.Content, for an item resource type — see
// writeback's own doc comment on why no separate field was needed); the
// underlying store returns an error if the item at (listID, position) no
// longer has that phrase, so a job that outlives a position shift
// (reorder/delete/re-import) fails instead of silently landing on the wrong
// item (see B3 in specs/features/phraseforge-export-import.md). applied is
// false (not an error) when the item's transcription already had content.
type ItemTranscriptionWriteback interface {
	SetItemTranscriptionIfBlank(ctx context.Context, listID int64, position int, phrase, transcription string) (applied bool, err error)
}

// ItemTranslationWriteback is the consumer-side interface HandleGenerate
// writes a completed translation back through for a vocabulary/models item.
// Unlike TranslationWriteback, one instance of this interface must dispatch
// to different underlying stores depending on resourceType (vocabulary vs.
// models), so — unlike every other writeback interface in this file — it is
// satisfied by a small adapter built in main.go, where both *vocabulary.Store
// and *models.Store are already imported, rather than directly by either
// store. phrase carries the same stale-target guard as
// ItemTranscriptionWriteback's own phrase parameter — see that field's doc
// comment. applied is false (not an error) when the item already had a
// translation for that locale.
type ItemTranslationWriteback interface {
	SetItemTranslation(ctx context.Context, resourceType string, listID int64, position int, phrase, locale, translation string) (applied bool, err error)
}

type Service struct {
	db  *pgxpool.Pool
	cfg config.Config

	// texts/dialogs/translations/vocabItems/modelsItems/itemTranslations
	// back a background job's writeback dispatch (see HandleGenerate) —
	// nil-safe only when ResourceID is always 0 (today's interactive-only
	// callers); a background caller must pass real stores via New.
	texts            TextWriteback
	dialogs          TextWriteback
	translations     TranslationWriteback
	vocabItems       ItemTranscriptionWriteback
	modelsItems      ItemTranscriptionWriteback
	itemTranslations ItemTranslationWriteback
}

func New(db *pgxpool.Pool, cfg config.Config, texts, dialogs TextWriteback, translations TranslationWriteback, vocabItems, modelsItems ItemTranscriptionWriteback, itemTranslations ItemTranslationWriteback) *Service {
	return &Service{
		db:               db,
		cfg:              cfg,
		texts:            texts,
		dialogs:          dialogs,
		translations:     translations,
		vocabItems:       vocabItems,
		modelsItems:      modelsItems,
		itemTranslations: itemTranslations,
	}
}

func (s *Service) ListPrompts(ctx context.Context) ([]Prompt, error) {
	rows, err := s.db.Query(ctx, `SELECT kind, source_language, target_language, coalesce(provider, ''), coalesce(model, ''), think, prompt, timeout_seconds FROM llm_prompts ORDER BY kind, source_language, target_language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.Kind, &p.SourceLanguage, &p.TargetLanguage, &p.Provider, &p.Model, &p.Think, &p.Prompt, &p.TimeoutSeconds); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) SetPrompt(ctx context.Context, p Prompt) error {
	_, err := s.db.Exec(ctx, `INSERT INTO llm_prompts(kind,source_language,target_language,provider,model,think,prompt,timeout_seconds) VALUES($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT(kind,source_language,target_language) DO UPDATE SET provider=excluded.provider, model=excluded.model, think=excluded.think, prompt=excluded.prompt, timeout_seconds=excluded.timeout_seconds, updated_at=now()`, p.Kind, p.SourceLanguage, p.TargetLanguage, strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model), p.Think, p.Prompt, p.TimeoutSeconds)
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
	var promptText string
	var timeoutSeconds *int
	err := s.db.QueryRow(ctx, `SELECT coalesce(provider, ''), coalesce(model, ''), think, prompt, timeout_seconds FROM llm_prompts WHERE kind=$1 AND source_language=$2 AND target_language=$3`, kind, source, target).Scan(&provider, &model, &think, &promptText, &timeoutSeconds)
	switch {
	case err == nil:
		if strings.TrimSpace(provider) != "" {
			p.Provider = provider
		}
		if strings.TrimSpace(model) != "" {
			p.Model = model
		}
		p.Think = think
		p.Prompt = promptText
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
	// def.Prompt (config.yaml, previously a hardcoded switch here — see
	// llm-purpose-timeout-and-prompt-config) is the fallback whenever no
	// admin override supplied a non-blank prompt of its own.
	if strings.TrimSpace(p.Prompt) == "" {
		p.Prompt = def.Prompt
	}
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

func (s *Service) Generate(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content is required")
	}
	def := s.purposeDefault(kind)
	prompt := s.prompt(ctx, kind, sourceLanguage, targetLanguage)
	user := strings.NewReplacer(
		"{{source_language}}", sourceLanguage,
		"{{target_language}}", targetLanguage,
		"{{content_type}}", contentType,
		"{{content}}", content,
	).Replace(userMessageTemplate)
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
	}
	start := time.Now()
	out, err := llm.New(clientCfg).Complete(ctx, []llm.Message{{Role: "system", Content: prompt.Prompt}, {Role: "user", Content: user}})
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	// Telemetry only — never log content or the response text.
	slog.Info("llm call", "kind", kind, "provider", prompt.Provider, "model", prompt.Model, "timeout_seconds", timeoutSeconds, "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
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
	// ("text", "dialog", "vocabulary_item", or "models_item"); a zero
	// ResourceID means today's interactive path — no writeback, result is
	// only returned. Locale is used for a "translation" writeback only.
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	Locale       string `json:"locale,omitempty"`

	// ItemPosition is the item's position within its list — only meaningful
	// when ResourceType is "vocabulary_item"/"models_item" (ResourceID is
	// then the list id, since a vocabulary/models item has no independent id
	// of its own). A text/dialog payload never sets this; resource type
	// alone, not a zero-check, is what disambiguates whether it applies,
	// since position 0 is a valid real position for an item.
	ItemPosition int `json:"item_position,omitempty"`
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
	text, err := s.Generate(ctx, p.Kind, p.SourceLanguage, p.TargetLanguage, p.ContentType, p.Content)
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
// failure. translations.SetIfAbsent and the *Store.SetTranscriptionIfBlank/
// SetItemTranscriptionIfBlank/SetItemTranslationIfAbsent methods below all
// treat an empty body as nothing to write — so an empty or whitespace-only
// LLM result reaching any of them here would either silently no-op or (worse,
// for the plain unconditional paths this function no longer uses) destroy a
// previously-good value. Guard against that by treating an empty result as
// this job's failure instead, before any writeback call: the job then
// correctly shows as failed/retryable, and nothing already stored is
// touched.
//
// For a vocabulary_item/models_item resource type, p.Content is already the
// item's phrase as of when this job was enqueued (see
// export_import.go's buildBackfillPayload, which sets Content to the item's
// phrase for every item-level decision) — passed through to
// SetItemTranscriptionIfBlank/SetItemTranslation as their stale-target guard
// rather than adding a separate payload field for it (see B3 in
// specs/features/phraseforge-export-import.md).
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
		if store := s.itemTranscriptionWriteback(p.ResourceType); store != nil {
			return store.SetItemTranscriptionIfBlank(ctx, p.ResourceID, p.ItemPosition, p.Content, trimmed)
		}
		store := s.textWriteback(p.ResourceType)
		if store == nil {
			return false, fmt.Errorf("ai: unknown resource_type %q for transcription writeback", p.ResourceType)
		}
		return store.SetTranscriptionIfBlank(ctx, p.ResourceID, trimmed)
	case "translation":
		if p.ResourceType == "vocabulary_item" || p.ResourceType == "models_item" {
			if s.itemTranslations == nil {
				return false, fmt.Errorf("ai: no item translation writeback configured for resource_type %q", p.ResourceType)
			}
			return s.itemTranslations.SetItemTranslation(ctx, p.ResourceType, p.ResourceID, p.ItemPosition, p.Content, p.Locale, trimmed)
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

func (s *Service) itemTranscriptionWriteback(resourceType string) ItemTranscriptionWriteback {
	switch resourceType {
	case "vocabulary_item":
		return s.vocabItems
	case "models_item":
		return s.modelsItems
	default:
		return nil
	}
}
