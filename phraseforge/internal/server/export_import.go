// Export/import for Texts and Dialogs (see
// specs/features/phraseforge-export-import.md). Vocabulary/Models get their
// own parallel implementation in a later pass — per that spec's explicit
// "deliberate duplication, not a shared abstraction" convention, Texts and
// Dialogs each get their own request/response types and handler functions
// (export_import_texts.go/export_import_dialogs.go), mirroring
// apiTextSummary/apiDialogSummary, apiTextRequest/apiDialogRequest's
// existing duplication in server.go/dialogs.go. This file holds only the
// resource-agnostic decision logic and job-enqueue plumbing shared between
// the two (already shared between text/dialog resourceType strings by
// ingest.go's own enqueueFollowUps/buildFollowUpPayloads).
package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"phraseforge/internal/ai"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/jobs"
	"phraseforge/internal/tags"
)

// exportImportBodyCap caps an import request body, matching knowledge's own
// cap exactly (knowledge/internal/server/server.go's importBodyCap) — see
// that file's precedent for why 8 MiB and http.MaxBytesReader together are
// how this codebase bounds an import body, the same shape as the ingest
// feature's SSRF-fix precedent for request-body capping (see ingest.go's
// ingestRequestBodyHeadroom doc comment) but sized for a bulk YAML/JSON
// payload instead of one piece of ingested content.
const exportImportBodyCap = 8 << 20

// transcriptionTargetLanguage mirrors ingest.go's own sentinel constant of
// the same name (duplicated rather than exported and imported — a job
// payload's field values are a wire-contract convention every independent
// caller re-declares for itself, matching ingest.generatePayload's own
// precedent) — the fixed target_language value every transcription call in
// this codebase uses, since llm_prompts overrides are looked up by the
// literal (kind, source_language, target_language) triple and transcription
// has no real "target language" of its own.
const transcriptionTargetLanguage = "transcription"

// --- shared response envelope (resource-agnostic, like writeJSON/writeErr) ---

// importError/importResult are one import request's per-item error record
// and overall result — shared across every import endpoint (Texts/Dialogs
// today; Vocabulary/Models in a later pass) since this shape carries no
// resource-specific fields, matching knowledge's own ImportError/
// ImportResult (knowledge/internal/qdrant/qdrant.go).
type importError struct {
	Index   int    `json:"index"`
	ID      int64  `json:"id,omitempty"`
	Message string `json:"message"`
}
type importResult struct {
	Imported  int           `json:"imported"`
	Deleted   int           `json:"deleted"`
	Unchanged int           `json:"unchanged"`
	Errors    []importError `json:"errors"`
}

// --- shared pure decision logic ---

// importFields is the set of primitive fields common to a text/dialog row
// that importUnchanged compares — pulled out as a plain data value (like
// ingest.go's directGenerateCall) so the comparison is unit-testable without
// a database, and so it can be shared between apiImportTexts and
// apiImportDialogs without a shared resource-type abstraction over the
// stores themselves. Translations is restricted to whichever locales the
// import item actually provides — that is the only set an import ever
// writes (see apiTextImportItem/apiDialogImportItem's Translations doc
// comment), so a locale the import omits must never affect this comparison.
type importFields struct {
	Title         string
	Body          string
	Transcription string
	Language      string
	Script        string
	Tags          []string
	Translations  map[string]string
}

// importUnchanged reports whether existing already matches everything
// incoming would write — mirrors knowledge's qdrant.unchanged (see
// knowledge/internal/qdrant/qdrant.go) for this app's own field set. Tags
// must already be normalized (tags.Parse/normalizeTagList) on both sides —
// this does not itself lowercase/dedupe/sort.
func importUnchanged(existing, incoming importFields) bool {
	if existing.Title != incoming.Title ||
		existing.Body != incoming.Body ||
		existing.Transcription != incoming.Transcription ||
		existing.Language != incoming.Language ||
		existing.Script != incoming.Script {
		return false
	}
	if !slices.Equal(existing.Tags, incoming.Tags) {
		return false
	}
	for locale, text := range incoming.Translations {
		if existing.Translations[locale] != text {
			return false
		}
	}
	return true
}

