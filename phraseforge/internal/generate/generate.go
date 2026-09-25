// Package generate implements the generate_vocab_from_text/
// generate_models_from_text background job handlers (see
// specs/features/phraseforge-generate-vocab-models-from-text.md): each reads
// a Text row, asks ai.Service.Generate to extract vocabulary/grammar items
// from its body, parses the line-based response, then either wholesale-
// replaces an already-linked list's items (a rerun) or creates a new list
// linked to the text via source_text_id (the first run) — mirroring
// phraseforge/internal/ingest's own package structure (job handlers holding
// references to the stores/ai.Service they need).
package generate

import (
	"context"
	"encoding/json"
	"fmt"

	"phraseforge/internal/ai"
	"phraseforge/internal/models"
	"phraseforge/internal/texts"
	"phraseforge/internal/vocabulary"
)

// KindGenerateVocabFromText and KindGenerateModelsFromText are the
// jobs.Service kinds registered for HandleGenerateVocabFromText/
// HandleGenerateModelsFromText — exported so main.go can call
// jobsSvc.Register(generate.KindGenerateVocabFromText, ...).
const (
	KindGenerateVocabFromText  = "generate_vocab_from_text"
	KindGenerateModelsFromText = "generate_models_from_text"
)

// aiKindGenerateVocabulary/aiKindGenerateModels are the ai.Service.Generate
// purpose kinds these jobs call — see ai.ValidKinds. contentType is fixed to
// "text" for both: the input is always a Text row's body, never a Dialog's.
const (
	aiKindGenerateVocabulary = "generate_vocabulary"
	aiKindGenerateModels     = "generate_models"
	contentTypeText          = "text"
)

// payload is the generate_vocab_from_text/generate_models_from_text job
// payload shape, constructed and enqueued by the two new HTTP handlers in
// phraseforge/internal/server. UserID becomes a newly created list's owner
// (Create's own userID convention, matching every other list-creating
// endpoint) — only used on the first run for a given text; a rerun instead
// reuses the already-linked list's existing owner.
type payload struct {
	TextID int64 `json:"text_id"`
	UserID int64 `json:"user_id"`
}

// result is this package's job result shape — nothing reads it today
// (fire-and-forget, matching ingest.processResult's own doc comment), but a
// job's result column is always populated with something sensible.
type result struct {
	ListID    int64 `json:"list_id"`
	ItemCount int   `json:"item_count"`
}

// Service implements the generate_vocab_from_text/generate_models_from_text
// job handlers.
type Service struct {
	texts  *texts.Store
	vocab  *vocabulary.Store
	models *models.Store
	ai     *ai.Service
}

func New(textsStore *texts.Store, vocabStore *vocabulary.Store, modelsStore *models.Store, aiSvc *ai.Service) *Service {
	return &Service{texts: textsStore, vocab: vocabStore, models: modelsStore, ai: aiSvc}
}

