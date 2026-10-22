package modelcfg

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/yamlschema"
)

const valid = `providers:
  - id: laptop
    type: ollama
    base_url: http://localhost:11434
    api_key_from: ""
  - id: gpu-box
    type: ollama
    base_url: https://gpu-box.lan:11434
    api_key_from: ""
models:
  - id: big
    provider: gpu-box
    model: gemma4:12b
    tags: [chat, review]
    priority: 20
    options: {num_ctx: 16384, think: true, num_predict: 512, first_token_timeout_seconds: 300, idle_timeout_seconds: 30, overall_timeout_seconds: 900}
    price: {input_per_mtok: 1.5, output_per_mtok: 6}
  - id: small
    provider: laptop
    model: gemma4:e4b
    tags: [chat]
    priority: 5
`

func load(t *testing.T, data string) (*Config, error) {
	t.Helper()
	return LoadBytes("models.yaml", []byte(data))
}

func loadError(t *testing.T, data string) *yamlschema.Error {
	t.Helper()
	_, err := load(t, data)
	var e *yamlschema.Error
	if !errors.As(err, &e) {
		t.Fatalf("want *yamlschema.Error, got %T %v", err, err)
	}
	return e
}

func finding(t *testing.T, e *yamlschema.Error, path string) yamlschema.Finding {
	t.Helper()
	for _, f := range e.Findings {
		if f.Path == path {
			return f
		}
	}
	t.Fatalf("no finding for %q in:\n%s", path, e)
	return yamlschema.Finding{}
}

func TestLoadsAValidFile(t *testing.T) {
	c, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Providers) != 2 || len(c.Models) != 2 {
		t.Fatalf("%d providers, %d models", len(c.Providers), len(c.Models))
	}
	big := c.Models[0]
	if big.ID != "big" || big.Provider != "gpu-box" || big.Name != "gemma4:12b" || big.Priority != 20 || strings.Join(big.Tags, ",") != "chat,review" {
		t.Errorf("big = %+v", big)
	}
	if big.Options != (Options{NumCtx: 16384, NumPredict: 512, Think: true, FirstTokenTimeoutSeconds: 300, IdleTimeoutSeconds: 30, OverallTimeoutSeconds: 900}) {
		t.Errorf("options = %+v", big.Options)
	}
	if big.Price != (Price{InputPerMTok: 1.5, OutputPerMTok: 6}) {
		t.Errorf("price = %+v", big.Price)
	}
	if small := c.Models[1]; small.Priority != 5 || small.Options != (Options{}) || small.Price != (Price{}) {
		t.Errorf("a model without options or price gets zeros, and priority 5 is kept: %+v", small)
	}
	if p, ok := c.Provider("gpu-box"); !ok || p.BaseURL != "https://gpu-box.lan:11434" || p.Type != "ollama" {
		t.Errorf("provider lookup = %+v %v", p, ok)
	}
	if _, ok := c.Provider("nope"); ok {
		t.Errorf("an unknown provider must not be found")
	}
}

func TestBreakerDefaultsAndOverrides(t *testing.T) {
	c, _ := load(t, valid)
	b := c.Routing.Breaker
	if b.Failures != 3 || b.CooldownSeconds != 60 || b.RateLimitedCooldownSeconds != 30 {
		t.Errorf("defaults = %+v", b)
	}
	if b.Cooldown() != 60*time.Second || b.RateLimitedCooldown() != 30*time.Second {
		t.Errorf("durations = %v %v", b.Cooldown(), b.RateLimitedCooldown())
	}
	c, err := load(t, valid+"routing:\n  breaker: {failures: 5, cooldown_seconds: 120}\n")
	if err != nil {
		t.Fatal(err)
	}
	if b := c.Routing.Breaker; b.Failures != 5 || b.CooldownSeconds != 120 || b.RateLimitedCooldownSeconds != 30 {
		t.Errorf("a partial override keeps the other defaults: %+v", b)
	}
}

