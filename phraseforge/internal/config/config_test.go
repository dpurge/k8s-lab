package config

import (
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

func TestLoadDefaultsWhenFileMissing(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"BindAddr", cfg.BindAddr, "0.0.0.0:8090"},
		{"PGHost", cfg.PGHost, "localhost"},
		{"PGPort", cfg.PGPort, "5432"},
		{"PGDatabase", cfg.PGDatabase, "phraseforge"},
		{"SessionKey", cfg.SessionKey, "dev-only-insecure-key-change-me"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (default)", c.name, c.got, c.want)
		}
	}
	for _, np := range cfg.Purposes() {
		if !reflect.DeepEqual(np.Purpose.ModelTags, []string{"default"}) {
			t.Errorf("%s.ModelTags = %v, want [default]", np.Name, np.Purpose.ModelTags)
		}
	}
	// Every purpose states its context window: 4096 for the four that used to leave
	// it unset (the window Ollama reports on both servers), 8192 or 16384 elsewhere.
	numCtxCases := []struct {
		name string
		got  int
		want int
	}{
		{"Transcription.NumCtx", cfg.Transcription.NumCtx, 4096},
		{"Translation.NumCtx", cfg.Translation.NumCtx, 4096},
		{"VocabularyItem.NumCtx", cfg.VocabularyItem.NumCtx, 4096},
		{"ModelsItem.NumCtx", cfg.ModelsItem.NumCtx, 4096},
		{"Title.NumCtx", cfg.Title.NumCtx, 8192},
		{"ProcessText.NumCtx", cfg.ProcessText.NumCtx, 8192},
		{"ProcessDialog.NumCtx", cfg.ProcessDialog.NumCtx, 8192},
		{"ReviewVocabulary.NumCtx", cfg.ReviewVocabulary.NumCtx, 8192},
		{"ReviewModels.NumCtx", cfg.ReviewModels.NumCtx, 8192},
		{"ReviewTranscription.NumCtx", cfg.ReviewTranscription.NumCtx, 16384},
		{"ReviewTranslation.NumCtx", cfg.ReviewTranslation.NumCtx, 16384},
	}
	for _, c := range numCtxCases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (default)", c.name, c.got, c.want)
		}
	}
	// TimeoutSeconds: 1800s overall backstop for every purpose; streamed
	// calls are bounded first by the provider's first-token/idle limits
	// (see llm-streaming-progress-timeout).
	timeoutCases := []struct {
		name string
		got  int
		want int
	}{
		{"Transcription.TimeoutSeconds", cfg.Transcription.TimeoutSeconds, 1800},
		{"Translation.TimeoutSeconds", cfg.Translation.TimeoutSeconds, 1800},
		{"Title.TimeoutSeconds", cfg.Title.TimeoutSeconds, 1800},
		{"ProcessText.TimeoutSeconds", cfg.ProcessText.TimeoutSeconds, 1800},
		{"ProcessDialog.TimeoutSeconds", cfg.ProcessDialog.TimeoutSeconds, 1800},
		{"GenerateVocabulary.TimeoutSeconds", cfg.GenerateVocabulary.TimeoutSeconds, 1800},
		{"GenerateModels.TimeoutSeconds", cfg.GenerateModels.TimeoutSeconds, 1800},
		{"VocabularyItem.TimeoutSeconds", cfg.VocabularyItem.TimeoutSeconds, 1800},
		{"ModelsItem.TimeoutSeconds", cfg.ModelsItem.TimeoutSeconds, 1800},
	}
	for _, c := range timeoutCases {
		if c.got != c.want {
			t.Errorf("%s = %d, want %d (default)", c.name, c.got, c.want)
		}
	}
	if cfg.LLMCallsRetentionDays != 30 {
		t.Errorf("LLMCallsRetentionDays = %d, want 30 (default)", cfg.LLMCallsRetentionDays)
	}
	if cfg.JobsRetentionDaysDone != 7 || cfg.JobsRetentionDaysFailed != 30 {
		t.Errorf("jobs retention = %d/%d, want 7/30 (default)", cfg.JobsRetentionDaysDone, cfg.JobsRetentionDaysFailed)
	}
	if cfg.IngestMaxContentBytes != 24576 {
		t.Errorf("IngestMaxContentBytes = %d, want 24576 (default)", cfg.IngestMaxContentBytes)
	}
	// MaxAttempts: one shared budget of 3 attempts per LLM call, every purpose
	// (see specs/features/phraseforge-item-correction-retry-budget.md).
	maxAttemptsCases := []struct {
		name string
		got  int
	}{
		{"Transcription.MaxAttempts", cfg.Transcription.MaxAttempts},
		{"Translation.MaxAttempts", cfg.Translation.MaxAttempts},
		{"Title.MaxAttempts", cfg.Title.MaxAttempts},
		{"ProcessText.MaxAttempts", cfg.ProcessText.MaxAttempts},
		{"ProcessDialog.MaxAttempts", cfg.ProcessDialog.MaxAttempts},
		{"GenerateVocabulary.MaxAttempts", cfg.GenerateVocabulary.MaxAttempts},
		{"GenerateModels.MaxAttempts", cfg.GenerateModels.MaxAttempts},
		{"VocabularyItem.MaxAttempts", cfg.VocabularyItem.MaxAttempts},
		{"ModelsItem.MaxAttempts", cfg.ModelsItem.MaxAttempts},
	}
	for _, c := range maxAttemptsCases {
		if c.got != 3 {
			t.Errorf("%s = %d, want 3 (default)", c.name, c.got)
		}
	}
	// Prompt must be non-empty for every purpose — a blank default would
	// silently defeat ai.go's fallback-to-config-default behavior.
	promptCases := []struct {
		name string
		got  string
	}{
		{"Transcription.Prompt", cfg.Transcription.Prompt},
		{"Translation.Prompt", cfg.Translation.Prompt},
		{"VocabularyItem.Prompt", cfg.VocabularyItem.Prompt},
		{"ModelsItem.Prompt", cfg.ModelsItem.Prompt},
		{"Title.Prompt", cfg.Title.Prompt},
		{"ProcessText.Prompt", cfg.ProcessText.Prompt},
		{"ProcessDialog.Prompt", cfg.ProcessDialog.Prompt},
		{"GenerateVocabulary.Prompt", cfg.GenerateVocabulary.Prompt},
		{"GenerateModels.Prompt", cfg.GenerateModels.Prompt},
	}
	for _, c := range promptCases {
		if c.got == "" {
			t.Errorf("%s = \"\", want a non-empty default prompt", c.name)
		}
	}
}

func TestLoadOverridesFromFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	yaml := "" +
		"bindAddr: \"0.0.0.0:9090\"\n" +
		"transcription:\n" +
		"  model_tags: [custom, fast]\n" +
		"  timeoutSeconds: 45\n" +
		"  prompt: custom transcription prompt\n" +
		"  maxAttempts: 5\n" +
		"ingest:\n" +
		"  maxContentBytes: 204800\n"
	if err := os.WriteFile(path, []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BindAddr != "0.0.0.0:9090" {
		t.Errorf("BindAddr = %q, want 0.0.0.0:9090 (from file)", cfg.BindAddr)
	}
	if cfg.PGDatabase != "phraseforge" {
		t.Errorf("PGDatabase = %q, want phraseforge (default, no longer file-sourced)", cfg.PGDatabase)
	}
	if !reflect.DeepEqual(cfg.Transcription.ModelTags, []string{"custom", "fast"}) {
		t.Errorf("Transcription.ModelTags = %v, want [custom fast] (from file)", cfg.Transcription.ModelTags)
	}
	if cfg.Transcription.TimeoutSeconds != 45 {
		t.Errorf("Transcription.TimeoutSeconds = %d, want 45 (from file)", cfg.Transcription.TimeoutSeconds)
	}
	if cfg.Transcription.Prompt != "custom transcription prompt" {
		t.Errorf("Transcription.Prompt = %q, want %q (from file)", cfg.Transcription.Prompt, "custom transcription prompt")
	}
	if cfg.IngestMaxContentBytes != 204800 {
		t.Errorf("IngestMaxContentBytes = %d, want 204800 (from file)", cfg.IngestMaxContentBytes)
	}
	if cfg.Transcription.MaxAttempts != 5 {
		t.Errorf("Transcription.MaxAttempts = %d, want 5 (from file)", cfg.Transcription.MaxAttempts)
	}
	// Fields left unset in the file must keep their Go defaults.
	if cfg.Translation.MaxAttempts != 3 {
		t.Errorf("Translation.MaxAttempts = %d, want 3 (default, unset in file)", cfg.Translation.MaxAttempts)
	}
	if !reflect.DeepEqual(cfg.Translation.ModelTags, []string{"default"}) {
		t.Errorf("Translation.ModelTags = %v, want [default] (default, unset in file)", cfg.Translation.ModelTags)
	}
	if cfg.Translation.TimeoutSeconds != 1800 {
		t.Errorf("Translation.TimeoutSeconds = %d, want 1800 (default, unset in file)", cfg.Translation.TimeoutSeconds)
	}
}

