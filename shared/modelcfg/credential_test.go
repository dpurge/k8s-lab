package modelcfg

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

const secretValue = "sk-SECRET-VALUE-NEVER-SHOWN"

func fakeEnv(env map[string]string) func(string) string {
	return func(name string) string { return env[name] }
}

func fakeFiles(files map[string]string) func(string) ([]byte, error) {
	return func(path string) ([]byte, error) {
		if content, ok := files[path]; ok {
			return []byte(content), nil
		}
		return nil, errors.New("no such file")
	}
}

func TestResolveCredentialFromAnEnvironmentVariable(t *testing.T) {
	p := Provider{ID: "or", Type: "openai", APIKeyFrom: "env:OR_KEY"}
	c := ResolveCredential(p, fakeEnv(map[string]string{"OR_KEY": "  " + secretValue + "\n"}), nil)
	if !c.Configured || !c.Present || c.Value() != secretValue || c.Source != "env:OR_KEY" {
		t.Errorf("credential = %#v", c)
	}
}

func TestResolveCredentialFromAFileTrimsAndRereadsEveryCall(t *testing.T) {
	files := map[string]string{"/run/key": "first\n"}
	p := Provider{ID: "g", Type: "vertex", APIKeyFrom: "file:/run/key"}
	read := fakeFiles(files)
	if c := ResolveCredential(p, nil, read); c.Value() != "first" || c.Source != "file:/run/key" || !c.Present {
		t.Fatalf("first = %#v", c)
	}
	files["/run/key"] = "\t second \n"
	if c := ResolveCredential(p, nil, read); c.Value() != "second" {
		t.Errorf("a changed file must be re-read: %#v", c)
	}
}

func TestResolveCredentialDistinguishesNotConfiguredFromConfiguredButAbsent(t *testing.T) {
	cases := []struct {
		name           string
		from           string
		env            map[string]string
		files          map[string]string
		configured, ok bool
	}{
		{"empty value", "", nil, nil, false, false},
		{"env unset", "env:K", nil, nil, true, false},
		{"env blank", "env:K", map[string]string{"K": " \n"}, nil, true, false},
		{"env set", "env:K", map[string]string{"K": "v"}, nil, true, true},
		{"file unreadable", "file:/x", nil, nil, true, false},
		{"file empty", "file:/x", nil, map[string]string{"/x": "\n"}, true, false},
		{"file set", "file:/x", nil, map[string]string{"/x": "v"}, true, true},
		{"malformed", "vault:x", nil, nil, true, false},
	}
	for _, tc := range cases {
		c := ResolveCredential(Provider{APIKeyFrom: tc.from}, fakeEnv(tc.env), fakeFiles(tc.files))
		if c.Configured != tc.configured || c.Present != tc.ok {
			t.Errorf("%s: configured=%t present=%t, want %t %t", tc.name, c.Configured, c.Present, tc.configured, tc.ok)
		}
		if !c.Present && c.Value() != "" {
			t.Errorf("%s: an absent credential has no value", tc.name)
		}
	}
}

func TestACredentialNeverPrintsItsValue(t *testing.T) {
	c := ResolveCredential(Provider{APIKeyFrom: "env:K"}, fakeEnv(map[string]string{"K": secretValue}), nil)
	for _, out := range []string{fmt.Sprint(c), fmt.Sprintf("%v %+v %#v %s", c, c, c, c)} {
		if strings.Contains(out, secretValue) {
			t.Errorf("printed the value: %s", out)
		}
	}
	_, err := ResolveCredential(Provider{ID: "p", APIKeyFrom: "env:K"}, fakeEnv(nil), nil).Require("p")
	if err == nil || !strings.Contains(err.Error(), "env:K") || !strings.Contains(err.Error(), `"p"`) {
		t.Errorf("Require error = %v", err)
	}
	if _, err := (Credential{}).Require("p"); err == nil || !strings.Contains(err.Error(), "api_key_from") {
		t.Errorf("unconfigured Require error = %v", err)
	}
}

func TestCredentialRequirementPerProviderType(t *testing.T) {
	want := map[string]bool{"ollama": false, "openai": false, "anthropic": true, "vertex": true, "demo": false}
	for typ, required := range want {
		p := Provider{Type: typ}
		if p.CredentialRequired() != required {
			t.Errorf("%s: CredentialRequired = %t, want %t", typ, !required, required)
		}
		// Usable without a credential only when none is required.
		if got := CredentialUsable(p, fakeEnv(nil), fakeFiles(nil)); got != !required {
			t.Errorf("%s: usable without a credential = %t", typ, got)
		}
		p.APIKeyFrom = "env:K"
		if !CredentialUsable(p, fakeEnv(map[string]string{"K": "v"}), nil) {
			t.Errorf("%s: usable with a credential present", typ)
		}
		// A configured but absent credential makes a requiring provider unusable.
		if got := CredentialUsable(p, fakeEnv(nil), nil); got != !required {
			t.Errorf("%s: usable with an absent credential = %t", typ, got)
		}
	}
}

// providerDoc is one provider of the type, whose api_key_from line is the given
// text (a whole line, or empty to leave the key out).
func providerDoc(typ, keyLine string) string {
	body := map[string]string{
		"ollama":    "base_url: http://localhost:11434",
		"openai":    "base_url: https://x.test/v1",
		"anthropic": "",
		"vertex":    "project: p\n    location: us-central1",
	}[typ]
	doc := "providers:\n  - id: p\n    type: " + typ + "\n    " + body + "\n"
	if keyLine != "" {
		doc += "    " + keyLine + "\n"
	}
	return doc + "models:\n  - {id: m, provider: p, model: x, tags: [chat], priority: 1}\n"
}

