package modelrouter

import (
	"context"
	"errors"

	"k8s-lab/shared/router"
	"knowledge/internal/embeddings"
)

// Embedder produces embeddings through the router, from models of one embedding
// space: the router falls back only between models of that space, so the vector
// store never mixes vector spaces. It has no dependency on the vector store; the
// store takes it as an embeddings.Embedder.
type Embedder struct {
	router    *router.Router
	space     string
	dimension int
}

var _ embeddings.Embedder = (*Embedder)(nil)

// NewEmbedder returns an embedder for the space whose vectors have dimension
// numbers. r may be nil for a process that only needs Model and Dimension
// (`migrate` builds the collection from them and embeds nothing); Embed then
// returns an error.
func NewEmbedder(r *router.Router, space string, dimension int) *Embedder {
	return &Embedder{router: r, space: space, dimension: dimension}
}

// Dimension is the length of every vector.
func (e *Embedder) Dimension() int { return e.dimension }

// Model is the name of the vector space; the store keeps it with every item.
func (e *Embedder) Model() string { return e.space }

// Embed returns the embedding of text from the best available model of the
// space. When every model fails the error lists each one and why.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float64, error) {
	if e.router == nil {
		return nil, errors.New("embeddings: no model router (models.yaml is not loaded in this process)")
	}
	resp, err := e.router.Embed(WithKind(ctx, KindEmbed), router.EmbedRequest{Space: e.space, Input: text})
	if err != nil {
		return nil, err
	}
	out := make([]float64, len(resp.Vector))
	for i, x := range resp.Vector {
		out[i] = float64(x)
	}
	return out, nil
}