func TestLoadEnvOverridesCredentialsAndSessionKey(t *testing.T) {
	// No file at all — env vars must still apply on top of pure defaults.
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	t.Setenv("PGUSER", "env-user")
	t.Setenv("PGPASSWORD", "env-password")
	t.Setenv("SESSION_KEY", "env-session-key")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"PGUser", cfg.PGUser, "env-user"},
		{"PGPassword", cfg.PGPassword, "env-password"},
		{"SessionKey", cfg.SessionKey, "env-session-key"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (from env)", c.name, c.got, c.want)
		}
	}
}

func TestLoadEnvOverridesConnectionFields(t *testing.T) {
	// No file at all — env vars must still apply on top of pure defaults.
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	t.Setenv("PGHOST", "env-host")
	t.Setenv("PGPORT", "9999")
	t.Setenv("PGDATABASE", "env-db")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	cases := []struct {
		name string
		got  string
		want string
	}{
		{"PGHost", cfg.PGHost, "env-host"},
		{"PGPort", cfg.PGPort, "9999"},
		{"PGDatabase", cfg.PGDatabase, "env-db"},
	}
	for _, c := range cases {
		if c.got != c.want {
			t.Errorf("%s = %q, want %q (from env)", c.name, c.got, c.want)
		}
	}
}

func TestLoadEnvOverridesOnTopOfFile(t *testing.T) {
	// Credentials and Postgres connection fields are never read from the
	// mounted file (see fileConfig's doc comment) — env must win even when a
	// file is present and sets unrelated, still-file-sourced fields.
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bindAddr: from-file:1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	t.Setenv("PGUSER", "from-env")
	t.Setenv("PGHOST", "from-env-host")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v, want nil", err)
	}
	if cfg.BindAddr != "from-file:1" {
		t.Errorf("BindAddr = %q, want from-file:1 (still file-sourced)", cfg.BindAddr)
	}
	if cfg.PGUser != "from-env" {
		t.Errorf("PGUser = %q, want from-env (credential, never file-sourced)", cfg.PGUser)
	}
	if cfg.PGHost != "from-env-host" {
		t.Errorf("PGHost = %q, want from-env-host (connection field, never file-sourced)", cfg.PGHost)
	}
}

func TestLoadMissingFileIsNotError(t *testing.T) {
	t.Setenv("CONFIG_FILE", filepath.Join(t.TempDir(), "does-not-exist.yaml"))
	if _, err := Load(); err != nil {
		t.Errorf("Load() error = %v, want nil for a missing config file", err)
	}
}

