package ext_test

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/cli"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/tasks"

	"example.com/acme/schematic/ext"
)

// This file is a reader of the task file (D203): a forge in miniature. It
// has what an out-of-tree forge has, the public tasks package, the acme
// binary's `tasks plan` and acme's own section, and with them it runs the
// example's tasks across commits, reusing each pass it has already seen
// by its key.

// taskFilePath is where shop-tasks installs its file, from the repository
// root.
const taskFilePath = "examples/acme-schematic/ci/tasks.json"

// registryRequest is what a task section's Verify receives.
type registryRequest = registry.TaskSectionRequest

// findings records what a Verify reports.
type findings struct{ errs []string }

func (f *findings) Errorf(_ string, format string, args ...any) {
	f.errs = append(f.errs, fmt.Sprintf(format, args...))
}
func (f *findings) Warnf(string, string, ...any) {}

func gitIn(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(),
		"GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_SYSTEM=/dev/null",
		"GIT_AUTHOR_NAME=acme", "GIT_AUTHOR_EMAIL=acme@example.com",
		"GIT_COMMITTER_NAME=acme", "GIT_COMMITTER_EMAIL=acme@example.com",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// exampleRepo is a repository of its own that holds the example where
// this one holds it: the files the example's tasks read, and the task
// file as it is committed here.
func exampleRepo(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	root := t.TempDir()
	example := filepath.Join(root, "examples", "acme-schematic")
	for _, name := range []string{"ci", "cmd", "ext", "schemas", "go.mod", "go.sum"} {
		copyTree(t, filepath.Join("..", name), filepath.Join(example, name))
	}
	gitIn(t, root, "init", "-q", "-b", "main")
	gitIn(t, root, "add", "-A")
	gitIn(t, root, "commit", "-q", "-m", "the example")
	return root
}

// copyTree copies a file or a directory, leaving out what a build writes.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() && (d.Name() == "dist" || d.Name() == "node_modules" || d.Name() == "target") {
			return filepath.SkipDir
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if d.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// planAt runs the acme binary's `tasks plan` and decodes what it prints.
func planAt(t *testing.T, repo, commit string, more ...string) *tasks.Plan {
	t.Helper()
	cmd := cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})
	var stdout, stderr bytes.Buffer
	cmd.SetOut(&stdout)
	cmd.SetErr(&stderr)
	cmd.SetArgs(append([]string{"tasks", "plan", commit, "--repo", repo, "--file", taskFilePath}, more...))
	if err := cmd.Execute(); err != nil {
		t.Fatalf("tasks plan %s: %v\n%s", commit, err, stderr.String())
	}
	plan, err := tasks.DecodePlan(stdout.Bytes())
	if err != nil {
		t.Fatalf("the plan `tasks plan` printed does not decode: %v\n%s", err, stdout.String())
	}
	return plan
}

// forge is a forge in miniature. It keeps each pass it saw under the
// task's name and key, with the outputs the task declared, and it reads
// one section of the plan, acme's, to choose the tasks of a tier.
type forge struct {
	t     *testing.T
	store map[string]map[string][]string
}

// run runs the plan's tasks of a tier in the plan's order, and returns
// the tasks it ran and the tasks it reused. A task whose key the store
// holds is reused. A task that runs has each output it uses put in place
// first, from what the store holds under the upstream task's key.
func (f *forge) run(file *tasks.File, plan *tasks.Plan, tier string) (ran, reused []string) {
	f.t.Helper()
	if f.store == nil {
		f.store = map[string]map[string][]string{}
	}
	for _, name := range plan.Order {
		planned := plan.Tasks[name]
		var placement ext.TaskPlacement
		if err := json.Unmarshal(planned.Sections[ext.TaskSection], &placement); err != nil {
			f.t.Fatalf("task %s: acme's section: %v", name, err)
		}
		if placement.Tier != tier {
			continue
		}
		if planned.Key != nil {
			if _, seen := f.store[name][*planned.Key]; seen {
				reused = append(reused, name)
				continue
			}
		}
		for _, use := range planned.Uses {
			upstream := plan.Tasks[use.Task]
			if upstream.Key == nil {
				f.t.Fatalf("task %s uses %s, which has no key to find its output under", name, use.Task)
			}
			if !contains(f.store[use.Task][*upstream.Key], use.Output) {
				f.t.Fatalf("task %s uses output %s of %s, and the store holds none under key %s", name, use.Output, use.Task, *upstream.Key)
			}
		}
		ran = append(ran, name)
		if planned.Key == nil {
			// A task that is not hermetic has no key: its pass is this
			// run's and no other's.
			continue
		}
		var outputs []string
		for _, out := range file.Tasks[name].Outputs {
			outputs = append(outputs, out.Name)
		}
		if f.store[name] == nil {
			f.store[name] = map[string][]string{}
		}
		f.store[name][*planned.Key] = outputs
	}
	sort.Strings(ran)
	sort.Strings(reused)
	return ran, reused
}

