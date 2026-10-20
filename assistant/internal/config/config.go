// Package config reads the assistant's settings from the environment. Nothing
// else in the program reads environment variables, so the full list of
// settings is in this file and in .env.example.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/retention"
)

// Names of the environment variables the assistant reads.
const (
	EnvDatabaseURL = "ASSISTANT_DATABASE_URL"
	EnvModelsFile  = "ASSISTANT_MODELS_FILE"
	// EnvWorkflowsDir names the directory of workflow YAML files.
	EnvWorkflowsDir = "ASSISTANT_WORKFLOWS_DIR"
	// EnvLLMCallsRetentionDays is how many days the model-call history is kept.
	EnvLLMCallsRetentionDays = "ASSISTANT_LLM_CALLS_RETENTION_DAYS"
)

// DefaultLLMCallsRetentionDays is how long llm_calls rows are kept when
// EnvLLMCallsRetentionDays is not set.
const DefaultLLMCallsRetentionDays = 90

// DefaultWorkflowsDir is where the workflow files are mounted in the cluster.
const DefaultWorkflowsDir = "/etc/assistant-workflows"

// DefaultModelsFile is where the models file is mounted in the cluster.
const DefaultModelsFile = "/etc/assistant/models.yaml"

// Config is the validated settings of one run.
type Config struct {
	// DatabaseURL is the PostgreSQL connection string. It holds a password,
	// so it is never logged or put in an error message.
	DatabaseURL string
	// ModelsFile is the path of models.yaml, which lists the providers and
	// models. It holds no secrets.
	ModelsFile string
	// WorkflowsDir is the directory of workflow YAML files (a ConfigMap in the
	// cluster). It holds no secrets.
	WorkflowsDir string
	// LLMCallsRetentionDays is how long llm_calls rows are kept; older rows are
	// purged automatically, so usage and call history cover only this period.
	// 0 keeps them forever. Tasks and their steps are not purged.
	LLMCallsRetentionDays int
}

// Lookup reads one environment variable, like os.LookupEnv.
type Lookup func(name string) (string, bool)

// Load validates the settings. Every problem is reported in one error naming
// the variable, so a misconfigured deployment is fixed in one go.
func Load(lookup Lookup) (Config, error) {
	var problems []error
	var cfg Config

	raw, ok := lookup(EnvDatabaseURL)
	switch {
	case !ok || strings.TrimSpace(raw) == "":
		problems = append(problems, fmt.Errorf("%s is required: a PostgreSQL connection string such as postgres://user:password@host:5432/assistant (see .env.example)", EnvDatabaseURL))
	default:
		// ParseConfig errors can quote the string, which holds the password,
		// so only the fact that it did not parse is reported.
		if _, err := pgxpool.ParseConfig(strings.TrimSpace(raw)); err != nil {
			problems = append(problems, fmt.Errorf("%s is not a valid PostgreSQL connection string (the value is not shown because it holds a password)", EnvDatabaseURL))
		} else {
			cfg.DatabaseURL = strings.TrimSpace(raw)
		}
	}

	cfg.ModelsFile = DefaultModelsFile
	if v, ok := lookup(EnvModelsFile); ok && strings.TrimSpace(v) != "" {
		cfg.ModelsFile = strings.TrimSpace(v)
	}

	cfg.WorkflowsDir = DefaultWorkflowsDir
	if v, ok := lookup(EnvWorkflowsDir); ok && strings.TrimSpace(v) != "" {
		cfg.WorkflowsDir = strings.TrimSpace(v)
	}

	cfg.LLMCallsRetentionDays = DefaultLLMCallsRetentionDays
	if v, ok := lookup(EnvLLMCallsRetentionDays); ok && strings.TrimSpace(v) != "" {
		days, err := strconv.Atoi(strings.TrimSpace(v))
		if err != nil || days < 0 || days > retention.MaxDays {
			problems = append(problems, fmt.Errorf("%s must be a whole number of days from 0 (keep forever) to %d, got %q", EnvLLMCallsRetentionDays, retention.MaxDays, strings.TrimSpace(v)))
		} else {
			cfg.LLMCallsRetentionDays = days
		}
	}

	if len(problems) > 0 {
		return Config{}, errors.Join(problems...)
	}
	return cfg, nil
}

// ApplyDotEnv sets the variables of a .env file that are not already set in
// the environment, so a real environment always wins. A missing file is not an
// error: in the cluster there is none. A malformed line is, reported with its
// number and never its content, since values are secrets.
func ApplyDotEnv(path string) error {
	f, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for n := 1; scanner.Scan(); n++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		line = strings.TrimPrefix(line, "export ")
		name, value, ok := strings.Cut(line, "=")
		name = strings.TrimSpace(name)
		if !ok || !validName(name) {
			return fmt.Errorf("%s line %d: expected NAME=value", path, n)
		}
		if _, set := os.LookupEnv(name); set {
			continue
		}
		if err := os.Setenv(name, unquote(strings.TrimSpace(value))); err != nil {
			return fmt.Errorf("%s line %d: %w", path, n, err)
		}
	}
	return scanner.Err()
}

func validName(name string) bool {
	if name == "" {
		return false
	}
	for i, r := range name {
		letter := r == '_' || (r >= 'A' && r <= 'Z') || (r >= 'a' && r <= 'z')
		digit := r >= '0' && r <= '9'
		if !letter && !(digit && i > 0) {
			return false
		}
	}
	return true
}

func unquote(v string) string {
	if len(v) >= 2 && (v[0] == '"' && v[len(v)-1] == '"' || v[0] == '\'' && v[len(v)-1] == '\'') {
		return v[1 : len(v)-1]
	}
	return v
}
