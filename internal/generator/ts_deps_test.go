package generator

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The TypeScript deps fixtures (D51): deps-ts-shop, a TypeScript API with
// a database (deps-db), a callee (deps-ts-pricing), a setting and a service
// clause, and deps-ts-pricing, which has none of them.
const tsDepsGoldenDir = "testdata/ts-deps/golden"

// buildTSDeps runs the fixtures' generators into outputRoot, under the
// repository root repoRoot, scaffolding the implementations there. The
// callee builds first, as the build plan orders it.
func buildTSDeps(t *testing.T, outputRoot, repoRoot string, paths naming.LocalPaths) {
	t.Helper()
	configs := map[string]*schemaconfig.SchemaConfig{}
	schemas := map[string]*ir.Schema{}
	for _, name := range []string{"deps-db", "deps-ts-pricing", "deps-ts-shop"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	for _, service := range []string{"deps-ts-pricing", "deps-ts-shop"} {
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			},
			Stage:              registry.StageAll,
			ImplementationRoot: repoRoot,
			RepositoryRoot:     repoRoot,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
}

// TestTypeScriptDepsAndScaffoldGolden: deps-ts-shop's package declares
// Deps with its config, a pg Pool, a client of deps-ts-pricing and a
// logger, and the factory type of its authenticate; config.ts joins its
// setting with the database and service fields and its callers field;
// deps-ts-pricing's Deps holds the logger alone. The scaffold writes each
// implementation, and the output root is the workspace of both. Regenerate
// with:
//
//	go test ./internal/generator -run TestTypeScriptDepsAndScaffoldGolden -update
func TestTypeScriptDepsAndScaffoldGolden(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildTSDeps(t, outputRoot, repoRoot, naming.LocalPaths{})

	names := naming.Default()
	files := map[string]string{"package.json": filepath.Join(outputRoot, "package.json")}
	for _, file := range []string{"deps.ts", "config.ts", "index.ts", "package.json", "values-schema.json"} {
		files[filepath.Join("api", "deps-ts-shop", file)] = filepath.Join(APIDir(outputRoot, "deps-ts-shop"), file)
	}
	for _, file := range []string{"deps.ts", "index.ts"} {
		files[filepath.Join("api", "deps-ts-pricing", file)] = filepath.Join(APIDir(outputRoot, "deps-ts-pricing"), file)
	}
	for _, file := range []string{"index.ts", "package.json", "tsconfig.json"} {
		files[filepath.Join("typescript", "deps-ts-shop", file)] = filepath.Join(names.TypeScriptImplementationDir(repoRoot, "deps-ts-shop"), file)
	}
	files[filepath.Join("typescript", "deps-ts-pricing", "index.ts")] = filepath.Join(names.TypeScriptImplementationDir(repoRoot, "deps-ts-pricing"), "index.ts")
	if _, err := os.Stat(filepath.Join(APIDir(outputRoot, "deps-ts-pricing"), "config.ts")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deps-ts-pricing has no configuration, yet its package has config.ts: %v", err)
	}

	rels := make([]string, 0, len(files))
	for rel := range files {
		rels = append(rels, rel)
	}
	sort.Strings(rels)
	for _, rel := range rels {
		got, err := os.ReadFile(files[rel])
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join(tsDepsGoldenDir, rel)
		if *updateNamingGolden {
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
			t.Errorf("%s differs from golden (run with -update to accept):\n--- got ---\n%s", rel, got)
		}
	}
}

// TestTypeScriptScaffoldNeverOverwrites: a build writes the implementation
// only while its package is missing, and never touches a package that
// holds a .ts file, whether the scaffold's own or another.
func TestTypeScriptScaffoldNeverOverwrites(t *testing.T) {
	repoRoot := t.TempDir()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildTSDeps(t, outputRoot, repoRoot, naming.LocalPaths{})
	dir := naming.Default().TypeScriptImplementationDir(repoRoot, "deps-ts-shop")
	file := filepath.Join(dir, tsrestgen.ImplementationFile)
	edited := []byte("// The engineer's code.\n")
	if err := os.WriteFile(file, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	buildTSDeps(t, outputRoot, repoRoot, naming.LocalPaths{})
	if got, err := os.ReadFile(file); err != nil || string(got) != string(edited) {
		t.Fatalf("a second build rewrote %s: %q, %v", file, got, err)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "shop.ts"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	buildTSDeps(t, outputRoot, repoRoot, naming.LocalPaths{})
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a build scaffolded into a package that holds shop.ts: %v", err)
	}
}

// TestTypeScriptDepsRefusesACalleeWithoutASDK: Deps holds a TypeScript SDK
// client per call, so a callee whose config the build has and that
// generates no TypeScript SDK is refused, naming the switch to turn on.
func TestTypeScriptDepsRefusesACalleeWithoutASDK(t *testing.T) {
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{"deps-db", "deps-ts-pricing", "deps-ts-shop"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, name))
		if err != nil {
			t.Fatal(err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	noSDK := *configs["deps-ts-pricing"]
	noSDK.Outputs = map[string]any{"types": map[string]any{"typescript": map[string]any{"enabled": true}}, "api": map[string]any{"enabled": true, "language": APILanguageTypeScript}}
	configs["deps-ts-pricing"] = &noSDK
	_, err := Run(schemas["deps-ts-shop"], configs["deps-ts-shop"], Options{
		OutputRoot:     t.TempDir(),
		LoadDependency: func(name string) (*ir.Schema, error) { return schemas[name], nil },
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			c, ok := configs[name]
			return c, ok
		},
	})
	if err == nil || !strings.Contains(err.Error(), "the TypeScript server of deps-ts-shop holds a client of deps-ts-pricing in its Deps, and deps-ts-pricing generates none; enable outputs.sdk.typescript in deps-ts-pricing's config") {
		t.Fatalf("Run = %v, want the refusal of a callee without a TypeScript SDK", err)
	}
}

// tsDepsUse type-checks a use of every member of deps-ts-shop's Deps, and
// the scaffold's create and authenticate against the generated types.
const tsDepsUse = `import { createLogger } from '@superschematic/http-runtime';
import type { AuthenticatorFactory, Constructor, Deps, EnvConfig } from '@schemas/deps-ts-shop-api';
import { authenticate, create } from './index';

export const constructor: Constructor = create;
export const factory: AuthenticatorFactory = authenticate;

export async function usesDeps(deps: Deps): Promise<string> {
  const config: EnvConfig = deps.config;
  const region: string = config.STOREFRONT_REGION;
  const database = config.DEPS_DB_DATABASE;
  const where: string = 'url' in database ? database.url : database.cloudSql.instance;
  const issuers: number = config.DEPS_TS_SHOP_CALLERS.issuers.length;
  const pricing: string = config.DEPS_TS_PRICING_SERVICE.url;
  const { rows } = await deps.db.query<{ one: number }>('SELECT 1 AS one');
  const quotes = deps.depsTsPricing.quotes;
  deps.logger.child({ region }).info('used', { where, issuers, pricing, rows: rows.length, quotes: typeof quotes });
  return region;
}

export const logger = createLogger({ api: 'deps-ts-shop' });
`

// tsDepsTest builds the implementation from Deps, as the entrypoint does:
// it loads the configuration from the variables ir.DerivedVariables made
// (env.json), and calls an operation, which answers 501.
const tsDepsTest = `import { expect, test } from 'bun:test';
import { readFileSync } from 'node:fs';
import { HttpProblem, createLogger, type RequestContext } from '@superschematic/http-runtime';
import { loadEnvConfig, type Deps } from '@schemas/deps-ts-shop-api';
import { authenticate, create } from './index';

const env = JSON.parse(readFileSync(new URL('./env.json', import.meta.url), 'utf8')) as Record<string, string>;

test('the implementation builds from Deps and answers 501', async () => {
  const config = loadEnvConfig(env);
  expect(config.STOREFRONT_REGION).toBe('eu');
  expect(config.DEPS_DB_DATABASE).toEqual({ cloudSql: { instance: 'acme:us-central1:shop', database: 'deps_db', user: 'deps-ts-shop@acme.iam' } });
  expect(config.DEPS_TS_PRICING_SERVICE.credential?.source).toBe('google-id-token');
  expect(config.DEPS_TS_SHOP_CALLERS).toEqual({ issuers: [] });
  const deps: Deps = {
    config,
    db: {} as Deps['db'],
    depsTsPricing: {} as Deps['depsTsPricing'],
    logger: createLogger({ api: 'deps-ts-shop' }, { write: () => {} }),
  };
  const implementations = await create(deps);
  let refusal: unknown;
  try {
    await implementations.carts.getCart({ id: 'c-1' }, {} as RequestContext);
  } catch (err) {
    refusal = err;
  }
  expect(refusal).toBeInstanceOf(HttpProblem);
  expect((refusal as HttpProblem).status).toBe(501);
  const authenticator = await authenticate(deps);
  expect(await authenticator({} as RequestContext)).toBeNull();
});

test('loadEnvConfig names every variable missing', () => {
  expect(() => loadEnvConfig({})).toThrow(/DEPS_DB_DATABASE_URL[\s\S]*DEPS_TS_PRICING_SERVICE_URL[\s\S]*DEPS_TS_SHOP_CALLERS_ISSUERS/);
});
`

// TestTypeScriptDepsAndScaffoldCompile: one Bun install at the output root
// resolves the scaffolded implementations' imports through the workspace,
// the generated API packages and the implementations, with a use of every
// Deps member, type-check, and the shop's implementation builds from the
// configuration loadEnvConfig reads and answers 501.
func TestTypeScriptDepsAndScaffoldCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist")); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar TypeScript binding not built: %v", err))
	}
	run := func(dir string, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, out)
		}
		return string(out)
	}
	// The workspace overrides the runtime with its checkout, which a
	// consumer reads from its dist/.
	install := exec.Command(bunPath, "install", "--frozen-lockfile")
	install.Dir = paths.HTTPRuntimeTypeScript
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed for the http runtime (likely offline): %v\n%s", err, out))
	}
	run(paths.HTTPRuntimeTypeScript, bunPath, "run", "build")

	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	buildTSDeps(t, outputRoot, repoRoot, paths)
	names := naming.Default()
	shopDir := names.TypeScriptImplementationDir(repoRoot, "deps-ts-shop")
	if err := os.WriteFile(filepath.Join(shopDir, "uses-deps.ts"), []byte(tsDepsUse), 0o644); err != nil {
		t.Fatal(err)
	}

	first := exec.Command(bunPath, "install")
	first.Dir = outputRoot
	if out, err := first.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, out))
	}
	// Again, from an implementation's member of the workspace: the root's
	// lockfile is current, and an install from a generated package finds
	// the root above it.
	run(outputRoot, bunPath, "install", "--frozen-lockfile")
	run(APIDir(outputRoot, "deps-ts-shop"), bunPath, "install")
	if _, err := os.Stat(filepath.Join(outputRoot, "bun.lock")); err != nil {
		t.Fatalf("the workspace lockfile is not at the output root: %v", err)
	}

	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, dir := range []string{
		APIDir(outputRoot, "deps-ts-pricing"),
		APIDir(outputRoot, "deps-ts-shop"),
		names.TypeScriptImplementationDir(repoRoot, "deps-ts-pricing"),
		shopDir,
	} {
		run(dir, tsc, "--noEmit", "-p", "tsconfig.json")
	}

	env := map[string]string{}
	for field, value := range map[string]any{
		"DEPS_DB_DATABASE": ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{
			Instance: "acme:us-central1:shop", Database: "deps_db", User: "deps-ts-shop@acme.iam",
		}},
		"DEPS_TS_PRICING_SERVICE": ir.ServiceEndpoint{URL: "https://deps-ts-pricing.run.app", Credential: &ir.ServiceCredential{
			Source: ir.CredentialGoogleIDToken, Audience: "https://deps-ts-pricing.run.app",
		}},
		ir.CallersField("deps-ts-shop"): ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{}},
	} {
		vars, err := ir.DerivedVariables(field, value)
		if err != nil {
			t.Fatal(err)
		}
		for _, v := range vars {
			env[v.Name] = v.Value.(string)
		}
	}
	data, err := json.Marshal(env)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shopDir, "env.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(shopDir, "deps.test.ts"), []byte(tsDepsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	if out := run(shopDir, bunPath, "test", "deps.test.ts"); !strings.Contains(out, " 0 fail") {
		t.Fatalf("unexpected bun test summary:\n%s", out)
	}
}
