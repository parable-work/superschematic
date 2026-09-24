package loader

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestLoadServiceDataFormatsMatchTSGolden is the acceptance gate for the
// JSON and YAML frontends: a service authored entirely in JSON or YAML
// produces IR equal to its TypeScript equivalent. The fixtures translate the
// tsreader fixture services; the comparison normalizes only the Owner file
// extension (.schema.json / .schema.yaml vs .schema.ts).
func TestLoadServiceDataFormatsMatchTSGolden(t *testing.T) {
	cases := []struct {
		service string
		golden  string
	}{
		{"fixture-general-json", "fixture-general"},
		{"fixture-general-yaml", "fixture-general"},
		{"fixture-db-json", "fixture-db"},
		{"fixture-db-yaml", "fixture-db"},
	}
	for _, tc := range cases {
		t.Run(tc.service, func(t *testing.T) {
			schema, err := LoadService(filepath.Join("testdata", "services", tc.service))
			if err != nil {
				t.Fatalf("LoadService(%s): %v", tc.service, err)
			}
			got, err := json.MarshalIndent(schema, "", "  ")
			if err != nil {
				t.Fatalf("marshal: %v", err)
			}
			normalized := strings.ReplaceAll(string(got), ".schema.json", ".schema.ts")
			normalized = strings.ReplaceAll(normalized, ".schema.yaml", ".schema.ts")
			normalized += "\n"

			tsSchema, err := LoadService(filepath.Join("tsreader", "testdata", "services", tc.golden))
			if err != nil {
				t.Fatalf("LoadService(%s): %v", tc.golden, err)
			}
			want, err := json.MarshalIndent(tsSchema, "", "  ")
			if err != nil {
				t.Fatalf("marshal TS schema: %v", err)
			}
			wantText := string(want) + "\n"
			if normalized != wantText {
				t.Errorf("IR mismatch against %s TypeScript service\ngot:\n%s", tc.golden, normalized)
			}
		})
	}
}

