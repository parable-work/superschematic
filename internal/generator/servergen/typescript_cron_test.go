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

// The cron fixture (D64): ts-cron-stack deploys ts-reports, whose job
// CompileReports its Edge environment places on stacktest's fake cron
// platform, whose artifact is a bundle as Cloudflare's Cron Triggers' is.
const (
	tsReports     = "ts-reports"
	tsCronStack   = "ts-cron-stack"
	tsCompileJobs = "ts-reports-compile-reports"
)

// tsCronGenerated lists the files of CompileReports' package the build of
// ts-cron-stack writes, by their path under tsGoldenRoot.
func tsCronGenerated(repoRoot string) map[string]string {
	files := map[string]string{}
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), tsCronStack, tsCompileJobs)
	for _, file := range []string{
		servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.TypeScriptConfigFile,
		servergen.TypeScriptScheduledFile, servergen.TypeScriptBundleFile, servergen.TypeScriptWorkersTypesFile,
	} {
		files[filepath.Join("server", tsCronStack, tsCompileJobs, file)] = filepath.Join(dir, file)
	}
	return files
}

// TestTypeScriptCronEntrypointGolden: a TypeScript job that some
// environment places on a platform whose artifact is a bundle gets
// scheduled.ts, its Workers entrypoint, bundle.ts, which bundles it, and
// workers.d.ts beside main.ts, which stack dev still runs, and no fetch.ts;
// its tsconfig.json type-checks scheduled.ts. CompileReports' scheduled.ts
// awaits its API's secret per try, runs two tries of two seconds, and logs
// each. ts-reports lists the memory store ts-cache, which Edge turns off:
// scheduled.ts holds it disabled and imports no Redis entry (D75).
// ts-reports' server, on an image in Edge, gets none of them. Regenerate
// with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptCronEntrypointGolden -update
func TestTypeScriptCronEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), "ts-cache", tsReports, tsCronStack)
	compareGoldens(t, tsGoldenRoot, tsCronGenerated(repoRoot))

	out := filepath.Join(repoRoot, "schemas", "dist")
	job := servergen.ServerDir(out, tsCronStack, tsCompileJobs)
	if _, err := os.Stat(filepath.Join(job, servergen.TypeScriptFetchFile)); err == nil {
		t.Errorf("the job's package has %s", servergen.TypeScriptFetchFile)
	}
	checkScheduledStoreDisabled(t, filepath.Join(job, servergen.TypeScriptScheduledFile))
	server := servergen.ServerDir(out, tsCronStack, tsReports)
	for _, file := range []string{servergen.TypeScriptScheduledFile, servergen.TypeScriptFetchFile, servergen.TypeScriptBundleFile} {
		if _, err := os.Stat(filepath.Join(server, file)); err == nil {
			t.Errorf("ts-reports' server, on an image, has %s", file)
		}
	}
}

// checkScheduledStoreDisabled checks that the scheduled.ts at file holds
// ts-cache as a disabled store and never imports the HTTP runtime's ./redis
// entry, which a Worker cannot run (D75).
func checkScheduledStoreDisabled(t *testing.T, file string) {
	t.Helper()
	scheduled, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(scheduled, []byte("const tsCacheStore = disabledMemoryStore('ts-cache');")) {
		t.Errorf("scheduled.ts does not hold ts-cache disabled:\n%s", scheduled)
	}
	for _, refused := range []string{"/redis", "openRedisMemoryStore"} {
		if bytes.Contains(scheduled, []byte(refused)) {
			t.Errorf("scheduled.ts holds %s, the Redis entry a Worker never reaches:\n%s", refused, scheduled)
		}
	}
}

