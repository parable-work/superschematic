package cigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/cigen"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

// notesRequest is a stack with one environment that builds as one binary
// (D116): a Rust server inside it, on a target that gives CI no way to
// sign in, since it rolls nothing out.
func notesRequest() publicregistry.CIRequest {
	req := storefrontRequest()
	req.Stack.Name, req.Stack.Dir = "notes-stack", "schemas/services/notes-stack"
	req.Stack.TypeScriptImplementations = nil
	req.Environments = []publicregistry.CIEnvironment{{
		Environment: &ir.ResolvedEnvironment{Stack: "notes-stack", Environment: "Desktop", Target: "embedded", Deployables: []*ir.ResolvedDeployable{
			{Name: "notes-api", Kind: ir.DeployableServer, Language: publicregistry.APILanguageRust, Artifact: ir.ArtifactBinary, Services: []ir.ServiceRef{{Name: "notes-api", Kind: ir.SchemaKindAPI}}},
			{Name: "notes-db", Kind: ir.DeployableDatabase, Dialect: "sqlite", Services: []ir.ServiceRef{{Name: "notes-db", Kind: ir.SchemaKindDB}}},
		}},
	}}
	return req
}

// TestABinaryEnvironmentInCheck: for an environment that builds as one
// binary, check holds the plans its releases recorded to what is
// committed and compiles the environment's crate, frozen to the
// workspace's lockfile, after the server's; the workflow deploys nothing
// for it, and says what builds it (D116).
func TestABinaryEnvironmentInCheck(t *testing.T) {
	files, err := cigen.GitHub().Render(notesRequest())
	if err != nil {
		t.Fatal(err)
	}
	w := parseWorkflow(t, files[0].Data)
	check := w.jobs["check"]
	var names []string
	for _, s := range check.Steps {
		names = append(names, s.Name)
	}
	tail := []string{
		"Check the Rust workspace's lockfile schemas/dist/Cargo.lock", "Compile server notes-api",
		"Check the recorded plans of Desktop", "Compile the binary of Desktop",
	}
	if len(names) < len(tail) || !slices.Equal(names[len(names)-len(tail):], tail) {
		t.Fatalf("check's last steps:\n%q\nwant:\n%q", names, tail)
	}
	if s := check.step("Compile the binary of Desktop"); s.WorkingDirectory != "schemas/dist/embedded/notes-stack/Desktop" || s.Run != "cargo check --locked" {
		t.Errorf("check compiles the binary with %+v", s)
	}
	if s := check.step("Check the recorded plans of Desktop"); s.WorkingDirectory != "schemas/services/notes-stack" {
		t.Errorf("check checks the recorded plans with %+v", s)
	}
	if len(w.order) != 1 {
		t.Errorf("the workflow has jobs %v, want check alone: the target rolls nothing out", w.order)
	}
	// The header's notes are wrapped as comments.
	header := strings.Join(strings.Fields(strings.ReplaceAll(string(files[0].Data), "\n#", " ")), " ")
	if want := "Desktop is on the embedded target, which builds one binary and rolls nothing out (D116): superschematic stack build Desktop builds it"; !strings.Contains(header, want) {
		t.Errorf("the workflow does not say what builds Desktop:\n%s", files[0].Data)
	}
}

// TestTheRecordedPlansStepRefusesAnUntrackedOrChangedPlan runs check's
// step on the recorded plans in a repository: a plan git does not track
// fails, saying it is not committed; a committed one passes; and one
// changed after it was committed fails, saying a recorded plan is never
// changed (D116, as D102 holds a projection's stamp).
func TestTheRecordedPlansStepRefusesAnUntrackedOrChangedPlan(t *testing.T) {
	git, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is not installed")
	}
	files, err := cigen.GitHub().Render(notesRequest())
	if err != nil {
		t.Fatal(err)
	}
	script := parseWorkflow(t, files[0].Data).jobs["check"].step("Check the recorded plans of Desktop").Run
	repo := t.TempDir()
	stack := filepath.Join(repo, "schemas", "services", "notes-stack")
	plans := filepath.Join(stack, "plans", "Desktop", "notes-db")
	if err := os.MkdirAll(plans, 0o755); err != nil {
		t.Fatal(err)
	}
	gitIn := func(args ...string) {
		t.Helper()
		cmd := exec.Command(git, append([]string{"-c", "user.name=t", "-c", "user.email=t@example.com"}, args...)...)
		cmd.Dir = repo
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	run := func() (string, error) {
		cmd := exec.Command("bash", "-eo", "pipefail", "-c", script)
		cmd.Dir = stack
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	gitIn("init", "--quiet")
	// No release yet: nothing is recorded, and nothing is refused.
	if output, err := run(); err != nil {
		t.Errorf("a stack with no recorded plan: %v\n%s", err, output)
	}
	first := filepath.Join(plans, "0001.json")
	if err := os.WriteFile(first, []byte("{\"hash\":\"a\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err := run()
	if want := "::error::schemas/services/notes-stack/plans/Desktop holds a recorded plan that is not committed; commit what superschematic stack build Desktop --release wrote"; err == nil || !strings.Contains(output, want) || !strings.Contains(output, "plans/Desktop/notes-db/0001.json") {
		t.Errorf("an untracked plan: %v\n%s", err, output)
	}
	gitIn("add", "schemas/services/notes-stack/plans")
	gitIn("commit", "--quiet", "-m", "release 1")
	if output, err := run(); err != nil {
		t.Errorf("a committed plan left as it was recorded: %v\n%s", err, output)
	}
	if err := os.WriteFile(first, []byte("{\"hash\":\"b\"}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	output, err = run()
	if want := "a recorded plan is never changed"; err == nil || !strings.Contains(output, want) {
		t.Errorf("a plan changed after it was committed: %v\n%s", err, output)
	}
}