func TestLoadErrorsOnMalformedFile(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("bindAddr: [this is not valid: yaml structure"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	if _, err := Load(); err == nil {
		t.Fatal("Load() error = nil, want error for malformed YAML")
	}
}

// TestConfigMapItemPromptsMatchDefaults: k8s/configmap.yaml declares every
// purpose explicitly, so its item-prompt templates must stay byte-identical
// to the compiled-in defaults (the ones mirrored from prompt-eval).
func TestConfigMapItemPromptsMatchDefaults(t *testing.T) {
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
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(cm.Data["config.yaml"]), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil {
		t.Fatal(err)
	}
	// translation and title are left out: the configmap keeps them as one-line
	// strings, the defaults as the (line-wrapped) text prompt-eval tests.
	for name, pair := range map[string][2]string{
		"vocabularyItem":      {cfg.VocabularyItem.Prompt, DefaultVocabularyItemPrompt},
		"modelsItem":          {cfg.ModelsItem.Prompt, DefaultModelsItemPrompt},
		"checkVocabulary":     {cfg.CheckVocabulary.Prompt, DefaultCheckVocabularyPrompt},
		"reviewVocabulary":    {cfg.ReviewVocabulary.Prompt, DefaultReviewVocabularyPrompt},
		"reviewModels":        {cfg.ReviewModels.Prompt, DefaultReviewModelsPrompt},
		"reviewTranscription": {cfg.ReviewTranscription.Prompt, DefaultReviewTranscriptionPrompt},
		"reviewTranslation":   {cfg.ReviewTranslation.Prompt, DefaultReviewTranslationPrompt},
		"transcription":       {cfg.Transcription.Prompt, DefaultTranscriptionPrompt},
		"processText":         {cfg.ProcessText.Prompt, DefaultProcessTextPrompt},
		"processDialog":       {cfg.ProcessDialog.Prompt, DefaultProcessDialogPrompt},
		"generateVocabulary":  {cfg.GenerateVocabulary.Prompt, DefaultGenerateVocabularyPrompt},
		"generateModels":      {cfg.GenerateModels.Prompt, DefaultGenerateModelsPrompt},
	} {
		if pair[0] != pair[1] {
			t.Errorf("configmap %s.prompt differs from its compiled-in default (a prompt change must reach both):\n%q\nvs\n%q", name, pair[0], pair[1])
		}
	}
	// Long pages are accepted in prod only through this value, so a typo in the
	// key must not fall back silently to the 24 KiB default.
	if cfg.IngestMaxContentBytes != 204800 {
		t.Errorf("configmap ingest.maxContentBytes = %d, want 204800", cfg.IngestMaxContentBytes)
	}
	if cfg.LLMCallsRetentionDays != 30 {
		t.Errorf("configmap llm_calls.retention_days = %d, want 30", cfg.LLMCallsRetentionDays)
	}
	if cfg.JobsRetentionDaysDone != 7 || cfg.JobsRetentionDaysFailed != 30 {
		t.Errorf("configmap jobs.retention_days = %d/%d, want 7/30", cfg.JobsRetentionDaysDone, cfg.JobsRetentionDaysFailed)
	}
}

func TestLLMCallsRetentionDaysComesFromTheFileAndZeroIsAllowed(t *testing.T) {
	for value, want := range map[string]int{"7": 7, "0": 0} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("llm_calls:\n  retention_days: "+value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONFIG_FILE", path)
		cfg, err := Load()
		if err != nil || cfg.LLMCallsRetentionDays != want {
			t.Errorf("retention_days %s: got %d, %v; want %d", value, cfg.LLMCallsRetentionDays, err, want)
		}
	}
}

func TestLoadRejectsNegativeLLMCallsRetentionDays(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("llm_calls:\n  retention_days: -1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	_, err := Load()
	if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "llm_calls.retention_days") {
		t.Errorf("err = %v, want it to name the file and llm_calls.retention_days", err)
	}
}

func TestJobsRetentionDaysComeFromTheFileAndZeroIsAllowed(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte("jobs:\n  retention_days_done: 0\n  retention_days_failed: 90\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	cfg, err := Load()
	if err != nil || cfg.JobsRetentionDaysDone != 0 || cfg.JobsRetentionDaysFailed != 90 {
		t.Errorf("got %d/%d, %v; want 0/90", cfg.JobsRetentionDaysDone, cfg.JobsRetentionDaysFailed, err)
	}
}

func TestLoadRejectsNegativeJobsRetentionDays(t *testing.T) {
	for _, key := range []string{"retention_days_done", "retention_days_failed"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("jobs:\n  "+key+": -1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONFIG_FILE", path)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), path) || !strings.Contains(err.Error(), "jobs."+key) {
			t.Errorf("%s: err = %v, want it to name the file and jobs.%s", key, err, key)
		}
	}
}

