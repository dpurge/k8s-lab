// Package modelcfg reads models.yaml: the providers the assistant can call and
// the models on them, each with tags and a priority.
//
// # Provider fields, per type
//
//	type       base_url              project, location  api_key_from (compulsory key, value may be empty)  credential needed?        sent as                   headers (optional)
//	ollama     required              not allowed        "", env:NAME or file:/abs/path                      no (proxy or local)       Authorization: Bearer     allowed
//	openai     required              not allowed        "", env:NAME or file:/abs/path                      no (local server)         Authorization: Bearer     allowed
//	anthropic  optional, default     not allowed        "", env:NAME or file:/abs/path                      yes                       x-api-key header          allowed
//	           https://api.anthropic.com
//	vertex     not allowed (follows  both required      "", env:NAME or file:/abs/path                      yes                       Authorization: Bearer     allowed
//	           from location)                                                                                                         (an OAuth access token)
//	demo       not allowed           not allowed        not allowed                                         no                        (answers in-process)      not allowed
//
// api_key_from must appear on every provider entry except demo, so that a
// forgotten credential is a configuration error rather than a silent default;
// writing api_key_from: "" says that there is none. env:NAME reads the
// environment variable NAME, file:/abs/path reads the file, trimmed of
// whitespace. A file: is re-read on every call (ResolveCredential), so a rotated
// secret or a refreshed token needs no restart; an env: value comes from the
// process environment, which a running process does not see change, so a
// changed variable needs a restart. A missing credential is never a
// startup error: a provider that needs one and has none present (empty, unset
// variable, unreadable or empty file) is skipped by the router (reason
// missing_credentials) until it appears; one that does not need one sends no
// credential when none is configured.
//
// # Custom headers
//
// headers: is an optional mapping of header name to value on every provider
// that makes HTTP calls (everything but demo), the same for all of them. A
// value is env:NAME (the environment variable, read from the process
// environment: a change needs a restart), file:/abs/path (the file's trimmed
// content, re-read on every call) or else a
// literal; a literal that must itself start with env: or file: cannot be
// written. They are sent after the provider's own headers and may replace a
// default such as anthropic-version. Names that other settings own are refused
// (authorization and x-api-key: use api_key_from; content-type, content-length,
// host, connection, transfer-encoding); names are case-insensitive and may not
// repeat. An env: or file: header with no value now makes the provider
// unusable, like a missing credential (see ResolveHeaders). Header names and
// values are never shown in an API response, a metric, a record or an error.
//
// # Model options, per type
//
//	option                                     applies to
//	num_ctx, think                             ollama only
//	first_token_timeout_seconds,
//	idle_timeout_seconds                       ollama only (the others do not stream)
//	num_predict (most tokens in a reply)       every type that calls a model
//	overall_timeout_seconds                    every type that calls a model
//
// A provider ignores the options that do not apply to it.
package modelcfg

