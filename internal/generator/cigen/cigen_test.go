package cigen_test

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/cigen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var update = flag.Bool("update", false, "rewrite the golden workflows")

const (
	// fixture is the stack the tests build, in the YAML data form.
	fixture = "testdata/shop-stack"
	// services holds the services the stack deploys.
	services = "../stackgen/testdata/services"
	// golden is the workflow the fixture renders at release version.
	golden  = "testdata/golden/shop-stack.yml"
	version = "1.2.3"
	// workflow is where the generator writes the fixture's workflow,
	// under the output root.
	workflow = "ci/shop-stack/github/shop-stack.yml"
)

// pulumi is the tool the fake provisioner declares in these tests, as the
// pulumi provisioner declares its CLI.
var pulumi = publicregistry.CLITool{Name: "pulumi", Version: "3.259.0"}

// repo is the fixture stack in a repository of its own: the stack's service
// at schemas/services/shop-stack, a bun.lock at the schemas root, and the
// build's output root outside the repository.
type repo struct {
	root  string
	stack string
	out   string
	reg   *registry.Registry
	log   bytes.Buffer
}

type repoOption func(t *testing.T, root string)

// withGit makes root a repository.
func withGit(t *testing.T, root string) {
	if err := os.Mkdir(filepath.Join(root, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
}

// withWorkflows makes the repository's .github/workflows, holding a
// workflow of its own.
func withWorkflows(t *testing.T, root string) {
	dir := filepath.Join(root, ".github", "workflows")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "ci.yml"), []byte("name: ci\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// withOutputs replaces the stack config's outputs block.
func withOutputs(outputs string) repoOption {
	return func(t *testing.T, root string) {
		config := "name: shop-stack\nkind: Stack\noutputs: " + outputs + "\n"
		if err := os.WriteFile(filepath.Join(root, "schemas", "services", "shop-stack", "schema.config.yaml"), []byte(config), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func newRepo(t *testing.T, opts ...repoOption) *repo {
	t.Helper()
	root := t.TempDir()
	stackDir := filepath.Join(root, "schemas", "services", "shop-stack")
	for _, file := range []string{"schema.config.yaml", "src/stack.schema.yaml"} {
		data, err := os.ReadFile(filepath.Join(fixture, file))
		if err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(stackDir, filepath.FromSlash(file))
		if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(root, "schemas", "bun.lock"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, opt := range opts {
		opt(t, root)
	}
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{Tools: []publicregistry.CLITool{pulumi}})
	if err != nil {
		t.Fatal(err)
	}
	return &repo{root: root, stack: stackDir, out: t.TempDir(), reg: reg}
}

// build runs the stack's generators as a single build does.
func (r *repo) build(t *testing.T) (*registry.Result, error) {
	t.Helper()
	schema, cfg, err := loader.LoadServiceWithConfig(r.stack, loader.WithRegistry(r.reg))
	if err != nil {
		return nil, err
	}
	return generator.Run(schema, cfg, generator.Options{
		OutputRoot:  r.out,
		ServicePath: r.stack,
		Registry:    r.reg,
		Log:         &r.log,
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(services, name), loader.WithRegistry(r.reg))
		},
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			return buildplan.ReadConfig(filepath.Join(services, name), r.reg)
		},
	})
}

func (r *repo) mustBuild(t *testing.T) *registry.Result {
	t.Helper()
	result, err := r.build(t)
	if err != nil {
		t.Fatalf("build: %v\n%s", err, r.log.String())
	}
	return result
}

func readFile(t *testing.T, path string) []byte {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// TestGoldenWorkflow renders the stack's workflow and checks it against the
// golden file, which -update rewrites, and its structure: the triggers,
// the jobs in order, the needs chain, each job's condition, the fork guard
// on every pull request job that signs in, and what each job signs in as.
func TestGoldenWorkflow(t *testing.T) {
	cigen.SetReleaseVersion(t, version)
	r := newRepo(t, withGit, withWorkflows)
	result := r.mustBuild(t)
	if got, want := result.Outputs[cigen.Name], cigen.OutDir(r.out, "shop-stack"); got != want {
		t.Errorf("ci output = %q, want %q", got, want)
	}
	got := readFile(t, filepath.Join(r.out, filepath.FromSlash(workflow)))
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if want := readFile(t, golden); !bytes.Equal(got, want) {
		t.Errorf("%s differs from %s; run with -update and review the diff", workflow, golden)
	}

	w := parseWorkflow(t, got)
	if w.name != "shop-stack" {
		t.Errorf("name = %q", w.name)
	}
	if want := []string{"check", "plan-beta", "plan-live", "preview-review", "deploy-beta", "deploy-live"}; !slices.Equal(w.order, want) {
		t.Fatalf("jobs = %v, want %v", w.order, want)
	}
	on := w.on
	if !slices.Equal(on.PullRequest.Branches, []string{"main"}) || !slices.Equal(on.PullRequest.Types, []string{"opened", "synchronize", "reopened", "closed"}) {
		t.Errorf("pull_request = %+v", on.PullRequest)
	}
	if !slices.Equal(on.Push.Branches, []string{"main"}) {
		t.Errorf("push = %+v", on.Push)
	}

	const (
		pullRequest = "github.event_name == 'pull_request' && github.event.action != 'closed' && github.event.pull_request.head.repo.full_name == github.repository"
		preview     = "!cancelled() && github.event_name == 'pull_request' && github.event.pull_request.head.repo.full_name == github.repository && (github.event.action == 'closed' || needs.check.result == 'success')"
		push        = "github.event_name != 'pull_request' && github.ref == 'refs/heads/main'"
	)
	for _, tc := range []struct {
		id, needs, cond, environment, role, account string
	}{
		{"check", "", "github.event.action != 'closed'", "", "", ""},
		{"plan-beta", "check", pullRequest, "", "planner", "shop-stack-planner@acme-beta.fake.test"},
		{"plan-live", "check", pullRequest, "", "planner", "shop-stack-planner@acme-live.fake.test"},
		{"preview-review", "check", preview, "", "deployer", "shop-stack-deployer@acme-beta.fake.test"},
		{"deploy-beta", "check", push, "Beta", "deployer", "shop-stack-deployer@acme-beta.fake.test"},
		{"deploy-live", "deploy-beta", push, "Live", "deployer", "shop-stack-deployer@acme-live.fake.test"},
	} {
		j := w.jobs[tc.id]
		if j.Needs != tc.needs || j.If != tc.cond || j.Environment != tc.environment {
			t.Errorf("%s: needs %q, if %q, environment %q; want %q, %q, %q", tc.id, j.Needs, j.If, j.Environment, tc.needs, tc.cond, tc.environment)
		}
		signIn := j.step("Sign in to Google Cloud as " + tc.role)
		switch {
		case tc.role == "":
			if j.Permissions["id-token"] != "" || signIn != nil {
				t.Errorf("%s asks for an identity token or signs in", tc.id)
			}
		case signIn == nil:
			t.Errorf("%s does not sign in as %s", tc.id, tc.role)
		default:
			if j.Permissions["id-token"] != "write" || j.Permissions["contents"] != "read" {
				t.Errorf("%s: permissions %v", tc.id, j.Permissions)
			}
			if signIn.With["service_account"] != tc.account || !strings.HasSuffix(signIn.With["workload_identity_provider"], "/workloadIdentityPools/shop-stack-ci/providers/ci") {
				t.Errorf("%s signs in with %v", tc.id, signIn.With)
			}
			if strings.HasPrefix(tc.id, "deploy-") == strings.Contains(j.If, "github.repository") {
				t.Errorf("%s: a pull request job, and only one, guards against forks: %s", tc.id, j.If)
			}
		}
		if install := j.step("Install superschematic " + version); install == nil || !strings.Contains(install.Run, "sha256sum --check --ignore-missing SHA256SUMS") ||
			!strings.Contains(install.Run, "https://github.com/parable-work/superschematic/releases/download/v1.2.3") {
			t.Errorf("%s does not install release %s checked against SHA256SUMS", tc.id, version)
		}
		if packages := j.step("Install the schemas root's packages"); packages == nil || packages.Run != "bun install --frozen-lockfile" || packages.WorkingDirectory != "schemas" {
			t.Errorf("%s does not install the schemas root's packages with bun: %+v", tc.id, packages)
		}
	}
	for _, id := range []string{"plan-beta", "preview-review", "deploy-live"} {
		if s := w.jobs[id].step("Install the pulumi CLI"); s == nil || s.With["pulumi-version"] != pulumi.Version {
			t.Errorf("%s does not install the pulumi CLI at %s", id, pulumi.Version)
		}
	}
	if w.jobs["deploy-live"].Concurrency.Group != "shop-stack-Live" || w.jobs["deploy-live"].Concurrency.CancelInProgress {
		t.Errorf("deploy-live concurrency = %+v", w.jobs["deploy-live"].Concurrency)
	}
	if g := w.jobs["preview-review"].Concurrency.Group; g != "shop-stack-Review-${{ github.event.pull_request.number }}" {
		t.Errorf("preview-review concurrency group = %q", g)
	}
	review := w.jobs["preview-review"]
	if s := review.step("Deploy Review for the pull request"); s == nil || s.If != "github.event.action != 'closed'" ||
		s.Run != "superschematic stack deploy Review --stack schemas/services/shop-stack --param pr=${{ github.event.pull_request.number }}" {
		t.Errorf("preview-review's deploy = %+v", s)
	}
	if s := review.step("Destroy Review for the pull request"); s == nil || s.If != "github.event.action == 'closed'" ||
		s.Run != "superschematic stack destroy Review --stack schemas/services/shop-stack --param pr=${{ github.event.pull_request.number }} --yes" {
		t.Errorf("preview-review's destroy = %+v", s)
	}
	check := w.jobs["check"]
	for _, server := range []string{"Orders", "shop-api"} {
		s := check.step("Compile server " + server)
		if s == nil || s.Run != "go build -mod=mod -o /dev/null ." || s.WorkingDirectory != "schemas/dist/server/shop-stack/"+server {
			t.Errorf("check compiles server %s with %+v", server, s)
		}
	}
	if s := check.step("Build the services"); s == nil || s.Run != "superschematic build-all schemas/services" {
		t.Errorf("check builds with %+v", s)
	}
	if !bytes.HasPrefix(got, []byte("# Code generated by superschematic 1.2.3 from stack shop-stack. DO NOT EDIT.\n")) ||
		!bytes.Contains(got, []byte("# - Dev is on the local target: superschematic stack dev runs it.")) {
		t.Errorf("the header does not say what generated the workflow and why Dev has no job:\n%s", got)
	}

	// The install wrote the same file beside the repository's own
	// workflow, which it left alone.
	installed := filepath.Join(r.root, ".github", "workflows", "shop-stack.yml")
	if !bytes.Equal(readFile(t, installed), got) {
		t.Errorf("%s is not the workflow the build wrote", installed)
	}
	if string(readFile(t, filepath.Join(r.root, ".github", "workflows", "ci.yml"))) != "name: ci\n" {
		t.Error("the install changed the repository's own workflow")
	}
}

// TestNoCIWritesNothing: a stack whose config names no CI renderer gets no
// workflow, even in a repository with .github/workflows.
func TestNoCIWritesNothing(t *testing.T) {
	cigen.SetReleaseVersion(t, version)
	r := newRepo(t, withGit, withWorkflows, withOutputs("{}"))
	result := r.mustBuild(t)
	if dir, ok := result.Outputs[cigen.Name]; ok {
		t.Errorf("ci output = %q", dir)
	}
	if _, err := os.Stat(cigen.OutDir(r.out, "shop-stack")); !os.IsNotExist(err) {
		t.Errorf("the build wrote %s: %v", cigen.OutDir(r.out, "shop-stack"), err)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".github", "workflows", "shop-stack.yml")); !os.IsNotExist(err) {
		t.Errorf("the build installed a workflow: %v", err)
	}
}

// TestInstallNeedsTheDirectory: without .github/workflows, or without a
// repository, the build writes the workflow under the output root alone,
// says why it did not install it, and renders it the same.
func TestInstallNeedsTheDirectory(t *testing.T) {
	cigen.SetReleaseVersion(t, version)
	for _, tc := range []struct {
		name string
		opts []repoOption
		log  func(r *repo) string
	}{
		{"no directory", []repoOption{withGit}, func(r *repo) string {
			return "not installed: " + filepath.Join(r.root, ".github", "workflows") + " does not exist"
		}},
		{"no repository", nil, func(r *repo) string { return "not installed: no repository holds " + r.stack }},
	} {
		t.Run(tc.name, func(t *testing.T) {
			r := newRepo(t, tc.opts...)
			r.mustBuild(t)
			got := readFile(t, filepath.Join(r.out, filepath.FromSlash(workflow)))
			if !bytes.Equal(got, readFile(t, golden)) {
				t.Errorf("%s differs from %s", workflow, golden)
			}
			if _, err := os.Stat(filepath.Join(r.root, ".github")); !os.IsNotExist(err) {
				t.Errorf("the build made .github: %v", err)
			}
			if want := "github CI of stack shop-stack " + tc.log(r); !strings.Contains(r.log.String(), want) {
				t.Errorf("the log does not say the install was skipped (%q):\n%s", want, r.log.String())
			}
		})
	}
}

// TestInstallTakesTheConfiguredDirectory: outputs.ci.github.install names
// another directory, and branch another branch.
func TestInstallTakesTheConfiguredDirectory(t *testing.T) {
	cigen.SetReleaseVersion(t, version)
	r := newRepo(t, withGit, withWorkflows, withOutputs(`{ci: {github: {branch: release/v1, install: ci/workflows}}}`))
	if err := os.MkdirAll(filepath.Join(r.root, "ci", "workflows"), 0o755); err != nil {
		t.Fatal(err)
	}
	r.mustBuild(t)
	got := readFile(t, filepath.Join(r.root, "ci", "workflows", "shop-stack.yml"))
	w := parseWorkflow(t, got)
	if !slices.Equal(w.on.Push.Branches, []string{"release/v1"}) || w.jobs["deploy-beta"].If != "github.event_name != 'pull_request' && github.ref == 'refs/heads/release/v1'" {
		t.Errorf("the workflow does not deploy from release/v1: %+v, %q", w.on.Push, w.jobs["deploy-beta"].If)
	}
	if _, err := os.Stat(filepath.Join(r.root, ".github", "workflows", "shop-stack.yml")); !os.IsNotExist(err) {
		t.Errorf("the build installed into .github/workflows as well: %v", err)
	}
}

// TestUnknownRendererIsRefused: outputs.ci naming a renderer no extension
// registered fails the build before anything is written.
func TestUnknownRendererIsRefused(t *testing.T) {
	r := newRepo(t, withGit, withOutputs(`{ci: {circle: {}}}`))
	_, err := r.build(t)
	want := `outputs.ci names CI renderer "circle", which is not registered (registered CI renderers: github)`
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("build = %v, want an error containing %q", err, want)
	}
	if _, err := os.Stat(cigen.OutDir(r.out, "shop-stack")); !os.IsNotExist(err) {
		t.Errorf("the build wrote %s", cigen.OutDir(r.out, "shop-stack"))
	}
}

// TestCheckoutBuild: a binary built from a checkout names no release, so
// every job's install step fails and says to generate the workflow again
// with a release.
func TestCheckoutBuild(t *testing.T) {
	cigen.SetReleaseVersion(t, "")
	r := newRepo(t, withGit)
	r.mustBuild(t)
	got := readFile(t, filepath.Join(r.out, filepath.FromSlash(workflow)))
	if !bytes.HasPrefix(got, []byte("# Code generated by superschematic (a checkout build, no release) from stack shop-stack. DO NOT EDIT.\n")) {
		t.Errorf("the header does not say a checkout build generated it:\n%s", got)
	}
	w := parseWorkflow(t, got)
	for _, id := range w.order {
		s := w.jobs[id].step("Install superschematic")
		if s == nil || !strings.HasSuffix(s.Run, "generate this workflow again with a released superschematic\" && exit 1") {
			t.Errorf("%s's install step = %+v", id, s)
		}
	}
	if bytes.Contains(got, []byte("releases/download")) {
		t.Error("a checkout build's workflow downloads a release")
	}
	lint(t, filepath.Join(r.out, filepath.FromSlash(workflow)))
}

// TestActionlint runs actionlint, when it is installed, over the golden
// workflow.
func TestActionlint(t *testing.T) {
	if _, err := exec.LookPath("actionlint"); err != nil {
		t.Skip("actionlint is not installed")
	}
	lint(t, golden)
}

// lint runs actionlint over a workflow when it is installed, and shellcheck
// over its scripts when that is.
func lint(t *testing.T, path string) {
	t.Helper()
	bin, err := exec.LookPath("actionlint")
	if err != nil {
		return
	}
	if out, err := exec.Command(bin, "-no-color", path).CombinedOutput(); err != nil {
		t.Errorf("actionlint %s: %v\n%s", path, err, out)
	}
}

// TestRendererNamesEnvironmentsWithNoJob: an environment with several
// parameters, one whose target gives no identity yet and one whose target
// has no CI seam get no job, and the header says why; a renderer refuses
// an identity kind or a tool it does not know.
func TestRendererNamesEnvironmentsWithNoJob(t *testing.T) {
	env := func(name string, params ...string) *ir.ResolvedEnvironment {
		return &ir.ResolvedEnvironment{Stack: "shop-stack", Environment: name, Target: stacktest.Target, Parameters: params}
	}
	identity := &publicregistry.CIIdentity{Kind: stacktest.WorkloadIdentity, Fields: map[string]string{"provider": "projects/1/p", "account": "a@b"}}
	req := publicregistry.CIRequest{
		Stack:   publicregistry.CIStack{Name: "shop-stack", Dir: "schemas/services/shop-stack", ServicesRoot: "schemas/services", SchemasRoot: "schemas", OutputRoot: "schemas/dist"},
		Options: publicregistry.CIOptions{Branch: "main"},
		Version: version,
		Environments: []publicregistry.CIEnvironment{
			{Environment: env("Staging"), HasSeam: true, Planner: identity, Deployer: identity},
			{Environment: env("Matrix", "pr", "region"), HasSeam: true, Planner: identity, Deployer: identity},
			{Environment: env("Production"), HasSeam: true},
			{Environment: &ir.ResolvedEnvironment{Stack: "shop-stack", Environment: "Edge", Target: "edge"}},
		},
	}
	files, err := cigen.GitHub().Render(req)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "shop-stack.yml" {
		t.Fatalf("files = %+v", files)
	}
	got := string(files[0].Data)
	for _, note := range []string{
		"# - Matrix takes 2 parameters (pr, region): CI deploys a member of an\n#   environment with one.",
		"# - Production has no CI identity for planner or deployer yet: run\n#   superschematic stack bootstrap Production --stack\n#   schemas/services/shop-stack, which records it, and build again.",
		"# - Edge is on the edge target, which gives a CI job no way to sign in.",
	} {
		if !strings.Contains(got, note) {
			t.Errorf("the header does not say\n%s\n%s", note, got)
		}
	}
	w := parseWorkflow(t, files[0].Data)
	if want := []string{"check", "plan-staging", "deploy-staging"}; !slices.Equal(w.order, want) {
		t.Errorf("jobs = %v, want %v", w.order, want)
	}
	if s := w.jobs["check"].step("Set up Go"); s != nil {
		t.Error("check sets up Go for a stack with no Go server")
	}
	if s := w.jobs["check"].step("Install the schemas root's packages"); s != nil {
		t.Error("check installs packages for a schemas root with no lockfile")
	}

	unknown := req
	unknown.Environments = []publicregistry.CIEnvironment{{Environment: env("Staging"), HasSeam: true,
		Planner: &publicregistry.CIIdentity{Kind: "aws-oidc"}, Deployer: identity}}
	if _, err := cigen.GitHub().Render(unknown); err == nil || !strings.Contains(err.Error(), `cannot sign in with a CI identity of kind "aws-oidc"`) {
		t.Errorf("an unknown identity kind: %v", err)
	}
	tool := req
	tool.Environments = []publicregistry.CIEnvironment{{Environment: env("Staging"), HasSeam: true, Planner: identity, Deployer: identity,
		Tools: []publicregistry.CLITool{{Name: "terraform", Version: "1.9.0"}}}}
	if _, err := cigen.GitHub().Render(tool); err == nil || !strings.Contains(err.Error(), "has no step that installs tool terraform") {
		t.Errorf("an unknown tool: %v", err)
	}
}

// parsed is a workflow as yaml.v3 reads it, its jobs in order.
type parsed struct {
	name  string
	on    triggers
	order []string
	jobs  map[string]parsedJob
}

type triggers struct {
	PullRequest struct {
		Branches []string `yaml:"branches"`
		Types    []string `yaml:"types"`
	} `yaml:"pull_request"`
	Push struct {
		Branches []string `yaml:"branches"`
	} `yaml:"push"`
}

type parsedJob struct {
	Name        string            `yaml:"name"`
	Needs       string            `yaml:"needs"`
	If          string            `yaml:"if"`
	Environment string            `yaml:"environment"`
	Permissions map[string]string `yaml:"permissions"`
	Concurrency struct {
		Group            string `yaml:"group"`
		CancelInProgress bool   `yaml:"cancel-in-progress"`
	} `yaml:"concurrency"`
	Steps []parsedStep `yaml:"steps"`
}

type parsedStep struct {
	Name             string            `yaml:"name"`
	If               string            `yaml:"if"`
	Uses             string            `yaml:"uses"`
	WorkingDirectory string            `yaml:"working-directory"`
	With             map[string]string `yaml:"with"`
	Run              string            `yaml:"run"`
}

func (j parsedJob) step(name string) *parsedStep {
	for i := range j.Steps {
		if j.Steps[i].Name == name {
			return &j.Steps[i]
		}
	}
	return nil
}

func parseWorkflow(t *testing.T, data []byte) parsed {
	t.Helper()
	var doc struct {
		Name string    `yaml:"name"`
		On   triggers  `yaml:"on"`
		Jobs yaml.Node `yaml:"jobs"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the workflow is not YAML: %v\n%s", err, data)
	}
	out := parsed{name: doc.Name, on: doc.On, jobs: map[string]parsedJob{}}
	for i := 0; i+1 < len(doc.Jobs.Content); i += 2 {
		id := doc.Jobs.Content[i].Value
		var j parsedJob
		if err := doc.Jobs.Content[i+1].Decode(&j); err != nil {
			t.Fatalf("job %s: %v", id, err)
		}
		out.order = append(out.order, id)
		out.jobs[id] = j
	}
	return out
}
