package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func writeConfig(t *testing.T, content string) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
}

func tagsOf(p PurposeConfig) string { return strings.Join(p.ModelTags, ",") }

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  any
		want any
	}{
		{"PGHost", cfg.PGHost, "localhost"},
		{"PGPort", cfg.PGPort, "5432"},
		{"PGDatabase", cfg.PGDatabase, "knowledge"},
		{"QdrantURL", cfg.QdrantURL, "http://localhost:6333"},
		{"Embeddings", cfg.Embeddings, EmbeddingsConfig{Space: "bge-m3", Dimension: 1024}},
		{"Chat.ModelTags", tagsOf(cfg.Chat), "chat"},
		{"Chat.NumCtx", cfg.Chat.NumCtx, 8192},
		{"GenerateTitle.ModelTags", tagsOf(cfg.GenerateTitle), "default"},
		{"GenerateSummary.ModelTags", tagsOf(cfg.GenerateSummary), "default"},
		{"Translate.ModelTags", tagsOf(cfg.Translate), "default"},
		{"KnowledgeLanguage", cfg.KnowledgeLanguage, "English"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %v, want %v (default)", c.name, c.got, c.want)
		}
	}
	for _, np := range cfg.Purposes() {
		if np.Purpose.TimeoutSeconds != 1800 || np.Purpose.Prompt == "" {
			t.Errorf("%s = %+v, want timeout 1800 and a non-empty prompt", np.Name, np.Purpose)
		}
	}
	if !strings.Contains(cfg.Translate.Prompt, "{{language}}") {
		t.Errorf("Translate.Prompt = %q, want the {{language}} placeholder", cfg.Translate.Prompt)
	}
}

func TestLoadOverridesFromFile(t *testing.T) {
	writeConfig(t, ""+
		"bind_addr: 127.0.0.1:9000\n"+
		"qdrant:\n"+
		"  search_min_score: 0.55\n"+
		"embeddings:\n"+
		"  embedding_space: bge-m3-large\n"+
		"  dimension: 2048\n"+
		"chat:\n"+
		"  model_tags: [chat, big]\n"+
		"  num_ctx: 16384\n"+
		"generate_summary:\n"+
		"  model_tags: [cheap]\n"+
		"  timeout_seconds: 60\n"+
		"  prompt: custom summary prompt\n"+
		"knowledge_language: Polish\n")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BindAddr != "127.0.0.1:9000" || cfg.SearchMinScore != 0.55 || cfg.KnowledgeLanguage != "Polish" {
		t.Errorf("bind %q, min score %v, language %q; want the file's values", cfg.BindAddr, cfg.SearchMinScore, cfg.KnowledgeLanguage)
	}
	if tagsOf(cfg.Chat) != "chat,big" || cfg.Chat.NumCtx != 16384 || cfg.Chat.Prompt == "" {
		t.Errorf("Chat = %+v, want the file's tags and num_ctx and the default prompt", cfg.Chat)
	}
	if got := cfg.GenerateSummary; tagsOf(got) != "cheap" || got.TimeoutSeconds != 60 || got.Prompt != "custom summary prompt" {
		t.Errorf("GenerateSummary = %+v, want the file's tags, timeout and prompt", got)
	}
	if tagsOf(cfg.GenerateTitle) != "default" {
		t.Errorf("GenerateTitle tags = %q, want default (unset in file)", tagsOf(cfg.GenerateTitle))
	}
	if cfg.Embeddings.Space != "bge-m3-large" || cfg.Embeddings.Dimension != 2048 || cfg.Embeddings.Fake {
		t.Errorf("Embeddings = %+v, want the file's space and dimension and not fake", cfg.Embeddings)
	}
}

