package servergen_test

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// tsWorkersStack is the fixture's stack whose Edge environment places
// ts-pricing's server on stacktest's fake bundle platform, whose artifact
// is a bundle as Cloudflare Workers' is (D64), and whose Local runs it on
// Bun.
const tsWorkersStack = "ts-workers-stack"

// tsWorkersGenerated lists the files of ts-pricing's package the build of
// ts-workers-stack writes, by their path under tsGoldenRoot.
func tsWorkersGenerated(repoRoot string) map[string]string {
	files := map[string]string{}
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), tsWorkersStack, "ts-pricing")
	for _, file := range []string{
		servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.TypeScriptConfigFile,
		servergen.TypeScriptFetchFile, servergen.TypeScriptBundleFile, servergen.TypeScriptWorkersTypesFile,
	} {
		files[filepath.Join("server", tsWorkersStack, "ts-pricing", file)] = filepath.Join(dir, file)
	}
	return files
}

// TestTypeScriptWorkersEntrypointGolden: a TypeScript server that some
// environment places on a platform whose artifact is a bundle gets
// fetch.ts, its Workers entrypoint, bundle.ts, which bundles it for
// workerd, and workers.d.ts beside main.ts, which stack dev still runs, and
// its tsconfig.json type-checks fetch.ts too (D64). ts-pricing's fetch.ts
// reads its config from the Worker's bindings when the version loads,
// awaits its secrets per isolate, and verifies its callers against its
// callers field. The same server in ts-stack, which places it nowhere but
// on Bun, gets none of them. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptWorkersEntrypointGolden -update
func TestTypeScriptWorkersEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), "ts-pricing", tsWorkersStack, "ts-stack")
	compareGoldens(t, tsGoldenRoot, tsWorkersGenerated(repoRoot))

	plain := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "ts-stack", "ts-pricing")
	for _, file := range []string{servergen.TypeScriptFetchFile, servergen.TypeScriptBundleFile, servergen.TypeScriptWorkersTypesFile} {
		if _, err := os.Stat(filepath.Join(plain, file)); err == nil {
			t.Errorf("ts-stack's ts-pricing, on Bun alone, has %s", file)
		}
	}
	config, err := os.ReadFile(filepath.Join(plain, servergen.TypeScriptConfigFile))
	if err != nil || strings.Contains(string(config), "fetch.ts") {
		t.Errorf("ts-stack's ts-pricing type-checks fetch.ts: %v\n%s", err, config)
	}
}

