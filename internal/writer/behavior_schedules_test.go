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

// TestSchedulesRoundTrip (D104): a table that implements Schedules beside a
// Workflow field is written in every format as its use, `implements
// Schedules<Problem, { ... }>` in TypeScript and `{ name, implements: true,
// config }` in the data forms, without the sub-table and the deadline,
// change and event queues the loader adds for it, and read back to the same
// IR; the API that serves its behavior operations and runs its queues in a
// @behaviorWorker reads back over its database to the same expanded IR,
// with neither the worker's queues and events nor the event types the
// loader adds in its written form.
func TestSchedulesRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-schedules-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema.Description = ""
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`implements Schedules<Problem, {`,
			`endOn: { enters: ["done", "dropped"] }`,
			`kinds: ["after", "every", "cron"]`,
			`retry: { afterMs: 300000; outcome: "missed" }`,
			`timeZone: "America/New_York"`,
		},
		FormatJSON: {`"implements": true`, `"name": "Schedules"`, `"timeZone": "America/New_York"`, `"afterMs": 300000`},
		FormatYAML: {"implements: true", "name: Schedules", "timeZone: America/New_York", "afterMs: 300000"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"ProblemSchedulesSchedules", "ProblemDeadline", "ProblemChange", "ProblemEvent", "behaviorTable", "behaviorQueue", "optimistic", "statusEnteredAt", "@behavior"} {
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
	api, err := loader.LoadService(tsFixtures+"/fixture-schedules-api", loader.WithWarnings(func(*verify.Diagnostic) {}))
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
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-schedules-db", "kind": "DB"}], "outputs": {}}`+"\n", api.Name)
			if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(cfg), 0o644); err != nil {
				t.Fatal(err)
			}
			if format == FormatTS {
				writeTSProject(t, dir, api)
			}
			source := writtenSource(t, api, format)
			for _, added := range []string{"schedulesSetSchedule", "ProblemSchedulesKind", "ProblemSchedulesRunEvent", "ProblemSchedulesRunPayload", "ProblemWakeUpsSchedulesRun", "ProblemEvent", "\"origin\""} {
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
