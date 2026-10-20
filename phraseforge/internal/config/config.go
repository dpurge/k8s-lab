package config

import (
	"fmt"
	"os"
	"strings"

	"k8s-lab/shared/retention"
)

// Config holds everything the server needs to start.
type Config struct {
	BindAddr string

	PGHost     string
	PGPort     string
	PGDatabase string
	PGUser     string
	PGPassword string

	Transcription      PurposeConfig
	Translation        PurposeConfig
	Title              PurposeConfig
	ProcessText        PurposeConfig
	ProcessDialog      PurposeConfig
	GenerateVocabulary PurposeConfig
	GenerateModels     PurposeConfig
	// VocabularyItem and ModelsItem translate one list item into one site
	// locale in a single structured (JSON) call; their prompts are
	// templates with the prompt-eval setup's camelCase placeholders (see
	// specs/features/phraseforge-structured-item-translation.md).
	VocabularyItem PurposeConfig
	ModelsItem     PurposeConfig
	// CheckVocabulary decides, for the known phrases of a freshly generated
	// vocabulary list, which need a new usage example (one call per list).
	CheckVocabulary PurposeConfig
	// ReviewVocabulary reviews one vocabulary item (phrase, grammar,
	// transcription, translations, notes, examples) and proposes corrections.
	ReviewVocabulary PurposeConfig
	// ReviewModels reviews one models item (phrase, transcription, translations).
	ReviewModels PurposeConfig
	// ReviewTranscription and ReviewTranslation review the transcription and the
	// translations of one text or dialog.
	//
	// Each review has its own prompt and model tags: a stronger model for
	// reviews is selected on every review purpose, none falls back to another's.
	ReviewTranscription PurposeConfig
	ReviewTranslation   PurposeConfig

	// IngestMaxContentBytes caps the raw content one ingest request may carry
	// (yaml ingest.maxContentBytes). Raising it above what one LLM call can
	// hold relies on the content being processed in chunks.
	IngestMaxContentBytes int

	// LLMCallsRetentionDays is how long llm_calls rows are kept (yaml
	// llm_calls.retention_days); older rows are purged automatically. 0 keeps
	// them forever.
	LLMCallsRetentionDays int

	// JobsRetentionDaysDone and JobsRetentionDaysFailed are how long finished
	// jobs are kept (yaml jobs.retention_days_done for status done, jobs.retention_days_failed
	// for failed and cancelled ones); older rows are purged automatically. 0
	// keeps them forever. Pending and running jobs are never purged.
	JobsRetentionDaysDone   int
	JobsRetentionDaysFailed int

	// SessionKey signs the login session cookie. Must be set in production;
	// a fixed dev default is used only so `go run` works with zero setup.
	SessionKey string
}

// PurposeConfig is a per-purpose (transcription/translation) LLM default,
// used when no admin-configured llm_prompts row exists for a given
// (kind, source_language, target_language).
type PurposeConfig struct {
	// ModelTags are the tags a model must all have to serve this purpose; the
	// router picks the highest-priority available model that has them. Models
	// and their tags are configured in models.yaml (see EnvModelsFile).
	ModelTags []string
	NumCtx    int
	// TimeoutSeconds is this purpose's default LLM call timeout — 0 means
	// "no purpose-level override", falling through to shared/llm's own
	// built-in default. An admin llm_prompts.timeout_seconds override (see
	// ai.go's resolveTimeout) takes precedence over this when set.
	TimeoutSeconds int
	// Prompt is this purpose's default system prompt — used when no
	// admin-configured llm_prompts row exists, or its own prompt is blank.
	// Previously hardcoded in ai.go's prompt() switch; moved here so it's
	// inspectable/editable without a rebuild (see
	// specs/features/llm-purpose-timeout-and-prompt-config.md).
	Prompt string
	// MaxAttempts is the total attempts (first try included) one LLM call
	// gets when its reply fails validation and is sent back to the model for
	// correction; <= 0 means the built-in default of 3 (see ai/retry.go).
	MaxAttempts int
	// MaxReplyTokens caps one reply's length in tokens (Ollama's
	// num_predict); 0 means no cap. It bounds a model that loops: a reply that
	// is cut off fails validation and is retried, instead of running until it
	// fills the context window.
	MaxReplyTokens int
}

