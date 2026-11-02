package behaviorlayout_test

import (
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// fixture is the DB service whose Order's status is a Workflow and whose
// Shipment has two, its status and its paymentStatus (D103).
const fixture = "../../loader/tsreader/testdata/services/fixture-workflow-fields-db"

func load(t *testing.T) (*ir.Schema, *registry.Registry) {
	t.Helper()
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.FromSlash(fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	return schema, reg
}

// The layout document holds each table that composes field behaviors:
// its table, key and version, its own columns, each use's owned columns
// by the layout rule and its operations, and the type as the core runs it.
func TestOf(t *testing.T) {
	schema, reg := load(t)
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	if layout == nil || len(layout.Types) != 2 {
		t.Fatalf("layout = %+v, want Order and Shipment", layout)
	}
	data, err := json.Marshal(layout.Types["Order"])
	if err != nil {
		t.Fatal(err)
	}
	want := `{"type":"Order","table":"order","key":{"field":"id","column":"id","type":"Identity.UUID"},"version":"_version",` +
		`"own":[{"field":"title","column":"title","type":"string"},{"field":"placedAt","column":"placed_at","type":"Temporal.DateTime","convert":"epochMillis"}],` +
		`"uses":[{"id":"status","behavior":"Workflow","anchor":"status","member":"status","fields":[` +
		`{"name":"status","field":"status","column":"status","type":"string","value":true},` +
		`{"name":"enteredAt","field":"statusEnteredAt","column":"status_entered_at","type":"Temporal.DateTime","convert":"epochMillis"}],` +
		`"operations":[{"name":"transition","writes":true}]}],` +
		`"spec":{"name":"Order","uses":[{"behavior":"Workflow","config":{"initial":"draft","states":["draft","paid","shipped"],"transitions":[{"from":"draft","to":"paid"},{"from":"paid","permission":"orders.ship","to":"shipped"}]},"anchor":{"field":"status"}}],` +
		`"fields":[{"name":"title","type":"string"},{"name":"placedAt","type":"Temporal.DateTime","dateTime":true}]}}`
	if string(data) != want {
		t.Errorf("Order's layout =\n%s\nwant\n%s", data, want)
	}

	shipment := layout.Types["Shipment"]
	var columns []string
	for _, use := range shipment.Uses {
		for _, f := range use.Fields {
			columns = append(columns, use.ID+"."+f.Name+"="+f.Field+"/"+f.Column.Column)
		}
	}
	if got, want := strings.Join(columns, " "), "status.status=status/status status.enteredAt=statusEnteredAt/status_entered_at "+
		"paymentStatus.status=paymentStatus/payment_status paymentStatus.enteredAt=paymentStatusEnteredAt/payment_status_entered_at"; got != want {
		t.Errorf("Shipment's owned columns = %s, want %s", got, want)
	}
	if len(shipment.Spec.Uses) != 2 || shipment.Spec.Uses[1].Anchor.Field != "paymentStatus" {
		t.Errorf("Shipment's spec = %+v", shipment.Spec)
	}
}

// A schema with no table that composes a field behavior has no document,
// and a table's layout from TableOf is Of's.
func TestOfNoneAndTableOf(t *testing.T) {
	schema, reg := load(t)
	table, err := behaviorlayout.TableOf(schema, schema.Types["Shipment"], reg)
	if err != nil {
		t.Fatal(err)
	}
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	a, _ := json.Marshal(table)
	b, _ := json.Marshal(layout.Types["Shipment"])
	if string(a) != string(b) {
		t.Errorf("TableOf =\n%s\nOf's =\n%s", a, b)
	}
	for _, td := range schema.Types {
		td.Behaviors = nil
	}
	if layout, err := behaviorlayout.Of(schema, reg); err != nil || layout != nil {
		t.Errorf("Of of a schema without field behaviors = %+v, %v", layout, err)
	}
}

// A declared field with no owned field on the table is a defect of the
// build, which the loader's pass rules out.
func TestTableOfMissingOwnedField(t *testing.T) {
	schema, reg := load(t)
	order := schema.Types["Order"]
	var fields []*ir.FieldDef
	for _, fd := range order.Fields {
		if fd.Name != "statusEnteredAt" {
			fields = append(fields, fd)
		}
	}
	order.Fields = fields
	if _, err := behaviorlayout.TableOf(schema, order, reg); err == nil || !strings.Contains(err.Error(), "behavior Workflow's field enteredAt has no owned field on the table") {
		t.Fatalf("TableOf = %v", err)
	}
}

// A list or a map of date-times has no convert: no adapter converts its
// elements, so it reaches the core as its column holds it, where a single
// date-time is epoch milliseconds.
func TestOfAListOfDateTimesIsNotConverted(t *testing.T) {
	schema, reg := load(t)
	order := schema.Types["Order"]
	order.Fields = append(order.Fields,
		&ir.FieldDef{Name: "remindAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime", IsArray: true}},
		&ir.FieldDef{Name: "seenAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime", IsMap: true}},
	)
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	converts := map[string]behaviorlayout.Convert{}
	for _, column := range layout.Types["Order"].Own {
		converts[column.Field] = column.Convert
	}
	for field, want := range map[string]behaviorlayout.Convert{"placedAt": behaviorlayout.ConvertEpochMillis, "remindAt": behaviorlayout.ConvertNone, "seenAt": behaviorlayout.ConvertNone} {
		if got, ok := converts[field]; !ok || got != want {
			t.Errorf("Order's own column %s converts as %q (held %v), want %q", field, got, ok, want)
		}
	}
}

