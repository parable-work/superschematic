package apigen_test

import (
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// rawBodyCheckAPI is a schema local to apigen's testdata: input types with
// Generic.JSON fields, decoded from a JSON body (saveNote) or from a JSON
// body or a multipart form (attachPhoto).
const rawBodyCheckAPI = "raw-body-check-api"

// rawBodyChecks looks raw-body checks up in a map, as a scalar catalog that
// declares them does.
type rawBodyChecks map[string]apigen.RawBodyCheck

func (c rawBodyChecks) RawBodyCheck(scalar string) (apigen.RawBodyCheck, bool) {
	check, ok := c[scalar]
	return check, ok
}

// documentedKeyCheck is a check with a result variable and a comment of its
// own: the shape a distribution registers to keep the output of a generator
// it ported.
var documentedKeyCheck = apigen.RawBodyCheck{
	ImportPath:  "example.com/checks/jsonkeys",
	PackageName: "jsonkeys",
	Func:        "DuplicateKeyErrors",
	ErrorsVar:   "keyErrors",
	Comment:     "Decoding keeps the last of two equal keys, so check\nthe raw body first.",
}

func loadRawBodyCheckAPI(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join("testdata", "services", rawBodyCheckAPI))
	if err != nil {
		t.Fatalf("load %s: %v", rawBodyCheckAPI, err)
	}
	return schema
}

func generateRawBodyCheckAPI(t *testing.T, schema *ir.Schema, checks apigen.RawBodyChecks) *apigen.APIOutput {
	t.Helper()
	output, err := apigen.Generate(schema, apigen.Options{
		Provider:      sessionauth.Provider{},
		SchemaName:    rawBodyCheckAPI,
		ModulePath:    "example.com/schemas/api/" + rawBodyCheckAPI,
		TypesModule:   "example.com/schemas/types/go/" + rawBodyCheckAPI,
		Clock:         goModuleClock,
		RawBodyChecks: checks,
	})
	if err != nil {
		t.Fatalf("apigen.Generate: %v", err)
	}
	return output
}

