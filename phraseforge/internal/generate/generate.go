// Package generate implements the generate_vocab_from_text/
// generate_models_from_text/generate_vocab_from_dialog/
// generate_models_from_dialog background job handlers (see
// specs/features/phraseforge-generate-vocab-models-from-text.md and
// specs/features/dialog-vocabulary-models-generation.md): each reads a
// Text or Dialog row, asks ai.Service.Generate to extract vocabulary/grammar
// items from its body, parses the line-based response, then either
// wholesale-replaces an already-linked list's items (a rerun) or creates a
// new list linked to the source via source_text_id/source_dialog_id (the
// first run) — mirroring phraseforge/internal/ingest's own package
// structure (job handlers holding references to the stores/ai.Service they
// need).
package generate

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/i18n"
	"phraseforge/internal/ime"
	"phraseforge/internal/jobs"
	"phraseforge/internal/models"
	"phraseforge/internal/texts"
	"phraseforge/internal/vocabulary"
)

// KindGenerateVocabFromText/KindGenerateModelsFromText/
// KindGenerateVocabFromDialog/KindGenerateModelsFromDialog are the
// jobs.Service kinds registered for HandleGenerateVocab/HandleGenerateModels
// — exported so main.go can call jobsSvc.Register(generate.KindGenerateVocabFromText, ...).
// The Text/Dialog pair for each of Vocab/Models shares one handler body
// (dispatched on the payload's ResourceType) but keeps distinct kind values
// so the Jobs page and Retry still show/route on what actually ran.
const (
	KindGenerateVocabFromText    = "generate_vocab_from_text"
	KindGenerateModelsFromText   = "generate_models_from_text"
	KindGenerateVocabFromDialog  = "generate_vocab_from_dialog"
	KindGenerateModelsFromDialog = "generate_models_from_dialog"
)

// aiKindGenerateVocabulary/aiKindGenerateModels are the ai.Service.Generate
// purpose kinds these jobs call — see ai.ValidKinds.
const (
	aiKindGenerateVocabulary = "generate_vocabulary"
	aiKindGenerateModels     = "generate_models"
	contentTypeText          = "text"
)

// resourceTypeText/resourceTypeDialog are the payload's ResourceType values.
const (
	resourceTypeText   = "text"
	resourceTypeDialog = "dialog"
)

// resourceTypeVocabItem/resourceTypeModelsItem are the item-backfill
// payload's ResourceType values — mirrors ai.generatePayload's own
// resource_type strings for a vocabulary/models item exactly (see that
// package's writeback dispatch).
const (
	resourceTypeVocabItem  = "vocabulary_item"
	resourceTypeModelsItem = "models_item"
)

// transcriptionTargetLanguage mirrors ingest.go's/export_import.go's own
// sentinel constant of the same name (duplicated rather than exported and
// imported — a job payload's field values are a wire-contract convention
// every independent caller re-declares for itself) — the fixed
// target_language value every transcription call in this codebase uses.
const transcriptionTargetLanguage = "transcription"

// llmGeneratePayload mirrors ai.Service's own (unexported) llm_generate job
// payload shape exactly (field-for-field, same JSON tags) — duplicated
// here rather than imported, matching server/export_import.go's own
// generatePayload precedent: a job payload is a wire contract between
// independently-versioned packages, not a shared Go type.
type llmGeneratePayload struct {
	Kind           string `json:"kind"`
	SourceLanguage string `json:"source_language"`
	TargetLanguage string `json:"target_language"`
	ContentType    string `json:"content_type"`
	Content        string `json:"content"`

	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	Locale       string `json:"locale,omitempty"`
	ItemPosition int    `json:"item_position,omitempty"`
}

