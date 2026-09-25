// Package ingest implements the process_text/process_dialog background job
// handlers: the HTTP ingest endpoint (a later pass, see
// specs/features/phraseforge-ingest-texts-dialogs.md steps 7/9/10) resolves
// raw content synchronously (fetched URL, uploaded file, or pasted text) and
// stages it in one of these jobs' payload; this package cleans that content,
// generates a title, creates the real Text/Dialog row, and enqueues the
// follow-up translation/transcription jobs a manually created row would get
// via the interactive Transcribe/Translate buttons.
package ingest

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/jobs"
	"phraseforge/internal/texts"
)

// KindProcessText and KindProcessDialog are the jobs.Service kinds
// registered for HandleProcessText/HandleProcessDialog — exported so
// main.go can call jobsSvc.Register(ingest.KindProcessText, ...).
const (
	KindProcessText   = "process_text"
	KindProcessDialog = "process_dialog"
)

// resourceTypeText/resourceTypeDialog match ai.Service.textWriteback's
// switch and server's own resourceTypeText/resourceTypeDialog constants —
// duplicated here rather than imported, since neither ai nor server exports
// them and ingest has no other reason to import server.
const (
	resourceTypeText   = "text"
	resourceTypeDialog = "dialog"
)

// transcriptionTargetLanguage is the fixed target_language sentinel every
// other transcription call site in this codebase uses: the interactive
// Transcribe button (templates/layout.html's phraseforgeGenerate:
// `target_language: kind === 'transcription' ? 'transcription' : ...`) and
// the admin LLM-prompt editor (static/js/admin-app.js:
// `targetLanguage: kind === "transcription" ? "transcription" : ...`).
// llm_prompts overrides are looked up by the exact (kind, source_language,
// target_language) triple, and transcription has no real "target language"
// of its own — every caller fixes this field so an override can be keyed by
// source language alone. Enqueuing a transcription follow-up job with
// anything else here would silently bypass any admin-configured
// transcription prompt.
const transcriptionTargetLanguage = "transcription"

// stepCreatedPrefix marks a job's step column, once handleProcess has
// successfully created its Text/Dialog row, with that row's id — see
// createdRow's doc comment for the retry-safety problem this closes.
const stepCreatedPrefix = "created:"

// processPayload is the process_text/process_dialog job payload shape,
// constructed and enqueued by the ingest HTTP endpoint (a later pass); this
// package only unmarshals it.
type processPayload struct {
	Content  string `json:"content"` // raw ingested content
	Source   string `json:"source"`  // URL or filename; "" for pasted text — becomes ingest_source
	Language string `json:"language"`
	Script   string `json:"script"`
	UserID   int64  `json:"user_id"`
}

// generatePayload mirrors ai.Service's own (unexported) llm_generate job
// payload shape exactly (field-for-field, same JSON tags). Duplicated here
// rather than imported: a job payload is a wire contract between
// independently-versioned packages, not a shared Go type, and ai
// deliberately keeps its payload type unexported.
type generatePayload struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	ContentType    string `json:"content_type"`
	Content        string `json:"content"`

	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	Locale       string `json:"locale,omitempty"`
}

// processResult is HandleProcessText/HandleProcessDialog's job result.
// Nothing reads it today (fire-and-forget), but a job's result column is
// always populated with something sensible rather than left null.
type processResult struct {
	ID int64 `json:"id"`
}

// rowStore is the subset of *texts.Store/*dialogs.Store's API handleProcess
// needs — lets HandleProcessText/HandleProcessDialog share one
// implementation instead of duplicating it per resource type. Both stores'
// Create/GetBody signatures already match this exactly. GetBody backs
// createdRow's retry-safety check.
type rowStore interface {
	Create(ctx context.Context, userID int64, title, body, transcription, language, script, ingestSource string) (int64, error)
	GetBody(ctx context.Context, id int64) (string, error)
}

var (
	_ rowStore = (*texts.Store)(nil)
	_ rowStore = (*dialogs.Store)(nil)

	_ jobs.HandlerFunc = (*Service)(nil).HandleProcessText
	_ jobs.HandlerFunc = (*Service)(nil).HandleProcessDialog
)

// Service implements the process_text/process_dialog job handlers.
type Service struct {
	texts   *texts.Store
	dialogs *dialogs.Store
	jobs    *jobs.Service
	ai      *ai.Service

	// db backs the ime_config "needs transcription" lookup
	// (ime.NeedsTranscriptionForLanguage) — not otherwise exposed by
	// texts.Store/dialogs.Store/ai.Service, so New takes it directly, the
	// same way server.Server already does for that same lookup.
	db *pgxpool.Pool
}

func New(textsStore *texts.Store, dialogsStore *dialogs.Store, jobsSvc *jobs.Service, aiSvc *ai.Service, db *pgxpool.Pool) *Service {
	return &Service{texts: textsStore, dialogs: dialogsStore, jobs: jobsSvc, ai: aiSvc, db: db}
}

// HandleProcessText is the jobs.HandlerFunc registered for KindProcessText.
func (s *Service) HandleProcessText(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	return s.handleProcess(ctx, id, payload, resourceTypeText, KindProcessText, s.texts)
}