// normalizeTagList applies tags.Parse's own normalization (trim, lowercase,
// dedupe, drop empties, sort) to an already-split tag list. An import
// item's tags field is a real string list, not the create/edit form's
// single comma-separated string, but every tag name already stored in this
// app is itself comma-free (tags.Parse is what created it, splitting on ","
// is how it was born), so joining with "," and reusing tags.Parse directly
// is safe and avoids re-implementing its dedupe/sort/lowercase logic here.
func normalizeTagList(names []string) []string {
	return tags.Parse(strings.Join(names, ","))
}

// backfillDecision is one llm_generate job (see ai.KindLLMGenerate)
// decideBackfill decided a just-created-or-updated text/dialog row needs.
// Kind mirrors generatePayload's own Kind field ("title"/"transcription"/
// "translation"); Locale is set only for a "translation" decision.
type backfillDecision struct {
	Kind   string
	Locale string
}

// decideBackfill returns the backfillDecisions an import item needs: a
// "title" job if title is blank, a "transcription" job if the language
// needs transcription (ime.NeedsTranscriptionForLanguage) and transcription
// is blank, and one "translation" job per site locale the import didn't
// provide a translation for (checked by key presence in
// providedTranslations, not by the stored value — an import that explicitly
// writes an empty translation still counts as "provided", matching
// translations.Set's own "empty means delete" convention). Pulled out as a
// pure function (matching ingest.go's buildFollowUpPayloads convention) so
// this decision is unit-testable without a database or LLM call.
func decideBackfill(title, transcription string, needsTranscription bool, providedTranslations map[string]string, siteLocales []i18n.Locale) []backfillDecision {
	var out []backfillDecision
	if strings.TrimSpace(title) == "" {
		out = append(out, backfillDecision{Kind: "title"})
	}
	if needsTranscription && strings.TrimSpace(transcription) == "" {
		out = append(out, backfillDecision{Kind: "transcription"})
	}
	for _, loc := range siteLocales {
		if _, provided := providedTranslations[loc.Code]; !provided {
			out = append(out, backfillDecision{Kind: "translation", Locale: loc.Code})
		}
	}
	return out
}

// decideItemBackfill mirrors decideBackfill for one vocabulary/models item —
// a "transcription" decision if the language needs transcription and
// transcription is blank, and one "translation" decision per site locale the
// item didn't provide a translation for. There is no "title" decision here:
// vocabulary/models items have no title field at all, unlike a text/dialog
// row (see this feature's Vocabulary/Models Approach section) — a separate
// function rather than decideBackfill called with a dummy non-blank title
// keeps that difference explicit rather than papered over by a fake value.
func decideItemBackfill(transcription string, needsTranscription bool, providedTranslations map[string]string, siteLocales []i18n.Locale) []backfillDecision {
	var out []backfillDecision
	if needsTranscription && strings.TrimSpace(transcription) == "" {
		out = append(out, backfillDecision{Kind: "transcription"})
	}
	for _, loc := range siteLocales {
		if _, provided := providedTranslations[loc.Code]; !provided {
			out = append(out, backfillDecision{Kind: "translation", Locale: loc.Code})
		}
	}
	return out
}

// listMetaFields is the set of a vocabulary/models list's own primitive
// metadata fields (everything except its items) — pulled out as a plain data
// value (like importFields) so listMetaUnchanged is unit-testable without a
// database, and so it can be shared between apiImportVocabulary and
// apiImportModels, the same way importFields is shared between
// apiImportTexts and apiImportDialogs. Unlike importFields, Title/Language/
// Script here are each individually optional on update (see
// specs/features/phraseforge-export-import.md's Vocabulary/Models Approach:
// "an update-by-id can omit them to leave the list's own metadata
// unchanged"), so listMetaUnchanged's comparison — and the caller's own
// resolved-value computation before an actual write — must treat a blank
// incoming value as "not provided", never as "explicitly cleared".
type listMetaFields struct {
	Title    string
	Language string
	Script   string
	Tags     []string
}