func TestSeveralOllamaProvidersMayCoexist(t *testing.T) {
	if _, err := load(t, valid); err != nil {
		t.Errorf("a local and a remote Ollama are the point: %v", err)
	}
}

func TestPriceCost(t *testing.T) {
	p := Price{InputPerMTok: 3, OutputPerMTok: 15}
	if got := p.Cost(1_000_000, 1_000_000); got != 18 {
		t.Errorf("cost = %v", got)
	}
	if got := p.Cost(1200, 300); got < 0.0081-1e-12 || got > 0.0081+1e-12 {
		t.Errorf("1200 in, 300 out = %v, want 0.0081", got)
	}
	if (Price{}).Cost(5_000_000, 5_000_000) != 0 || p.Cost(0, 0) != 0 {
		t.Errorf("free and empty calls cost nothing")
	}
}

func TestSchemaViolationsNameKeyAndLine(t *testing.T) {
	tests := []struct {
		name, edit, with, path string
		line                   int
	}{
		{"unknown provider type", "type: ollama\n    base_url: http://localhost:11434", "type: bedrock\n    base_url: http://localhost:11434", "providers.0.type", 3},
		{"provider id with capitals", "id: laptop", "id: Laptop", "providers.0.id", 2},
		{"no base_url", "    base_url: http://localhost:11434\n", "", "providers.0", 2},
		{"empty tags", "tags: [chat, review]", "tags: []", "models.0.tags", 14},
		{"blank tag", "tags: [chat, review]", `tags: ["chat", ""]`, "models.0.tags.1", 14},
		{"tag with a space", "tags: [chat, review]", `tags: ["chat", "two words"]`, "models.0.tags.1", 14},
		{"duplicate tag", "tags: [chat, review]", "tags: [chat, chat]", "models.0.tags", 14},
		{"priority is text", "priority: 20", "priority: high", "models.0.priority", 15},
		{"priority is negative", "priority: 20", "priority: -1", "models.0.priority", 15},
		{"tag with an underscore", "tags: [chat, review]", `tags: ["chat", "two_words"]`, "models.0.tags.1", 14},
		{"tag starting with a digit", "tags: [chat, review]", `tags: ["chat", "2fast"]`, "models.0.tags.1", 14},
		{"tag with a capital", "tags: [chat, review]", `tags: ["chat", "Fast"]`, "models.0.tags.1", 14},
		{"priority is fractional", "priority: 20", "priority: 2.5", "models.0.priority", 15},
		{"negative price", "output_per_mtok: 6", "output_per_mtok: -1", "models.0.price.output_per_mtok", 17},
		{"num_ctx zero", "num_ctx: 16384", "num_ctx: 0", "models.0.options.num_ctx", 16},
		{"unknown option", "think: true,", "think: true, temperature: 2,", "models.0.options.temperature", 16},
		{"unknown key on a model", "priority: 20", "priority: 20\n    colour: red", "models.0.colour", 16},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if !strings.Contains(valid, tc.edit) {
				t.Fatalf("test edit %q not in the fixture", tc.edit)
			}
			e := loadError(t, strings.Replace(valid, tc.edit, tc.with, 1))
			f := finding(t, e, tc.path)
			if f.Line != tc.line {
				t.Errorf("line = %d, want %d (%s)", f.Line, tc.line, e)
			}
		})
	}
}

func TestBreakerValuesMustBePositive(t *testing.T) {
	for _, bad := range []string{"failures: 0", "cooldown_seconds: 0", "rate_limited_cooldown_seconds: -3", "failures: many"} {
		e := loadError(t, valid+"routing:\n  breaker: {"+bad+"}\n")
		if len(e.Findings) == 0 {
			t.Errorf("%s must be refused", bad)
		}
	}
}