// writeService lays out a temp service directory from a map of relative
// paths to file contents.
func writeService(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, contents := range files {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

const minimalConfig = `{
  "name": "temp-service",
  "kind": "General",
  "outputs": {}
}`

func TestHydrateScalarsFromRegistryPopulatesScalarLibMetadata(t *testing.T) {
	schema := ir.NewSchema("temp-service", ir.SchemaKindGeneral)
	schema.Scalars["Contact.Email"] = &ir.ScalarDef{
		Name:              "Contact.Email",
		LanguagePrimitive: ir.LanguageString,
	}
	schema.Scalars["Temporal.Seconds"] = &ir.ScalarDef{
		Name:              "Temporal.Seconds",
		LanguagePrimitive: ir.LanguageNumber,
	}

	if err := hydrateScalarsFromRegistry(schema, registry.CoreScalars()); err != nil {
		t.Fatalf("hydrateScalarsFromRegistry: %v", err)
	}

	scalar := schema.Scalars["Contact.Email"]
	if scalar.Description == "" {
		t.Fatal("Description was not hydrated from superscalar metadata")
	}
	if scalar.LanguagePrimitive != ir.LanguageString {
		t.Fatalf("LanguagePrimitive = %q, want %q", scalar.LanguagePrimitive, ir.LanguageString)
	}
	if !scalar.HasCustomValidate {
		t.Fatal("HasCustomValidate was not hydrated from superscalar metadata")
	}
	if !scalar.HasCustomNormalize {
		t.Fatal("HasCustomNormalize was not hydrated from superscalar metadata")
	}
	if scalar.HasCustomParse {
		t.Fatal("HasCustomParse should remain false until superscalar exposes an email parse hook to superschematic")
	}
	if scalar.TypeMappings["sql"] != "CITEXT" {
		t.Fatalf("sql mapping = %q, want CITEXT", scalar.TypeMappings["sql"])
	}
	if scalar.TypeMappings["json_schema"] != "string" {
		t.Fatalf("json_schema mapping = %q, want string", scalar.TypeMappings["json_schema"])
	}

	seconds := schema.Scalars["Temporal.Seconds"]
	if seconds.Minimum == nil || *seconds.Minimum != -9007199254740991 {
		t.Fatalf("Minimum = %v, want -9007199254740991", seconds.Minimum)
	}
	if seconds.Maximum == nil || *seconds.Maximum != 9007199254740991 {
		t.Fatalf("Maximum = %v, want 9007199254740991", seconds.Maximum)
	}
	if !seconds.HasCustomParse {
		t.Fatal("HasCustomParse was not hydrated from superscalar metadata")
	}
}

// A custom-parse scalar with an object JSON shape takes its TypeScript and
// Python types from the catalog, over whatever the schema file declared,
// because its parser returns a native map in both languages.
func TestHydrateObjectParserUsesCatalogLanguageTypes(t *testing.T) {
	stringMap := scalars.ScalarMetadataByCanonical["Generic.StringMap"]
	if stringMap == nil || !stringMap.HasCustomParse || stringMap.TypeScriptType == "" || stringMap.PythonType == "" {
		t.Fatal("the linked catalog must mark Generic.StringMap custom-parse and declare its TypeScript and Python types")
	}
	headers := &scalars.ScalarMetadata{
		CanonicalName:  "Acme.Headers",
		Primitive:      "String",
		TypeScriptType: "Record<string, string>",
		PythonType:     "Dict[str, str]",
		JSONSchemaType: "object",
		HasCustomParse: true,
	}
	catalog := registry.ScalarCatalogOf(map[string]*scalars.ScalarMetadata{
		"Generic.StringMap": stringMap,
		"Acme.Headers":      headers,
	})
	schema := ir.NewSchema("temp-service", ir.SchemaKindGeneral)
	for _, name := range []string{"Generic.StringMap", "Acme.Headers"} {
		schema.Scalars[name] = &ir.ScalarDef{
			Name:         name,
			TypeMappings: map[string]string{"typescript": "string", "python": "str"},
		}
	}
	if err := hydrateScalarsFromRegistry(schema, catalog); err != nil {
		t.Fatalf("hydrateScalarsFromRegistry: %v", err)
	}
	for name, want := range map[string]*scalars.ScalarMetadata{"Generic.StringMap": stringMap, "Acme.Headers": headers} {
		got := schema.Scalars[name]
		if !got.HasCustomParse {
			t.Errorf("%s: HasCustomParse was not hydrated", name)
		}
		if got.TypeMappings["typescript"] != want.TypeScriptType || got.TypeMappings["python"] != want.PythonType {
			t.Errorf("%s: typescript = %q, python = %q; want %q, %q", name,
				got.TypeMappings["typescript"], got.TypeMappings["python"], want.TypeScriptType, want.PythonType)
		}
	}
}

func TestLoadServiceRejectsDuplicateDefinitionsAcrossFiles(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  `{"name": "Widget", "role": "EmbeddedStruct", "fields": [{"name": "id", "typeRef": {"name": "string"}}]}`,
		"src/b.schema.json":  `{"name": "Widget", "role": "EmbeddedStruct", "fields": [{"name": "id", "typeRef": {"name": "string"}}]}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a duplicate type definition")
	}
	if !strings.Contains(err.Error(), `duplicate type "Widget"`) {
		t.Errorf("error %q does not report the duplicate", err.Error())
	}
}

func TestLoadServiceRejectsDanglingTypeReferences(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  `{"name": "Widget", "role": "EmbeddedStruct", "fields": [{"name": "owner", "typeRef": {"name": "Ghost"}}]}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a dangling type reference")
	}
	if !strings.Contains(err.Error(), `unknown type "Ghost"`) {
		t.Errorf("error %q does not report the dangling reference", err.Error())
	}
}

func TestLoadServiceImportsAreKnownExternals(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{
			"name": "temp-service",
			"kind": "API",
			"dependencies": [{"name": "web-db", "kind": "DB"}],
			"outputs": {}
		}`,
		"src/a.schema.json": `{
			"imports": [{"package": "@schemas/web-db", "types": ["Tenant"]}],
			"types": {
				"Widget": {
					"name": "Widget",
					"role": "EmbeddedStruct",
					"fields": [{"name": "tenant", "typeRef": {"name": "Tenant"}, "required": true}]
				}
			}
		}`,
	})
	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if len(schema.Imports) != 1 || schema.Imports[0].Package != "@schemas/web-db" {
		t.Errorf("imports = %+v", schema.Imports)
	}
}

func TestLoadServiceRejectsWildcardImports(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json": `{
			"imports": [{"package": "@schemas/web-db", "types": ["*"]}],
			"types": {}
		}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a wildcard import")
	}
	if !strings.Contains(err.Error(), "wildcard") {
		t.Errorf("error %q does not report the wildcard import", err.Error())
	}
}

