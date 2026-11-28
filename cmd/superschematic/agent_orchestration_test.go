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

// buildAgentOrchestration copies examples/agent-orchestration's schemas and
// its implementation of work-api into a project, beside a copy of
// examples/runtime-providers' provider packages, which work-api lists and
// its implementation imports, and builds the stack with the installed
// binary, after edit, when set, changes the implementation's index.ts, and
// stack, when given, the stack's schema. It returns the project, the output
// root and the build's error.
func buildAgentOrchestration(t *testing.T, edit func(implementation string) string, stack ...func(schema string) string) (string, string, error) {
	t.Helper()
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	example := filepath.Join(repoRoot, "examples", "agent-orchestration")
	examples := t.TempDir()
	project := filepath.Join(examples, "agent-orchestration")
	schemasRoot := filepath.Join(project, "schemas")
	copyTree(t, filepath.Join(example, "schemas", "services"), filepath.Join(schemasRoot, "services"))
	copyTree(t, filepath.Join(example, "typescript"), filepath.Join(project, "typescript"))
	copyTree(t, filepath.Join(repoRoot, "examples", "runtime-providers", "providers"), filepath.Join(examples, "runtime-providers", "providers"))
	rel, err := filepath.Rel(project, repoRoot)
	if err != nil {
		t.Fatal(err)
	}
	rel = filepath.ToSlash(rel)
	for name, replace := range map[string]*strings.Replacer{
		"superschematic.toml": strings.NewReplacer(`"../../`, `"`+rel+"/", `"../.."`, `"`+rel+`"`),
		"tsconfig.base.json":  strings.NewReplacer(`"../../../`, `"../`+rel+"/"),
	} {
		data, err := os.ReadFile(filepath.Join(example, "schemas", name))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(schemasRoot, name), []byte(replace.Replace(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for _, change := range stack {
		path := filepath.Join(schemasRoot, "services", "work-stack", "src", "stack.schema.ts")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(change(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if edit != nil {
		path := filepath.Join(project, "typescript", "work-api", "index.ts")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(edit(string(data))), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)
	if keep := os.Getenv("AGENTS_EXAMPLE_KEEP"); keep != "" {
		t.Cleanup(func() { _ = exec.Command("cp", "-R", project, keep).Run() })
	}
	outputRoot := filepath.Join(schemasRoot, "dist")
	buf := new(bytes.Buffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs([]string{"build-all", filepath.Join(schemasRoot, "services"), "--out", outputRoot})
	if err := root.Execute(); err != nil {
		return project, outputRoot, fmt.Errorf("%w\n%s", err, buf)
	}
	return project, outputRoot, nil
}

// TestAgentOrchestrationExampleUsesTheGeneratedWiring builds
// examples/agent-orchestration's stack (D183): work-api's generated server
// builds the providers its config names (D159), the notifier (D153), the
// inbound path (D157) and the agent dispatcher into its Deps, the last with
// the options its implementation's createAgentOptions answers, mounts
// their routes beside the router, and runs a grant's token as its holder
// around the implementation's authenticator (D156); the workers of the
// dispatcher's queues, Dispatch (AgentRunRequest) and InboundRuns
// (RunRequest), hand each message to the dispatcher, Notify's to the
// notifier and Indexing's to search's indexing (D155), and the
// implementation has no method for any of them; TaskDeadlines' is the
// implementation's. But in -short mode, the generated server and workers
// type-check against the example's implementation in the output root's
// workspace.
func TestAgentOrchestrationExampleUsesTheGeneratedWiring(t *testing.T) {
	project, out, err := buildAgentOrchestration(t, nil)
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
	server := read("server", "work-stack", "work-api", "main.ts")
	for _, want := range []string{
		"providers: await construct('work-api', 'providers', () => workApiApi.providersOf(workApiConfig,",
		"workApiApi.runtimeOptionsOf(workApiImpl)(workApiDepsBase)",
		"workApiImpl.createAgentOptions(workApiDepsBase)",
		"workApiApi.withRuntime(workApiDepsBase, workApiRuntimeOptions, workApiAgentOptions)",
		"workApiApi.runtimeRoutes(workApiDeps, workApiRuntimeOptions, { authenticate: requestAuthenticator(workApiAuthenticate) })",
		"grantAuthenticator({ orm: workApiDeps.orm, inner: await workApiImpl.authenticate(workApiDeps), runTokens: 'inner' })",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("work-api's main.ts has no %s:\n%s", want, server)
		}
	}
	for worker, handler := range map[string]string{
		"work-api-dispatch":     "workApiApi.dispatchAgentMessage(workApiDeps, claim.message)",
		"work-api-inbound-runs": "workApiApi.dispatchAgentMessage(workApiDeps, claim.message)",
		"work-api-notify":       "workApiApi.notifyMessage(workApiDeps, claim.message)",
		"work-api-indexing":     "workApiApi.indexSearchMessage(workApiDeps, claim.message, workApiRuntimeOptions)",
	} {
		main := read("server", "work-stack", worker, "main.ts")
		if !strings.Contains(main, handler) || strings.Contains(main, "createWorkers(") {
			t.Errorf("%s's main.ts does not hand its messages to %s:\n%s", worker, handler, main)
		}
		if got, want := strings.Contains(main, "workApiDeps.agents.prune(limit)"), worker == "work-api-dispatch"; got != want {
			t.Errorf("%s's main.ts prunes the transcripts: %v, want %v:\n%s", worker, got, want, main)
		}
	}
	deps := read("api", "work-api", "deps.ts")
	for _, want := range []string{"readonly agents: Agents;", "readonly notifier: Notifier;", "readonly inbound: Inbound;", "readonly providers: Providers;"} {
		if !strings.Contains(deps, want) {
			t.Errorf("work-api's deps.ts has no %s:\n%s", want, deps)
		}
	}
	for _, method := range []string{"dispatch(message", "inboundRuns(message", "notify(message", "indexing(message"} {
		if strings.Contains(deps, method) {
			t.Errorf("work-api's deps.ts declares %s, a method of a runtime piece's queue:\n%s", method, deps)
		}
	}
	if providers := read("api", "work-api", "providers.ts"); !strings.Contains(providers, "import localSandboxProvider from '") || !strings.Contains(providers, "SMS: 'channel',") {
		t.Errorf("work-api's providers.ts does not import the listed providers:\n%s", providers)
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
	// The provider packages import the runtime as an installed package, as
	// scripts/link.sh puts it beside them: the copy the workspace installed
	// for the implementation, so both read one runtime.
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
	for _, dir := range []string{
		filepath.Join(out, "server", "work-stack", "work-api"),
		filepath.Join(out, "server", "work-stack", "work-api-dispatch"),
		filepath.Join(out, "server", "work-stack", "work-api-inbound-runs"),
		filepath.Join(out, "server", "work-stack", "work-api-notify"),
		filepath.Join(out, "server", "work-stack", "work-api-indexing"),
		filepath.Join(out, "server", "work-stack", "work-api-task-deadlines"),
		filepath.Join(out, "server", "work-stack", "work-api-run-deadlines"),
		filepath.Join(project, "typescript", "work-api"),
	} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = dir
		if output, err := check.CombinedOutput(); err != nil {
			t.Errorf("tsc in %s: %v\n%s", dir, err, output)
		}
	}
}

// TestAgentOrchestrationExampleRefusesNoAgentOptions: an implementation
// that exports no createAgentOptions, which every entrypoint of work-api
// builds its dispatcher with (D183), is refused by the build, saying what
// to add, rather than when tsc checks an entrypoint or Bun runs it.
func TestAgentOrchestrationExampleRefusesNoAgentOptions(t *testing.T) {
	_, _, err := buildAgentOrchestration(t, func(source string) string {
		return strings.Replace(source, "export const createAgentOptions: AgentOptionsConstructor", "const createAgentOptions: AgentOptionsConstructor", 1)
	})
	for _, want := range []string{"exports no createAgentOptions", "export const createAgentOptions: AgentOptionsConstructor = deps => ({", "importing AgentOptionsConstructor from @schemas/work-api-api"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("the build answered %v, want a refusal that says %q", err, want)
		}
	}
}

// edgeEnvironment is a Cloudflare environment of the work stack, for the
// build alone: work-db over Hyperdrive, the slots bound to the stubs of
// examples/runtime-providers. The account, the subdomain, the Secrets
// Store and the config are placeholders, which resolve offline.
const edgeEnvironment = `
@environment({
  target: "cloudflare",
  cloudflare: {
    accountId: "0123456789abcdef0123456789abcdef",
    workersSubdomain: "work-subdomain",
    secretsStoreId: "fedcba9876543210fedcba9876543210",
  },
  settings: [
    { of: WorkDb, hyperdrive: "00000000000000000000000000000000" },
    {
      of: WorkApi,
      env: { SANDBOX: "local-sandbox", MODEL: "stub-model", SMS: "sms-relay-a", SMS_URL: "https://relay.invalid", AGENT_ENDPOINT: "https://work-api.invalid/api/agents" },
    },
  ],
})
export abstract class Edge {}
`

// TestAgentOrchestrationExampleOnCloudflare builds the work stack with a
// Cloudflare environment beside its local one (D183, D92): work-api's
// fetch.ts builds the same Deps as its main.ts, the providers, the
// notifier, the inbound path and the dispatcher, mounts the same runtime
// routes and runs a grant's token as its holder; each worker's drain.ts
// hands its messages to the same runtime piece as its main.ts. But in
// -short mode, the Workers entrypoints type-check against the example's
// implementation.
func TestAgentOrchestrationExampleOnCloudflare(t *testing.T) {
	// The cloudflare target's authoring types, as deploy-smoke's stack
	// declares them, so @environment takes its values. The goldens are read
	// from here too, before the build changes the directory.
	goldenDir, err := filepath.Abs(filepath.Join("testdata", "agent-orchestration"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(filepath.Join("..", "..", "examples", "deploy-smoke", "schemas", "services", "smoke-stack", "src", "cloudflare-target.ts"))
	if err != nil {
		t.Fatal(err)
	}
	project, out, err := buildAgentOrchestration(t, nil, func(schema string) string {
		schema = strings.Replace(schema, `import { WorkApi } from "@schemas/work-api";`, `import { WorkApi } from "@schemas/work-api";`+"\n"+`import { WorkDb } from "@schemas/work-db";`, 1)
		return schema + edgeEnvironment + strings.Replace(string(target), "export {};", "", 1)
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
	// The generated wiring is held to goldens: the API package's runtime
	// pieces, and the entrypoints that build and mount them, on a process
	// and on Workers.
	for _, rel := range []string{
		"api/work-api/deps.ts",
		"api/work-api/providers.ts",
		"api/work-api/runtime.ts",
		"server/work-stack/work-api/main.ts",
		"server/work-stack/work-api/fetch.ts",
		"server/work-stack/work-api-notify/main.ts",
		"server/work-stack/work-api-notify/drain.ts",
		"server/work-stack/work-api-indexing/drain.ts",
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
	fetch := read("server", "work-stack", "work-api", "fetch.ts")
	for _, want := range []string{
		"providers: await construct('work-api', 'providers', () => workApiApi.providersOf(workApiConfig,",
		"workApiApi.runtimeOptionsOf(workApiImpl)(workApiDepsBase)",
		"workApiImpl.createAgentOptions(workApiDepsBase)",
		"workApiApi.withRuntime(workApiDepsBase, workApiRuntimeOptions, workApiAgentOptions)",
		"workApiApi.runtimeRoutes(workApiDeps, workApiRuntimeOptions, { authenticate: requestAuthenticator(workApiAuthenticate) })",
		"grantAuthenticator({ orm: workApiDeps.orm, inner: await workApiImpl.authenticate(workApiDeps), runTokens: 'inner' })",
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
		if !strings.Contains(drain, handler) || !strings.Contains(drain, "workApiApi.withRuntime(") || strings.Contains(drain, "createWorkers(") {
			t.Errorf("%s's drain.ts does not build the runtime and hand its messages to %s:\n%s", worker, handler, drain)
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
	for _, server := range []string{"work-api", "work-api-dispatch", "work-api-notify", "work-api-indexing"} {
		check := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		check.Dir = filepath.Join(out, "server", "work-stack", server)
		if output, err := check.CombinedOutput(); err != nil {
			t.Errorf("tsc in %s: %v\n%s", check.Dir, err, output)
		}
	}
}