// payload is the generate_vocab_from_*/generate_models_from_* job payload
// shape, constructed and enqueued by the HTTP handlers in
// phraseforge/internal/server. UserID becomes a newly created list's owner
// (Create's own userID convention, matching every other list-creating
// endpoint) — only used on the first run for a given source; a rerun
// instead reuses the already-linked list's existing owner.
//
// ResourceType/ResourceID are the current shape (dialog-vocabulary-models-
// generation): ResourceType is "text" or "dialog", ResourceID is that
// resource's id. TextID is the pre-existing shape, kept for backward
// compatibility with already-queued/failed job rows — resourceType/
// resourceID below treat an absent/empty ResourceType as "text" with
// TextID as the id, so an old row still decodes and retries correctly.
type payload struct {
	TextID       int64  `json:"text_id,omitempty"`
	ResourceType string `json:"resource_type,omitempty"`
	ResourceID   int64  `json:"resource_id,omitempty"`
	UserID       int64  `json:"user_id"`
}

// resourceType returns the payload's effective resource type, defaulting a
// blank/absent ResourceType (the pre-existing payload shape) to "text".
func (p payload) resourceType() string {
	if p.ResourceType == "" {
		return resourceTypeText
	}
	return p.ResourceType
}

// resourceID returns the payload's effective source id — TextID for the
// pre-existing "text" shape (ResourceType absent), ResourceID otherwise.
func (p payload) resourceID() int64 {
	if p.ResourceType == "" {
		return p.TextID
	}
	return p.ResourceID
}

// source is what HandleGenerateVocab/HandleGenerateModels need from either a
// Text or a Dialog row — resolved once by resolveSource so the rest of each
// handler doesn't care which resource type it came from.
type source struct {
	Title    string
	Language string
	Script   string
	Body     string
}

// resolveSource fetches {title, language, script, body} from the resource
// p identifies, dispatching on p.resourceType().
func (s *Service) resolveSource(ctx context.Context, p payload) (source, error) {
	switch p.resourceType() {
	case resourceTypeDialog:
		d, err := s.dialogs.Get(ctx, p.resourceID())
		if err != nil {
			return source{}, fmt.Errorf("generate: read dialog %d: %w", p.resourceID(), err)
		}
		return source{Title: d.Title, Language: d.Language, Script: d.Script, Body: d.Body}, nil
	default:
		t, err := s.texts.Get(ctx, p.resourceID())
		if err != nil {
			return source{}, fmt.Errorf("generate: read text %d: %w", p.resourceID(), err)
		}
		return source{Title: t.Title, Language: t.Language, Script: t.Script, Body: t.Body}, nil
	}
}

// result is this package's job result shape — nothing reads it today
// (fire-and-forget, matching ingest.processResult's own doc comment), but a
// job's result column is always populated with something sensible.
type result struct {
	ListID    int64 `json:"list_id"`
	ItemCount int   `json:"item_count"`
}

// Service implements the generate_vocab_from_*/generate_models_from_*
// job handlers.
type Service struct {
	texts   *texts.Store
	dialogs *dialogs.Store
	vocab   *vocabulary.Store
	models  *models.Store
	ai      *ai.Service
	jobs    *jobs.Service
	db      *pgxpool.Pool
}

func New(textsStore *texts.Store, dialogsStore *dialogs.Store, vocabStore *vocabulary.Store, modelsStore *models.Store, aiSvc *ai.Service, jobsSvc *jobs.Service, db *pgxpool.Pool) *Service {
	return &Service{texts: textsStore, dialogs: dialogsStore, vocab: vocabStore, models: modelsStore, ai: aiSvc, jobs: jobsSvc, db: db}
}