func writeRoutes(t *testing.T, output *apigen.APIOutput) string {
	t.Helper()
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	routes, err := os.ReadFile(filepath.Join(outDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(routes)
}

// TestWriteAPIGoldenRawBodyChecks pins routes.go of raw-body-check-api
// with a check registered for Generic.JSON. routes.go imports the check's
// package, and each route calls the check on the raw JSON of the body
// before it decodes the body, in all three decode paths: a JSON body
// (saveNote), and a JSON body or the data part of a multipart form
// (attachPhoto). The call names the input's single-valued Generic.JSON
// fields, binds the check's result variable, and carries its comment in
// the two JSON-body paths. Regenerate with
// go test ./internal/generator/apigen -run TestWriteAPIGoldenRawBodyChecks -update
func TestWriteAPIGoldenRawBodyChecks(t *testing.T) {
	output := generateRawBodyCheckAPI(t, loadRawBodyCheckAPI(t), rawBodyChecks{"Generic.JSON": documentedKeyCheck})
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("apigen.WriteAPI: %v", err)
	}
	checkGoldenFiles(t, outDir, filepath.Join("testdata", "golden", rawBodyCheckAPI), []string{"routes.go"})

	routes, err := os.ReadFile(filepath.Join(outDir, "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`jsonkeys "example.com/checks/jsonkeys"`,
		// saveNote: the JSON body.
		"\t\t// Decoding keeps the last of two equal keys, so check\n\t\t// the raw body first.\n" +
			"\t\tif keyErrors := jsonkeys.DuplicateKeyErrors(rawInput, \"body\", \"meta\"); keyErrors.HasErrors() {\n" +
			"\t\t\tRespondInputRefusal(w, r, bodyargs.Mismatch(\"validation failed\", keyErrors))\n\t\t\treturn\n\t\t}\n" +
			"\t\tif err := json.Unmarshal(rawInput, &input); err != nil {",
		// attachPhoto: a JSON body.
		"\t\t\t// Decoding keeps the last of two equal keys, so check\n\t\t\t// the raw body first.\n" +
			"\t\t\tif keyErrors := jsonkeys.DuplicateKeyErrors(rawInput, \"caption\"); keyErrors.HasErrors() {\n" +
			"\t\t\t\tRespondInputRefusal(w, r, bodyargs.Mismatch(\"validation failed\", keyErrors))\n\t\t\t\treturn\n\t\t\t}\n" +
			"\t\t\tif err := json.Unmarshal(rawInput, &input); err != nil {",
		// attachPhoto: the data part of a multipart form.
		"\t\t\tif dataField != \"\" {\n" +
			"\t\t\t\tif keyErrors := jsonkeys.DuplicateKeyErrors([]byte(dataField), \"caption\"); keyErrors.HasErrors() {\n" +
			"\t\t\t\t\tRespondInputRefusal(w, r, bodyargs.Mismatch(\"validation failed\", keyErrors))\n\t\t\t\t\treturn\n\t\t\t\t}\n" +
			"\t\t\t\tif err := json.Unmarshal([]byte(dataField), &input); err != nil {",
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go has no\n%s", want)
		}
	}
}

// TestRawBodyChecksNameTheSingleValuedFieldsOfTheScalar: an endpoint's
// check names the wire names of its input's top-level fields of the
// scalar, in field order, and leaves out a list of it (saveNote's history)
// and fields of other types. routes.go imports each check's package once,
// and ImportsRawBodyCheckPackage reports it, so an auth provider's import
// snippet can leave its own import of the same package out.
func TestRawBodyChecksNameTheSingleValuedFieldsOfTheScalar(t *testing.T) {
	output := generateRawBodyCheckAPI(t, loadRawBodyCheckAPI(t), rawBodyChecks{"Generic.JSON": documentedKeyCheck})
	got := map[string][]apigen.RawBodyCheckCall{}
	for _, endpoint := range output.Endpoints {
		got[endpoint.Name] = endpoint.RawBodyChecks
	}
	want := map[string][]apigen.RawBodyCheckCall{
		"saveNote":    {{RawBodyCheck: documentedKeyCheck, Fields: []string{"body", "meta"}}},
		"attachPhoto": {{RawBodyCheck: documentedKeyCheck, Fields: []string{"caption"}}},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("RawBodyChecks = %+v, want %+v", got, want)
	}
	wantImports := []apigen.RawBodyCheckImport{{Name: "jsonkeys", Path: "example.com/checks/jsonkeys"}}
	if !reflect.DeepEqual(output.RawBodyCheckImports, wantImports) {
		t.Errorf("RawBodyCheckImports = %+v, want %+v", output.RawBodyCheckImports, wantImports)
	}
	if !output.ImportsRawBodyCheckPackage("example.com/checks/jsonkeys") || output.ImportsRawBodyCheckPackage(output.TypesModule) {
		t.Error("ImportsRawBodyCheckPackage reports a package routes.go does not import for a check, or misses the check's")
	}
	if n := strings.Count(writeRoutes(t, output), `"example.com/checks/jsonkeys"`); n != 1 {
		t.Errorf("routes.go imports the check's package %d times, want once", n)
	}
}

// TestChecksOfSeveralScalarsAreOneCallPerCheck: scalars that register the
// same check share one call naming all their fields; a second check is a
// second call, ordered by its first field. A check without ErrorsVar binds
// checkErrors, and one without a comment writes none.
func TestChecksOfSeveralScalarsAreOneCallPerCheck(t *testing.T) {
	schema := loadRawBodyCheckAPI(t)
	schema.Scalars["Text.Label"] = &ir.ScalarDef{Name: "Text.Label", LanguagePrimitive: ir.LanguageString}
	schema.Scalars["Text.Slug"] = &ir.ScalarDef{Name: "Text.Slug", LanguagePrimitive: ir.LanguageString}
	input := schema.Types["SaveNoteInput"]
	input.Fields = append([]*ir.FieldDef{
		{Name: "label", TypeRef: ir.TypeRef{Name: "Text.Label"}},
	}, input.Fields...)
	input.Fields = append(input.Fields, &ir.FieldDef{Name: "slug", TypeRef: ir.TypeRef{Name: "Text.Slug"}})
	textCheck := apigen.RawBodyCheck{ImportPath: "example.com/checks/text", PackageName: "text", Func: "LabelErrors"}

	output := generateRawBodyCheckAPI(t, schema, rawBodyChecks{
		"Generic.JSON": documentedKeyCheck,
		"Text.Label":   textCheck,
		"Text.Slug":    textCheck,
	})
	var saveNote apigen.EndpointInfo
	for _, endpoint := range output.Endpoints {
		if endpoint.Name == "saveNote" {
			saveNote = endpoint
		}
	}
	want := []apigen.RawBodyCheckCall{
		{RawBodyCheck: textCheck, Fields: []string{"label", "slug"}},
		{RawBodyCheck: documentedKeyCheck, Fields: []string{"body", "meta"}},
	}
	if !reflect.DeepEqual(saveNote.RawBodyChecks, want) {
		t.Fatalf("saveNote RawBodyChecks = %+v, want %+v", saveNote.RawBodyChecks, want)
	}
	wantImports := []apigen.RawBodyCheckImport{
		{Name: "jsonkeys", Path: "example.com/checks/jsonkeys"},
		{Name: "text", Path: "example.com/checks/text"},
	}
	if !reflect.DeepEqual(output.RawBodyCheckImports, wantImports) {
		t.Errorf("RawBodyCheckImports = %+v, want %+v", output.RawBodyCheckImports, wantImports)
	}
	routes := writeRoutes(t, output)
	for _, want := range []string{
		"\t\t}\n\t\tif checkErrors := text.LabelErrors(rawInput, \"label\", \"slug\"); checkErrors.HasErrors() {\n" +
			"\t\t\tRespondInputRefusal(w, r, bodyargs.Mismatch(\"validation failed\", checkErrors))\n\t\t\treturn\n\t\t}\n" +
			"\t\t// Decoding keeps the last of two equal keys, so check\n",
		`text "example.com/checks/text"`,
	} {
		if !strings.Contains(routes, want) {
			t.Errorf("routes.go has no\n%s", want)
		}
	}
}

// TestRoutesWithoutARegisteredCheckAreUnchanged: routes.go is byte for
// byte the same with no lookup and with one that registers a check only
// for a scalar the schema does not use; neither imports a check package
// or calls a check.
func TestRoutesWithoutARegisteredCheckAreUnchanged(t *testing.T) {
	schema := loadRawBodyCheckAPI(t)
	without := writeRoutes(t, generateRawBodyCheckAPI(t, schema, nil))
	unused := writeRoutes(t, generateRawBodyCheckAPI(t, schema, rawBodyChecks{"Text.Label": documentedKeyCheck}))
	if without != unused {
		t.Error("a check for a scalar the schema does not use changed routes.go")
	}
	if strings.Contains(without, "jsonkeys") || strings.Contains(without, "HasErrors() {\n\t\t\tRespondValidationErrors(w, r, keyErrors)") {
		t.Error("routes.go without a registered check calls one")
	}
}

// TestGenerateRefusesAnInvalidRawBodyCheck: a check must name an import
// path, a package name that is a Go identifier and not one routes.go
// already imports, an exported function, and a result variable the call
// does not read; two checks may not import different packages under one
// name.
func TestGenerateRefusesAnInvalidRawBodyCheck(t *testing.T) {
	valid := apigen.RawBodyCheck{ImportPath: "example.com/checks/jsonkeys", PackageName: "jsonkeys", Func: "DuplicateKeyErrors"}
	with := func(edit func(*apigen.RawBodyCheck)) apigen.RawBodyCheck {
		check := valid
		edit(&check)
		return check
	}
	for _, tc := range []struct {
		name   string
		checks rawBodyChecks
		want   string
	}{
		{"no import path", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.ImportPath = "" })}, "import path"},
		{"a quote in the import path", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.ImportPath = `example.com/"x` })}, "import path"},
		{"a package name that is not an identifier", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.PackageName = "json-keys" })}, "package name"},
		{"the blank package name", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.PackageName = "_" })}, "package name"},
		{"a package name routes.go imports", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.PackageName = "types" })}, "routes.go already imports"},
		{"an unexported function", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.Func = "duplicateKeyErrors" })}, "exported"},
		{"a result variable the call reads", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.ErrorsVar = "rawInput" })}, "result variable"},
		{"a comment with a carriage return", rawBodyChecks{"Generic.JSON": with(func(c *apigen.RawBodyCheck) { c.Comment = "one\r\ntwo" })}, "comment"},
		{"one package name, two import paths", rawBodyChecks{
			"Generic.JSON": valid,
			"Media.Photo":  with(func(c *apigen.RawBodyCheck) { c.ImportPath = "example.com/other/jsonkeys" }),
		}, "jsonkeys"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := apigen.Generate(loadRawBodyCheckAPI(t), apigen.Options{
				Provider:      sessionauth.Provider{},
				SchemaName:    rawBodyCheckAPI,
				Clock:         goModuleClock,
				RawBodyChecks: tc.checks,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Generate = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestRawBodyCheckRoutesRefuseBeforeDecoding generates the Go types and API
// modules of raw-body-check-api with a check registered for Generic.JSON,
// writes the check (testdata/raw_body_check/jsonkeys.go) into the API
// module as its jsonkeys package, copies
// testdata/raw_body_check_routes_test.go into the module and runs it. A
// body whose check fails is refused with the check's errors at their
// field paths before the route decodes it, and never reaches the
// implementation. The check registers no result variable or comment, so
// the defaults compile. attachPhoto is left out: the core cannot build a
// Go API with an upload field (D4), so its two decode paths are pinned by
// TestWriteAPIGoldenRawBodyChecks instead.
func TestRawBodyCheckRoutesRefuseBeforeDecoding(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	schema := loadRawBodyCheckAPI(t)
	for _, set := range schema.OperationSets {
		set.Operations = slices.DeleteFunc(set.Operations, func(op *ir.FieldDef) bool { return op.Name == "attachPhoto" })
	}
	delete(schema.Types, "AttachPhotoInput")
	delete(schema.Scalars, "Media.Photo")

	check := apigen.RawBodyCheck{
		ImportPath:  "example.com/schemas/api/" + rawBodyCheckAPI + "/jsonkeys",
		PackageName: "jsonkeys",
		Func:        "DuplicateKeyErrors",
	}
	apiDir := writeGoAPIModuleWith(t, schema, rawBodyCheckAPI, rawBodyChecks{"Generic.JSON": check})
	for source, target := range map[string]string{
		filepath.Join("testdata", "raw_body_check", "jsonkeys.go"): filepath.Join(apiDir, "jsonkeys", "jsonkeys.go"),
		filepath.Join("testdata", "raw_body_check_routes_test.go"): filepath.Join(apiDir, "raw_body_check_routes_test.go"),
	} {
		content, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(target, content, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGoAPIModule(t, apiDir)
}
