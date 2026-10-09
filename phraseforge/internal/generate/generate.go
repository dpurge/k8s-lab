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

	"github.com/jackc/pgx/v5/pgxpool"

	"phraseforge/internal/ai"
	"phraseforge/internal/checkpoint"
	"phraseforge/internal/dialogs"
	"phraseforge/internal/i18n"
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
	// SkippedLines counts model output lines that stayed malformed after the
	// correction rounds and were dropped; omitted when none were.
	SkippedLines int `json:"skipped_lines,omitempty"`
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

// enqueueItemBackfill enqueues the structured item calls (ai.ItemJobsFor)
// for one just-created/replaced vocabulary/models item — every site locale it
// isn't already translated in (its phrase may be, through another list); each
// call also fills grammar/transcription where the language has a section.
// Per the user's direction: "when vocabulary is created also jobs to
// translate all words to all site languages are submitted".
//
// Best-effort: a lookup or enqueue failure here is logged and skipped, not
// returned as an error — the vocabulary/models list and its items were
// already successfully created by the time this runs, and a downstream
// backfill-enqueue problem must never turn an otherwise-successful
// generate job into a failed one (which would prompt a Retry that redoes
// the entire LLM extraction and wholesale item replacement just to retry
// enqueueing).
func (s *Service) enqueueItemBackfill(ctx context.Context, itemResourceType string, listID int64, position int, language, phrase, grammar, transcription string) {
	state := ai.ItemState{ResourceType: itemResourceType, Grammar: grammar, Transcription: transcription}
	// Phrases are shared across lists, so this one may already be translated
	// through another list: don't submit it again.
	var translated map[string]bool
	var err error
	if itemResourceType == resourceTypeVocabItem {
		translated, err = s.vocab.TranslatedLocales(ctx, listID, position)
	} else {
		translated, err = s.models.TranslatedLocales(ctx, listID, position)
	}
	if err != nil {
		log.Printf("generate: read existing translations for %s %d position %d: %v", itemResourceType, listID, position, err)
		return
	}
	state.Translated = translated
	itemJobs, err := s.ai.ItemJobsFor(ctx, state, listID, position, language, phrase, nil, i18n.Locales)
	if err != nil {
		log.Printf("generate: plan item backfill for %s %d position %d: %v", itemResourceType, listID, position, err)
		return
	}
	for _, j := range itemJobs {
		if _, err := s.jobs.Enqueue(ctx, j.Kind, jobs.PriorityBackground, j.Payload); err != nil {
			log.Printf("generate: enqueue %s for %s %d position %d: %v", j.Kind, itemResourceType, listID, position, err)
		}
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
	chunks := s.ai.ChunksFor(aiKindGenerateVocabulary, src.Body)
	items, skipped, err := generateItems(ctx, chunks, s.ai.CorrectionRounds(aiKindGenerateVocabulary),
		func(ctx context.Context, chunk string) (string, error) {
			return s.ai.GenerateLines(ctx, aiKindGenerateVocabulary, src.Language, aiKindGenerateVocabulary, contentTypeText, chunk)
		},
		ParseVocabularyLines,
		func(ctx context.Context, rejected []RejectedLine) (string, error) {
			return s.ai.CorrectLines(ctx, aiKindGenerateVocabulary, src.Language, vocabularyFormatHelp, rejected)
		},
		func(it vocabulary.Item) string { return it.Phrase },
		checkpoint.Start(ctx, "items/"+aiKindGenerateVocabulary+"/"+src.Language, chunks))
	if err != nil {
		return nil, fmt.Errorf("generate: vocabulary: %w", err)
	}

	existingID, found, err := s.lookupExistingVocab(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("generate: look up existing vocabulary list for %s %d: %w", p.resourceType(), p.resourceID(), err)
	}
	decision := decideTargetList(existingID, found)

	var listID int64
	if decision.Reuse {
		listID = decision.ListID
	} else {
		listID, err = s.createVocabList(ctx, p, "Vocabulary: "+src.Title, src.Language, src.Script)
		if err != nil {
			return nil, fmt.Errorf("generate: create vocabulary list for %s %d: %w", p.resourceType(), p.resourceID(), err)
		}
	}
	if err := s.syncVocabItems(ctx, listID, src.Language, items); err != nil {
		return nil, err
	}
	return json.Marshal(result{ListID: listID, ItemCount: len(items), SkippedLines: skipped})
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
	chunks := s.ai.ChunksFor(aiKindGenerateModels, src.Body)
	items, skipped, err := generateItems(ctx, chunks, s.ai.CorrectionRounds(aiKindGenerateModels),
		func(ctx context.Context, chunk string) (string, error) {
			return s.ai.GenerateLines(ctx, aiKindGenerateModels, src.Language, aiKindGenerateModels, contentTypeText, chunk)
		},
		ParseModelsLines,
		func(ctx context.Context, rejected []RejectedLine) (string, error) {
			return s.ai.CorrectLines(ctx, aiKindGenerateModels, src.Language, modelsFormatHelp, rejected)
		},
		func(it models.Item) string { return it.Phrase },
		checkpoint.Start(ctx, "items/"+aiKindGenerateModels+"/"+src.Language, chunks))
	if err != nil {
		return nil, fmt.Errorf("generate: models: %w", err)
	}

	existingID, found, err := s.lookupExistingModels(ctx, p)
	if err != nil {
		return nil, fmt.Errorf("generate: look up existing models list for %s %d: %w", p.resourceType(), p.resourceID(), err)
	}
	decision := decideTargetList(existingID, found)

	var listID int64
	if decision.Reuse {
		listID = decision.ListID
	} else {
		listID, err = s.createModelsList(ctx, p, "Models: "+src.Title, src.Language, src.Script)
		if err != nil {
			return nil, fmt.Errorf("generate: create models list for %s %d: %w", p.resourceType(), p.resourceID(), err)
		}
	}
	if err := s.syncModelsItems(ctx, listID, src.Language, items); err != nil {
		return nil, err
	}
	return json.Marshal(result{ListID: listID, ItemCount: len(items), SkippedLines: skipped})
}

// syncVocabItems makes listID's items exactly the freshly generated items, in
// one transaction, then enqueues the backfill each needs. The items are synced
// (vocabulary.Store.SyncItemsTx), not deleted and re-added: a phrase that is
// generated again keeps its translations, and so is never submitted for
// translation twice. Generated items carry no phrase id, so every one links
// the phrase with its exact fields, creating it only if no list has it.
func (s *Service) syncVocabItems(ctx context.Context, listID int64, language string, items []vocabulary.Item) error {
	syncItems := make([]vocabulary.SyncItem, len(items))
	for i, it := range items {
		syncItems[i] = vocabulary.SyncItem{Phrase: it.Phrase, Grammar: it.Grammar, Transcription: it.Transcription}
	}
	tx, err := s.vocab.Begin(ctx)
	if err != nil {
		return fmt.Errorf("generate: begin vocabulary item sync for list %d: %w", listID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	if err := s.vocab.SyncItemsTx(ctx, tx, listID, syncItems, false); err != nil {
		return fmt.Errorf("generate: sync vocabulary items of list %d: %w", listID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("generate: commit vocabulary item sync for list %d: %w", listID, err)
	}
	// Backfill is enqueued only once the transaction has committed — an item
	// that gets rolled back must never have a backfill job enqueued for it —
	// and from the items as stored (the store normalises the text), since a
	// job's stale-target guard compares against the stored phrase.
	stored, err := s.vocab.Items(ctx, listID)
	if err != nil {
		log.Printf("generate: read synced vocabulary items of list %d for backfill: %v", listID, err)
		return nil
	}
	for _, it := range stored {
		s.enqueueItemBackfill(ctx, resourceTypeVocabItem, listID, it.Position, language, it.Phrase, it.Grammar, it.Transcription)
	}
	return nil
}

// syncModelsItems mirrors syncVocabItems — see that method's doc comment.
func (s *Service) syncModelsItems(ctx context.Context, listID int64, language string, items []models.Item) error {
	syncItems := make([]models.SyncItem, len(items))
	for i, it := range items {
		syncItems[i] = models.SyncItem{Phrase: it.Phrase, Transcription: it.Transcription}
	}
	tx, err := s.models.Begin(ctx)
	if err != nil {
		return fmt.Errorf("generate: begin models item sync for list %d: %w", listID, err)
	}
	defer tx.Rollback(ctx) //nolint:errcheck // no-op once Commit succeeds

	if err := s.models.SyncItemsTx(ctx, tx, listID, syncItems, false); err != nil {
		return fmt.Errorf("generate: sync models items of list %d: %w", listID, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("generate: commit models item sync for list %d: %w", listID, err)
	}
	stored, err := s.models.Items(ctx, listID)
	if err != nil {
		log.Printf("generate: read synced models items of list %d for backfill: %v", listID, err)
		return nil
	}
	for _, it := range stored {
		s.enqueueItemBackfill(ctx, resourceTypeModelsItem, listID, it.Position, language, it.Phrase, "", it.Transcription)
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
// function (matching this codebase's decideBackfill/ai.ItemCallLocales
// convention) so this decision is unit-testable without a database.
func decideTargetList(existingID int64, found bool) targetListDecision {
	if found {
		return targetListDecision{Reuse: true, ListID: existingID}
	}
	return targetListDecision{Reuse: false}
}
