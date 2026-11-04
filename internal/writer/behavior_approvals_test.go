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

// TestApprovalsRoundTrip (D104): a table that implements Constants,
// StateRules and Approvals is written in every format as its uses, in the
// type's order, `implements Constants<Task, { ... }>, StateRules<Task, {
// ... }>, Approvals<Task, { ... }>` in TypeScript and `{ name, implements:
// true, config }` in the data forms, with oneWay, by, resetOn ([] kept) and
// pinned, without the sub-table and the @optimistic the loader adds for it,
// and reads back to the same IR; the API that serves its behavior
// operations reads back over its database to the same expanded IR.
func TestApprovalsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-approvals-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema.Description = ""
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { Approvals, AutoGenerate, Constants, StateRules, Workflow, key } from "@superschematic/db";`,
			`export abstract class Task implements Constants<Task, { fields: ["owner"]; oneWay: { attempts: "up"; urgent: "up" }; permission: "tasks.admin" }>, StateRules<Task, { states: { planned: { by: ["createdBy"]; frozen: ["plan"] }; proving: { required: ["proof"] }; working: { by: ["reviewer"]; frozen: ["plan"] } } }>, Approvals<Task, { gates: { done: { count: 1; permission: "tasks.review"; pinned: true; resetOn: ["proof"] }; proving: { count: 1; permission: "tasks.review"; resetOn: [] } } }> {`,
		},
		FormatJSON: {`"implements": true`, `"name": "Constants"`, `"name": "StateRules"`, `"name": "Approvals"`, `"resetOn": []`, `"pinned": true`, `"oneWay": {`},
		FormatYAML: {"implements: true", "name: Constants", "name: StateRules", "name: Approvals", "resetOn: []", "pinned: true", "oneWay:\n"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"TaskApprovalsSignOffs", "behaviorTable", "optimistic", "statusEnteredAt", "@behavior"} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
				t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}

	db := schema
	api, err := loader.LoadService(tsFixtures + "/fixture-approvals-api")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	wantAPI := normalizeIR(t, api)
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run("api "+string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(api, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-approvals-db", "kind": "DB"}], "outputs": {}}`+"\n", api.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, api)
			}
			source := writtenSource(t, api, format)
			for _, added := range []string{"approvalsSignOff", "TaskApprovalsGate", "statusTransition", "\"origin\""} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
				if name != db.Name {
					return nil, fmt.Errorf("no service %s", name)
				}
				return db, nil
			}))
			if err != nil {
				dumpService(t, dir)
				t.Fatalf("reloading %s output: %v", format, err)
			}
			if got := normalizeIR(t, reloaded); got != wantAPI {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, wantAPI, got)
			}
		})
	}
}
