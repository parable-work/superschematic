package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
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
// Workers whose APIs open a database, list a bucket or authenticate with
// the identity runtime is refused, saying why, since its Workers
// entrypoint builds none of them yet; a job's entrypoint on Workers is the
// cron platform's (D64).
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
		{"a database", func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
			o.Deps.Database = "shop-db"
			c.Derived = []envgen.DerivedField{{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"}}
		}, false, "server shop-api serves shop-api, whose database is shop-db, and an environment places the server on Cloudflare Workers, which reaches a database through D1 alone"},
		{"a bucket", func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
			o.Deps.Buckets = []tsrestgen.DepsBucket{{Service: "shop-media", Field: "media"}}
			c.Derived = []envgen.DerivedField{{Key: "SHOP_MEDIA_BUCKET", Kind: ir.EdgeBucket, Service: "shop-media"}}
		}, false, "server shop-api serves shop-api, which lists bucket shop-media, and an environment places the server on Cloudflare Workers, whose entrypoint opens no bucket yet"},
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
// service, which registers a route by hand and calls another API: its
// fetch.ts reads the API's config and CORS field from the bindings it
// awaits per isolate, builds the callee's client with fetch through the
// service binding its endpoint names, mounts the router with its
// authenticator, its service authenticator and its manual routes, and
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
				Deps: tsrestgen.DepsInfo{Calls: []tsrestgen.DepsCall{{
					Service: "shop-pricing", Field: "pricing", Package: "@schemas/shop-pricing-sdk", Client: "ShopPricingSDK",
				}}},
			},
			Routes: &apigen.APIOutput{SchemaName: "shop-api"},
			Config: &envgen.ConfigOutput{
				SchemaName:   "shop-api",
				CallersField: ir.CallersField("shop-api"),
				Derived:      []envgen.DerivedField{{Key: "SHOP_PRICING_SERVICE", Kind: ir.EdgeHTTP, Service: "shop-pricing"}},
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
		"import { awaitSecrets, isolate, requestIP, serviceFetch, stackEnvOf } from '@superschematic/http-runtime/workers';",
		"configure('shop-api', () => shopApiApi.loadEnvConfig(stackEnvOf(env)));",
		"const values = await awaitSecrets(env);",
		"const shopApiConfig = configure('shop-api', () => shopApiApi.loadEnvConfig(values));",
		"const shopApiOrigins = configure('shop-api', () => loadCors('SHOP_API_CORS', values));",
		"const shopPricingClient = newShopPricingClient(shopApiConfig.SHOP_PRICING_SERVICE);",
		"fetch: serviceFetch(env, endpoint),",
		"const shopApiAuthenticate = await construct('shop-api', 'authenticator', () => shopApiImpl.authenticate(shopApiDeps));",
		"const shopApiManualRoutes = await construct('shop-api', 'manual routes', () => shopApiImpl.createManualRoutes(shopApiDeps));",
		"authenticate: shopApiAuthenticate, authenticateService: serviceAuthenticator(shopApiConfig.SHOP_API_CALLERS), manualRoutes: shopApiManualRoutes",
		"(request: Request) => app.fetch(request, bindings)",
		"return handle;",
		"const bindings = { requestIP };",
	} {
		if !strings.Contains(string(fetch), want) {
			t.Errorf("fetch.ts does not hold %q:\n%s", want, fetch)
		}
	}
}
