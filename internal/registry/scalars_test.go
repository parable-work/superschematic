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
	for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig", "stack"} {
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

// TestRegisterScalarsRefusesObjectRowWithoutJSONMapping: a row whose
// primitive the loader reads as object must say which JSON the scalar holds
// through its JSONSchemaType. A catalog with rows that do not is refused,
// each one named with the routes, and nothing is installed. The same rows
// with object, array or any register.
func TestRegisterScalarsRefusesObjectRowWithoutJSONMapping(t *testing.T) {
	rows := func(jsonType string) map[string]*scalars.ScalarMetadata {
		return map[string]*scalars.ScalarMetadata{
			"Identity.UUID": scalars.ScalarMetadataByCanonical["Identity.UUID"],
			"Acme.Blob":     {CanonicalName: "Acme.Blob", Symbol: "AcmeBlob", Primitive: "Object", JSONSchemaType: jsonType},
			"Acme.Doc":      {CanonicalName: "Acme.Doc", Symbol: "AcmeDoc", Primitive: "JSON", JSONSchemaType: jsonType},
		}
	}

	for _, jsonType := range []string{"", "string"} {
		reg := New(naming.Default())
		err := reg.RegisterScalars("acme", ScalarCatalogOf(rows(jsonType)))
		if err == nil {
			t.Fatalf("JSONSchemaType %q: RegisterScalars accepted object rows that do not say which JSON they hold", jsonType)
		}
		for _, want := range []string{
			"registry: acme's scalar catalog: scalar Acme.Blob has language primitive object but no json_schema type mapping of object, array or any to say which JSON it holds",
			"registry: acme's scalar catalog: scalar Acme.Doc has language primitive object but no json_schema type mapping",
			"add typeMappings: { json_schema: object } (or array or any; JSONSchemaType in a catalog row)",
			"use the catalog's Generic.JSON for free-form JSON",
			"model a value with known fields as a nested object type",
		} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("JSONSchemaType %q: error %q does not say %q", jsonType, err.Error(), want)
			}
		}
		if strings.Contains(err.Error(), "Identity.UUID") {
			t.Errorf("JSONSchemaType %q: error %q names a string row", jsonType, err.Error())
		}
		if reg.scalars != nil || len(reg.Extensions()) != 0 {
			t.Fatalf("JSONSchemaType %q: a refused catalog was installed", jsonType)
		}
	}

	for _, jsonType := range []string{"object", "array", "any"} {
		reg := New(naming.Default())
		if err := reg.RegisterScalars("acme", ScalarCatalogOf(rows(jsonType))); err != nil {
			t.Errorf("JSONSchemaType %q: RegisterScalars: %v", jsonType, err)
		}
	}
}

// TestRegisterScalarsNamesAnUnknownPrimitive: the loader reads a primitive
// it does not know as object, so a row with one and no JSON mapping is
// refused with a message of its own: it names the scalar once, the
// primitive the row wrote, and the spellings it may have meant. With a JSON
// mapping it is an object scalar and registers, as before.
func TestRegisterScalarsNamesAnUnknownPrimitive(t *testing.T) {
	row := &scalars.ScalarMetadata{CanonicalName: "Acme.Id", Symbol: "AcmeId", Primitive: "Uuid"}
	err := New(naming.Default()).RegisterScalars("acme", ScalarCatalogOf(map[string]*scalars.ScalarMetadata{"Acme.Id": row}))
	want := `registry: acme's scalar catalog: scalar Acme.Id has primitive "Uuid", which the loader does not know and reads as object, so the validators check its values as strings: ` +
		`spell the primitive String, Int, Float or Bool, or, for a scalar that holds JSON, Object with a JSONSchemaType of any, or of object or array with no pattern and no length`
	if err == nil || err.Error() != want {
		t.Fatalf("RegisterScalars: err = %v\nwant %s", err, want)
	}

	// With an object mapping and a length, the same message: following it
	// to Object alone would meet the length's refusal, so it names both.
	lengthy := *row
	lengthy.JSONSchemaType = "object"
	lengthy.MaxLength = 10
	err = New(naming.Default()).RegisterScalars("acme", ScalarCatalogOf(map[string]*scalars.ScalarMetadata{"Acme.Id": &lengthy}))
	if err == nil || err.Error() != want {
		t.Fatalf("RegisterScalars with an object mapping and a length: err = %v\nwant %s", err, want)
	}

	mapped := *row
	mapped.JSONSchemaType = "object"
	if err := New(naming.Default()).RegisterScalars("acme", ScalarCatalogOf(map[string]*scalars.ScalarMetadata{"Acme.Id": &mapped})); err != nil {
		t.Fatalf("RegisterScalars with a JSON mapping: %v", err)
	}
}

