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

// TestGenerateRefusesFileDownloadWithoutManualRegistration: the Rust
// router answers JSON, so it refuses a FileDownload result (D81) unless the
// service mounts the route and answers the file; the refusal names the fix
// and says support follows.
func TestGenerateRefusesFileDownloadWithoutManualRegistration(t *testing.T) {
	for _, manual := range []bool{false, true} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, nestedArraysService))
		if err != nil {
			t.Fatalf("load %s: %v", nestedArraysService, err)
		}
		for _, set := range schema.OperationSets {
			if set.Name != "GridMutations" {
				continue
			}
			set.Operations = append(set.Operations, &ir.FieldDef{
				Name:                    "exportGrid",
				Required:                true,
				FileDownload:            &ir.FileDownloadDef{ContentType: "text/csv", Filename: "grid.csv"},
				HTTPMethod:              "GET",
				RestPath:                "grid-exports",
				ManualRouteRegistration: manual,
			})
		}
		output, err := generateFrom(schema, apiSource{}, Options{
			SchemaName: nestedArraysService,
			TypesCrate: "schemas-" + nestedArraysService + "-types",
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		})
		if !manual {
			if err == nil || !strings.Contains(err.Error(), "operation grid.exportGrid answers a FileDownload") ||
				!strings.Contains(err.Error(), "@manualRouteRegistration") || !strings.Contains(err.Error(), "support follows") {
				t.Fatalf("Generate = %v, want the refusal of grid.exportGrid", err)
			}
			continue
		}
		if err != nil {
			t.Fatalf("Generate with a manual download: %v", err)
		}
		if len(output.ManualEndpoints) != 1 || output.ManualEndpoints[0].Name != "exportGrid" {
			t.Fatalf("ManualEndpoints = %+v, want grid.exportGrid", output.ManualEndpoints)
		}
	}
}
