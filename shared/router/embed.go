package router

import (
	"context"
	"errors"
	"fmt"
	"time"

	"k8s-lab/shared/modelcfg"
)

// EmbedRequest asks for the embedding of one text in one vector space.
type EmbedRequest struct {
	// Space is the embedding_space the vector must be in: only models of that
	// space are tried, never another, because vectors of different spaces cannot
	// be compared (not even when they have the same length).
	Space string
	Input string
	// Timeout, when set, replaces the model's overall timeout for the call.
	Timeout time.Duration
}

// EmbedResponse is an embedding.
type EmbedResponse struct {
	Vector      []float32
	InputTokens int
	// ModelID is the id of the model that answered.
	ModelID string
}

// EmbeddingProvider is a Provider that can also make embeddings. Only models
// whose provider type implements it may carry an embedding_space.
type EmbeddingProvider interface {
	Provider
	Embed(ctx context.Context, p modelcfg.Provider, m modelcfg.Model, req EmbedRequest) (EmbedResponse, error)
}

// Embed returns the embedding of req.Input from the best available model of
// req.Space, falling back to the next model of that space when one fails; it
// shares selection, the breaker, attempts and logs with Complete. A reply whose
// vector is not as long as the model's dimension is a failed attempt, never
// returned. When every model of the space fails, or none is available, the
// error says that no model of another space is tried.
func (r *Router) Embed(ctx context.Context, req EmbedRequest) (EmbedResponse, error) {
	if req.Space == "" {
		return EmbedResponse{}, errors.New("an embedding call needs the embedding space")
	}
	var resp EmbedResponse
	winner, err := r.route(ctx, callSpec{
		kind: AttemptEmbed,
		tags: []string{"embedding_space=" + req.Space},
		eligible: func(m modelcfg.Model) string {
			if m.EmbeddingSpace != req.Space {
				return fmt.Sprintf("is not an embedding model of space %q", req.Space)
			}
			return ""
		},
		invoke: func(ctx context.Context, prov modelcfg.Provider, m modelcfg.Model) (int, int, error) {
			var err error
			resp, err = r.providers[prov.Type].(EmbeddingProvider).Embed(ctx, prov, m, req)
			if err != nil {
				return 0, 0, err
			}
			if len(resp.Vector) != m.Dimension {
				got := len(resp.Vector)
				resp = EmbedResponse{}
				return 0, 0, &Failure{Kind: FailClient, Err: fmt.Errorf("model %s returned a vector of %d numbers, but its dimension is %d", m.ID, got, m.Dimension)}
			}
			return resp.InputTokens, 0, nil
		},
	})
	if err != nil {
		var none *NoModelError
		var all *AllFailedError
		if errors.As(err, &none) || errors.As(err, &all) {
			return EmbedResponse{}, fmt.Errorf("%w; no model of another embedding space is tried", err)
		}
		return EmbedResponse{}, err
	}
	resp.ModelID = winner
	return resp, nil
}