// enqueueItemBackfill enqueues one background transcription job (only if
// itemLanguage needs one and transcription is still blank) plus one
// translation job per site locale, for one just-created/replaced
// vocabulary/models item — see the user's own direction: "when vocabulary
// is created also jobs to translate all words to all site languages are
// submitted... these translations should happen automatically when I
// ingest text or dialog." Every freshly-generated item genuinely has no
// translation yet in any locale (a rerun cascade-deletes the old item rows'
// translations along with the rows themselves — see
// vocabulary_item_translation's ON DELETE CASCADE FK in schema.sql), so
// unlike server/export_import.go's own enqueueItemBackfill (which must
// check which locales an import actually provided), every site locale
// unconditionally needs a job here.
//
// Best-effort: a lookup or enqueue failure here is logged and skipped, not
// returned as an error — the vocabulary/models list and its items were
// already successfully created by the time this runs, and a downstream
// backfill-enqueue problem must never turn an otherwise-successful
// generate job into a failed one (which would prompt a Retry that redoes
// the entire LLM extraction and wholesale item replacement just to retry
// enqueueing).
func (s *Service) enqueueItemBackfill(ctx context.Context, itemResourceType string, listID int64, position int, language, phrase, transcription string) {
	needsTranscription, err := ime.NeedsTranscriptionForLanguage(ctx, s.db, language)
	if err != nil {
		log.Printf("generate: check needs_transcription for language %s: %v", language, err)
		needsTranscription = false
	}
	if needsTranscription && strings.TrimSpace(transcription) == "" {
		s.enqueueLLMGenerate(ctx, llmGeneratePayload{
			Kind: "transcription", SourceLanguage: language, TargetLanguage: transcriptionTargetLanguage,
			ContentType: itemResourceType, Content: phrase,
			ResourceType: itemResourceType, ResourceID: listID, ItemPosition: position,
		})
	}
	for _, loc := range i18n.Locales {
		s.enqueueLLMGenerate(ctx, llmGeneratePayload{
			Kind: "translation", SourceLanguage: language, TargetLanguage: loc.Code,
			ContentType: itemResourceType, Content: phrase,
			ResourceType: itemResourceType, ResourceID: listID, Locale: loc.Code, ItemPosition: position,
		})
	}
}

// enqueueLLMGenerate marshals and enqueues one llmGeneratePayload under
// ai.JobKind(p.Kind) — logged, not returned, on failure; see
// enqueueItemBackfill's own doc comment on why this stays best-effort.
func (s *Service) enqueueLLMGenerate(ctx context.Context, p llmGeneratePayload) {
	raw, err := json.Marshal(p)
	if err != nil {
		log.Printf("generate: marshal %s backfill payload for %s %d position %d: %v", p.Kind, p.ResourceType, p.ResourceID, p.ItemPosition, err)
		return
	}
	if _, err := s.jobs.Enqueue(ctx, ai.JobKind(p.Kind), jobs.PriorityBackground, raw); err != nil {
		log.Printf("generate: enqueue %s backfill job for %s %d position %d: %v", p.Kind, p.ResourceType, p.ResourceID, p.ItemPosition, err)
	}
}

// lookupExistingVocab/createVocabList dispatch GetBySourceTextID/
// GetBySourceDialogID and CreateFromText/CreateFromDialog on p's resource
// type — the only place vocabulary_lists' two source columns need
// resource-specific handling; decideTargetList itself stays source-agnostic.
func (s *Service) lookupExistingVocab(ctx context.Context, p payload) (id int64, found bool, err error) {
	if p.resourceType() == resourceTypeDialog {
		return s.vocab.GetBySourceDialogID(ctx, p.resourceID())
	}
	return s.vocab.GetBySourceTextID(ctx, p.resourceID())
}

func (s *Service) createVocabList(ctx context.Context, p payload, title, language, script string) (int64, error) {
	if p.resourceType() == resourceTypeDialog {
		return s.vocab.CreateFromDialog(ctx, p.UserID, title, language, script, p.resourceID())
	}
	return s.vocab.CreateFromText(ctx, p.UserID, title, language, script, p.resourceID())
}

// lookupExistingModels/createModelsList mirror lookupExistingVocab/
// createVocabList for models_lists.
func (s *Service) lookupExistingModels(ctx context.Context, p payload) (id int64, found bool, err error) {
	if p.resourceType() == resourceTypeDialog {
		return s.models.GetBySourceDialogID(ctx, p.resourceID())
	}
	return s.models.GetBySourceTextID(ctx, p.resourceID())
}

func (s *Service) createModelsList(ctx context.Context, p payload, title, language, script string) (int64, error) {
	if p.resourceType() == resourceTypeDialog {
		return s.models.CreateFromDialog(ctx, p.UserID, title, language, script, p.resourceID())
	}
	return s.models.CreateFromText(ctx, p.UserID, title, language, script, p.resourceID())
}