func TestLoadServiceRejectsMismatchedDocumentIdentity(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  `{"name": "other-service", "kind": "General", "types": {}}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a document naming a different service")
	}
	if !strings.Contains(err.Error(), "does not match the service name") {
		t.Errorf("error %q does not report the identity mismatch", err.Error())
	}
}

func TestLoadServiceRejectsUnknownDecoratorKeys(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.json":  `{"name": "Widget", "role": "EmbeddedStruct", "fields": [{"name": "id", "typeRef": {"name": "string"}, "sparkles": true}]}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted an unknown decorator key")
	}
	if !strings.Contains(err.Error(), "sparkles") {
		t.Errorf("error %q does not mention the unknown key", err.Error())
	}
}

func TestLoadServiceCompositeDefaultCanonicalizesAndEntersIR(t *testing.T) {
	dir := writeCompositeDefaultService(t, `{
		"type": "FixtureDefaults",
		"value": {
			"environment": "production",
			"nested": {"values": [3, 1]},
			"choice": {"kind": "alpha", "label": "O'Reilly"}
		}
	}`)

	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}

	defaults := reflect.ValueOf(schema).Elem().FieldByName("CompositeDefaults")
	if !defaults.IsValid() || defaults.Len() != 1 {
		t.Fatalf("CompositeDefaults = %#v, want one default", defaults)
	}
	entry := defaults.MapIndex(reflect.ValueOf("FixtureDefaults"))
	if !entry.IsValid() {
		t.Fatal("FixtureDefaults composite default missing from IR")
	}
	canonical := entry.Elem().FieldByName("CanonicalJSON")
	if !canonical.IsValid() {
		t.Fatal("composite default IR entry has no CanonicalJSON")
	}
	const want = `{"choice":{"kind":"alpha","label":"O'Reilly"},"environment":"production","nested":{"values":[3,1]}}`
	if got := canonical.String(); got != want {
		t.Fatalf("CanonicalJSON = %q, want %q", got, want)
	}
}

func TestLoadServiceRejectsMalformedCompositeDefault(t *testing.T) {
	tests := []struct {
		name        string
		defaultJSON string
		wantError   string
	}{
		{
			name: "missing required nested field",
			defaultJSON: `{
				"type": "FixtureDefaults",
				"value": {
					"environment": "production",
					"nested": {},
					"choice": {"kind": "alpha", "label": "valid"}
				}
			}`,
			wantError: "nested.values",
		},
		{
			name: "invalid enum member",
			defaultJSON: `{
				"type": "FixtureDefaults",
				"value": {
					"environment": "preview",
					"nested": {"values": [1]},
					"choice": {"kind": "alpha", "label": "valid"}
				}
			}`,
			wantError: "environment",
		},
		{
			name: "invalid union member",
			defaultJSON: `{
				"type": "FixtureDefaults",
				"value": {
					"environment": "production",
					"nested": {"values": [1]},
					"choice": {"kind": "alpha", "count": 4}
				}
			}`,
			wantError: "FixtureDefaults.choice.label is required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := writeCompositeDefaultService(t, tt.defaultJSON)
			_, err := LoadService(dir)
			if err == nil {
				t.Fatal("LoadService accepted malformed composite default")
			}
			if !strings.Contains(err.Error(), tt.wantError) {
				t.Fatalf("error %q does not contain %q", err, tt.wantError)
			}
		})
	}
}

func TestLoadServiceCompositeDefaultUsesDiscriminatorBeforeStructuralMatching(t *testing.T) {
	dir := writeCompositeDefaultService(t, `{
		"type": "FixtureDefaults",
		"value": {
			"environment": "production",
			"nested": {"values": [1]},
			"choice": {"kind": "alpha", "label": 42}
		}
	}`)

	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted the invalid selected union member")
	}
	if !strings.Contains(err.Error(), "FixtureDefaults.choice.label must be a string") {
		t.Fatalf("error %q does not report the selected member validation failure", err)
	}
}

func TestLoadServiceCompositeDefaultRejectsAmbiguousStructuralUnionMatch(t *testing.T) {
	dir := writeAmbiguousCompositeDefaultService(t)

	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted an ambiguous union default")
	}
	for _, want := range []string{
		"ambiguously matches multiple AmbiguousChoice union members",
		"FirstChoice",
		"SecondChoice",
		"NoMatchDefaults.choice does not match any AmbiguousChoice union member",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("error %q does not contain %q", err, want)
		}
	}
}

