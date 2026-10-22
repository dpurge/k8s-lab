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
const EnvModelsFile = "KNOWLEDGE_MODELS_FILE"

// DefaultModelsFile is where the ConfigMap mounts models.yaml.
const DefaultModelsFile = "/etc/knowledge/models.yaml"

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
	findings := append(ValidatePurposes(cfg, models), ValidateEmbeddings(cfg, models)...)
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
// Ollama model whose options.num_ctx is below the purpose's effective num_ctx:
// the purpose sizes its input to that window and sends it with each call, which
// a model that supports less cannot honour. Providers of other types take no
// num_ctx. Each finding names the purpose by its key in the config file, and
// the model by its id.
func ValidatePurposes(cfg Config, models *modelcfg.Config) []string {
	var findings []string
	for _, np := range cfg.Purposes() {
		needed := EffectiveNumCtx(np.Purpose.NumCtx)
		matched := false
		for _, m := range models.Models {
			if !router.HasAllTags(m.Tags, np.Purpose.ModelTags) {
				continue
			}
			matched = true
			provider, _ := models.Provider(m.Provider)
			if provider.Type != "ollama" || m.Options.NumCtx >= needed {
				continue
			}
			if m.Options.NumCtx == 0 {
				findings = append(findings, fmt.Sprintf("purpose %s, model %s: options.num_ctx is not set; set options.num_ctx to at least %d", np.Name, m.ID, needed))
				continue
			}
			findings = append(findings, fmt.Sprintf("purpose %s, model %s: options.num_ctx is %d, below the %d the purpose needs (num_ctx); set options.num_ctx to at least %d", np.Name, m.ID, m.Options.NumCtx, needed, needed))
		}
		if !matched {
			findings = append(findings, fmt.Sprintf("purpose %s: no model has all of its model_tags [%s]; add a model with those tags to models.yaml", np.Name, strings.Join(np.Purpose.ModelTags, ", ")))
		}
	}
	return findings
}

// ValidateEmbeddings returns a finding when no model of the configured embedding
// space is in models.yaml, and one for every model of that space whose dimension
// is not the configured one: the vector store was built for vectors of that
// length, and the router would only reject every reply. Nothing is checked when
// the embeddings are fake.
func ValidateEmbeddings(cfg Config, models *modelcfg.Config) []string {
	if cfg.Embeddings.Fake {
		return nil
	}
	var findings []string
	found := false
	for _, m := range models.Models {
		if m.EmbeddingSpace != cfg.Embeddings.Space {
			continue
		}
		found = true
		if m.Dimension != cfg.Embeddings.Dimension {
			findings = append(findings, fmt.Sprintf("embeddings, model %s: dimension is %d, but embeddings.dimension is %d", m.ID, m.Dimension, cfg.Embeddings.Dimension))
		}
	}
	if !found {
		findings = append(findings, fmt.Sprintf("embeddings: no model has embedding_space %q; add an embedding model of that space (embedding_space and dimension) to models.yaml", cfg.Embeddings.Space))
	}
	return findings
}