// HandleGenerateVocab is the jobs.HandlerFunc registered for both
// KindGenerateVocabFromText and KindGenerateVocabFromDialog. id (this job's
// own id) is unused — unlike ingest's handlers, there is no multi-step
// retry-safety concern here: if this job fails after the list is created
// but before every item is added, a Retry's next run finds the
// already-linked list via lookupExistingVocab and wholesale-replaces its
// (partial) items instead of creating a second list.
func (s *Service) HandleGenerateVocab(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("generate: unmarshal %s payload: %w", KindGenerateVocabFromText, err)
	}
	src, err := s.resolveSource(ctx, p)
	if err != nil {
		return nil, err
	}
	// target_language is fixed to the kind's own name, matching
	// ingest.buildCleaningCall/buildTitleCall's sentinel-target convention
	// (see that function's doc comment) — generate_vocabulary has no real
	// target language of its own, and the admin LLM-prompt editor already
	// fixes this field the same way for every non-"translation" kind.
	out, err := s.ai.Generate(ctx, aiKindGenerateVocabulary, src.Language, aiKindGenerateVocabulary, contentTypeText, src.Body)
	if err != nil {
		return nil, fmt.Errorf("generate: llm call: %w", err)
	}
	items := ParseVocabularyLines(out)

	existingID, found, err := s.lookupExistingVocab(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("generate: look up existing vocabulary list for %s %d: %w", p.resourceType(), p.resourceID(), err)
	}
	decision := decideTargetList(existingID, found)

	var listID int64
	if decision.Reuse {
		listID = decision.ListID
		if err := s.replaceVocabItems(ctx, listID, src.Language, items); err != nil {
			return nil, err
		}
	} else {
		listID, err = s.createVocabList(ctx, p, "Vocabulary: "+src.Title, src.Language, src.Script)
		if err != nil {
			return nil, fmt.Errorf("generate: create vocabulary list for %s %d: %w", p.resourceType(), p.resourceID(), err)
		}
		// Not transactional — matches the export-import feature's own
		// scoping decision for a brand-new list's item creation (see
		// specs/features/phraseforge-export-import.md): a partial failure
		// here just leaves a partially-populated new list, not destroyed
		// existing content.
		for _, it := range items {
			position, err := s.vocab.AddItem(ctx, listID, it.Phrase, it.Grammar, it.Transcription)
			if err != nil {
				return nil, fmt.Errorf("generate: add vocabulary item to new list %d: %w", listID, err)
			}
			s.enqueueItemBackfill(ctx, resourceTypeVocabItem, listID, position, src.Language, it.Phrase, it.Transcription)
		}
	}
	return json.Marshal(result{ListID: listID, ItemCount: len(items)})
}

// HandleGenerateModels is the jobs.HandlerFunc registered for both
// KindGenerateModelsFromText and KindGenerateModelsFromDialog — mirrors
// HandleGenerateVocab, see that method's doc comment.
func (s *Service) HandleGenerateModels(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("generate: unmarshal %s payload: %w", KindGenerateModelsFromText, err)
	}
	src, err := s.resolveSource(ctx, p)
	if err != nil {
		return nil, err
	}
	out, err := s.ai.Generate(ctx, aiKindGenerateModels, src.Language, aiKindGenerateModels, contentTypeText, src.Body)
	if err != nil {
		return nil, fmt.Errorf("generate: llm call: %w", err)
	}
	items := ParseModelsLines(out)

	existingID, found, err := s.lookupExistingModels(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("generate: look up existing models list for %s %d: %w", p.resourceType(), p.resourceID(), err)
	}
	decision := decideTargetList(existingID, found)

	var listID int64
	if decision.Reuse {
		listID = decision.ListID
		if err := s.replaceModelsItems(ctx, listID, src.Language, items); err != nil {
			return nil, err
		}
	} else {
		listID, err = s.createModelsList(ctx, p, "Models: "+src.Title, src.Language, src.Script)
		if err != nil {
			return nil, fmt.Errorf("generate: create models list for %s %d: %w", p.resourceType(), p.resourceID(), err)
		}
		for _, it := range items {
			position, err := s.models.AddItem(ctx, listID, it.Phrase, it.Transcription)
			if err != nil {
				return nil, fmt.Errorf("generate: add models item to new list %d: %w", listID, err)
			}
			s.enqueueItemBackfill(ctx, resourceTypeModelsItem, listID, position, src.Language, it.Phrase, it.Transcription)
		}
	}
	return json.Marshal(result{ListID: listID, ItemCount: len(items)})
}

