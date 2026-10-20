package modelcfg

import (
	"strings"
	"testing"
)

func loadYAML(t *testing.T, body string) (*Config, error) {
	t.Helper()
	return LoadBytes("m.yaml", []byte(body))
}

const demoModel = "models:\n  - {id: m, provider: d, model: x, tags: [t], priority: 1}\n"

func TestADemoProviderNeedsNoAddress(t *testing.T) {
	cfg, err := loadYAML(t, "providers:\n  - {id: d, type: demo}\n"+demoModel)
	if err != nil || cfg.Providers[0].Type != "demo" || cfg.Providers[0].BaseURL != "" {
		t.Errorf("%+v %v", cfg, err)
	}
}

func TestADemoProviderWithAnAddressIsRefused(t *testing.T) {
	_, err := loadYAML(t, "providers:\n  - {id: d, type: demo, base_url: \"http://x\"}\n"+demoModel)
	if err == nil || !strings.Contains(err.Error(), "demo provider takes no base_url") || !strings.Contains(err.Error(), "m.yaml:2") {
		t.Errorf("err = %v", err)
	}
}

func TestAnOllamaProviderStillNeedsItsAddress(t *testing.T) {
	_, err := loadYAML(t, "providers:\n  - {id: d, type: ollama}\n"+demoModel)
	if err == nil || !strings.Contains(err.Error(), "base_url") {
		t.Errorf("err = %v", err)
	}
}

func TestAnUnknownProviderTypeIsRefused(t *testing.T) {
	_, err := loadYAML(t, "providers:\n  - {id: d, type: telepathy}\n"+demoModel)
	if err == nil || !strings.Contains(err.Error(), "type") {
		t.Errorf("err = %v", err)
	}
}
