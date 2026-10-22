package modelrouter

import (
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// ModelInfo is one configured model as a page shows it: configured ids, tags,
// priority and whether the router can use it now. It never carries a base URL,
// a key, the name of a key variable or the provider's own name for the model.
type ModelInfo struct {
	ID         string   `json:"id"`
	ProviderID string   `json:"provider_id"`
	Tags       []string `json:"tags"`
	Priority   int      `json:"priority"`
	// EmbeddingSpace is the vector space of an embedding model; omitted for a chat
	// model.
	EmbeddingSpace string `json:"embedding_space,omitempty"`
	// Available is false while the breaker holds the model, or its provider, out
	// of service, or its provider lacks a credential.
	Available bool `json:"available"`
	// UnavailableReason is "cooldown" while the breaker holds the model and
	// "missing_credentials" when its provider lacks a credential; omitted when it
	// is available.
	UnavailableReason string `json:"unavailable_reason,omitempty"`
}

// Models lists the models of cfg in the order of the file, with the router's
// current view of each.
func Models(cfg *modelcfg.Config, r *router.Router) []ModelInfo {
	space := make(map[string]string, len(cfg.Models))
	for _, m := range cfg.Models {
		space[m.ID] = m.EmbeddingSpace
	}
	status := r.Status()
	out := make([]ModelInfo, len(status))
	for i, s := range status {
		tags := s.Tags
		if tags == nil {
			tags = []string{}
		}
		out[i] = ModelInfo{
			ID: s.ID, ProviderID: s.Provider, Tags: tags, Priority: s.Priority,
			EmbeddingSpace: space[s.ID], Available: s.Available, UnavailableReason: s.Reason,
		}
	}
	return out
}
