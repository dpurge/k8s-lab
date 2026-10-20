package generate

import (
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/ollama"

	"phraseforge/internal/ai"
	"phraseforge/internal/config"
)

// testAI is an ai.Service with one Ollama provider; no test here calls a model.
func testAI(t testing.TB, db *pgxpool.Pool) *ai.Service {
	t.Helper()
	models, err := modelcfg.LoadBytes("models.yaml", []byte(`
providers:
  - id: ollama
    type: ollama
    base_url: http://127.0.0.1:1
    api_key_from: ""
models:
  - id: test-model
    provider: ollama
    model: test-model
    tags: [default]
    priority: 10
    options:
      num_ctx: 32768
`))
	if err != nil {
		t.Fatal(err)
	}
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": ollama.New()})
	if err != nil {
		t.Fatal(err)
	}
	svc := ai.New(db, config.Config{}, nil, nil, nil, nil)
	svc.SetRouter(r, models)
	return svc
}
