package behaviorlayout_test

import (
	"encoding/json"
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
		`"uses":[{"id":"status","behavior":"Workflow","anchor":"status","fields":[` +
		`{"name":"status","field":"status","column":"status","type":"string","value":true},` +
		`{"name":"enteredAt","field":"statusEnteredAt","column":"status_entered_at","type":"Temporal.DateTime","convert":"epochMillis"}],` +
		`"operations":[{"name":"transition","writes":true}]}],` +
		`"spec":{"name":"Order","uses":[{"behavior":"Workflow","config":{"initial":"draft","states":["draft","paid","shipped"],"transitions":[{"from":"draft","to":"paid"},{"from":"paid","permission":"orders.ship","to":"shipped"}]},"anchor":{"field":"status"}}]}}`
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