// TestRegisterScalarsJudgesARowAsTheValidatorsDo: a row is read as the
// loader fills a scalar in from it. An object row whose JSONSchemaType is
// object or array but which has a pattern or a length, rules on a string,
// is refused with them named, and so is an object row the catalog declares
// as a file upload, with a JSON mapping or without: it takes the String
// primitive. Geo.Location, a JSON object with the String primitive, is no
// object-primitive row and passes.
func TestRegisterScalarsJudgesARowAsTheValidatorsDo(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{
		"Geo.Location": scalars.ScalarMetadataByCanonical["Geo.Location"],
		"Acme.Blob":    {CanonicalName: "Acme.Blob", Symbol: "AcmeBlob", Primitive: "Object", JSONSchemaType: "object", MaxLength: 10},
		"Media.Photo":  {CanonicalName: "Media.Photo", Symbol: "MediaPhoto", Primitive: "Object"},
		"Media.Scan":   {CanonicalName: "Media.Scan", Symbol: "MediaScan", Primitive: "Object", JSONSchemaType: "object"},
	}
	catalog, err := ScalarCatalogWithUploads(ScalarCatalogOf(rows), map[string]ScalarUpload{
		"Media.Photo": {FileUpload: ir.FileUploadConfig{MaxSize: 1024}},
		"Media.Scan":  {FileUpload: ir.FileUploadConfig{MaxSize: 1024}},
	})
	if err != nil {
		t.Fatal(err)
	}
	reg := New(naming.Default())
	err = reg.RegisterScalars("acme", catalog)
	if err == nil {
		t.Fatal("RegisterScalars accepted object rows the validators check as strings")
	}
	for _, want := range []string{
		"registry: acme's scalar catalog: scalar Acme.Blob has language primitive object and json_schema type mapping object, but its maxLength 10 is a rule on a string, so the validators check its values as strings: drop the maxLength",
		"registry: acme's scalar catalog: scalar Media.Photo is a file-upload scalar with language primitive object: a file part is not JSON, so " +
			"an upload scalar takes the string primitive (languagePrimitive: string; Primitive String in a catalog row)",
		// A file part is no JSON, whatever the mapping says.
		"registry: acme's scalar catalog: scalar Media.Scan is a file-upload scalar with language primitive object: a file part is not JSON, so " +
			"an upload scalar takes the string primitive (languagePrimitive: string; Primitive String in a catalog row)",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not say %q", err.Error(), want)
		}
	}
	if strings.Contains(err.Error(), "Geo.Location") {
		t.Errorf("error %q names Geo.Location, which has the String primitive", err.Error())
	}
	if reg.scalars != nil {
		t.Fatal("a refused catalog was installed")
	}

	rows["Acme.Blob"] = &scalars.ScalarMetadata{CanonicalName: "Acme.Blob", Symbol: "AcmeBlob", Primitive: "Object", JSONSchemaType: "object"}
	rows["Media.Photo"] = &scalars.ScalarMetadata{CanonicalName: "Media.Photo", Symbol: "MediaPhoto", Primitive: "String"}
	rows["Media.Scan"] = &scalars.ScalarMetadata{CanonicalName: "Media.Scan", Symbol: "MediaScan", Primitive: "String"}
	if err := New(naming.Default()).RegisterScalars("acme", catalog); err != nil {
		t.Fatalf("RegisterScalars with the length dropped and the upload a string: %v", err)
	}
}