// TestTypeScriptWorkersRefusals: a server some environment places on
// Workers whose API's database lists postgres first, which a Worker reaches
// through no D1 binding, or which authenticates with the identity runtime
// is refused, saying why, and so is a job on Cron Triggers whose API's
// database lists postgres first (D64), and a server or a job whose API's
// database is an engine database, whose engine is a native addon no
// Worker loads (D58), whatever dialect it lists first.
func TestTypeScriptWorkersRefusals(t *testing.T) {
	input := func(edit func(*tsrestgen.APIOutput, *envgen.ConfigOutput)) servergen.TypeScriptInput {
		output := &tsrestgen.APIOutput{
			SchemaName:   "shop-api",
			PackageName:  "@schemas/shop-api-api",
			HasEnvConfig: true,
			Jobs:         []tsrestgen.JobInfo{{Name: "Reindex", Method: "reindex"}},
		}
		config := &envgen.ConfigOutput{SchemaName: "shop-api"}
		edit(output, config)
		return servergen.TypeScriptInput{
			Stack:   "edge-stack",
			Server:  "shop-api",
			Dir:     t.TempDir(),
			Workers: true,
			APIs: []servergen.TypeScriptAPIInput{{
				Output:         output,
				Routes:         &apigen.APIOutput{SchemaName: "shop-api"},
				Config:         config,
				Implementation: servergen.TypeScriptImplementation{Package: "@schemas/shop-api-implementation"},
			}},
		}
	}
	none := func(*tsrestgen.APIOutput, *envgen.ConfigOutput) {}
	if s, err := servergen.PlanTypeScript(input(none)); err != nil || !s.Workers {
		t.Fatalf("a server on Workers = %+v, %v", s, err)
	}
	// A database that lists sqlite first, which a Worker reaches through
	// its D1 binding.
	sqliteDB := func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
		o.Deps.Database = "shop-db"
		o.Deps.Dialect = "sqlite"
		c.Derived = []envgen.DerivedField{{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"}}
	}
	if s, err := servergen.PlanTypeScript(input(sqliteDB)); err != nil || len(s.SQLiteDatabases()) != 1 {
		t.Fatalf("a server on Workers over a D1 database = %+v, %v", s, err)
	}
	for _, tc := range []struct {
		name   string
		edit   func(*tsrestgen.APIOutput, *envgen.ConfigOutput)
		job    bool
		want   string
		engine bool
	}{
		{"a Postgres database", func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
			o.Deps.Database = "shop-db"
			c.Derived = []envgen.DerivedField{{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"}}
		}, false, "server shop-api serves shop-api, whose database shop-db lists postgres first, and an environment places the server on Cloudflare Workers, which reaches a database through a D1 binding alone", false},
		{"the identity runtime", func(o *tsrestgen.APIOutput, _ *envgen.ConfigOutput) {
			o.Identity = &tsrestgen.IdentityInfo{AuthDB: "shop-db"}
		}, false, "server shop-api serves shop-api, which authenticates with the identity runtime over the user model (D50), and an environment places the server on Cloudflare Workers, where the identity runtime does not run", false},
		{"a job's Postgres database", func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
			o.Deps.Database = "shop-db"
			c.Derived = []envgen.DerivedField{{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"}}
		}, true, "job shop-api of shop-api, whose database shop-db lists postgres first, runs on Cloudflare's Cron Triggers in an environment, which reaches a database through a D1 binding alone", false},
		{"an engine database", sqliteDB, false, "server shop-api serves shop-api, whose database shop-db is an engine database whose engine the entrypoint opens (D58), and an environment places the server on a platform whose artifact is a bundle, Cloudflare Workers, which cannot load the engine: it is a native addon, the Rust engine through Node-API (D64); place the server on a platform that builds an image", true},
		{"a job's engine database", sqliteDB, true, "job shop-api serves shop-api, whose database shop-db is an engine database whose engine the entrypoint opens (D58), and an environment places the job on a platform whose artifact is a bundle", true},
		{"an engine database that lists postgres first", func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
			o.Deps.Database = "shop-db"
			c.Derived = []envgen.DerivedField{{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"}}
		}, false, "server shop-api serves shop-api, whose database shop-db is an engine database whose engine the entrypoint opens (D58)", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input(tc.edit)
			if tc.job {
				in.Job = &servergen.JobInput{Name: "Reindex"}
			}
			if tc.engine {
				in.APIs[0].Routes.Engine = &apigen.EngineInfo{Database: "shop-db", Namespace: "shop_db", Mount: "/engine/shop-db"}
			}
			if _, err := servergen.PlanTypeScript(in); err == nil || !strings.Contains(err.Error(), "stack edge-stack: "+tc.want) {
				t.Fatalf("PlanTypeScript = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestTypeScriptEngineOnSQLiteRefused: a TypeScript entrypoint opens an
// engine on Postgres, beside its pools, so a server on Bun is refused,
// saying so, when its API's engine database lists sqlite first, and when
// another API it serves connects to a database that lists sqlite first,
// whose file main.ts would open beside the engine (D58, D64).
func TestTypeScriptEngineOnSQLiteRefused(t *testing.T) {
	api := func(name, db, dialect string, engine bool) servergen.TypeScriptAPIInput {
		routes := &apigen.APIOutput{SchemaName: name}
		if engine {
			routes.Engine = &apigen.EngineInfo{Database: db, Namespace: strings.ReplaceAll(db, "-", "_"), Mount: "/engine/" + db}
		}
		field := strings.ToUpper(strings.ReplaceAll(db, "-", "_")) + "_DATABASE"
		return servergen.TypeScriptAPIInput{
			Output: &tsrestgen.APIOutput{
				SchemaName:   name,
				PackageName:  "@schemas/" + name + "-api",
				HasEnvConfig: true,
				Deps:         tsrestgen.DepsInfo{Database: db, Dialect: dialect},
			},
			Routes: routes,
			Config: &envgen.ConfigOutput{
				SchemaName: name,
				Derived:    []envgen.DerivedField{{Key: field, Kind: ir.EdgeSQL, Service: db}},
			},
			Implementation: servergen.TypeScriptImplementation{Package: "@schemas/" + name + "-implementation"},
		}
	}
	for _, tc := range []struct {
		name string
		apis []servergen.TypeScriptAPIInput
		want string
	}{
		{"its engine database", []servergen.TypeScriptAPIInput{api("shop-api", "shop-db", "sqlite", true)},
			"server shop-api serves shop-api, whose engine database shop-db lists sqlite first; a TypeScript entrypoint opens the engine on Postgres"},
		{"another API's database", []servergen.TypeScriptAPIInput{api("shop-api", "shop-db", "postgres", true), api("shop-notes", "notes-db", "sqlite", false)},
			"server shop-api opens the engine of shop-db (D58), and its database notes-db lists sqlite first; a TypeScript entrypoint that opens an engine opens it and its other databases on Postgres alone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := servergen.TypeScriptInput{Stack: "edge-stack", Server: "shop-api", Dir: t.TempDir(), APIs: tc.apis}
			if _, err := servergen.PlanTypeScript(in); err == nil || !strings.Contains(err.Error(), "stack edge-stack: "+tc.want) {
				t.Fatalf("PlanTypeScript = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestTypeScriptWorkersEntrypointWiring plans a server on Workers whose
// API a site calls, whose operations need an end user and a calling
// service, which registers a route by hand, calls another API, lists a
// bucket and reads a secret: its fetch.ts loads the API's config on the
// isolate's first request, not when the version loads, since a Secrets
// Store secret is read only within a request, with the secret awaited
// beside the string bindings; reads the CORS field from them; builds the
// callee's client with fetch through the service binding its endpoint
// names, and the bucket over its R2 binding; mounts the router with its
// authenticator, its service authenticator and its manual routes; and
// answers CORS before the app, with the client's address (D64).
func TestTypeScriptWorkersEntrypointWiring(t *testing.T) {
	in := servergen.TypeScriptInput{
		Stack:   "edge-stack",
		Server:  "shop-api",
		Dir:     t.TempDir(),
		Workers: true,
		APIs: []servergen.TypeScriptAPIInput{{
			Output: &tsrestgen.APIOutput{
				SchemaName:        "shop-api",
				PackageName:       "@schemas/shop-api-api",
				HasEnvConfig:      true,
				HasServiceCallers: true,
				Endpoints:         []tsrestgen.EndpointInfo{{Name: "getCart", RequiresAuth: true}},
				ManualEndpoints:   []tsrestgen.EndpointInfo{{Name: "exportCart"}},
				Deps: tsrestgen.DepsInfo{
					Calls: []tsrestgen.DepsCall{{
						Service: "shop-pricing", Field: "pricing", Package: "@schemas/shop-pricing-sdk", Client: "ShopPricingSDK",
					}},
					Buckets: []tsrestgen.DepsBucket{{Service: "shop-media", Field: "media"}},
				},
			},
			Routes: &apigen.APIOutput{SchemaName: "shop-api"},
			Config: &envgen.ConfigOutput{
				SchemaName:   "shop-api",
				CallersField: ir.CallersField("shop-api"),
				Fields:       []envgen.ConfigField{{Key: "STRIPE_KEY", Secret: true}, {Key: "LOG_LEVEL"}},
				Derived: []envgen.DerivedField{
					{Key: "SHOP_PRICING_SERVICE", Kind: ir.EdgeHTTP, Service: "shop-pricing"},
					{Key: "SHOP_MEDIA_BUCKET", Kind: ir.EdgeBucket, Service: "shop-media"},
				},
			},
			Implementation: servergen.TypeScriptImplementation{Package: "@schemas/shop-api-implementation"},
			CORS:           true,
		}},
	}
	s, err := servergen.PlanTypeScript(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.WriteTypeScript(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	fetch, err := os.ReadFile(filepath.Join(in.Dir, servergen.TypeScriptFetchFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"import { binding, perIsolate, requestIP, serviceFetch, stackEnvOf, stackEnvWithSecrets } from '@superschematic/http-runtime/workers';",
		"import { openR2Bucket } from '@superschematic/http-runtime/r2';",
		"// shop-api's config holds Secret<T> fields, which a Worker reads only within\n// a request, so the isolate's first request loads it.",
		"const values = await stackEnvWithSecrets(env, ['STRIPE_KEY']);",
		"const shopApiConfig = configure('shop-api', () => shopApiApi.loadEnvConfig(values));",
		"const shopApiOrigins = configure('shop-api', () => loadCors('SHOP_API_CORS', values));",
		"const shopPricingClient = newShopPricingClient(shopApiConfig.SHOP_PRICING_SERVICE);",
		"const shopMediaBucket = openR2Bucket(binding(env, bindingName('SHOP_MEDIA_BUCKET', shopApiConfig.SHOP_MEDIA_BUCKET.binding), 'r2'), shopApiConfig.SHOP_MEDIA_BUCKET);",
		"media: shopMediaBucket,",
		"fetch: telemetry.traced(serviceFetch(binding(env, bindingName('SHOP_PRICING_SERVICE', endpoint.binding?.name), 'service'))),",
		"const shopApiAuthenticate = await construct('shop-api', 'authenticator', () => shopApiImpl.authenticate(shopApiDeps));",
		"const shopApiManualRoutes = await construct('shop-api', 'manual routes', () => shopApiImpl.createManualRoutes(shopApiDeps));",
		"authenticate: shopApiAuthenticate, authenticateService: serviceAuthenticator(shopApiConfig.SHOP_API_CALLERS), manualRoutes: shopApiManualRoutes",
		"(request: Request) => app.fetch(request, { requestIP })",
		"return corsHandler(",
		"const server = perIsolate(build);",
	} {
		if !strings.Contains(string(fetch), want) {
			t.Errorf("fetch.ts does not hold %q:\n%s", want, fetch)
		}
	}
	if strings.Contains(string(fetch), "loadEnvConfig(stackEnvOf(env))") {
		t.Errorf("fetch.ts loads a config with a Secret<T> field when the version loads:\n%s", fetch)
	}
}

// TestTypeScriptWorkersEntrypointCompilesAndBundles: ts-pricing's package
// on Workers installs in the output root's workspace with its
// implementation; fetch.ts type-checks against the HTTP runtime's ./workers
// entry and workers.d.ts; bun bundle.ts writes main.js, its source map and
// superscalar's WebAssembly module, the same bytes into two directories of
// different depths, which the bundle's digest needs; and --check passes. A
// fetch.ts that imports node:fs, which Bun's browser target would swap
// for an empty module, fails both (D64).
func TestTypeScriptWorkersEntrypointCompilesAndBundles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist", "workerd", "superscalar_wasm_bg.wasm")); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar's workerd build is not built (scripts/superscalar-dep.sh builds it): %v", err))
	}
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, paths, "ts-pricing", tsWorkersStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, "ts-pricing")
	if err := os.WriteFile(filepath.Join(impl, "index.ts"), []byte(tsPricingImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, output)
		}
		return string(output)
	}
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	pkg := servergen.ServerDir(out, tsWorkersStack, "ts-pricing")
	run(pkg, filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc"), "--noEmit", "-p", "tsconfig.json")

	first, second := t.TempDir(), filepath.Join(t.TempDir(), "a", "b")
	run(pkg, bun, "bundle.ts", first)
	run(pkg, bun, "bundle.ts", second)
	for _, file := range []string{"main.js", "main.js.map", "superscalar_wasm_bg.wasm"} {
		a, err := os.ReadFile(filepath.Join(first, file))
		if err != nil {
			t.Fatalf("the bundle has no %s: %v", file, err)
		}
		b, err := os.ReadFile(filepath.Join(second, file))
		if err != nil || !bytes.Equal(a, b) {
			t.Errorf("%s differs between two bundles of the same code: %v", file, err)
		}
	}
	main, _ := os.ReadFile(filepath.Join(first, "main.js"))
	for _, want := range []string{`from"cloudflare:workers"`, `"./superscalar_wasm_bg.wasm"`, "//# sourceMappingURL=main.js.map"} {
		if !bytes.Contains(main, []byte(want)) {
			t.Errorf("main.js does not hold %s", want)
		}
	}
	// ts-pricing lists the memory store ts-cache, which a Worker holds
	// disabled: the bundle holds no Redis client (D75).
	for _, refused := range []string{"@redis/client", "node-redis", "openRedisMemoryStore"} {
		if bytes.Contains(main, []byte(refused)) {
			t.Errorf("main.js holds %s, the Redis client a Worker never reaches", refused)
		}
	}
	if output := run(pkg, bun, "bundle.ts", "--check"); !strings.Contains(output, "bundle (checked): main.js") {
		t.Errorf("bundle.ts --check printed %s", output)
	}

	fetch := filepath.Join(pkg, servergen.TypeScriptFetchFile)
	source, err := os.ReadFile(fetch)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(fetch, append([]byte("import { readFileSync } from 'node:fs';\nconsole.log(readFileSync);\n"), source...), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"bundle.ts", "--check"}, {"bundle.ts", t.TempDir()}} {
		cmd := exec.Command(bun, args...)
		cmd.Dir = pkg
		output, err := cmd.CombinedOutput()
		if err == nil || !strings.Contains(string(output), "fetch.ts imports node:fs, which a Worker has only under nodejs_compat") {
			t.Errorf("bun %s over a node:fs import = %v:\n%s", strings.Join(args, " "), err, output)
		}
	}
}

// TestTypeScriptWorkersEntrypointCallsInProcess plans a server on Workers
// that serves shop-api and shop-pricing, with shop-api calling both
// shop-pricing and shop-quotes, another server's: the call to shop-pricing
// stays in process, its client's fetch handing each request to the
// server's own app, which the build sets once it has mounted every router;
// the call to shop-quotes goes through the service binding its endpoint
// names (D64, section 18.2). A server whose every call stays in process
// imports neither the binding lookup nor the service binding's transport.
func TestTypeScriptWorkersEntrypointCallsInProcess(t *testing.T) {
	plan := func(callees ...string) string {
		t.Helper()
		var calls []tsrestgen.DepsCall
		var derived []envgen.DerivedField
		for _, callee := range callees {
			calls = append(calls, tsrestgen.DepsCall{Service: callee, Field: strings.TrimPrefix(callee, "shop-"), Package: "@schemas/" + callee + "-sdk", Client: "SDK"})
			derived = append(derived, envgen.DerivedField{Key: strings.ToUpper(strings.ReplaceAll(callee, "-", "_")) + "_SERVICE", Kind: ir.EdgeHTTP, Service: callee})
		}
		api := func(name string, calls []tsrestgen.DepsCall, derived []envgen.DerivedField) servergen.TypeScriptAPIInput {
			return servergen.TypeScriptAPIInput{
				Output: &tsrestgen.APIOutput{
					SchemaName: name, PackageName: "@schemas/" + name + "-api", HasEnvConfig: len(calls) > 0,
					Deps: tsrestgen.DepsInfo{Calls: calls},
				},
				Routes:         &apigen.APIOutput{SchemaName: name},
				Config:         &envgen.ConfigOutput{SchemaName: name, Derived: derived},
				Implementation: servergen.TypeScriptImplementation{Package: "@schemas/" + name + "-implementation"},
			}
		}
		in := servergen.TypeScriptInput{
			Stack: "edge-stack", Server: "edge", Dir: t.TempDir(), Workers: true,
			APIs: []servergen.TypeScriptAPIInput{api("shop-api", calls, derived), api("shop-pricing", nil, nil)},
		}
		s, err := servergen.PlanTypeScript(in)
		if err != nil {
			t.Fatal(err)
		}
		if err := servergen.WriteTypeScript(s, in.Dir); err != nil {
			t.Fatal(err)
		}
		fetch, err := os.ReadFile(filepath.Join(in.Dir, servergen.TypeScriptFetchFile))
		if err != nil {
			t.Fatal(err)
		}
		return string(fetch)
	}

	fetch := plan("shop-pricing", "shop-quotes")
	for _, want := range []string{
		"import { binding, perIsolate, requestIP, serviceFetch, stackEnvOf } from '@superschematic/http-runtime/workers';",
		"let self: Handle | undefined;",
		"  app.onError(errorHandler());\n  self = request => app.fetch(request, { requestIP });\n",
		"const inProcess = (async (input: RequestInfo | URL, init?: RequestInit) => {",
		"    baseUrl: endpoint.url,\n    fetch: telemetry.traced(inProcess),\n",
		"    fetch: telemetry.traced(serviceFetch(binding(env, bindingName('SHOP_QUOTES_SERVICE', endpoint.binding?.name), 'service'))),",
		"function bindingName(field: string, name: string | undefined): string {",
	} {
		if !strings.Contains(fetch, want) {
			t.Errorf("fetch.ts does not hold %q:\n%s", want, fetch)
		}
	}
	if strings.Contains(fetch, "bindingName('SHOP_PRICING_SERVICE'") {
		t.Errorf("fetch.ts reaches shop-pricing, which it serves, through a binding:\n%s", fetch)
	}

	alone := plan("shop-pricing")
	for _, unwanted := range []string{"serviceFetch", "binding,", "function bindingName"} {
		if strings.Contains(alone, unwanted) {
			t.Errorf("fetch.ts whose every call stays in process holds %q:\n%s", unwanted, alone)
		}
	}
	if !strings.Contains(alone, "fetch: telemetry.traced(inProcess),") {
		t.Errorf("fetch.ts does not call shop-pricing in process:\n%s", alone)
	}
}

// tsTillStack is the fixture's stack whose server tsTillServer serves
// tsCheckout and ts-pricing on stacktest's fake bundle platform, as
// Cloudflare Workers (D64): tsCheckout calls ts-pricing, whose quotes admit
// only calling services, so the call stays in process and carries the
// server's loopback credential (docs/stack-model.md, sections 9.9 and
// 18.2).
const (
	tsTillStack  = "ts-till-stack"
	tsTillServer = "Till"
	tsCheckout   = "ts-checkout"
)

// tsCheckoutImplementation answers a checkout with ts-pricing's quote,
// which it asks for with the client in its Deps.
const tsCheckoutImplementation = `import type { Constructor } from '@schemas/ts-checkout-api';

export const create: Constructor = deps => ({
  checkouts: {
    async getCheckout({ sku }, ctx) {
      const quote = await deps.tsPricing.quotes.getQuote(sku, { forward: ctx });
      return { sku, cents: quote.cents };
    },
  },
});
`

// TestTypeScriptWorkersCallAnAPIItServesWithItsLoopbackKey: Till, on
// Workers, serves ts-checkout and ts-pricing, whose quotes admit only
// calling services. Its fetch.ts generates the loopback key within the
// isolate's build, as main.ts does, and the client of ts-pricing, which
// stays in process, sends its credential when the endpoint carries none:
// the transport is the isolate's own app, so the token never leaves it,
// whatever host the endpoint names. ts-pricing's router accepts the key
// beside its callers field. The package type-checks and its bundle passes
// bundle.ts --check, and under workerd a checkout reaches the quote while
// a quote asked for from outside with no credential, or with a token
// Till's key did not sign, is 401 (D64, D37 amended; docs/stack-model.md,
// sections 9.9 and 18.2).
func TestTypeScriptWorkersCallAnAPIItServesWithItsLoopbackKey(t *testing.T) {
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	paths := tsFakePaths(repoRoot)
	if !testing.Short() {
		paths = testpaths.Local(t)
	}
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, paths, "ts-pricing", tsCheckout, tsTillStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	pkg := servergen.ServerDir(out, tsTillStack, tsTillServer)
	fetch, err := os.ReadFile(filepath.Join(pkg, servergen.TypeScriptFetchFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"loopbackServiceAuth, type Loopback",
		"  const loopback = await loopbackServiceAuth('Till', ['ts-checkout', 'ts-pricing']);\n",
		"const tsPricingClient = newTsPricingClient(tsCheckoutConfig.TS_PRICING_SERVICE, loopback);",
		"function newTsPricingClient(endpoint: Service, loopback: Loopback): tsPricingSdk.TsPricingSDK {",
		"    fetch: telemetry.traced(inProcess),\n    serviceCredential: serviceCredentialFor(endpoint.credential) ?? loopback.credential,\n",
		"authenticateService: serviceAuthenticator(loopback.accept(tsPricingConfig.TS_PRICING_CALLERS))",
	} {
		if !strings.Contains(string(fetch), want) {
			t.Errorf("fetch.ts does not hold %q:\n%s", want, fetch)
		}
	}
	if strings.Index(string(fetch), "const loopback =") > strings.Index(string(fetch), "const tsPricingClient =") {
		t.Errorf("fetch.ts builds the client before the loopback key:\n%s", fetch)
	}
	if testing.Short() {
		return
	}

	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	installTypeScriptRuntime(t, bun, paths)
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist", "workerd", "superscalar_wasm_bg.wasm")); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar's workerd build is not built (scripts/superscalar-dep.sh builds it): %v", err))
	}
	names := naming.Default()
	for service, code := range map[string]string{"ts-pricing": tsPricingImplementation, tsCheckout: tsCheckoutImplementation} {
		if err := os.WriteFile(filepath.Join(names.TypeScriptImplementationDir(repoRoot, service), "index.ts"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	run := func(dir, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, output)
		}
		return string(output)
	}
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, dir := range []string{names.TypeScriptImplementationDir(repoRoot, tsCheckout), pkg} {
		run(dir, tsc, "--noEmit", "-p", "tsconfig.json")
	}
	if output := run(pkg, bun, "bundle.ts", "--check"); !strings.Contains(output, "bundle (checked): main.js") {
		t.Errorf("bundle.ts --check printed %s", output)
	}

	workerd := filepath.Join(paths.HTTPRuntimeTypeScript, "test", "workerd", "node_modules", ".bin", "workerd")
	if _, err := os.Stat(workerd); err != nil {
		testpaths.RequireOrSkipTS(t, "workerd is not installed: run bun install --frozen-lockfile in runtime/http/typescript/test/workerd")
		return
	}
	dir := t.TempDir()
	run(pkg, bun, "bundle.ts", dir)
	for _, file := range []string{"till.capnp", "tester.js"} {
		data, err := os.ReadFile(filepath.Join("testdata", "workerd", "till-loopback", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output := run(dir, workerd, "test", "till.capnp")
	if testing.Verbose() {
		t.Logf("workerd test till.capnp:\n%s", output)
	}
	for _, want := range []string{
		"Till under workerd: the in-process call carried the loopback credential",
		`"msg":"quoted","stack":"ts-till-stack","server":"Till","api":"ts-pricing","sku":"tea","caller":"Till"}`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("workerd's output does not hold %s:\n%s", want, output)
		}
	}
}