// A soft-deletable table's layout names the column its live rows hold
// NULL, its deletedAt's as @column names it, which the adapter holds every
// read and fenced write to; a table without deletedAt has none (D103).
func TestOfLive(t *testing.T) {
	schema, reg := load(t)
	order := schema.Types["Order"]
	order.Fields = append(order.Fields, &ir.FieldDef{Name: "deletedAt", TypeRef: ir.TypeRef{Name: "Temporal.DateTime"}, SQLName: "removed_at"})
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	if live := layout.Types["Order"].Live; live == nil || live.Column != "removed_at" {
		t.Errorf("Order's live = %+v, want the column removed_at", live)
	}
	for _, column := range layout.Types["Order"].Own {
		if column.Field == "deletedAt" {
			t.Errorf("Order's own columns hold deletedAt, an audit field: %+v", column)
		}
	}
	if live := layout.Types["Shipment"].Live; live != nil {
		t.Errorf("Shipment's live = %+v, want none", live)
	}
	data, err := json.Marshal(layout.Types["Order"])
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"version":"_version","live":{"column":"removed_at"},"own":`) {
		t.Errorf("Order's layout = %s, want its live after its version", data)
	}
}

var update = flag.Bool("update", false, "rewrite testdata/*.layout.json")

// timersFixture is the DB service whose Ticket implements Timers beside its
// status Workflow, and whose Invoice implements Timers that reads the
// second of its two Workflows (D103).
const timersFixture = "../../loader/tsreader/testdata/services/fixture-timers-db"

// reactionsFixture is the DB service whose Project's Reactions hears its
// Tasks through task.project, and whose Task's Reactions moves its project
// (D103).
const reactionsFixture = "../../loader/tsreader/testdata/services/fixture-reactions-db"

// layoutOfFixture loads the DB service at dir and holds its layout document
// to testdata/<name>.layout.json, which -update rewrites.
func layoutOfFixture(t *testing.T, dir, name string) *behaviorlayout.Layout {
	t.Helper()
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.FromSlash(dir), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := json.MarshalIndent(layout, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	got = append(got, '\n')
	golden := filepath.Join("testdata", name+".layout.json")
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("missing golden (run with -update): %v", err)
	}
	if string(got) != string(want) {
		t.Errorf("the layout of %s differs from %s (run with -update and review the diff):\n%s", name, golden, got)
	}
	return layout
}

// The layout document of a table that implements Timers holds every table
// use with its member, the use's sub-table with its row's column, its key
// and its fields, the table's deadline queue and its change queue, Timers
// hearing its writes, and the spec's own fields, a date-time marked;
// testdata/fixture-timers-db.layout.json is the document the adapters of
// every language are written against.
func TestOfTimers(t *testing.T) {
	layout := layoutOfFixture(t, timersFixture, "fixture-timers-db")

	ticket := layout.Types["Ticket"]
	if ticket == nil || len(ticket.Uses) != 2 {
		t.Fatalf("Ticket's layout = %+v, want its Timers and its status", ticket)
	}
	timers := ticket.Uses[0]
	if timers.ID != "Timers" || timers.Member != "timers" || timers.Anchor != "" || len(timers.Fields) != 0 || len(timers.Tables) != 1 {
		t.Errorf("Ticket's Timers = %+v", timers)
	}
	if status := ticket.Uses[1]; status.Member != "status" || status.Anchor != "status" || len(status.Tables) != 0 {
		t.Errorf("Ticket's status = %+v", status)
	}
	deadlines := timers.Tables[0]
	if deadlines.Table != "ticket_timers_deadlines" || deadlines.Row != "ticket_id" || deadlines.Key.Column.Column != "name" || len(deadlines.Fields) != 6 {
		t.Errorf("Ticket's deadlines = %+v", deadlines)
	}
	want2 := &behaviorlayout.Deadlines{Queue: "TicketDeadline", Table: "ticket_deadline_queue", RowKey: "row_key", Use: "use_id", Name: "name", At: "at", Payload: "payload"}
	if d := ticket.Deadlines; d == nil || *d != *want2 {
		t.Errorf("Ticket's deadlines queue = %+v, want %+v", d, want2)
	}
	changes := &behaviorlayout.Changes{Queue: "TicketChange", Table: "ticket_change_queue", RowKey: "row_key", Write: "write", At: "at", Before: "before", After: "after"}
	if c := ticket.Changes; c == nil || *c != *changes {
		t.Errorf("Ticket's change queue = %+v, want %+v", c, changes)
	}
	fields, _ := json.Marshal(ticket.Spec.Fields)
	if string(fields) != `[{"name":"title","type":"string"},{"name":"visitAt","type":"Temporal.DateTime","dateTime":true}]` {
		t.Errorf("Ticket's spec fields = %s", fields)
	}
	if anchor, _ := json.Marshal(ticket.Spec.Uses[0].Anchor); string(anchor) != `"type"` {
		t.Errorf("Ticket's Timers anchor = %s, want \"type\"", anchor)
	}
}

// The layout document of tables whose Reactions hear their writes names
// each table's change queue, and the spec's field of a to-one relation its
// relation, by which the core finds the listeners of the table the relation
// points at; testdata/fixture-reactions-db.layout.json is the document.
func TestOfReactions(t *testing.T) {
	layout := layoutOfFixture(t, reactionsFixture, "fixture-reactions-db")
	project, task := layout.Types["Project"], layout.Types["Task"]
	if project == nil || task == nil {
		t.Fatalf("the layout's types = %v, want Project and Task", layout.Types)
	}
	for _, table := range []*behaviorlayout.Table{project, task} {
		want := &behaviorlayout.Changes{Queue: table.Type + "Change", Table: strings.ToLower(table.Type) + "_change_queue", RowKey: "row_key", Write: "write", At: "at", Before: "before", After: "after"}
		if c := table.Changes; c == nil || *c != *want || table.Deadlines != nil {
			t.Errorf("%s's queues = changes %+v and deadlines %+v, want %+v and none", table.Type, c, table.Deadlines, want)
		}
		if len(table.Uses) != 2 || table.Uses[0].Behavior != "Reactions" || table.Uses[0].Member != "reactions" || len(table.Uses[0].Operations) != 0 {
			t.Errorf("%s's uses = %+v, want Reactions with no operation, then its status", table.Type, table.Uses)
		}
	}
	fields, _ := json.Marshal(task.Spec.Fields)
	if string(fields) != `[{"name":"title","type":"string"},{"name":"priority","type":"string"},{"name":"projectId","type":"Identity.UUID","relation":{"name":"project","type":"Project"}}]` {
		t.Errorf("Task's spec fields = %s", fields)
	}
}

// The image a change trigger writes is the layout's own columns and each
// use's owned columns: no key, no _version, no audit column.
func TestImageColumns(t *testing.T) {
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.FromSlash(reactionsFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(behaviorlayout.ImageColumns(schema, schema.Types["Task"]), ","); got != "title,priority,project_id,status,status_entered_at" {
		t.Errorf("Task's image = %s", got)
	}
	timers, err := loader.LoadService(filepath.FromSlash(timersFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(behaviorlayout.ImageColumns(timers, timers.Types["Invoice"]), ","); got != "number,status,status_entered_at,payment,payment_entered_at" {
		t.Errorf("Invoice's image = %s", got)
	}
}

// OwnFields are the layout's own fields: the key, the version and the
// fields a behavior owns are none of them.
func TestOwnFields(t *testing.T) {
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.FromSlash(timersFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, c := range behaviorlayout.OwnFields(schema, schema.Types["Invoice"]) {
		names = append(names, c.Field)
	}
	if got := strings.Join(names, ","); got != "number" {
		t.Errorf("Invoice's own fields = %s, want number", got)
	}
}
