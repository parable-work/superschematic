package providers

import (
	"context"
	"errors"

	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"
)

// ErrDoesNotEmbed is SearchEmbeddingsOf's refusal of a model provider
// that implements no ModelEmbedder.
var ErrDoesNotEmbed = errors.New("providers: SearchEmbeddingsOf: the model provider does not embed")

// SearchEmbeddingsOf is search's embeddings over a model provider (D155,
// D159) that embeds, under model, embedding at most maxChars characters of
// a row's text (8000 when zero): what crosstable.SearchTables re-ranks by
// and crosstable.IndexSearch stores. A provider that implements no
// ModelEmbedder is refused at once, as TypeScript's searchEmbeddingsOf
// refuses one with no embed; Go tells one by its type, where Rust asks the
// provider to embed nothing (D187).
func SearchEmbeddingsOf(provider ModelProvider, model string, maxChars int) (*crosstable.Embeddings, error) {
	embedder, ok := provider.(ModelEmbedder)
	if !ok {
		return nil, ErrDoesNotEmbed
	}
	return &crosstable.Embeddings{
		Model:    model,
		MaxChars: maxChars,
		Embedder: crosstable.EmbedderFunc(func(ctx context.Context, texts []string) ([][]float64, error) {
			embedding, err := embedder.Embed(ctx, ModelEmbedRequest{Model: model, Texts: texts})
			if err != nil {
				return nil, err
			}
			return embedding.Vectors, nil
		}),
	}, nil
}