// listMetaUnchanged reports whether existing already matches every
// listMetaFields value incoming actually provided. Title/Language/Script are
// compared only when incoming's value is non-blank (a blank value means the
// import omitted that field, not that it wants to clear it — vocabulary/
// models lists have no legitimate reason to hold a blank title/language/
// script, so "non-blank" is an unambiguous provided/omitted signal). Tags
// follows the same provided-only rule at the whole-list-of-tags granularity
// (an incoming Tags of length zero means "tags omitted", matching
// apiImportVocabulary's/apiImportModels' own "tags.SetFor if tags given"
// rule) — this is a deliberate difference from importFields/importUnchanged,
// where an import's tags field is always fully applied (an omitted list
// clears every existing tag); tags must already be normalized
// (tags.Parse/normalizeTagList) on both sides — this does not itself
// lowercase/dedupe/sort.
func listMetaUnchanged(existing, incoming listMetaFields) bool {
	if incoming.Title != "" && incoming.Title != existing.Title {
		return false
	}
	if incoming.Language != "" && incoming.Language != existing.Language {
		return false
	}
	if incoming.Script != "" && incoming.Script != existing.Script {
		return false
	}
	if len(incoming.Tags) > 0 && !slices.Equal(existing.Tags, incoming.Tags) {
		return false
	}
	return true
}

// generatePayload mirrors ai.Service's own (unexported) llm_generate job
// payload shape exactly (field-for-field, same JSON tags) — duplicated here
// rather than imported, matching ingest.generatePayload's own precedent: a
// job payload is a wire contract between independently-versioned packages,
// not a shared Go type.
type generatePayload struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	ContentType    string `json:"content_type"`
	Content        string `json:"content"`

	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	Locale       string `json:"locale,omitempty"`

	// ItemPosition mirrors ai.generatePayload's own field of the same name —
	// only meaningful (and only ever set, by enqueueItemBackfill) for a
	// vocabulary_item/models_item resource type, where ResourceID is the
	// list id rather than the item's own id (an item has no independent id).
	ItemPosition int `json:"item_position,omitempty"`
}

// buildBackfillPayload builds one backfillDecision's llm_generate job
// payload. TargetLanguage follows the same sentinel-target convention
// documented on transcriptionTargetLanguage/ingest.go's buildTitleCall:
// "title" and "transcription" fix TargetLanguage to their own kind name (so
// an admin llm_prompts override is reachable), while "translation" sets it
// to the destination locale code, exactly like the interactive Translate
// button and ingest's own buildFollowUpPayloads already do.
func buildBackfillPayload(d backfillDecision, resourceType string, resourceID int64, language, content string) generatePayload {
	p := generatePayload{
		Kind: d.Kind, SourceLanguage: language, ContentType: resourceType, Content: content,
		ResourceType: resourceType, ResourceID: resourceID,
	}
	switch d.Kind {
	case "title":
		p.TargetLanguage = "title"
	case "transcription":
		p.TargetLanguage = transcriptionTargetLanguage
	case "translation":
		p.TargetLanguage = d.Locale
		p.Locale = d.Locale
	}
	return p
}

// exportFilter is the required language+script and optional tags parsed
// from an export request's query string — shared by all four export
// handlers (see requireExportFilter's doc comment and this feature's own
// Acceptance Criteria: "one shared helper/pattern, not four
// independently-diverging implementations").
type exportFilter struct {
	Language string
	Script   string
	Tags     []string
}

// requireExportFilter validates that an export request's language and
// script query params are both present, writing a 400 and reporting
// ok=false if either is missing — every export handler must check ok and
// return immediately when false, matching readImportBody's own ok-return
// convention. Tags is parsed (trimmed, lowercased, deduped, sorted) via
// tags.Parse, the same normalization already used for the create/edit
// form's tag input, so "Travel, travel ,ROMANIAN" behaves identically here.
func requireExportFilter(w http.ResponseWriter, r *http.Request) (exportFilter, bool) {
	language := strings.TrimSpace(r.URL.Query().Get("language"))
	script := strings.TrimSpace(r.URL.Query().Get("script"))
	if language == "" || script == "" {
		writeErr(w, http.StatusBadRequest, "validation_error", "language and script query parameters are both required")
		return exportFilter{}, false
	}
	return exportFilter{Language: language, Script: script, Tags: tags.Parse(r.URL.Query().Get("tags"))}, true
}

