package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// d1Environment is a Cloudflare environment of the work stack over D1, for
// the build alone: work-db on D1, the slots bound to the stubs of
// examples/runtime-providers. The account, the subdomain, the Secrets
// Store and the config are placeholders, which resolve offline.
const d1Environment = `
@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "work-subdomain",
    secretsStoreId: "fedcba9876543210fedcba9876543210",
  },
  settings: [
    {
      of: WorkApi,
      env: { SANDBOX: "local-sandbox", MODEL: "stub-model", SMS: "sms-relay-a", SMS_URL: "https://relay.invalid", AGENT_ENDPOINT: "https://work-api.invalid/api/agents" },
    },
  ],
})
export abstract class Edge {}
`

// onSQLite makes work-db list sqlite first, with its TypeScript types
// alone: the Go repositories read a @versioned table's history by
// Postgres's statements, which the loader refuses beside sqlite (D189).
func onSQLite(config string) string {
	config = strings.Replace(config, `sql: { dialects: ["postgres"] }`, `sql: { dialects: ["sqlite", "postgres"] }`, 1)
	return strings.Replace(config, `[TargetLanguage.Go]: { enabled: true },`, `[TargetLanguage.Go]: { enabled: false },`, 1)
}

// TestAgentOrchestrationExampleOnD1 builds the work stack over a work-db
// that lists sqlite first, with a Cloudflare environment that places it on
// D1 (#702): work-api's package builds the providers, the notifier, the
// inbound path, the dispatcher, the calendar's and search's routes and the
// grants from the runtime's ./d1 entry, which imports no driver; fetch.ts
// reaches work-db through its d1 binding and installs the queue wakes; and
// each worker's drain.ts, the dispatcher's, the notifier's, search's
// indexing and the behavior workers' among them, claims its queues over
// the binding at each firing and each wake of its Workflow (D151, D189),
// as its main.ts claims over the SQLite file under stack dev. But in
// -short mode, the Workers entrypoints and the processes' type-check
// against the example's implementation.
func TestAgentOrchestrationExampleOnD1(t *testing.T) {
	goldenDir, err := filepath.Abs(filepath.Join("testdata", "agent-orchestration-d1"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(filepath.Join("..", "..", "examples", "deploy-smoke", "schemas", "services", "smoke-stack", "src", "cloudflare-target.ts"))
	if err != nil {
		t.Fatal(err)
	}
	project, out, err := buildAgentOrchestrationWith(t, nil, map[string][]func(string) string{
		"work-db/schema.config.ts": {onSQLite},
		"work-stack/src/stack.schema.ts": {func(schema string) string {
			return schema + d1Environment + strings.Replace(string(target), "export {};", "", 1)
		}},
	})
	if err != nil {
		t.Fatalf("build: %v", err)
	}
	read := func(parts ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{out}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, rel := range []string{
		"api/work-api/deps.ts",
		"api/work-api/runtime.ts",
		"api/work-api/behavior-workers.ts",
		"server/work-stack/work-api/main.ts",
		"server/work-stack/work-api/fetch.ts",
		"server/work-stack/work-api-dispatch/drain.ts",
		"server/work-stack/work-api-notify/main.ts",
		"server/work-stack/work-api-notify/drain.ts",
		"server/work-stack/work-api-run-deadlines/drain.ts",
	} {
		got := []byte(read(strings.Split(rel, "/")...))
		golden := filepath.Join(goldenDir, filepath.FromSlash(rel))
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
			t.Fatalf("%v (run go test -update to write it)", err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s differs from %s; run go test -update and review the diff", rel, golden)
		}
	}
	for _, rel := range []string{"api/work-api/deps.ts", "api/work-api/runtime.ts", "api/work-api/behavior-workers.ts", "server/work-stack/work-api/fetch.ts"} {
		data := read(strings.Split(rel, "/")...)
		if strings.Contains(data, "/postgres'") || strings.Contains(data, "from 'pg'") {
			t.Errorf("%s reaches Postgres, though work-db lists sqlite first:\n%s", rel, data)
		}
	}
	fetch := read("server", "work-stack", "work-api", "fetch.ts")
	for _, want := range []string{
		"const workDbDatabase = d1Database('WORK_DB_DATABASE', workApiConfig.WORK_DB_DATABASE);",
		"workApiApi.withRuntime(workApiDepsBase, workApiRuntimeOptions, workApiAgentOptions)",
		"grantAuthenticator({ orm: workApiDeps.orm, inner: await workApiImpl.authenticate(workApiDeps), runTokens: 'inner' })",
		"installQueueWakes(env);",
	} {
		if !strings.Contains(fetch, want) {
			t.Errorf("work-api's fetch.ts has no %s:\n%s", want, fetch)
		}
	}
	for worker, handler := range map[string]string{
		"work-api-dispatch":     "workApiApi.dispatchAgentMessage(workApiDeps, claim.message)",
		"work-api-inbound-runs": "workApiApi.dispatchAgentMessage(workApiDeps, claim.message)",
		"work-api-notify":       "workApiApi.notifyMessage(workApiDeps, claim.message)",
		"work-api-indexing":     "workApiApi.indexSearchMessage(workApiDeps, claim.message, workApiRuntimeOptions)",
	} {
		drain := read("server", "work-stack", worker, "drain.ts")
		if !strings.Contains(drain, handler) || !strings.Contains(drain, "claimsOver(workDbDatabase,") || strings.Contains(drain, "hyperdrive") {
			t.Errorf("%s's drain.ts does not claim over the D1 binding and hand its messages to %s:\n%s", worker, handler, drain)
		}
		main := read("server", "work-stack", worker, "main.ts")
		if !strings.Contains(main, handler) || !strings.Contains(main, "claimsOver(workDbDatabase,") {
			t.Errorf("%s's main.ts does not claim over the SQLite file and hand its messages to %s:\n%s", worker, handler, main)
		}
	}
	if testing.Short() {
		return
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	runtime, err := filepath.EvalSymlinks(filepath.Join(project, "typescript", "work-api", "node_modules", "@superschematic", "http-runtime"))
	if err != nil {
		t.Fatal(err)
	}
	modules := filepath.Join(filepath.Dir(project), "runtime-providers", "node_modules", "@superschematic")
	if err := os.MkdirAll(modules, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(runtime, filepath.Join(modules, "http-runtime")); err != nil {
		t.Fatal(err)
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, server := range []string{"work-api", "work-api-dispatch", "work-api-inbound-runs", "work-api-notify", "work-api-indexing", "work-api-task-deadlines", "work-api-run-deadlines"} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = filepath.Join(out, "server", "work-stack", server)
		if output, err := check.CombinedOutput(); err != nil {
			t.Errorf("tsc in %s: %v\n%s", check.Dir, err, output)
		}
	}
}