func writeCompositeDefaultService(t *testing.T, defaultJSON string) string {
	t.Helper()
	return writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/defaults.fixture.schema.json": `{
			"enums": {
				"Environment": {
					"name": "Environment",
					"values": [
						{"name": "Development", "serializedAs": "development"},
						{"name": "Production", "serializedAs": "production"}
					]
				}
			},
			"types": {
				"NestedDefaults": {
					"name": "NestedDefaults",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "values", "typeRef": {"name": "number", "isArray": true}, "required": true}
					]
				},
				"AlphaChoice": {
					"name": "AlphaChoice",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "kind", "typeRef": {"name": "string"}, "required": true, "internalMetadata": true, "default": "alpha"},
						{"name": "label", "typeRef": {"name": "string"}, "required": true}
					]
				},
				"BetaChoice": {
					"name": "BetaChoice",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "kind", "typeRef": {"name": "string"}, "required": true, "internalMetadata": true, "default": "beta"},
						{"name": "label", "typeRef": {"name": "string"}, "required": true}
					]
				},
				"FixtureDefaults": {
					"name": "FixtureDefaults",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "environment", "typeRef": {"name": "Environment"}, "required": true},
						{"name": "nested", "typeRef": {"name": "NestedDefaults"}, "required": true},
						{"name": "choice", "typeRef": {"name": "FixtureChoice"}, "required": true}
					]
				}
			},
			"unions": {
				"FixtureChoice": {
					"name": "FixtureChoice",
					"types": ["AlphaChoice", "BetaChoice"]
				}
			}
		}`,
		"src/fixture-defaults.platform-default.json": defaultJSON,
	})
}

func writeAmbiguousCompositeDefaultService(t *testing.T) string {
	t.Helper()
	return writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/defaults.fixture.schema.json": `{
			"types": {
				"FirstChoice": {
					"name": "FirstChoice",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "value", "typeRef": {"name": "string"}, "required": true}
					]
				},
				"SecondChoice": {
					"name": "SecondChoice",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "value", "typeRef": {"name": "string"}, "required": true}
					]
				},
				"AmbiguousDefaults": {
					"name": "AmbiguousDefaults",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "choice", "typeRef": {"name": "AmbiguousChoice"}, "required": true}
					]
				},
				"NoMatchDefaults": {
					"name": "NoMatchDefaults",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "choice", "typeRef": {"name": "AmbiguousChoice"}, "required": true}
					]
				}
			},
			"unions": {
				"AmbiguousChoice": {
					"name": "AmbiguousChoice",
					"types": ["FirstChoice", "SecondChoice"]
				}
			}
		}`,
		"src/ambiguous-defaults.platform-default.json": `{
			"type": "AmbiguousDefaults",
			"value": {"choice": {"value": "same"}}
		}`,
		"src/no-match-defaults.platform-default.json": `{
			"type": "NoMatchDefaults",
			"value": {"choice": {"other": "value"}}
		}`,
	})
}

// --- format-agnostic verification pass ---
//
// These tests prove the verify pass runs on the assembled IR for every
// frontend: rules the TypeScript walk used to own now also fire for JSON
// and YAML services.

// TestVerifyIllegalToolchainImportTS: the kind/import rules fire through the
// loader for a TypeScript service, with the error carrying the import
// statement's source location.
func TestVerifyIllegalToolchainImportTS(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-illegal-import"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	msg := err.Error()
	if !strings.Contains(msg, "a General schema cannot import @superschematic/db") {
		t.Errorf("expected kind/import rule error, got: %s", msg)
	}
	if !strings.Contains(msg, "illegal.schema.ts:2:1") {
		t.Errorf("expected location of the import statement, got: %s", msg)
	}
}

// TestVerifyTraitCarrierOfNonTrait: `implements Trait<Plain>` where Plain is
// not a @trait class passes the compiler (the Trait<T> carrier erases to an
// empty object type) but the verification pass rejects it.
func TestVerifyTraitCarrierOfNonTrait(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-trait-carrier-non-trait"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	if !strings.Contains(err.Error(), "Gadget implements Plain, which is not a trait") {
		t.Errorf("expected the non-trait verification error, got: %s", err.Error())
	}
}

// TestVerifySourceProjectionTS: the full @source verification fires for a
// TypeScript service through compiler-resolved external types: an
// assignability mismatch and an unmarked extra field are hard errors, and
// omitting a @sourceMustProject field draws a warning even on a failing load.
func TestVerifySourceProjectionTS(t *testing.T) {
	var warnings bytes.Buffer
	prev := WarningWriter
	WarningWriter = &warnings
	defer func() { WarningWriter = prev }()

	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-source-mismatch"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	msg := err.Error()
	for _, want := range []string{
		`BrokenTenantView.name type "number" is not assignable to source type fixture-db.Tenant.name "Identity.Name"`,
		"BrokenTenantView.extraField is not in source type fixture-db.Tenant and is not marked @virtual",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("error %q is missing %q", msg, want)
		}
	}
	if !strings.Contains(warnings.String(), "SlimTenantView omits fixture-db.Tenant.name, which is marked @sourceMustProject") {
		t.Errorf("warnings %q do not report the must-project omission", warnings.String())
	}
}

// TestVerifyCrossKindReferenceJSON: a General JSON service referencing types
// from a DB dependency violates the cross-kind rules; the error anchors at
// the declaring file.
func TestVerifyCrossKindReferenceJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{
			"name": "temp-service",
			"kind": "General",
			"dependencies": [{"name": "web-db", "kind": "DB"}],
			"outputs": {}
		}`,
		"src/a.schema.json": `{
			"imports": [{"package": "@schemas/web-db", "types": ["Tenant"]}],
			"types": {
				"Widget": {
					"name": "Widget",
					"role": "EmbeddedStruct",
					"fields": [{"name": "tenant", "typeRef": {"name": "Tenant"}, "required": true}]
				}
			}
		}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a General -> DB type reference")
	}
	if !strings.Contains(err.Error(), `a General schema cannot reference types from DB service "web-db"`) {
		t.Errorf("error %q does not report the cross-kind violation", err.Error())
	}
	if !strings.Contains(err.Error(), "src/a.schema.json") {
		t.Errorf("error %q does not anchor at the declaring file", err.Error())
	}
}

// TestVerifyServiceScopeFollowsWithNaming: the npm scope that marks an
// import as a service reference reaches the verifier through WithNaming, not
// a process-wide value. The same General -> DB reference is a violation
// under the scope that owns it and invisible under any other.
func TestVerifyServiceScopeFollowsWithNaming(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{
			"name": "temp-service",
			"kind": "General",
			"dependencies": [{"name": "web-db", "kind": "DB"}],
			"outputs": {}
		}`,
		"src/a.schema.json": `{
			"imports": [{"package": "@acme/web-db", "types": ["Tenant"]}],
			"types": {
				"Widget": {
					"name": "Widget",
					"role": "EmbeddedStruct",
					"fields": [{"name": "tenant", "typeRef": {"name": "Tenant"}, "required": true}]
				}
			}
		}`,
	})
	_, err := LoadService(dir, WithNaming(naming.Naming{NpmScope: "@acme"}))
	if err == nil {
		t.Fatal("LoadService accepted a General -> DB type reference under the @acme scope")
	}
	if !strings.Contains(err.Error(), `a General schema cannot reference types from DB service "web-db"`) {
		t.Errorf("error %q does not report the cross-kind violation", err.Error())
	}

	if _, err := LoadService(dir); err != nil {
		t.Errorf("an @acme import is not a service reference under the default scope, got %v", err)
	}
}

