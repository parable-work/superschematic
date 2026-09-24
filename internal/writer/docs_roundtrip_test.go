package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestTSWriterRoundTripsOperationDocs: the TypeScript writer emits @docs on
// each operation that has a record, and reading its output back gives the
// same IR. The corpus covers the data-form legs.
func TestTSWriterRoundTripsOperationDocs(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-docs")
	if err != nil {
		t.Fatal(err)
	}

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "orders.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	for _, want := range []string{
		`import { HttpMethod, docs as apiDocs, rest } from "@superschematic/api";`,
		`@apiDocs({ title: "Get an order", description: "Returns one order by its identifier.", capability: "orders.get", lifecycle: "active", visibility: "public", audience: "shoppers" })`,
		`@apiDocs({ title: "Open a return", description: "Opens a return for one delivered order.", capability: "orders.returns.open", lifecycle: "deprecated", visibility: "preview", mappingStatus: "uncertain", replacement: "orders.returns.create", sunset: "2027-01-31" })`,
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("TS output missing %q:\n%s", want, got)
		}
	}

	if got, want := normalizeIR(t, writeAndReload(t, schema, FormatTS)), normalizeIR(t, schema); got != want {
		t.Fatalf("IR changed after a TS -> TS round trip\nwant:\n%s\ngot:\n%s", want, got)
	}
}
