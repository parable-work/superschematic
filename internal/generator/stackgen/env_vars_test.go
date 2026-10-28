package stackgen_test

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sentinel"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
)

// financesRoot holds finances-db, whose Encrypted<T> field takes its keys
// from FieldKeys.FINANCES_DB_FIELD_KEYS, finances-api, which declares the
// pair the database-tables guide documents, `@envVars FieldKeys` and
// `@envVars FinancesConfig extends FieldKeys`, and finances-stack, which
// deploys the API and sets EMBEDDING_MODEL, a field of FinancesConfig
// alone (D94).
const financesRoot = "testdata/finances/services"

// financesOptions are a single build's over financesRoot. Each dependency
// is loaded once, so a test can build the stack many times cheaply: what
// varies between builds is then only the order Go walks a map in.
func financesOptions(t *testing.T, reg *registry.Registry, out, service string) generator.Options {
	t.Helper()
	schemas := map[string]*ir.Schema{}
	return generator.Options{
		OutputRoot:  out,
		ServicePath: filepath.Join(financesRoot, service),
		Registry:    reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			if schema, ok := schemas[name]; ok {
				return schema, nil
			}
			schema, err := loader.LoadService(filepath.Join(financesRoot, name), loader.WithRegistry(reg))
			if err == nil {
				schemas[name] = schema
			}
			return schema, err
		},
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			return buildplan.ReadConfig(filepath.Join(financesRoot, name), reg)
		},
	}
}

// configFields names the fields of a stack's config, in its order.
func configFields(config *stack.Config) []string {
	var names []string
	for _, f := range config.Fields {
		names = append(names, f.Name)
	}
	return names
}

