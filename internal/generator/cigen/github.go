package cigen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// GitHubRenderer is the name of the core's CI renderer, GitHub Actions,
// and GitHubDir the directory GitHub reads a repository's workflows from.
const (
	GitHubRenderer = "github"
	GitHubDir      = ".github/workflows"
)

// GitHub is the core's CI renderer: one GitHub Actions workflow per stack,
// `<stack>.yml` (docs/stack-model.md, section 11.3, D47).
func GitHub() registry.CIRendererSpec {
	return registry.CIRendererSpec{Name: GitHubRenderer, Dir: GitHubDir, Render: renderGitHub}
}

// action is a GitHub action pinned by commit, with the release the commit
// is, as this repository's own workflows pin theirs.
type action struct {
	repo, commit, release string
}

func (a action) String() string { return a.repo + "@" + a.commit + " # " + a.release }

// The actions the workflow uses. Every one but google-github-actions/auth
// is at the commit .github/workflows/ci.yml pins, which a test holds them
// to.
var (
	actionCheckout   = action{"actions/checkout", "3d3c42e5aac5ba805825da76410c181273ba90b1", "v7.0.1"}
	actionSetupGo    = action{"actions/setup-go", "b7ad1dad31e06c5925ef5d2fc7ad053ef454303e", "v7.0.0"}
	actionSetupBun   = action{"oven-sh/setup-bun", "0c5077e51419868618aeaa5fe8019c62421857d6", "v2.2.0"}
	actionSetupNode  = action{"actions/setup-node", "820762786026740c76f36085b0efc47a31fe5020", "v7.0.0"}
	actionPulumi     = action{"pulumi/actions", "8e5e406f4007fca908480587cb9893c07090f58d", "v7.0.0"}
	actionGoogleAuth = action{"google-github-actions/auth", "7c6bc770dae815cd3e89ee6cdf493a5fab2cc093", "v3.0.0"}
)

// gcpWorkloadIdentity is the identity kind of gcp's CI seam: Workload
// Identity Federation through the provider bootstrap creates, as the
// role's service account. google-github-actions/auth signs in with it and
// writes a credentials file, which gives superschematic and Pulumi
// application default credentials.
const gcpWorkloadIdentity = "gcp-workload-identity"

// pulumiTool is the pulumi provisioner's CLI, which pulumi/actions
// installs.
const pulumiTool = "pulumi"

// The expressions the jobs' conditions are made of.
const (
	onPullRequest  = "github.event_name == 'pull_request'"
	notClosed      = "github.event.action != 'closed'"
	closed         = "github.event.action == 'closed'"
	sameRepository = "github.event.pull_request.head.repo.full_name == github.repository"
	prNumber       = "${{ github.event.pull_request.number }}"
)

// renderGitHub writes the stack's workflow.
func renderGitHub(req registry.CIRequest) ([]registry.CIFile, error) {
	g := github{req: req, branch: req.Options.Branch}
	if g.branch == "" {
		g.branch = registry.DefaultCIBranch
	}
	data, err := g.render()
	if err != nil {
		return nil, err
	}
	return []registry.CIFile{{Path: req.Stack.Name + ".yml", Data: data}}, nil
}

type github struct {
	req    registry.CIRequest
	branch string
}

// job is one job of the workflow.
type job struct {
	id, name    string
	needs       string
	cond        string
	environment string
	// cloud jobs sign in, so they may ask GitHub for an identity token.
	cloud       bool
	concurrency string
	steps       []step
}

// step is one step of a job.
type step struct {
	name string
	cond string
	uses *action
	dir  string
	with [][2]string
	run  string
}

