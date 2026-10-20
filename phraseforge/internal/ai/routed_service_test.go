package ai

import (
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/ollama"
)

// modelsAt is a models configuration with one Ollama model, "test-model", at
// baseURL. The breaker never trips (a test that scripts several server errors
// in a row must keep reaching the model); a test of fallback or of the breaker
// writes its own configuration.
func modelsAt(t *testing.T, baseURL string) *modelcfg.Config {
	t.Helper()
	return mustModels(t, `
providers:
  - id: ollama
    type: ollama
    base_url: `+baseURL+`
    api_key_from: ""
models:
  - id: test-model
    provider: ollama
    model: test-model
    tags: [default]
    priority: 10
    options:
      num_ctx: 32768
routing:
  breaker:
    failures: 1000
`)
}

func mustModels(t *testing.T, yaml string) *modelcfg.Config {
	t.Helper()
	models, err := modelcfg.LoadBytes("models.yaml", []byte(yaml))
	if err != nil {
		t.Fatalf("test models configuration: %v", err)
	}
	return models
}

// useModels gives s a router over models whose Ollama providers are real
// ollama.Provider values, so a fake Ollama server (httptest) answers.
func useModels(t *testing.T, s *Service, models *modelcfg.Config, opts ...router.Option) *Service {
	t.Helper()
	r, err := router.NewRouter(models, map[string]router.Provider{"ollama": ollama.New()}, opts...)
	if err != nil {
		t.Fatal(err)
	}
	s.SetRouter(r, models)
	return s
}

// useFakeOllama routes s's calls to the fake Ollama at baseURL.
func useFakeOllama(t *testing.T, s *Service, baseURL string) *Service {
	t.Helper()
	return useModels(t, s, modelsAt(t, baseURL))
}
