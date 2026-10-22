// Package embeddings is the interface knowledge's vector store takes to turn text
// into a vector, plus a deterministic fake for tests and local runs without a
// model. The real implementation is in package modelrouter: it asks the shared
// router for a model of one embedding space, so the store knows nothing about
// providers, and nothing here depends on a particular vector store.
package embeddings

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"math"
	"strings"
)

// Embedder turns text into a vector of Dimension numbers. Model names the
// vector space (the embedding_space of models.yaml): vectors of one Model can be
// compared, vectors of different Models cannot.
type Embedder interface {
	Embed(ctx context.Context, text string) ([]float64, error)
	Dimension() int
	Model() string
}

// Fake is an Embedder that hashes words into a unit vector: the same text always
// gives the same vector, and texts that share words are close. It calls nothing.
type Fake struct {
	model string
	dim   int
}

// NewFake returns a Fake in the vector space named model with dim numbers.
func NewFake(model string, dim int) Fake { return Fake{model: model, dim: dim} }

func (f Fake) Dimension() int { return f.dim }
func (f Fake) Model() string  { return f.model }
func (f Fake) Embed(_ context.Context, text string) ([]float64, error) {
	v := make([]float64, f.dim)
	words := strings.Fields(strings.ToLower(text))
	for _, w := range words {
		h := sha256.Sum256([]byte(w))
		idx := int(binary.BigEndian.Uint32(h[:4]) % uint32(f.dim))
		v[idx] += 1
	}
	n := 0.0
	for _, x := range v {
		n += x * x
	}
	if n > 0 {
		n = math.Sqrt(n)
		for i := range v {
			v[i] /= n
		}
	}
	return v, nil
}