func TestLoadRejectsNonPositiveIngestMaxContentBytes(t *testing.T) {
	for _, value := range []string{"0", "-5"} {
		path := filepath.Join(t.TempDir(), "config.yaml")
		if err := os.WriteFile(path, []byte("ingest:\n  maxContentBytes: "+value+"\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		t.Setenv("CONFIG_FILE", path)
		_, err := Load()
		if err == nil || !strings.Contains(err.Error(), "ingest.maxContentBytes must be greater than 0") {
			t.Errorf("maxContentBytes %s: err = %v, want a 'must be greater than 0' error", value, err)
		}
	}
}

// TestConfigMapStatesEveryPurposeExplicitly: the ConfigMap names every field of
// every purpose, so the deployment does not depend on the image's defaults, and
// each value is the compiled-in default (a prompt change is made in both).
func TestConfigMapStatesEveryPurposeExplicitly(t *testing.T) {
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
	var stated map[string]any
	if err := yaml.Unmarshal([]byte(cm.Data["config.yaml"]), &stated); err != nil {
		t.Fatal(err)
	}
	if _, ok := stated["providers"]; ok {
		t.Error("configmap config.yaml still has a providers section; providers live in models.yaml")
	}
	defaults := defaultFileConfig()
	fields := map[string]func(purposeFileConfig) any{
		"numCtx":         func(c purposeFileConfig) any { return c.NumCtx },
		"timeoutSeconds": func(c purposeFileConfig) any { return c.TimeoutSeconds }, "maxAttempts": func(c purposeFileConfig) any { return c.MaxAttempts },
		"maxReplyTokens": func(c purposeFileConfig) any { return c.MaxReplyTokens },
	}
	for _, p := range defaults.purposes() {
		got, ok := stated[p.name].(map[string]any)
		if !ok {
			t.Errorf("configmap has no %s block", p.name)
			continue
		}
		if _, ok := got["prompt"]; !ok {
			t.Errorf("configmap %s states no prompt", p.name)
		}
		if tags, _ := got["model_tags"].([]any); len(tags) != 1 || tags[0] != "default" {
			t.Errorf("configmap %s.model_tags = %v, want [default]", p.name, got["model_tags"])
		}
		for _, legacy := range []string{"provider", "model", "think"} {
			if _, ok := got[legacy]; ok {
				t.Errorf("configmap %s still has the legacy key %s", p.name, legacy)
			}
		}
		for field, of := range fields {
			value, ok := got[field]
			if !ok {
				t.Errorf("configmap %s does not state %s (it would rely on the image default)", p.name, field)
				continue
			}
			if value != of(p.config) {
				t.Errorf("configmap %s.%s = %v, the compiled-in default is %v", p.name, field, value, of(p.config))
			}
		}
	}
}

// TestPurposesListsEveryPurposeOfTheFile: purposes() is what Load validates, so
// a purpose added to the file configuration must be added to it too.
func TestPurposesListsEveryPurposeOfTheFile(t *testing.T) {
	fileType := reflect.TypeOf(fileConfig{})
	want := map[string]bool{}
	for i := 0; i < fileType.NumField(); i++ {
		if fileType.Field(i).Type == reflect.TypeOf(purposeFileConfig{}) {
			want[strings.Split(fileType.Field(i).Tag.Get("yaml"), ",")[0]] = true
		}
	}
	got := map[string]bool{}
	for _, p := range defaultFileConfig().purposes() {
		got[p.name] = true
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("purposes() lists %v, the file configures %v", got, want)
	}
}

// TestConfigPurposesListsEveryPurposeOfTheConfig: Purposes() is what
// ValidatePurposes checks, so a purpose added to Config must be added to it too.
func TestConfigPurposesListsEveryPurposeOfTheConfig(t *testing.T) {
	configType := reflect.TypeOf(Config{})
	want := map[string]bool{}
	for i := 0; i < configType.NumField(); i++ {
		if configType.Field(i).Type == reflect.TypeOf(PurposeConfig{}) {
			want[configType.Field(i).Name] = true
		}
	}
	if got := len(Config{}.Purposes()); got != len(want) {
		t.Errorf("Purposes() lists %d purposes, Config has %d PurposeConfig fields", got, len(want))
	}
	fromFile := map[string]bool{}
	for _, p := range defaultFileConfig().purposes() {
		fromFile[p.name] = true
	}
	for _, np := range (Config{}).Purposes() {
		if !fromFile[np.Name] {
			t.Errorf("Purposes() names %q, which the config file does not", np.Name)
		}
	}
}

func TestEffectiveNumCtx(t *testing.T) {
	for in, want := range map[int]int{0: 4096, -5: 4096, 2048: 2048, 8192: 8192} {
		if got := EffectiveNumCtx(in); got != want {
			t.Errorf("EffectiveNumCtx(%d) = %d, want %d", in, got, want)
		}
	}
}

// loadFrom loads a config file with the given content.
func loadFrom(t *testing.T, content string) (Config, error) {
	t.Helper()
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CONFIG_FILE", path)
	return Load()
}

func TestLoadRejectsEveryLegacyKeyNamingFilePurposeAndReplacement(t *testing.T) {
	for name, tc := range map[string]struct{ content, wantPurpose, wantReplacement string }{
		"provider":                               {"reviewModels:\n  provider: ollama\n", "reviewModels.provider", "replaced by model_tags; models are configured in models.yaml (PHRASEFORGE_MODELS_FILE)"},
		"model":                                  {"title:\n  model: gemma4:12b\n", "title.model", "replaced by model_tags; models are configured in models.yaml (PHRASEFORGE_MODELS_FILE)"},
		"think":                                  {"translation:\n  think: true\n", "translation.think", "moved to the model's options.think in models.yaml"},
		"providers":                              {"providers:\n  ollama:\n    baseURL: http://x:1\n", "providers", "replaced by the providers in models.yaml; the Ollama key is now api_key_from"},
		"blank provider is still the legacy key": {"reviewModels:\n  provider: \"\"\n", "reviewModels.provider", "replaced by model_tags"},
		"old modelTags":                          {"title:\n  modelTags: [a]\n", "title.modelTags", "renamed to model_tags"},
		"old llmCalls":                           {"llmCalls:\n  retentionDays: 5\n", "llmCalls", "renamed to llm_calls"},
		"old llm_calls.retentionDays":            {"llm_calls:\n  retentionDays: 5\n", "llm_calls.retentionDays", "renamed to llm_calls.retention_days"},
		"old jobs.retentionDaysDone":             {"jobs:\n  retentionDaysDone: 3\n", "jobs.retentionDaysDone", "renamed to jobs.retention_days_done"},
		"old jobs.retentionDaysFailed":           {"jobs:\n  retentionDaysFailed: 3\n", "jobs.retentionDaysFailed", "renamed to jobs.retention_days_failed"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONFIG_FILE", path)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() succeeded, want the legacy key rejected")
			}
			for _, want := range []string{path, tc.wantPurpose, tc.wantReplacement} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestLoadReportsAllLegacyFindingsInOneError(t *testing.T) {
	_, err := loadFrom(t, "providers: {}\ntitle:\n  provider: a\n  model: b\n  think: true\nprocessText:\n  model: c\n")
	if err == nil {
		t.Fatal("Load() succeeded")
	}
	for _, want := range []string{"5 problem(s)", "providers:", "title.provider", "title.model", "title.think", "processText.model"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestLoadRejectsBlankDuplicateAndEmptyModelTags(t *testing.T) {
	for name, tc := range map[string]struct{ content, want string }{
		"blank":     {"title:\n  model_tags: [a, \" \"]\n", "title.model_tags[1] is blank"},
		"duplicate": {"title:\n  model_tags: [a, b, a]\n", `title.model_tags lists "a" more than once`},
		"empty":     {"title:\n  model_tags: []\n", "title.model_tags is empty"},
	} {
		if _, err := loadFrom(t, tc.content); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want it to contain %q", name, err, tc.want)
		}
	}
}

func TestLoadAcceptsModelTagsAndKeepsDefaultsForOtherPurposes(t *testing.T) {
	cfg, err := loadFrom(t, "title:\n  model_tags: [strong]\n")
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(cfg.Title.ModelTags, []string{"strong"}) || !reflect.DeepEqual(cfg.Translation.ModelTags, []string{"default"}) {
		t.Errorf("Title.ModelTags = %v, Translation.ModelTags = %v", cfg.Title.ModelTags, cfg.Translation.ModelTags)
	}
}

func TestRetentionDaysHaveAnUpperBoundOf36500(t *testing.T) {
	for _, key := range []string{"llm_calls.retention_days", "jobs.retention_days_done", "jobs.retention_days_failed"} {
		parent, field, _ := strings.Cut(key, ".")
		content := func(days string) string { return parent + ":\n  " + field + ": " + days + "\n" }
		if _, err := loadFrom(t, content("36500")); err != nil {
			t.Errorf("%s: 36500 must be accepted: %v", key, err)
		}
		for _, days := range []string{"36501", "9999999999"} {
			_, err := loadFrom(t, content(days))
			if err == nil || !strings.Contains(err.Error(), key+" must be from 0 (keep forever) to 36500") || !strings.Contains(err.Error(), days) {
				t.Errorf("%s: %s: err = %v, want a finding naming the key and the bound", key, days, err)
			}
		}
	}
}

func TestLoadRejectsAnUnknownKeyNamingFileAndFullKey(t *testing.T) {
	for name, tc := range map[string]struct{ content, wantKey string }{
		"misspelt purpose key": {"title:\n  num_ctx: 8192\n", "title.num_ctx"},
		"unknown top level":    {"bindAdress: 0.0.0.0:1\n", "bindAdress"},
		"nested under jobs":    {"jobs:\n  retention_days_don: 3\n", "jobs.retention_days_don"},
		"nested under ingest":  {"ingest:\n  maxContentByte: 3\n", "ingest.maxContentByte"},
		"later purpose":        {"title:\n  numCtx: 8192\nreviewModels:\n  timeout_seconds: 5\n", "reviewModels.timeout_seconds"},
	} {
		t.Run(name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := os.WriteFile(path, []byte(tc.content), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Setenv("CONFIG_FILE", path)
			_, err := Load()
			if err == nil {
				t.Fatal("Load() succeeded, want the unknown key rejected")
			}
			for _, want := range []string{path, tc.wantKey + ": unknown key (line ", "check its spelling", "1 problem(s)"} {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q does not contain %q", err, want)
				}
			}
		})
	}
}

func TestEveryLegacyKeyGetsItsSpecificMessageAndNoUnknownKeyMessage(t *testing.T) {
	content := `providers: {}
llmCalls: {retentionDays: 5}
llm_calls: {retentionDays: 5}
jobs: {retentionDaysDone: 3, retentionDaysFailed: 3}
`
	for _, p := range (fileConfig{}).purposes() {
		content += p.name + ":\n  model_tags: [a]\n  modelTags: [a]\n  provider: a\n  model: b\n  think: true\n"
	}
	_, err := loadFrom(t, content)
	if err == nil {
		t.Fatal("Load() succeeded")
	}
	if strings.Contains(err.Error(), "unknown key") {
		t.Errorf("a legacy key must get its own message, not the generic one:\n%v", err)
	}
	want := 5 + 4*len((fileConfig{}).purposes())
	if !strings.Contains(err.Error(), fmt.Sprintf("%d problem(s)", want)) {
		t.Errorf("want %d legacy findings in:\n%v", want, err)
	}
}

func TestAnUnknownKeyAndALegacyKeyAreBothReportedInOneError(t *testing.T) {
	_, err := loadFrom(t, "title:\n  model: a\n  num_ctx: 5\n")
	if err == nil {
		t.Fatal("Load() succeeded")
	}
	for _, want := range []string{"2 problem(s)", "title.model: replaced by model_tags", "title.num_ctx: unknown key"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not contain %q", err, want)
		}
	}
}

func TestAnEmptyOrCommentOnlyConfigFileStillMeansDefaults(t *testing.T) {
	for name, content := range map[string]string{"empty": "", "comments only": "# nothing here\n"} {
		cfg, err := loadFrom(t, content)
		if err != nil || cfg.IngestMaxContentBytes != 24*1024 || cfg.LLMCallsRetentionDays != 30 {
			t.Errorf("%s: got %+v, %v; want the defaults", name, cfg.IngestMaxContentBytes, err)
		}
	}
}

func TestAWrongTypeIsStillAParseError(t *testing.T) {
	_, err := loadFrom(t, "title:\n  numCtx: lots\n")
	if err == nil || !strings.Contains(err.Error(), "parse config file") {
		t.Errorf("err = %v, want a parse error", err)
	}
}

// The defaults, written out as a file, use only keys the strict decoder knows.
func TestTheDefaultsAsAFileLoadUnderStrictDecoding(t *testing.T) {
	out, err := yaml.Marshal(defaultFileConfig())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := loadFrom(t, string(out)); err != nil {
		t.Errorf("the defaults rejected: %v", err)
	}
}
