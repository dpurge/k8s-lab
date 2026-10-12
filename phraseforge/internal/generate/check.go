package generate

import (
	"context"
	"log"

	"phraseforge/internal/ai"
	"phraseforge/internal/vocabulary"
)

// maxCheckBatch is how many known candidates one Checker call gets; a longer
// list is checked in several calls, so a call's prompt stays well inside the
// model's context window.
const maxCheckBatch = 20

// KnownCandidate is a generated candidate whose phrase the database already
// knows (it has a translation or an example) and that came with a usage: what
// a Checker needs to tell whether the usage shows a use the examples do not.
type KnownCandidate struct {
	Phrase       string
	Usage        string
	Examples     []string
	Translations map[string]string // non-blank translations by site locale code
}

// Verdict is what a Checker decided for a known candidate: its usage is worth
// one more example, and/or shows a meaning its translations do not cover yet.
type Verdict struct {
	NeedsExample, NeedsSense bool
}

// Checker decides which known candidates need a new example or a new sense. It
// returns the verdicts, by index into known, of those that do; an index it
// leaves out means no job. An error means the whole batch is skipped.
type Checker interface {
	Check(ctx context.Context, language string, known []KnownCandidate) (map[int]Verdict, error)
}

// AICheck is the Checker backed by the checkVocabulary LLM purpose.
type AICheck struct{ AI *ai.Service }

// Check implements Checker with one structured call per batch.
func (c AICheck) Check(ctx context.Context, language string, known []KnownCandidate) (map[int]Verdict, error) {
	items := make([]ai.CheckItem, len(known))
	for i, k := range known {
		items[i] = ai.CheckItem{Phrase: k.Phrase, Usage: k.Usage, Examples: k.Examples, Translations: k.Translations}
	}
	verdicts, err := c.AI.CheckVocabulary(ctx, language, items)
	if err != nil {
		return nil, err
	}
	out := make(map[int]Verdict, len(verdicts))
	for n, v := range verdicts {
		out[n] = Verdict{NeedsExample: v.NeedsExample, NeedsSense: v.NeedsSense}
	}
	return out, nil
}

// SetChecker turns on the step-2 check of known phrases. Without one (nil),
// generating a list queues exactly the item jobs it always did.
func (s *Service) SetChecker(c Checker) { s.checker = c }

// knownCandidates picks, from the items just stored, those the check applies
// to: a usage was generated for them and the phrase already has a translation
// or an example. indexes[i] is the position in stored of known[i]. A phrase
// the sync just created has neither, so it is never known.
func knownCandidates(stored []vocabulary.Item, usages []string, translations map[int64]map[string]string) (known []KnownCandidate, indexes []int) {
	for i, it := range stored {
		if usages[i] == "" || (len(it.Examples) == 0 && len(translations[it.ID]) == 0) {
			continue
		}
		known = append(known, KnownCandidate{Phrase: it.Phrase, Usage: usages[i], Examples: it.Examples, Translations: translations[it.ID]})
		indexes = append(indexes, i)
	}
	return known, indexes
}

// itemsToRevise runs the check for the items of list listID and returns the
// verdicts, by position in stored, of those that need a new example or a new
// sense. It is best-effort, like the
// backfill enqueue: a failed read or a failed batch is logged with the list id
// and those items get no check-driven job, so the generate job still succeeds.
func (s *Service) itemsToRevise(ctx context.Context, listID int64, language string, stored []vocabulary.Item, usages []string) map[int]Verdict {
	if s.checker == nil {
		return nil
	}
	translations, err := s.vocab.TranslationsByItem(ctx, listID)
	if err != nil {
		log.Printf("generate: read translations of vocabulary list %d for the example check: %v", listID, err)
		return nil
	}
	known, indexes := knownCandidates(stored, usages, translations)
	return checkInBatches(ctx, s.checker, listID, language, known, indexes)
}

// checkInBatches calls checker for known in batches of maxCheckBatch and maps
// each verdict back through indexes. Indexes a checker returns outside its
// batch, and verdicts that need nothing, are ignored.
func checkInBatches(ctx context.Context, checker Checker, listID int64, language string, known []KnownCandidate, indexes []int) map[int]Verdict {
	var needs map[int]Verdict
	for start := 0; start < len(known); start += maxCheckBatch {
		end := min(start+maxCheckBatch, len(known))
		verdicts, err := checker.Check(ctx, language, known[start:end])
		if err != nil {
			log.Printf("generate: example check of vocabulary list %d (candidates %d-%d) skipped: %v", listID, start+1, end, err)
			continue
		}
		for n, verdict := range verdicts {
			if (verdict.NeedsExample || verdict.NeedsSense) && n >= 0 && n < end-start {
				if needs == nil {
					needs = map[int]Verdict{}
				}
				needs[indexes[start+n]] = verdict
			}
		}
	}
	return needs
}