// fileConfig mirrors the mounted ConfigMap YAML file's shape. Credentials
// (PGUser/PGPassword; the provider API keys live in the environment variables
// models.yaml names) and Postgres connection fields
// (PGHost/PGPort/PGDatabase) are deliberately absent here — they stay
// plain/Secret-sourced env vars, never read from this file, so `migrate`
// works before the ConfigMap exists (it's a pre-install/pre-upgrade hook).
type fileConfig struct {
	BindAddr string `yaml:"bindAddr"`
	Ingest   struct {
		MaxContentBytes int `yaml:"maxContentBytes"`
	} `yaml:"ingest"`
	LLMCalls struct {
		RetentionDays int `yaml:"retention_days"`
	} `yaml:"llm_calls"`
	Jobs struct {
		RetentionDaysDone   int `yaml:"retention_days_done"`
		RetentionDaysFailed int `yaml:"retention_days_failed"`
	} `yaml:"jobs"`
	Transcription       purposeFileConfig `yaml:"transcription"`
	Translation         purposeFileConfig `yaml:"translation"`
	Title               purposeFileConfig `yaml:"title"`
	ProcessText         purposeFileConfig `yaml:"processText"`
	ProcessDialog       purposeFileConfig `yaml:"processDialog"`
	GenerateVocabulary  purposeFileConfig `yaml:"generateVocabulary"`
	GenerateModels      purposeFileConfig `yaml:"generateModels"`
	VocabularyItem      purposeFileConfig `yaml:"vocabularyItem"`
	ModelsItem          purposeFileConfig `yaml:"modelsItem"`
	CheckVocabulary     purposeFileConfig `yaml:"checkVocabulary"`
	ReviewVocabulary    purposeFileConfig `yaml:"reviewVocabulary"`
	ReviewModels        purposeFileConfig `yaml:"reviewModels"`
	ReviewTranscription purposeFileConfig `yaml:"reviewTranscription"`
	ReviewTranslation   purposeFileConfig `yaml:"reviewTranslation"`
}

type purposeFileConfig struct {
	ModelTags      []string `yaml:"model_tags"`
	NumCtx         int      `yaml:"numCtx"`
	TimeoutSeconds int      `yaml:"timeoutSeconds"`
	Prompt         string   `yaml:"prompt"`
	MaxAttempts    int      `yaml:"maxAttempts"`
	MaxReplyTokens int      `yaml:"maxReplyTokens"`
}

// namedPurpose is one purpose's file configuration with its key in the file.
type namedPurpose struct {
	name   string
	config purposeFileConfig
}

// purposes lists every purpose the file configures, under its key in the file.
func (f fileConfig) purposes() []namedPurpose {
	return []namedPurpose{
		{"transcription", f.Transcription}, {"translation", f.Translation}, {"title", f.Title},
		{"processText", f.ProcessText}, {"processDialog", f.ProcessDialog},
		{"generateVocabulary", f.GenerateVocabulary}, {"generateModels", f.GenerateModels},
		{"vocabularyItem", f.VocabularyItem}, {"modelsItem", f.ModelsItem},
		{"checkVocabulary", f.CheckVocabulary}, {"reviewVocabulary", f.ReviewVocabulary},
		{"reviewModels", f.ReviewModels}, {"reviewTranscription", f.ReviewTranscription},
		{"reviewTranslation", f.ReviewTranslation},
	}
}

