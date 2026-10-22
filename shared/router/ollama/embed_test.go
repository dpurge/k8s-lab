package ollama

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"k8s-lab/shared/modelcfg"
	"k8s-lab/shared/router"
)

const embedReply = `{"model":"bge-m3","embeddings":[[0.5,0.25,0.125]],"total_duration":123,"load_duration":4,"prompt_eval_count":9}`

var embedModel = modelcfg.Model{ID: "bge", Provider: "p", Name: "bge-m3", EmbeddingSpace: "bge-m3", Dimension: 3}

func embedWith(t *testing.T, p *Provider, prov modelcfg.Provider, req router.EmbedRequest) (router.EmbedResponse, error) {
	t.Helper()
	if req.Space == "" {
		req.Space = "bge-m3"
	}
	return p.Embed(context.Background(), prov, embedModel, req)
}

func jsonReply(body string, status int) func(w http.ResponseWriter, r *http.Request) {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	}
}

func TestEmbedPostsTheModelAndTheInputAndReadsTheVector(t *testing.T) {
	f := newFake(t, jsonReply(embedReply, 200))
	prov := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: f.URL + "/", APIKeyFrom: ""}
	resp, err := embedWith(t, New(), prov, router.EmbedRequest{Input: "some text"})
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vector) != 3 || resp.Vector[0] != 0.5 || resp.InputTokens != 9 {
		t.Errorf("resp = %+v, want the 3-number vector and 9 tokens (Ollama's prompt_eval_count)", resp)
	}
	got := f.last(t)
	if got.Path != "/api/embed" || got.Auth != "" || got.Fields["model"] != "bge-m3" || got.Fields["input"] != "some text" || len(got.Fields) != 2 {
		t.Errorf("request = %+v, want /api/embed with only the model and the input and no Authorization", got)
	}
}

func TestEmbedSendsTheBearerKeyAndHeadersAndNeverEchoesTheKey(t *testing.T) {
	var team string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		team = r.Header.Get("X-Team")
		if r.Header.Get("Authorization") != "Bearer sk-embed-secret" {
			jsonReply(`{"error":"unauthorized"}`, 401)(w, r)
			return
		}
		jsonReply(embedReply, 200)(w, r)
	}))
	defer srv.Close()
	prov := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: srv.URL, APIKeyFrom: "env:K", Headers: map[string]string{"X-Team": "search"}}
	withKey := New(WithGetenv(func(string) string { return " sk-embed-secret\n" }))
	if _, err := embedWith(t, withKey, prov, router.EmbedRequest{Input: "x"}); err != nil {
		t.Fatalf("with the key: %v", err)
	}
	if team != "search" {
		t.Errorf("X-Team = %q, want search", team)
	}
	wrong := New(WithGetenv(func(string) string { return "sk-wrong" }))
	_, err := embedWith(t, wrong, prov, router.EmbedRequest{Input: "x"})
	if f := failureOf(t, err); f.Kind != router.FailClient || f.HTTPStatus != 401 || strings.Contains(err.Error(), "sk-wrong") {
		t.Errorf("a wrong key: err = %v, want a client failure that does not echo the key", err)
	}
}

func TestEmbedRefusesAnEmptyInputAndAMissingCredentialWithoutCalling(t *testing.T) {
	f := newFake(t, jsonReply(embedReply, 200))
	prov := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: f.URL, APIKeyFrom: "env:K"}
	if _, err := embedWith(t, New(WithGetenv(func(string) string { return "" })), prov, router.EmbedRequest{Input: "x"}); failureOf(t, err).Kind != router.FailClient {
		t.Errorf("a missing credential: err = %v, want a client failure", err)
	}
	prov.APIKeyFrom = ""
	if _, err := embedWith(t, New(), prov, router.EmbedRequest{Input: " \n"}); failureOf(t, err).Kind != router.FailClient {
		t.Errorf("an empty input: err = %v, want a client failure", err)
	}
	if len(f.requests) != 0 {
		t.Errorf("%d requests were sent, want none", len(f.requests))
	}
}

func TestEmbedStatusRepliesAreClassified(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   router.FailureKind
		text   string
	}{
		{404, `{"error":"model \"bge-m3\" not found, try pulling it first"}`, router.FailClient, "not found, try pulling it first"},
		{429, `{"error":"busy"}`, router.FailRateLimited, "busy"},
		{500, `boom`, router.FailServer, "boom"},
		{503, `{"error":"loading"}`, router.FailServer, "loading"},
	} {
		f := newFake(t, jsonReply(c.body, c.status))
		prov := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: f.URL, APIKeyFrom: ""}
		_, err := embedWith(t, New(), prov, router.EmbedRequest{Input: "x"})
		got := failureOf(t, err)
		if got.Kind != c.want || got.HTTPStatus != c.status || !strings.Contains(err.Error(), c.text) {
			t.Errorf("status %d: failure = %+v (%v), want kind %s mentioning %q", c.status, got, err, c.want, c.text)
		}
	}
}

func TestEmbedRepliesThatAreNotOneVectorAreServerFailures(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"no vectors": `{"embeddings":[]}`,
		"two":        `{"embeddings":[[1],[2]]}`,
	} {
		f := newFake(t, jsonReply(body, 200))
		prov := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: f.URL, APIKeyFrom: ""}
		_, err := embedWith(t, New(), prov, router.EmbedRequest{Input: "x"})
		if failureOf(t, err).Kind != router.FailServer {
			t.Errorf("%s: err = %v, want a server failure", name, err)
		}
	}
}

func TestEmbedTransportFailures(t *testing.T) {
	f := newFake(t, jsonReply(embedReply, 200))
	prov := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: f.URL, APIKeyFrom: ""}
	f.Close() // nothing listens any more: connection refused
	if _, err := embedWith(t, New(), prov, router.EmbedRequest{Input: "x"}); failureOf(t, err).Kind != router.FailConnection {
		t.Errorf("a closed server: err = %v, want a connection failure", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	slowProv := modelcfg.Provider{ID: "p", Type: "ollama", BaseURL: slow.URL, APIKeyFrom: ""}
	_, err := embedWith(t, New(), slowProv, router.EmbedRequest{Input: "x", Timeout: 40 * time.Millisecond})
	if failureOf(t, err).Kind != router.FailTimeout {
		t.Errorf("a slow server: err = %v, want a timeout failure", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New().Embed(ctx, slowProv, embedModel, router.EmbedRequest{Space: "bge-m3", Input: "x"})
	if failureOf(t, err).Kind != router.FailCanceled {
		t.Errorf("a cancelled context: err = %v, want a canceled failure", err)
	}
}
