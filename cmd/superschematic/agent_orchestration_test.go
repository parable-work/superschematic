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
// binary, after edit, when set, changes the implementation's index.ts. It
// returns the project, the output root and the build's error.
func buildAgentOrchestration(t *testing.T, edit func(implementation string) string) (string, string, error) {
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
// builds the agent dispatcher into its Deps with the options its
// implementation's createAgentOptions answers, and mounts the agent routes
// a run's sandbox instance calls; the workers of the dispatcher's queues,
// Dispatch (AgentRunRequest) and InboundRuns (RunRequest, D157), hand each
// message to the dispatcher, and the implementation has no method for
// either; Notify's and TaskDeadlines' are the implementation's. But in
// -short mode, the generated server and workers type-check against the
// example's implementation in the output root's workspace.
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
		"workApiApi.withAgents(",
		"workApiImpl.createAgentOptions(workApiDepsBase)",
		"workApiDeps.agents.routes()",
	} {
		if !strings.Contains(server, want) {
			t.Errorf("work-api's main.ts has no %s:\n%s", want, server)
		}
	}
	for worker, transcripts := range map[string]bool{"work-api-dispatch": true, "work-api-inbound-runs": false} {
		main := read("server", "work-stack", worker, "main.ts")
		if !strings.Contains(main, "workApiApi.dispatchAgentMessage(workApiDeps, claim.message)") || strings.Contains(main, "createWorkers(") {
			t.Errorf("%s's main.ts does not hand its messages to the dispatcher:\n%s", worker, main)
		}
		if got := strings.Contains(main, "workApiDeps.agents.prune(limit)"); got != transcripts {
			t.Errorf("%s's main.ts prunes the transcripts: %v, want %v:\n%s", worker, got, transcripts, main)
		}
	}
	if notify := read("server", "work-stack", "work-api-notify", "main.ts"); !strings.Contains(notify, "workers.notify(claim.message, handlerSignal)") {
		t.Errorf("work-api-notify's main.ts does not run the implementation's notify:\n%s", notify)
	}
	deps := read("api", "work-api", "deps.ts")
	if !strings.Contains(deps, "readonly agents: Agents;") || strings.Contains(deps, "dispatch(message") || strings.Contains(deps, "inboundRuns(message") {
		t.Errorf("work-api's deps.ts does not hold the dispatcher, or declares a method of its queues:\n%s", deps)
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