var credentialTypes = []string{"ollama", "openai", "anthropic", "vertex"}

func TestAMissingApiKeyFromIsAFindingOnEveryTypeNamingTheProviderAndTheForms(t *testing.T) {
	for _, typ := range credentialTypes {
		e := loadError(t, providerDoc(typ, ""))
		f := finding(t, e, "providers.0")
		for _, want := range []string{`"p"`, "api_key_from", `""`, "env:NAME", "file:/absolute/path"} {
			if !strings.Contains(f.Message, want) {
				t.Errorf("%s: finding %q lacks %q", typ, f.Message, want)
			}
		}
		if f.Line == 0 {
			t.Errorf("%s: no line", typ)
		}
	}
}

func TestEveryAcceptedFormIsValidOnEveryProviderType(t *testing.T) {
	for _, typ := range credentialTypes {
		for _, value := range []string{`""`, "env:SOME_KEY", "env:_x1", "file:/run/secrets/key"} {
			c, err := load(t, providerDoc(typ, "api_key_from: "+value))
			if err != nil {
				t.Errorf("%s with %s: %v", typ, value, err)
				continue
			}
			if want := strings.Trim(value, `"`); c.Providers[0].APIKeyFrom != want {
				t.Errorf("%s: APIKeyFrom = %q, want %q", typ, c.Providers[0].APIKeyFrom, want)
			}
		}
	}
}

func TestAMalformedApiKeyFromIsAFindingThatNeverEchoesTheValue(t *testing.T) {
	bad := []string{
		"sk-ant-pasted-key", `"env:"`, "env:1BAD", `"env:MY KEY"`, `"env:KEY=value"`,
		"file:relative/key", `"file:"`, `"file: /lead-space"`, "vault:secret", `"ENV:UPPER"`,
	}
	for _, typ := range credentialTypes {
		for _, value := range bad {
			e := loadError(t, providerDoc(typ, "api_key_from: "+value))
			f := finding(t, e, "providers.0.api_key_from")
			if !strings.Contains(f.Message, "api_key_from must be empty, env:NAME or file:/absolute/path") || !strings.Contains(f.Message, `"p"`) || f.Line == 0 {
				t.Errorf("%s %s: %+v", typ, value, f)
			}
			raw := strings.Trim(value, `"`)
			// "env:" and "file:" are part of the guidance text itself.
			if raw != "env:" && raw != "file:" && strings.Contains(e.Error(), raw) {
				t.Errorf("%s: the value %q must not be echoed: %v", typ, raw, e)
			}
		}
	}
}

func TestADemoProviderHasNoApiKeyFrom(t *testing.T) {
	e := loadError(t, "providers:\n  - {id: d, type: demo, api_key_from: \"\"}\n"+demoModel)
	f := finding(t, e, "providers.0.api_key_from")
	if !strings.Contains(f.Message, "demo provider") || !strings.Contains(f.Message, "no api_key_from") {
		t.Errorf("finding = %+v", f)
	}
	if _, err := load(t, "providers:\n  - {id: d, type: demo}\n"+demoModel); err != nil {
		t.Errorf("a demo provider without the key is valid: %v", err)
	}
}

// The two earlier credential keys no longer exist; they are unknown keys, like
// any other.
func TestTheOldApiKeyEnvAndApiKeyFileKeysAreRejectedAsUnknown(t *testing.T) {
	for _, key := range []string{"api_key_env: SOME_KEY", "api_key_file: /run/key"} {
		doc := providerDoc("openai", "api_key_from: \"\"\n    "+key)
		_, err := load(t, doc)
		if err == nil || !strings.Contains(err.Error(), strings.SplitN(key, ":", 2)[0]) {
			t.Errorf("%s: err = %v", key, err)
		}
	}
}

func TestACredentialWithALineBreakOrNULInsideIsNotPresent(t *testing.T) {
	for _, bad := range []string{"abc\ndef", "abc\r\ndef", "abc\rdef", "abc\x00def"} {
		fromFile := ResolveCredential(Provider{ID: "g", Type: "vertex", APIKeyFrom: "file:/k"}, nil, fakeFiles(map[string]string{"/k": bad + "\n"}))
		fromEnv := ResolveCredential(Provider{ID: "g", Type: "vertex", APIKeyFrom: "env:K"}, fakeEnv(map[string]string{"K": bad}), nil)
		for name, c := range map[string]Credential{"file": fromFile, "env": fromEnv} {
			if !c.Configured || c.Present || c.Value() != "" {
				t.Errorf("%s %q: credential = %#v, want configured and absent with no value", name, bad, c)
			}
			if _, err := c.Require("g"); err == nil || strings.Contains(err.Error(), "def") {
				t.Errorf("%s %q: Require must fail without the value: %v", name, bad, err)
			}
		}
		p := Provider{ID: "g", Type: "vertex", APIKeyFrom: "file:/k"}
		if CredentialUsable(p, nil, fakeFiles(map[string]string{"/k": bad})) {
			t.Errorf("%q: a provider that requires a credential is unusable with it", bad)
		}
	}
}

func TestATrailingNewlineInACredentialFileIsStillFine(t *testing.T) {
	c := ResolveCredential(Provider{ID: "g", Type: "vertex", APIKeyFrom: "file:/k"}, nil, fakeFiles(map[string]string{"/k": "tok\r\n"}))
	if !c.Present || c.Value() != "tok" {
		t.Errorf("credential = %#v", c)
	}
}
