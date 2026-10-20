package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func lookupFrom(env map[string]string) Lookup {
	return func(name string) (string, bool) {
		v, ok := env[name]
		return v, ok
	}
}

const validURL = "postgres://assistant:s3cr3t-pass@localhost:5432/assistant?sslmode=disable"

func TestLoadAcceptsAValidDatabaseURL(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: "  " + validURL + "\n"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.DatabaseURL != validURL {
		t.Errorf("DatabaseURL = %q (it should be trimmed)", cfg.DatabaseURL)
	}
}

func TestLoadAcceptsKeywordValueConnectionStrings(t *testing.T) {
	if _, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: "host=localhost port=5432 dbname=assistant user=a password=b"})); err != nil {
		t.Errorf("a libpq key=value string is a valid connection string: %v", err)
	}
}

func TestLoadRequiresTheDatabaseURLAndNamesIt(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset":      {},
		"empty":      {EnvDatabaseURL: ""},
		"whitespace": {EnvDatabaseURL: "   "},
	} {
		_, err := Load(lookupFrom(env))
		if err == nil {
			t.Fatalf("%s: expected an error", name)
		}
		if !strings.Contains(err.Error(), "ASSISTANT_DATABASE_URL is required") || !strings.Contains(err.Error(), ".env.example") {
			t.Errorf("%s: error = %q", name, err)
		}
	}
}

func TestLoadRejectsAnInvalidURLWithoutEchoingTheSecret(t *testing.T) {
	_, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: "postgres://assistant:s3cr3t-pass@local host:5432/assistant?sslmode=bogus"}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "ASSISTANT_DATABASE_URL is not a valid") {
		t.Errorf("error = %q", err)
	}
	if strings.Contains(err.Error(), "s3cr3t-pass") {
		t.Errorf("the password leaked into the error: %q", err)
	}
}

func writeEnvFile(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), ".env")
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestApplyDotEnvSetsUnsetVariables(t *testing.T) {
	t.Setenv("DOTENV_TEST_ALREADY", "from-environment")
	os.Unsetenv("DOTENV_TEST_A")
	os.Unsetenv("DOTENV_TEST_B")
	os.Unsetenv("DOTENV_TEST_C")
	os.Unsetenv("DOTENV_TEST_D")
	os.Unsetenv("DOTENV_TEST_E")
	t.Cleanup(func() {
		for _, n := range []string{"DOTENV_TEST_A", "DOTENV_TEST_B", "DOTENV_TEST_C", "DOTENV_TEST_D", "DOTENV_TEST_E"} {
			os.Unsetenv(n)
		}
	})
	path := writeEnvFile(t, `# a comment

DOTENV_TEST_A=plain
DOTENV_TEST_B = "double quoted = with equals"
export DOTENV_TEST_C='single quoted'
DOTENV_TEST_D=postgres://u:p@h/db?x=1&y=2
DOTENV_TEST_E=
DOTENV_TEST_ALREADY=from-file
`)
	if err := ApplyDotEnv(path); err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"DOTENV_TEST_A":       "plain",
		"DOTENV_TEST_B":       "double quoted = with equals",
		"DOTENV_TEST_C":       "single quoted",
		"DOTENV_TEST_D":       "postgres://u:p@h/db?x=1&y=2",
		"DOTENV_TEST_E":       "",
		"DOTENV_TEST_ALREADY": "from-environment",
	}
	for name, v := range want {
		if got, ok := os.LookupEnv(name); !ok || got != v {
			t.Errorf("%s = %q (set %v), want %q", name, got, ok, v)
		}
	}
}

func TestApplyDotEnvMissingFileIsNotAnError(t *testing.T) {
	if err := ApplyDotEnv(filepath.Join(t.TempDir(), "nope")); err != nil {
		t.Errorf("err = %v", err)
	}
}

func TestApplyDotEnvReportsTheLineNumberButNeverTheContent(t *testing.T) {
	path := writeEnvFile(t, "GOOD_ONE=1\n\nthis line has a secret-token-123 and no equals sign\n")
	err := ApplyDotEnv(path)
	if err == nil {
		t.Fatal("expected an error")
	}
	if !strings.Contains(err.Error(), "line 3") || strings.Contains(err.Error(), "secret-token-123") {
		t.Errorf("error = %q", err)
	}
	os.Unsetenv("GOOD_ONE")
}