// TestCoreScalarsSayWhichJSONTheyHold: no core row is an object scalar
// without a JSON mapping, so the core catalog, which the loader falls back
// to with no registration, passes the rule, and so does an extension's
// catalog that copies its rows in, as acme's does.
func TestCoreScalarsSayWhichJSONTheyHold(t *testing.T) {
	if err := checkObjectScalarRows("core", CoreScalars()); err != nil {
		t.Fatalf("core catalog: %v", err)
	}
	if err := New(naming.Default()).RegisterScalars("acme", CoreScalars()); err != nil {
		t.Fatalf("RegisterScalars(core rows): %v", err)
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

// jsonKeysCheck is a raw-body check for the tests below.
var jsonKeysCheck = ScalarRawBodyCheck{
	ImportPath:  "example.com/checks/jsonkeys",
	PackageName: "jsonkeys",
	Func:        "DuplicateKeyErrors",
	ErrorsVar:   "keyErrors",
	Comment:     "Check the raw body first.",
}

func TestScalarCatalogWithRawBodyChecksDeclaresChecks(t *testing.T) {
	checks := map[string]ScalarRawBodyCheck{"Generic.JSON": jsonKeysCheck}
	catalog, err := ScalarCatalogWithRawBodyChecks(CoreScalars(), checks)
	if err != nil {
		t.Fatalf("ScalarCatalogWithRawBodyChecks: %v", err)
	}
	if got, want := catalog.Names(), CoreScalars().Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Names() = %d names, want the wrapped catalog's %d", len(got), len(want))
	}
	if check, ok := catalog.RawBodyCheck("Generic.JSON"); !ok || check != jsonKeysCheck {
		t.Fatalf("RawBodyCheck(Generic.JSON) = %+v, %v; want the declared check", check, ok)
	}
	if _, ok := catalog.RawBodyCheck("Identity.UUID"); ok {
		t.Fatal("RawBodyCheck() declared a check nobody registered")
	}

	// The catalog keeps its own copy of the map.
	checks["Identity.UUID"] = jsonKeysCheck
	if _, ok := catalog.RawBodyCheck("Identity.UUID"); ok {
		t.Fatal("an edit to the declaring map reached the catalog")
	}
}

func TestScalarCatalogWithRawBodyChecksRejectsUnknownScalarInvalidCheckAndNilCatalog(t *testing.T) {
	_, err := ScalarCatalogWithRawBodyChecks(CoreScalars(), map[string]ScalarRawBodyCheck{"Media.Note": jsonKeysCheck})
	if err == nil || !strings.Contains(err.Error(), "Media.Note") || !strings.Contains(err.Error(), "does not define") {
		t.Fatalf("check on an unknown scalar: err = %v, want it named", err)
	}
	invalid := jsonKeysCheck
	invalid.Func = "duplicateKeyErrors"
	_, err = ScalarCatalogWithRawBodyChecks(CoreScalars(), map[string]ScalarRawBodyCheck{"Generic.JSON": invalid})
	if err == nil || !strings.Contains(err.Error(), "Generic.JSON") || !strings.Contains(err.Error(), "exported") {
		t.Fatalf("invalid check: err = %v, want the scalar and the rule named", err)
	}
	if _, err := ScalarCatalogWithRawBodyChecks(nil, map[string]ScalarRawBodyCheck{"Generic.JSON": jsonKeysCheck}); err == nil || !strings.Contains(err.Error(), "nil scalar catalog") {
		t.Fatalf("nil catalog: err = %v", err)
	}
}

// TestUploadsAndRawBodyChecksComposeInEitherOrder: a distribution with an
// upload scalar and a checked scalar wraps its catalog twice, and the outer
// wrapper answers for both, whichever it is.
func TestUploadsAndRawBodyChecksComposeInEitherOrder(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{
		"Generic.JSON": scalars.ScalarMetadataByCanonical["Generic.JSON"],
		"Media.Photo":  {CanonicalName: "Media.Photo", Symbol: "MediaPhoto", Primitive: "String"},
	}
	uploads := map[string]ScalarUpload{"Media.Photo": {FileUpload: ir.FileUploadConfig{MaxSize: 1024}}}
	checks := map[string]ScalarRawBodyCheck{"Generic.JSON": jsonKeysCheck}

	uploadsFirst, err := ScalarCatalogWithUploads(ScalarCatalogOf(rows), uploads)
	if err != nil {
		t.Fatal(err)
	}
	checksOuter, err := ScalarCatalogWithRawBodyChecks(uploadsFirst, checks)
	if err != nil {
		t.Fatal(err)
	}
	checksFirst, err := ScalarCatalogWithRawBodyChecks(ScalarCatalogOf(rows), checks)
	if err != nil {
		t.Fatal(err)
	}
	uploadsOuter, err := ScalarCatalogWithUploads(checksFirst, uploads)
	if err != nil {
		t.Fatal(err)
	}

	for name, catalog := range map[string]ScalarCatalog{"checks outside": checksOuter, "uploads outside": uploadsOuter} {
		declaresUploads, ok := catalog.(UploadCatalog)
		if !ok {
			t.Fatalf("%s: not an UploadCatalog", name)
		}
		if upload, ok := declaresUploads.Upload("Media.Photo"); !ok || upload.FileUpload.MaxSize != 1024 {
			t.Errorf("%s: Upload(Media.Photo) = %+v, %v", name, upload, ok)
		}
		declaresChecks, ok := catalog.(RawBodyCheckCatalog)
		if !ok {
			t.Fatalf("%s: not a RawBodyCheckCatalog", name)
		}
		if check, ok := declaresChecks.RawBodyCheck("Generic.JSON"); !ok || check != jsonKeysCheck {
			t.Errorf("%s: RawBodyCheck(Generic.JSON) = %+v, %v", name, check, ok)
		}

		reg := New(naming.Default())
		if err := reg.RegisterScalars("acme", catalog); err != nil {
			t.Fatal(err)
		}
		lookup := reg.RawBodyChecks()
		if lookup == nil {
			t.Fatalf("%s: RawBodyChecks() = nil", name)
		}
		if check, ok := lookup.RawBodyCheck("Generic.JSON"); !ok || check != jsonKeysCheck {
			t.Errorf("%s: RawBodyChecks().RawBodyCheck(Generic.JSON) = %+v, %v", name, check, ok)
		}
	}
}

func TestRawBodyChecksIsNilForACatalogThatDeclaresNone(t *testing.T) {
	if lookup := New(naming.Default()).RawBodyChecks(); lookup != nil {
		t.Fatalf("RawBodyChecks() over the core catalog = %v, want nil", lookup)
	}
}

func TestScalarCatalogWithNpmPackagesNamesANamespacesPackage(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{
		"Generic.JSON": scalars.ScalarMetadataByCanonical["Generic.JSON"],
		"Acme.Photo":   {CanonicalName: "Acme.Photo", Symbol: "AcmePhoto", Primitive: "String"},
	}
	packages := map[string]string{"Acme": "@acme/schema"}
	catalog, err := ScalarCatalogWithNpmPackages(ScalarCatalogOf(rows), packages)
	if err != nil {
		t.Fatalf("ScalarCatalogWithNpmPackages: %v", err)
	}
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{"Acme.Photo", "Generic.JSON"}) {
		t.Fatalf("Names() = %v, want the wrapped catalog's", got)
	}
	if pkg, ok := catalog.NpmPackage("Acme"); !ok || pkg != "@acme/schema" {
		t.Fatalf("NpmPackage(Acme) = %q, %v; want @acme/schema", pkg, ok)
	}
	if pkg, ok := catalog.NpmPackage("Generic"); ok {
		t.Fatalf("NpmPackage(Generic) = %q; want none, so the writer falls back to the scalar library", pkg)
	}
	if pkg, ok := NpmPackageOf(catalog, "Acme"); !ok || pkg != "@acme/schema" {
		t.Fatalf("NpmPackageOf(catalog, Acme) = %q, %v; want @acme/schema", pkg, ok)
	}
	for name, other := range map[string]ScalarCatalog{"nil": nil, "core": CoreScalars()} {
		if pkg, ok := NpmPackageOf(other, "Acme"); ok {
			t.Fatalf("NpmPackageOf(%s, Acme) = %q; want none", name, pkg)
		}
	}

	// The catalog keeps its own copy of the map.
	packages["Generic"] = "@acme/scalars"
	if _, ok := catalog.NpmPackage("Generic"); ok {
		t.Fatal("an edit to the declaring map reached the catalog")
	}
}

