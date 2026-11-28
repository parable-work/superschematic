package gosdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// rowFeaturesService is the API that serves fixture-references-db's
// Ticket's and Note's references, backlinks and files (#723).
const rowFeaturesService = "fixture-references-api"

// TestRowFeaturesSDKGolden pins every file of the Go SDK for
// fixture-references-api, its tool documents included: each operation of
// a table's references and files is a method of the table's namespace,
// TicketNamespace.Backlinks(ctx, id) and
// TicketNamespace.CompleteUpload(ctx, id, file). Regenerate with:
// go test ./internal/generator/gosdkgen -run TestRowFeaturesSDKGolden -update
func TestRowFeaturesSDKGolden(t *testing.T) {
	load := func(name string) *ir.Schema {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name), loader.WithWarnings(func(*verify.Diagnostic) {}))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		return schema
	}
	schema, db := load(rowFeaturesService), load("fixture-references-db")
	apiOutput, err := apigen.Generate(schema, apigen.Options{
		Provider:     sessionauth.Provider{},
		SchemaName:   rowFeaturesService,
		ModulePath:   "example.com/schemas/api/" + rowFeaturesService,
		TypesModule:  "example.com/schemas/types/go/" + rowFeaturesService,
		Dependencies: map[string]*ir.Schema{"fixture-references-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	root := t.TempDir()
	sdkDir := filepath.Join(root, "sdk", "go", rowFeaturesService)
	typesDir := filepath.Join(root, "types", "go", rowFeaturesService)
	if err := os.MkdirAll(typesDir, 0o755); err != nil {
		t.Fatal(err)
	}
	sdkOutput, err := Generate(apiOutput, "example.com/schemas/sdk/go/"+rowFeaturesService, "sdk", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, typesDir, nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", rowFeaturesService))

	source, err := os.ReadFile(filepath.Join(sdkDir, "namespaces", "ticket.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{"func (n *TicketNamespace) Backlinks(", "func (n *TicketNamespace) CompleteUpload(", "/files/", "types.RowAttachmentBegun"} {
		if !strings.Contains(string(source), each) {
			t.Errorf("namespaces/ticket.go lacks %q", each)
		}
	}
}