// TestVerifySourceProjectionYAML: the full @source verification fires for a
// YAML service: a projection field missing from the source and not marked
// virtual is a hard error.
func TestVerifySourceProjectionYAML(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-api", "kind": "API", "outputs": {}}`,
		"src/a.schema.yaml": `
types:
  Tenant:
    name: Tenant
    role: EmbeddedStruct
    fields:
      - name: id
        typeRef: { name: string }
        required: true
  TenantView:
    name: TenantView
    role: APIView
    source: { target: temp-api.Tenant }
    fields:
      - name: id
        typeRef: { name: string }
        required: true
      - name: mystery
        typeRef: { name: string }
`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a non-virtual field missing from the source")
	}
	if !strings.Contains(err.Error(), "TenantView.mystery is not in source type temp-api.Tenant and is not marked @virtual") {
		t.Errorf("error %q does not report the @virtual violation", err.Error())
	}
}

// TestVerifySourceMustProjectWarningJSON: omitting a @sourceMustProject
// source field draws a warning, not an error, and the load succeeds with
// recomputed SourceRef metadata.
func TestVerifySourceMustProjectWarningJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-api", "kind": "API", "outputs": {}}`,
		"src/a.schema.json": `{
			"types": {
				"Tenant": {
					"name": "Tenant",
					"role": "EmbeddedStruct",
					"fields": [
						{"name": "id", "typeRef": {"name": "string"}, "required": true},
						{"name": "name", "typeRef": {"name": "string"}, "required": true, "sourceMustProject": true}
					]
				},
				"TenantView": {
					"name": "TenantView",
					"role": "APIView",
					"source": {"target": "temp-api.Tenant"},
					"fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true}]
				}
			}
		}`,
	})

	var warnings bytes.Buffer
	prev := WarningWriter
	WarningWriter = &warnings
	defer func() { WarningWriter = prev }()

	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if !strings.Contains(warnings.String(), "TenantView omits temp-api.Tenant.name, which is marked @sourceMustProject") {
		t.Errorf("warnings %q do not report the must-project omission", warnings.String())
	}
	src := schema.Types["TenantView"].Source
	if len(src.OmittedFromSource) != 1 || src.OmittedFromSource[0] != "name" {
		t.Errorf("omittedFromSource = %v, want [name]", src.OmittedFromSource)
	}
}

