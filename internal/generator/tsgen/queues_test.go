package tsgen

import (
	"fmt"
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
// whose message holds a field of every kind a column takes, and of
// fixture-queue-geo-db, whose message holds locations and durations,
// queues.ts carries the statements the Go ORM runs, byte for byte, the
// prune and a complete that deletes for a queue that keeps no done message
// among them, with the Go ORM's arguments, and writes each field to the
// column the Go ORM writes, as JSON where the Go ORM marshals it, as an
// array where it passes a list, as a POINT where it passes a pgtype.Point
// and as an INTERVAL where it passes a time.Duration, with the enum default
// it writes (D53, D68). A message one language enqueues the other claims
// alike.
func TestQueuesWriteWhatTheGoORMWrites(t *testing.T) {
	for _, fixture := range []string{"fixture-queue-db", "fixture-queue-geo-db"} {
		t.Run(fixture, func(t *testing.T) { testQueuesWriteWhatTheGoORMWrites(t, fixture) })
	}
}

func testQueuesWriteWhatTheGoORMWrites(t *testing.T, fixture string) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, fixture))
	if err != nil {
		t.Fatal(err)
	}
	orm, err := ormgen.Generate(schema, ormgen.Options{
		SchemaName:  fixture,
		ModulePath:  "example.com/schemas/orm/" + fixture,
		TypesModule: "example.com/schemas/types/go/" + fixture,
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatal(err)
	}
	queues, err := TypeScriptQueues(schema, true)
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
		ts := fmt.Sprintf("%s %d %d %d %d %d", q.Table, q.Retries, q.BackoffSeconds, q.MaxBackoffSeconds, q.LeaseSeconds, q.RetainSeconds)
		goORM := fmt.Sprintf("%s %d %d %d %d %d", g.QuotedTableName, g.Retries, g.BackoffSeconds, g.MaxBackoffSeconds, g.LeaseSeconds, g.RetainSeconds)
		if ts != goORM {
			t.Errorf("queue %s: queues.ts's table, retries, backoff, maximum backoff, lease and retention %s are not the Go ORM's %s", q.Name, ts, goORM)
		}
		if len(q.Fields) != len(g.Fields) {
			t.Fatalf("queue %s: queues.ts writes %d fields and the Go ORM %d", q.Name, len(q.Fields), len(g.Fields))
		}
		for j, f := range q.Fields {
			gf := g.Fields[j]
			if f.Name != gf.Name || f.Column != gf.QuotedDBName || f.JSON != gf.IsJSONField || f.List != gf.NativeList() || f.Default != gf.EnumDefault ||
				f.Point != gf.IsGeoPoint || f.Interval != gf.IsInterval {
				t.Errorf("queue %s field %s: queues.ts writes %+v, the Go ORM column %s, JSON %v, list %v, default %q, point %v, interval %v",
					q.Name, f.Name, f, gf.QuotedDBName, gf.IsJSONField, gf.NativeList(), gf.EnumDefault, gf.IsGeoPoint, gf.IsInterval)
			}
		}
	}
}

// TestQueuesLeaveOutWhatTheClientCannotHold: a queue whose message holds a
// value the TypeScript queue client cannot write and read back yet, a list
// of Temporal.Duration values, which Postgres holds as an INTERVAL[], is
// left out of queues.ts with why, and the other queues are written.
func TestQueuesLeaveOutWhatTheClientCannotHold(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queue-db"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL"}}
	ping := schema.Types["Ping"]
	ping.Fields = append(ping.Fields, &ir.FieldDef{Name: "every", TypeRef: ir.TypeRef{Name: "Temporal.Duration", IsArray: true}})
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
		"Queue Ping is left out: field every is a list of Temporal.Duration values, an INTERVAL[] column, which the TypeScript queue client does not read yet.",
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
