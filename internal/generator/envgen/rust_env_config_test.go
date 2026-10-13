package envgen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// rustEnvSchema is an API with a setting, an authDb, a callee, a bucket and
// a service clause: every field the Rust EnvConfig holds (D57).
func rustEnvSchema() *ir.Schema {
	region := `"eu"`
	return &ir.Schema{
		Name:    "shop-api",
		Kind:    ir.SchemaKindAPI,
		AuthDB:  "shop-db",
		Calls:   []ir.ServiceRef{{Name: "pricing-api", Kind: ir.SchemaKindAPI}},
		Buckets: []ir.ServiceRef{{Name: "shop-media", Kind: ir.SchemaKindBucket}},
		Types: map[string]*ir.TypeDef{
			"ShopConfig": {Name: "ShopConfig", EnvVars: true, Fields: []*ir.FieldDef{
				{Name: "STOREFRONT_REGION", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Default: &region},
				{Name: "MAX_CART_ITEMS", TypeRef: ir.TypeRef{Name: "number"}},
			}},
		},
		OperationSets: []*ir.OperationSet{{Name: "Stock", ServiceCallers: &ir.ServiceCallers{Mode: ir.ServiceCallersRequire}, Operations: []*ir.FieldDef{
			{Name: "reindex", TypeRef: ir.TypeRef{Name: "string"}, HTTPMethod: "POST"},
		}}},
	}
}

// rustEnvOptions are the options the Rust API's build passes.
func rustEnvOptions() envgen.Options {
	return envgen.Options{SchemaName: "shop-api", Naming: naming.Default(), Derived: true, Callers: true, CORS: true, Identity: true}
}

// TestRustEnvConfigGolden pins the Rust EnvConfig (D57): the settings of
// the @envVars type, a field per edge, the callers field, the CORS field
// and the identity config, each read through the runtime's readers with
// every refusal reported together; and the EnvConfig of an API with no
// @envVars type, which reads the .env file itself. Regenerate with:
//
//	go test ./internal/generator/envgen -run TestRustEnvConfigGolden -update
func TestRustEnvConfigGolden(t *testing.T) {
	withoutSettings := rustEnvSchema()
	withoutSettings.Types = nil
	withoutSettings.Buckets = nil
	for name, schema := range map[string]*ir.Schema{"rust-env-config": rustEnvSchema(), "rust-env-config-derived": withoutSettings} {
		output, err := envgen.GenerateWithOptions(schema, rustEnvOptions())
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := envgen.WriteRustConfig(output, dir); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, "src", "config.rs"))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", "golden", name, "config.rs")
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n%s", name, got)
		}
	}
}

// TestRustConfigWithoutEnvConfig: without Derived, config.rs is the
// @envVars loader alone, as it was before the derived fields (D57).
func TestRustConfigWithoutEnvConfig(t *testing.T) {
	output, err := envgen.GenerateWithOptions(rustEnvSchema(), envgen.Options{SchemaName: "shop-api"})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := envgen.WriteRustConfig(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "src", "config.rs"))
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"EnvConfig", "load_env_config", "crate::runtime", "stackconfig"} {
		if strings.Contains(string(got), absent) {
			t.Errorf("config.rs without Derived mentions %s", absent)
		}
	}
	if !strings.Contains(string(got), "pub fn load_config() -> Result<ShopConfig, ConfigError> {") {
		t.Errorf("config.rs without Derived has no load_config:\n%s", got)
	}
}

// TestCORSAndIdentityFields: with CORS and Identity set, every API gets its
// CORS field and its identity config field, which only the Rust EnvConfig
// holds (D57), even without settings or edges; a setting that collides
// with either is refused; neither is in values-schema.json.
func TestCORSAndIdentityFields(t *testing.T) {
	schema := &ir.Schema{Name: "shop-api", Kind: ir.SchemaKindAPI}
	output, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: "shop-api", Derived: true, CORS: true})
	if err != nil || output == nil || output.CORSField != "SHOP_API_CORS" || output.IdentityField != "" || !output.EnvConfig {
		t.Fatalf("an API with CORS alone: %+v, %v", output, err)
	}
	output, err = envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: "shop-api", Derived: true, Identity: true})
	if err != nil || output == nil || output.IdentityField != "SHOP_API_IDENTITY" || output.CORSField != "" {
		t.Fatalf("an API with identity alone: %+v, %v", output, err)
	}
	if output, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: "shop-api", Derived: true}); err != nil || output != nil {
		t.Errorf("without CORS or Identity: %+v, %v", output, err)
	}
	for field, what := range map[string]string{"SHOP_API_CORS_ORIGINS": "SHOP_API_CORS, the API's CORS field", "SHOP_API_IDENTITY": "SHOP_API_IDENTITY, the API's identity config field"} {
		schema.Types = map[string]*ir.TypeDef{"ShopConfig": {Name: "ShopConfig", EnvVars: true, Fields: []*ir.FieldDef{
			{Name: field, TypeRef: ir.TypeRef{Name: "string"}},
		}}}
		if _, err := envgen.GenerateWithOptions(schema, rustEnvOptions()); err == nil || !strings.Contains(err.Error(), "collides with "+what) {
			t.Errorf("a setting %s: %v", field, err)
		}
	}
	output, err = envgen.GenerateWithOptions(rustEnvSchema(), rustEnvOptions())
	if err != nil {
		t.Fatal(err)
	}
	for _, v := range envgen.BuildValuesSchema(output).XSuperschematic.EnvVars {
		if strings.HasPrefix(v.Name, "SHOP_API_") {
			t.Errorf("values-schema.json lists %s", v.Name)
		}
	}
}

// TestRustFieldName: an EnvConfig field is its config field's name in
// lower case.
func TestRustFieldName(t *testing.T) {
	if got := envgen.RustFieldName("SHOP_DB_DATABASE"); got != "shop_db_database" {
		t.Errorf("RustFieldName = %q", got)
	}
}
