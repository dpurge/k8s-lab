package config

import (
	"bytes"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"k8s-lab/shared/modelcfg"
)

// writeModels writes a models file and points PHRASEFORGE_MODELS_FILE at it.
func writeModels(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv(EnvModelsFile, path)
	return path
}

func defaultConfig(t *testing.T) Config {
	t.Helper()
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "absent.yaml"))
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	return cfg
}

const ollamaProvider = "providers:\n  - id: local\n    type: ollama\n    base_url: http://localhost:11434\n    api_key_from: \"\"\n"

func ollamaModel(id, tags string, numCtx int) string {
	m := "  - id: " + id + "\n    provider: local\n    model: m\n    tags: " + tags + "\n    priority: 10\n"
	if numCtx > 0 {
		m += "    options:\n      num_ctx: " + strconv.Itoa(numCtx) + "\n"
	}
	return m
}

func TestLoadModelsMissingFileNamesTheVariableAndTheFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.yaml")
	t.Setenv(EnvModelsFile, path)
	_, err := LoadModels(defaultConfig(t))
	if err == nil {
		t.Fatal("LoadModels succeeded without a file")
	}
	for _, want := range []string{EnvModelsFile, path} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestLoadModelsUsesTheDefaultPathWhenTheVariableIsUnset(t *testing.T) {
	cfg := defaultConfig(t)
	t.Setenv(EnvModelsFile, "")
	_, err := LoadModels(cfg)
	if err == nil || !strings.Contains(err.Error(), DefaultModelsFile) {
		t.Errorf("err = %v, want it to name %s", err, DefaultModelsFile)
	}
}