import (
	_ "embed"
	"fmt"
	"net/url"
	"os"
	pathpkg "path"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"go.yaml.in/yaml/v3"

	"k8s-lab/shared/yamlschema"
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
	// APIKeyFrom says where the provider's API key or access token comes from:
	// empty (no credential), "env:NAME" (the environment variable NAME) or
	// "file:/absolute/path" (the trimmed content of that file). The key must be
	// present in the file for every provider except demo, but its value may be
	// empty. The secret itself never belongs in models.yaml.
	APIKeyFrom string `yaml:"api_key_from"`
	// Headers are extra HTTP headers sent on every request to the provider
	// (not demo): header name -> value. A value is env:NAME (read from the
	// process environment, which a running process does not see change), file:/absolute/path (the trimmed content,
	// re-read on every call) or else a literal; a literal that must start with
	// env: or file: cannot be written. See ResolveHeaders.
	Headers map[string]string `yaml:"headers"`
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

// modelsSchema is the JSON Schema of models.yaml.
//
//go:embed models.schema.json
var modelsSchema []byte

var (
	compileOnce    sync.Once
	compiledSchema *yamlschema.Schema
	compileErr     error
)

func schema() (*yamlschema.Schema, error) {
	compileOnce.Do(func() {
		compiledSchema, compileErr = yamlschema.Compile(modelsSchema, "models.schema.json")
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
		hasKey := providerHasKey(c.root, i, "api_key_from")
		lintCredential(p, hasKey, func(onKey bool, msg string) {
			if onKey {
				add(msg, "providers", at, "api_key_from")
			} else {
				add(msg, "providers", at)
			}
		})
		lintHeaders(p, providerHasKey(c.root, i, "headers"), func(name, msg string) {
			if name == "" {
				add(msg, "providers", at, "headers")
			} else {
				add(msg, "providers", at, "headers", name)
			}
		})
		switch p.Type {
		case "demo":
			if p.BaseURL != "" {
				add("a demo provider takes no base_url: it answers from inside the assistant", "providers", at, "base_url")
			}
		case "vertex":
			if p.BaseURL != "" {
				add("a vertex provider takes no base_url: its address follows from its location", "providers", at, "base_url")
			}
		case "anthropic":
			// base_url is optional: the provider defaults to Anthropic's API.
			if p.BaseURL != "" {
				lintBaseURL(p.BaseURL, func(msg string) { add(msg, "providers", at, "base_url") })
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

// providerHasKey says whether the index-th provider of the document has the
// key, whatever its value: a schema cannot tell an empty value from a missing
// key once decoded.
func providerHasKey(root *yaml.Node, index int, key string) bool {
	node := root
	if node.Kind == yaml.DocumentNode && len(node.Content) > 0 {
		node = node.Content[0]
	}
	for i := 0; i+1 < len(node.Content); i += 2 {
		if node.Content[i].Value != "providers" {
			continue
		}
		list := node.Content[i+1]
		if list.Kind != yaml.SequenceNode || index >= len(list.Content) {
			return false
		}
		entry := list.Content[index]
		for j := 0; j+1 < len(entry.Content); j += 2 {
			if entry.Content[j].Value == key {
				return true
			}
		}
	}
	return false
}

// lintCredential checks api_key_from: it is compulsory (its value may be empty)
// on every provider but demo, which takes none, and must be empty, env:NAME or
// file:/absolute/path. A bad value may be a pasted key and is never echoed.
func lintCredential(p Provider, hasKey bool, report func(onKey bool, message string)) {
	if p.Type == "demo" {
		if hasKey {
			report(true, fmt.Sprintf("provider %q is a demo provider, which takes no api_key_from: it answers from inside the assistant", p.ID))
		}
		return
	}
	if !hasKey {
		report(false, fmt.Sprintf("provider %q has no api_key_from: it is required, though its value may be empty. Use api_key_from: \"\" (no credential), api_key_from: env:NAME or api_key_from: file:/absolute/path", p.ID))
		return
	}
	if _, _, ok := parseKeyFrom(p.APIKeyFrom); !ok {
		report(true, fmt.Sprintf("provider %q: api_key_from must be empty, env:NAME or file:/absolute/path", p.ID))
	}
}

var envNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// keyKind says how api_key_from is read.
type keyKind int

const (
	keyNone keyKind = iota
	keyEnv
	keyFile
)

// parseKeyFrom splits an api_key_from value; ok is false for a malformed one.
func parseKeyFrom(value string) (kind keyKind, arg string, ok bool) {
	switch {
	case value == "":
		return keyNone, "", true
	case strings.HasPrefix(value, "env:"):
		name := strings.TrimPrefix(value, "env:")
		return keyEnv, name, envNamePattern.MatchString(name)
	case strings.HasPrefix(value, "file:"):
		path := strings.TrimPrefix(value, "file:")
		return keyFile, path, path == strings.TrimSpace(path) && !strings.ContainsRune(path, 0) && path != "" && pathpkg.IsAbs(path)
	}
	return keyNone, "", false
}

func lintBaseURL(raw string, report func(string)) {
	u, err := url.Parse(raw)
	switch {
	case err != nil:
		// The parse error quotes the URL, which may hold a key, so it is not echoed.
		report("base_url is not a valid URL")
	case u.Scheme != "http" && u.Scheme != "https":
		report("base_url must start with http:// or https://")
	case u.Hostname() == "":
		report("base_url has no host")
	case u.User != nil:
		report("base_url must not contain a user name or password: a models file is not a place for secrets")
	case strings.ContainsAny(raw, "?#"):
		report("base_url must not contain a query string or a fragment: they can carry keys, and a models file is not a place for secrets")
	}
}
