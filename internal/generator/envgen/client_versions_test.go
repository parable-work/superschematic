package envgen_test

import (
	"go/format"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// clientVersionsGolden compares got with the golden file of name, or
// writes it with -update.
func clientVersionsGolden(t *testing.T, name, file string, got []byte) {
	t.Helper()
	golden := filepath.Join("testdata", "golden", name, file)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if string(got) != string(want) {
		t.Errorf("%s/%s differs from golden (run with -update to accept):\n%s", name, file, got)
	}
}

// TestClientVersionsSetting: an API whose config says clientVersions gets
// the optional setting MIN_CLIENT_VERSIONS in its EnvConfig (D143), in Go,
// TypeScript and Rust, read by each HTTP runtime's own reader, and in
// values-schema.json, not required; an API with nothing else in its
// EnvConfig gets one for it; a schema that is no API and an output without
// EnvConfig get none; and an @envVars field of that name is refused.
func TestClientVersionsSetting(t *testing.T) {
	schema := rustEnvSchema()
	schema.ClientVersions = true
	output, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: "shop-api", Naming: naming.Default(), Derived: true, Callers: true})
	if err != nil {
		t.Fatal(err)
	}
	if !output.ClientVersions || !output.EnvConfig {
		t.Fatalf("output = %+v", output)
	}

	dir := t.TempDir()
	if err := envgen.WriteTypeScriptEnvConfig(output, dir); err != nil {
		t.Fatal(err)
	}
	typescript, err := os.ReadFile(filepath.Join(dir, envgen.TypeScriptEnvConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	clientVersionsGolden(t, "typescript-env-config-client-versions", envgen.TypeScriptEnvConfigFile, typescript)
	for _, want := range []string{"  loadMinClientVersions,\n", "  type MinClientVersions,\n", "  readonly MIN_CLIENT_VERSIONS: MinClientVersions;\n", "    MIN_CLIENT_VERSIONS: read(() => loadMinClientVersions(env)),\n"} {
		if !strings.Contains(string(typescript), want) {
			t.Errorf("config.ts lacks %q", want)
		}
	}

	dir = t.TempDir()
	if err := envgen.WriteRustConfig(output, dir); err != nil {
		t.Fatal(err)
	}
	rust, err := os.ReadFile(filepath.Join(dir, "src", "config.rs"))
	if err != nil {
		t.Fatal(err)
	}
	clientVersionsGolden(t, "rust-env-config-client-versions", "config.rs", rust)
	for _, want := range []string{"pub min_client_versions: client_version::Minimums,", "client_version::Minimums::from_env().map_err(|err| vec![err.to_string()])"} {
		if !strings.Contains(string(rust), want) {
			t.Errorf("config.rs lacks %q", want)
		}
	}

	dir = t.TempDir()
	if err := envgen.WriteConfig(output, dir); err != nil {
		t.Fatal(err)
	}
	goConfig, err := os.ReadFile(filepath.Join(dir, "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	clientVersionsGolden(t, "go-env-config-client-versions", "config.go", goConfig)
	if formatted, err := format.Source(goConfig); err != nil || string(formatted) != string(goConfig) {
		t.Errorf("config.go is not gofmt's form: %v", err)
	}
	for _, want := range []string{`/clientversion"`, "\tMinClientVersions clientversion.Minimums\n", "clientversion.Load()"} {
		if !strings.Contains(string(goConfig), want) {
			t.Errorf("config.go lacks %q", want)
		}
	}

	values := envgen.BuildValuesSchema(output)
	prop, listed := values.Properties["MIN_CLIENT_VERSIONS"]
	if !listed || prop.Type != "string" {
		t.Errorf("values-schema.json's MIN_CLIENT_VERSIONS = %+v, %t", prop, listed)
	}
	for _, required := range values.Required {
		if required == "MIN_CLIENT_VERSIONS" {
			t.Error("values-schema.json requires MIN_CLIENT_VERSIONS")
		}
	}
	found := false
	for _, v := range values.XSuperschematic.EnvVars {
		if v.Name == "MIN_CLIENT_VERSIONS" {
			found = true
			if v.Required || v.Primitive != "string" {
				t.Errorf("x-superschematic's MIN_CLIENT_VERSIONS = %+v", v)
			}
		}
	}
	if !found {
		t.Error("x-superschematic lists no MIN_CLIENT_VERSIONS")
	}

	// The setting alone makes an EnvConfig.
	bare := &ir.Schema{Name: "shop-api", Kind: ir.SchemaKindAPI, ClientVersions: true}
	alone, err := envgen.GenerateWithOptions(bare, envgen.Options{SchemaName: "shop-api", Naming: naming.Default(), Derived: true})
	if err != nil || alone == nil || !alone.ClientVersions || !alone.EnvConfig || alone.TypeName != "" {
		t.Fatalf("an API with clientVersions alone: %+v, %v", alone, err)
	}
	dir = t.TempDir()
	if err := envgen.WriteConfig(alone, dir); err != nil {
		t.Fatal(err)
	}
	goConfig, err = os.ReadFile(filepath.Join(dir, "config.go"))
	if err != nil {
		t.Fatal(err)
	}
	if formatted, err := format.Source(goConfig); err != nil || string(formatted) != string(goConfig) {
		t.Errorf("config.go of the setting alone is not gofmt's form: %v\n%s", err, goConfig)
	}

	// Not without EnvConfig, and not for a schema that is no API.
	if without, err := envgen.GenerateWithOptions(bare, envgen.Options{SchemaName: "shop-api"}); err != nil || without != nil {
		t.Errorf("without Derived: %+v, %v", without, err)
	}
	db := &ir.Schema{Name: "shop-db", Kind: ir.SchemaKindDB, ClientVersions: true}
	if got, err := envgen.GenerateWithOptions(db, envgen.Options{SchemaName: "shop-db", Derived: true}); err != nil || got != nil {
		t.Errorf("a DB schema: %+v, %v", got, err)
	}

	// A setting of the same name is refused.
	bare.Types = map[string]*ir.TypeDef{"ShopConfig": {Name: "ShopConfig", EnvVars: true, Fields: []*ir.FieldDef{
		{Name: "MIN_CLIENT_VERSIONS", TypeRef: ir.TypeRef{Name: "string"}},
	}}}
	if _, err := envgen.GenerateWithOptions(bare, envgen.Options{SchemaName: "shop-api", Derived: true}); err == nil || !strings.Contains(err.Error(), "collides with MIN_CLIENT_VERSIONS, the API's client versions setting") {
		t.Errorf("an @envVars field MIN_CLIENT_VERSIONS: %v", err)
	}
}