func TestApplyDotEnvRejectsBadNames(t *testing.T) {
	for _, line := range []string{"1BAD=x", "BAD NAME=x", "=x", "BAD-NAME=x"} {
		if err := ApplyDotEnv(writeEnvFile(t, line+"\n")); err == nil || !strings.Contains(err.Error(), "line 1") {
			t.Errorf("%q: err = %v", line, err)
		}
	}
}

func TestApplyDotEnvOpenError(t *testing.T) {
	if err := ApplyDotEnv(t.TempDir()); err == nil {
		t.Errorf("a directory is not a readable .env file")
	}
}

func TestModelsFileDefaultsAndCanBeSet(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL}))
	if err != nil || cfg.ModelsFile != DefaultModelsFile || DefaultModelsFile != "/etc/assistant/models.yaml" {
		t.Errorf("default: %q %v", cfg.ModelsFile, err)
	}
	cfg, err = Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL, EnvModelsFile: " ./models.yaml "}))
	if err != nil || cfg.ModelsFile != "./models.yaml" {
		t.Errorf("set: %q %v", cfg.ModelsFile, err)
	}
	cfg, _ = Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL, EnvModelsFile: "  "}))
	if cfg.ModelsFile != DefaultModelsFile {
		t.Errorf("blank means the default: %q", cfg.ModelsFile)
	}
}

func TestWorkflowsDirDefaultsAndCanBeSet(t *testing.T) {
	cfg, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL}))
	if err != nil || cfg.WorkflowsDir != DefaultWorkflowsDir || DefaultWorkflowsDir != "/etc/assistant-workflows" {
		t.Errorf("default: %q %v", cfg.WorkflowsDir, err)
	}
	cfg, _ = Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL, EnvWorkflowsDir: " ./workflows "}))
	if cfg.WorkflowsDir != "./workflows" {
		t.Errorf("set: %q", cfg.WorkflowsDir)
	}
	cfg, _ = Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL, EnvWorkflowsDir: " "}))
	if cfg.WorkflowsDir != DefaultWorkflowsDir {
		t.Errorf("blank means the default: %q", cfg.WorkflowsDir)
	}
}

// A Vertex token now comes from the provider's api_key_from in models.yaml, so
// no environment variable names it any more.
func TestTheRemovedVertexTokenFileVariableIsNotRead(t *testing.T) {
	var asked []string
	lookup := func(name string) (string, bool) {
		asked = append(asked, name)
		if name == EnvDatabaseURL {
			return validURL, true
		}
		return "", false
	}
	if _, err := Load(lookup); err != nil {
		t.Fatal(err)
	}
	for _, name := range asked {
		if strings.Contains(name, "VERTEX") {
			t.Errorf("Load reads %s", name)
		}
	}
}

func TestLoadKeepsLLMCallsForNinetyDaysByDefault(t *testing.T) {
	for name, env := range map[string]map[string]string{
		"unset": {EnvDatabaseURL: validURL},
		"empty": {EnvDatabaseURL: validURL, EnvLLMCallsRetentionDays: "  "},
	} {
		cfg, err := Load(lookupFrom(env))
		if err != nil || cfg.LLMCallsRetentionDays != 90 {
			t.Errorf("%s: got %d, %v; want 90, nil", name, cfg.LLMCallsRetentionDays, err)
		}
	}
}

func TestLoadReadsTheLLMCallsRetentionAndZeroKeepsForever(t *testing.T) {
	for value, want := range map[string]int{"7": 7, " 365 ": 365, "0": 0, "36500": 36500} {
		cfg, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL, EnvLLMCallsRetentionDays: value}))
		if err != nil || cfg.LLMCallsRetentionDays != want {
			t.Errorf("%q: got %d, %v; want %d, nil", value, cfg.LLMCallsRetentionDays, err, want)
		}
	}
}

func TestLoadRejectsANegativeOrNonNumericLLMCallsRetention(t *testing.T) {
	for _, value := range []string{"-1", "abc", "1.5", "30d", "36501", "99999999999"} {
		_, err := Load(lookupFrom(map[string]string{EnvDatabaseURL: validURL, EnvLLMCallsRetentionDays: value}))
		if err == nil || !strings.Contains(err.Error(), "ASSISTANT_LLM_CALLS_RETENTION_DAYS") {
			t.Errorf("%q: err = %v, want it to name ASSISTANT_LLM_CALLS_RETENTION_DAYS", value, err)
		}
	}
}