// HandleProcessDialog is the jobs.HandlerFunc registered for
// KindProcessDialog. Unlike a manually created dialog's body, the cleaned
// content is stored as-is, without wrapDialogBody's {start-dialog} wrapper
// — that wrapper is applied only at render time (see
// server/dialogs.go:wrapDialogBody's own doc comment), exactly matching how
// a manually created dialog's raw body is stored today.
func (s *Service) HandleProcessDialog(ctx context.Context, id string, payload json.RawMessage) (json.RawMessage, error) {
	return s.handleProcess(ctx, id, payload, resourceTypeDialog, KindProcessDialog, s.dialogs)
}

// handleProcess is HandleProcessText/HandleProcessDialog's shared body:
// clean the raw content, generate a title, create the row, and enqueue its
// follow-up translation/transcription jobs. processKind doubles as both
// this job's own registered kind (for error context) and the
// ai.Service.Generate purpose kind for the cleanup call itself — they are
// deliberately the same string.
//
// jobID identifies this job's own row (see jobs.HandlerFunc's doc comment)
// and is used only for createdRow's retry-safety check below — it is not
// otherwise part of this pipeline's logic.
func (s *Service) handleProcess(ctx context.Context, jobID string, payload json.RawMessage, resourceType, processKind string, store rowStore) (json.RawMessage, error) {
	var p processPayload
	if err := json.Unmarshal(payload, &p); err != nil {
		return nil, fmt.Errorf("ingest: unmarshal %s payload: %w", processKind, err)
	}

	// A Retry (see jobs.Service.Retry) mints a new job that carries the
	// failed job's Step forward — if that step already names a row this same
	// pipeline created on an earlier run (e.g. it failed after Create
	// succeeded but before/during enqueueFollowUps), skip straight to
	// enqueueFollowUps for that existing row instead of creating a second
	// one and re-running both LLM calls below.
	resourceID, content, err := s.createdRow(ctx, jobID, store)
	if err != nil {
		return nil, err
	}
	if resourceID == 0 {
		// No real translation happens in this call — source and target are
		// the same ingested content's language; process_text/process_dialog's
		// prompts clean formatting only, they never move content into
		// another language. target_language is fixed to the kind's own name
		// (not p.Language) — see buildCleaningCall's doc comment.
		cc := buildCleaningCall(processKind, p.Language, p.Content)
		cleaned, err := s.ai.Generate(ctx, cc.Kind, cc.SourceLanguage, cc.TargetLanguage, resourceType, cc.Content)
		if err != nil {
			return nil, fmt.Errorf("ingest: clean content: %w", err)
		}

		tc := buildTitleCall(p.Language, cleaned)
		title, err := s.ai.Generate(ctx, tc.Kind, tc.SourceLanguage, tc.TargetLanguage, resourceType, tc.Content)
		if err != nil {
			return nil, fmt.Errorf("ingest: generate title: %w", err)
		}

		resourceID, err = store.Create(ctx, p.UserID, strings.TrimSpace(title), cleaned, "", p.Language, p.Script, p.Source)
		if err != nil {
			return nil, fmt.Errorf("ingest: create %s row: %w", resourceType, err)
		}
		// Record the created row's id before enqueueing follow-ups, so a
		// Retry after a failure past this point takes the createdRow branch
		// above instead of repeating Create. Best effort: if this write
		// itself fails, this one job's retry-duplication risk stays open,
		// but Create's own success is not undone by it.
		if err := s.jobs.SetStep(ctx, jobID, stepCreatedPrefix+strconv.FormatInt(resourceID, 10)); err != nil {
			return nil, fmt.Errorf("ingest: record created %s row id in job step: %w", resourceType, err)
		}
		content = cleaned
	}

	if err := s.enqueueFollowUps(ctx, resourceType, p.Language, content, resourceID); err != nil {
		return nil, err
	}

	return json.Marshal(processResult{ID: resourceID})
}

// createdRow reports whether an earlier run of this same job (a Retry mints
// a new job id but carries the failed job's Step forward — see
// jobs.Service.Retry) already created its Text/Dialog row: if jobID's own
// Step column carries the stepCreatedPrefix marker handleProcess records
// right after a successful Create, this returns that row's id and its
// stored body (used as enqueueFollowUps' content in place of the "cleaned"
// value the first, already-succeeded, run computed — that value is gone,
// since this run never recomputes it). Returns id 0, "" if no such marker is
// present — the normal, not-a-retry-of-a-partial-success path.
//
// Known accepted gap: this is a best-effort idempotency check keyed on the
// job row's own accumulated state, not a true exactly-once guarantee — e.g.
// it does not defend against two workers racing on the same step (this
// queue has exactly one worker goroutine today, so that case cannot occur in
// practice; see jobs.Service's own doc comment).
func (s *Service) createdRow(ctx context.Context, jobID string, store rowStore) (id int64, body string, err error) {
	j, err := s.jobs.Get(ctx, jobID)
	if err != nil {
		return 0, "", fmt.Errorf("ingest: read own job %s: %w", jobID, err)
	}
	parsedID, ok, err := parseCreatedStep(j.Step)
	if err != nil {
		return 0, "", fmt.Errorf("ingest: job %s: %w", jobID, err)
	}
	if !ok {
		return 0, "", nil
	}
	existingBody, err := store.GetBody(ctx, parsedID)
	if err != nil {
		return 0, "", fmt.Errorf("ingest: read already-created row %d for job %s: %w", parsedID, jobID, err)
	}
	return parsedID, existingBody, nil
}