func (g github) render() ([]byte, error) {
	st := g.req.Stack
	var notes []string
	var plans, previews, deploys []job
	previous := "check"
	for _, env := range g.req.Environments {
		e := env.Environment
		switch {
		case e.Target == local.Target:
			notes = append(notes, fmt.Sprintf("%s is on the %s target: superschematic stack dev runs it.", e.Environment, local.Target))
			continue
		case len(e.Parameters) > 1:
			notes = append(notes, fmt.Sprintf("%s takes %d parameters (%s): CI deploys a member of an environment with one.", e.Environment, len(e.Parameters), strings.Join(e.Parameters, ", ")))
			continue
		case !env.HasSeam:
			notes = append(notes, fmt.Sprintf("%s is on the %s target, which gives a CI job no way to sign in.", e.Environment, e.Target))
			continue
		}
		roles := []registry.CIRole{registry.CIPlanner, registry.CIDeployer}
		if len(e.Parameters) == 1 {
			roles = roles[1:]
		}
		var missing []string
		for _, role := range roles {
			if env.Identity(role) == nil {
				missing = append(missing, string(role))
			}
		}
		if len(missing) > 0 {
			notes = append(notes, fmt.Sprintf("%s has no CI identity for %s yet: run superschematic stack bootstrap %s --stack %s, which records it, and build again.",
				e.Environment, strings.Join(missing, " or "), e.Environment, st.Dir))
			continue
		}
		if len(e.Parameters) == 1 {
			j, err := g.preview(env)
			if err != nil {
				return nil, err
			}
			previews = append(previews, j)
			continue
		}
		plan, err := g.plan(env)
		if err != nil {
			return nil, err
		}
		plans = append(plans, plan)
		deploy, err := g.deploy(env, previous)
		if err != nil {
			return nil, err
		}
		deploys = append(deploys, deploy)
		previous = deploy.id
	}
	jobs := append([]job{g.check()}, plans...)
	jobs = append(jobs, previews...)
	jobs = append(jobs, deploys...)
	seen := map[string]string{}
	for _, j := range jobs {
		if other, dup := seen[j.id]; dup {
			return nil, fmt.Errorf("jobs %q and %q both take the id %s; rename an environment", other, j.name, j.id)
		}
		seen[j.id] = j.name
	}

	w := &yamlWriter{}
	g.header(w, notes)
	w.line(0, "name: "+scalar(st.Name))
	w.blank()
	w.line(0, "on:")
	w.line(1, "pull_request:")
	w.line(2, "branches: ["+scalar(g.branch)+"]")
	w.line(2, "types: [opened, synchronize, reopened, closed]")
	w.line(1, "push:")
	w.line(2, "branches: ["+scalar(g.branch)+"]")
	w.line(1, "workflow_dispatch:")
	w.blank()
	w.line(0, "permissions:")
	w.line(1, "contents: read")
	w.blank()
	w.line(0, "jobs:")
	for i, j := range jobs {
		if i > 0 {
			w.blank()
		}
		j.write(w)
	}
	return w.bytes(), nil
}

// header writes the comment that opens the workflow.
func (g github) header(w *yamlWriter, notes []string) {
	st := g.req.Stack
	by := "superschematic " + g.req.Version
	if g.req.Version == "" {
		by = "superschematic (a checkout build, no release)"
	}
	w.line(0, fmt.Sprintf("# Code generated by %s from stack %s. DO NOT EDIT.", by, st.Name))
	w.line(0, "#")
	w.comment(0, fmt.Sprintf("The stack's service is %s. Change it and build it again with superschematic build, which writes this file.", st.Dir))
	w.line(0, "#")
	w.comment(0, fmt.Sprintf("On a pull request to %[1]s, check builds the stack and compiles its servers with no credentials; each plan job plans a cloud environment as its planner account, and each preview job deploys a member of an environment with one parameter for the pull request, and destroys it when the pull request closes. A pull request from a fork runs check alone. On a push to %[1]s, each deploy job deploys a cloud environment in the order the stack declares them, in the GitHub environment of its name, whose required reviewers approve it.", g.branch))
	if len(notes) > 0 {
		w.line(0, "#")
		w.line(0, "# Environments with no job:")
		for _, note := range notes {
			w.item(0, note)
		}
	}
	w.blank()
}

// check is the job that builds the stack and compiles its servers, with
// no credentials: levels 1 to 3 of section 10.
func (g github) check() job {
	st := g.req.Stack
	servers := g.servers()
	steps := []step{g.checkout(), g.install()}
	if len(servers) > 0 {
		steps = append(steps, step{
			name: "Set up Go",
			uses: &actionSetupGo,
			with: [][2]string{{"go-version", servergen.GoVersion}, {"cache", "false"}},
		})
	}
	steps = append(steps, g.packages()...)
	steps = append(steps, g.build())
	for _, server := range servers {
		steps = append(steps, step{
			name: "Compile server " + server,
			dir:  path.Join(st.OutputRoot, "server", st.Name, server),
			run:  "go build -mod=mod -o /dev/null .",
		})
	}
	return job{id: "check", name: "check", cond: notClosed, steps: steps}
}

