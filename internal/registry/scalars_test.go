package registry

import (
	"reflect"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func TestScalarsFallsBackToCoreScalars(t *testing.T) {
	reg := New(naming.Default())

	if got, want := reg.Scalars().Names(), CoreScalars().Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Scalars() without RegisterScalars = %d names, want the core's %d", len(got), len(want))
	}
	if _, ok := reg.Scalars().Scalar("Identity.UUID"); !ok {
		t.Fatal("core catalog does not resolve Identity.UUID")
	}
}

func TestRegisterScalarsInstallsOneCatalogPerRegistry(t *testing.T) {
	reg := New(naming.Default())
	row := scalars.ScalarMetadataByCanonical["Identity.UUID"]
	first := ScalarCatalogOf(map[string]*scalars.ScalarMetadata{"Identity.UUID": row})

	if err := reg.RegisterScalars("acme", first); err != nil {
		t.Fatalf("RegisterScalars: %v", err)
	}
	if got := reg.Scalars().Names(); !reflect.DeepEqual(got, []string{"Identity.UUID"}) {
		t.Fatalf("Scalars().Names() = %v, want the registered catalog", got)
	}
	if got := reg.Extensions(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("Extensions() = %v, want the catalog owner", got)
	}

	err := reg.RegisterScalars("other", CoreScalars())
	if err == nil || !strings.Contains(err.Error(), "already registered by acme") || !strings.Contains(err.Error(), "other") {
		t.Fatalf("second RegisterScalars: err = %v, want both owners named", err)
	}
	if got := reg.Scalars().Names(); !reflect.DeepEqual(got, []string{"Identity.UUID"}) {
		t.Fatalf("a rejected second catalog replaced the first: %v", got)
	}
}

func TestRegisterScalarsRejectsMissingOwnerAndNilCatalog(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterScalars("", CoreScalars()); err == nil || !strings.Contains(err.Error(), "no owner") {
		t.Fatalf("empty owner: err = %v", err)
	}
	if err := reg.RegisterScalars("acme", nil); err == nil || !strings.Contains(err.Error(), "nil scalar catalog") {
		t.Fatalf("nil catalog: err = %v", err)
	}
	if reg.scalars != nil {
		t.Fatal("a rejected RegisterScalars left a catalog installed")
	}
}

func TestRegisterScalarsAfterFinalizeIsAnError(t *testing.T) {
	reg := New(naming.Default())
	for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig"} {
		if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	err := reg.RegisterScalars("acme", CoreScalars())
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("RegisterScalars after Finalize: err = %v", err)
	}
}

func TestScalarCatalogOfSkipsNilRows(t *testing.T) {
	catalog := ScalarCatalogOf(map[string]*scalars.ScalarMetadata{
		"Identity.UUID": scalars.ScalarMetadataByCanonical["Identity.UUID"],
		"Acme.Gone":     nil,
	})
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{"Identity.UUID"}) {
		t.Fatalf("Names() = %v, want nil rows dropped", got)
	}
	if _, ok := catalog.Scalar("Acme.Gone"); ok {
		t.Fatal("Scalar() resolved a nil row")
	}
}

func TestScalarCatalogWithUploadsDeclaresUploadScalars(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{
		"Identity.UUID": scalars.ScalarMetadataByCanonical["Identity.UUID"],
		"Media.Photo":   {CanonicalName: "Media.Photo", Symbol: "MediaPhoto", Primitive: "String"},
	}
	ratio := 1.0
	declared := map[string]ScalarUpload{
		"Media.Photo": {
			FileUpload:       ir.FileUploadConfig{MaxSize: 1024, AllowedTypes: []string{"image/png"}, Category: "image"},
			ImageConstraints: &ir.ImageConstraints{MaxWidth: 512, MinAspectRatio: &ratio},
		},
	}
	catalog, err := ScalarCatalogWithUploads(ScalarCatalogOf(rows), declared)
	if err != nil {
		t.Fatalf("ScalarCatalogWithUploads: %v", err)
	}
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{"Identity.UUID", "Media.Photo"}) {
		t.Fatalf("Names() = %v, want the wrapped catalog's", got)
	}
	if _, ok := catalog.Upload("Identity.UUID"); ok {
		t.Fatal("Upload() declared Identity.UUID a file upload")
	}
	upload, ok := catalog.Upload("Media.Photo")
	if !ok || upload.FileUpload.MaxSize != 1024 || upload.ImageConstraints == nil || upload.ImageConstraints.MaxWidth != 512 {
		t.Fatalf("Upload(Media.Photo) = %+v, %v; want the declared metadata", upload, ok)
	}

	// The catalog keeps its own copy: neither the declaring map nor a
	// caller that edits what Upload returned changes the next answer.
	declared["Media.Photo"].FileUpload.AllowedTypes[0] = "text/plain"
	ratio = 9
	upload.FileUpload.AllowedTypes[0] = "text/csv"
	upload.ImageConstraints.MaxWidth = 1
	again, _ := catalog.Upload("Media.Photo")
	if again.FileUpload.AllowedTypes[0] != "image/png" || again.ImageConstraints.MaxWidth != 512 || *again.ImageConstraints.MinAspectRatio != 1 {
		t.Fatalf("Upload(Media.Photo) after edits = %+v, want the declared metadata", again)
	}
}

func TestScalarCatalogWithUploadsRejectsUnknownScalarAndNilCatalog(t *testing.T) {
	uploads := map[string]ScalarUpload{"Media.Photo": {FileUpload: ir.FileUploadConfig{MaxSize: 1024}}}
	_, err := ScalarCatalogWithUploads(CoreScalars(), uploads)
	if err == nil || !strings.Contains(err.Error(), "Media.Photo") || !strings.Contains(err.Error(), "does not define") {
		t.Fatalf("upload on an unknown scalar: err = %v, want it named", err)
	}
	if _, err := ScalarCatalogWithUploads(nil, uploads); err == nil || !strings.Contains(err.Error(), "nil scalar catalog") {
		t.Fatalf("nil catalog: err = %v", err)
	}
}
