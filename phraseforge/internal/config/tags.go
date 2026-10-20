package config

import (
	"fmt"
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultModelTag is the tag every purpose asks for unless the config file says
// otherwise, and the tag of the model in models.example.yaml, so that a
// zero-config local run loads.
const DefaultModelTag = "default"

func defaultModelTags() []string { return []string{DefaultModelTag} }

// modelTagFindings reports what is wrong with one purpose's model_tags: none at
// all, a blank tag, or a tag twice.
func modelTagFindings(purpose string, tags []string) []string {
	if len(tags) == 0 {
		return []string{fmt.Sprintf("%s.model_tags is empty; list at least one tag (the models with all of them serve this purpose)", purpose)}
	}
	var findings []string
	seen := map[string]bool{}
	for i, tag := range tags {
		trimmed := strings.TrimSpace(tag)
		switch {
		case trimmed == "":
			findings = append(findings, fmt.Sprintf("%s.model_tags[%d] is blank", purpose, i))
		case seen[trimmed]:
			findings = append(findings, fmt.Sprintf("%s.model_tags lists %q more than once", purpose, trimmed))
		}
		seen[trimmed] = true
	}
	return findings
}

// legacyKeyFindings reports every key the file still has from before models
// moved to models.yaml, each with what replaces it. The typed decode ignores
// unknown keys, so without this a stale file would load and silently route
// every purpose to the default model.
func legacyKeyFindings(data []byte) ([]string, error) {
	var root yaml.Node
	if err := yaml.Unmarshal(data, &root); err != nil {
		return nil, err
	}
	top := mappingOf(&root)
	if top == nil {
		return nil, nil
	}
	var findings []string
	if mappingValue(top, "providers") != nil {
		findings = append(findings, "providers: replaced by the providers in models.yaml; the Ollama key is now api_key_from")
	}
	findings = append(findings, renamedKeyFindings(top)...)
	for _, p := range (fileConfig{}).purposes() {
		purpose := mappingValue(top, p.name)
		if purpose == nil || purpose.Kind != yaml.MappingNode {
			continue
		}
		if mappingValue(purpose, "modelTags") != nil {
			findings = append(findings, fmt.Sprintf("%s.modelTags: renamed to model_tags", p.name))
		}
		for _, key := range []string{"provider", "model"} {
			if mappingValue(purpose, key) != nil {
				findings = append(findings, fmt.Sprintf("%s.%s: replaced by model_tags; models are configured in models.yaml (%s)", p.name, key, EnvModelsFile))
			}
		}
		if mappingValue(purpose, "think") != nil {
			findings = append(findings, fmt.Sprintf("%s.think: moved to the model's options.think in models.yaml", p.name))
		}
	}
	return findings, nil
}

// renamedKeyFindings reports the old camelCase spellings of the keys of the
// model router (llmCalls, jobs.retentionDays*). The typed decode ignores them,
// so a file that kept one would load with the default retention instead.
func renamedKeyFindings(top *yaml.Node) []string {
	var findings []string
	if mappingValue(top, "llmCalls") != nil {
		findings = append(findings, "llmCalls: renamed to llm_calls (and retentionDays under it to retention_days)")
	}
	if calls := mappingValue(top, "llm_calls"); calls != nil && calls.Kind == yaml.MappingNode && mappingValue(calls, "retentionDays") != nil {
		findings = append(findings, "llm_calls.retentionDays: renamed to llm_calls.retention_days")
	}
	if jobs := mappingValue(top, "jobs"); jobs != nil && jobs.Kind == yaml.MappingNode {
		for _, key := range []struct{ old, replacement string }{
			{"retentionDaysDone", "retention_days_done"},
			{"retentionDaysFailed", "retention_days_failed"},
		} {
			if mappingValue(jobs, key.old) != nil {
				findings = append(findings, fmt.Sprintf("jobs.%s: renamed to jobs.%s", key.old, key.replacement))
			}
		}
	}
	return findings
}

// mappingOf returns the top-level mapping of a parsed document, or nil.
func mappingOf(doc *yaml.Node) *yaml.Node {
	if doc.Kind == yaml.DocumentNode && len(doc.Content) == 1 {
		doc = doc.Content[0]
	}
	if doc.Kind != yaml.MappingNode {
		return nil
	}
	return doc
}

// mappingValue returns the value node under key in a mapping node, or nil.
func mappingValue(mapping *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(mapping.Content); i += 2 {
		if mapping.Content[i].Value == key {
			return mapping.Content[i+1]
		}
	}
	return nil
}
