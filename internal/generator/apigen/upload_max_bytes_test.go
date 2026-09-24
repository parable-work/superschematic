package apigen

import (
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGenerateUsesFieldUploadMaxBytesInMultipartContract: a field's
// Validate<T, { uploadMaxBytes }> replaces the upload scalar's own limit in
// the multipart handler and in the OpenAPI description.
func TestGenerateUsesFieldUploadMaxBytesInMultipartContract(t *testing.T) {
	limit := int64(64 * 1024 * 1024)
	schema := ir.NewSchema("upload-api", ir.SchemaKindAPI)
	schema.Scalars["Media.File"] = &ir.ScalarDef{
		Name:              "Media.File",
		LanguagePrimitive: ir.LanguageObject,
		FileUpload:        &ir.FileUploadConfig{MaxSize: 1024, Category: "file"},
	}
	schema.Types["UploadArchiveInput"] = &ir.TypeDef{
		Name: "UploadArchiveInput",
		Role: ir.RoleAPIInput,
		Fields: []*ir.FieldDef{
			{Name: "archive", TypeRef: ir.TypeRef{Name: "Media.File"}, Required: true, ValidateUploadMaxBytes: &limit},
			{Name: "thumbnail", TypeRef: ir.TypeRef{Name: "Media.File"}},
		},
	}
	schema.OperationSets = []*ir.OperationSet{{
		Name: "ArchiveMutations",
		Operations: []*ir.FieldDef{{
			Name:       "uploadArchive",
			HTTPMethod: "POST",
			RestPath:   "archives/actions/upload",
			TypeRef:    ir.TypeRef{Name: "string"},
			Arguments: []*ir.ArgumentDef{{
				Name: "input", TypeRef: ir.TypeRef{Name: "UploadArchiveInput"}, Required: true,
			}},
		}},
	}}

	output, err := Generate(schema, Options{
		Provider:    stubProvider{},
		SchemaName:  "upload-api",
		ModulePath:  "example.com/schemas/api/upload-api",
		TypesModule: "example.com/schemas/types/go/upload-api",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.Endpoints) != 1 || len(output.Endpoints[0].FileUploadFields) != 2 {
		t.Fatalf("multipart endpoint = %#v", output.Endpoints)
	}
	sizes := map[string]int64{}
	for _, field := range output.Endpoints[0].FileUploadFields {
		sizes[field.Name] = field.MaxSize
	}
	if sizes["archive"] != limit {
		t.Fatalf("archive max size = %d, want the field bound %d", sizes["archive"], limit)
	}
	if sizes["thumbnail"] != 1024 {
		t.Fatalf("thumbnail max size = %d, want the scalar limit 1024", sizes["thumbnail"])
	}
	if !strings.Contains(output.OpenAPISpecRaw, "max size: 67108864 bytes") {
		t.Fatal("OpenAPI did not advertise the field upload bound")
	}
}

// TestUploadFieldsComeFromFileUploadMetadataOnly: a field is an upload when
// its scalar carries fileUpload metadata. A scalar named like an upload
// without it is an ordinary field, and one with it becomes an upload
// whatever its name, keeping its category and image constraints.
func TestUploadFieldsComeFromFileUploadMetadataOnly(t *testing.T) {
	schema := ir.NewSchema("upload-api", ir.SchemaKindAPI)
	for _, name := range []string{"Artifact.File", "Asset.File", "Asset.Image", "Asset.LogoImage"} {
		schema.Scalars[name] = &ir.ScalarDef{Name: name, LanguagePrimitive: ir.LanguageString}
	}
	schema.Scalars["Media.Badge"] = &ir.ScalarDef{
		Name:              "Media.Badge",
		LanguagePrimitive: ir.LanguageObject,
		FileUpload:        &ir.FileUploadConfig{MaxSize: 2048, Category: "image", AllowedTypes: []string{"image/png"}},
		ImageConstraints:  &ir.ImageConstraints{RequireTransparency: true},
	}
	schema.Types["UploadBadgeInput"] = &ir.TypeDef{
		Name: "UploadBadgeInput",
		Role: ir.RoleAPIInput,
		Fields: []*ir.FieldDef{
			{Name: "artifact", TypeRef: ir.TypeRef{Name: "Artifact.File"}},
			{Name: "file", TypeRef: ir.TypeRef{Name: "Asset.File"}},
			{Name: "image", TypeRef: ir.TypeRef{Name: "Asset.Image"}},
			{Name: "logo", TypeRef: ir.TypeRef{Name: "Asset.LogoImage"}},
			{Name: "badge", TypeRef: ir.TypeRef{Name: "Media.Badge"}, Required: true},
		},
	}
	schema.OperationSets = []*ir.OperationSet{{
		Name: "BadgeMutations",
		Operations: []*ir.FieldDef{{
			Name:       "uploadBadge",
			HTTPMethod: "POST",
			RestPath:   "badges/actions/upload",
			TypeRef:    ir.TypeRef{Name: "string"},
			Arguments: []*ir.ArgumentDef{{
				Name: "input", TypeRef: ir.TypeRef{Name: "UploadBadgeInput"}, Required: true,
			}},
		}},
	}}

	output, err := Generate(schema, Options{
		Provider:    stubProvider{},
		SchemaName:  "upload-api",
		ModulePath:  "example.com/schemas/api/upload-api",
		TypesModule: "example.com/schemas/types/go/upload-api",
		Clock:       codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}
	if len(output.Endpoints) != 1 {
		t.Fatalf("endpoints = %#v", output.Endpoints)
	}
	fields := output.Endpoints[0].FileUploadFields
	if len(fields) != 1 || fields[0].Name != "badge" {
		t.Fatalf("upload fields = %#v, want only badge", fields)
	}
	badge := fields[0]
	if badge.Category != "image" || badge.MaxSize != 2048 || len(badge.AllowedTypes) != 1 ||
		badge.ImageConstraints == nil || !badge.ImageConstraints.RequireTransparency {
		t.Errorf("badge upload = %#v", badge)
	}
}