func contains(list []string, s string) bool {
	for _, item := range list {
		if item == s {
			return true
		}
	}
	return false
}

func appendTo(t *testing.T, path, text string) {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, append(data, []byte(text)...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, repo, message string) string {
	t.Helper()
	gitIn(t, repo, "add", "-A")
	gitIn(t, repo, "commit", "-q", "-m", message)
	return gitIn(t, repo, "rev-parse", "HEAD")
}

func expect(t *testing.T, what string, got, want []string) {
	t.Helper()
	if len(got) == 0 && len(want) == 0 {
		return
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s: got %v, want %v", what, got, want)
	}
}

// TestAForgeReusesWorkByKeyAcrossCommits runs the example's tasks as a
// forge would. Every hermetic task runs once on the first commit and is
// reused on the next run of it. An edit then runs the tasks that read the
// edited file and the tasks that use what those wrote, and no other.
func TestAForgeReusesWorkByKeyAcrossCommits(t *testing.T) {
	repo := exampleRepo(t)
	example := filepath.Join(repo, "examples", "acme-schematic")
	committed, err := os.ReadFile(filepath.Join(example, "ci", "tasks.json"))
	if err != nil {
		t.Fatal(err)
	}
	file, err := tasks.Decode(committed)
	if err != nil {
		t.Fatalf("the committed task file does not decode: %v", err)
	}
	// What the forge reads of the file is its own section.
	var section struct {
		Tiers []ext.TaskTier `json:"tiers"`
	}
	if err := json.Unmarshal(file.Sections[ext.TaskSection], &section); err != nil || len(section.Tiers) != 2 || section.Tiers[0].Name != "pr" {
		t.Fatalf("acme's section of the file: %+v, %v", section, err)
	}

	f := &forge{t: t}
	first := planAt(t, repo, "HEAD")
	if first.KeyVersion != "1:sha1" {
		t.Fatalf("key version %s", first.KeyVersion)
	}
	hermetic := []string{"api-build", "schema-build", "task-file", "vet"}
	ran, reused := f.run(file, first, "pr")
	expect(t, "the first run ran", ran, hermetic)
	expect(t, "the first run reused", reused, nil)

	// The same commit again: every task's key is one the store holds.
	ran, reused = f.run(file, planAt(t, repo, "HEAD"), "pr")
	expect(t, "the second run ran", ran, nil)
	expect(t, "the second run reused", reused, hermetic)

	// smoke is not hermetic. It has no key, so it runs on every run of
	// its tier.
	for range 2 {
		ran, reused = f.run(file, first, "nightly")
		expect(t, "a nightly run ran", ran, []string{"smoke"})
		expect(t, "a nightly run reused", reused, nil)
	}

	// An edit to the extension. vet and schema-build read it; api-build
	// uses what schema-build writes, so its key covers schema-build's.
	// task-file reads none of it.
	appendTo(t, filepath.Join(example, "ext", "kind.go"), "\n// An edit.\n")
	second := planAt(t, repo, commit(t, repo, "the extension"))
	ran, reused = f.run(file, second, "pr")
	expect(t, "after an edit to the extension, ran", ran, []string{"api-build", "schema-build", "vet"})
	expect(t, "after an edit to the extension, reused", reused, []string{"task-file"})

	// An edit to shop-db's schema. shop-api names shop-db, so the schema
	// input of schema-build holds it, and api-build follows through
	// uses. vet reads no schema: it comes after nothing and uses nothing.
	// shop-tasks names the services its tasks read, so its own set holds
	// them, and task-file runs too.
	appendTo(t, filepath.Join(example, "schemas", "services", "shop-db", "src", "shop.schema.ts"), "\n// An edit.\n")
	third := planAt(t, repo, commit(t, repo, "shop-db"))
	ran, reused = f.run(file, third, "pr")
	expect(t, "after an edit to shop-db, ran", ran, []string{"api-build", "schema-build", "task-file"})
	expect(t, "after an edit to shop-db, reused", reused, []string{"vet"})

	// A file no task reads changes no key.
	if err := os.WriteFile(filepath.Join(repo, "NOTES.md"), []byte("notes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	fourth := planAt(t, repo, commit(t, repo, "notes"))
	ran, reused = f.run(file, fourth, "pr")
	expect(t, "after an edit no task reads, ran", ran, nil)
	expect(t, "after an edit no task reads, reused", reused, hermetic)

	// A change that edits a task. Planned on its base, vet is keyed on
	// both definitions, so no pass of the task it replaces is reused;
	// api-build comes after vet and uses nothing of it, so it is.
	edited := *file
	edited.Tasks = map[string]tasks.Task{}
	for name, task := range file.Tasks {
		edited.Tasks[name] = task
	}
	vet := edited.Tasks["vet"]
	vet.Run = []string{"go", "vet", "-tags", "acme", "./..."}
	edited.Tasks["vet"] = vet
	data, err := edited.Encode()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(example, "ci", "tasks.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	base := gitIn(t, repo, "rev-parse", "HEAD")
	change := commit(t, repo, "vet with a tag")
	proposed := planAt(t, repo, change, "--base", base)
	if proposed.Tasks["vet"].BaseWorkHash != fourth.Tasks["vet"].WorkHash {
		t.Fatalf("vet's base work hash is %q, want the base's %q", proposed.Tasks["vet"].BaseWorkHash, fourth.Tasks["vet"].WorkHash)
	}
	ran, reused = f.run(&edited, proposed, "pr")
	// task-file reads the committed file, which the change edits.
	expect(t, "a change that edits vet, ran", ran, []string{"task-file", "vet"})
	expect(t, "a change that edits vet, reused", reused, []string{"api-build", "schema-build"})

	// Once the change has landed, the commit is planned with no base, and
	// vet's key is the key of its one definition: it runs once more.
	landed := planAt(t, repo, change)
	if *landed.Tasks["vet"].Key == *proposed.Tasks["vet"].Key {
		t.Fatal("vet's key on its base equals its key once landed")
	}
	ran, _ = f.run(&edited, landed, "pr")
	expect(t, "the landed change ran", ran, []string{"vet"})
}

// TestTheExampleTaskFileIsCurrent: the file committed here is what
// shop-tasks gives, so the reader above reads the schema's own tasks.
func TestTheExampleTaskFileIsCurrent(t *testing.T) {
	if testing.Short() {
		t.Skip("loads the example's TypeScript")
	}
	cmd := cli.New(cli.Config{Name: "acme-schematic"}, ext.Extension{})
	var out bytes.Buffer
	cmd.SetOut(&out)
	cmd.SetErr(&out)
	cmd.SetArgs([]string{"tasks", filepath.Join(schemasRoot, "services", "shop-tasks"), "--check"})
	if err := cmd.Execute(); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
}

// TestTheAcmeSectionHoldsEachTaskToATier: what the section's schemas
// cannot say, its Verify does.
func TestTheAcmeSectionHoldsEachTaskToATier(t *testing.T) {
	reg, _ := assemble(t)
	section, ok := reg.TaskSection(ext.TaskSection)
	if !ok || section.Extension != ext.Name {
		t.Fatalf("section %s: %+v, %v", ext.TaskSection, section, ok)
	}
	tiers := json.RawMessage(`{"tiers":[{"name":"pr","budgetSeconds":180}]}`)
	for _, tc := range []struct {
		name string
		req  registryRequest
		want string
	}{
		{"a task with no tier", registryRequest{File: tiers, Names: []string{"vet"}}, "task vet names no tier"},
		{"a tier the file does not declare", registryRequest{File: tiers, Names: []string{"vet"}, Tasks: map[string]json.RawMessage{"vet": json.RawMessage(`{"tier":"weekly"}`)}}, "task vet runs in tier weekly, which @tasks does not declare"},
		{"a timeout over its tier's budget", registryRequest{File: tiers, Names: []string{"vet"}, Tasks: map[string]json.RawMessage{"vet": json.RawMessage(`{"tier":"pr","timeoutSeconds":600}`)}}, "task vet may run 600 seconds, longer than the 180 its tier pr allows"},
		{"tasks and no tiers", registryRequest{Names: []string{"vet"}}, "the tasks name tiers and @tasks declares none"},
	} {
		var f findings
		section.Verify(tc.req, &f)
		if len(f.errs) != 1 || !strings.Contains(f.errs[0], tc.want) {
			t.Errorf("%s: got %v, want one error holding %q", tc.name, f.errs, tc.want)
		}
	}
	var f findings
	section.Verify(registryRequest{File: tiers, Names: []string{"vet"}, Tasks: map[string]json.RawMessage{"vet": json.RawMessage(`{"tier":"pr","timeoutSeconds":60}`)}}, &f)
	if len(f.errs) != 0 {
		t.Errorf("a task in a declared tier within its budget: %v", f.errs)
	}
}