// TestVerifyTraitConfigYAML: trait shape assertion fires for a YAML service
// supplying config arguments a marker trait does not take.
func TestVerifyTraitConfigYAML(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": minimalConfig,
		"src/a.schema.yaml": `
types:
  Reviewed:
    name: Reviewed
    role: Trait
    isTrait: true
  Notification:
    name: Notification
    role: EmbeddedStruct
    implements:
      - name: Reviewed
        configArgs: { channel: email }
    fields:
      - name: subject
        typeRef: { name: string }
        required: true
`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted config arguments on a marker trait")
	}
	if !strings.Contains(err.Error(), "trait Reviewed takes no configuration but Notification supplies config arguments") {
		t.Errorf("error %q does not report the trait shape violation", err.Error())
	}
}

func TestLoadVersionedTypeJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.json": `{
			"types": {
				"Tenant": {
					"name": "Tenant",
					"role": "DBTable",
					"versioned": true,
					"fields": [
						{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}
					]
				}
			}
		}`,
	})

	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if !schema.Types["Tenant"].Versioned {
		t.Fatalf("Tenant.Versioned = false, want true")
	}
}

func TestLoadConfiguredVersionedTypeTS(t *testing.T) {
	// pruneKeepReferencedBy takes one reference or a list of them: a versioned
	// table can be pinned by more than one reader of its historical rows, and
	// the single-object form has to keep working so existing declarations are
	// untouched.
	cases := []struct {
		name      string
		decorator string
		want      []ir.PruneReference
	}{
		{
			name:      "single reference",
			decorator: `@versioned({ retentionDays: 90, partitionBy: "month", pruneKeepReferencedBy: { table: "commit_entry", keyColumn: "entity_id", versionColumn: "entity_version" } })`,
			want:      []ir.PruneReference{{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"}},
		},
		{
			name: "list of references",
			decorator: `@versioned({ retentionDays: 90, partitionBy: "month", pruneKeepReferencedBy: [` +
				`{ table: "commit_entry", keyColumn: "entity_id", versionColumn: "entity_version" }, ` +
				`{ table: "proposal_resolution", keyColumn: "stash_id", versionColumn: "stash_version" }] })`,
			want: []ir.PruneReference{
				{Table: "commit_entry", KeyColumn: "entity_id", VersionColumn: "entity_version"},
				{Table: "proposal_resolution", KeyColumn: "stash_id", VersionColumn: "stash_version"},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			dbPackagePath := filepath.ToSlash(filepath.Join(repoRoot(t), "packages", "db", "src", "index.ts"))
			dir := writeService(t, map[string]string{
				"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
				"package.json":       `{"private":true}`,
				"tsconfig.json": `{
			"compilerOptions": {
				"experimentalDecorators": true,
				"emitDecoratorMetadata": false,
				"module": "ESNext",
				"target": "ES2022",
				"moduleResolution": "Bundler",
				"strict": true,
				"strictPropertyInitialization": false,
				"baseUrl": ".",
				"paths": {
					"@superschematic/db": ["` + dbPackagePath + `"]
				}
			},
			"include": ["src/**/*.ts"]
		}`,
				"src/tenant.schema.ts": `
import { key, versioned } from "@superschematic/db";