// plan is the job that plans env on a pull request from the repository,
// as planner: levels 5 and 6.
func (g github) plan(env registry.CIEnvironment) (job, error) {
	e := env.Environment
	steps, err := g.cloudSetup(env, registry.CIPlanner)
	if err != nil {
		return job{}, err
	}
	steps = append(steps, step{name: "Plan " + e.Environment, run: g.stackCommand("plan", e.Environment)})
	return job{
		id:          "plan-" + jobID(e.Environment),
		name:        "plan " + e.Environment,
		needs:       "check",
		cond:        strings.Join([]string{onPullRequest, notClosed, sameRepository}, " && "),
		cloud:       true,
		concurrency: fmt.Sprintf("%s-plan-%s-%s", g.req.Stack.Name, e.Environment, prNumber),
		steps:       steps,
	}, nil
}

// preview is the job that deploys the member of env, an environment with
// one parameter, for a pull request from the repository, as deployer, and
// destroys it when the pull request closes: level 7.
func (g github) preview(env registry.CIEnvironment) (job, error) {
	e := env.Environment
	steps, err := g.cloudSetup(env, registry.CIDeployer)
	if err != nil {
		return job{}, err
	}
	param := "--param " + e.Parameters[0] + "=" + prNumber
	build := g.build()
	build.cond = notClosed
	steps = append(steps, build,
		step{name: "Deploy " + e.Environment + " for the pull request", cond: notClosed, run: g.stackCommand("deploy", e.Environment, param)},
		step{name: "Destroy " + e.Environment + " for the pull request", cond: closed, run: g.stackCommand("destroy", e.Environment, param, "--yes")},
	)
	return job{
		id:    "preview-" + jobID(e.Environment),
		name:  "preview " + e.Environment,
		needs: "check",
		// check does not run when the pull request closes, and the
		// destroy must run all the same.
		cond:        fmt.Sprintf("!cancelled() && %s && %s && (%s || needs.check.result == 'success')", onPullRequest, sameRepository, closed),
		cloud:       true,
		concurrency: fmt.Sprintf("%s-%s-%s", g.req.Stack.Name, e.Environment, prNumber),
		steps:       steps,
	}, nil
}

// deploy is the job that deploys env on a push to the branch, as deployer,
// after the job before it.
func (g github) deploy(env registry.CIEnvironment, after string) (job, error) {
	e := env.Environment
	steps, err := g.cloudSetup(env, registry.CIDeployer)
	if err != nil {
		return job{}, err
	}
	steps = append(steps, g.build(), step{name: "Deploy " + e.Environment, run: g.stackCommand("deploy", e.Environment)})
	return job{
		id:          "deploy-" + jobID(e.Environment),
		name:        "deploy " + e.Environment,
		needs:       after,
		cond:        fmt.Sprintf("github.event_name != 'pull_request' && github.ref == 'refs/heads/%s'", g.branch),
		environment: e.Environment,
		cloud:       true,
		concurrency: g.req.Stack.Name + "-" + e.Environment,
		steps:       steps,
	}, nil
}

// cloudSetup is the steps a cloud job takes before its command: the
// checkout, superschematic, the schemas root's packages, the provisioner's
// tools, and the sign-in as role.
func (g github) cloudSetup(env registry.CIEnvironment, role registry.CIRole) ([]step, error) {
	steps := []step{g.checkout(), g.install()}
	steps = append(steps, g.packages()...)
	for _, tool := range env.Tools {
		s, err := toolStep(tool)
		if err != nil {
			return nil, fmt.Errorf("environment %s: %w", env.Environment.Environment, err)
		}
		steps = append(steps, s)
	}
	s, err := signIn(env.Identity(role), role)
	if err != nil {
		return nil, fmt.Errorf("environment %s: %w", env.Environment.Environment, err)
	}
	return append(steps, s), nil
}

func (g github) checkout() step {
	return step{name: "Check out", uses: &actionCheckout}
}

