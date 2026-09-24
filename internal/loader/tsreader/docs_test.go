package tsreader

import (
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

func operationNamed(t *testing.T, schema *ir.Schema, name string) *ir.FieldDef {
	t.Helper()
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == name {
				return op
			}
		}
	}
	t.Fatalf("operation %s not found", name)
	return nil
}

// TestOperationDocsDecoratorLoadsIntoIR: @docs writes the operation's docs
// record, mappingStatus defaults to mapped, and an operation without @docs
// has none.
func TestOperationDocsDecoratorLoadsIntoIR(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-docs"))
	if err != nil {
		t.Fatal(err)
	}
	get := operationNamed(t, schema, "getOrder")
	if get.Docs == nil {
		t.Fatal("getOrder has no docs")
	}
	if get.Docs.Title != "Get an order" || get.Docs.Audience != "shoppers" || get.Docs.MappingStatus != ir.DocsMappingStatusMapped {
		t.Fatalf("getOrder docs = %+v", get.Docs)
	}
	open := operationNamed(t, schema, "openReturn")
	if open.Docs == nil || open.Docs.Lifecycle != ir.DocsLifecycleDeprecated || open.Docs.MappingStatus != ir.DocsMappingStatusUncertain || open.Docs.Sunset != "2027-01-31" {
		t.Fatalf("openReturn docs = %+v", open.Docs)
	}
	if list := operationNamed(t, schema, "listOrders"); list.Docs != nil {
		t.Fatalf("listOrders has docs it never declared: %+v", list.Docs)
	}
}

// TestInvalidOperationDocsIsALocatedSchemaError: a @docs record that fails
// validation fails the load with a diagnostic at the decorator's argument.
func TestInvalidOperationDocsIsALocatedSchemaError(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-docs"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, `invalid @docs config: capability "GetNote" must contain at least two dot-separated lowercase segments`) {
		t.Fatalf("unexpected error: %s", msg)
	}
	if !regexp.MustCompile(`broken\.schema\.ts:\d+:\d+:`).MatchString(msg) {
		t.Fatalf("diagnostic should carry file:line:col, got: %s", msg)
	}
}

// TestFieldPresentationDecoratorsLoadIntoIR: @docs({ title }), @purpose and
// @icon from @superschematic/schema write the field's title, purpose and
// icon, next to operation @docs from @superschematic/api in the same file.
func TestFieldPresentationDecoratorsLoadIntoIR(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-docs"))
	if err != nil {
		t.Fatal(err)
	}
	var total, id *ir.FieldDef
	for _, f := range schema.Types["Order"].Fields {
		switch f.Name {
		case "totalCents":
			total = f
		case "id":
			id = f
		}
	}
	if total == nil || total.Title != "Total" || total.Purpose != "The order total in **cents**, tax included." || total.Icon != "receipt" {
		t.Fatalf("Order.totalCents = %+v", total)
	}
	if id == nil || id.Title != "" || id.Purpose != "" || id.Icon != "" {
		t.Fatalf("Order.id carries presentation it never declared: %+v", id)
	}
}

// TestOperationMCPDecoratorsLoadIntoIR: @mcp writes the classification,
// @icon from @superschematic/api writes the operation's icon, @docs carries
// the replay keys, and an operation without @mcp has no record.
func TestOperationMCPDecoratorsLoadIntoIR(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-mcp"))
	if err != nil {
		t.Fatal(err)
	}
	get := operationNamed(t, schema, "getOrder")
	if get.MCP == nil || get.MCP.Handle != "get_order" || get.MCP.Hidden || get.Icon != "receipt" {
		t.Fatalf("getOrder mcp = %+v, icon %q", get.MCP, get.Icon)
	}
	ui, _ := get.MCP.Meta["ui"].(map[string]any)
	if ui["resourceUri"] != "ui://orders/detail" {
		t.Fatalf("getOrder _meta = %#v", get.MCP.Meta)
	}
	open := operationNamed(t, schema, "openReturn")
	if open.Docs.ReplayMode != ir.DocsReplayModeIdempotent || len(open.Docs.IdempotencyKeyPointers) != 1 || open.Docs.IdempotencyKeyPointers[0] != "/requestId" {
		t.Fatalf("openReturn replay = %+v", open.Docs)
	}
	del := operationNamed(t, schema, "deleteOrder")
	if del.MCP == nil || !del.MCP.Hidden || del.MCP.HiddenReason != "Staff console only." || del.Docs != nil {
		t.Fatalf("deleteOrder mcp = %+v", del.MCP)
	}
	if export := operationNamed(t, schema, "exportOrders"); export.MCP != nil || export.Icon != "" {
		t.Fatalf("exportOrders carries MCP metadata it never declared: %+v", export.MCP)
	}
}

// TestInvalidOperationMCPIsALocatedSchemaError: a bad @mcp handle fails the
// load with a diagnostic at the decorator's argument.
func TestInvalidOperationMCPIsALocatedSchemaError(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-mcp"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, `invalid @mcp config: handle "getNote" must be lowercase snake_case and begin with a letter`) {
		t.Fatalf("unexpected error: %s", msg)
	}
	if !regexp.MustCompile(`broken\.schema\.ts:\d+:\d+:`).MatchString(msg) {
		t.Fatalf("diagnostic should carry file:line:col, got: %s", msg)
	}
}