` + tc.decorator + `
export abstract class Tenant {
  @key
  id: string;
}
`,
			})

			schema, err := LoadService(dir)
			if err != nil {
				t.Fatalf("LoadService: %v", err)
			}
			cfg := schema.Types["Tenant"].VersionedConfig
			if cfg == nil {
				t.Fatal("Tenant.VersionedConfig = nil, want config")
			}
			if cfg.RetentionDays == nil || *cfg.RetentionDays != 90 {
				t.Fatalf("Tenant retentionDays = %#v, want 90", cfg.RetentionDays)
			}
			if cfg.PartitionBy != "month" {
				t.Fatalf("Tenant partitionBy = %q, want month", cfg.PartitionBy)
			}
			if len(cfg.PruneKeepReferencedBy) != len(tc.want) {
				t.Fatalf("Tenant pruneKeepReferencedBy = %#v, want %d pins", cfg.PruneKeepReferencedBy, len(tc.want))
			}
			for i, want := range tc.want {
				if *cfg.PruneKeepReferencedBy[i] != want {
					t.Fatalf("pin %d = %#v, want %#v", i, *cfg.PruneKeepReferencedBy[i], want)
				}
			}
		})
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}
	return root
}

func TestLoadConfiguredVersionedTypeJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.json": `{
			"types": {
				"Tenant": {
					"name": "Tenant",
					"role": "DBTable",
					"versioned": true,
					"versionedConfig": {"retentionDays": 30, "partitionBy": "month", "pruneKeepReferencedBy": [{"table": "commit_entry", "keyColumn": "entity_id", "versionColumn": "entity_version"}]},
					"fields": [
						{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}
					]
				}
			}
		}`,
	})

	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	cfg := schema.Types["Tenant"].VersionedConfig
	if cfg == nil || cfg.RetentionDays == nil || *cfg.RetentionDays != 30 || cfg.PartitionBy != "month" {
		t.Fatalf("Tenant.VersionedConfig = %#v, want retentionDays=30 partitionBy=month", cfg)
	}
	pins := cfg.PruneKeepReferencedBy
	if len(pins) != 1 || pins[0].Table != "commit_entry" || pins[0].KeyColumn != "entity_id" || pins[0].VersionColumn != "entity_version" {
		t.Fatalf("Tenant pruneKeepReferencedBy = %#v, want commit_entry(entity_id, entity_version)", pins)
	}
}

func TestVerifyVersionedRulesYAML(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.yaml": `
types:
  Tenant:
    name: Tenant
    role: DBTable
    versioned: true
    fields:
      - name: id
        typeRef: { name: string }
        required: true
      - name: tenantId
        typeRef: { name: string }
        required: true
        key: true
      - name: sourceId
        typeRef: { name: string }
        required: true
        key: true
`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("LoadService accepted a versioned table with multiple @key fields")
	}
	if !strings.Contains(err.Error(), "Tenant: @versioned requires exactly one @key field, found 2") {
		t.Errorf("error %q does not report the versioned key violation", err.Error())
	}
}

func TestLoadConfiguredVersionedTypeYAML(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.yaml": `
types:
  Tenant:
    name: Tenant
    role: DBTable
    versioned: true
    versionedConfig:
      retentionDays: 14
      partitionBy: month
    fields:
      - name: id
        typeRef: { name: string }
        required: true
        key: true
