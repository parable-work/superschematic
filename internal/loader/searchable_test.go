package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSearchableExpandsIntoTheIndex: fixture-search-db's @searchable
// tables share one search index the loader adds, with no repository, and
// the queue they name gains the fields the build fills and a key that
// keeps one ready message per row (D155).
func TestSearchableExpandsIntoTheIndex(t *testing.T) {
	schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-search-db"))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, td := range schema.SearchableTables() {
		names = append(names, td.Name)
	}
	if want := []string{"Contract", "Document", "Folder", "Note", "Request", "Unit"}; !slices.Equal(names, want) {
		t.Errorf("searchable tables = %v, want %v", names, want)
	}
	request := schema.Types["Request"].Searchable
	if request.Title != "title" || !slices.Equal(request.Body, []string{"details", "resolution"}) || request.Scope != "unit" || request.Queue != "SearchIndexing" {
		t.Errorf("Request's @searchable = %+v", request)
	}
	index := schema.Types[ir.SearchIndexTypeName]
	if index == nil || !index.SearchIndex || index.Origin != ir.OriginSearch {
		t.Fatalf("the search index = %+v", index)
	}
	var fields []string
	for _, fd := range index.Fields {
		fields = append(fields, fd.Name)
	}
	want := []string{"id", "sourceTable", "sourceKey", "title", "body", "comments", "files", "scope", "audience", "agents", "textHash", "embedding", "embeddingModel", "indexedAt"}
	if !slices.Equal(fields, want) {
		t.Errorf("the index's fields = %v, want %v", fields, want)
	}
	queue := schema.Types["SearchIndexing"]
	fields = nil
	for _, fd := range queue.Fields {
		fields = append(fields, fd.Name)
	}
	if !slices.Equal(fields, ir.SearchIndexingFields) {
		t.Errorf("SearchIndexing's fields = %v", fields)
	}
	if q := queue.Queue; !slices.Equal(q.Keys, ir.SearchIndexingFields) || q.OnDuplicate != ir.QueueOnDuplicateSkip || !q.Serialize {
		t.Errorf("SearchIndexing's key = %+v", q)
	}
}

// TestSearchableRefuses: what the verification pass refuses of a
// declaration (D155), each in its words.
func TestSearchableRefuses(t *testing.T) {
	const head = `
import { Identity } from "superscalar";
import { Nullable } from "@superschematic/schema";
import { AutoGenerate, Encrypted, key, queue, searchable } from "@superschematic/db";
@queue()
export abstract class Indexing {}
`
	for _, tc := range []struct {
		name, schema, dialects, want string
	}{
		{"an encrypted body", head + `
@searchable<Ticket>({ title: "title", body: "secret", queue: Indexing })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
  secret: Nullable<Encrypted<string>>;
}`, "", "@searchable on Ticket: body names secret, which is Encrypted<T>: an index row holds its text in the clear, so an encrypted field is never indexed"},
		{"an encrypted title", head + `
@searchable<Ticket>({ title: "secret", queue: Indexing })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  secret: Encrypted<string>;
}`, "", "@searchable on Ticket: title names secret, which is Encrypted<T>"},
		// The declaration's type names the table's fields, so tsc refuses
		// a field that is not one before the pass would.
		{"a field that is not the table's", head + `
@searchable<Ticket>({ title: "name", queue: Indexing })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}`, "", `Type '"name"' is not assignable to type '"id" | "title"'`},
		{"a title that is no text", head + `
@searchable<Ticket>({ title: "count", queue: Indexing })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  count: number;
}`, "", "@searchable on Ticket: title names count, which holds"},
		{"comments on a table with no conversation", head + `
@searchable<Ticket>({ title: "title", queue: Indexing, comments: true })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}`, "", "comments is true, and Ticket implements no Comments"},
		{"a queue that is no queue", `
import { Identity } from "superscalar";
import { AutoGenerate, key, searchable } from "@superschematic/db";
export abstract class Indexing {}
@searchable<Ticket>({ title: "title", queue: Indexing })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  title: string;
}`, "", "queue names Indexing, which is no @queue class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dialects := tc.dialects
			if dialects == "" {
				dialects = `"postgres"`
			}
			base := filepath.ToSlash(filepath.Join(testpaths.RepoRoot(t), "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
			dir := writeService(t, map[string]string{
				"schema.config.json":    `{"name": "tickets-db", "kind": "DB", "outputs": {"sql": {"dialects": [` + dialects + `]}}}`,
				"tsconfig.json":         `{"extends": "` + base + `", "include": ["src/**/*.ts"]}`,
				"src/tickets.schema.ts": tc.schema,
			})
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want it to hold %q", err, tc.want)
			}
		})
	}
}
