package ormgen

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestQueuesHoldALocationAndADuration: DeliveryPlanned of
// fixture-queue-geo-db holds a location, a POINT column, and a duration, an
// INTERVAL one, each required and optional. The ORM converts a location
// through pgtype.Point and a duration through time.Duration, which pgx
// writes as its microseconds, and refuses a list of either, which no queue
// client holds yet (D53, amended).
func TestQueuesHoldALocationAndADuration(t *testing.T) {
	load := func() *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, queueGeoFixture))
		if err != nil {
			t.Fatal(err)
		}
		return schema
	}
	generate := func(schema *ir.Schema) (*ORMOutput, error) {
		return Generate(schema, Options{
			SchemaName:  queueGeoFixture,
			ModulePath:  "example.com/schemas/orm/" + queueGeoFixture,
			TypesModule: "example.com/schemas/types/go/" + queueGeoFixture,
			Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		})
	}
	output, err := generate(load())
	if err != nil {
		t.Fatal(err)
	}
	if len(output.Queues) != 1 {
		t.Fatalf("queues = %+v, want DeliveryPlanned", output.Queues)
	}
	want := map[string]string{
		"dropAt": "pgtype.Point",
		"wait":   "time.Duration",
		"meetAt": "pgtype.Point",
		"grace":  "*time.Duration",
	}
	for _, f := range output.Queues[0].Fields {
		if f.Name == "orderId" {
			continue
		}
		point := f.Name == "dropAt" || f.Name == "meetAt"
		if f.IsGeoPoint != point || f.IsInterval == point || f.QueueScanTemp() != want[f.Name] {
			t.Errorf("field %s: IsGeoPoint %t, IsInterval %t, scanned into %q; want %t, %t, %q", f.Name, f.IsGeoPoint, f.IsInterval, f.QueueScanTemp(), point, !point, want[f.Name])
		}
	}
	if !output.QueuesImportPgtype() {
		t.Error("queues.go converts a location through pgtype, which it does not import")
	}

	for _, tc := range []struct{ scalar, column string }{
		{"Geo.Location", "a POINT[] column"},
		{"Temporal.Duration", "an INTERVAL[] column"},
	} {
		schema := load()
		td := schema.Types["DeliveryPlanned"]
		td.Fields = append(td.Fields, &ir.FieldDef{Name: "stops", TypeRef: ir.TypeRef{Name: tc.scalar, IsArray: true}, Required: true})
		_, err := generate(schema)
		wantErr := "queue DeliveryPlanned: field stops is a list of " + tc.scalar + " values, " + tc.column + ", which a queue's message does not hold yet; hold it in a @jsonField type"
		if err == nil || !strings.Contains(err.Error(), wantErr) {
			t.Errorf("Generate with a list of %s = %v, want %q", tc.scalar, err, wantErr)
		}
	}
}