// cronInput is a job of shop-api on Workers whose API calls shop-pricing,
// lists the bucket shop-media and has a callers field, edited by edit.
func cronInput(t *testing.T, edit func(*tsrestgen.APIOutput, *envgen.ConfigOutput)) servergen.TypeScriptInput {
	output := &tsrestgen.APIOutput{
		SchemaName:        "shop-api",
		PackageName:       "@schemas/shop-api-api",
		HasEnvConfig:      true,
		HasServiceCallers: true,
		Endpoints:         []tsrestgen.EndpointInfo{{Name: "getCart", RequiresAuth: true}},
		Jobs:              []tsrestgen.JobInfo{{Name: "Reindex", Method: "reindex"}},
		Deps: tsrestgen.DepsInfo{
			Calls: []tsrestgen.DepsCall{{
				Service: "shop-pricing", Field: "pricing", Package: "@schemas/shop-pricing-sdk", Client: "ShopPricingSDK",
			}},
			Buckets: []tsrestgen.DepsBucket{{Service: "shop-media", Field: "media"}},
		},
	}
	config := &envgen.ConfigOutput{
		SchemaName:   "shop-api",
		CallersField: ir.CallersField("shop-api"),
		Fields:       []envgen.ConfigField{{Key: "STRIPE_KEY", Secret: true}, {Key: "LOG_LEVEL"}},
		Derived: []envgen.DerivedField{
			{Key: "SHOP_PRICING_SERVICE", Kind: ir.EdgeHTTP, Service: "shop-pricing"},
			{Key: "SHOP_MEDIA_BUCKET", Kind: ir.EdgeBucket, Service: "shop-media"},
		},
	}
	if edit != nil {
		edit(output, config)
	}
	return servergen.TypeScriptInput{
		Stack:   "edge-stack",
		Server:  "shop-api-reindex",
		Dir:     t.TempDir(),
		Workers: true,
		Job:     &servergen.JobInput{Name: "Reindex", TimeoutSeconds: 120, Retries: 2},
		APIs: []servergen.TypeScriptAPIInput{{
			Output:         output,
			Routes:         &apigen.APIOutput{SchemaName: "shop-api"},
			Config:         config,
			Implementation: servergen.TypeScriptImplementation{Package: "@schemas/shop-api-implementation"},
			CORS:           true,
		}},
	}
}