func TestScalarCatalogWithNpmPackagesRejectsAnUnknownNamespaceAPathAndNilCatalog(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{
		"Acme.Photo": {CanonicalName: "Acme.Photo", Symbol: "AcmePhoto", Primitive: "String"},
	}
	for _, c := range []struct {
		packages map[string]string
		want     []string
	}{
		{map[string]string{"Media": "@acme/schema"}, []string{`namespace "Media"`, "no scalar of the catalog"}},
		// A key is a namespace, not a canonical name.
		{map[string]string{"Acme.Photo": "@acme/schema"}, []string{`namespace "Acme.Photo"`, "no scalar of the catalog"}},
		{map[string]string{"Acme": "./scalars"}, []string{"namespace Acme", `"./scalars" is not an npm package name`}},
		{map[string]string{"Acme": "/srv/scalars"}, []string{"namespace Acme", `"/srv/scalars" is not an npm package name`}},
		{map[string]string{"Acme": ""}, []string{"namespace Acme", `"" is not an npm package name`}},
	} {
		_, err := ScalarCatalogWithNpmPackages(ScalarCatalogOf(rows), c.packages)
		if err == nil {
			t.Fatalf("%v: no error", c.packages)
		}
		for _, want := range c.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%v: err = %v, want it to contain %q", c.packages, err, want)
			}
		}
	}
	if _, err := ScalarCatalogWithNpmPackages(nil, map[string]string{"Acme": "@acme/schema"}); err == nil || !strings.Contains(err.Error(), "nil scalar catalog") {
		t.Fatalf("nil catalog: err = %v", err)
	}
}

