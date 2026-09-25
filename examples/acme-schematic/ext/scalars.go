package ext

import (
	scalars "github.com/parable-work/superscalar/go"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// PhotoScalar is the scalar the extension adds to the core set: a product
// photo, uploaded as a multipart file part. Its TypeScript brand is
// Acme.Photo in @acme/schema.
const PhotoScalar = "Acme.Photo"

// PhotoUpload is the upload metadata the extension declares on PhotoScalar.
// A field may lower MaxSize with Validate<Acme.Photo, { uploadMaxBytes }>.
var PhotoUpload = registry.ScalarUpload{
	FileUpload: ir.FileUploadConfig{
		MaxSize:      8 << 20,
		AllowedTypes: []string{"image/jpeg", "image/png", "image/webp"},
		Category:     "image",
	},
}

// registerScalars replaces the core scalar catalog with the core rows plus
// PhotoScalar. The superscalar row type has no upload fields, so the upload
// metadata travels beside the rows through ScalarCatalogWithUploads; the
// loader hydrates it onto every schema that references Acme.Photo.
func registerScalars(r *registry.Registry) error {
	core := registry.CoreScalars()
	rows := make(map[string]*scalars.ScalarMetadata, len(core.Names())+1)
	for _, name := range core.Names() {
		row, _ := core.Scalar(name)
		rows[name] = row
	}
	rows[PhotoScalar] = &scalars.ScalarMetadata{
		CanonicalName:  PhotoScalar,
		Symbol:         "AcmePhoto",
		Primitive:      "String",
		Description:    "A product photo, uploaded as a multipart file part.",
		TypeScriptType: "string",
		PythonType:     "str",
		GoType:         "string",
	}
	catalog, err := registry.ScalarCatalogWithUploads(registry.ScalarCatalogOf(rows), map[string]registry.ScalarUpload{
		PhotoScalar: PhotoUpload,
	})
	if err != nil {
		return err
	}
	return r.RegisterScalars(Name, catalog)
}
