package typegen

import (
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAJSONScalarWithoutAParserImportsNoJSON: a JSON-object scalar whose
// row has no parse function gets no Parse alias, so scalars.go calls no
// json function and does not import encoding/json. The import was
// written for it anyway, and Go refuses an unused import.
func TestAJSONScalarWithoutAParserImportsNoJSON(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatal(err)
	}
	schema.Scalars["Doc.Body"] = &ir.ScalarDef{
		Name:              "Doc.Body",
		LanguagePrimitive: ir.LanguageObject,
		TypeMappings:      map[string]string{"go": "DocBody", "json_schema": ir.JSONSchemaObjectType},
	}
	output, err := Generate(schema, Options{
		SchemaName: "fixture-general",
		ModulePath: "example.com/schemas/types/go/json-scalar",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	source, err := os.ReadFile(filepath.Join(dir, "scalars.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(source), "type DocBody = ") {
		t.Fatalf("scalars.go does not declare DocBody:\n%s", source)
	}
	file, err := parser.ParseFile(token.NewFileSet(), "scalars.go", source, parser.ImportsOnly)
	if err != nil {
		t.Fatal(err)
	}
	for _, spec := range file.Imports {
		if path, _ := strconv.Unquote(spec.Path.Value); path == "encoding/json" && !strings.Contains(string(source), "json.") {
			t.Fatalf("scalars.go imports encoding/json and calls no json function:\n%s", source)
		}
	}
}