func defaultFileConfig() fileConfig {
	var f fileConfig
	f.BindAddr = "0.0.0.0:8090"
	f.Ingest.MaxContentBytes = 24 * 1024
	f.LLMCalls.RetentionDays = 30
	f.Jobs.RetentionDaysDone = 7
	f.Jobs.RetentionDaysFailed = 30
	// TimeoutSeconds is only an overall backstop (1800s for every purpose):
	// streamed Ollama calls are primarily bounded by the provider's
	// first-token/idle limits, since a CPU-only node legitimately needs
	// ~300s for one short translation (see
	// specs/features/llm-streaming-progress-timeout.md). Prompt text is
	// copied verbatim from ai.go's former hardcoded switch so upgrading
	// doesn't change any existing behavior.
	f.Transcription = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 4096, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultTranscriptionPrompt,
	}
	f.Translation = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 4096, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultTranslationPrompt,
	}
	f.Title = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultTitlePrompt,
	}
	f.ProcessText = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultProcessTextPrompt,
	}
	f.ProcessDialog = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultProcessDialogPrompt,
	}
	f.GenerateVocabulary = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultGenerateVocabularyPrompt,
	}
	f.GenerateModels = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultGenerateModelsPrompt,
	}
	// NumCtx left 0 (Ollama's default) to match the prompt-eval request.
	f.VocabularyItem = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 4096, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 512,
		Prompt: DefaultVocabularyItemPrompt,
	}
	f.ModelsItem = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 4096, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 512,
		Prompt: DefaultModelsItemPrompt,
	}
	f.CheckVocabulary = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultCheckVocabularyPrompt,
	}
	// Every purpose names its own model tags, none falls back to another's:
	// a stronger model for a review is selected by tags on that purpose alone.
	f.ReviewVocabulary = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultReviewVocabularyPrompt,
	}
	f.ReviewModels = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 8192, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultReviewModelsPrompt,
	}
	f.ReviewTranscription = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 16384, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultReviewTranscriptionPrompt,
	}
	f.ReviewTranslation = purposeFileConfig{
		ModelTags: defaultModelTags(), NumCtx: 16384, TimeoutSeconds: 1800, MaxAttempts: 3, MaxReplyTokens: 0,
		Prompt: DefaultReviewTranslationPrompt,
	}
	return f
}

func env(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}