// parseCreatedStep parses a job's step column for the stepCreatedPrefix
// marker createdRow relies on, returning the row id it names and whether the
// marker was present at all. Pulled out as a pure function (like
// buildFollowUpPayloads/buildCleaningCall) so this parsing is unit-testable
// without a database.
func parseCreatedStep(step string) (id int64, ok bool, err error) {
	rest, ok := strings.CutPrefix(step, stepCreatedPrefix)
	if !ok {
		return 0, false, nil
	}
	id, err = strconv.ParseInt(rest, 10, 64)
	if err != nil {
		return 0, false, fmt.Errorf("parse step %q: %w", step, err)
	}
	return id, true, nil
}

// directGenerateCall describes one of handleProcess's two direct (not
// enqueued) ai.Generate invocations — cleaning the raw content, then
// generating a title from the cleaned result. Modeled as a pure data value,
// the same way buildFollowUpPayloads below is, so the sentinel-target
// convention is unit-testable without a real LLM call.
type directGenerateCall struct {
	Kind           string
	SourceLanguage string
	TargetLanguage string
	Content        string
}

// buildCleaningCall builds handleProcess's process_text/process_dialog
// cleanup call. target_language is fixed to processKind's own name — never
// p.Language — matching transcriptionTargetLanguage's existing convention
// exactly: llm_prompts overrides are looked up by the literal
// (kind, source_language, target_language) triple, and process_text/
// process_dialog (like transcription) have no real "target language" of
// their own. Getting this wrong (e.g. passing the real language, as an
// earlier version of this code did) would make an admin override for these
// kinds unreachable, since the admin LLM-prompt editor also fixes this
// field to the kind's own name for every non-"translation" kind (see
// static/js/admin-app.js's refreshLLMTarget).
func buildCleaningCall(processKind, language, content string) directGenerateCall {
	return directGenerateCall{Kind: processKind, SourceLanguage: language, TargetLanguage: processKind, Content: content}
}

// buildTitleCall builds handleProcess's title-generation call — same
// sentinel-target rule as buildCleaningCall, fixed to the literal "title".
func buildTitleCall(language, cleanedContent string) directGenerateCall {
	return directGenerateCall{Kind: "title", SourceLanguage: language, TargetLanguage: "title", Content: cleanedContent}
}

// enqueueFollowUps enqueues one llm_generate translation job per site
// locale, plus one llm_generate transcription job if language needs
// transcription (the same ime_config check that drives the interactive
// Transcribe button's visibility).
func (s *Service) enqueueFollowUps(ctx context.Context, resourceType, language, content string, resourceID int64) error {
	needsTranscription, err := ime.NeedsTranscriptionForLanguage(ctx, s.db, language)
	if err != nil {
		return fmt.Errorf("ingest: check needs_transcription for language %s: %w", language, err)
	}

	for _, payload := range buildFollowUpPayloads(resourceType, language, content, resourceID, i18n.Locales, needsTranscription) {
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("ingest: marshal %s job payload: %w", payload.Kind, err)
		}
		if _, err := s.jobs.Enqueue(ctx, ai.KindLLMGenerate, jobs.PriorityBackground, raw); err != nil {
			return fmt.Errorf("ingest: enqueue %s job: %w", payload.Kind, err)
		}
	}
	return nil
}

// buildFollowUpPayloads returns the llm_generate job payloads a newly
// ingested resourceType row needs: one "translation" payload per locale
// (target_language is the locale code itself — matching how the existing
// interactive Translate button already fills target_language straight from
// its locale-valued <select>, layout.html's phraseforgeGenerate), plus one
// "transcription" payload if needsTranscription is true. Pulled out of
// enqueueFollowUps as a pure function so this decision — which jobs, with
// which fields — is unit-testable without a database or LLM call.
func buildFollowUpPayloads(resourceType, language, content string, resourceID int64, locales []i18n.Locale, needsTranscription bool) []generatePayload {
	out := make([]generatePayload, 0, len(locales)+1)
	for _, loc := range locales {
		out = append(out, generatePayload{
			Kind:           "translation",
			SourceLanguage: language,
			TargetLanguage: loc.Code,
			ContentType:    resourceType,
			Content:        content,
			ResourceType:   resourceType,
			ResourceID:     resourceID,
			Locale:         loc.Code,
		})
	}
	if needsTranscription {
		out = append(out, generatePayload{
			Kind:           "transcription",
			SourceLanguage: language,
			TargetLanguage: transcriptionTargetLanguage,
			ContentType:    resourceType,
			Content:        content,
			ResourceType:   resourceType,
			ResourceID:     resourceID,
		})
	}
	return out
}
