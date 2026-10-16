// Package models reads models.yaml: the providers the assistant can call and
// the models on them, each with tags and a priority.
package models

import (
	"fmt"
	"net/url"
	"os"
	"sort"
	"strconv"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"assistant/internal/yamlschema"
	"assistant/schemas"
)

// Defaults of the circuit breaker when models.yaml does not say.
const (
	DefaultBreakerFailures            = 3
	DefaultBreakerCooldownSeconds     = 60
	DefaultRateLimitedCooldownSeconds = 30
)

// Config is a validated models.yaml.
type Config struct {
	Providers []Provider `yaml:"providers"`
	Models    []Model    `yaml:"models"`
	Routing   Routing    `yaml:"routing"`

	// root is the parsed document, kept so findings can name a line.
	root *yaml.Node
}

// Provider is a place where models run.
type Provider struct {
	ID      string `yaml:"id"`
	Type    string `yaml:"type"`
	BaseURL string `yaml:"base_url"`
	// Project and Location say where a Vertex AI provider runs.
	Project  string `yaml:"project"`
	Location string `yaml:"location"`
}

// Model is one routable model.
type Model struct {
	ID       string   `yaml:"id"`
	Provider string   `yaml:"provider"`
	Name     string   `yaml:"model"`
	Tags     []string `yaml:"tags"`
	Priority int      `yaml:"priority"`
	Options  Options  `yaml:"options"`
	Price    Price    `yaml:"price"`
}

// Options are Ollama's settings for a model; zero means "not set".
type Options struct {
	NumCtx                   int  `yaml:"num_ctx"`
	NumPredict               int  `yaml:"num_predict"`
	Think                    bool `yaml:"think"`
	FirstTokenTimeoutSeconds int  `yaml:"first_token_timeout_seconds"`
	IdleTimeoutSeconds       int  `yaml:"idle_timeout_seconds"`
	OverallTimeoutSeconds    int  `yaml:"overall_timeout_seconds"`
}

// Price is what a model costs, in US dollars per million tokens.
type Price struct {
	InputPerMTok  float64 `yaml:"input_per_mtok"`
	OutputPerMTok float64 `yaml:"output_per_mtok"`
}

// Cost is the price of a call that used the given tokens.
func (p Price) Cost(inputTokens, outputTokens int) float64 {
	return (float64(inputTokens)*p.InputPerMTok + float64(outputTokens)*p.OutputPerMTok) / 1e6
}

// Routing holds the router's settings.
type Routing struct {
	Breaker Breaker `yaml:"breaker"`
}

// Breaker says when a model that keeps failing is set aside, and for how long.
type Breaker struct {
	Failures                   int `yaml:"failures"`
	CooldownSeconds            int `yaml:"cooldown_seconds"`
	RateLimitedCooldownSeconds int `yaml:"rate_limited_cooldown_seconds"`
}

// Cooldown is how long a model that tripped the breaker stays unavailable.
func (b Breaker) Cooldown() time.Duration { return time.Duration(b.CooldownSeconds) * time.Second }

// RateLimitedCooldown is the same after a rate limit.
func (b Breaker) RateLimitedCooldown() time.Duration {
	return time.Duration(b.RateLimitedCooldownSeconds) * time.Second
}

// Provider returns the provider with the id.
func (c *Config) Provider(id string) (Provider, bool) {
	for _, p := range c.Providers {
		if p.ID == id {
			return p, true
		}
	}
	return Provider{}, false
}

var (
	compileOnce    sync.Once
	compiledSchema *yamlschema.Schema
	compileErr     error
)

func schema() (*yamlschema.Schema, error) {
	compileOnce.Do(func() {
		compiledSchema, compileErr = yamlschema.Compile(schemas.Models, "models.schema.json")
	})
	return compiledSchema, compileErr
}

// Load reads and validates the models file at path.
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read models file %s: %w", path, err)
	}
	return LoadBytes(path, data)
}

// LoadBytes validates data and returns the configuration with the defaults
// filled in. A file that breaks the schema, or that is inconsistent (a model on
// a provider that is not listed, a duplicate id), yields a *yamlschema.Error
// listing every problem with its key path and line, so a Config that is
// returned is always usable.
func LoadBytes(name string, data []byte) (*Config, error) {
	s, err := schema()
	if err != nil {
		return nil, err
	}
	root, err := s.Check(name, data)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := root.Decode(&c); err != nil {
		return nil, fmt.Errorf("decode models file %s: %w", name, err)
	}
	c.root = root
	c.applyDefaults()
	if found := lint(&c); len(found) > 0 {
		return nil, &yamlschema.Error{Name: name, Findings: found}
	}
	return &c, nil
}

func (c *Config) applyDefaults() {
	b := &c.Routing.Breaker
	if b.Failures == 0 {
		b.Failures = DefaultBreakerFailures
	}
	if b.CooldownSeconds == 0 {
		b.CooldownSeconds = DefaultBreakerCooldownSeconds
	}
	if b.RateLimitedCooldownSeconds == 0 {
		b.RateLimitedCooldownSeconds = DefaultRateLimitedCooldownSeconds
	}
}

// lint reports what a schema cannot say: ids that repeat, a model naming a
// provider that is not listed, and a base_url that is not a plain http(s)
// address. Findings are ordered by line, then path.
func lint(c *Config) []yamlschema.Finding {
	var found []yamlschema.Finding
	add := func(message string, segments ...string) {
		found = append(found, yamlschema.Finding{
			Path:    yamlschema.FormatPath(segments),
			Line:    yamlschema.LineOf(c.root, segments),
			Message: message,
		})
	}

	providers := map[string]bool{}
	for i, p := range c.Providers {
		at := strconv.Itoa(i)
		if providers[p.ID] {
			add(fmt.Sprintf("provider id %q is used more than once", p.ID), "providers", at, "id")
		}
		providers[p.ID] = true
		switch p.Type {
		case "demo":
			if p.BaseURL != "" {
				add("a demo provider takes no base_url: it answers from inside the assistant", "providers", at, "base_url")
			}
		case "vertex":
			if p.BaseURL != "" {
				add("a vertex provider takes no base_url: its address follows from its location", "providers", at, "base_url")
			}
		default:
			lintBaseURL(p.BaseURL, func(msg string) { add(msg, "providers", at, "base_url") })
		}
	}

	ids := map[string]bool{}
	for i, m := range c.Models {
		at := strconv.Itoa(i)
		if ids[m.ID] {
			add(fmt.Sprintf("model id %q is used more than once", m.ID), "models", at, "id")
		}
		ids[m.ID] = true
		if !providers[m.Provider] {
			add(fmt.Sprintf("model %q is on provider %q, which is not listed under providers", m.ID, m.Provider), "models", at, "provider")
		}
	}

	sort.SliceStable(found, func(i, j int) bool {
		if found[i].Line != found[j].Line {
			return found[i].Line < found[j].Line
		}
		return found[i].Path < found[j].Path
	})
	return found
}

func lintBaseURL(raw string, report func(string)) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		report(fmt.Sprintf("base_url is not a valid URL: %v", err))
	case u.Scheme != "http" && u.Scheme != "https":
		report("base_url must start with http:// or https://")
	case u.Hostname() == "":
		report("base_url has no host")
	case u.User != nil:
		report("base_url must not contain a user name or password: a models file is not a place for secrets")
	}
}