// exportScopeLanguages mirrors apiListTexts'/apiListDialogs' own ?language=
// filtering exactly (see apiListTexts), but applied to the languages the
// caller already resolved (editable, for export/import, rather than
// viewable, for the list view) — pulled out as a pure function so this
// narrowing rule is unit-testable without a database.
func exportScopeLanguages(langs []string, all bool, languageFilter string) ([]string, bool) {
	if languageFilter != "" && (all || slices.Contains(langs, languageFilter)) {
		return []string{languageFilter}, false
	}
	return langs, all
}

// --- shared handler plumbing ---

// writeYAML mirrors knowledge's own convention exactly (knowledge/internal/
// server/server.go's writeYAML) for cross-app consistency: a 2-space-indent
// YAML body, "application/yaml" the way writeJSON's sibling always sets
// "application/json".
func writeYAML(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/yaml; charset=utf-8")
	w.WriteHeader(status)
	enc := yaml.NewEncoder(w)
	enc.SetIndent(2)
	enc.Encode(v) //nolint:errcheck // headers already sent; nothing to do if encoding fails
	enc.Close()   //nolint:errcheck // same
}

// readImportBody bounds and reads an import request's raw body
// (yaml.Unmarshal handles JSON too, since YAML is a superset), matching
// knowledge's own importItems handler exactly (knowledge/internal/server/
// server.go): a MaxBytesReader-triggered *http.MaxBytesError becomes 413,
// any other read failure 400. Returns ok=false once it has already written
// the error response, so the caller can just return.
func readImportBody(w http.ResponseWriter, r *http.Request) (body []byte, ok bool) {
	r.Body = http.MaxBytesReader(w, r.Body, exportImportBodyCap)
	body, err := io.ReadAll(r.Body)
	if err != nil {
		var maxBytesErr *http.MaxBytesError
		if errors.As(err, &maxBytesErr) {
			writeErr(w, http.StatusRequestEntityTooLarge, "payload_too_large", err.Error())
			return nil, false
		}
		writeErr(w, http.StatusBadRequest, "invalid_body", err.Error())
		return nil, false
	}
	return body, true
}

// enqueueBackfill enqueues the llm_generate jobs (see ai.KindLLMGenerate) a
// just-created-or-updated text/dialog row needs for whatever title/
// transcription/translation fields the import left genuinely blank —
// shared between apiImportTexts and apiImportDialogs since decideBackfill
// and generatePayload are already resource-agnostic (only resourceType/
// resourceID/language/body differ per call), the same way ingest.go's own
// enqueueFollowUps is a single function shared across resourceType "text"
// and "dialog".
func (s *Server) enqueueBackfill(ctx context.Context, resourceType string, resourceID int64, language, title, body, transcription string, providedTranslations map[string]string) error {
	needsTranscription, err := ime.NeedsTranscriptionForLanguage(ctx, s.db, language)
	if err != nil {
		return fmt.Errorf("check needs_transcription for language %s: %w", language, err)
	}
	decisions := decideBackfill(title, transcription, needsTranscription, providedTranslations, i18n.Locales)
	for _, d := range decisions {
		payload := buildBackfillPayload(d, resourceType, resourceID, language, body)
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal %s backfill payload: %w", payload.Kind, err)
		}
		if _, err := s.jobs.Enqueue(ctx, ai.JobKind(payload.Kind), jobs.PriorityBackground, raw); err != nil {
			return fmt.Errorf("enqueue %s backfill job: %w", payload.Kind, err)
		}
	}
	return nil
}

