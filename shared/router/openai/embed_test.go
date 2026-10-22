package openai

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

const embedBody = `{"object":"list","data":[{"object":"embedding","index":0,"embedding":[0.1,0.2,0.3]}],"model":"x","usage":{"prompt_tokens":6,"total_tokens":6}}`

var embedModel = modelcfg.Model{ID: "emb", Provider: "or", Name: "text-embedding-3-small", EmbeddingSpace: "openai-small", Dimension: 3}

func embed(t *testing.T, p *Provider, prov modelcfg.Provider, input string) (router.EmbedResponse, error) {
	t.Helper()
	return p.Embed(context.Background(), prov, embedModel, router.EmbedRequest{Space: "openai-small", Input: input})
}

func TestEmbedPostsTheModelAndTheInputWithTheBearerKey(t *testing.T) {
	f := newFake(t, 200, embedBody)
	resp, err := embed(t, New(envWith(" "+secretKey+"\n")), keyed(f), "hello world")
	if err != nil {
		t.Fatal(err)
	}
	if len(resp.Vector) != 3 || resp.Vector[1] != 0.2 || resp.InputTokens != 6 {
		t.Errorf("resp = %+v, want the 3-number vector and 6 input tokens", resp)
	}
	if len(f.reqs) != 1 {
		t.Fatalf("%d requests, want 1", len(f.reqs))
	}
	got := f.reqs[0]
	if got.Method != "POST" || got.Path != "/embeddings" || got.Auth != "Bearer "+secretKey || got.ContentType != "application/json" {
		t.Errorf("request = %+v, want POST /embeddings with the trimmed Bearer key", got)
	}
	if got.Fields["model"] != "text-embedding-3-small" || got.Fields["input"] != "hello world" || len(got.Fields) != 2 {
		t.Errorf("body = %v, want only the model and the input", got.Fields)
	}
}

func TestEmbedSendsNoAuthorizationWithoutACredential(t *testing.T) {
	f := newFake(t, 200, embedBody)
	prov := modelcfg.Provider{ID: "local", Type: "openai", BaseURL: f.URL + "/", APIKeyFrom: ""}
	if _, err := embed(t, New(), prov, "x"); err != nil {
		t.Fatal(err)
	}
	if f.reqs[0].HasAuth || f.reqs[0].Path != "/embeddings" {
		t.Errorf("request = %+v, want no Authorization header and a clean path despite the trailing slash", f.reqs[0])
	}
}

func TestEmbedSendsTheProvidersExtraHeaders(t *testing.T) {
	var gotTeam string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotTeam = r.Header.Get("X-Team")
		_, _ = w.Write([]byte(embedBody))
	}))
	defer srv.Close()
	prov := modelcfg.Provider{ID: "or", Type: "openai", BaseURL: srv.URL, APIKeyFrom: "", Headers: map[string]string{"X-Team": "search"}}
	if _, err := embed(t, New(), prov, "x"); err != nil {
		t.Fatal(err)
	}
	if gotTeam != "search" {
		t.Errorf("X-Team = %q, want search", gotTeam)
	}
}

func TestEmbedRefusesAMissingCredentialAndAnEmptyInputWithoutCalling(t *testing.T) {
	f := newFake(t, 200, embedBody)
	if _, err := embed(t, New(envWith("")), keyed(f), "x"); failure(t, err).Kind != router.FailClient {
		t.Errorf("a missing credential: err = %v, want a client failure", err)
	}
	if _, err := embed(t, New(envWith(secretKey)), keyed(f), "  \n"); failure(t, err).Kind != router.FailClient {
		t.Errorf("an empty input: err = %v, want a client failure", err)
	}
	if len(f.reqs) != 0 {
		t.Errorf("%d requests were sent, want none", len(f.reqs))
	}
}

func TestEmbedStatusRepliesAreClassifiedAndNeverEchoTheKey(t *testing.T) {
	for _, c := range []struct {
		status int
		body   string
		want   router.FailureKind
	}{
		{401, `{"error":{"message":"bad key"}}`, router.FailClient},
		{404, `{"error":{"message":"model not found"}}`, router.FailClient},
		{429, `{"error":{"message":"slow down"}}`, router.FailRateLimited},
		{500, `oops`, router.FailServer},
		{503, `{"error":{"message":"overloaded"}}`, router.FailServer},
	} {
		f := newFake(t, c.status, c.body)
		_, err := embed(t, New(envWith(secretKey)), keyed(f), "x")
		got := failure(t, err)
		if got.Kind != c.want || got.HTTPStatus != c.status || got.RequestID != "req_test_1" {
			t.Errorf("status %d: failure = %+v, want kind %s with the status and request id", c.status, got, c.want)
		}
		if strings.Contains(err.Error(), secretKey) {
			t.Errorf("status %d: the error contains the key: %v", c.status, err)
		}
	}
}

func TestEmbedRepliesThatAreNotOneVectorAreServerFailures(t *testing.T) {
	for name, body := range map[string]string{
		"not json":   `<html>`,
		"no vectors": `{"data":[],"usage":{"prompt_tokens":1}}`,
		"two":        `{"data":[{"embedding":[1]},{"embedding":[2]}]}`,
	} {
		f := newFake(t, 200, body)
		_, err := embed(t, New(envWith(secretKey)), keyed(f), "x")
		if failure(t, err).Kind != router.FailServer {
			t.Errorf("%s: err = %v, want a server failure", name, err)
		}
	}
}

func TestEmbedTransportFailures(t *testing.T) {
	f := newFake(t, 200, embedBody)
	prov := keyed(f)
	f.Close() // nothing listens any more: connection refused
	if _, err := embed(t, New(envWith(secretKey)), prov, "x"); failure(t, err).Kind != router.FailConnection {
		t.Errorf("a closed server: err = %v, want a connection failure", err)
	}
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(300 * time.Millisecond) }))
	defer slow.Close()
	_, err := New().Embed(context.Background(), modelcfg.Provider{ID: "or", Type: "openai", BaseURL: slow.URL, APIKeyFrom: ""}, embedModel,
		router.EmbedRequest{Space: "openai-small", Input: "x", Timeout: 40 * time.Millisecond})
	if failure(t, err).Kind != router.FailTimeout {
		t.Errorf("a slow server: err = %v, want a timeout failure", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err = New().Embed(ctx, modelcfg.Provider{ID: "or", Type: "openai", BaseURL: slow.URL, APIKeyFrom: ""}, embedModel, router.EmbedRequest{Space: "openai-small", Input: "x"})
	if failure(t, err).Kind != router.FailCanceled {
		t.Errorf("a cancelled context: err = %v, want a canceled failure", err)
	}
}