func TestLoadRejectsRenamedAndRemovedKeysNamingTheReplacement(t *testing.T) {
	for _, c := range []struct{ name, yaml, want string }{
		{"bindAddr", "bindAddr: x:1\n", "bindAddr is no longer supported; use bind_addr"},
		{"searchMinScore", "qdrant:\n  searchMinScore: 0.5\n", "qdrant.searchMinScore is no longer supported; use qdrant.search_min_score"},
		{"knowledgeLanguage", "knowledgeLanguage: x\n", "knowledgeLanguage is no longer supported; use knowledge_language"},
		{"generateTitle", "generateTitle:\n  prompt: x\n", "generateTitle is no longer supported; use generate_title"},
		{"generateSummary", "generateSummary:\n  prompt: x\n", "generateSummary is no longer supported; use generate_summary"},
		{"embeddings.provider", "embeddings:\n  provider: ollama\n", "embeddings.provider is no longer supported; use embeddings.embedding_space"},
		{"embeddings.model", "embeddings:\n  model: bge-m3\n", "embeddings.model is no longer supported; use embeddings.embedding_space and an embedding model in models.yaml"},
		{"embeddings.base_url", "embeddings:\n  base_url: http://x\n", "embeddings.base_url is no longer supported; use the base_url of a provider in models.yaml"},
		{"embeddings.baseURL", "embeddings:\n  baseURL: http://x\n", "embeddings.baseURL is no longer supported; use the base_url of a provider in models.yaml"},
		{"providers block", "providers:\n  ollama:\n    baseURL: http://x\n", "providers is no longer supported; providers, their base_url"},
		{"chat.provider", "chat:\n  provider: ollama\n", "chat.provider is no longer supported; use model_tags"},
		{"chat.model", "chat:\n  model: m\n", "chat.model is no longer supported; use model_tags"},
		{"chat.numCtx", "chat:\n  numCtx: 8192\n", "chat.numCtx is no longer supported; use num_ctx"},
		{"translate.timeoutSeconds", "translate:\n  timeoutSeconds: 5\n", "translate.timeoutSeconds is no longer supported; use timeout_seconds"},
		{"chat.think", "chat:\n  think: true\n", "chat.think is no longer supported; use the think option of a model in models.yaml"},
		{"chat.baseURL", "chat:\n  baseURL: http://x\n", "chat.baseURL is no longer supported; use the base_url of a provider in models.yaml"},
		{"generate", "generate:\n  model: m\n", "generate is no longer supported; use generate_title and generate_summary"},
		{"prompts", "prompts:\n  chat: x\n", "prompts is no longer supported"},
	} {
		t.Run(c.name, func(t *testing.T) {
			writeConfig(t, c.yaml)
			_, err := Load()
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("Load() error = %v, want it to contain %q", err, c.want)
			}
			if err != nil && !strings.Contains(err.Error(), "config file") {
				t.Errorf("Load() error = %v, want it to name the config file", err)
			}
		})
	}
}

func TestLoadNamesTheNewSectionKeyForOldSectionsAndReportsEverythingAtOnce(t *testing.T) {
	writeConfig(t, "generateTitle:\n  provider: ollama\n  numCtx: 4096\nbindAddr: x:1\n")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want errors")
	}
	for _, want := range []string{
		"generateTitle is no longer supported; use generate_title",
		"generate_title.provider is no longer supported",
		"generate_title.numCtx is no longer supported; use num_ctx",
		"bindAddr is no longer supported",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestLoadRejectsUnknownKeysNamingFileAndKey(t *testing.T) {
	for _, yamlText := range []string{"typo_key: 1\n", "chat:\n  model_tag: [x]\n", "qdrant:\n  colection: x\n", "PGHost: elsewhere\n"} {
		writeConfig(t, yamlText)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "config file") {
			t.Errorf("Load(%q) error = %v, want an unknown-key error naming the file", yamlText, err)
		}
	}
}

func TestLoadRejectsBadValuesAllAtOnce(t *testing.T) {
	writeConfig(t, ""+
		"chat:\n  model_tags: []\n"+
		"generate_title:\n  model_tags: [a, a]\n"+
		"generate_summary:\n  model_tags: [\" \"]\n"+
		"translate:\n  model_tags: [default]\n  num_ctx: -1\n  timeout_seconds: -5\n")
	_, err := Load()
	if err == nil {
		t.Fatal("Load() error = nil, want errors")
	}
	for _, want := range []string{
		"chat.model_tags must list at least one tag",
		`generate_title.model_tags lists "a" twice`,
		"generate_summary.model_tags has a blank tag",
		"translate.num_ctx must not be negative",
		"translate.timeout_seconds must not be negative",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error missing %q:\n%v", want, err)
		}
	}
}

func TestLoadRejectsRemovedEnvVars(t *testing.T) {
	writeConfig(t, "")
	t.Setenv("CHAT_API_KEY", "x")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), "CHAT_API_KEY is no longer supported") {
		t.Errorf("Load() error = %v, want it to reject CHAT_API_KEY", err)
	}
}

func TestLoadRejectsABadEmbeddingSpaceAndDimensionAndAcceptsFake(t *testing.T) {
	writeConfig(t, "embeddings:\n  embedding_space: BGE_M3\n  dimension: 0\n")
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), `embeddings.embedding_space "BGE_M3"`) || !strings.Contains(err.Error(), "embeddings.dimension must be at least 1") {
		t.Errorf("Load() error = %v, want both the space and the dimension named", err)
	}
	writeConfig(t, "embeddings:\n  fake: true\n")
	cfg, err := Load()
	if err != nil || !cfg.Embeddings.Fake {
		t.Errorf("Load() with fake embeddings = %+v, %v; want fake accepted", cfg.Embeddings, err)
	}
}

func TestLoadErrorsOnMalformedFile(t *testing.T) {
	writeConfig(t, "chat: [this is not valid: yaml structure")
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed YAML")
	}
}

func TestLoadTakesCredentialsAndConnectionFieldsFromEnvOnly(t *testing.T) {
	writeConfig(t, "chat:\n  model_tags: [custom]\n")
	t.Setenv("PGUSER", "from-env")
	t.Setenv("PGHOST", "from-env-host")
	t.Setenv("QDRANT_URL", "http://from-env:6333")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if tagsOf(cfg.Chat) != "custom" {
		t.Errorf("Chat tags = %q, want custom (file-sourced)", tagsOf(cfg.Chat))
	}
	if cfg.PGUser != "from-env" || cfg.PGHost != "from-env-host" || cfg.QdrantURL != "http://from-env:6333" {
		t.Errorf("PGUser %q, PGHost %q, QdrantURL %q; want the environment's values", cfg.PGUser, cfg.PGHost, cfg.QdrantURL)
	}
}

