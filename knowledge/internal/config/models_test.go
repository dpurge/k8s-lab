package config

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"k8s-lab/shared/modelcfg"
)

func defaultConfig(t *testing.T) Config {
	t.Helper()
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

func writeModels(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvModelsFile, path)
}

const oneProvider = "providers:\n  - id: o\n    type: ollama\n    base_url: http://o:11434\n    api_key_from: \"\"\n"

// embedModel is a bge-m3 embedding model of the given dimension on provider o.
func embedModel(id string, dimension int) string {
	return "  - id: " + id + "\n    provider: o\n    model: bge-m3\n    tags: [embed]\n    priority: 10\n    embedding_space: bge-m3\n    dimension: " + strconv.Itoa(dimension) + "\n"
}

func model(id, tags string, numCtx int) string {
	s := "  - id: " + id + "\n    provider: o\n    model: m\n    tags: [" + tags + "]\n    priority: 10\n"
	if numCtx > 0 {
		s += "    options: { num_ctx: " + strconv.Itoa(numCtx) + " }\n"
	}
	return s
}

func TestDevModelsFileFitsTheDefaultPurposes(t *testing.T) {
	cfg := defaultConfig(t)
	t.Setenv(EnvModelsFile, "../../models.dev.yaml")
	if _, err := LoadModels(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestLoadModelsNamesTheVariableAndFileWhenMissingOrInvalid(t *testing.T) {
	cfg := defaultConfig(t)
	missing := filepath.Join(t.TempDir(), "nope.yaml")
	t.Setenv(EnvModelsFile, missing)
	if _, err := LoadModels(cfg); err == nil || !strings.Contains(err.Error(), EnvModelsFile) || !strings.Contains(err.Error(), missing) {
		t.Errorf("missing file: err = %v, want it to name %s and the file", err, EnvModelsFile)
	}
	writeModels(t, "providers: not-a-list\n")
	if _, err := LoadModels(cfg); err == nil || !strings.Contains(err.Error(), EnvModelsFile) {
		t.Errorf("invalid file: err = %v, want it to name %s", err, EnvModelsFile)
	}
}

func TestLoadModelsDefaultsToTheMountedPath(t *testing.T) {
	cfg := defaultConfig(t)
	t.Setenv(EnvModelsFile, "")
	_, err := LoadModels(cfg)
	if err == nil || !strings.Contains(err.Error(), DefaultModelsFile) {
		t.Errorf("err = %v, want it to name the default path %s", err, DefaultModelsFile)
	}
}

func TestLoadModelsFailsWhenNoModelHasAPurposesTags(t *testing.T) {
	cfg := defaultConfig(t)
	// chat needs [chat] and the others [default]; this file has only "default".
	writeModels(t, oneProvider+"models:\n"+model("a", "default", 8192)+embedModel("e", 1024))
	_, err := LoadModels(cfg)
	if err == nil || !strings.Contains(err.Error(), "purpose chat: no model has all of its model_tags [chat]") {
		t.Errorf("err = %v, want chat named as matching no model", err)
	}
	if err != nil && strings.Contains(err.Error(), "purpose translate") {
		t.Errorf("err = %v, want translate (tag default) to be fine", err)
	}
}

func TestLoadModelsChecksTheContextWindowOfOllamaModels(t *testing.T) {
	cfg := defaultConfig(t)
	writeModels(t, oneProvider+"models:\n"+model("small", "default, chat", 4096)+embedModel("e", 1024))
	_, err := LoadModels(cfg)
	if err == nil || !strings.Contains(err.Error(), "purpose chat, model small: options.num_ctx is 4096, below the 8192") {
		t.Errorf("err = %v, want chat's 8192 window refused for a 4096 model", err)
	}
	writeModels(t, oneProvider+"models:\n"+model("unset", "default, chat", 0)+embedModel("e", 1024))
	_, err = LoadModels(cfg)
	if err == nil || !strings.Contains(err.Error(), "model unset: options.num_ctx is not set") {
		t.Errorf("err = %v, want an unset num_ctx on an Ollama model refused", err)
	}
}

func TestLoadModelsDoesNotCheckTheWindowOfOtherProviderTypes(t *testing.T) {
	cfg := defaultConfig(t)
	writeModels(t, ""+
		"providers:\n  - id: or\n    type: openai\n    base_url: https://openrouter.ai/api/v1\n    api_key_from: env:OPENROUTER_API_KEY\n"+
		"models:\n  - id: remote\n    provider: or\n    model: x/y\n    tags: [default, chat]\n    priority: 10\n"+
		"  - id: e\n    provider: or\n    model: text-embedding-3-small\n    tags: [embed]\n    priority: 10\n    embedding_space: bge-m3\n    dimension: 1024\n")
	if _, err := LoadModels(cfg); err != nil {
		t.Errorf("LoadModels() error = %v, want nil: a non-Ollama model takes no num_ctx", err)
	}
}

func TestPurposesWithoutUsableModelListsPurposesWhoseModelsAllLackACredential(t *testing.T) {
	cfg := defaultConfig(t)
	writeModels(t, ""+
		"providers:\n  - id: or\n    type: openai\n    base_url: https://openrouter.ai/api/v1\n    api_key_from: env:OPENROUTER_API_KEY\n"+
		"models:\n  - id: remote\n    provider: or\n    model: x/y\n    tags: [default, chat]\n    priority: 10\n")
	models, err := modelcfg.Load(os.Getenv(EnvModelsFile))
	if err != nil {
		t.Fatal(err)
	}
	none := PurposesWithoutUsableModel(cfg, models, func(modelcfg.Provider) bool { return false })
	if len(none) != 4 {
		t.Errorf("without credentials: %d purposes listed, want all 4", len(none))
	}
	if some := PurposesWithoutUsableModel(cfg, models, func(modelcfg.Provider) bool { return true }); len(some) != 0 {
		t.Errorf("with credentials: %d purposes listed, want none", len(some))
	}
}

func TestLoadModelsChecksTheEmbeddingSpaceAndItsDimension(t *testing.T) {
	cfg := defaultConfig(t)
	purposes := model("m", "default, chat", 8192)

	writeModels(t, oneProvider+"models:\n"+purposes)
	if _, err := LoadModels(cfg); err == nil || !strings.Contains(err.Error(), `embeddings: no model has embedding_space "bge-m3"`) {
		t.Errorf("no embedding model: err = %v, want the space named", err)
	}

	writeModels(t, oneProvider+"models:\n"+purposes+embedModel("wrong", 768))
	if _, err := LoadModels(cfg); err == nil || !strings.Contains(err.Error(), "embeddings, model wrong: dimension is 768, but embeddings.dimension is 1024") {
		t.Errorf("wrong dimension: err = %v, want both dimensions named", err)
	}

	// Two copies of the same model in the space are fine; one with another dimension is not.
	writeModels(t, oneProvider+"models:\n"+purposes+embedModel("a", 1024)+embedModel("b", 1024))
	if _, err := LoadModels(cfg); err != nil {
		t.Errorf("two copies: err = %v, want nil", err)
	}
	writeModels(t, oneProvider+"models:\n"+purposes+embedModel("a", 1024)+embedModel("b", 512))
	if _, err := LoadModels(cfg); err == nil || !strings.Contains(err.Error(), "model b") || strings.Contains(err.Error(), "model a:") {
		t.Errorf("one bad copy: err = %v, want only model b named", err)
	}
}

func TestLoadModelsDoesNotCheckEmbeddingsWhenTheyAreFake(t *testing.T) {
	writeConfig(t, "embeddings:\n  fake: true\n")
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	writeModels(t, oneProvider+"models:\n"+model("m", "default, chat", 8192))
	if _, err := LoadModels(cfg); err != nil {
		t.Errorf("err = %v, want nil: fake embeddings need no embedding model", err)
	}
}
