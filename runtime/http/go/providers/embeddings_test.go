package providers

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type talker struct{}

func (talker) Complete(context.Context, ModelTurn) (ModelCompletion, error) {
	return ModelCompletion{}, errors.New("completes nothing")
}

type embedding struct {
	talker
	asked []ModelEmbedRequest
}

func (e *embedding) Embed(_ context.Context, request ModelEmbedRequest) (ModelEmbedding, error) {
	e.asked = append(e.asked, request)
	out := ModelEmbedding{}
	for range request.Texts {
		out.Vectors = append(out.Vectors, []float64{1, 0})
	}
	return out, nil
}

// TestSearchEmbeddingsOf: a model provider that embeds is search's
// embeddings under the model named, each call made of that model; one
// that embeds nothing is refused at once (D155, D187).
func TestSearchEmbeddingsOf(t *testing.T) {
	model := &embedding{}
	embeddings, err := SearchEmbeddingsOf(model, "stand-in-1", 0)
	if err != nil {
		t.Fatal(err)
	}
	vectors, err := embeddings.Embedder.Embed(context.Background(), []string{"a door", "a hall"})
	if err != nil || !reflect.DeepEqual(vectors, [][]float64{{1, 0}, {1, 0}}) {
		t.Fatalf("Embed = %v, %v", vectors, err)
	}
	if embeddings.Model != "stand-in-1" || len(model.asked) != 1 || model.asked[0].Model != "stand-in-1" {
		t.Errorf("embeddings %q, asked %+v", embeddings.Model, model.asked)
	}
	if _, err := SearchEmbeddingsOf(talker{}, "stand-in-1", 0); !errors.Is(err, ErrDoesNotEmbed) {
		t.Errorf("a provider that embeds nothing: %v", err)
	}
}
