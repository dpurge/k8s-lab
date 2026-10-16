// Package config reads the assistant's settings from the environment. Nothing
// else in the program reads environment variables, so the full list of
// settings is in this file and in .env.example.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/jackc/pgx/v5/pgxpool"
)

// Names of the environment variables the assistant reads.
const (
	EnvDatabaseURL = "ASSISTANT_DATABASE_URL"
	EnvModelsFile  = "ASSISTANT_MODELS_FILE"
	// EnvWorkflowsDir names the directory of workflow YAML files.
	EnvWorkflowsDir = "ASSISTANT_WORKFLOWS_DIR"
	// EnvVertexTokenFile names the file holding the access token for Vertex AI.
	// It is a secret: it is read per call, never logged and never in a message.
	EnvVertexTokenFile = "ASSISTANT_VERTEX_TOKEN_FILE"
)

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
	// VertexTokenFile is the path of the file with the Vertex AI access token.
	// Only the path is configuration; empty means no vertex provider may be used.
	VertexTokenFile string
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

	if v, ok := lookup(EnvVertexTokenFile); ok {
		cfg.VertexTokenFile = strings.TrimSpace(v)
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