// TestTheStackAndTheLoadersReadOneConfig: an API with the documented
// Encrypted<T> pair has one config, FinancesConfig, the @envVars class no
// other extends, with FieldKeys' field and its own, and the stack, the
// TypeScript, Go and Rust loaders and the API's handle all read it, on
// every walk of the schema's types (D94). Before D94 each read took the
// first @envVars class a walk of the types met, so the stack read
// FieldKeys in about half the runs while the TypeScript build read
// FinancesConfig, and every environment that set EMBEDDING_MODEL failed.
func TestTheStackAndTheLoadersReadOneConfig(t *testing.T) {
	reg := assemble(t)
	api, cfg := load(t, reg, filepath.Join(financesRoot, "finances-api"))
	db, _ := load(t, reg, filepath.Join(financesRoot, "finances-db"))
	outputs, err := registry.ParseOutputs(cfg.Outputs, reg)
	if err != nil {
		t.Fatal(err)
	}
	deps := map[string]*ir.Schema{"finances-db": db}
	want := []string{"FINANCES_DB_FIELD_KEYS", "PLAID_CLIENT_ID", "EMBEDDING_MODEL"}

	for range 200 {
		svc, err := stackgen.Service(api, outputs, deps)
		if err != nil {
			t.Fatal(err)
		}
		if svc.Config == nil || svc.Config.Type != "FinancesConfig" || !slices.Equal(configFields(svc.Config), want) {
			t.Fatalf("the stack reads %+v, want FinancesConfig with %v", svc.Config, want)
		}
		if f := svc.Config.Fields[0]; f.InheritedFrom != "FieldKeys" || !f.Secret {
			t.Fatalf("FINANCES_DB_FIELD_KEYS = %+v, want FieldKeys' secret", f)
		}
		// The options each language's API build passes.
		for lang, opts := range map[string]envgen.Options{
			"typescript": {Derived: true, Callers: true},
			"go":         {Derived: true},
			"rust":       {Derived: true, Callers: true, CORS: true},
		} {
			opts.SchemaName, opts.Dependencies, opts.Naming = "finances-api", deps, naming.Default()
			contract, err := envgen.GenerateWithOptions(api, opts)
			if err != nil {
				t.Fatal(err)
			}
			var fields []string
			for _, f := range contract.Fields {
				fields = append(fields, f.Key)
			}
			if contract.TypeName != svc.Config.Type || !slices.Equal(fields, want) {
				t.Fatalf("the %s loader reads %s with %v; the stack reads %s with %v", lang, contract.TypeName, fields, svc.Config.Type, want)
			}
		}
		if got := sentinel.ConfigTypeOf(api); got == nil || *got != (sentinel.ConfigType{Name: "FinancesConfig", Module: "./finances-api.schema"}) {
			t.Fatalf("the handle's config type = %+v, want FinancesConfig", got)
		}
	}

	// Each language's loader reads FinancesConfig and every field of it.
	contract, err := envgen.GenerateWithOptions(api, envgen.Options{SchemaName: "finances-api", Dependencies: deps, Naming: naming.Default(), Derived: true})
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	if err := envgen.WriteTypeScriptConfig(contract, filepath.Join(dir, "ts")); err != nil {
		t.Fatal(err)
	}
	if err := envgen.WriteConfigModule(contract, filepath.Join(dir, "go")); err != nil {
		t.Fatal(err)
	}
	if err := envgen.WriteRustConfig(contract, filepath.Join(dir, "rust")); err != nil {
		t.Fatal(err)
	}
	for file, wants := range map[string][]string{
		"ts/config.ts":       {"export function loadFinancesConfig(", "FINANCES_DB_FIELD_KEYS", "PLAID_CLIENT_ID", "EMBEDDING_MODEL"},
		"go/config.go":       {"func LoadConfig() (*FinancesConfig, error)", `"FINANCES_DB_FIELD_KEYS"`, `"PLAID_CLIENT_ID"`, `"EMBEDDING_MODEL"`},
		"rust/src/config.rs": {"pub fn load_config() -> Result<FinancesConfig, ConfigError>", `"FINANCES_DB_FIELD_KEYS"`, `"PLAID_CLIENT_ID"`, `"EMBEDDING_MODEL"`},
	} {
		data, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wants {
			if !strings.Contains(string(data), w) {
				t.Errorf("%s does not hold %q:\n%s", file, w, data)
			}
		}
	}

	// The fixture's committed handle is the one the build writes.
	committed, err := os.ReadFile(filepath.Join(financesRoot, "finances-api", "src", "service.generated.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if got := sentinel.Content("finances-api", ir.SchemaKindAPI, naming.Default(), sentinel.ConfigTypeOf(api)); got != string(committed) {
		t.Errorf("finances-api's handle is\n%s\nthe build writes\n%s", committed, got)
	}
}

// TestTheDocumentedEncryptedPairBuilds builds finances-api and
// finances-stack as `build` does: the API's TypeScript loaders and
// values-schema.json read FinancesConfig, and the stack's environment
// binds EMBEDDING_MODEL and FieldKeys' secret on every build (D94). The
// goldens are laid out as a build writes them. Regenerate with:
//
//	go test ./internal/generator/stackgen -run TestTheDocumentedEncryptedPairBuilds -update
func TestTheDocumentedEncryptedPairBuilds(t *testing.T) {
	reg := assemble(t)
	out := t.TempDir()
	api, apiCfg := load(t, reg, filepath.Join(financesRoot, "finances-api"))
	if _, err := generator.Run(api, apiCfg, financesOptions(t, reg, out, "finances-api")); err != nil {
		t.Fatal(err)
	}
	files := []string{
		filepath.Join(generator.TypesDir("", generator.LangTypeScript, "finances-api"), envgen.TypeScriptConfigFile),
		filepath.Join(generator.APIDir("", "finances-api"), envgen.TypeScriptEnvConfigFile),
		filepath.Join(generator.APIDir("", "finances-api"), "values-schema.json"),
	}

	st, stCfg := load(t, reg, filepath.Join(financesRoot, "finances-stack"))
	opts := financesOptions(t, reg, out, "finances-stack")
	var first []byte
	for i := range 10 {
		if _, err := generator.Run(st, stCfg, opts); err != nil {
			t.Fatalf("build %d of finances-stack: %v", i+1, err)
		}
		got, err := os.ReadFile(stack.EnvironmentPath(out, "finances-stack", "Staging"))
		if err != nil {
			t.Fatal(err)
		}
		if first != nil && !bytes.Equal(got, first) {
			t.Fatalf("build %d of finances-stack resolved Staging otherwise than the first", i+1)
		}
		first = got
	}
	for _, want := range []string{`"field": "EMBEDDING_MODEL"`, `"secret": "FieldKeys.FINANCES_DB_FIELD_KEYS"`, `"secret": "FinancesConfig.PLAID_CLIENT_ID"`} {
		if !bytes.Contains(first, []byte(want)) {
			t.Errorf("Staging does not hold %s:\n%s", want, first)
		}
	}
	files = append(files, stack.EnvironmentPath("", "finances-stack", "Staging"))

	for _, file := range files {
		got, err := os.ReadFile(filepath.Join(out, file))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(goldenRoot, file)
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s; run with -update and review the diff", file, golden)
		}
	}
}
