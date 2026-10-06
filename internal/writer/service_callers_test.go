package writer

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestServiceCallersRoundTrip: the service clauses of
// fixture-service-auth-api survive every format. The TypeScript writer
// spells each from name as the API service's sentinel, imported from its
// package, which the reader resolves back to the name. The writer has no
// TypeScript form for @publicRoute, or for @auth on some operations of a
// set and not others, so the fixture drops its @publicRoute operation and
// keeps @auth only in its Authenticated set, with a permission as the user
// clause of syncMyStock's @allowService instead.
func TestServiceCallersRoundTrip(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-service-auth-api")
	if err != nil {
		t.Fatalf("loading fixture: %v", err)
	}
	for _, set := range schema.OperationSets {
		set.Operations = slices.DeleteFunc(set.Operations, func(op *ir.FieldDef) bool { return op.Public })
		if set.Name == "LedgerQueries" {
			continue
		}
		for _, op := range set.Operations {
			op.Auth = false
			if op.Name == "syncMyStock" {
				op.Permissions = []string{"stock.sync"}
			}
		}
	}
	want := normalizeIR(t, schema)
	for _, target := range []Format{FormatTS, FormatJSON, FormatYAML} {
		t.Run(string(target), func(t *testing.T) {
			if got := normalizeIR(t, writeAndReload(t, schema, target)); got != want {
				t.Errorf("IR mismatch after TS -> %s round trip\nwant:\n%s\ngot:\n%s", target, want, got)
			}
		})
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "src", "stock-api.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	for _, want := range []string{
		`import { FixtureServiceCallerApi } from "@schemas/fixture-service-caller-api";`,
		"@requireService({ from: [FixtureServiceCallerApi] })\nexport class SyncOperations {",
		"  @requireService()\n",
		"  @allowService()\n",
		"@allowService({ from: [FixtureServiceCallerApi] })\nexport class LedgerQueries extends Authenticated {",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("TS output missing %q:\n%s", want, source)
		}
	}
}
