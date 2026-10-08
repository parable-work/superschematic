package stackgen_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/buildcache"
	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var update = flag.Bool("update", false, "rewrite the golden environment.json files")

const (
	// servicesRoot holds acme-shop-shaped services and shop-stack, the
	// stack stacktest builds by hand, authored in TypeScript.
	servicesRoot = "testdata/services"
	// yamlStack is the same stack in the YAML data form.
	yamlStack = "testdata/yaml/shop-stack"
	// goldenRoot is laid out as a build writes the stack's output.
	goldenRoot = "testdata/golden"
	// stacktestGolden holds the environments resolved from stacktest's
	// hand-built stack and facts.
	stacktestGolden = "../../../stack/stacktest/testdata/golden"
)

// assemble is the core registry with stacktest's fake target.
func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func load(t *testing.T, reg *registry.Registry, dir string) (*ir.Schema, *schemaconfig.SchemaConfig) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(dir, loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", dir, err)
	}
	return schema, cfg
}

// options are a single build's: each service the stack reaches is a
// sibling of the stack's directory.
func options(reg *registry.Registry, out string) generator.Options {
	return generator.Options{
		OutputRoot:  out,
		ServicePath: filepath.Join(servicesRoot, "shop-stack"),
		Registry:    reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(servicesRoot, name), loader.WithRegistry(reg))
		},
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			return buildplan.ReadConfig(filepath.Join(servicesRoot, name), reg)
		},
	}
}

// TestGenerateWritesStacktestsEnvironments: the stack authored in
// TypeScript over services loaded from their schemas resolves to the
// environments stacktest resolves from its hand-built stack and facts, byte
// for byte.
func TestGenerateWritesStacktestsEnvironments(t *testing.T) {
	reg := assemble(t)
	schema, cfg := load(t, reg, filepath.Join(servicesRoot, "shop-stack"))
	out := t.TempDir()
	result, err := generator.Run(schema, cfg, options(reg, out))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := result.Outputs[stackgen.Name], stackgen.OutDir(out, "shop-stack"); got != want {
		t.Errorf("stack output = %q, want %q", got, want)
	}
	for _, env := range []string{"Preview", "Production", "Staging"} {
		got, err := os.ReadFile(stack.EnvironmentPath(out, "shop-stack", env))
		if err != nil {
			t.Fatal(err)
		}
		golden := stack.EnvironmentPath(goldenRoot, "shop-stack", env)
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
			t.Errorf("%s differs from %s; run with -update and review the diff", env, golden)
		}
		handBuilt, err := os.ReadFile(stack.EnvironmentPath(stacktestGolden, "shop-stack", env))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(want, handBuilt) {
			t.Errorf("%s: %s differs from stacktest's hand-built environment", env, golden)
		}
	}
}