// writeScheduled plans and writes in, and returns its scheduled.ts.
func writeScheduled(t *testing.T, in servergen.TypeScriptInput) string {
	t.Helper()
	s, err := servergen.PlanTypeScript(in)
	if err != nil {
		t.Fatal(err)
	}
	if !s.Workers || s.BundleEntry() != servergen.TypeScriptScheduledFile {
		t.Fatalf("the job on Workers is planned as %+v, bundling %s", s, s.BundleEntry())
	}
	if err := servergen.WriteTypeScript(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(in.Dir, servergen.TypeScriptFetchFile)); err == nil {
		t.Error("the job's package has fetch.ts")
	}
	bundle, err := os.ReadFile(filepath.Join(in.Dir, servergen.TypeScriptBundleFile))
	if err != nil || !strings.Contains(string(bundle), "const ENTRY = 'scheduled.ts';") {
		t.Errorf("bundle.ts does not bundle scheduled.ts: %v\n%s", err, bundle)
	}
	data, err := os.ReadFile(filepath.Join(in.Dir, servergen.TypeScriptScheduledFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestTypeScriptCronEntrypointWiring plans a job on Workers whose API
// calls another, lists a bucket, reads a secret, has a callers field and
// is called by a site: its scheduled.ts loads the config per try, with the
// secret awaited beside the string bindings, through the loader that reads
// no callers field; builds the callee's client through the service binding
// its endpoint names, and the bucket over its R2 binding; answers no CORS,
// authenticates no one and builds nothing per isolate; and runs three
// tries of two minutes. Without a secret, the config loads as the version
// loads too (D64).
func TestTypeScriptCronEntrypointWiring(t *testing.T) {
	scheduled := writeScheduled(t, cronInput(t, nil))
	for _, want := range []string{
		"import { env, WorkflowEntrypoint, type WorkflowEvent, type WorkflowStep } from 'cloudflare:workers';",
		"import { createLogger, serviceCredentialFor, type Service } from '@superschematic/http-runtime';",
		"import { binding, serviceFetch, stackEnvOf, stackEnvWithSecrets } from '@superschematic/http-runtime/workers';",
		"import { openR2Bucket } from '@superschematic/http-runtime/r2';",
		"const TIMEOUT_MS = 120 * 1000;",
		"const TRIES: number = 3;",
		"const logger = createLogger({ stack: 'edge-stack', job: 'shop-api-reindex' });",
		"// shop-api's config holds Secret<T> fields, which a Worker reads only within\n// an invocation, so each try loads it.",
		"const values = await stackEnvWithSecrets(env, ['STRIPE_KEY']);",
		"const shopApiConfig = configure('shop-api', () => shopApiApi.loadBackgroundEnvConfig(values));",
		"const shopPricingClient = newShopPricingClient(shopApiConfig.SHOP_PRICING_SERVICE);",
		"const shopMediaBucket = openR2Bucket(binding(env, bindingName('SHOP_MEDIA_BUCKET', shopApiConfig.SHOP_MEDIA_BUCKET.binding), 'r2'), shopApiConfig.SHOP_MEDIA_BUCKET);",
		"pricing: shopPricingClient,",
		"media: shopMediaBucket,",
		"return construct('shop-api', 'jobs', () => shopApiImpl.createJobs(shopApiDeps));",
		"telemetry.job('Reindex', () => jobs.reindex(signal)),",
		"async scheduled(controller: ScheduledController, _env: unknown, ctx: ExecutionContext): Promise<void> {",
		"ctx.waitUntil(telemetry.flush());",
		"const telemetry = startWorkersTelemetry(env, { service: 'shop-api-reindex', stack: 'edge-stack', kind: 'job', logger });",
		"await runTries({ cron: controller.cron });",
		"logger.info('job started', { api: 'shop-api', class: 'Reindex', ...fields, try: attempt, tries: TRIES });",
		// The job's Workflow (D64, amended).
		"export class JobRun extends WorkflowEntrypoint<unknown, RunPayload | string> {",
		"const running = env['SUPERSCHEMATIC_BUNDLE'];",
		"import { NonRetryableError } from 'cloudflare:workflows';",
		"await step.do('check the bundle', { retries: { limit: 0, delay: 0 } }, async () => {",
		"throw new NonRetryableError(",
		"await step.do('run', { retries: { limit: 0, delay: 0 }, timeout: TIMEOUT_MS * TRIES + 60_000 }, async () => {",
		"await runTries({ instance: event.instanceId });",
		"logger.info('job done', { took: (Date.now() - started) / 1000, try: attempt });",
		"logger.error('job failed', { took: (Date.now() - started) / 1000, try: attempt, tries: TRIES, error });",
		"throw new Error(`job shop-api-reindex failed on each of its ${TRIES} tries:",
		"fetch: telemetry.traced(serviceFetch(binding(env, bindingName('SHOP_PRICING_SERVICE', endpoint.binding?.name), 'service'))),",
	} {
		if !strings.Contains(scheduled, want) {
			t.Errorf("scheduled.ts does not hold %q:\n%s", want, scheduled)
		}
	}
	for _, unwanted := range []string{"perIsolate", "requestIP", "corsHandler", "authenticate", "serviceAuthenticator", "SHOP_API_CALLERS", "loadEnvConfig(", "export default {\n  async fetch"} {
		if strings.Contains(scheduled, unwanted) {
			t.Errorf("scheduled.ts holds %q:\n%s", unwanted, scheduled)
		}
	}

	plain := writeScheduled(t, cronInput(t, func(_ *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
		c.Fields = []envgen.ConfigField{{Key: "LOG_LEVEL"}}
	}))
	for _, want := range []string{
		"configure('shop-api', () => shopApiApi.loadBackgroundEnvConfig(stackEnvOf(env)));",
		"  // The string bindings.\n  const values = stackEnvOf(env);",
	} {
		if !strings.Contains(plain, want) {
			t.Errorf("scheduled.ts with no secret does not hold %q:\n%s", want, plain)
		}
	}
	if strings.Contains(plain, "stackEnvWithSecrets") {
		t.Errorf("scheduled.ts with no secret awaits secrets:\n%s", plain)
	}

	bare := writeScheduled(t, cronInput(t, func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
		o.HasEnvConfig = false
		o.HasServiceCallers = false
		o.Deps = tsrestgen.DepsInfo{}
		*c = envgen.ConfigOutput{SchemaName: "shop-api"}
	}))
	if strings.Contains(bare, "/workers';") || strings.Contains(bare, "const values") {
		t.Errorf("scheduled.ts with no config and no binding reads the bindings:\n%s", bare)
	}
}

// TestTypeScriptCronD1: a job on Workers whose API's database lists sqlite
// first opens it through the d1 binding its connection names, as a
// server's fetch.ts does, and hands it to Deps as db (D64).
func TestTypeScriptCronD1(t *testing.T) {
	scheduled := writeScheduled(t, cronInput(t, func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
		o.Deps.Database = "shop-db"
		o.Deps.Dialect = tsrestgen.SQLiteDialect
		c.Derived = append(c.Derived, envgen.DerivedField{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"})
	}))
	for _, want := range []string{
		"import { createLogger, serviceCredentialFor, type DatabaseConnection, type Service } from '@superschematic/http-runtime';",
		"import type { SqlDatabase } from '@superschematic/http-runtime/d1';",
		"const shopDbDatabase = d1Database('SHOP_DB_DATABASE', shopApiConfig.SHOP_DB_DATABASE);",
		"    db: shopDbDatabase,",
		"function d1Database(field: string, db: DatabaseConnection): SqlDatabase {",
		"return binding(env, db.d1.binding, 'd1');",
	} {
		if !strings.Contains(scheduled, want) {
			t.Errorf("scheduled.ts does not hold %q:\n%s", want, scheduled)
		}
	}
	if strings.Contains(scheduled, "ping") {
		t.Errorf("scheduled.ts pings its database, which a job serves no /readyz for:\n%s", scheduled)
	}
}

// TestTypeScriptCronRefusals: a job on Workers whose API's database lists
// postgres first is refused, since a Worker reaches a database through D1
// alone, and a worker on a bundle platform is refused, since a worker is a
// process that claims until it is stopped, which no Worker runs (D64, D53
// amended).
func TestTypeScriptCronRefusals(t *testing.T) {
	in := cronInput(t, func(o *tsrestgen.APIOutput, c *envgen.ConfigOutput) {
		o.Deps.Database = "shop-db"
		c.Derived = append(c.Derived, envgen.DerivedField{Key: "SHOP_DB_DATABASE", Kind: ir.EdgeSQL, Service: "shop-db"})
	})
	want := "stack edge-stack: job shop-api-reindex of shop-api, whose database shop-db lists postgres first, runs on Cloudflare's Cron Triggers in an environment, which reaches a database through a D1 binding alone"
	if _, err := servergen.PlanTypeScript(in); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("PlanTypeScript = %v, want it to say %q", err, want)
	}
	in = cronInput(t, func(o *tsrestgen.APIOutput, _ *envgen.ConfigOutput) {
		o.Workers = []tsrestgen.WorkerInfo{{Name: "Remind", Method: "remind", Queue: "CartAbandoned"}}
		o.Deps.Database = "shop-db"
		o.Deps.DatabaseTypesPackage = "@schemas/shop-db-types"
	})
	in.Job = nil
	in.Worker = &servergen.WorkerInput{Name: "Remind"}
	want = "stack edge-stack: worker shop-api-reindex runs on a platform whose artifact is a bundle, and a Workers entrypoint is built for a server or a job alone"
	if _, err := servergen.PlanTypeScript(in); err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("PlanTypeScript = %v, want it to say %q", err, want)
	}
}

// tsReportsImplementation answers a report by its id, and implements the
// job CompileReports: each try fails until REPORT_FAILURES tries have
// failed, counted per isolate, and with REPORT_FAILURES hang it waits until
// its signal aborts. It reads its secret, as a try must to log the token's
// length. Each try publishes cartChanged on ts-cache through the store's
// TypeScript definitions, which its scaffold's package.json depends on, and
// logs whether the store took it: Edge holds the store disabled (D75).
const tsReportsImplementation = `import { MemoryStoreUnavailableError, signal as wake } from '@superschematic/http-runtime';
import { cartChanged } from '@schemas/ts-cache-types';
import type { Constructor, JobsConstructor } from '@schemas/ts-reports-api';

export const create: Constructor = () => ({
  reports: {
    async getReport({ id }) {
      return { id };
    },
  },
});

let tries = 0;

export const createJobs: JobsConstructor = deps => ({
  async compileReports(signal) {
    tries++;
    const published = await cartChanged.for('reports').publish(deps.tsCache, wake).then(
      () => true,
      err => {
        if (err instanceof MemoryStoreUnavailableError) return false;
        throw err;
      },
    );
    deps.logger.info('compiling reports', { try: tries, tokenLength: deps.config.REPORT_TOKEN.reveal().length, published });
    if (deps.config.REPORT_FAILURES === 'hang') {
      await new Promise((_, reject) => signal.addEventListener('abort', () => reject(signal.reason)));
    }
    if (tries <= Number(deps.config.REPORT_FAILURES)) throw new Error('try ' + tries + ' of CompileReports fails');
  },
});
`

// TestTypeScriptCronEntrypointCompilesAndBundles: CompileReports' package
// on Cron Triggers installs in the output root's workspace with ts-reports'
// implementation; scheduled.ts type-checks against the HTTP runtime's
// ./workers entry and workers.d.ts; bun bundle.ts writes main.js, its
// source map and superscalar's WebAssembly module, and --check passes.
// ts-reports' implementation publishes on ts-cache through the store's
// definitions, which its scaffold depends on; scheduled.ts holds the store
// disabled, and the bundle holds no Redis client (D75).
// Under the runtime's pinned workerd, the bundle's scheduled handler runs
// as a Cron Trigger fires it (testdata/workerd/cron): a run that succeeds,
// one that succeeds on its retry, one whose tries all fail and one whose
// tries outlive the job's timeout, which throw, each try logged (D64); and
// its Workflow, JobRun, runs as Cloudflare's engine runs an instance,
// through a binding to the entrypoint: it runs the tries within one step
// for the bundle its Worker runs, after a step that checks the bundle,
// which refuses another bundle and none for good, and throws the last
// try's error (D64, amended).
func TestTypeScriptCronEntrypointCompilesAndBundles(t *testing.T) {
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
	f.build(t, repoRoot, paths, "ts-cache", tsReports, tsCronStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, tsReports)
	if err := os.WriteFile(filepath.Join(impl, "index.ts"), []byte(tsReportsImplementation), 0o644); err != nil {
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
	pkg := servergen.ServerDir(out, tsCronStack, tsCompileJobs)
	run(pkg, filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc"), "--noEmit", "-p", "tsconfig.json")
	run(impl, filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc"), "--noEmit", "-p", "tsconfig.json")

	bundle := t.TempDir()
	run(pkg, bun, "bundle.ts", bundle)
	main, err := os.ReadFile(filepath.Join(bundle, "main.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{`from"cloudflare:workers"`, `"./superscalar_wasm_bg.wasm"`, "//# sourceMappingURL=main.js.map", `"ts-cache"`} {
		if !bytes.Contains(main, []byte(want)) {
			t.Errorf("main.js does not hold %s", want)
		}
	}
	checkScheduledStoreDisabled(t, filepath.Join(pkg, servergen.TypeScriptScheduledFile))
	for _, refused := range []string{"@redis/client", "node-redis", "openRedisMemoryStore"} {
		if bytes.Contains(main, []byte(refused)) {
			t.Errorf("main.js holds %s, the Redis client a Worker never reaches", refused)
		}
	}
	if output := run(pkg, bun, "bundle.ts", "--check"); !strings.Contains(output, "bundle (checked): main.js") {
		t.Errorf("bundle.ts --check printed %s", output)
	}

	workerd := filepath.Join(paths.HTTPRuntimeTypeScript, "test", "workerd", "node_modules", ".bin", "workerd")
	if _, err := os.Stat(workerd); err != nil {
		testpaths.RequireOrSkipTS(t, "workerd is not installed: run bun install --frozen-lockfile in runtime/http/typescript/test/workerd")
	}
	dir := t.TempDir()
	for _, file := range []string{"main.js", "superscalar_wasm_bg.wasm"} {
		data, err := os.ReadFile(filepath.Join(bundle, file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, file := range []string{"cron.capnp", "tester.js", "standins.js"} {
		data, err := os.ReadFile(filepath.Join("testdata", "workerd", "cron", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	output := run(dir, workerd, "test", "cron.capnp")
	if testing.Verbose() {
		t.Logf("workerd test cron.capnp:\n%s", output)
	}
	for _, want := range []string{
		"the job under workerd: ok, flaky after a retry, broken and hung failed; its Workflow ran ok, refused another bundle and none, and failed hung",
		`"msg":"job started","stack":"ts-cron-stack","job":"ts-reports-compile-reports","api":"ts-reports","class":"CompileReports","cron":"0 4 * * *","try":2,"tries":2}`,
		`"msg":"job done"`,
		`"msg":"job failed"`,
		"try 1 of CompileReports fails",
		`timed out after 2s, the job's timeout`,
		`"msg":"compiling reports"`,
		`"tokenLength":7,"published":false`,
	} {
		if !strings.Contains(output, want) {
			t.Errorf("workerd's output does not hold %s:\n%s", want, output)
		}
	}
}