func TestRequiredTopLevelKeysAndEmptyLists(t *testing.T) {
	e := loadError(t, "routing: {}\n")
	for _, key := range []string{"providers", "models"} {
		if !strings.Contains(e.Error(), `missing required key "`+key+`"`) {
			t.Errorf("no finding for %s: %s", key, e)
		}
	}
	for _, in := range []string{"providers: []\nmodels: []\n", "providers:\n  - {id: a, type: ollama, base_url: 'http://h'}\nmodels: []\n"} {
		if e := loadError(t, in); len(e.Findings) == 0 {
			t.Errorf("empty lists must be refused: %q", in)
		}
	}
}

func TestInconsistenciesAreReportedTogether(t *testing.T) {
	data := `providers:
  - {id: laptop, type: ollama, base_url: "http://localhost:11434", api_key_from: ""}
  - {id: laptop, type: ollama, base_url: "ftp://elsewhere", api_key_from: ""}
  - {id: web, type: ollama, base_url: "http://user:secret@host:11434", api_key_from: ""}
  - {id: bare, type: ollama, base_url: "http://", api_key_from: ""}
  - {id: odd, type: ollama, base_url: "not a url", api_key_from: ""}
models:
  - {id: a, provider: laptop, model: m, tags: [x], priority: 1}
  - {id: a, provider: ghost, model: m, tags: [x], priority: 1}
`
	e := loadError(t, data)
	want := map[string]string{
		"providers.1.id":       `provider id "laptop" is used more than once`,
		"providers.1.base_url": "must start with http:// or https://",
		"providers.2.base_url": "must not contain a user name or password",
		"providers.3.base_url": "has no host",
		"providers.4.base_url": "must start with http:// or https://",
		"models.1.id":          `model id "a" is used more than once`,
		"models.1.provider":    `provider "ghost", which is not listed`,
	}
	for path, text := range want {
		if f := finding(t, e, path); !strings.Contains(f.Message, text) {
			t.Errorf("%s: %q does not contain %q", path, f.Message, text)
		}
	}
	for i := 1; i < len(e.Findings); i++ {
		if e.Findings[i].Line < e.Findings[i-1].Line {
			t.Errorf("not in line order:\n%s", e)
		}
	}
}

func TestACredentialInTheURLIsNeverEchoed(t *testing.T) {
	e := loadError(t, strings.Replace(valid, "http://localhost:11434", "http://admin:hunter2@localhost:11434", 1))
	if strings.Contains(e.Error(), "hunter2") {
		t.Errorf("the password leaked into the error: %s", e)
	}
}

func TestLoadReadsAFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "models.yaml")
	if err := os.WriteFile(path, []byte(valid), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err != nil {
		t.Fatal(err)
	}
	_, err := Load(filepath.Join(dir, "nope.yaml"))
	if err == nil || !errors.Is(err, os.ErrNotExist) || !strings.Contains(err.Error(), "nope.yaml") {
		t.Errorf("missing file: %v", err)
	}
}

func TestTheExampleFileIsValidAndShowsTwoOllamas(t *testing.T) {
	c, err := Load("../../assistant/models.example.yaml")
	if err != nil {
		t.Fatal(err)
	}
	urls := map[string]bool{}
	for _, p := range c.Providers {
		urls[p.BaseURL] = true
	}
	if len(c.Providers) < 2 || len(urls) < 2 {
		t.Errorf("the example should show a local and a remote Ollama: %+v", c.Providers)
	}
	tags := map[string]bool{}
	for _, m := range c.Models {
		for _, tag := range m.Tags {
			tags[tag] = true
		}
	}
	if !tags["chat"] || !tags["review"] {
		t.Errorf("tags = %v", tags)
	}
}

const openaiHeader = `providers:
  - id: or
    type: openai
    base_url: https://openrouter.ai/api/v1
    api_key_from: env:OPENROUTER_API_KEY
  - id: local
    type: openai
    base_url: http://localhost:8080/v1
    api_key_from: ""
  - id: idle
    type: openai
    base_url: https://api.openai.com/v1
    api_key_from: env:OPENAI_API_KEY
models:
  - {id: remote, provider: or, model: vendor/m, tags: [chat], priority: 1}
  - {id: self, provider: local, model: m, tags: [chat], priority: 1}
`