// TestGenerateRemovesAnEnvironmentTheStackNoLongerDeclares: the stack's
// directory holds only what the last build resolved.
func TestGenerateRemovesAnEnvironmentTheStackNoLongerDeclares(t *testing.T) {
	reg := assemble(t)
	schema, cfg := load(t, reg, filepath.Join(servicesRoot, "shop-stack"))
	out := t.TempDir()
	stale := stack.EnvironmentPath(out, "shop-stack", "Retired")
	if err := os.MkdirAll(filepath.Dir(stale), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(stale, []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := generator.Run(schema, cfg, options(reg, out)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(stale); !os.IsNotExist(err) {
		t.Errorf("%s survived the build", stale)
	}
}

// TestUnresolvedStackFailsTheBuild: a resolve check fails the build, names
// the stack, the environment and the problem, and writes nothing.
func TestUnresolvedStackFailsTheBuild(t *testing.T) {
	reg := assemble(t)
	schema, cfg := load(t, reg, filepath.Join(servicesRoot, "shop-stack"))
	// Staging no longer sets the declared server's FULFILLMENT_REGION,
	// which has no default.
	schema.Types["Staging"].Environment.Settings = nil
	out := t.TempDir()
	_, err := generator.Run(schema, cfg, options(reg, out))
	if err == nil {
		t.Fatal("the build passed")
	}
	for _, want := range []string{"stack shop-stack environment Staging does not resolve", string(stack.CodeUnboundField), "FULFILLMENT_REGION"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error lacks %q:\n%v", want, err)
		}
	}
	if _, err := os.Stat(stackgen.OutDir(out, "shop-stack")); !os.IsNotExist(err) {
		t.Errorf("a failed build wrote %s", stackgen.OutDir(out, "shop-stack"))
	}
}

// TestTheTypeScriptStackIsStacktestsShop: the decorators build the stack
// stacktest writes by hand, its environments in the order the schema
// declares them, Staging, Production and Preview, which is not their names'.
func TestTheTypeScriptStackIsStacktestsShop(t *testing.T) {
	reg := assemble(t)
	schema, _ := load(t, reg, filepath.Join(servicesRoot, "shop-stack"))
	got := ir.StackOf(schema)
	if want := stacktest.Shop(); !reflect.DeepEqual(got, want) {
		t.Errorf("StackOf =\n%s\nwant\n%s", dump(t, got), dump(t, want))
	}
}

// TestTheYAMLStackLoadsToTheSameIR: the data form declares the same stack,
// each environment's order written as the TypeScript reader numbers it.
func TestTheYAMLStackLoadsToTheSameIR(t *testing.T) {
	reg := assemble(t)
	ts, _ := load(t, reg, filepath.Join(servicesRoot, "shop-stack"))
	yaml, _ := load(t, reg, yamlStack)
	if got, want := ir.StackOf(yaml), ir.StackOf(ts); !reflect.DeepEqual(got, want) {
		t.Errorf("YAML StackOf =\n%s\nwant the TypeScript form's\n%s", dump(t, got), dump(t, want))
	}
	for name, td := range ts.Types {
		other := yaml.Types[name]
		if other == nil {
			t.Errorf("the YAML form has no type %s", name)
			continue
		}
		if td.Role != other.Role || td.Extends != other.Extends {
			t.Errorf("type %s: role %s extends %q in YAML, %s extends %q in TypeScript", name, other.Role, other.Extends, td.Role, td.Extends)
		}
		if td.Environment != nil && other.Environment != nil && td.Environment.Order != other.Environment.Order {
			t.Errorf("environment %s: order %d in YAML, %d in TypeScript", name, other.Environment.Order, td.Environment.Order)
		}
	}
	if len(yaml.Types) != len(ts.Types) {
		t.Errorf("the YAML form has %d types, the TypeScript form %d", len(yaml.Types), len(ts.Types))
	}
}

// TestServiceReadsStacktestsFacts: the adapter reads, from the services'
// IR and configs, the facts stacktest writes by hand for the services the
// stack reaches. The hand-built facts leave shop-orders' language and
// shop-db's dialects to the resolver's defaults, Go and postgres, which the
// configs' outputs spell out. Their operations are acme-shop's, where the
// fixture's APIs declare one open query each, so the operations compared
// are the fixture's.
func TestServiceReadsStacktestsFacts(t *testing.T) {
	reg := assemble(t)
	c := registry.GenerateContext{Options: options(reg, t.TempDir()), Registry: reg}
	c.LoadDependency = c.Options.LoadDependency
	services, err := stackgen.Services(c, stacktest.Shop())
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]stack.Service{}
	for _, svc := range services {
		got[svc.Name] = svc
	}
	if len(got) != 3 {
		t.Errorf("services = %v, want shop-api, shop-orders and shop-db", keys(got))
	}
	operations := map[string][]stack.Operation{
		"shop-api":    {{Name: "ProductQueries.getProduct"}},
		"shop-orders": {{Name: "OrderQueries.getOrder"}},
	}
	for _, want := range stacktest.AcmeShop() {
		svc, ok := got[want.Name]
		if !ok {
			continue
		}
		if want.Kind == ir.SchemaKindAPI && want.Language == "" {
			want.Language = registry.APILanguageGo
		}
		if want.Kind == ir.SchemaKindDB && want.Dialects == nil {
			want.Dialects = []string{registry.SQLDialectPostgres}
		}
		if want.Kind == ir.SchemaKindAPI {
			want.Operations = operations[want.Name]
		}
		if want.Config != nil {
			sort.Slice(want.Config.Fields, func(i, j int) bool { return want.Config.Fields[i].Name < want.Config.Fields[j].Name })
		}
		if svc.Config != nil {
			sort.Slice(svc.Config.Fields, func(i, j int) bool { return svc.Config.Fields[i].Name < svc.Config.Fields[j].Name })
		}
		if !reflect.DeepEqual(svc, want) {
			t.Errorf("%s:\n got %s\nwant %s", want.Name, dump(t, svc), dump(t, want))
		}
	}
}

func keys(m map[string]stack.Service) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

func dump(t *testing.T, v any) string {
	t.Helper()
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// copyServices copies the fixture's services into a schemas root of their
// own, with the base tsconfig's paths made absolute, and returns the
// services root.
func copyServices(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas")
	if err := os.CopyFS(filepath.Join(root, "services"), os.DirFS(servicesRoot)); err != nil {
		t.Fatal(err)
	}
	base, err := os.ReadFile("testdata/tsconfig.base.json")
	if err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs("../../..")
	if err != nil {
		t.Fatal(err)
	}
	text := strings.ReplaceAll(string(base), "../../../../", filepath.ToSlash(repoRoot)+"/")
	if err := os.WriteFile(filepath.Join(root, "tsconfig.base.json"), []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	return filepath.Join(root, "services")
}

// TestTheLoaderRefusesABadStack: a declaration the decorators or the kind
// refuse fails the load where it is written, and so does a settings value
// tsc refuses, in the TypeScript form; the data forms get the kind's rules.
func TestTheLoaderRefusesABadStack(t *testing.T) {
	reg := assemble(t)
	for _, tc := range []struct {
		name, from, to string
		want           []string
	}{
		{
			name: "values under a name that is not the target, which tsc refuses",
			from: `fake: { project: "acme-staging", region: "us-east1" },`,
			to:   `other: { project: "acme-staging", region: "us-east1" },`,
			want: []string{"stack.schema.ts:18:3:", "'other' does not exist in type 'EnvironmentOptions<\"fake\""},
		},
		{
			name: "values under a name that is not the target, which the loader refuses again",
			from: `fake: { project: "acme-staging", region: "us-east1" },`,
			to: `// @ts-expect-error the loader checks it again
  other: { project: "acme-staging", region: "us-east1" },`,
			want: []string{"stack.schema.ts:", `@environment holds values under "other", but its target is fake`},
		},
		{
			name: "a literal for a secret, which tsc refuses",
			from: `env: { LOG_LEVEL: "warn" }`,
			to:   `env: { LOG_LEVEL: "warn", STRIPE_KEY: "sk_live" }`,
			want: []string{"stack.schema.ts:35:63:", "Type 'string' is not assignable to type 'never'"},
		},
		{
			name: "settings of a class that is no deployable",
			from: `{ of: Orders, env: { FULFILLMENT_REGION: "us" } },`,
			to:   `{ of: Shop },`,
			want: []string{"@environment class Staging settings[0] of names class Shop, which is not an @server or @database class"},
		},
		{
			name: "a job the API does not declare, which tsc refuses",
			from: `job: "ShipOrders", schedule`,
			to:   `job: "ShipOrder", schedule`,
			want: []string{"stack.schema.ts:24:", "is not assignable to type 'never'"},
		},
		{
			name: "a schedule that is no five-field cron",
			from: `schedule: "0 * * * *"`,
			to:   `schedule: "0 * * *"`,
			want: []string{"@environment class Staging settings[1] schedule:", "has 4 fields; a schedule has five"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			services := copyServices(t)
			file := filepath.Join(services, "shop-stack", "src", "stack.schema.ts")
			src, err := os.ReadFile(file)
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(src), tc.from) {
				t.Fatalf("stack.schema.ts has no %q", tc.from)
			}
			if err := os.WriteFile(file, []byte(strings.Replace(string(src), tc.from, tc.to, 1)), 0o644); err != nil {
				t.Fatal(err)
			}
			_, _, err = loader.LoadServiceWithConfig(filepath.Join(services, "shop-stack"), loader.WithRegistry(reg))
			if err == nil {
				t.Fatal("the load passed")
			}
			for _, want := range tc.want {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error lacks %q:\n%v", want, err)
				}
			}
		})
	}

	t.Run("the YAML form", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.CopyFS(dir, os.DirFS(yamlStack)); err != nil {
			t.Fatal(err)
		}
		file := filepath.Join(dir, "src", "stack.schema.yaml")
		src, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		bad := strings.Replace(string(src), "of: { deployable: Orders }", "of: { deployable: Missing }", 1)
		if err := os.WriteFile(file, []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		_, _, err = loader.LoadServiceWithConfig(dir, loader.WithRegistry(reg))
		if err == nil || !strings.Contains(err.Error(), "@environment class Staging settings[0] of names class Missing") {
			t.Errorf("load = %v, want the unknown class named", err)
		}
	})
}

// TestAStacksReferencesAreTheServicesItNames: the handles a stack's
// declarations hold are its references in both forms (D41): the TypeScript
// form records them from the decorators' arguments, and the loader adds
// them from the typed fields the YAML form writes. The stack's config names
// none of them.
func TestAStacksReferencesAreTheServicesItNames(t *testing.T) {
	reg := assemble(t)
	want := []ir.ServiceRef{
		{Name: "shop-api", Kind: ir.SchemaKindAPI},
		{Name: "shop-db", Kind: ir.SchemaKindDB},
		{Name: "shop-orders", Kind: ir.SchemaKindAPI},
	}
	for _, dir := range []string{filepath.Join(servicesRoot, "shop-stack"), yamlStack} {
		schema, cfg := load(t, reg, dir)
		if !reflect.DeepEqual(schema.References, want) {
			t.Errorf("%s: References = %+v, want %+v", dir, schema.References, want)
		}
		if len(cfg.Dependencies) != 0 {
			t.Errorf("%s: the config declares dependencies %+v", dir, cfg.Dependencies)
		}
	}
}

// TestAStacksCacheKeyFollowsTheServicesItReaches: build-all keys a stack's
// cached output on the services its schema references and on every service
// their configs reach, from the depfile the stack's build writes (D41), so
// a change to any of them rebuilds the stack. None orders the build.
func TestAStacksCacheKeyFollowsTheServicesItReaches(t *testing.T) {
	reg := assemble(t)
	services := copyServices(t)
	schemasRoot := filepath.Dir(services)
	stack, _ := load(t, reg, filepath.Join(services, "shop-stack"))
	if err := buildcache.WriteSchemaReferences(schemasRoot, stack.Name, stack.References, stack.IdentitySentinels); err != nil {
		t.Fatal(err)
	}
	hashes := func() map[string]string {
		t.Helper()
		discovered, err := buildplan.DiscoverWith(services, t.TempDir(), reg)
		if err != nil {
			t.Fatal(err)
		}
		sorted, err := buildplan.TopologicalSort(discovered)
		if err != nil {
			t.Fatal(err)
		}
		hashes, err := buildcache.ComputeInputHashes(sorted, filepath.Dir(schemasRoot), reg.Naming())
		if err != nil {
			t.Fatal(err)
		}
		return hashes
	}
	before := hashes()
	for _, service := range []string{"shop-db", "shop-api", "shop-orders"} {
		file := filepath.Join(services, service, "src", "added.schema.ts")
		if err := os.WriteFile(file, []byte("export enum Added { A = \"a\" }\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		after := hashes()
		if after[service] == before[service] {
			t.Fatalf("%s's key did not change", service)
		}
		if after["shop-stack"] == before["shop-stack"] {
			t.Errorf("a change to %s leaves shop-stack's key as it was", service)
		}
		before = after
	}
}