// TestConfigMapLoadsAndMatchesDefaults: the lab ConfigMap must load under the
// current shape (no removed keys) and keep the compiled-in defaults' tags,
// window, timeouts and prompts (trimmed: a "|" block adds a trailing newline,
// which chat.go trims too).
func TestConfigMapLoadsAndMatchesDefaults(t *testing.T) {
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
	if _, ok := cm.Data["models.yaml"]; ok {
		t.Error("the knowledge-config ConfigMap has models.yaml; it lives in models.dev.yaml (the knowledge-models ConfigMap)")
	}
	writeConfig(t, cm.Data["config.yaml"])
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load(configmap) error = %v", err)
	}
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))
	def, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	got, want := cfg.Purposes(), def.Purposes()
	for i := range got {
		if strings.TrimSpace(got[i].Purpose.Prompt) != strings.TrimSpace(want[i].Purpose.Prompt) {
			t.Errorf("%s prompt differs from default:\n%q\nvs\n%q", got[i].Name, got[i].Purpose.Prompt, want[i].Purpose.Prompt)
		}
		if tagsOf(got[i].Purpose) != tagsOf(want[i].Purpose) || got[i].Purpose.NumCtx != want[i].Purpose.NumCtx || got[i].Purpose.TimeoutSeconds != want[i].Purpose.TimeoutSeconds {
			t.Errorf("%s = %+v, want the tags, num_ctx and timeout of default %+v", got[i].Name, got[i].Purpose, want[i].Purpose)
		}
	}
	if cfg.Embeddings != def.Embeddings {
		t.Errorf("Embeddings = %+v, want the default %+v", cfg.Embeddings, def.Embeddings)
	}
}

func TestLLMCallsRetentionDefaultsOverridesAndLimits(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))
	def, err := Load()
	if err != nil || def.LLMCallsRetentionDays != 30 {
		t.Fatalf("default = %d, %v; want 30 days", def.LLMCallsRetentionDays, err)
	}
	writeConfig(t, "llm_calls:\n  retention_days: 7\n")
	if cfg, err := Load(); err != nil || cfg.LLMCallsRetentionDays != 7 {
		t.Errorf("override = %d, %v; want 7", cfg.LLMCallsRetentionDays, err)
	}
	writeConfig(t, "llm_calls:\n  retention_days: 0\n")
	if cfg, err := Load(); err != nil || cfg.LLMCallsRetentionDays != 0 {
		t.Errorf("0 = %d, %v; want 0 (keep forever)", cfg.LLMCallsRetentionDays, err)
	}
	for _, days := range []string{"-1", "36501"} {
		writeConfig(t, "llm_calls:\n  retention_days: "+days+"\n")
		if _, err := Load(); err == nil || !strings.Contains(err.Error(), "llm_calls.retention_days must be a whole number of days from 0") {
			t.Errorf("%s: err = %v, want the limits named", days, err)
		}
	}
	writeConfig(t, "llmCalls:\n  retentionDays: 5\n")
	if _, err := Load(); err == nil {
		t.Error("an unknown camelCase key must be rejected")
	}
}

func TestNATSURLDefaultsToDisabledAndTakesTheFileThenTheEnvironment(t *testing.T) {
	t.Setenv("KNOWLEDGE_NATS_URL", "")
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "missing.yaml"))
	if cfg, err := Load(); err != nil || cfg.NATSURL != "" {
		t.Fatalf("default = %q, %v; want empty (the bus is off)", cfg.NATSURL, err)
	}
	writeConfig(t, "nats:\n  url: nats://from-file:4222\n")
	if cfg, err := Load(); err != nil || cfg.NATSURL != "nats://from-file:4222" {
		t.Errorf("file = %q, %v; want nats://from-file:4222", cfg.NATSURL, err)
	}
	t.Setenv("KNOWLEDGE_NATS_URL", "nats://from-env:4222")
	if cfg, err := Load(); err != nil || cfg.NATSURL != "nats://from-env:4222" {
		t.Errorf("env over file = %q, %v; want nats://from-env:4222", cfg.NATSURL, err)
	}
	writeConfig(t, "nats:\n  uRL: nats://x:4222\n")
	if _, err := Load(); err == nil {
		t.Error("an unknown key under nats must be rejected")
	}
}

func TestConfigMapSetsTheDevNATSURL(t *testing.T) {
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
	t.Setenv("KNOWLEDGE_NATS_URL", "")
	writeConfig(t, cm.Data["config.yaml"])
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	if cfg.NATSURL != "nats://jdp-data-nats.data.svc:4222" {
		t.Errorf("NATSURL = %q, want the dev NATS service under its production name", cfg.NATSURL)
	}
}
