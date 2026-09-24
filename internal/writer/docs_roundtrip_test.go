package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestTSWriterRoundTripsOperationDocs: the TypeScript writer emits @docs on
// each operation that has a record and the field presentation decorators,
// under aliases because both authoring packages export docs, and reading
// its output back gives the same IR. The corpus covers the data-form legs.
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
		`import { docs as schemaDocs, icon as schemaIcon, purpose } from "@superschematic/schema";`,
		`@schemaDocs({ title: "Total" })`,
		`@purpose("The order total in **cents**, tax included.")`,
		`@schemaIcon("receipt")`,
		`@apiDocs({ title: "Get an order", description: "Returns one order by its identifier.", capability: "orders.get", lifecycle: "active", visibility: "public", audience: "shoppers", useWhen: "Use when you have an order identifier.", doNotUseWhen: "Do not use to list orders; call listOrders.", success: "Returns the order with its total.", errors: [{ code: "order_not_found", description: "No order has that identifier.", commonCorrection: "Take the identifier from a listOrders result." }] })`,
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

// TestTSWriterRoundTripsOperationMCP: the TypeScript writer emits @mcp,
// operation @icon (as apiIcon) and the @docs replay keys, and reading its
// output back gives the same IR. The fixture's map field has no TypeScript
// authoring form, so it is dropped first.
func TestTSWriterRoundTripsOperationMCP(t *testing.T) {
	schema, err := loader.LoadService(tsFixtures + "/fixture-mcp")
	if err != nil {
		t.Fatal(err)
	}
	request := schema.Types["ReturnRequest"]
	fields := request.Fields[:0]
	for _, f := range request.Fields {
		if !f.TypeRef.IsMap {
			fields = append(fields, f)
		}
	}
	request.Fields = fields

	dir := t.TempDir()
	if _, err := WriteService(schema, FormatTS, dir); err != nil {
		t.Fatalf("WriteService: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "orders.schema.ts"))
	if err != nil {
		t.Fatalf("reading TS output: %v", err)
	}
	for _, want := range []string{
		`import { HttpMethod, QueryParam, docs as apiDocs, icon as apiIcon, mcp, rest } from "@superschematic/api";`,
		`@apiIcon("receipt")`,
		`@mcp({ handle: "get_order", invocationPolicy: "auto", _meta: { ui: { resourceUri: "ui://orders/detail" } } })`,
		`@mcp({ handle: "update_order", invocationPolicy: "ask" })`,
		`replayMode: "idempotent", idempotencyKeyPointers: ["/requestId"] })`,
		`replayMode: "compare_and_swap", expectedRevisionPointers: ["/revision"] })`,
		`@mcp({ hidden: true, reason: "Staff console only." })`,
	} {
		if !strings.Contains(string(got), want) {
			t.Fatalf("TS output missing %q:\n%s", want, got)
		}
	}

	if got, want := normalizeIR(t, writeAndReload(t, schema, FormatTS)), normalizeIR(t, schema); got != want {
		t.Fatalf("IR changed after a TS -> TS round trip\nwant:\n%s\ngot:\n%s", want, got)
	}
}
