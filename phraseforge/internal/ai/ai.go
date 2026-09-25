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

// KindLLMGenerate is the jobs.Service kind registered for HandleGenerate.
// Exported so main.go can call jobsSvc.Register(ai.KindLLMGenerate,
// aiSvc.HandleGenerate).
const KindLLMGenerate = "llm_generate"

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
}

// TextWriteback is the consumer-side interface HandleGenerate writes a
// completed transcription back through — satisfied structurally by both
// *texts.Store and *dialogs.Store (their SetTranscription methods already
// match this signature), so ai never imports either package directly.
type TextWriteback interface {
	SetTitle(ctx context.Context, id int64, title string) error
	SetTranscription(ctx context.Context, id int64, transcription string) error
}

// TranslationWriteback is the consumer-side interface HandleGenerate writes
// a completed translation back through — satisfied structurally by
// *translations.Store.
type TranslationWriteback interface {
	Set(ctx context.Context, resourceType string, resourceID int64, locale, text string) error
}

// ItemTranscriptionWriteback is the consumer-side interface HandleGenerate
// writes a completed transcription back through for a vocabulary/models
// item — addressed by (listID, position) rather than its own independent
// row id. Satisfied structurally by both *vocabulary.Store and
// *models.Store (their SetItemTranscription methods already match this
// signature). phrase is the item's phrase as of when the job was enqueued
// (generatePayload.Content, for an item resource type — see writeback's own
// doc comment on why no separate field was needed); the underlying store
// only applies the write if the item at (listID, position) still has that
// phrase, so a job that outlives a position shift (reorder/delete/re-import)
// fails instead of silently landing on the wrong item (see B3 in
// specs/features/phraseforge-export-import.md).
type ItemTranscriptionWriteback interface {
	SetItemTranscription(ctx context.Context, listID int64, position int, phrase, transcription string) error
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
// comment.
type ItemTranslationWriteback interface {
	SetItemTranslation(ctx context.Context, resourceType string, listID int64, position int, phrase, locale, translation string) error
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
	rows, err := s.db.Query(ctx, `SELECT kind, source_language, target_language, coalesce(provider, ''), coalesce(model, ''), think, prompt FROM llm_prompts ORDER BY kind, source_language, target_language`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Prompt
	for rows.Next() {
		var p Prompt
		if err := rows.Scan(&p.Kind, &p.SourceLanguage, &p.TargetLanguage, &p.Provider, &p.Model, &p.Think, &p.Prompt); err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
func (s *Service) SetPrompt(ctx context.Context, p Prompt) error {
	_, err := s.db.Exec(ctx, `INSERT INTO llm_prompts(kind,source_language,target_language,provider,model,think,prompt) VALUES($1,$2,$3,$4,$5,$6,$7) ON CONFLICT(kind,source_language,target_language) DO UPDATE SET provider=excluded.provider, model=excluded.model, think=excluded.think, prompt=excluded.prompt, updated_at=now()`, p.Kind, p.SourceLanguage, p.TargetLanguage, strings.TrimSpace(p.Provider), strings.TrimSpace(p.Model), p.Think, p.Prompt)
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
	err := s.db.QueryRow(ctx, `SELECT coalesce(provider, ''), coalesce(model, ''), think, prompt FROM llm_prompts WHERE kind=$1 AND source_language=$2 AND target_language=$3`, kind, source, target).Scan(&provider, &model, &think, &promptText)
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
	case errors.Is(err, pgx.ErrNoRows):
		// No admin-configured override for this (kind, source, target) —
		// expected; fall through to config.yaml's defaults below.
	default:
		// Unexpected DB error (connection issue, etc.) — log it so an
		// operator gets a signal, but still fall through to defaults so
		// Generate() doesn't hard-fail on a transient DB problem.
		log.Printf("ai: lookup llm_prompts override for kind=%s source=%s target=%s: %v", kind, source, target, err)
	}
	if strings.TrimSpace(p.Prompt) != "" {
		return p
	}
	switch kind {
	case "transcription":
		p.Prompt = "Create a romanized transcription for the source language content. Return only the transcription, preserving line breaks and structure. Do not add explanations."
	case "title":
		p.Prompt = "You write a short, specific title for the given text. Respond with only the title text on a single line — no quotes, no punctuation at the end, no preamble."
	case "process_text":
		p.Prompt = "You reformat raw extracted text into clean Markdown prose suitable as a language-learning reading text. Remove navigation menus, ads, boilerplate, and unrelated content. Preserve the actual article/passage content and its paragraph structure. Do not translate or summarize. Respond with only the cleaned Markdown."
	case "process_dialog":
		p.Prompt = "You reformat raw extracted text into a clean dialog transcript in Markdown. Identify distinct speakers/turns and format each turn on its own line. Remove navigation, ads, and unrelated content. Respond with only the cleaned dialog content."
	case "generate_vocabulary":
		p.Prompt = "You extract vocabulary and grammar items from the given text for a language learner. Respond ONLY with one item per line, in this exact format: phrase {grammar} [transcription] = translation — where {grammar} is a short grammar tag (e.g. part of speech), [transcription] is a romanized reading, and = translation is the item's translation; each of {grammar}, [transcription], and = translation is optional and must be omitted entirely (not left as empty brackets) when not applicable. Do not add commentary, a preamble, numbering, or code fences — only the item lines themselves."
	case "generate_models":
		p.Prompt = "You extract short grammar/sentence-pattern models from the given text for a language learner. Respond ONLY with one item per line, in this exact format: phrase [transcription] = translation — where [transcription] is a romanized reading and = translation is the item's translation; each of [transcription] and = translation is optional and must be omitted entirely (not left as empty brackets) when not applicable. Do not add commentary, a preamble, numbering, or code fences — only the item lines themselves."
	default:
		p.Prompt = "Translate the source language content to the target language. Return only the translation, preserving line breaks and structure. Do not add explanations."
	}
	return p
}

func (s *Service) Generate(ctx context.Context, kind, sourceLanguage, targetLanguage, contentType, content string) (string, error) {
	if strings.TrimSpace(content) == "" {
		return "", fmt.Errorf("content is required")
	}
	def := s.purposeDefault(kind)
	prompt := s.prompt(ctx, kind, sourceLanguage, targetLanguage)
	user := fmt.Sprintf("Source language: %s\nTarget language: %s\nContent type: %s\n\n%s", sourceLanguage, targetLanguage, contentType, content)
	provider, ok := s.cfg.Providers[prompt.Provider]
	if !ok {
		return "", fmt.Errorf("llm provider %q is not in the configured provider registry", prompt.Provider)
	}
	clientCfg := llm.Config{
		Provider: prompt.Provider,
		BaseURL:  provider.BaseURL,
		APIKey:   provider.APIKey,
		Model:    prompt.Model,
		NumCtx:   def.NumCtx,
		Think:    prompt.Think,
	}
	start := time.Now()
	out, err := llm.New(clientCfg).Complete(ctx, []llm.Message{{Role: "system", Content: prompt.Prompt}, {Role: "user", Content: user}})
	outcome := "success"
	if err != nil {
		outcome = "error"
	}
	// Telemetry only — never log content or the response text.
	slog.Info("llm call", "kind", kind, "provider", prompt.Provider, "model", prompt.Model, "duration_ms", time.Since(start).Milliseconds(), "outcome", outcome)
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
	if p.ResourceID != 0 {
		if err := s.writeback(ctx, p, text); err != nil {
			return nil, err
		}
	}
	return json.Marshal(generateResult{Text: text})
}

// writeback stores a completed generation against the resource it was
// generated for. "title" writes back only for a text/dialog resource type
// (vocabulary/models items have no title); "process_text"/"process_dialog"
// are still called directly by the ingest package (see
// phraseforge-ingest-texts-dialogs step 6), not routed through this
// job-queue path with a resource already in hand.
//
// translations.Set and the *Store.SetTranscription/SetItemTranscription/
// SetItemTranslation methods below all treat an empty body as "delete this
// translation/transcription" (by design, for an editor clearing a field by
// hand) — so an empty or whitespace-only LLM result reaching any of them
// here would silently destroy a previously-good value instead of merely
// failing to update it. Guard against that by treating an empty result as
// this job's failure instead: the job then correctly shows as
// failed/retryable, and nothing already stored is touched.
//
// For a vocabulary_item/models_item resource type, p.Content is already the
// item's phrase as of when this job was enqueued (see
// export_import.go's buildBackfillPayload, which sets Content to the item's
// phrase for every item-level decision) — passed through to
// SetItemTranscription/SetItemTranslation as their stale-target guard rather
// than adding a separate payload field for it (see B3 in
// specs/features/phraseforge-export-import.md).
func (s *Service) writeback(ctx context.Context, p generatePayload, text string) error {
	trimmed := strings.TrimSpace(text)
	if trimmed == "" {
		return fmt.Errorf("llm returned an empty result for kind %q", p.Kind)
	}
	switch p.Kind {
	case "title":
		store := s.textWriteback(p.ResourceType)
		if store == nil {
			return fmt.Errorf("ai: unknown resource_type %q for title writeback", p.ResourceType)
		}
		return store.SetTitle(ctx, p.ResourceID, trimmed)
	case "transcription":
		if store := s.itemTranscriptionWriteback(p.ResourceType); store != nil {
			return store.SetItemTranscription(ctx, p.ResourceID, p.ItemPosition, p.Content, trimmed)
		}
		store := s.textWriteback(p.ResourceType)
		if store == nil {
			return fmt.Errorf("ai: unknown resource_type %q for transcription writeback", p.ResourceType)
		}
		return store.SetTranscription(ctx, p.ResourceID, trimmed)
	case "translation":
		if p.ResourceType == "vocabulary_item" || p.ResourceType == "models_item" {
			if s.itemTranslations == nil {
				return fmt.Errorf("ai: no item translation writeback configured for resource_type %q", p.ResourceType)
			}
			return s.itemTranslations.SetItemTranslation(ctx, p.ResourceType, p.ResourceID, p.ItemPosition, p.Content, p.Locale, trimmed)
		}
		return s.translations.Set(ctx, p.ResourceType, p.ResourceID, p.Locale, trimmed)
	default:
		return nil
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
