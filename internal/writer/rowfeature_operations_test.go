package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRowFeatureOperationsRoundTrip: fixture-references-api written in
// each format and read back over its database is the same expanded IR
// (#723). Every format writes the @rowFeatureOperations sets with their
// tables, their options and their clauses, and none writes the
// operations, types or enums the loader fills them with.
func TestRowFeatureOperationsRoundTrip(t *testing.T) {
	db, err := loader.LoadService(tsFixtures + "/fixture-references-db")
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	dependency := loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, fmt.Errorf("no service %s", name)
		}
		return db, nil
	})
	schema, err := loader.LoadService(tsFixtures+"/fixture-references-api", dependency)
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { Note, Ticket } from "@schemas/fixture-references-db";`,
			"@rowFeatureOperations(Ticket)\n@auth\nexport abstract class TicketActions {}\n",
			`@rowFeatureOperations(Note, { operations: ["references", "addReference", "removeReference", "backlinks", "attachments", "beginUpload", "completeUpload", "download", "fileAttachment"], permissions: { beginUpload: ["notes.upload"] } })`,
		},
		FormatJSON: {`"rowFeatureOperations": {`, `"type": "Ticket"`, `"notes.upload"`, `"operations": []`},
		FormatYAML: {"rowFeatureOperations:\n", "type: Ticket", "- notes.upload", "operations: []"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(schema, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-references-db", "kind": "DB"}], "buckets": [{"name": "fixture-references-files", "kind": "Bucket"}], "outputs": {}}`+"\n", schema.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, schema)
			}
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"RowBacklinks", "RowAttachment", "RowBacklinkVia", "ticket_backlinks", "RowAttachmentBegun", "rowFeatureOperation\""} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, dependency)
			if err != nil {
				dumpService(t, dir)
				t.Fatalf("reloading %s output: %v", format, err)
			}
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}
