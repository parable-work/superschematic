package rustsdkgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// rowFeaturesService is the API that serves fixture-references-db's
// Ticket's and Note's references, backlinks and files (#723).
const rowFeaturesService = "fixture-references-api"

// TestRowFeaturesSDKGolden pins every file of the Rust SDK for
// fixture-references-api, its tool documents included: each operation of
// a table's references and files is a method of the table's namespace,
// ticket.backlinks(id, None) and ticket.complete_upload(id, file, None),
// and the tools are the reads and the add of a reference alone.
// Regenerate with:
// go test ./internal/generator/rustsdkgen -run TestRowFeaturesSDKGolden -update
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
	sdkDir := filepath.Join(root, "sdk", "rust", rowFeaturesService)
	sdkOutput, err := Generate(apiOutput, naming.Default().RustSDKCrate(rowFeaturesService), naming.Default().RustTypesCrate(rowFeaturesService), nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, filepath.Join(root, "types", "rust", rowFeaturesService), nestedArraysClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}
	compareGoldenTree(t, sdkDir, filepath.Join("testdata", "golden", rowFeaturesService))
	var source []byte
	err = filepath.WalkDir(filepath.Join(sdkDir, "src"), func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		source = append(source, data...)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{"pub async fn backlinks(", "pub async fn complete_upload(", "RowAttachmentBegun"} {
		if !strings.Contains(string(source), each) {
			t.Errorf("the SDK's sources lack %q", each)
		}
	}
}
