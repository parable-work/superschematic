package buildplan

import (
	"os"
	"path/filepath"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

func writeFile(t *testing.T, path string, contents string) {
	t.Helper()
	require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
	require.NoError(t, os.WriteFile(path, []byte(contents), 0o644))
}

func TestDiscoverOrdersDependenciesAndOutputs(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	outputRoot := filepath.Join(root, "dist")

	writeFile(t, filepath.Join(servicesRoot, "leaf", "schema.config.yaml"), `name: leaf
kind: API
dependencies:
  - { name: base, kind: DB }
outputs:
  types:
    go: { enabled: true }
    typescript: { enabled: true }
  api: { enabled: true }
  sdk:
    typescript: { enabled: true }
`)
	writeFile(t, filepath.Join(servicesRoot, "base", "schema.config.json"), `{
		"name": "base",
		"kind": "DB",
		"outputs": {"types": {"go": {"enabled": true}}}
	}`)
	writeFile(t, filepath.Join(servicesRoot, "_ignored", "schema.config.json"), `{
		"name": "ignored",
		"kind": "General",
		"outputs": {}
	}`)

	services, err := Discover(servicesRoot, outputRoot)
	require.NoError(t, err)
	require.Len(t, services, 2)
	assert.Equal(t, "base", services[0].Name)
	assert.Equal(t, "leaf", services[1].Name)
	// Output dirs follow the kind's registry pipeline: sql, orm, types for
	// DB; types, api, sdks for API.
	assert.Equal(t, []string{
		filepath.Join(outputRoot, "sql", "base"),
		filepath.Join(outputRoot, "orm", "base"),
		filepath.Join(outputRoot, "types", "go", "base"),
	}, services[0].OutputDirs)
	assert.Equal(t, []string{
		filepath.Join(outputRoot, "types", "go", "leaf"),
		filepath.Join(outputRoot, "types", "typescript", "leaf"),
		filepath.Join(outputRoot, "api", "leaf"),
		filepath.Join(outputRoot, "sdk", "typescript", "leaf"),
	}, services[1].OutputDirs)
}

func TestTopologicalSortRejectsCycles(t *testing.T) {
	services := []Service{
		{
			Name: "a",
			Config: &schemaconfig.SchemaConfig{
				Name:         "a",
				Kind:         ir.SchemaKindGeneral,
				Dependencies: []schemaconfig.ServiceDependency{{Name: "b", Kind: ir.SchemaKindGeneral}},
			},
		},
		{
			Name: "b",
			Config: &schemaconfig.SchemaConfig{
				Name:         "b",
				Kind:         ir.SchemaKindGeneral,
				Dependencies: []schemaconfig.ServiceDependency{{Name: "a", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	_, err := TopologicalSort(services)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency")
}

func TestGroupIntoPhasesHonorsAlreadyBuilt(t *testing.T) {
	services := []Service{
		{Name: "base", Config: &schemaconfig.SchemaConfig{Name: "base"}},
		{
			Name: "leaf",
			Config: &schemaconfig.SchemaConfig{
				Name:         "leaf",
				Dependencies: []schemaconfig.ServiceDependency{{Name: "base", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	phases, err := GroupIntoPhases(services[1:], map[string]bool{"base": true})
	require.NoError(t, err)
	require.Len(t, phases, 1)
	assert.Equal(t, "leaf", phases[0][0].Name)
}

func TestValidateDependencyKindsRejectsPackagelessDependency(t *testing.T) {
	services := []Service{
		{Name: "platform-deploy", Config: &schemaconfig.SchemaConfig{Name: "platform-deploy", Kind: ir.SchemaKindGeneral}},
		{
			Name: "env",
			Config: &schemaconfig.SchemaConfig{
				Name:         "env",
				Kind:         ir.SchemaKindGeneral,
				Outputs:      map[string]any{"types": map[string]any{"go": map[string]any{"enabled": true}}},
				Dependencies: []schemaconfig.ServiceDependency{{Name: "platform-deploy", Kind: ir.SchemaKindGeneral}},
			},
		},
	}

	err := validateDependencyKinds(services)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "authoring imports are auto-tracked")

	services[1].Config.Dependencies = nil
	require.NoError(t, validateDependencyKinds(services))
}

func TestTopologicalSortOrdersAuthDBBeforeDependent(t *testing.T) {
	// The API's config names the DB only through authDb; the sort must still
	// put the DB first even though Dir order says otherwise.
	services := []Service{
		{Name: "api", Dir: "/services/api", Config: &schemaconfig.SchemaConfig{Name: "api", Kind: ir.SchemaKindAPI, AuthDB: "db"}},
		{Name: "db", Dir: "/services/db", Config: &schemaconfig.SchemaConfig{Name: "db", Kind: ir.SchemaKindDB}},
	}

	sorted, err := TopologicalSort(services)
	require.NoError(t, err)
	assert.Equal(t, []string{"db", "api"}, serviceNames(sorted))

	phases, err := GroupIntoPhases(sorted, nil)
	require.NoError(t, err)
	require.Len(t, phases, 2)
	assert.Equal(t, []string{"db"}, serviceNames(phases[0]))
	assert.Equal(t, []string{"api"}, serviceNames(phases[1]))
}

func apiCalling(name string, callees ...string) Service {
	cfg := &schemaconfig.SchemaConfig{Name: name, Kind: ir.SchemaKindAPI}
	for _, callee := range callees {
		cfg.Calls = append(cfg.Calls, schemaconfig.ServiceDependency{Name: callee, Kind: ir.SchemaKindAPI})
	}
	return Service{Name: name, Dir: "/services/" + name, Config: cfg}
}

func TestTopologicalSortOrdersCalleeBeforeCaller(t *testing.T) {
	// The caller sorts first by Dir; calls must still put the callee first,
	// since the caller's generated code imports the callee's SDK.
	sorted, err := TopologicalSort([]Service{apiCalling("orders", "shop"), apiCalling("shop")})
	require.NoError(t, err)
	assert.Equal(t, []string{"shop", "orders"}, serviceNames(sorted))

	closure, err := Closure(sorted, "orders")
	require.NoError(t, err)
	assert.Equal(t, []string{"shop", "orders"}, serviceNames(closure))
}

func TestTopologicalSortNamesACycleOfCalls(t *testing.T) {
	// "lead" reaches the cycle without being in it, so the message must
	// start at the service the cycle returns to.
	services := []Service{apiCalling("lead", "orders"), apiCalling("orders", "shop"), apiCalling("shop", "orders")}

	_, err := TopologicalSort(services)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency involving orders: orders calls shop, shop calls orders;")
	assert.Contains(t, err.Error(), "section 3.3")
	assert.NotContains(t, err.Error(), "lead")
}

func TestTopologicalSortNamesEachEdgeOfAMixedCycle(t *testing.T) {
	db := Service{Name: "db", Config: &schemaconfig.SchemaConfig{Name: "db", Kind: ir.SchemaKindDB, Dependencies: []schemaconfig.ServiceDependency{{Name: "api", Kind: ir.SchemaKindAPI}}}}
	api := Service{Name: "api", Config: &schemaconfig.SchemaConfig{
		Name:         "api",
		Kind:         ir.SchemaKindAPI,
		AuthDB:       "db",
		Dependencies: []schemaconfig.ServiceDependency{{Name: "db", Kind: ir.SchemaKindDB}},
	}}

	_, err := TopologicalSort([]Service{db, api})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency involving db: db depends on api, api depends on and authenticates against db")
	assert.NotContains(t, err.Error(), "section 3.3", "only a cycle of calls alone points at output ordering")
}

func TestValidateHandleKindsChecksEachHandleAgainstItsService(t *testing.T) {
	services := func(mutate func(*schemaconfig.SchemaConfig)) []Service {
		caller := apiCalling("orders", "shop")
		caller.Config.AuthDB = "db"
		caller.Config.AuthDBKind = ir.SchemaKindDB
		caller.Config.Dependencies = []schemaconfig.ServiceDependency{{Name: "db", Kind: ir.SchemaKindDB}}
		mutate(caller.Config)
		return []Service{
			caller,
			apiCalling("shop"),
			{Name: "db", Config: &schemaconfig.SchemaConfig{Name: "db", Kind: ir.SchemaKindDB}},
		}
	}
	require.NoError(t, validateHandleKinds(services(func(*schemaconfig.SchemaConfig) {})))

	err := validateHandleKinds(services(func(cfg *schemaconfig.SchemaConfig) {
		cfg.Calls = []schemaconfig.ServiceDependency{{Name: "db", Kind: ir.SchemaKindAPI}}
	}))
	require.Error(t, err)
	assert.Equal(t, "orders: calls names db with kind API, but db is kind DB", err.Error())

	err = validateHandleKinds(services(func(cfg *schemaconfig.SchemaConfig) { cfg.AuthDBKind = ir.SchemaKindGeneral }))
	require.Error(t, err)
	assert.Equal(t, "orders: authDb names db with kind General, but db is kind DB", err.Error())

	err = validateHandleKinds(services(func(cfg *schemaconfig.SchemaConfig) { cfg.Dependencies[0].Kind = ir.SchemaKindAPI }))
	require.Error(t, err)
	assert.Equal(t, "orders: dependencies names db with kind API, but db is kind DB", err.Error())

	// A data-form authDb has no kind to check, and an undiscovered service
	// is left to Closure.
	require.NoError(t, validateHandleKinds(services(func(cfg *schemaconfig.SchemaConfig) { cfg.AuthDBKind = "" })))
	require.NoError(t, validateHandleKinds(services(func(cfg *schemaconfig.SchemaConfig) {
		cfg.Calls = []schemaconfig.ServiceDependency{{Name: "elsewhere", Kind: ir.SchemaKindAPI}}
	})))
}

func TestDiscoverRefusesACallsHandleOfTheWrongKind(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	writeFile(t, filepath.Join(servicesRoot, "orders", "schema.config.yaml"), `name: orders
kind: API
calls:
  - { name: shop, kind: API }
outputs: {}
`)
	writeFile(t, filepath.Join(servicesRoot, "shop", "schema.config.yaml"), `name: shop
kind: General
outputs: {}
`)

	_, err := Discover(servicesRoot, filepath.Join(root, "dist"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "orders: calls names shop with kind API, but shop is kind General")
}

func TestClosureKeepsDiscoverOrderAndDropsUnrelated(t *testing.T) {
	general := func(name string, deps ...string) Service {
		cfg := &schemaconfig.SchemaConfig{Name: name, Kind: ir.SchemaKindGeneral}
		for _, dep := range deps {
			cfg.Dependencies = append(cfg.Dependencies, schemaconfig.ServiceDependency{Name: dep, Kind: ir.SchemaKindGeneral})
		}
		return Service{Name: name, Dir: "/services/" + name, Config: cfg}
	}
	leaf := general("leaf", "mid")
	leaf.Config.Kind = ir.SchemaKindAPI
	leaf.Config.AuthDB = "auth"
	auth := general("auth")
	auth.Config.Kind = ir.SchemaKindDB
	// Dir order deliberately puts dependents before their dependencies.
	services := []Service{leaf, general("other", "base"), general("mid", "base"), general("base"), auth}

	sorted, err := TopologicalSort(services)
	require.NoError(t, err)
	closure, err := Closure(sorted, "leaf")
	require.NoError(t, err)

	names := serviceNames(closure)
	assert.ElementsMatch(t, []string{"base", "mid", "auth", "leaf"}, names, "closure is the transitive dependencies plus authDb, root included")
	assert.NotContains(t, names, "other", "a sibling that shares a dependency is not in the closure")
	assert.Less(t, indexOf(names, "base"), indexOf(names, "mid"))
	assert.Less(t, indexOf(names, "mid"), indexOf(names, "leaf"))
	assert.Less(t, indexOf(names, "auth"), indexOf(names, "leaf"))

	var fromSorted []string
	for _, name := range serviceNames(sorted) {
		if name != "other" {
			fromSorted = append(fromSorted, name)
		}
	}
	assert.Equal(t, fromSorted, names, "closure preserves the order the resolver produced")
}

func TestClosureRejectsUnknownRootAndUndiscoveredDependency(t *testing.T) {
	services := []Service{
		{Name: "leaf", Config: &schemaconfig.SchemaConfig{Name: "leaf", Kind: ir.SchemaKindGeneral, Dependencies: []schemaconfig.ServiceDependency{{Name: "missing", Kind: ir.SchemaKindGeneral}}}},
	}

	_, err := Closure(services, "nope")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "nope")

	_, err = Closure(services, "leaf")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "missing")
}

func serviceNames(services []Service) []string {
	names := make([]string, 0, len(services))
	for _, service := range services {
		names = append(names, service.Name)
	}
	return names
}

func indexOf(names []string, want string) int {
	for i, name := range names {
		if name == want {
			return i
		}
	}
	return -1
}

// tsService writes a TypeScript-form service under servicesRoot whose
// tsconfig resolves @superschematic/schema-config to this checkout's sources
// and @acme/<dir> to a sibling's src/index.ts, as a schemas workspace does.
func tsService(t *testing.T, servicesRoot, dir, config string, files map[string]string) string {
	t.Helper()
	configPackage, err := filepath.Abs("../../packages/schema-config/src/index.ts")
	require.NoError(t, err)
	serviceDir := filepath.Join(servicesRoot, dir)
	writeFile(t, filepath.Join(serviceDir, "tsconfig.json"), `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "noEmit": true,
    "skipLibCheck": true,
    "experimentalDecorators": true,
    "paths": {
      "@superschematic/schema-config": [`+strconv.Quote(filepath.ToSlash(configPackage))+`],
      "@acme/*": ["../*/src/index.ts"]
    }
  },
  "include": ["schema.config.ts", "src/**/*.ts"]
}
`)
	writeFile(t, filepath.Join(serviceDir, "package.json"), `{"name": "@acme/`+dir+`", "private": true}`)
	writeFile(t, filepath.Join(serviceDir, "schema.config.ts"), config)
	for rel, content := range files {
		writeFile(t, filepath.Join(serviceDir, filepath.FromSlash(rel)), content)
	}
	return serviceDir
}

// apiConfig is a schema.config.ts for an API named name; imports and fields
// are spliced in as written.
func apiConfig(name, imports, fields string) string {
	return `import { defineConfig, SchemaKind } from "@superschematic/schema-config";
` + imports + `
export default defineConfig({ name: "` + name + `", kind: SchemaKind.API, ` + fields + ` outputs: {} });
`
}

// TestDiscoverReadsConfigsThatImportSentinels: on a tree where no sentinel
// exists yet, discovery cannot resolve a config's imported handles; the
// sweep writes every sentinel from names and kinds alone, after which
// discovery reads the handles and orders each service after the ones it
// names (D34).
func TestDiscoverReadsConfigsThatImportSentinels(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	tsService(t, servicesRoot, "shop-db", `import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
export default defineConfig({ name: "shop-db", kind: SchemaKind.DB, outputs: { types: { [TargetLanguage.Go]: { enabled: true } } } });
`, nil)
	tsService(t, servicesRoot, "shop-api", apiConfig("shop-api", "", ""), nil)
	tsService(t, servicesRoot, "shop-orders", apiConfig("shop-orders", `import { ShopApi } from "@acme/shop-api";
import { ShopDb } from "@acme/shop-db";`, "authDb: ShopDb, calls: [ShopApi],"), nil)
	reg := generator.CoreRegistry(naming.Default())
	outputRoot := filepath.Join(root, "dist")

	_, err := DiscoverWith(servicesRoot, outputRoot, reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `which does not resolve`)

	require.NoError(t, EnsureSentinels(servicesRoot, reg, nil))
	services, err := DiscoverWith(servicesRoot, outputRoot, reg)
	require.NoError(t, err)
	names := serviceNames(services)
	assert.Less(t, indexOf(names, "shop-db"), indexOf(names, "shop-orders"))
	assert.Less(t, indexOf(names, "shop-api"), indexOf(names, "shop-orders"))
	orders := services[indexOf(names, "shop-orders")].Config
	assert.Equal(t, "shop-db", orders.AuthDB)
	assert.Equal(t, ir.SchemaKindDB, orders.AuthDBKind)
	assert.Equal(t, []schemaconfig.ServiceDependency{{Name: "shop-api", Kind: ir.SchemaKindAPI}}, orders.Calls)
}

// TestDiscoverReachesTheCycleOfAPIsThatImportEachOther: two configs that
// import each other's sentinels are no module cycle, since nothing imports a
// config. The sweep and the static reads succeed, and the build plan
// reports the cycle of calls.
func TestDiscoverReachesTheCycleOfAPIsThatImportEachOther(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	tsService(t, servicesRoot, "shop-api", apiConfig("shop-api", `import { ShopOrders } from "@acme/shop-orders";`, "calls: [ShopOrders],"), nil)
	tsService(t, servicesRoot, "shop-orders", apiConfig("shop-orders", `import { ShopApi } from "@acme/shop-api";`, "calls: [ShopApi],"), nil)
	reg := generator.CoreRegistry(naming.Default())

	require.NoError(t, EnsureSentinels(servicesRoot, reg, nil))
	_, err := DiscoverWith(servicesRoot, filepath.Join(root, "dist"), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "circular dependency involving shop-api: shop-api calls shop-orders, shop-orders calls shop-api")
}

// TestDiscoverRefusesAConfigImportThatIsNotASentinel: a config imports the
// config package and other services' sentinels, nothing else; a schema
// class from the same package is refused at the import.
func TestDiscoverRefusesAConfigImportThatIsNotASentinel(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	tsService(t, servicesRoot, "shop-db", `import { defineConfig, SchemaKind, TargetLanguage } from "@superschematic/schema-config";
export default defineConfig({ name: "shop-db", kind: SchemaKind.DB, outputs: { types: { [TargetLanguage.Go]: { enabled: true } } } });
`, map[string]string{
		"src/products.schema.ts": "export abstract class Product {\n  name: string;\n}\n",
		"src/index.ts":           "export { Product } from \"./products.schema\";\nexport * from \"./service.generated\";\n",
	})
	tsService(t, servicesRoot, "shop-api", apiConfig("shop-api", `import { Product, ShopDb } from "@acme/shop-db";`, "authDb: ShopDb,"), nil)
	reg := generator.CoreRegistry(naming.Default())

	require.NoError(t, EnsureSentinels(servicesRoot, reg, nil))
	_, err := DiscoverWith(servicesRoot, filepath.Join(root, "dist"), reg)
	require.Error(t, err)
	assert.Contains(t, err.Error(), `schema.config.ts imports Product from "@acme/shop-db", which is a class, not a service sentinel`)
}

// aliasedConfigService writes a TypeScript-form service whose
// schema.config.ts imports the config package under a distribution's own
// name, @acme/schema-config, which its tsconfig resolves to this checkout's
// @superschematic/schema-config sources.
func aliasedConfigService(t *testing.T, servicesRoot string) {
	t.Helper()
	configPackage, err := filepath.Abs("../../packages/schema-config/src/index.ts")
	require.NoError(t, err)
	dir := filepath.Join(servicesRoot, "aliased")
	writeFile(t, filepath.Join(dir, "tsconfig.json"), `{
  "compilerOptions": {
    "target": "ES2022",
    "module": "ESNext",
    "moduleResolution": "Bundler",
    "strict": true,
    "noEmit": true,
    "skipLibCheck": true,
    "paths": {"@acme/schema-config": [`+strconv.Quote(filepath.ToSlash(configPackage))+`]}
  },
  "include": ["schema.config.ts"]
}
`)
	writeFile(t, filepath.Join(dir, "package.json"), `{"name": "@acme/aliased", "private": true}`)
	writeFile(t, filepath.Join(dir, "schema.config.ts"), `import { defineConfig, SchemaKind, TargetLanguage } from "@acme/schema-config";

export default defineConfig({
  name: "aliased",
  kind: SchemaKind.General,
  outputs: { types: { [TargetLanguage.TypeScript]: { enabled: true } } }
});
`)
}

// TestDiscoverAcceptsAliasedConfigImport: a distribution that republishes
// the config package under its own name maps that name onto the declaring
// package in [package_aliases]. Discovery (build-all, build --with-deps)
// accepts a schema.config.ts that imports the aliased name and reads the
// config through it.
func TestDiscoverAcceptsAliasedConfigImport(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	aliasedConfigService(t, servicesRoot)

	n := naming.Default()
	n.PackageAliases = map[string]string{"@acme/schema-config": "@superschematic/schema-config"}
	services, err := DiscoverWith(servicesRoot, filepath.Join(root, "dist"), generator.CoreRegistry(n))
	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "aliased", services[0].Name)
	assert.Equal(t, ir.SchemaKindGeneral, services[0].Config.Kind)
}

// TestDiscoverReadsTheConfigPackageByIdentity: the import rule goes by what
// a binding resolves to, not by its specifier, so a config whose tsconfig
// resolves @acme/schema-config to the config package loads without a
// [package_aliases] entry too. The alias still decides how a sentinel
// spells the import.
func TestDiscoverReadsTheConfigPackageByIdentity(t *testing.T) {
	root := t.TempDir()
	servicesRoot := filepath.Join(root, "services")
	aliasedConfigService(t, servicesRoot)

	services, err := DiscoverWith(servicesRoot, filepath.Join(root, "dist"), generator.CoreRegistry(naming.Default()))
	require.NoError(t, err)
	require.Len(t, services, 1)
	assert.Equal(t, "aliased", services[0].Name)
}
