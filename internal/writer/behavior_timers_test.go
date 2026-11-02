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

// TestTimersRoundTrip (D103): a table that implements Timers is written in
// every format as its use, `implements Timers<Ticket, { ... }>` in
// TypeScript and `{ name, implements: true, config }` in the data forms,
// without the sub-table, the deadline queue and the @optimistic the loader
// adds for it, and reads back to the same IR.
func TestTimersRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-timers-db")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	schema.Description = ""
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { AutoGenerate, Timers, Workflow, key } from "@superschematic/db";`,
			`export abstract class Ticket implements Timers<Ticket, { deadlines: { idle: { afterMs: 3600000; start: { enters: "waiting" }; then: { transition: "closed" } }; remind: { afterMs: -86400000; start: { field: "visitAt" } }; respond: { afterMs: 1500; start: { create: true }; then: { transition: "escalated" }; until: { leaves: "open" } } }; permission: "tickets.timers" }> {`,
			`export abstract class Invoice implements Timers<Invoice, { deadlines: { overdue: { afterMs: 2592000000; start: { create: true }; then: { transition: "overdue" }; until: { leaves: "unpaid" } } }; status: "payment" }> {`,
		},
		FormatJSON: {`"implements": true`, `"name": "Timers"`},
		FormatYAML: {"implements: true", "name: Timers"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			source := writtenSource(t, schema, format)
			for _, want := range contains[format] {
				if !strings.Contains(source, want) {
					t.Errorf("written %s lacks %q:\n%s", format, want, source)
				}
			}
			for _, added := range []string{"TicketTimersDeadlines", "TicketDeadline", "behaviorTable", "behaviorQueue", "optimistic", "timersDeadlines"} {
				if strings.Contains(source, added) {
					t.Errorf("written %s carries the loader's %s:\n%s", format, added, source)
				}
			}
			if got := normalizeIR(t, writeAndReload(t, schema, format)); got != want {
				t.Errorf("IR mismatch after the %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}

// TestBehaviorWorkerRoundTrip (D103): fixture-timers-api's @behaviorWorker
// is written in every format with its table and options and without the
// queue the loader gives it, and reads back over its database to the same
// expanded IR.
func TestBehaviorWorkerRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-timers-api")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	db, err := loader.LoadService(tsFixtures + "/fixture-timers-db")
	if err != nil {
		t.Fatalf("loading the database: %v", err)
	}
	// The permissions option is written and read back too.
	schema.Workers[0].BehaviorWorker.Permissions = []string{"tickets.escalate"}
	want := normalizeIR(t, schema)
	contains := map[Format][]string{
		FormatTS: {
			`import { behaviorOperations, behaviorWorker, requirePermission } from "@superschematic/api";`,
			`import { Ticket } from "@schemas/fixture-timers-db";`,
			"// fixture-timers-api-ticket-timers.\n@behaviorWorker(Ticket, { concurrency: 2, permissions: [\"tickets.escalate\"] })\nexport abstract class TicketTimers {}\n",
		},
		FormatJSON: {`"behaviorWorker": {`, `"type": "Ticket"`, `"tickets.escalate"`},
		FormatYAML: {"behaviorWorker:\n", "type: Ticket", "- tickets.escalate"},
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(format), func(t *testing.T) {
			dir := t.TempDir()
			if _, err := WriteService(schema, format, dir); err != nil {
				t.Fatalf("WriteService: %v", err)
			}
			cfg := fmt.Sprintf(`{"name": %q, "kind": "API", "dependencies": [{"name": "fixture-timers-db", "kind": "DB"}], "outputs": {}}`+"\n", schema.Name)
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
			for _, added := range []string{"TicketDeadline", "timersFireTimer", "\"origin\"", "queue:"} {
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
			if got := normalizeIR(t, reloaded); got != want {
				t.Errorf("IR mismatch after writing %s\nwant:\n%s\ngot:\n%s", format, want, got)
			}
		})
	}
}
