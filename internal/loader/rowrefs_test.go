package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRowFeaturesExpandIntoSubTables: fixture-references-db's @references
// and @attachments tables each gain a sub-table beside them, a child of
// the row with no repository, and the queues they name gain the fields the
// build fills (D154).
func TestRowFeaturesExpandIntoSubTables(t *testing.T) {
	schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-references-db"))
	if err != nil {
		t.Fatal(err)
	}
	ticket := schema.Types["Ticket"]
	if ticket.RowReferences == nil || ticket.Attachments == nil {
		t.Fatalf("Ticket's declarations = %+v, %+v", ticket.RowReferences, ticket.Attachments)
	}
	if got := ticket.Attachments; got.Released != "AttachmentReleased" || got.MaxFileBytes != 1048576 || got.MaxRowBytes != 4194304 || got.MaxFiles != 10 {
		t.Errorf("Ticket's @attachments = %+v", got)
	}
	if got := schema.Types["Note"].RowReferences.Tables; !slices.Equal(got, []string{"Ticket", "Note"}) {
		t.Errorf("Note's @references tables = %v", got)
	}
	for _, tc := range []struct {
		name, feature string
		fields        []string
	}{
		{"TicketReferences", ir.OriginReferences, []string{"id", "ticket", "targetTable", "targetKey", "role", "addedBy", "addedAt"}},
		{"NoteReferences", ir.OriginReferences, []string{"id", "note", "targetTable", "targetKey", "role", "addedBy", "addedAt"}},
		{"TicketAttachments", ir.OriginAttachments, []string{"id", "ticket", "name", "mediaType", "size", "contentHash", "objectKey", "caption", "state", "uploadedBy", "uploadedAt", "expiresAt", "filedFrom", "extraction", "extractedText", "preview", "previewObject"}},
	} {
		sub := schema.Types[tc.name]
		if sub == nil {
			t.Errorf("no type %s", tc.name)
			continue
		}
		if sub.RowTable == nil || sub.RowTable.Feature != tc.feature || sub.Origin != tc.feature {
			t.Errorf("%s's marker = %+v, origin %q", tc.name, sub.RowTable, sub.Origin)
		}
		var names []string
		for _, fd := range sub.Fields {
			names = append(names, fd.Name)
		}
		if !slices.Equal(names, tc.fields) {
			t.Errorf("%s's fields = %v, want %v", tc.name, names, tc.fields)
		}
	}
	for queue, want := range map[string][]string{
		"ReferenceCleared":   ir.RowReferencesClearedFields,
		"AttachmentReleased": ir.AttachmentsReleasedFields,
	} {
		var names []string
		for _, fd := range schema.Types[queue].Fields {
			names = append(names, fd.Name)
		}
		if !slices.Equal(names, want) {
			t.Errorf("%s's fields = %v, want %v", queue, names, want)
		}
	}
}

// TestRowFeaturesRefuse: what the verification pass refuses of a
// declaration (D154), each in its words.
func TestRowFeaturesRefuse(t *testing.T) {
	for _, tc := range []struct {
		name, schema, want string
	}{
		{"a table that is not the database's", `
import { Identity } from "superscalar";
import { AutoGenerate, key, queue, references } from "@superschematic/db";
@queue()
export abstract class Cleared {}
@references<Ticket>({ tables: [Cleared], cleared: Cleared })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
}`, "@references on Ticket: tables lists Cleared, which is no table of"},
		{"a queue that declares its own fields", `
import { Identity } from "superscalar";
import { AutoGenerate, attachments, key, queue } from "@superschematic/db";
@queue()
export abstract class Released {
  objectKey: string;
}
@attachments<Ticket>({ released: Released })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
}`, "@attachments on Ticket: released names Released, which declares fields of its own"},
		{"a released queue that is no queue", `
import { Identity } from "superscalar";
import { AutoGenerate, attachments, key } from "@superschematic/db";
export abstract class Released {}
@attachments<Ticket>({ released: Released })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
}`, "released names Released, which is no @queue class"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			base := filepath.ToSlash(filepath.Join(testpaths.RepoRoot(t), "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
			dir := writeService(t, map[string]string{
				"schema.config.json":    `{"name": "tickets-db", "kind": "DB", "outputs": {"sql": {"dialects": ["postgres"]}}}`,
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

// TestRowFeaturesOnSQLite: a service that lists the sqlite dialect keeps
// references and files when TypeScript's types alone read them, and is
// refused, naming the language, when it enables Go's or Rust's, whose
// repositories do not serve them on SQLite yet (D182).
func TestRowFeaturesOnSQLite(t *testing.T) {
	const schema = `
import { Identity } from "superscalar";
import { AutoGenerate, attachments, audience, key, queue, references } from "@superschematic/db";
@queue()
export abstract class Released {}
@audience<Ticket>({ owner: "owner" })
@references<Ticket>({})
@attachments<Ticket>({ released: Released })
export abstract class Ticket {
  @key
  id: AutoGenerate<Identity.UUID>;
  owner: Identity.UUID;
}`
	base := filepath.ToSlash(filepath.Join(testpaths.RepoRoot(t), "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
	for _, tc := range []struct {
		name, types string
		want        []string
	}{
		{"TypeScript's types alone", `{"typescript": {"enabled": true}}`, nil},
		{"Go's types too", `{"typescript": {"enabled": true}, "go": {"enabled": true}}`, []string{
			"@audience on Ticket: tickets-db lists the sqlite dialect and enables the Go types",
			"@references on Ticket: tickets-db lists the sqlite dialect and enables the Go types",
			"@attachments on Ticket: tickets-db lists the sqlite dialect and enables the Go types",
		}},
		{"Rust's types", `{"rust": {"enabled": true}}`, []string{"enables the Rust types, whose repositories read Ticket's rows on SQLite without it"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json":    `{"name": "tickets-db", "kind": "DB", "outputs": {"types": ` + tc.types + `, "sql": {"dialects": ["sqlite", "postgres"]}}}`,
				"tsconfig.json":         `{"extends": "` + base + `", "include": ["src/**/*.ts"]}`,
				"src/tickets.schema.ts": schema,
			})
			_, err := LoadService(dir)
			if tc.want == nil {
				if err != nil {
					t.Fatalf("LoadService: %v", err)
				}
				return
			}
			for _, want := range tc.want {
				if err == nil || !strings.Contains(err.Error(), want) {
					t.Errorf("LoadService = %v, want it to hold %q", err, want)
				}
			}
		})
	}
}
