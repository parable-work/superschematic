package writer

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// TestSeatsRoundTrip (D104): a table that implements Seats, and a field
// that is a Reconcile beside a Workflow field, are written in every format
// as their uses, `implements Seats<Problem, { ... }>, StateRules<Problem,
// { ... }>` and `observed: Reconcile<{ ... }>` in TypeScript and `{ name,
// implements: true, config }` and `{ name, field: "observed", config }` in
// the data forms, with seats in a by, without the owned fields, sub-table
// and deadline queue the loader adds for them, and read back to the same
// IR; the API that serves their behavior operations and runs Sandbox's
// deadlines reads back over its database to the same expanded IR.
func TestSeatsRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-seats-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema.Description = ""
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`implements Seats<Problem, {`,
			`accountable: { assign: "problems.assign"; max: 1; onFull: "displace"; take: "problems.own" }`,
			`responsible: { assign: "problems.assign"; fromField: "agent"; take: "problems.work" }`,
			`StateRules<Problem, { states: { review: { by: ["seat:accountable"]; frozen: ["title"] } } }>`,
			`by: ["seat:responsible"]`,
			`observed: Reconcile<{ observePermission: "sandboxes.observe"; staleAfterMs: 300000; status: "status"; unreachable: "unreachable" }>`,
		},
		FormatJSON: {`"implements": true`, `"name": "Seats"`, `"name": "Reconcile"`, `"field": "observed"`, `"fromField": "agent"`, `"seat:accountable"`, `"staleAfterMs": 300000`},
		FormatYAML: {"implements: true", "name: Seats", "name: Reconcile", "field: observed", "fromField: agent", "seat:accountable", "staleAfterMs: 300000"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"ProblemSeatsOccupants", "SandboxDeadline", "behaviorTable", "behaviorQueue", "optimistic", "observedObservedAt", "observedDrift", "statusEnteredAt", "@behavior"} {
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
	api, err := loader.LoadService(tsFixtures+"/fixture-seats-api", loader.WithWarnings(func(*verify.Diagnostic) {}))
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
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-seats-db", "kind": "DB"}], "outputs": {}}`+"\n", api.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, api)
			}
			source := writtenSource(t, api, format)
			for _, added := range []string{"seatsTakeSeat", "ProblemSeatsSeat", "SandboxObservedState", "observedObserve", "\"origin\""} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			reloaded, err := loader.LoadService(dir, loader.WithWarnings(func(*verify.Diagnostic) {}), loader.WithDependencyLoader(func(name string) (*ir.Schema, error) {
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
