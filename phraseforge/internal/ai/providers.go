package ai

import "sort"

// Providers returns the configured provider registry's keys (e.g. "ollama",
// "openrouter") in sorted order. The admin API uses this to advertise which
// provider values are valid for a saved llm_prompts row, and the admin UI
// uses it to populate the provider dropdown.
func (s *Service) Providers() []string {
	keys := make([]string, 0, len(s.cfg.Providers))
	for k := range s.cfg.Providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
