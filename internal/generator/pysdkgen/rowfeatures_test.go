package pysdkgen

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

// TestRowFeaturesSDKGolden pins every file of the Python SDK for
// fixture-references-api: each operation of a table's references and
// files is a method of the table's namespace, sdk.ticket.backlinks(id) and
// sdk.ticket.complete_upload(id, file). Regenerate with:
// go test ./internal/generator/pysdkgen -run TestRowFeaturesSDKGolden -update
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
		Dependencies: map[string]*ir.Schema{"fixture-references-db": db},
		Clock:        nestedArraysClock,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	outDir := t.TempDir()
	sdkOutput, err := Generate(apiOutput, "", "", nestedArraysClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	if err := WriteSDK(sdkOutput, outDir); err != nil {
		t.Fatalf("WriteSDK: %v", err)
	}
	compareGoldenTree(t, outDir, filepath.Join("testdata", "golden", rowFeaturesService))
	var source []byte
	err = filepath.WalkDir(outDir, func(path string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(path, filepath.Join("namespaces", "ticket.py")) {
			return err
		}
		source, err = os.ReadFile(path)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, each := range []string{"def backlinks(", "def complete_upload(", "RowAttachmentBegun"} {
		if !strings.Contains(string(source), each) {
			t.Errorf("namespaces/ticket.py lacks %q", each)
		}
	}
}