// TestNpmPackagesUploadsAndRawBodyChecksComposeInAnyOrder: a distribution
// whose scalars live in its own package, with an upload scalar and a
// checked scalar, wraps its catalog three times, and the outermost wrapper
// answers for all three, whichever order it wrapped them in.
func TestNpmPackagesUploadsAndRawBodyChecksComposeInAnyOrder(t *testing.T) {
	rows := map[string]*scalars.ScalarMetadata{
		"Generic.JSON": scalars.ScalarMetadataByCanonical["Generic.JSON"],
		"Media.Photo":  {CanonicalName: "Media.Photo", Symbol: "MediaPhoto", Primitive: "String"},
	}
	wraps := map[string]func(ScalarCatalog) (ScalarCatalog, error){
		"packages": func(c ScalarCatalog) (ScalarCatalog, error) {
			return ScalarCatalogWithNpmPackages(c, map[string]string{"Media": "@media/scalars"})
		},
		"uploads": func(c ScalarCatalog) (ScalarCatalog, error) {
			return ScalarCatalogWithUploads(c, map[string]ScalarUpload{"Media.Photo": {FileUpload: ir.FileUploadConfig{MaxSize: 1024}}})
		},
		"checks": func(c ScalarCatalog) (ScalarCatalog, error) {
			return ScalarCatalogWithRawBodyChecks(c, map[string]ScalarRawBodyCheck{"Generic.JSON": jsonKeysCheck})
		},
	}
	for _, order := range [][]string{
		{"packages", "uploads", "checks"}, {"packages", "checks", "uploads"},
		{"uploads", "packages", "checks"}, {"uploads", "checks", "packages"},
		{"checks", "packages", "uploads"}, {"checks", "uploads", "packages"},
	} {
		name := strings.Join(order, " then ")
		catalog := ScalarCatalogOf(rows)
		for _, wrap := range order {
			var err error
			if catalog, err = wraps[wrap](catalog); err != nil {
				t.Fatalf("%s: %s: %v", name, wrap, err)
			}
		}
		reg := New(naming.Default())
		if err := reg.RegisterScalars("acme", catalog); err != nil {
			t.Fatalf("%s: RegisterScalars: %v", name, err)
		}
		if pkg, ok := NpmPackageOf(reg.Scalars(), "Media"); !ok || pkg != "@media/scalars" {
			t.Errorf("%s: NpmPackage(Media) = %q, %v", name, pkg, ok)
		}
		declaresUploads, ok := reg.Scalars().(UploadCatalog)
		if !ok {
			t.Fatalf("%s: not an UploadCatalog", name)
		}
		if upload, ok := declaresUploads.Upload("Media.Photo"); !ok || upload.FileUpload.MaxSize != 1024 {
			t.Errorf("%s: Upload(Media.Photo) = %+v, %v", name, upload, ok)
		}
		if check, ok := reg.RawBodyChecks().RawBodyCheck("Generic.JSON"); !ok || check != jsonKeysCheck {
			t.Errorf("%s: RawBodyCheck(Generic.JSON) = %+v, %v", name, check, ok)
		}
	}
}
