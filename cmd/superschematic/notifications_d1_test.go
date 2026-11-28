package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// ticketsStack deploys tickets-api over tickets-db (#702): on this machine,
// where tickets-db is a SQLite file, and on Cloudflare, where it is a D1
// database. The account, the subdomain, the Secrets Store and the config
// are placeholders, which resolve offline.
const ticketsStack = `import { TicketsApi } from "@schemas/tickets-api";
import { environment, stack } from "@superschematic/stack";

@stack({ deploy: [TicketsApi] })
export abstract class Tickets {}

@environment({
  target: "local",
  settings: [{ of: TicketsApi, env: { SMS: "sms-relay-a", SMS_URL: "http://127.0.0.1:4310" } }],
})
export abstract class Dev {}

@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "tickets-subdomain",
    secretsStoreId: "fedcba9876543210fedcba9876543210",
  },
  settings: [{ of: TicketsApi, env: { SMS: "sms-relay-a", SMS_URL: "https://relay.invalid" } }],
})
export abstract class Edge {}
`

// buildTicketsOnD1 copies examples/notifications' schemas into a project,
// beside a copy of examples/runtime-providers' provider packages, which
// tickets-api lists, makes tickets-db list sqlite first, adds ticketsStack
// and builds it with the installed binary, which scaffolds tickets-api's
// implementation. It returns the project and the output root.
func buildTicketsOnD1(t *testing.T) (string, string) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(filepath.Join(repoRoot, "examples", "deploy-smoke", "schemas", "services", "smoke-stack", "src", "cloudflare-target.ts"))
	if err != nil {
		t.Fatal(err)
	}
	examples := t.TempDir()
	project := filepath.Join(examples, "tickets-d1")
	schemasRoot := filepath.Join(project, "schemas")
	services := filepath.Join(schemasRoot, "services")
	copyTree(t, filepath.Join(repoRoot, "examples", "notifications", "schemas", "services"), services)
	copyTree(t, filepath.Join(repoRoot, "examples", "runtime-providers", "providers"), filepath.Join(examples, "runtime-providers", "providers"))
	rel, err := filepath.Rel(project, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)
	// The agent-orchestration example's naming file and tsconfig, whose
	// paths name the stack's authoring package too.
	for name, replace := range map[string]*strings.Replacer{
		"superschematic.toml": strings.NewReplacer(`"../../`, `"`+rel+"/", `"../.."`, `"`+rel+`"`, "example.com/agent-orchestration", "example.com/tickets-d1"),
		"tsconfig.base.json":  strings.NewReplacer(`"../../../`, `"../`+rel+"/"),
	} {
		data, err := os.ReadFile(filepath.Join(repoRoot, "examples", "agent-orchestration", "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(schemasRoot, name), []byte(replace.Replace(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, data string) {
		t.Helper()
		path := filepath.Join(services, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	config, err := os.ReadFile(filepath.Join(services, "tickets-db", "schema.config.ts"))
	if err != nil {
		t.Fatal(err)
	}
	onSQLite := strings.Replace(string(config), `sql: { dialects: ["postgres"] }`, `sql: { dialects: ["sqlite", "postgres"] }`, 1)
	if onSQLite == string(config) {
		t.Fatal("tickets-db's config lists no postgres dialect to put sqlite before")
	}
	write("tickets-db/schema.config.ts", onSQLite)
	write("tickets-stack/schema.config.ts", `import { defineConfig, SchemaKind } from "@superschematic/schema-config";

export default defineConfig({ name: "tickets-stack", kind: SchemaKind.Stack, outputs: {} });
`)
	write("tickets-stack/package.json", `{ "name": "@schemas/tickets-stack", "private": true }`+"\n")
	write("tickets-stack/tsconfig.json", `{ "extends": "../../tsconfig.base.json", "include": ["schema.config.ts", "src/**/*.ts"] }`+"\n")
	write("tickets-stack/src/stack.schema.ts", ticketsStack+strings.Replace(string(target), "export {};", "", 1))
	t.Chdir(project)
	outputRoot := filepath.Join(schemasRoot, "dist")
	buf := new(bytes.Buffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"build-all", services, "--out", outputRoot})
	if err := root.Execute(); err != nil {
		t.Fatalf("build: %v\n%s", err, buf)
	}
	return project, outputRoot
}

// TestNotificationsOnD1 builds examples/notifications' tickets-api over a
// tickets-db that lists sqlite first, deployed on this machine and on
// Cloudflare (#702): the API package builds the providers and the notifier
// from the runtime's ./d1 entry, which imports no driver, and its behavior
// worker's queues over a SqlDatabase; fetch.ts reaches tickets-db through
// its d1 binding and installs the queue wakes; and each worker's drain.ts,
// the notifier's, an implementation's and the behavior worker's, claims its
// queues over the binding at each firing and each wake of its Workflow
// (D151, D189), as its main.ts claims over the SQLite file under stack
// dev; the notifier's worker asks every channel's health beside its prune.
// But in -short mode, every entrypoint type-checks against the scaffolded
// implementation.
func TestNotificationsOnD1(t *testing.T) {
	goldenDir, err := filepath.Abs(filepath.Join("testdata", "notifications-d1"))
	if err != nil {
		t.Fatal(err)
	}
	project, out := buildTicketsOnD1(t)
	read := func(parts ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{out}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	for _, rel := range []string{
		"api/tickets-api/deps.ts",
		"api/tickets-api/runtime.ts",
		"api/tickets-api/behavior-workers.ts",
		"server/tickets-stack/tickets-api/fetch.ts",
		"server/tickets-stack/tickets-api-notify/main.ts",
		"server/tickets-stack/tickets-api-notify/drain.ts",
		"server/tickets-stack/tickets-api-hear-cleared/drain.ts",
		"server/tickets-stack/tickets-api-duty-deadlines/drain.ts",
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
	for _, rel := range []string{"api/tickets-api/deps.ts", "api/tickets-api/runtime.ts", "api/tickets-api/behavior-workers.ts", "server/tickets-stack/tickets-api/fetch.ts", "server/tickets-stack/tickets-api-notify/drain.ts", "server/tickets-stack/tickets-api-duty-deadlines/drain.ts"} {
		if data := read(strings.Split(rel, "/")...); strings.Contains(data, "/postgres'") || strings.Contains(data, "from 'pg'") || strings.Contains(data, "hyperdrive") {
			t.Errorf("%s reaches Postgres, though tickets-db lists sqlite first:\n%s", rel, data)
		}
	}
	fetch := read("server", "tickets-stack", "tickets-api", "fetch.ts")
	for _, want := range []string{"d1Database('TICKETS_DB_DATABASE',", "ticketsApiApi.withRuntime(", "installQueueWakes(env);"} {
		if !strings.Contains(fetch, want) {
			t.Errorf("tickets-api's fetch.ts has no %s:\n%s", want, fetch)
		}
	}
	drain := read("server", "tickets-stack", "tickets-api-notify", "drain.ts")
	for _, want := range []string{"claimsOver(ticketsDbDatabase, noticeWorkQueue)", "ticketsApiApi.notifyMessage(ticketsApiDeps, claim.message)", "ticketsApiApi.notifierUpkeepOf(ticketsApiDeps)", "await drain({ cron: controller.cron });"} {
		if !strings.Contains(drain, want) {
			t.Errorf("tickets-api-notify's drain.ts has no %s:\n%s", want, drain)
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
	// The provider packages import the runtime as an installed package: the
	// copy the workspace installed for the implementation.
	runtime, err := filepath.EvalSymlinks(filepath.Join(project, "typescript", "tickets-api", "node_modules", "@superschematic", "http-runtime"))
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
	for _, server := range []string{"tickets-api", "tickets-api-notify", "tickets-api-hear-cleared", "tickets-api-duty-deadlines"} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = filepath.Join(out, "server", "tickets-stack", server)
		if output, err := check.CombinedOutput(); err != nil {
			t.Errorf("tsc in %s: %v\n%s", check.Dir, err, output)
		}
	}
}
