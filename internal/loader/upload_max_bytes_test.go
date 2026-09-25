package loader

import (
	"path/filepath"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// photoUploadCatalog is the core catalog plus Media.Photo, a file-upload
// scalar declared the way an extension's scalar package declares one: a
// metadata row, and the upload metadata beside it that the row has no field
// for.
func photoUploadCatalog(t *testing.T) registry.ScalarCatalog {
	t.Helper()
	rows := map[string]*scalars.ScalarMetadata{}
	for name, row := range scalars.ScalarMetadataByCanonical {
		rows[name] = row
	}
	rows["Media.Photo"] = &scalars.ScalarMetadata{
		CanonicalName:  "Media.Photo",
		Symbol:         "MediaPhoto",
		Primitive:      "String",
		Description:    "A photo uploaded as a multipart file part",
		TypeScriptType: "string",
		GoType:         "string",
	}
	catalog, err := registry.ScalarCatalogWithUploads(registry.ScalarCatalogOf(rows), map[string]registry.ScalarUpload{
		"Media.Photo": {
			FileUpload: ir.FileUploadConfig{
				MaxSize:      8 << 20,
				AllowedTypes: []string{"image/jpeg", "image/png"},
				Category:     "image",
			},
			ImageConstraints: &ir.ImageConstraints{MaxWidth: 4096},
		},
	})
	if err != nil {
		t.Fatalf("ScalarCatalogWithUploads: %v", err)
	}
	return catalog
}

// photoUploadDataForm is the data-form twin of the TypeScript fixture
// fixture-upload-max-bytes: Media.Photo is a bare catalog reference, so its
// upload metadata can only come from the catalog.
const photoUploadDataForm = `scalars:
  Media.Photo:
    name: Media.Photo
    languagePrimitive: string
types:
  PhotoUpload:
    name: PhotoUpload
    role: EmbeddedStruct
    fields:
      - name: photo
        typeRef: { name: Media.Photo }
        required: true
        validateUploadMaxBytes: 1048576
      - name: caption
        typeRef: { name: string }
        required: true
`

const photoUploadDataFormJSON = `{
  "scalars": {"Media.Photo": {"name": "Media.Photo", "languagePrimitive": "string"}},
  "types": {
    "PhotoUpload": {
      "name": "PhotoUpload",
      "role": "EmbeddedStruct",
      "fields": [
        {"name": "photo", "typeRef": {"name": "Media.Photo"}, "required": true, "validateUploadMaxBytes": 1048576},
        {"name": "caption", "typeRef": {"name": "string"}, "required": true}
      ]
    }
  }
}`

// TestUploadMaxBytesOnRegisteredUploadScalar: Validate<T, { uploadMaxBytes }>
// on a scalar the registered catalog declares a file upload loads in every
// authoring form, and the hydrated scalar carries the catalog's upload
// metadata. The TypeScript frontend used to check the bound before the
// loader hydrated the scalar, when a brand carries only its name, so a
// genuine upload scalar was always rejected.
func TestUploadMaxBytesOnRegisteredUploadScalar(t *testing.T) {
	services := map[string]string{
		"typescript": filepath.Join("tsreader", "testdata", "services", "fixture-upload-max-bytes"),
		"json": writeService(t, map[string]string{
			"schema.config.json":    minimalConfig,
			"src/photo.schema.json": photoUploadDataFormJSON,
		}),
		"yaml": writeService(t, map[string]string{
			"schema.config.yaml":    "name: temp-service\nkind: General\noutputs: {}\n",
			"src/photo.schema.yaml": photoUploadDataForm,
		}),
	}
	for _, form := range []string{"typescript", "json", "yaml"} {
		t.Run(form, func(t *testing.T) {
			schema, err := LoadService(services[form], WithRegistry(registryWithCatalog(t, photoUploadCatalog(t))))
			if err != nil {
				t.Fatalf("LoadService: %v", err)
			}
			photo := schema.Scalars["Media.Photo"]
			if photo == nil || photo.FileUpload == nil {
				t.Fatalf("Media.Photo was not hydrated as a file upload: %+v", photo)
			}
			if photo.FileUpload.MaxSize != 8<<20 || strings.Join(photo.FileUpload.AllowedTypes, ",") != "image/jpeg,image/png" || photo.FileUpload.Category != "image" {
				t.Fatalf("Media.Photo upload metadata = %+v, want the catalog's", photo.FileUpload)
			}
			if photo.ImageConstraints == nil || photo.ImageConstraints.MaxWidth != 4096 {
				t.Fatalf("Media.Photo image constraints = %+v, want the catalog's", photo.ImageConstraints)
			}
			var bound *int64
			for _, field := range schema.Types["PhotoUpload"].Fields {
				if field.Name == "photo" {
					bound = field.ValidateUploadMaxBytes
				}
			}
			if bound == nil || *bound != 1048576 {
				t.Fatalf("PhotoUpload.photo uploadMaxBytes = %v, want 1048576", bound)
			}
		})
	}
}

// TestUploadMaxBytesOnNonUploadScalarFails: the same schema against a
// catalog that knows Media.Photo but declares no upload metadata for it is
// rejected in every form, after hydration.
func TestUploadMaxBytesOnNonUploadScalarFails(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{}
	for name, row := range scalars.ScalarMetadataByCanonical {
		rows[name] = row
	}
	rows["Media.Photo"] = &scalars.ScalarMetadata{CanonicalName: "Media.Photo", Symbol: "MediaPhoto", Primitive: "String"}
	services := map[string]string{
		"typescript": filepath.Join("tsreader", "testdata", "services", "fixture-upload-max-bytes"),
		"json": writeService(t, map[string]string{
			"schema.config.json":    minimalConfig,
			"src/photo.schema.json": photoUploadDataFormJSON,
		}),
		"yaml": writeService(t, map[string]string{
			"schema.config.yaml":    "name: temp-service\nkind: General\noutputs: {}\n",
			"src/photo.schema.yaml": photoUploadDataForm,
		}),
	}
	for _, form := range []string{"typescript", "json", "yaml"} {
		t.Run(form, func(t *testing.T) {
			_, err := LoadService(services[form], WithRegistry(registryWithCatalog(t, registry.ScalarCatalogOf(rows))))
			if err == nil || !strings.Contains(err.Error(), "PhotoUpload.photo uploadMaxBytes requires a file-upload scalar") {
				t.Fatalf("want the file-upload scalar error, got %v", err)
			}
		})
	}
}

// TestUploadMaxBytesOnNonUploadFieldFails: the key reaches the IR from
// TypeScript, and the check after hydration rejects it on a field that is
// not a file-upload scalar.
func TestUploadMaxBytesOnNonUploadFieldFails(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-upload-max-bytes"))
	if err == nil || !strings.Contains(err.Error(), "Attachment.label uploadMaxBytes requires a file-upload scalar") {
		t.Fatalf("want the file-upload scalar error, got %v", err)
	}
}
