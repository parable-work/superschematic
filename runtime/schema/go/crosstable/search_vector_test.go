package crosstable

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// TestASearchByVectorAlone (#740): a query with no term to hold and a
// vector ranks the index rows by similarity alone, its excluded terms
// kept out, and needs embeddings to say the vector's model; with no
// vector it finds nothing without a statement.
func TestASearchByVectorAlone(t *testing.T) {
	caller := Caller{Principal: access.Principal{Subject: "00000000-0000-4000-8000-0000000000a1", Kind: access.KindUser}}
	embeddings := SearchOptions{Embeddings: &Embeddings{Model: "m1"}}
	pg := &recordHost{defs: dialectDefinitions}
	got, err := SearchTables(context.Background(), pg.host(), caller, SearchRequest{Q: "-draft", Vector: []float64{3, 4}}, embeddings)
	if err != nil {
		t.Fatal(err)
	}
	if !got.Reranked {
		t.Errorf("a search by vector alone says it did not rank by the vector")
	}
	holdsWords(t, "a search by vector alone", pg.statements, []string{
		"ORDER BY h.similarity DESC, h.source_table, h.source_key",
		"NOT (",
		"@@ (phraseto_tsquery('simple'::regconfig, $1::text))",
		"si.embedding_model = $",
	}, []string{"ts_rank_cd", "ts_headline"})
	args := pg.args[0]
	if v := args[len(args)-2]; fmt.Sprint(v) != "[0.6 0.8]" {
		t.Errorf("the vector is bound as %v, want it of unit length", v)
	}
	if _, err := SearchTables(context.Background(), pg.host(), caller, SearchRequest{Vector: []float64{1, 0}}, SearchOptions{}); !errors.As(err, new(Invalid)) {
		t.Errorf("a vector with no embeddings: %v, want Invalid", err)
	}
	pg.statements = nil
	if got, err := SearchTables(context.Background(), pg.host(), caller, SearchRequest{Q: "-draft"}, embeddings); err != nil || got.Total != 0 || len(pg.statements) != 0 {
		t.Errorf("excluded terms alone: %+v, %v, %d statements", got, err, len(pg.statements))
	}
}
