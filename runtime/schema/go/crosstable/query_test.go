package crosstable

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// searchTablesParityPath is the query syntax's vectors (#740), which the
// TypeScript runtime writes (SEARCH_TABLES_PARITY_WRITE=1 bun test
// src/search-tables-parity.test.ts) and the Rust runtime runs too.
var searchTablesParityPath = filepath.Join("..", "..", "..", "http", "testdata", "search_tables_parity.json")

type searchTablesParity struct {
	Plans []struct {
		Name     string     `json:"name"`
		Q        string     `json:"q"`
		Plan     SearchPlan `json:"plan"`
		Postgres struct {
			Query string `json:"query"`
			Args  []any  `json:"args"`
		} `json:"postgres"`
		FTS5 *string `json:"fts5"`
	} `json:"plans"`
}

// TestSearchQueryParityVectors runs search_tables_parity.json's plans
// (#740): the plan each query reads as, the Postgres query it renders
// under the simple configuration, and its FTS5 match.
func TestSearchQueryParityVectors(t *testing.T) {
	data, err := os.ReadFile(searchTablesParityPath)
	if err != nil {
		t.Fatal(err)
	}
	var file searchTablesParity
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	if len(file.Plans) == 0 {
		t.Fatal("search_tables_parity.json holds no plans")
	}
	for _, tc := range file.Plans {
		plan := ReadSearchQuery(tc.Q)
		if !reflect.DeepEqual(plan, tc.Plan) {
			t.Errorf("%s: ReadSearchQuery(%q) = %+v, want %+v", tc.Name, tc.Q, plan, tc.Plan)
		}
		var args []any
		query := PostgresQuery(plan, "'simple'::regconfig", func(value any) string {
			args = append(args, value)
			return fmt.Sprintf("$%d", len(args))
		})
		if query != tc.Postgres.Query || fmt.Sprint(args) != fmt.Sprint(tc.Postgres.Args) {
			t.Errorf("%s: PostgresQuery = %s %v, want %s %v", tc.Name, query, args, tc.Postgres.Query, tc.Postgres.Args)
		}
		want := ""
		if tc.FTS5 != nil {
			want = *tc.FTS5
		}
		if got := FTSQuery(plan); got != want {
			t.Errorf("%s: FTSQuery = %q, want %q", tc.Name, got, want)
		}
	}
}
