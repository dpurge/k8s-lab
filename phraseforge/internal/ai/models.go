package ai

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"phraseforge/internal/config"
)

// ModelInfo describes one configured model for the admin UI. It carries
// nothing that locates or authenticates the model: no base URL, key or key
// variable name.
type ModelInfo struct {
	ID         string   `json:"id"`
	ProviderID string   `json:"provider_id"`
	Tags       []string `json:"tags"`
	Priority   int      `json:"priority"`
	// Available is false while the router's breaker holds the model, or its
	// provider, out of service, or its provider lacks a credential.
	Available bool `json:"available"`
	// UnavailableReason says why it is not available: "cooldown" while the
	// breaker holds it, "missing_credentials" when its provider lacks a
	// credential. Omitted when it is available. It never says which variable or
	// file the credential comes from.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// Models lists the models in models.yaml in the order of the file, with their
// current availability; none before SetRouter.
func (s *Service) Models() []ModelInfo {
	if s.router == nil {
		return nil
	}
	status := s.router.Status()
	out := make([]ModelInfo, len(status))
	for i, m := range status {
		out[i] = ModelInfo{ID: m.ID, ProviderID: m.Provider, Tags: slices.Clone(m.Tags), Priority: m.Priority, Available: m.Available, UnavailableReason: m.Reason}
	}
	return out
}

// KnownTags returns the distinct tags of all models in models.yaml, sorted;
// none before SetRouter.
func (s *Service) KnownTags() []string {
	if s.models == nil {
		return nil
	}
	seen := map[string]bool{}
	var tags []string
	for _, m := range s.models.Models {
		for _, tag := range m.Tags {
			if !seen[tag] {
				seen[tag] = true
				tags = append(tags, tag)
			}
		}
	}
	sort.Strings(tags)
	return tags
}

// StoredModelTags is the value a llm_prompts.model_tags column takes for
// tags: nil (NULL, "inherit the purpose's model_tags") when there are none,
// because the column's check rejects an empty list.
func StoredModelTags(tags []string) []string {
	if len(tags) == 0 {
		return nil
	}
	return tags
}

// ValidateModelTags checks the tags an llm_prompts row of kind may carry: none
// (inherit), or tags that are each non-blank and present on some model, and
// that one model has all of together, since a call needs a single model with
// every tag. As for the purposes' own tags at startup (config.ValidatePurposes),
// no Ollama model with all of the tags may have an options.num_ctx below the
// numCtx of the kind's purpose. The error names the offending tags, or the kind
// and model.
func (s *Service) ValidateModelTags(kind string, tags []string) error {
	if len(tags) == 0 {
		return nil
	}
	if slices.ContainsFunc(tags, func(tag string) bool { return strings.TrimSpace(tag) == "" }) {
		return fmt.Errorf("model_tags must not contain a blank tag")
	}
	known := s.KnownTags()
	var unknown []string
	for _, tag := range tags {
		if !slices.Contains(known, tag) {
			unknown = append(unknown, tag)
		}
	}
	if len(unknown) > 0 {
		return fmt.Errorf("unknown model tags [%s]; known tags are [%s]", strings.Join(unknown, ", "), strings.Join(known, ", "))
	}
	needed := config.EffectiveNumCtx(s.purposeDefault(kind).NumCtx)
	matched, tooSmall := config.NumCtxFindings("kind", kind, s.models, tags, needed)
	if !matched {
		return fmt.Errorf("no model has all of the tags [%s] together", strings.Join(tags, ", "))
	}
	if len(tooSmall) > 0 {
		return fmt.Errorf("the tags [%s] route %s to a model whose context window is too small: %s", strings.Join(tags, ", "), kind, strings.Join(tooSmall, "; "))
	}
	return nil
}

// DefaultModelTags maps each kind to the tags its calls need without an
// llm_prompts override.
func (s *Service) DefaultModelTags() map[string][]string {
	defaults := make(map[string][]string, len(ValidKinds))
	for _, kind := range ValidKinds {
		defaults[kind] = s.purposeDefault(kind).ModelTags
	}
	return defaults
}