// itemBackfillSpec is one vocabulary/models item's backfill-relevant state
// after a wholesale item replacement has added it (see
// replaceVocabItemsTx/replaceModelsItemsTx) — collected during the
// transactional replace step so the caller can enqueue backfill jobs
// afterward, once the transaction has actually committed (an item that gets
// rolled back must never have a backfill job enqueued against it).
type itemBackfillSpec struct {
	position             int
	phrase               string
	transcription        string
	providedTranslations map[string]string
}

// itemReplaceTxTimeout bounds backgroundTxContext's derived context — see
// that function's doc comment.
const itemReplaceTxTimeout = 30 * time.Second

// backgroundTxContext derives a context that survives ctx's own cancellation
// but is still bounded by a fixed timeout — used to wrap a vocabulary/models
// list's wholesale item-replacement transaction (see B2 in
// specs/features/phraseforge-export-import.md): a client disconnect
// mid-request must never abort that transaction partway through and leave a
// list's items permanently deleted with nothing re-added. Mirrors jobs.go's
// own precedent of using context.Background() for a completion write that
// must finish even after its triggering context is done (see
// jobs.Service.runOne), sized to a real timeout here instead of being
// unkillable forever.
func backgroundTxContext(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeout(context.WithoutCancel(ctx), itemReplaceTxTimeout)
}

// enqueueItemBackfill mirrors enqueueBackfill for one vocabulary/models item
// — addressed by (listID, position) rather than its own independent id, per
// ai.generatePayload's ItemPosition field (see that field's doc comment).
// Shared between apiImportVocabulary and apiImportModels the same way
// enqueueBackfill is shared between apiImportTexts and apiImportDialogs.
func (s *Server) enqueueItemBackfill(ctx context.Context, resourceType string, listID int64, position int, language, phrase, transcription string, providedTranslations map[string]string) error {
	needsTranscription, err := ime.NeedsTranscriptionForLanguage(ctx, s.db, language)
	if err != nil {
		return fmt.Errorf("check needs_transcription for language %s: %w", language, err)
	}
	decisions := decideItemBackfill(transcription, needsTranscription, providedTranslations, i18n.Locales)
	for _, d := range decisions {
		payload := buildBackfillPayload(d, resourceType, listID, language, phrase)
		payload.ItemPosition = position
		raw, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("marshal %s backfill payload: %w", payload.Kind, err)
		}
		if _, err := s.jobs.Enqueue(ctx, ai.JobKind(payload.Kind), jobs.PriorityBackground, raw); err != nil {
			return fmt.Errorf("enqueue %s backfill job: %w", payload.Kind, err)
		}
	}
	return nil
}

// exportTranslations returns resourceID's translations for every site
// locale that actually has one — a locale with no stored translation is
// simply absent from the map (never present with an empty string), so a
// later re-import's decideBackfill correctly treats it as still missing
// rather than "explicitly set to empty".
func (s *Server) exportTranslations(ctx context.Context, resourceType string, resourceID int64) (map[string]string, error) {
	out := map[string]string{}
	for _, loc := range i18n.Locales {
		body, has, err := s.translations.Get(ctx, resourceType, resourceID, loc.Code)
		if err != nil {
			return nil, err
		}
		if has {
			out[loc.Code] = body
		}
	}
	return out, nil
}

// existingTranslationsFor returns resourceID's currently-stored translation
// for every locale in provided — restricted to those locales because they
// are the only ones an import would ever overwrite (translations.Set is
// only called "for whichever locales are given", see apiImportTexts'/
// apiImportDialogs' own doc comments), so a locale the import omits can
// never make importUnchanged see the row as changed.
func (s *Server) existingTranslationsFor(ctx context.Context, resourceType string, resourceID int64, provided map[string]string) (map[string]string, error) {
	if len(provided) == 0 {
		return nil, nil
	}
	out := make(map[string]string, len(provided))
	for locale := range provided {
		body, _, err := s.translations.Get(ctx, resourceType, resourceID, locale)
		if err != nil {
			return nil, err
		}
		out[locale] = body
	}
	return out, nil
}
