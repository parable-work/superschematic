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
