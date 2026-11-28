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

// TestTriageOperationsRoundTrip: fixture-triage-api written in each format
// and read back over its database is the same expanded IR (#746). Every
// format writes the @triageOperations set with its party table, its
// refersTo and its clause, and none writes the operations, types or enums
// the loader fills it with.
func TestTriageOperationsRoundTrip(t *testing.T) {
	db, err := loader.LoadService(tsFixtures + "/fixture-parties-db")
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	dependency := loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, fmt.Errorf("no service %s", name)
		}
		return db, nil
	})
	schema, err := loader.LoadService(tsFixtures+"/fixture-triage-api", dependency)
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { Contact } from "@schemas/fixture-parties-db";`,
			"@triageOperations(Contact, { refersTo: ContactView })\n@requirePermission([\"parties.triage\"])\nexport abstract class Triage {}\n",
		},
		FormatJSON: {`"triageOperations": {`, `"party": "Contact"`, `"refersTo": "ContactView"`, `"operations": []`},
		FormatYAML: {"triageOperations:\n", "party: Contact", "refersTo: ContactView", "operations: []"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(schema, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-parties-db", "kind": "DB"}], "outputs": {}}`+"\n", schema.Name)
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
			for _, added := range []string{"TriageMessage", "TriageSettleInput", "TriageOutcome", "triage_message", "triageOperation\""} {
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