// install downloads the release of superschematic that wrote the workflow
// from the release page of its repository, and checks it against the
// release's SHA256SUMS. A checkout build is no release, so its install
// step fails.
func (g github) install() step {
	v := g.req.Version
	if v == "" {
		return step{
			name: "Install superschematic",
			run:  `echo "A superschematic built from a checkout generated this workflow, and it names no release to install; generate this workflow again with a released superschematic" && exit 1`,
		}
	}
	dist := fmt.Sprintf("superschematic_%s_linux-x64", v)
	return step{
		name: "Install superschematic " + v,
		run: strings.Join([]string{
			"set -euo pipefail",
			`dir="$RUNNER_TEMP/superschematic"`,
			`mkdir -p "$dir"`,
			fmt.Sprintf("release=https://%s/releases/download/v%s", rootModule, v),
			fmt.Sprintf(`curl -fsSL -o "$dir/%[1]s.tar.gz" "$release/%[1]s.tar.gz"`, dist),
			`curl -fsSL -o "$dir/SHA256SUMS" "$release/SHA256SUMS"`,
			`(cd "$dir" && sha256sum --check --ignore-missing SHA256SUMS)`,
			fmt.Sprintf(`tar -xzf "$dir/%s.tar.gz" -C "$dir"`, dist),
			fmt.Sprintf(`echo "$dir/%s" >> "$GITHUB_PATH"`, dist),
		}, "\n"),
	}
}

// packages sets up the schemas root's package manager and installs its
// packages, as its lockfile says.
func (g github) packages() []step {
	root := g.req.Stack.SchemasRoot
	switch g.req.Stack.PackageManager {
	case registry.PackageManagerBun:
		return []step{
			{name: "Set up Bun", uses: &actionSetupBun},
			{name: "Install the schemas root's packages", dir: root, run: "bun install --frozen-lockfile"},
		}
	case registry.PackageManagerNPM:
		return []step{
			{name: "Set up Node.js", uses: &actionSetupNode, with: [][2]string{{"node-version", "lts/*"}}},
			{name: "Install the schemas root's packages", dir: root, run: "npm ci"},
		}
	}
	return nil
}

// build builds every service of the services root, the stack's included,
// to the default output root: it resolves every environment and checks its
// graph, and writes each server's entrypoint and Dockerfile, which a
// deploy builds the server's image from.
func (g github) build() step {
	return step{name: "Build the services", run: "superschematic build-all " + g.req.Stack.ServicesRoot}
}

// stackCommand is one of the stack commands on the stack, which the job
// names since it runs from the repository root.
func (g github) stackCommand(command, environment string, flags ...string) string {
	args := append([]string{"superschematic", "stack", command, environment, "--stack", g.req.Stack.Dir}, flags...)
	return strings.Join(args, " ")
}

// servers are the stack's Go servers, whose entrypoint modules the build
// writes, sorted. No environment changes the servers.
func (g github) servers() []string {
	var names []string
	for _, env := range g.req.Environments {
		for _, d := range env.Environment.Deployables {
			if d.Kind == ir.DeployableServer && d.Language == registry.APILanguageGo && !slices.Contains(names, d.Name) {
				names = append(names, d.Name)
			}
		}
	}
	slices.Sort(names)
	return names
}

// toolStep installs a provisioner's tool.
func toolStep(tool registry.CLITool) (step, error) {
	switch tool.Name {
	case pulumiTool:
		return step{name: "Install the pulumi CLI", uses: &actionPulumi, with: [][2]string{{"pulumi-version", tool.Version}}}, nil
	}
	return step{}, fmt.Errorf("the %s CI renderer has no step that installs tool %s", GitHubRenderer, tool.Name)
}

// signIn is the step that signs in as role with identity.
func signIn(identity *registry.CIIdentity, role registry.CIRole) (step, error) {
	switch identity.Kind {
	case gcpWorkloadIdentity:
		provider, account := identity.Fields["provider"], identity.Fields["account"]
		if provider == "" || account == "" {
			return step{}, fmt.Errorf("a %s identity needs a provider and an account", identity.Kind)
		}
		return step{
			name: "Sign in to Google Cloud as " + string(role),
			uses: &actionGoogleAuth,
			with: [][2]string{{"workload_identity_provider", provider}, {"service_account", account}},
		}, nil
	}
	return step{}, fmt.Errorf("the %s CI renderer cannot sign in with a CI identity of kind %q", GitHubRenderer, identity.Kind)
}

// jobID is an environment's name as a job id: lower case, words joined by
// hyphens, and nothing a job id may not hold.
func jobID(name string) string {
	var b strings.Builder
	for i, r := range name {
		switch {
		case r >= 'A' && r <= 'Z':
			if i > 0 && b.Len() > 0 && !strings.HasSuffix(b.String(), "-") {
				prev := name[i-1]
				if prev >= 'a' && prev <= 'z' || prev >= '0' && prev <= '9' {
					b.WriteByte('-')
				}
			}
			b.WriteRune(r - 'A' + 'a')
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_':
			b.WriteRune(r)
		}
	}
	return b.String()
}

