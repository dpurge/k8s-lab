package server

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/anthropic"
	"k8s-lab/shared/router/ollama"

	"phraseforge/internal/ai"
	"phraseforge/internal/config"
)

// Distinctive values in the test models configuration, so a test can check
// that the admin API never reveals where a model is or how it is authenticated.
const (
	testBaseURL   = "http://distinctive-host.invalid:48121"
	testAPIKeyEnv = "PF_DISTINCTIVE_API_KEY_VARIABLE"
	// testAPIKeyFile is a credential file that does not exist, so the provider
	// that names it is unusable (reason missing_credentials).
	testAPIKeyFile = "/distinctive-secrets-dir/pf-credential-file"
	// Custom headers: a name, a literal value, and an environment variable and
	// a file that supply more; none may ever appear in an API response.
	testHeaderName     = "X-Distinctive-Header-Name"
	testHeaderLiteral  = "distinctive-literal-header-value"
	testHeaderEnv      = "PF_DISTINCTIVE_HEADER_VARIABLE"
	testHeaderFile     = "/distinctive-secrets-dir/pf-header-file"
	testHeaderFileName = "X-Distinctive-File-Header"
	testHeaderEnvName  = "X-Distinctive-Env-Header"
)

// testAI is an ai.Service with an Ollama provider, "ollama", and an Anthropic
// provider, "claude", whose credential file is missing, and three models
// (tags: default; default and big; vision, the last on "claude" and so
// unavailable), as the admin API lists models and
// validates model tags from the models configuration. No test here calls a
// model, so the address is never used.
func testAI(t testing.TB, db *pgxpool.Pool) *ai.Service {
	t.Helper()
	models, err := modelcfg.LoadBytes("models.yaml", []byte(`
providers:
  - id: ollama
    type: ollama
    base_url: `+testBaseURL+`
    api_key_from: env:`+testAPIKeyEnv+`
    headers:
      `+testHeaderName+`: `+testHeaderLiteral+`
  - id: claude
    type: anthropic
    api_key_from: file:`+testAPIKeyFile+`
    headers:
      `+testHeaderEnvName+`: env:`+testHeaderEnv+`
      `+testHeaderFileName+`: file:`+testHeaderFile+`
models:
  - id: test-model
    provider: ollama
    model: test-model
    tags: [default]
    priority: 10
    options:
      num_ctx: 32768
  - id: big-model
    provider: ollama
    model: big-model-name
    tags: [default, big]
    priority: 20
    options:
      num_ctx: 32768
  - id: vision-model
    provider: claude
    model: vision-model-name
    tags: [vision]
    priority: 5
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": ollama.New(), "anthropic": anthropic.New()},
		router.WithAvailability(router.CredentialAvailability(nil, nil)))
	if err != nil {
		t.Fatal(err)
	}
	svc := ai.New(db, config.Config{}, nil, nil, nil, nil)
	svc.SetRouter(r, models)
	return svc
}
