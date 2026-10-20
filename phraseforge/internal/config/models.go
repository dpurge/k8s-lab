package config

import (
	"fmt"
	"log/slog"
	"os"
	"strings"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

// EnvModelsFile names the environment variable with the path of models.yaml,
// the providers and models the router can call.
const EnvModelsFile = "PHRASEFORGE_MODELS_FILE"

// DefaultModelsFile is where the ConfigMap mounts models.yaml.
const DefaultModelsFile = "/etc/phraseforge/models.yaml"

// LoadModels reads models.yaml (path from EnvModelsFile) and checks it against
// cfg's purposes: the file must be valid and every purpose must match a model.
// A missing credential is never an error: a purpose whose matching models are
// all on providers without a usable credential only gets a startup warning
// (the router skips such providers until the credential appears). Unlike Load
// it fails when the file is missing, so only the server calls it: `migrate`
// runs before the ConfigMap exists. The error lists every problem found and
// never a key value.
func LoadModels(cfg Config) (*modelcfg.Config, error) {
	path := env(EnvModelsFile, DefaultModelsFile)
	models, err := modelcfg.Load(path)
	if err != nil {
		return nil, fmt.Errorf("models file %s (%s): %w", path, EnvModelsFile, err)
	}
	findings := ValidatePurposes(cfg, models)
	if len(findings) > 0 {
		return nil, fmt.Errorf("models file %s (%s) does not fit the configuration, %d problem(s):\n  - %s",
			path, EnvModelsFile, len(findings), strings.Join(findings, "\n  - "))
	}
	for _, np := range PurposesWithoutUsableModel(cfg, models, func(p modelcfg.Provider) bool {
		return modelcfg.CredentialUsable(p, os.Getenv, os.ReadFile)
	}) {
		slog.Warn("purpose has no usable model: every model matching its model_tags is on a provider without a credential",
			"purpose", np.Name, "model_tags", np.Purpose.ModelTags)
	}
	return models, nil
}

// PurposesWithoutUsableModel returns the purposes that match at least one model
// but whose matching models are all on providers for which usable is false. A
// purpose that matches no model at all is not listed: ValidatePurposes reports
// it as an error.
func PurposesWithoutUsableModel(cfg Config, models *modelcfg.Config, usable func(modelcfg.Provider) bool) []NamedPurpose {
	var out []NamedPurpose
	for _, np := range cfg.Purposes() {
		matched, served := false, false
		for _, m := range models.Models {
			if !router.HasAllTags(m.Tags, np.Purpose.ModelTags) {
				continue
			}
			matched = true
			if provider, ok := models.Provider(m.Provider); ok && usable(provider) {
				served = true
			}
		}
		if matched && !served {
			out = append(out, np)
		}
	}
	return out
}

// ValidatePurposes returns one finding for every purpose whose model_tags match
// no model (a model matches when it has all the tags), and for every matching
// Ollama model whose options.num_ctx is below the purpose's effective numCtx:
// the purpose sizes its input to that window and sends it with each call, which
// a model that supports less cannot honour. Providers of other types take no
// num_ctx. Each finding names the purpose by its key in the config file, and
// the model by its id.
func ValidatePurposes(cfg Config, models *modelcfg.Config) []string {
	var findings []string
	for _, np := range cfg.Purposes() {
		matched, tooSmall := NumCtxFindings("purpose", np.Name, models, np.Purpose.ModelTags, EffectiveNumCtx(np.Purpose.NumCtx))
		findings = append(findings, tooSmall...)
		if !matched {
			findings = append(findings, fmt.Sprintf("purpose %s: no model has all of its model_tags [%s]; add a model with those tags to models.yaml", np.Name, strings.Join(np.Purpose.ModelTags, ", ")))
		}
	}
	return findings
}

// NumCtxFindings looks at the models that have all of tags and returns one
// finding for each Ollama one whose options.num_ctx is below needed, the window
// the subject sizes its input to; matched says whether any model has all the
// tags. noun and name say what is being checked ("purpose" and its key in the
// config file, or "kind" and the kind of an admin tag override) and are used in
// the finding, which also names the model by its id. Providers of other types
// take no num_ctx.
func NumCtxFindings(noun, name string, models *modelcfg.Config, tags []string, needed int) (matched bool, findings []string) {
	for _, m := range models.Models {
		if !router.HasAllTags(m.Tags, tags) {
			continue
		}
		matched = true
		provider, _ := models.Provider(m.Provider)
		if provider.Type != "ollama" || m.Options.NumCtx >= needed {
			continue
		}
		if m.Options.NumCtx == 0 {
			findings = append(findings, fmt.Sprintf("%s %s, model %s: options.num_ctx is not set; set options.num_ctx to at least %d", noun, name, m.ID, needed))
			continue
		}
		findings = append(findings, fmt.Sprintf("%s %s, model %s: options.num_ctx is %d, below the %d the %s needs (numCtx); set options.num_ctx to at least %d", noun, name, m.ID, m.Options.NumCtx, needed, noun, needed))
	}
	return matched, findings
}