func TestLoadModelsInvalidFileNamesTheVariableAndTheFileAndKeepsTheLintList(t *testing.T) {
	path := writeModels(t, ollamaProvider+"models:\n  - id: a\n    provider: ghost\n    model: m\n    tags: [default]\n    priority: 1\n")
	_, err := LoadModels(defaultConfig(t))
	if err == nil {
		t.Fatal("LoadModels accepted a model on an unlisted provider")
	}
	for _, want := range []string{EnvModelsFile, path, `provider "ghost"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoadModelsAcceptsAModelThatServesEveryPurpose(t *testing.T) {
	writeModels(t, ollamaProvider+"models:\n"+ollamaModel("big", "[default]", 16384))
	models, err := LoadModels(defaultConfig(t))
	if err != nil {
		t.Fatal(err)
	}
	if len(models.Models) != 1 {
		t.Errorf("models = %d", len(models.Models))
	}
}

// captureWarnings routes slog's default logger to a buffer for the test.
func captureWarnings(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	previous := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(buf, nil)))
	t.Cleanup(func() { slog.SetDefault(previous) })
	return buf
}

const anthropicProvider = "providers:\n  - id: claude\n    type: anthropic\n    api_key_from: env:PF_TEST_UNSET_KEY\n  - id: local\n    type: ollama\n    base_url: http://localhost:11434\n    api_key_from: \"\"\n"

func TestLoadModelsStartsWithAMissingKeyAndNeverEchoesAValue(t *testing.T) {
	writeModels(t, "providers:\n  - id: local\n    type: ollama\n    base_url: http://localhost:11434\n    api_key_from: env:PF_TEST_UNSET_KEY\nmodels:\n"+ollamaModel("big", "[default]", 16384))
	cfg := defaultConfig(t)
	t.Setenv("PF_TEST_UNSET_KEY", "")
	warnings := captureWarnings(t)
	if _, err := LoadModels(cfg); err != nil {
		t.Fatalf("a missing key must not stop startup: %v", err)
	}
	if warnings.Len() != 0 {
		t.Errorf("an ollama provider needs no key, so nothing to warn about: %s", warnings)
	}
}

func TestAPurposeWhoseModelsAreAllOnProvidersWithoutACredentialWarnsAtStartup(t *testing.T) {
	writeModels(t, anthropicProvider+"models:\n  - {id: opus, provider: claude, model: x, tags: [default], priority: 10}\n")
	cfg := defaultConfig(t)
	t.Setenv("PF_TEST_UNSET_KEY", "")
	warnings := captureWarnings(t)
	if _, err := LoadModels(cfg); err != nil {
		t.Fatalf("a missing credential must not stop startup: %v", err)
	}
	for _, want := range []string{"purpose has no usable model", "purpose=reviewVocabulary", "purpose=title", "default"} {
		if !strings.Contains(warnings.String(), want) {
			t.Errorf("warnings do not contain %q:\n%s", want, warnings)
		}
	}
	if strings.Contains(warnings.String(), "PF_TEST_UNSET_KEY") {
		t.Errorf("a startup warning names the purpose and tags, not the credential: %s", warnings)
	}

	// With the credential present, or a usable model among the matches, no warning.
	t.Setenv("PF_TEST_UNSET_KEY", "a-test-value")
	warnings.Reset()
	if _, err := LoadModels(cfg); err != nil || warnings.Len() != 0 {
		t.Errorf("with the credential: err=%v warnings=%s", err, warnings)
	}
	t.Setenv("PF_TEST_UNSET_KEY", "")
	writeModels(t, anthropicProvider+"models:\n  - {id: opus, provider: claude, model: x, tags: [default], priority: 10}\n  - {id: small, provider: local, model: m, tags: [default], priority: 1, options: {num_ctx: 16384}}\n")
	warnings.Reset()
	if _, err := LoadModels(cfg); err != nil || warnings.Len() != 0 {
		t.Errorf("with a usable model: err=%v warnings=%s", err, warnings)
	}
}

func TestAPurposeWhoseTagsMatchNoModelStaysFatalEvenWithoutACredential(t *testing.T) {
	writeModels(t, anthropicProvider+"models:\n  - {id: opus, provider: claude, model: x, tags: [default], priority: 10}\n")
	cfg := defaultConfig(t)
	cfg.Title.ModelTags = []string{"typo"}
	t.Setenv("PF_TEST_UNSET_KEY", "")
	_, err := LoadModels(cfg)
	if err == nil || !strings.Contains(err.Error(), "purpose title") || !strings.Contains(err.Error(), "typo") {
		t.Errorf("err = %v", err)
	}
}

func TestPurposesWithoutUsableModelIgnoresPurposesThatMatchNothing(t *testing.T) {
	writeModels(t, anthropicProvider+"models:\n  - {id: opus, provider: claude, model: x, tags: [default], priority: 10}\n")
	cfg := defaultConfig(t)
	cfg.Title.ModelTags = []string{"typo"}
	models, err := modelcfg.Load(os.Getenv(EnvModelsFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, np := range PurposesWithoutUsableModel(cfg, models, func(modelcfg.Provider) bool { return false }) {
		if np.Name == "title" {
			t.Errorf("a purpose matching no model is a fatal error, not a warning: %v", np.Name)
		}
	}
}

func TestValidatePurposesFindsATagThatMatchesNoModel(t *testing.T) {
	writeModels(t, ollamaProvider+"models:\n"+ollamaModel("big", "[default]", 16384))
	cfg := defaultConfig(t)
	cfg.ReviewVocabulary.ModelTags = []string{"default", "reviewer"}
	_, err := LoadModels(cfg)
	if err == nil {
		t.Fatal("LoadModels accepted a purpose no model can serve")
	}
	for _, want := range []string{"purpose reviewVocabulary", "default, reviewer"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestAModelMatchesOnlyWhenItHasAllTheTags(t *testing.T) {
	writeModels(t, ollamaProvider+"models:\n"+ollamaModel("big", "[default]", 16384))
	cfg := defaultConfig(t)
	cfg.Title.ModelTags = []string{"default", "fast"}
	if _, err := LoadModels(cfg); err == nil || !strings.Contains(err.Error(), "purpose title") {
		t.Errorf("err = %v, want title unservable: the only model lacks the fast tag", err)
	}
}

func TestValidatePurposesFindsAnOllamaModelWithTooSmallOrNoNumCtx(t *testing.T) {
	cfg := defaultConfig(t) // reviewTranscription needs 16384, the rest at most 8192
	for name, tc := range map[string]struct {
		numCtx int
		want   []string
	}{
		"too small": {8192, []string{"purpose reviewTranscription, model small", "options.num_ctx is 8192", "set options.num_ctx to at least 16384"}},
		"zero":      {0, []string{"purpose reviewTranscription, model small", "not set", "set options.num_ctx to at least 16384"}},
	} {
		writeModels(t, ollamaProvider+"models:\n"+ollamaModel("small", "[default]", tc.numCtx))
		_, err := LoadModels(cfg)
		if err == nil {
			t.Fatalf("%s: LoadModels accepted it", name)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%s: error %q does not contain %q", name, err, want)
			}
		}
	}
}

func TestValidatePurposesSkipsTheNumCtxCheckForOtherProviderTypes(t *testing.T) {
	writeModels(t, "providers:\n  - id: cloud\n    type: openai\n    base_url: https://api.example.com/v1\n    api_key_from: \"\"\nmodels:\n  - id: gpt\n    provider: cloud\n    model: m\n    tags: [default]\n    priority: 1\n")
	if _, err := LoadModels(defaultConfig(t)); err != nil {
		t.Errorf("an openai model without num_ctx: %v", err)
	}
}

func TestValidatePurposesReportsAllFindingsTogether(t *testing.T) {
	writeModels(t, ollamaProvider+"models:\n"+ollamaModel("small", "[default]", 8192)+ollamaModel("zero", "[default, extra]", 0))
	cfg := defaultConfig(t)
	cfg.Title.ModelTags = []string{"nobody"}
	_, err := LoadModels(cfg)
	if err == nil {
		t.Fatal("LoadModels succeeded")
	}
	for _, want := range []string{
		"purpose title: no model has all of its model_tags [nobody]",
		"purpose reviewTranscription, model small",
		"purpose reviewTranslation, model small",
		"purpose reviewTranslation, model zero",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestValidatePurposesWithAMatchingModelPerPurposeIsClean(t *testing.T) {
	writeModels(t, ollamaProvider+"models:\n"+ollamaModel("a", "[default]", 16384)+ollamaModel("b", "[default, reviewer]", 32768))
	cfg := defaultConfig(t)
	cfg.ReviewVocabulary.ModelTags = []string{"reviewer"}
	if _, err := LoadModels(cfg); err != nil {
		t.Error(err)
	}
}

func TestModelsExampleLoadsAndServesTheDefaultPurposes(t *testing.T) {
	cfg := defaultConfig(t)
	t.Setenv(EnvModelsFile, "../../models.example.yaml")
	if _, err := LoadModels(cfg); err != nil {
		t.Fatal(err)
	}
}

func TestConfigMapModelsYamlLoadsAndServesTheConfigMapPurposes(t *testing.T) {
	raw, err := os.ReadFile("../../k8s/configmap.yaml")
	if err != nil {
		t.Fatal(err)
	}
	var cm struct {
		Data map[string]string `yaml:"data"`
	}
	if err := yaml.Unmarshal(raw, &cm); err != nil {
		t.Fatal(err)
	}
	writeModels(t, cm.Data["models.yaml"])
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(cm.Data["config.yaml"]), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := LoadModels(cfg); err != nil {
		t.Fatal(err)
	}
}
