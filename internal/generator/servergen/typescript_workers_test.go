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
// is refused, saying why; a job's entrypoint on Workers is the cron
// platform's (D64).
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
	for _, tc := range []struct {
		name string
		edit func(*tsrestgen.APIOutput, *envgen.ConfigOutput)
		job  bool
		want string
	}{
		{"a Postgres database", func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
			o.Deps.Database = "shop-db"
			c.Derived = []envgen.DerivedField{{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"}}
		}, false, "server shop-api serves shop-api, whose database shop-db lists postgres first, and an environment places the server on Cloudflare Workers, which reaches a database through a D1 binding alone"},
		{"the identity runtime", func(o *tsrestgen.APIOutput, _ *envgen.ConfigOutput) {
			o.Identity = &tsrestgen.IdentityInfo{AuthDB: "shop-db"}
		}, false, "server shop-api serves shop-api, which authenticates with the identity runtime over the user model (D50), and an environment places the server on Cloudflare Workers, where the identity runtime does not run"},
		{"a job", none, true, "job shop-api runs on a platform whose artifact is a bundle, and a Workers entrypoint is built for a server alone"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			in := input(tc.edit)
			if tc.job {
				in.Job = &servergen.JobInput{Name: "Reindex"}
			}
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
		"fetch: serviceFetch(binding(env, bindingName('SHOP_PRICING_SERVICE', endpoint.binding?.name), 'service')),",
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
