package rustrestgen

import (
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// addUploadOperation adds grid.uploadGrid to fixture-nested-arrays-api: a
// POST whose input type has a file-upload field, so its body is multipart.
func addUploadOperation(schema *ir.Schema, manual bool) {
	const fileScalar = "Upload.File"
	schema.Scalars[fileScalar] = &ir.ScalarDef{Name: fileScalar, LanguagePrimitive: ir.LanguageString, FileUpload: &ir.FileUploadConfig{}}
	schema.Types["GridUpload"] = &ir.TypeDef{
		Name:   "GridUpload",
		Role:   ir.RoleAPIInput,
		Fields: []*ir.FieldDef{{Name: "file", TypeRef: ir.TypeRef{Name: fileScalar}, Required: true}},
	}
	for _, set := range schema.OperationSets {
		if set.Name != "GridMutations" {
			continue
		}
		set.Operations = append(set.Operations, &ir.FieldDef{
			Name:                    "uploadGrid",
			TypeRef:                 ir.TypeRef{Name: "boolean"},
			Required:                true,
			Arguments:               []*ir.ArgumentDef{{Name: "input", TypeRef: ir.TypeRef{Name: "GridUpload"}, Required: true}},
			HTTPMethod:              "POST",
			RestPath:                "grid-uploads",
			ManualRouteRegistration: manual,
		})
	}
}

// TestGenerateRefusesFileUploadWithoutManualRegistration: the Rust
// router's handlers take JSON, so it refuses a file upload unless the
// service mounts the route and reads the multipart body, as the TypeScript
// router does.
func TestGenerateRefusesFileUploadWithoutManualRegistration(t *testing.T) {
	for _, manual := range []bool{false, true} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysService))
		if err != nil {
			t.Fatalf("load %s: %v", nestedArraysService, err)
		}
		addUploadOperation(schema, manual)
		output, err := generateFrom(schema, apiSource{}, Options{
			SchemaName: nestedArraysService,
			TypesCrate: "schemas-" + nestedArraysService + "-types",
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		})
		if !manual {
			if err == nil || !strings.Contains(err.Error(), "operation grid.uploadGrid uploads files") || !strings.Contains(err.Error(), "@manualRouteRegistration") {
				t.Fatalf("Generate = %v, want the refusal of grid.uploadGrid", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Generate with a manual upload: %v", err)
		}
		if len(output.ManualEndpoints) != 1 || output.ManualEndpoints[0].Name != "uploadGrid" {
			t.Fatalf("ManualEndpoints = %+v, want grid.uploadGrid", output.ManualEndpoints)
		}
	}
}