// replaceVocabItems wholesale-replaces listID's items — deleting every
// existing position (highest first, so no lower position ever needs to
// shift mid-replace, see vocabulary.Store.DeleteItem's own doc comment) then
// adding every freshly-generated item, as a single transaction using
// vocabulary.Store's exported Begin/DeleteItemTx/AddItemTx primitives (added
// for phraseforge-export-import's own wholesale item replacement — reused
// directly here rather than reimplemented, per this feature's own Approach).
func (s *Service) replaceVocabItems(ctx context.Context, listID int64, language string, items []vocabulary.Item) error {
	existing, err := s.vocab.Items(ctx, listID)
	if err != nil {
		return fmt.Errorf("generate: read existing vocabulary items for list %d: %w", listID, err)
	}
	tx, err := s.vocab.Begin(ctx)
	if err != nil {
		return fmt.Errorf("generate: begin vocabulary item replace for list %d: %w", listID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	for pos := len(existing) - 1; pos >= 0; pos-- {
		if err := s.vocab.DeleteItemTx(ctx, tx, listID, pos); err != nil {
			return fmt.Errorf("generate: delete existing vocabulary item %d in list %d: %w", pos, listID, err)
		}
	}
	positions := make([]int, len(items))
	for i, it := range items {
		position, err := s.vocab.AddItemTx(ctx, tx, listID, it.Phrase, it.Grammar, it.Transcription)
		if err != nil {
			return fmt.Errorf("generate: add vocabulary item to list %d: %w", listID, err)
		}
		positions[i] = position
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("generate: commit vocabulary item replace for list %d: %w", listID, err)
	}
	// Backfill enqueueing happens only once the transaction has actually
	// committed (matching server/export_import.go's own itemBackfillSpec
	// pattern) — an item that gets rolled back must never have a backfill
	// job enqueued against it.
	for i, it := range items {
		s.enqueueItemBackfill(ctx, resourceTypeVocabItem, listID, positions[i], language, it.Phrase, it.Transcription)
	}
	return nil
}

// replaceModelsItems mirrors replaceVocabItems — see that method's doc
// comment.
func (s *Service) replaceModelsItems(ctx context.Context, listID int64, language string, items []models.Item) error {
	existing, err := s.models.Items(ctx, listID)
	if err != nil {
		return fmt.Errorf("generate: read existing models items for list %d: %w", listID, err)
	}
	tx, err := s.models.Begin(ctx)
	if err != nil {
		return fmt.Errorf("generate: begin models item replace for list %d: %w", listID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	for pos := len(existing) - 1; pos >= 0; pos-- {
		if err := s.models.DeleteItemTx(ctx, tx, listID, pos); err != nil {
			return fmt.Errorf("generate: delete existing models item %d in list %d: %w", pos, listID, err)
		}
	}
	positions := make([]int, len(items))
	for i, it := range items {
		position, err := s.models.AddItemTx(ctx, tx, listID, it.Phrase, it.Transcription)
		if err != nil {
			return fmt.Errorf("generate: add models item to list %d: %w", listID, err)
		}
		positions[i] = position
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("generate: commit models item replace for list %d: %w", listID, err)
	}
	// Backfill enqueueing happens only once the transaction has actually
	// committed — see replaceVocabItems's own doc comment.
	for i, it := range items {
		s.enqueueItemBackfill(ctx, resourceTypeModelsItem, listID, positions[i], language, it.Phrase, it.Transcription)
	}
	return nil
}

// targetListDecision is decideTargetList's result — whether to reuse an
// already-linked list (by id) or create a new one.
type targetListDecision struct {
	Reuse  bool
	ListID int64
}

// decideTargetList decides which list a generate job should write into,
// given whatever GetBySourceTextID already found — pulled out as a pure
// function (matching this codebase's decideBackfill/decideItemBackfill
// convention) so this decision is unit-testable without a database.
func decideTargetList(existingID int64, found bool) targetListDecision {
	if found {
		return targetListDecision{Reuse: true, ListID: existingID}
	}
	return targetListDecision{Reuse: false}
}