func TestAnOpenAIProviderWithAndWithoutAKeyIsValid(t *testing.T) {
	c, err := load(t, openaiHeader)
	if err != nil {
		t.Fatal(err)
	}
	if c.Providers[0].Type != "openai" || c.Providers[0].APIKeyFrom != "env:OPENROUTER_API_KEY" || c.Providers[1].APIKeyFrom != "" {
		t.Errorf("providers = %+v", c.Providers)
	}
}

func TestAnOpenAIProviderNeedsABaseURL(t *testing.T) {
	e := loadError(t, "providers:\n  - id: or\n    type: openai\n    api_key_from: \"\"\nmodels:\n  - {id: m, provider: or, model: x, tags: [chat], priority: 1}\n")
	if !strings.Contains(e.Error(), "base_url") {
		t.Errorf("err = %v", e)
	}
}

func TestCredentialsInAnOpenAIBaseURLAreRejected(t *testing.T) {
	e := loadError(t, "providers:\n  - id: or\n    type: openai\n    api_key_from: \"\"\n    base_url: https://user:pw@x.test/v1\nmodels:\n  - {id: m, provider: or, model: x, tags: [chat], priority: 1}\n")
	finding(t, e, "providers.0.base_url")
}

const anthropicModel = "models:\n  - {id: m, provider: claude, model: x, tags: [chat], priority: 1}\n"

func TestAnAnthropicProviderIsValidWithoutABaseURL(t *testing.T) {
	c, err := load(t, "providers:\n  - id: claude\n    type: anthropic\n    api_key_from: env:ANTHROPIC_API_KEY\n"+anthropicModel)
	if err != nil {
		t.Fatal(err)
	}
	if p := c.Providers[0]; p.Type != "anthropic" || p.BaseURL != "" || p.APIKeyFrom != "env:ANTHROPIC_API_KEY" {
		t.Errorf("provider = %+v", p)
	}
	if _, err := load(t, "providers:\n  - id: claude\n    type: anthropic\n    base_url: https://proxy.test\n    api_key_from: env:ANTHROPIC_API_KEY\n"+anthropicModel); err != nil {
		t.Errorf("a base_url is allowed: %v", err)
	}
}

func TestAnAnthropicProviderWithoutACredentialIsValid(t *testing.T) {
	c, err := load(t, "providers:\n  - id: claude\n    type: anthropic\n    api_key_from: \"\"\n"+anthropicModel)
	if err != nil {
		t.Fatalf("a missing credential is never a configuration error: %v", err)
	}
	if !c.Providers[0].CredentialRequired() {
		t.Error("anthropic requires a credential to be usable")
	}
}

func TestCredentialsInAnAnthropicBaseURLAreRejected(t *testing.T) {
	e := loadError(t, "providers:\n  - id: claude\n    type: anthropic\n    base_url: https://user:pw@x.test\n    api_key_from: env:ANTHROPIC_API_KEY\n"+anthropicModel)
	finding(t, e, "providers.0.base_url")
}

func TestABaseURLWithAQueryOrFragmentIsRefusedAndAMalformedOneIsNeverEchoed(t *testing.T) {
	data := `providers:
  - {id: a, type: ollama, base_url: "http://h:11434/v1?key=SECRET-QUERY", api_key_from: ""}
  - {id: b, type: ollama, base_url: "http://h:11434/v1#SECRET-FRAGMENT", api_key_from: ""}
  - {id: c, type: ollama, base_url: "http://h:11434/v1?", api_key_from: ""}
  - {id: d, type: ollama, base_url: "http://h:bad-port/v1/SECRET-PATH", api_key_from: ""}
models:
  - {id: m, provider: a, model: m, tags: [x], priority: 1}
`
	e := loadError(t, data)
	for path, text := range map[string]string{
		"providers.0.base_url": "must not contain a query string or a fragment",
		"providers.1.base_url": "must not contain a query string or a fragment",
		"providers.2.base_url": "must not contain a query string or a fragment",
		"providers.3.base_url": "base_url is not a valid URL",
	} {
		if f := finding(t, e, path); !strings.Contains(f.Message, text) {
			t.Errorf("%s: %q does not contain %q", path, f.Message, text)
		}
	}
	for _, secret := range []string{"SECRET-QUERY", "SECRET-FRAGMENT", "SECRET-PATH", "bad-port"} {
		if strings.Contains(e.Error(), secret) {
			t.Errorf("the findings echo %q: %s", secret, e)
		}
	}
}