// HandleGenerateVocabFromText is the jobs.HandlerFunc registered for
// KindGenerateVocabFromText. id (this job's own id) is unused — unlike
// ingest's handlers, there is no multi-step retry-safety concern here: if
// this job fails after CreateFromText succeeds but before every item is
// added, a Retry's next run finds the already-linked list via
// GetBySourceTextID and wholesale-replaces its (partial) items instead of
// creating a second list.
func (s *Service) HandleGenerateVocabFromText(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("generate: unmarshal %s payload: %w", KindGenerateVocabFromText, err)
	}
	t, err := s.texts.Get(ctx, p.TextID)
	if err != nil {
		return nil, fmt.Errorf("generate: read text %d: %w", p.TextID, err)
	}
	// target_language is fixed to the kind's own name, matching
	// ingest.buildCleaningCall/buildTitleCall's sentinel-target convention
	// (see that function's doc comment) — generate_vocabulary has no real
	// target language of its own, and the admin LLM-prompt editor already
	// fixes this field the same way for every non-"translation" kind.
	out, err := s.ai.Generate(ctx, aiKindGenerateVocabulary, t.Language, aiKindGenerateVocabulary, contentTypeText, t.Body)
	if err != nil {
		return nil, fmt.Errorf("generate: llm call: %w", err)
	}
	items := ParseVocabularyLines(out)

	existingID, found, err := s.vocab.GetBySourceTextID(ctx, p.TextID)
	if err != nil {
		return nil, fmt.Errorf("generate: look up existing vocabulary list for text %d: %w", p.TextID, err)
	}
	decision := decideTargetList(existingID, found)

	var listID int64
	if decision.Reuse {
		listID = decision.ListID
		if err := s.replaceVocabItems(ctx, listID, items); err != nil {
			return nil, err
		}
	} else {
		listID, err = s.vocab.CreateFromText(ctx, p.UserID, "Vocabulary: "+t.Title, t.Language, t.Script, p.TextID)
		if err != nil {
			return nil, fmt.Errorf("generate: create vocabulary list for text %d: %w", p.TextID, err)
		}
		// Not transactional — matches the export-import feature's own
		// scoping decision for a brand-new list's item creation (see
		// specs/features/phraseforge-export-import.md): a partial failure
		// here just leaves a partially-populated new list, not destroyed
		// existing content.
		for _, it := range items {
			if _, err := s.vocab.AddItem(ctx, listID, it.Phrase, it.Grammar, it.Transcription); err != nil {
				return nil, fmt.Errorf("generate: add vocabulary item to new list %d: %w", listID, err)
			}
		}
	}
	return json.Marshal(result{ListID: listID, ItemCount: len(items)})
}

// HandleGenerateModelsFromText is the jobs.HandlerFunc registered for
// KindGenerateModelsFromText — mirrors HandleGenerateVocabFromText, see that
// method's doc comment.
func (s *Service) HandleGenerateModelsFromText(ctx context.Context, id string, raw json.RawMessage) (json.RawMessage, error) {
	var p payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, fmt.Errorf("generate: unmarshal %s payload: %w", KindGenerateModelsFromText, err)
	}
	t, err := s.texts.Get(ctx, p.TextID)
	if err != nil {
		return nil, fmt.Errorf("generate: read text %d: %w", p.TextID, err)
	}
	out, err := s.ai.Generate(ctx, aiKindGenerateModels, t.Language, aiKindGenerateModels, contentTypeText, t.Body)
	if err != nil {
		return nil, fmt.Errorf("generate: llm call: %w", err)
	}
	items := ParseModelsLines(out)

	existingID, found, err := s.models.GetBySourceTextID(ctx, p.TextID)
	if err != nil {
		return nil, fmt.Errorf("generate: look up existing models list for text %d: %w", p.TextID, err)
	}
	decision := decideTargetList(existingID, found)

	var listID int64
	if decision.Reuse {
		listID = decision.ListID
		if err := s.replaceModelsItems(ctx, listID, items); err != nil {
			return nil, err
		}
	} else {
		listID, err = s.models.CreateFromText(ctx, p.UserID, "Models: "+t.Title, t.Language, t.Script, p.TextID)
		if err != nil {
			return nil, fmt.Errorf("generate: create models list for text %d: %w", p.TextID, err)
		}
		for _, it := range items {
			if _, err := s.models.AddItem(ctx, listID, it.Phrase, it.Transcription); err != nil {
				return nil, fmt.Errorf("generate: add models item to new list %d: %w", listID, err)
			}
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
func (s *Service) replaceVocabItems(ctx context.Context, listID int64, items []vocabulary.Item) error {
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
	for _, it := range items {
		if _, err := s.vocab.AddItemTx(ctx, tx, listID, it.Phrase, it.Grammar, it.Transcription); err != nil {
			return fmt.Errorf("generate: add vocabulary item to list %d: %w", listID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("generate: commit vocabulary item replace for list %d: %w", listID, err)
	}
	return nil
}

// replaceModelsItems mirrors replaceVocabItems — see that method's doc
// comment.
func (s *Service) replaceModelsItems(ctx context.Context, listID int64, items []models.Item) error {
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
	for _, it := range items {
		if _, err := s.models.AddItemTx(ctx, tx, listID, it.Phrase, it.Transcription); err != nil {
			return fmt.Errorf("generate: add models item to list %d: %w", listID, err)
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("generate: commit models item replace for list %d: %w", listID, err)
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