// Load reads the mounted config file (path from CONFIG_FILE, default
// /etc/phraseforge/config.yaml) over built-in defaults — a missing file is
// fine (defaults apply, so local runs without a cluster still work), but a
// malformed one is a real error, not something to silently paper over.
func Load() (Config, error) {
	fc := defaultFileConfig()
	path := env("CONFIG_FILE", "/etc/phraseforge/config.yaml")
	data, err := os.ReadFile(path)
	var findings []string
	switch {
	case os.IsNotExist(err):
		// use defaults
	case err != nil:
		return Config{}, fmt.Errorf("read config file %s: %w", path, err)
	default:
		unknown, err := decodeStrict(data, &fc)
		if err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
		}
		legacy, err := legacyKeyFindings(data)
		if err != nil {
			return Config{}, fmt.Errorf("parse config file %s: %w", path, err)
		}
		// The specific rename and legacy messages say more than "unknown key".
		findings = append(findings, legacy...)
		findings = append(findings, unknown...)
	}
	for _, p := range fc.purposes() {
		findings = append(findings, modelTagFindings(p.name, p.config.ModelTags)...)
	}
	if fc.Ingest.MaxContentBytes <= 0 {
		findings = append(findings, fmt.Sprintf("ingest.maxContentBytes must be greater than 0, got %d", fc.Ingest.MaxContentBytes))
	}
	findings = append(findings, retentionFindings("llm_calls.retention_days", fc.LLMCalls.RetentionDays)...)
	findings = append(findings, retentionFindings("jobs.retention_days_done", fc.Jobs.RetentionDaysDone)...)
	findings = append(findings, retentionFindings("jobs.retention_days_failed", fc.Jobs.RetentionDaysFailed)...)
	if len(findings) > 0 {
		for i, f := range findings {
			findings[i] = path + ": " + f
		}
		return Config{}, fmt.Errorf("config file has %d problem(s):\n  - %s", len(findings), strings.Join(findings, "\n  - "))
	}

	return Config{
		BindAddr: fc.BindAddr,

		PGHost:     env("PGHOST", "localhost"),
		PGPort:     env("PGPORT", "5432"),
		PGDatabase: env("PGDATABASE", "phraseforge"),
		PGUser:     env("PGUSER", "phraseforge"),
		PGPassword: env("PGPASSWORD", ""),

		Transcription:       PurposeConfig(fc.Transcription),
		Translation:         PurposeConfig(fc.Translation),
		Title:               PurposeConfig(fc.Title),
		ProcessText:         PurposeConfig(fc.ProcessText),
		ProcessDialog:       PurposeConfig(fc.ProcessDialog),
		GenerateVocabulary:  PurposeConfig(fc.GenerateVocabulary),
		GenerateModels:      PurposeConfig(fc.GenerateModels),
		VocabularyItem:      PurposeConfig(fc.VocabularyItem),
		ModelsItem:          PurposeConfig(fc.ModelsItem),
		CheckVocabulary:     PurposeConfig(fc.CheckVocabulary),
		ReviewVocabulary:    PurposeConfig(fc.ReviewVocabulary),
		ReviewModels:        PurposeConfig(fc.ReviewModels),
		ReviewTranscription: PurposeConfig(fc.ReviewTranscription),
		ReviewTranslation:   PurposeConfig(fc.ReviewTranslation),

		IngestMaxContentBytes: fc.Ingest.MaxContentBytes,
		LLMCallsRetentionDays: fc.LLMCalls.RetentionDays,

		JobsRetentionDaysDone:   fc.Jobs.RetentionDaysDone,
		JobsRetentionDaysFailed: fc.Jobs.RetentionDaysFailed,

		SessionKey: env("SESSION_KEY", "dev-only-insecure-key-change-me"),
	}, nil
}

// retentionFindings reports a retention in days outside 0 (keep forever) to
// retention.MaxDays, naming key; a larger value would overflow time.Duration
// when turned into a cutoff.
func retentionFindings(key string, days int) []string {
	if days < 0 || days > retention.MaxDays {
		return []string{fmt.Sprintf("%s must be from 0 (keep forever) to %d days, got %d", key, retention.MaxDays, days)}
	}
	return nil
}

// NamedPurpose is one purpose's configuration with its key in the config file.
type NamedPurpose struct {
	Name    string
	Purpose PurposeConfig
}

// Purposes lists every purpose under its key in the config file.
func (c Config) Purposes() []NamedPurpose {
	return []NamedPurpose{
		{"transcription", c.Transcription}, {"translation", c.Translation}, {"title", c.Title},
		{"processText", c.ProcessText}, {"processDialog", c.ProcessDialog},
		{"generateVocabulary", c.GenerateVocabulary}, {"generateModels", c.GenerateModels},
		{"vocabularyItem", c.VocabularyItem}, {"modelsItem", c.ModelsItem},
		{"checkVocabulary", c.CheckVocabulary}, {"reviewVocabulary", c.ReviewVocabulary},
		{"reviewModels", c.ReviewModels}, {"reviewTranscription", c.ReviewTranscription},
		{"reviewTranslation", c.ReviewTranslation},
	}
}

// DefaultNumCtx is the window used for a purpose whose NumCtx is 0. It is
// Ollama's own default for the CPU-only prod node and the dev machine (both
// report context_length 4096), and is sent explicitly with the call, so the
// chunk size and the call agree on the window instead of the call quietly
// running under whatever the server defaults to.
const DefaultNumCtx = 4096

// EffectiveNumCtx is n, or DefaultNumCtx when n is unset (zero or negative).
func EffectiveNumCtx(n int) int {
	if n <= 0 {
		return DefaultNumCtx
	}
	return n
}
