package tsgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/ormgen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestQueuesWriteWhatTheGoORMWrites: for every queue of fixture-queue-db,
// whose message holds a field of every kind a column takes, queues.ts
// carries the statements the Go ORM runs, byte for byte, and writes each
// field to the column the Go ORM writes, as JSON where the Go ORM marshals
// it and as an array where it passes a list, with the enum default it
// writes (D53). A message one language enqueues the other claims alike.
func TestQueuesWriteWhatTheGoORMWrites(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	orm, err := ormgen.Generate(schema, ormgen.Options{
		SchemaName:  "fixture-queue-db",
		ModulePath:  "example.com/schemas/orm/fixture-queue-db",
		TypesModule: "example.com/schemas/types/go/fixture-queue-db",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	queues, err := TypeScriptQueues(schema)
	if err != nil {
		t.Fatal(err)
	}
	if len(queues) != len(orm.Queues) || len(queues) == 0 {
		t.Fatalf("queues.ts holds %d queues and the Go ORM %d", len(queues), len(orm.Queues))
	}
	for i, q := range queues {
		g := orm.Queues[i]
		if q.Name != g.Name || q.Unsupported != "" {
			t.Fatalf("queue %d is %s (%s) in queues.ts and %s in the Go ORM", i, q.Name, q.Unsupported, g.Name)
		}
		if q.Statements != g.Statements {
			t.Errorf("queue %s: queues.ts's statements are not the Go ORM's:\n%+v\n%+v", q.Name, q.Statements, g.Statements)
		}
		if q.Table != g.QuotedTableName || q.Retries != g.Retries || q.BackoffSeconds != g.BackoffSeconds || q.LeaseSeconds != g.LeaseSeconds {
			t.Errorf("queue %s: queues.ts's table and arguments %s %d %d %d are not the Go ORM's %s %d %d %d", q.Name,
				q.Table, q.Retries, q.BackoffSeconds, q.LeaseSeconds, g.QuotedTableName, g.Retries, g.BackoffSeconds, g.LeaseSeconds)
		}
		if len(q.Fields) != len(g.Fields) {
			t.Fatalf("queue %s: queues.ts writes %d fields and the Go ORM %d", q.Name, len(q.Fields), len(g.Fields))
		}
		for j, f := range q.Fields {
			gf := g.Fields[j]
			if f.Name != gf.Name || f.Column != gf.QuotedDBName || f.JSON != gf.IsJSONField || f.List != gf.NativeList() || f.Default != gf.EnumDefault {
				t.Errorf("queue %s field %s: queues.ts writes %+v, the Go ORM column %s, JSON %v, list %v, default %q",
					q.Name, f.Name, f, gf.QuotedDBName, gf.IsJSONField, gf.NativeList(), gf.EnumDefault)
			}
		}
	}
}

// TestQueuesLeaveOutWhatTheClientCannotHold: a queue whose message holds a
// value the TypeScript queue client cannot write and read back yet, a
// Temporal.Duration, which Postgres holds as an INTERVAL, is left out of
// queues.ts with why, and the other queues are written.
func TestQueuesLeaveOutWhatTheClientCannotHold(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL"}}
	ping := schema.Types["Ping"]
	ping.Fields = append(ping.Fields, &ir.FieldDef{Name: "every", TypeRef: ir.TypeRef{Name: "Temporal.Duration"}})
	output, err := Generate(schema, Options{SchemaName: "fixture-queue-db", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	text, err := os.ReadFile(filepath.Join(dir, QueuesFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"Queue Ping is left out: field every is a Temporal.Duration, an INTERVAL column, which the TypeScript queue client does not read yet.",
		"import type { OrderPlaced } from './types';",
		"export const orderPlacedQueue: QueueDefinition<OrderPlaced> = {",
	} {
		if !strings.Contains(string(text), want) {
			t.Errorf("queues.ts lacks %q:\n%s", want, text)
		}
	}
	if strings.Contains(string(text), "pingQueue") {
		t.Errorf("queues.ts writes the queue it leaves out:\n%s", text)
	}
}
