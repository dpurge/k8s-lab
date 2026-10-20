package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runCLI(args ...string) (code int, stdout, stderr string) {
	var out, errOut bytes.Buffer
	code = run(args, &out, &errOut)
	return code, out.String(), errOut.String()
}

func writeFile(t *testing.T, name, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), name)
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

const goodWorkflow = `id: demo
version: 2
description: A demo.
state_schema: {type: object}
start: a
limits: {max_steps: 5, max_tokens: 1000, max_retries: 1, max_visits: 3}
states:
  a:
    instructions: Do it.
    tools: [rag_search]
    end: true
`

func TestValidateGoodFile(t *testing.T) {
	path := writeFile(t, "good.yaml", goodWorkflow)
	code, stdout, stderr := runCLI("workflow", "validate", path)
	if code != 0 || stderr != "" {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if !strings.Contains(stdout, `ok: workflow "demo" version 2, 1 states`) {
		t.Errorf("stdout = %q", stdout)
	}
}

func TestValidateReportsSchemaProblemWithLine(t *testing.T) {
	path := writeFile(t, "bad.yaml", strings.Replace(goodWorkflow, "version: 2", "version: two", 1))
	code, stdout, stderr := runCLI("workflow", "validate", path)
	if code != 1 || stdout != "" {
		t.Fatalf("code=%d stdout=%q", code, stdout)
	}
	if !strings.Contains(stderr, ":2: version:") {
		t.Errorf("stderr = %q", stderr)
	}
}

func TestValidateReportsLintFindings(t *testing.T) {
	path := writeFile(t, "lint.yaml", strings.Replace(goodWorkflow, "start: a", "start: ghost", 1))
	code, _, stderr := runCLI("workflow", "validate", path)
	if code != 1 || !strings.Contains(stderr, ":5: start:") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestValidateToolCheckOnlyWithToolsFlag(t *testing.T) {
	path := writeFile(t, "tools.yaml", goodWorkflow)
	if code, _, stderr := runCLI("workflow", "validate", path); code != 0 {
		t.Fatalf("without --tools: code=%d stderr=%q", code, stderr)
	}
	code, _, stderr := runCLI("workflow", "validate", "--tools", "memory_search", path)
	if code != 1 || !strings.Contains(stderr, `unknown tool "rag_search"`) {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
	if code, _, stderr := runCLI("workflow", "validate", "--tools", "memory_search, rag_search", path); code != 0 {
		t.Fatalf("with both tools: code=%d stderr=%q", code, stderr)
	}
}

func TestValidateSeveralFilesFailsIfAnyFails(t *testing.T) {
	good := writeFile(t, "good.yaml", goodWorkflow)
	bad := writeFile(t, "bad.yaml", "id: x\n")
	code, stdout, stderr := runCLI("workflow", "validate", good, bad)
	if code != 1 {
		t.Fatalf("code = %d", code)
	}
	if !strings.Contains(stdout, "ok:") || !strings.Contains(stderr, "missing required key") {
		t.Errorf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestValidateMissingFile(t *testing.T) {
	code, _, stderr := runCLI("workflow", "validate", filepath.Join(t.TempDir(), "nope.yaml"))
	if code != 1 || !strings.Contains(stderr, "nope.yaml") {
		t.Fatalf("code=%d stderr=%q", code, stderr)
	}
}

func TestUsageErrors(t *testing.T) {
	for _, args := range [][]string{
		{},
		{"workflow"},
		{"workflow", "check", "x.yaml"},
		{"other", "validate", "x.yaml"},
		{"workflow", "validate"},
		{"workflow", "validate", "--bogus", "x.yaml"},
	} {
		code, _, stderr := runCLI(args...)
		if code != 2 || stderr == "" {
			t.Errorf("args %v: code=%d stderr=%q, want 2 and a message", args, code, stderr)
		}
	}
}

func TestDispatchRejectsAnUnknownCommand(t *testing.T) {
	if code := dispatch([]string{"frobnicate"}); code != 2 {
		t.Errorf("code = %d", code)
	}
}

func TestServeWithoutConfigurationFailsNamingTheVariable(t *testing.T) {
	t.Chdir(t.TempDir()) // no .env here
	t.Setenv("ASSISTANT_DATABASE_URL", "")
	if err := os.Unsetenv("ASSISTANT_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	orig := os.Stderr
	os.Stderr = w
	code := serve()
	w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)
	if code != 1 || !strings.Contains(string(out), "ASSISTANT_DATABASE_URL is required") {
		t.Errorf("code=%d stderr=%q", code, out)
	}
}

func TestLoadConfigReadsDotEnvButTheEnvironmentWins(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(".env", []byte("ASSISTANT_DATABASE_URL=postgres://from-file@localhost/a\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Unsetenv("ASSISTANT_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Unsetenv("ASSISTANT_DATABASE_URL") })
	cfg, err := loadConfig()
	if err != nil || cfg.DatabaseURL != "postgres://from-file@localhost/a" {
		t.Fatalf("from file: %v %+v", err, cfg)
	}
	t.Setenv("ASSISTANT_DATABASE_URL", "postgres://from-env@localhost/a")
	cfg, err = loadConfig()
	if err != nil || cfg.DatabaseURL != "postgres://from-env@localhost/a" {
		t.Fatalf("env wins: %v %+v", err, cfg)
	}
}

func TestMigrateWithoutConfigurationFailsNamingTheVariable(t *testing.T) {
	t.Chdir(t.TempDir())
	if err := os.Unsetenv("ASSISTANT_DATABASE_URL"); err != nil {
		t.Fatal(err)
	}
	r, w, _ := os.Pipe()
	orig := os.Stderr
	os.Stderr = w
	code := migrate()
	w.Close()
	os.Stderr = orig
	out, _ := io.ReadAll(r)
	if code != 1 || !strings.Contains(string(out), "ASSISTANT_DATABASE_URL is required") {
		t.Errorf("code=%d stderr=%q", code, out)
	}
}

func TestModelsValidate(t *testing.T) {
	good := writeFile(t, "models.yaml", `providers:
  - {id: local, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
models:
  - {id: m, provider: local, model: gemma4:12b, tags: [chat], priority: 1}
`)
	var out, errOut bytes.Buffer
	if code := runModels([]string{"models", "validate", good}, &out, &errOut); code != 0 || !strings.Contains(out.String(), "ok: 1 providers, 1 models") || errOut.Len() != 0 {
		t.Errorf("good file: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}

	bad := writeFile(t, "bad.yaml", strings.Replace("providers:\n  - {id: local, type: ollama, base_url: \"http://localhost:11434\"}\nmodels:\n  - {id: m, provider: ghost, model: x, tags: [chat], priority: 1}\n", "x", "y", 1))
	out.Reset()
	errOut.Reset()
	if code := runModels([]string{"models", "validate", good, bad}, &out, &errOut); code != 1 || !strings.Contains(out.String(), "ok:") || !strings.Contains(errOut.String(), `provider "ghost"`) || !strings.Contains(errOut.String(), ":4:") {
		t.Errorf("bad file: code=%d out=%q err=%q", code, out.String(), errOut.String())
	}

	out.Reset()
	errOut.Reset()
	if code := runModels([]string{"models", "validate", filepath.Join(t.TempDir(), "nope.yaml")}, &out, &errOut); code != 1 || !strings.Contains(errOut.String(), "nope.yaml") {
		t.Errorf("missing file: code=%d err=%q", code, errOut.String())
	}
	for _, args := range [][]string{{"models"}, {"models", "validate"}, {"models", "check", "x"}} {
		if code := runModels(args, &out, &errOut); code != 2 {
			t.Errorf("%v: code = %d", args, code)
		}
	}
}

func TestModelsValidateAcceptsAndRejectsHeaders(t *testing.T) {
	doc := func(headers string) string {
		return "providers:\n  - {id: local, type: ollama, base_url: \"http://localhost:11434\", api_key_from: \"\", headers: " + headers + "}\nmodels:\n  - {id: m, provider: local, model: x, tags: [chat], priority: 1}\n"
	}
	const secret = "pasted-SECRET-HEADER-VALUE"
	var out, errOut bytes.Buffer
	good := writeFile(t, "good.yaml", doc(`{X-Title: app, X-Env: "env:SOME_VARIABLE", X-File: "file:/run/h", anthropic-version: "2023-06-01"}`))
	if code := runModels([]string{"models", "validate", good}, &out, &errOut); code != 0 {
		t.Errorf("good headers: code=%d err=%q", code, errOut.String())
	}
	bad := writeFile(t, "bad.yaml", doc(`{Authorization: `+secret+`, "Bad Name": v, X-Env: "env:1X"}`))
	out.Reset()
	errOut.Reset()
	code := runModels([]string{"models", "validate", bad}, &out, &errOut)
	if code != 1 || !strings.Contains(errOut.String(), `"local"`) || !strings.Contains(errOut.String(), "Authorization") || !strings.Contains(errOut.String(), "api_key_from") {
		t.Errorf("bad headers: code=%d err=%q", code, errOut.String())
	}
	if strings.Contains(errOut.String(), secret) || strings.Contains(out.String(), secret) {
		t.Error("the output echoes a header value")
	}
}

func TestTheExampleModelsFileValidatesWithTheCommand(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := runModels([]string{"models", "validate", "models.example.yaml"}, &out, &errOut); code != 0 {
		t.Errorf("code=%d err=%q", code, errOut.String())
	}
}