func (j job) write(w *yamlWriter) {
	w.line(1, j.id+":")
	w.line(2, "name: "+scalar(j.name))
	if j.needs != "" {
		w.line(2, "needs: "+j.needs)
	}
	if j.cond != "" {
		w.line(2, "if: "+scalar(j.cond))
	}
	w.line(2, "runs-on: ubuntu-latest")
	if j.environment != "" {
		w.line(2, "environment: "+scalar(j.environment))
	}
	if j.cloud {
		w.line(2, "permissions:")
		w.line(3, "contents: read")
		w.line(3, "id-token: write")
	}
	if j.concurrency != "" {
		w.line(2, "concurrency:")
		w.line(3, "group: "+scalar(j.concurrency))
		w.line(3, "cancel-in-progress: false")
	}
	w.line(2, "steps:")
	for _, s := range j.steps {
		s.write(w)
	}
}

func (s step) write(w *yamlWriter) {
	w.line(3, "- name: "+scalar(s.name))
	if s.cond != "" {
		w.line(4, "if: "+scalar(s.cond))
	}
	if s.uses != nil {
		w.line(4, "uses: "+s.uses.String())
	}
	if s.dir != "" {
		w.line(4, "working-directory: "+scalar(s.dir))
	}
	if len(s.with) > 0 {
		w.line(4, "with:")
		for _, kv := range s.with {
			w.line(5, kv[0]+": "+scalar(kv[1]))
		}
	}
	switch {
	case strings.Contains(s.run, "\n"):
		w.line(4, "run: |")
		for _, l := range strings.Split(s.run, "\n") {
			w.line(5, l)
		}
	case s.run != "":
		w.line(4, "run: "+scalar(s.run))
	}
}

// yamlWriter writes the workflow line by line, two spaces an indent.
type yamlWriter struct{ b bytes.Buffer }

func (w *yamlWriter) line(indent int, s string) {
	w.b.WriteString(strings.Repeat("  ", indent))
	w.b.WriteString(s)
	w.b.WriteByte('\n')
}

func (w *yamlWriter) blank() { w.b.WriteByte('\n') }

func (w *yamlWriter) bytes() []byte { return w.b.Bytes() }

// commentWidth is the column a comment wraps at.
const commentWidth = 76

// comment writes text as comment lines.
func (w *yamlWriter) comment(indent int, text string) {
	for _, l := range wrap(text, commentWidth-2*indent-2) {
		w.line(indent, "# "+l)
	}
}

// item writes text as a comment list item, its later lines indented under
// its first.
func (w *yamlWriter) item(indent int, text string) {
	for i, l := range wrap(text, commentWidth-2*indent-4) {
		prefix := "# - "
		if i > 0 {
			prefix = "#   "
		}
		w.line(indent, prefix+l)
	}
}

// wrap breaks text into lines of at most width runes at spaces; a word
// longer than width gets a line of its own.
func wrap(text string, width int) []string {
	var lines []string
	var cur string
	for _, word := range strings.Fields(text) {
		switch {
		case cur == "":
			cur = word
		case len(cur)+1+len(word) <= width:
			cur += " " + word
		default:
			lines = append(lines, cur)
			cur = word
		}
	}
	if cur != "" {
		lines = append(lines, cur)
	}
	return lines
}

// plainUnsafe matches what a plain YAML scalar may not hold: a leading
// indicator, a leading or trailing space, ": " or " #", a trailing colon,
// or a control character.
var plainUnsafe = regexp.MustCompile("^[-?:,\\[\\]{}#&*!|>'\"%@` 0-9.+]|[ :]$|: | #|[\\x00-\\x1f]")

// yamlKeywords are the plain scalars YAML reads as something other than a
// string.
var yamlKeywords = []string{"true", "false", "yes", "no", "on", "off", "y", "n", "null", "~"}

// scalar writes s as a plain YAML scalar when one reads back as s, else
// double-quoted. A scalar that begins with a digit is quoted, so a version
// stays a string.
func scalar(s string) string {
	if s != "" && !plainUnsafe.MatchString(s) && !slices.Contains(yamlKeywords, strings.ToLower(s)) {
		return s
	}
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(s)
	return strings.TrimSuffix(b.String(), "\n")
}
