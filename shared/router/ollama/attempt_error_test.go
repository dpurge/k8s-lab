package ollama

import (
	"context"
	"net/http"
	"strings"
	"testing"
	"unicode/utf8"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
	"k8s-lab/shared/router/routertest"
)

// An Ollama that answers with a 1 MB error body must leave a bounded, valid
// UTF-8 error in the attempt the router reports, since rows, the API and the
// logs all take it from there.
func TestAHugeOllamaErrorBodyIsBoundedInTheReportedAttempt(t *testing.T) {
	f := newFake(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(strings.Repeat("é", 500000)))
	})
	cfg, err := modelcfg.LoadBytes("models.yaml", []byte(`providers:
  - {id: box, type: ollama, base_url: "`+f.URL+`", api_key_from: ""}
models:
  - {id: m, provider: box, model: gemma, tags: [chat], priority: 1}
`))
	if err != nil {
		t.Fatal(err)
	}
	observer := &routertest.Observer{}
	r, err := router.NewRouter(cfg, map[string]router.Provider{"ollama": New()}, router.WithObserver(observer))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := r.Complete(context.Background(), router.Request{Input: "{}", Tags: []string{"chat"}}); err == nil {
		t.Fatal("the call must fail")
	}
	attempts := observer.Attempts()
	if len(attempts) != 1 {
		t.Fatalf("attempts = %d", len(attempts))
	}
	got := attempts[0].Error
	if len(got) == 0 || len(got) > 1000+len("…") || !utf8.ValidString(got) || !strings.HasSuffix(got, "…") {
		t.Errorf("Attempt.Error is %d bytes, valid UTF-8 %v", len(got), utf8.ValidString(got))
	}
}
