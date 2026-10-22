package modelrouter

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

func TestModelsListsConfiguredIdsWithAvailabilityAndNeverAnAddressOrUpstreamName(t *testing.T) {
	const doc = `
providers:
  - id: lan
    type: anthropic
    base_url: http://secret-gpu-host.lan:11434
    api_key_from: env:SECRET_KEY_VARIABLE
  - id: local
    type: ollama
    base_url: http://localhost:11434
    api_key_from: ""
models:
  - id: chat-lan
    provider: lan
    model: upstream-secret-name
    tags: [chat]
    priority: 100
  - id: chat-local
    provider: local
    model: gemma4:12b
    tags: [chat]
    priority: 10
    options: { num_ctx: 8192 }
  - id: bge-local
    provider: local
    model: bge-m3
    tags: [embed]
    priority: 10
    embedding_space: bge-m3
    dimension: 3
`
	path := filepath.Join(t.TempDir(), "models.yaml")
	if err := os.WriteFile(path, []byte(doc), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := modelcfg.Load(path)
	if err != nil {
		t.Fatal(err)
	}
	fake := routertest.NewProvider(nil)
	// The LAN provider needs a credential and its key variable is unset, so its
	// model is unavailable; an Ollama provider needs none.
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": fake, "anthropic": fake},
		router.WithAvailability(router.CredentialAvailability(func(string) string { return "" }, os.ReadFile)))
	if err != nil {
		t.Fatal(err)
	}
	got := Models(cfg, r)
	if len(got) != 3 || got[0].ID != "chat-lan" || got[2].ID != "bge-local" {
		t.Fatalf("Models = %+v, want the three models in the order of the file", got)
	}
	if got[0].Available || got[0].UnavailableReason != "missing_credentials" || !got[1].Available || got[1].UnavailableReason != "" {
		t.Errorf("availability = %+v / %+v, want the LAN model held for a missing credential and the local one available", got[0], got[1])
	}
	if got[2].EmbeddingSpace != "bge-m3" || got[1].EmbeddingSpace != "" || got[1].Priority != 10 || got[0].ProviderID != "lan" {
		t.Errorf("models = %+v, want the space only on the embedding model, the priority and the provider id", got)
	}
	b, _ := json.Marshal(got)
	for _, secret := range []string{"secret-gpu-host", "SECRET_KEY_VARIABLE", "upstream-secret-name", "gemma4:12b", "11434", "http"} {
		if strings.Contains(string(b), secret) {
			t.Errorf("the models JSON contains %q: %s", secret, b)
		}
	}
}
