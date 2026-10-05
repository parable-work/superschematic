package sqlite_test

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/runtime/versiongraph/go/canonical"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/sqlite"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/storage"
)

// TestForeignRows: a live row another writer stored, with whitespace,
// escapes canonical JSON does not write, characters encoding/json would
// escape, a repeated name and numbers in other forms, reads as the
// TypeScript adapter reads it: each member
// written again, its strings as canonical JSON writes them, its numbers as
// their text and its objects' members in their order, a repeated name's
// last value at its first place. The rows want are what the TypeScript
// adapter's rows returned for these stored rows.
func TestForeignRows(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	ref := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
		return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: "main", Actor: cook})
	}))(t)
	for _, c := range []struct {
		kind, key, stored, want string
	}{
		{
			"note", "Note",
			`{"body": "café \/ x é\u0009t\"q\"", "reply_to" : null}`,
			`{"_version":1,"body":"café / x é\tt\"q\"","deleted_on_ref":false,"entity_key":"Note","id":"NoteId","recipe_id":"Bread","ref_id":"REF","reply_to":null}`,
		},
		{
			// A printable character and one outside the Basic Multilingual
			// Plane stored as escapes, a surrogate pair for the latter, read
			// as the characters; <, > and & and the line and paragraph
			// separators read as they are, unescaped.
			"note", "Escaped",
			`{"body": "caf\u00e9 \ud83c\udf5e <&> X` + "\u2028Y\u2029Z" + `", "reply_to": null}`,
			`{"_version":1,"body":"café 🍞 <&> X` + "\u2028Y\u2029Z" + `","deleted_on_ref":false,"entity_key":"Escaped","id":"EscapedId","recipe_id":"Bread","ref_id":"REF","reply_to":null}`,
		},
		{
			"ingredient", "Flour",
			`{ "quantity":"10 g", "step_key":null, "substitutes": [ {"name": "spelt", "ratio": 1.50, "name": "rye"} , {"b":1,"a":-0, "c": 1E5} ] }`,
			`{"_version":1,"deleted_on_ref":false,"entity_key":"Flour","id":"FlourId","quantity":"10 g","recipe_id":"Bread","ref_id":"REF","step_key":null,"substitutes":[{"name":"rye","ratio":1.50},{"b":1,"a":-0,"c":1E5}]}`,
		},
	} {
		if _, err := s.db.Exec(`INSERT INTO "graph_member" (id, graph, kind, entity_key, ref_id, root_id, tombstone, _version, data) VALUES (?1, 'recipe', ?2, ?3, ?4, 'Bread', 0, 1, ?5)`,
			c.key+"Id", c.kind, c.key, ref.ID, c.stored); err != nil {
			t.Fatal(err)
		}
		all := must(in(s.storage, func(ctx context.Context, tx storage.Tx) ([]json.RawMessage, error) {
			return tx.Rows(ctx, c.kind, ref.ID)
		}))(t)
		var rows []json.RawMessage
		for _, row := range all {
			if memberText(t, row, "entity_key") == string(mustJSON(t, c.key)) {
				rows = append(rows, row)
			}
		}
		want := strings.Replace(c.want, `"REF"`, string(mustJSON(t, ref.ID)), 1)
		if len(rows) != 1 || string(rows[0]) != want {
			t.Fatalf("the stored %s row %s reads as\n%s\nwhere the TypeScript adapter reads\n%s", c.kind, c.stored, rows, want)
		}
	}
}

// TestHistoryImageEscapes: a ref's history image writes its name as
// canonical JSON writes a string: a quote and a backslash escaped, a tab
// as \t, another control character as \u00xx in lowercase hex, and text
// outside ASCII as it is.
func TestHistoryImageEscapes(t *testing.T) {
	s := newSetup(t, sqlite.Options{}, "")
	for _, name := range []string{"a \"quoted\"\tname", "back\\slash\u0001\u001f\u007f", "crème 🍞\n\r\b\f"} {
		ref := must(in(s.storage, func(ctx context.Context, tx storage.Tx) (storage.Ref, error) {
			return tx.CreateRef(ctx, storage.NewRef{Root: bread, Name: name, Actor: cook})
		}))(t)
		images := historyOf(t, s.db, `"graph_ref_history"`, ref.ID)
		if len(images) != 1 {
			t.Fatalf("%d images of a new ref", len(images))
		}
		want := must(canonical.Postgres(canonical.String, mustJSON(t, name)))(t)
		if got := memberText(t, images[0].data, "name"); got != string(want) {
			t.Fatalf("the image writes the name %q as %s, where canonical JSON writes %s", name, got, want)
		}
		if !strings.Contains(images[0].data, `"name":`+string(want)+`,`) {
			t.Fatalf("the image %s does not hold the name as canonical JSON writes it, %s", images[0].data, want)
		}
	}
}