`,
	})

	schema, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	cfg := schema.Types["Tenant"].VersionedConfig
	if cfg == nil || cfg.RetentionDays == nil || *cfg.RetentionDays != 14 || cfg.PartitionBy != "month" {
		t.Fatalf("Tenant.VersionedConfig = %#v, want retentionDays=14 partitionBy=month", cfg)
	}
}

func requireBun(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun not on PATH; executor-backed tests need it")
	}
}

// genericOnlyCatalog is a scalar catalog holding only scalars the superscalar
// core defines (no Acme extension scalar): the shape the engine sees when
// no extension registers a catalog. The rows come from the linked scalar
// package so the test asserts catalog membership, not metadata values.
func genericOnlyCatalog(t *testing.T) registry.ScalarCatalog {
	t.Helper()
	rows := map[string]*scalars.ScalarMetadata{}
	for _, name := range []string{"Network.Url", "Identity.UUID", "Contact.Email"} {
		row, ok := scalars.ScalarMetadataByCanonical[name]
		if !ok {
			t.Fatalf("scalar package does not know %s", name)
		}
		rows[name] = row
	}
	return registry.ScalarCatalogOf(rows)
}

func scalarFixtureService(t *testing.T, scalar string) string {
	t.Helper()
	return writeService(t, map[string]string{
		"schema.config.yaml": "name: temp-service\nkind: General\noutputs: {}\n",
		"src/config.schema.yaml": "scalars:\n" +
			"  " + scalar + ":\n" +
			"    name: " + scalar + "\n" +
			"    languagePrimitive: string\n" +
			"types:\n" +
			"  Widget:\n" +
			"    name: Widget\n" +
			"    role: EmbeddedStruct\n" +
			"    fields:\n" +
			"      - name: value\n" +
			"        typeRef: { name: " + scalar + " }\n" +
			"        required: true\n",
	})
}

func registryWithCatalog(t *testing.T, catalog registry.ScalarCatalog) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := reg.RegisterScalars("fixture", catalog); err != nil {
		t.Fatalf("RegisterScalars: %v", err)
	}
	return reg
}

// TestLoadServiceHydratesGenericScalarWithoutExtension: a schema that uses
// only core scalars loads against a catalog with no extension scalars, and
// hydration fills the row from that catalog.
func TestLoadServiceHydratesGenericScalarWithoutExtension(t *testing.T) {
	dir := scalarFixtureService(t, "Network.Url")
	schema, err := LoadService(dir, WithRegistry(registryWithCatalog(t, genericOnlyCatalog(t))))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	def := schema.Scalars["Network.Url"]
	if def == nil || def.Description == "" || def.Primitive != "String" {
		t.Fatalf("Network.Url was not hydrated from the catalog: %+v", def)
	}
}

// TestLoadServiceRejectsExtensionScalarWithoutExtension: Acme.Slug is a
// scalar only an extension's scalar package defines. A catalog without that
// package does not know it, and the loader says so instead of emitting an
// unhydrated string scalar.
func TestLoadServiceRejectsExtensionScalarWithoutExtension(t *testing.T) {
	dir := scalarFixtureService(t, "Acme.Slug")
	_, err := LoadService(dir, WithRegistry(registryWithCatalog(t, genericOnlyCatalog(t))))
	if err == nil {
		t.Fatal("LoadService accepted Acme.Slug against a catalog that does not define it")
	}
	if !strings.Contains(err.Error(), "unknown scalar Acme.Slug") || !strings.Contains(err.Error(), "scalar registry") {
		t.Errorf("error %q does not name the unknown scalar and the registry", err.Error())
	}
}

// TestLoadServiceHydratesExtensionScalarWithAssembledCatalog: the same
// schema loads when the registry carries a catalog that includes the
// extension's row, the way an extension's RegisterScalars call supplies it.
func TestLoadServiceHydratesExtensionScalarWithAssembledCatalog(t *testing.T) {
	dir := scalarFixtureService(t, "Acme.Slug")
	rows := map[string]*scalars.ScalarMetadata{}
	for name, row := range scalars.ScalarMetadataByCanonical {
		rows[name] = row
	}
	rows["Acme.Slug"] = &scalars.ScalarMetadata{
		CanonicalName: "Acme.Slug",
		Symbol:        "AcmeSlug",
		Primitive:     "String",
		Description:   "URL-safe identifier",
		GoType:        "string",
		SQLType:       "CITEXT",
		Pattern:       "^[a-z0-9-]+$",
	}
	schema, err := LoadService(dir, WithRegistry(registryWithCatalog(t, registry.ScalarCatalogOf(rows))))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	def := schema.Scalars["Acme.Slug"]
	if def == nil || def.Description == "" || def.Pattern == "" {
		t.Fatalf("Acme.Slug was not hydrated from the assembled catalog: %+v", def)
	}
}

// TestHydrateScalarsKeepsInlineDataFormScalar: a data-form scalar that
// defines its own constraints is not a catalog reference and is left as
// written even when the catalog does not know it.
func TestHydrateScalarsKeepsInlineDataFormScalar(t *testing.T) {
	schema := ir.NewSchema("temp-service", ir.SchemaKindGeneral)
	schema.Scalars["Acme.Sku"] = &ir.ScalarDef{
		Name:              "Acme.Sku",
		LanguagePrimitive: ir.LanguageString,
		Pattern:           "^[A-Z]{3}-[0-9]{4}$",
	}
	if err := hydrateScalarsFromRegistry(schema, genericOnlyCatalog(t)); err != nil {
		t.Fatalf("hydrateScalarsFromRegistry: %v", err)
	}
	if schema.Scalars["Acme.Sku"].Pattern != "^[A-Z]{3}-[0-9]{4}$" {
		t.Fatal("inline scalar definition was altered")
	}
}