func TestPriorityZeroAndTagsWithDigitsAndHyphensAreAccepted(t *testing.T) {
	data := strings.Replace(valid, "priority: 20", "priority: 0", 1)
	data = strings.Replace(data, "tags: [chat, review]", "tags: [chat, gpu-24gb, a1]", 1)
	if _, err := load(t, data); err != nil {
		t.Fatal(err)
	}
}

const embeddingFile = `providers:
  - id: laptop
    type: ollama
    base_url: http://localhost:11434
    api_key_from: ""
  - id: remote
    type: openai
    base_url: https://api.example.com/v1
    api_key_from: env:KEY
  - id: claude
    type: anthropic
    api_key_from: env:CLAUDE
models:
  - id: embed-laptop
    provider: laptop
    model: bge-m3
    tags: [embed]
    priority: 10
    embedding_space: bge-m3
    dimension: 1024
  - id: embed-remote
    provider: remote
    model: text-embedding-3-small
    tags: [embed]
    priority: 20
    embedding_space: openai-small
    dimension: 1536
`

func TestEmbeddingModelsCarryASpaceAndADimension(t *testing.T) {
	c, err := load(t, embeddingFile)
	if err != nil {
		t.Fatal(err)
	}
	if got := c.Models[0]; !got.IsEmbedding() || got.EmbeddingSpace != "bge-m3" || got.Dimension != 1024 {
		t.Errorf("model = %+v, want an embedding model of space bge-m3 and dimension 1024", got)
	}
	if c.Models[0].EmbeddingSpace == c.Models[1].EmbeddingSpace {
		t.Error("the two models should be in different spaces")
	}
	c2, err := load(t, valid)
	if err != nil {
		t.Fatal(err)
	}
	if c2.Models[0].IsEmbedding() {
		t.Error("a model without embedding_space is not an embedding model")
	}
}

func TestEmbeddingSpaceAndDimensionComeTogetherAndFitAProvider(t *testing.T) {
	cases := []struct{ name, change, want string }{
		{"space without dimension", "    embedding_space: bge-m3\n", "has embedding_space but no dimension"},
		{"dimension without space", "    dimension: 1024\n", "has dimension but no embedding_space"},
		{"bad space name", "    embedding_space: BGE_M3\n    dimension: 4\n", "embedding_space"},
		{"zero dimension", "    embedding_space: bge-m3\n    dimension: 0\n", "dimension"},
		{"negative dimension", "    embedding_space: bge-m3\n    dimension: -3\n", "dimension"},
	}
	for _, c := range cases {
		doc := "providers:\n  - id: laptop\n    type: ollama\n    base_url: http://localhost:11434\n    api_key_from: \"\"\nmodels:\n  - id: m\n    provider: laptop\n    model: x\n    tags: [embed]\n    priority: 1\n" + c.change
		_, err := load(t, doc)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to contain %q", c.name, err, c.want)
		}
	}
	// A provider type with no embeddings endpoint is refused, naming model, provider and type.
	doc := strings.Replace(embeddingFile, "  - id: embed-remote\n    provider: remote", "  - id: embed-remote\n    provider: claude", 1)
	_, err := load(t, doc)
	if err == nil || !strings.Contains(err.Error(), `model "embed-remote" is an embedding model, but provider "claude" has type "anthropic"`) {
		t.Errorf("err = %v, want the anthropic embedding model refused with a clear message", err)
	}
}
