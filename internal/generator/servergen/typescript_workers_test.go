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
