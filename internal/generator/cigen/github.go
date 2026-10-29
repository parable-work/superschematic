package cigen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"path"
	"regexp"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/release"
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
	actionUpload     = action{"actions/upload-artifact", "043fb46d1a93c77aae656e7c1c64a875d1fc6a0a", "v7.0.1"}
	actionDownload   = action{"actions/download-artifact", "3e5f45b2cfb9172054b4087a40e8e0b5a5461e7c", "v8.0.1"}
)

// preparedArtifact names the artifact the prepare job uploads and every
// cloud job downloads: the directory `stack prepare` wrote (D47,
// amended), in the runner's temporary directory, preparedDir.
const (
	preparedArtifact = "prepared"
	preparedDir      = "${{ runner.temp }}/prepared"
	preparedFlag     = `--prepared "$RUNNER_TEMP/prepared"`
)

// gcpWorkloadIdentity is the identity kind of gcp's CI seam: Workload
// Identity Federation through the provider bootstrap creates, as the
// role's service account. google-github-actions/auth signs in with it and
// writes a credentials file, which gives superschematic and Pulumi
// application default credentials, and exports its path and the project
// to every later step of the job: so the job signs in after its installs
// and builds, which run the pull request's lockfile and scripts, and
// right before its stack commands (cloudSetup). It runs no project code:
// the prepare job ran it, with no identity token (D47, amended).
const gcpWorkloadIdentity = "gcp-workload-identity"

// cloudflareAPIToken is the identity kind of the cloudflare target's CI
// seam (D64): a long-lived account token and the state's passphrase, each
// a secret of the workflow, whose names its fields `token` and
// `passphrase` give. No action signs in with it: the env of each step that
// runs a stack command reads both secrets, CLOUDFLARE_API_TOKEN for the
// provider and the state store, and PULUMI_CONFIG_PASSPHRASE for the
// state's secrets, and no other step sees them: not the install of a
// pull request's lockfile, whose packages' scripts run with the step's
// environment.
const cloudflareAPIToken = "cloudflare-api-token"

// secretName is a GitHub Actions secret's name: letters, digits and
// underscores, not beginning with a digit.
var secretName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// pulumiTool is the pulumi provisioner's CLI, which pulumi/actions
// installs.
const pulumiTool = "pulumi"

// runnerPlatform is the platform of the runner every job runs on,
// ubuntu-latest, by the name the release's assets carry.
const runnerPlatform = "linux-x64"

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
	var err error
	if g.cli, g.why, err = cliOf(req); err != nil {
		return nil, err
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
	// cli is the superschematic binary every job installs and runs, and
	// why is the header's sentence that says why, when it is not
	// superschematic (cliOf).
	cli, why string
}

// coreCLI is the binary a release ships: the core with the official
// extensions linked.
const coreCLI = release.CLI

// cliOf is the binary the workflow installs and runs, and why when it is
// not superschematic: superschematic, unless an app needs an extension its
// build needs (D78), which the released superschematic does not link, and
// whose build superschematic refuses. The apps are the stack's and those of
// the services root's other stacks (RootApps): every job builds the whole
// root. The binary that links an extension e is superschematic-e, as the
// refusal says: the official extensions superschematic links, with e
// beside them, so it builds every service the released binary does. The
// release ships one for each of release.ExtensionCLIs, and the workflow
// installs it in every job as it installs superschematic. An extension the
// release ships no binary of, and apps that need two extensions between
// them, which no one binary links, are refused: the workflow's build
// would refuse the root on every run.
func cliOf(req registry.CIRequest) (string, string, error) {
	needs := map[string][]string{}
	need := func(app string, exts []string) {
		for _, ext := range exts {
			if !slices.Contains(needs[ext], app) {
				needs[ext] = append(needs[ext], app)
			}
		}
	}
	for _, env := range req.Environments {
		for _, d := range env.Environment.Deployables {
			if d.Kind == ir.DeployableApp && d.App != nil {
				need(fmt.Sprintf("app %s of %s", d.App.Name, d.App.API), d.App.Extensions)
			}
		}
	}
	for _, a := range req.RootApps {
		need(fmt.Sprintf("app %s of %s in stack %s", a.App.Name, a.App.API, a.Stack), a.App.Extensions)
	}
	switch exts := slices.Sorted(maps.Keys(needs)); len(exts) {
	case 0:
		return coreCLI, "", nil
	case 1:
		if !slices.Contains(release.ExtensionCLIs, exts[0]) {
			return "", "", fmt.Errorf("stack %s: %s needs the %s extension, and superschematic's release ships no binary that links it, so the workflow's build would refuse the services root; build the stack's CI yourself with a binary that links it", req.Stack.Name, strings.Join(needs[exts[0]], " and "), exts[0])
		}
		cli := release.ExtensionCLI(exts[0])
		return cli, fmt.Sprintf("Every job runs %s, the release's superschematic with the %s extension linked too: its build of the services root builds %s, which needs it.", cli, exts[0], strings.Join(needs[exts[0]], " and ")), nil
	default:
		var named []string
		for _, ext := range exts {
			named = append(named, fmt.Sprintf("%s needs %s", strings.Join(needs[ext], " and "), ext))
		}
		return "", "", fmt.Errorf("stack %s: %s; one binary builds the services root in CI, and no superschematic-<extension> links more than one extension: give the apps one extension between them", req.Stack.Name, strings.Join(named, ", "))
	}
}

// job is one job of the workflow.
type job struct {
	id, name    string
	needs       string
	cond        string
	environment string
	// cloud jobs sign in with an identity token they ask GitHub for.
	cloud       bool
	concurrency string
	steps       []step
}

// signedIn is how a cloud job signs in: a step, which may ask GitHub for
// an identity token, or environment variables that each step running a
// stack command reads from the workflow's secrets (D64).
type signedIn struct {
	step    *step
	idToken bool
	env     [][2]string
}

// step is one step of a job.
type step struct {
	name string
	cond string
	uses *action
	dir  string
	with [][2]string
	// env is the step's environment variables, in order: what a stack
	// command signs in with when the identity is a secret (D64).
	env [][2]string
	run string
}

func (g github) render() ([]byte, error) {
	st := g.req.Stack
	var notes []string
	var plans, previews, deploys []job
	var prepared []string
	previous := "check"
	for _, env := range g.req.Environments {
		e := env.Environment
		switch {
		case e.Target == local.Target:
			notes = append(notes, fmt.Sprintf("%s is on the %s target: %s stack dev runs it.", e.Environment, local.Target, g.cli))
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
			notes = append(notes, fmt.Sprintf("%s has no CI identity for %s yet: run %s stack bootstrap %s --stack %s, which records it, and build again.",
				e.Environment, strings.Join(missing, " or "), g.cli, e.Environment, st.Dir))
			continue
		}
		prepared = append(prepared, e.Environment)
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
	for _, env := range g.req.Environments {
		for _, d := range env.Environment.Deployables {
			if _, ok := st.Sites[d.Name]; d.Kind == ir.DeployableSite && !ok {
				return nil, fmt.Errorf("environment %s deploys site %s, whose package the stack does not locate", env.Environment.Environment, d.Name)
			}
		}
	}
	jobs := []job{g.check()}
	if len(prepared) > 0 {
		jobs = append(jobs, g.prepare(prepared, len(previews) > 0))
	}
	jobs = append(jobs, plans...)
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
	w.comment(0, fmt.Sprintf("The stack's service is %s. Change it and build it again with %s build, which writes this file.", st.Dir, g.cli))
	w.line(0, "#")
	if g.why != "" {
		w.comment(0, g.why)
		w.line(0, "#")
	}
	w.comment(0, fmt.Sprintf("On a pull request to %[1]s, check builds the stack and compiles its servers, jobs, workers and engine runners with no credentials; each plan job plans a cloud environment as its planner account, and each preview job deploys a member of an environment with one parameter for the pull request, and destroys it when the pull request closes. A pull request from a fork runs check alone. On a push to %[1]s, each deploy job deploys a cloud environment in the order the stack declares them, in the GitHub environment of its name, whose required reviewers approve it.", g.branch))
	w.line(0, "#")
	w.comment(0, "The prepare job runs, with no credentials, all the project code the cloud jobs need: the build, the installs, the sites' and bundles' builds and the sidecar documents (superschematic stack prepare). A cloud job takes what it made: it runs no build, no package's script and no sidecar document, and its only package manager run, the schemas root's install with --ignore-scripts, which the loader reads the types of, is on a Bun or Node.js downloaded from its release, never restored from a cache another job could write.")
	if len(notes) > 0 {
		w.line(0, "#")
		w.line(0, "# Environments with no job:")
		for _, note := range notes {
			w.item(0, note)
		}
	}
	w.blank()
}

// check is the job that builds the stack and compiles its servers, jobs,
// workers and engine runners, with no credentials: levels 1 to 3 of
// section 10. Each Go server, job, worker and runner's module compiles,
// against the release's static archives, or the project's engine archive,
// which it builds first, for one whose engine database composes a behavior
// the core's archive leaves out (D58); for the TypeScript ones and the
// sites it
// installs the output root's Bun workspace from the lockfile the project
// commits, which fails when the lockfile is missing or does not match the
// packages the build wrote, type-checks each TypeScript server's, job's,
// worker's and runner's package and the implementation each imports (D51,
// amended; D52, D53), bundles each one some environment places on Workers
// with its bundle.ts --check, as `stack deploy` bundles it (D64), and
// builds each site with its build script, as `stack deploy` does, so a
// site that does not build fails the pull request (D55). For the Rust
// ones it
// sets up the Rust release tools.env pins before the build, which brings
// the output root's Cargo lockfile up to date with it, refuses a lockfile
// git does not track or the build changed, and checks each one's crate,
// with the implementation it depends on, frozen to the lockfile (D57,
// D65).
func (g github) check() job {
	st := g.req.Stack
	goModules := g.deployables(registry.APILanguageGo)
	tsPackages := g.deployables(registry.APILanguageTypeScript)
	rustCrates := g.deployables(registry.APILanguageRust)
	sites := slices.Sorted(maps.Keys(st.Sites))
	workspace := len(tsPackages) > 0 || len(sites) > 0
	steps := append([]step{g.checkout()}, g.install())
	if len(goModules) > 0 && g.req.Version != "" {
		steps = append(steps, g.archives())
	}
	if len(goModules) > 0 {
		steps = append(steps, step{
			name: "Set up Go",
			uses: &actionSetupGo,
			with: [][2]string{{"go-version", servergen.GoVersion}, {"cache", "false"}},
		})
	}
	engines := g.goEngines(goModules)
	if len(rustCrates) > 0 || len(engines) > 0 {
		steps = append(steps, setupRust())
	}
	packages := g.packages()
	if workspace && st.PackageManager != registry.PackageManagerBun {
		packages = append([]step{setupBun()}, packages...)
	}
	steps = append(steps, packages...)
	archives := slices.DeleteFunc(slices.Sorted(maps.Keys(st.EngineArchives)), func(db string) bool {
		return !slices.Contains(slices.Collect(maps.Values(engines)), db)
	})
	engineTypeScript := st.EngineCheckout != "" && st.EngineCheckoutTypeScript && len(tsPackages) > 0
	if st.EngineCheckout != "" && (len(archives) > 0 || engineTypeScript) {
		steps = append(steps, engineSuperscalar(st.EngineCheckout))
	}
	steps = append(steps, g.build())
	for _, db := range archives {
		steps = append(steps, engineArchive(db, st.EngineArchives[db], st.EngineCheckout))
	}
	for _, m := range goModules {
		compile := goBuild
		if db, ok := engines[m.name]; ok {
			// The project's engine archive, searched before the release's,
			// which holds the core's under the same name (D58).
			compile = fmt.Sprintf(`CGO_LDFLAGS="-L%s $CGO_LDFLAGS" %s`, engineArchiveDir(db), compile)
		}
		steps = append(steps, step{
			name: "Compile " + string(m.kind) + " " + m.name,
			dir:  path.Join(st.OutputRoot, "server", st.Name, m.name),
			run:  compile,
		})
	}
	if engineTypeScript {
		steps = append(steps, engineTypeScriptPackages(st.EngineCheckout))
	}
	if workspace {
		steps = append(steps, g.typeScriptWorkspace())
	}
	if len(tsPackages) > 0 {
		for _, p := range tsPackages {
			steps = append(steps, step{
				name: "Type-check " + string(p.kind) + " " + p.name,
				dir:  path.Join(st.OutputRoot, "server", st.Name, p.name),
				run:  typeCheck,
			})
		}
		for _, p := range g.bundled() {
			steps = append(steps, step{
				name: "Bundle " + string(p.kind) + " " + p.name + " for Workers",
				dir:  path.Join(st.OutputRoot, "server", st.Name, p.name),
				run:  bundleCheck,
			})
		}
		for _, service := range slices.Sorted(maps.Keys(st.TypeScriptImplementations)) {
			steps = append(steps, step{
				name: "Type-check the implementation of " + service,
				dir:  st.TypeScriptImplementations[service],
				run:  typeCheck,
			})
		}
	}
	if len(rustCrates) > 0 {
		steps = append(steps, g.rustLockfile())
		for _, c := range rustCrates {
			steps = append(steps, step{
				name: "Compile " + string(c.kind) + " " + c.name,
				dir:  path.Join(st.OutputRoot, "server", st.Name, c.name),
				run:  rustCheck,
			})
		}
	}
	for _, site := range sites {
		steps = append(steps, step{
			name: "Build site " + site,
			dir:  st.Sites[site].Dir,
			run:  "bun run " + st.Sites[site].Build,
		})
	}
	return job{id: "check", name: "check", cond: notClosed, steps: steps}
}

// bundleCheck bundles a server, job or worker some environment places on a
// platform whose artifact is a bundle, Cloudflare Workers, as a deploy
// does, into a temporary directory, and fails on what a Worker cannot run:
// a node: import, Bun's global, or a WebAssembly module other than
// superscalar's (docs/stack-model.md, section 18.5, D64).
const bundleCheck = "bun bundle.ts --check"

// goEngines are the Go servers, jobs, workers and engine runners of the
// stack (modules) whose engine database's deployables link a project's
// engine archive (registry.CIStack.EngineArchives), each with its
// database: a deployable's sql edge to it says it connects to it.
func (g github) goEngines(modules []deployable) map[string]string {
	out := map[string]string{}
	if len(g.req.Stack.EngineArchives) == 0 {
		return out
	}
	links := func(name string) bool {
		return slices.ContainsFunc(modules, func(m deployable) bool { return m.name == name })
	}
	for _, env := range g.req.Environments {
		for _, e := range env.Environment.Edges {
			if _, ok := g.req.Stack.EngineArchives[e.Service.Name]; ok && e.Kind == ir.EdgeSQL && links(e.From) {
				out[e.From] = e.Service.Name
			}
		}
	}
	return out
}

// engineArchiveDir is where a check job stages the project's engine
// archive of the engine database db.
func engineArchiveDir(db string) string {
	return "$RUNNER_TEMP/superschematic-engine/" + db
}

// engineArchive builds the project's engine archive of the engine database
// db from the ffi member of its engine crate at crate, which the build
// wrote, and stages it as libsuperschematic_engine.a, the name the engine's
// Go binding links (D58): the static library cargo reports it built for
// the crate, on the Rust release tools.env pins, which built the release's
// other archives. With the engine's checkout at checkout, a crate with no
// lock file of its own starts from the engine workspace's, as the image's
// engine stage does.
func engineArchive(db, crate, checkout string) step {
	lines := []string{
		"set -euo pipefail",
		fmt.Sprintf(`dir="%s"`, engineArchiveDir(db)),
		`mkdir -p "$dir"`,
	}
	if checkout != "" {
		lines = append(lines, fmt.Sprintf(`test -f ../Cargo.lock || cp "$GITHUB_WORKSPACE/%s/runtime/engine/rust/Cargo.lock" ../Cargo.lock`, checkout))
	}
	return step{
		name: "Build the engine archive of " + db,
		dir:  crate,
		run: strings.Join(append(lines,
			`archive="$(cargo build --release --message-format=json-render-diagnostics | jq -r 'select(.reason == "compiler-artifact" and (.target.kind | index("staticlib"))) | .filenames[] | select(endswith(".a"))' | tail -n 1)"`,
			`test -n "$archive"`,
			`cp "$archive" "$dir/libsuperschematic_engine.a"`,
		), "\n"),
	}
}

// engineSuperscalar stands up the superscalar checkout under the engine's
// checkout at checkout, which its crates reach by path and git does not
// hold, from the commit its superscalar.pin names (D58).
func engineSuperscalar(checkout string) step {
	return step{
		name: "Check out superscalar for the engine's checkout " + checkout,
		dir:  checkout,
		run:  "scripts/superscalar-dep.sh --checkout",
	}
}

// engineTypeScriptPackages builds the dist/ of the packages of the engine's
// checkout at checkout that the output root's workspace takes in place of
// the published ones, which no checkout holds: superscalar's TypeScript
// binding, the HTTP runtime's, and the engine's, which copies the HTTP
// runtime's build (D58). The workspace's install, after it, takes them.
func engineTypeScriptPackages(checkout string) step {
	return step{
		name: "Build the TypeScript packages of the engine's checkout " + checkout,
		dir:  checkout,
		run: strings.Join([]string{
			"set -euo pipefail",
			"(cd third_party/superscalar/bindings/typescript && bun install --frozen-lockfile && bun x --no-install tsc -p tsconfig.json && bun x --no-install tsc -p tsconfig.esm.json && bun scripts/fix-esm-extensions.mjs)",
			"(cd runtime/http/typescript && bun install --frozen-lockfile && bun run build)",
			"(cd runtime/engine/typescript && bun install --frozen-lockfile && bun run build)",
		}, "\n"),
	}
}

// goBuild compiles the entrypoint module of a Go server, job, worker or
// engine runner, with the implementation it imports, and writes no
// binary: the deploy builds the image.
const goBuild = "go build -mod=mod -o /dev/null ."

// rustCheck compiles the crate of a Rust server, job, worker or runner and every
// crate it depends on, the implementations among them, frozen to the
// workspace's lockfile.
const rustCheck = "cargo check --locked"

// setupRust installs the Rust release tools.env pins,
// servergen.RustVersion, the one a Rust server's image builds with, as the
// default toolchain: the build's cargo update --workspace and the
// compiles run on it (D57). The runner's rustup installs it.
func setupRust() step {
	v := servergen.RustVersion
	return step{name: "Set up Rust", run: fmt.Sprintf("rustup toolchain install %s --profile minimal\nrustup default %s", v, v)}
}

// rustLockfile checks the output root's Cargo lockfile, which the project
// commits (D57). The build brings it up to date with cargo update
// --workspace, so a lockfile the build changed was stale, and one git does
// not track is missing; the compiles after it build frozen to it.
func (g github) rustLockfile() step {
	out := g.req.Stack.OutputRoot
	lock := path.Join(out, servergen.RustLockfileName)
	fix := fmt.Sprintf("build every service (%s build-all), which brings %s up to date, and commit it", g.cli, lock)
	return step{
		name: "Check the Rust workspace's lockfile " + lock,
		dir:  out,
		run: strings.Join([]string{
			"if ! git ls-files --error-unmatch Cargo.lock >/dev/null 2>&1; then",
			fmt.Sprintf(`  echo "::error file=%s::%s is not committed; %s" && exit 1`, lock, lock, fix),
			"fi",
			"git diff --quiet -- Cargo.lock || {",
			fmt.Sprintf(`  echo "::error file=%s::%s does not match the crates the build wrote; %s" && git --no-pager diff -- Cargo.lock && exit 1`, lock, lock, fix),
			"}",
		}, "\n"),
	}
}

// typeCheck type-checks a TypeScript package with the compiler its
// manifest depends on, which the workspace's install linked into it, and
// never with one bunx would download.
const typeCheck = "bun x --no-install tsc --noEmit -p tsconfig.json"

// typeScriptWorkspace installs the output root's Bun workspace from its
// lockfile, which the project commits (D51, amended). A frozen install
// fails on a lockfile that does not match the workspace's manifests, but
// installs without one, so the step refuses a missing lockfile itself.
func (g github) typeScriptWorkspace() step {
	out := g.req.Stack.OutputRoot
	lock := path.Join(out, "bun.lock")
	fix := fmt.Sprintf("build every service, run bun install in %s and commit %s", out, lock)
	return step{
		name: "Install the TypeScript workspace from " + lock,
		dir:  out,
		run: strings.Join([]string{
			"if [ ! -f bun.lock ]; then",
			fmt.Sprintf(`  echo "::error file=%s::%s is not committed; %s" && exit 1`, lock, lock, fix),
			"fi",
			"bun install --frozen-lockfile || {",
			fmt.Sprintf(`  echo "::error file=%s::%s does not match the packages the build wrote; %s" && exit 1`, lock, lock, fix),
			"}",
		}, "\n"),
	}
}

// plan is the job that plans env on a pull request from the repository,
// as planner: levels 5 and 6.
func (g github) plan(env registry.CIEnvironment) (job, error) {
	e := env.Environment
	steps, in, err := g.cloudSetup(env, registry.CIPlanner)
	if err != nil {
		return job{}, err
	}
	steps = append(steps, step{name: "Plan " + e.Environment, env: in.env, run: g.stackCommand("plan", e.Environment)})
	return job{
		id:          "plan-" + jobID(e.Environment),
		name:        "plan " + e.Environment,
		needs:       "[check, prepare]",
		cond:        strings.Join([]string{onPullRequest, notClosed, sameRepository}, " && "),
		cloud:       in.idToken,
		concurrency: fmt.Sprintf("%s-plan-%s-%s", g.req.Stack.Name, e.Environment, prNumber),
		steps:       steps,
	}, nil
}

// preview is the job that deploys the member of env, an environment with
// one parameter, for a pull request from the repository, as deployer, and
// destroys it when the pull request closes: level 7.
func (g github) preview(env registry.CIEnvironment) (job, error) {
	e := env.Environment
	steps, in, err := g.cloudSetup(env, registry.CIDeployer)
	if err != nil {
		return job{}, err
	}
	param := "--param " + e.Parameters[0] + "=" + prNumber
	steps = append(steps,
		step{name: "Deploy " + e.Environment + " for the pull request", cond: notClosed, env: in.env, run: g.stackCommand("deploy", e.Environment, param)},
		step{name: "Destroy " + e.Environment + " for the pull request", cond: closed, env: in.env, run: g.stackCommand("destroy", e.Environment, param, "--yes")},
	)
	return job{
		id:    "preview-" + jobID(e.Environment),
		name:  "preview " + e.Environment,
		needs: "[check, prepare]",
		// check does not run when the pull request closes, and the
		// destroy must run all the same, with the documents prepare
		// evaluated.
		cond:        fmt.Sprintf("!cancelled() && %s && %s && needs.prepare.result == 'success' && (%s || needs.check.result == 'success')", onPullRequest, sameRepository, closed),
		cloud:       in.idToken,
		concurrency: fmt.Sprintf("%s-%s-%s", g.req.Stack.Name, e.Environment, prNumber),
		steps:       steps,
	}, nil
}

// deploy is the job that deploys env on a push to the branch, as deployer,
// after the job before it.
func (g github) deploy(env registry.CIEnvironment, after string) (job, error) {
	e := env.Environment
	steps, in, err := g.cloudSetup(env, registry.CIDeployer)
	if err != nil {
		return job{}, err
	}
	steps = append(steps, step{name: "Deploy " + e.Environment, env: in.env, run: g.stackCommand("deploy", e.Environment)})
	return job{
		id:          "deploy-" + jobID(e.Environment),
		name:        "deploy " + e.Environment,
		needs:       "[" + after + ", prepare]",
		cond:        fmt.Sprintf("github.event_name != 'pull_request' && github.ref == 'refs/heads/%s'", g.branch),
		environment: e.Environment,
		cloud:       in.idToken,
		concurrency: g.req.Stack.Name + "-" + e.Environment,
		steps:       steps,
	}, nil
}

// cloudSetup is the steps a cloud job takes before its commands: the
// checkout, superschematic, the schemas root's packages, installed with
// none of their scripts, which the loader reads the types of, the
// provisioner's tools, the directory the prepare job wrote, and last the
// sign-in as role: a step, or the env the steps that run the stack's
// commands take. A cloud job runs no project code (D47, amended): the
// prepare job built, installed and evaluated it all with no identity
// token, which a job's `id-token: write` would give every one of its
// steps, and the stack commands take it with --prepared, starting no Bun,
// install or build. Only the stack commands read the credentials the
// sign-in leaves.
func (g github) cloudSetup(env registry.CIEnvironment, role registry.CIRole) ([]step, signedIn, error) {
	steps := []step{g.checkout(), g.install()}
	if role == registry.CIDeployer && env.Migrate {
		// The target migrates from the deploying machine (D64).
		steps = append(steps, g.installMigrate())
	}
	steps = append(steps, g.scriptlessPackages()...)
	for _, tool := range env.Tools {
		s, err := toolStep(tool)
		if err != nil {
			return nil, signedIn{}, fmt.Errorf("environment %s: %w", env.Environment.Environment, err)
		}
		steps = append(steps, s)
	}
	steps = append(steps, step{
		name: "Download what prepare made",
		uses: &actionDownload,
		with: [][2]string{{"name", preparedArtifact}, {"path", preparedDir}},
	})
	in, err := signIn(env.Identity(role), role)
	if err != nil {
		return nil, signedIn{}, fmt.Errorf("environment %s: %w", env.Environment.Environment, err)
	}
	if in.step != nil {
		steps = append(steps, *in.step)
	}
	return steps, in, nil
}

func (g github) checkout() step {
	return step{name: "Check out", uses: &actionCheckout}
}

// install downloads the release's tarball of the binary every job runs,
// superschematic or the superschematic-<extension> an app needs (cliOf),
// for the runner's platform, from the release page of superschematic's
// repository, checks it against the release's SHA256SUMS, unpacks it, puts
// the binary on PATH, and checks that it runs and reports the release. The
// release of the binary that wrote the workflow is the one it installs,
// whichever of the two wrote it: both are built from one commit. A
// checkout build is no release, so its install step fails, and says to
// render the workflow again with a released binary.
func (g github) install() step {
	v := g.req.Version
	if v == "" {
		return step{
			name: "Install " + g.cli,
			run:  fmt.Sprintf(`echo "A superschematic built from a checkout generated this workflow, and it names no release to install; generate this workflow again with a released %s" && exit 1`, g.cli),
		}
	}
	dist := release.CLIName(g.cli, v, runnerPlatform)
	return step{
		name: fmt.Sprintf("Install %s %s", g.cli, v),
		run: strings.Join([]string{
			"set -euo pipefail",
			`dir="$RUNNER_TEMP/superschematic"`,
			`mkdir -p "$dir"`,
			"release=" + strings.TrimSuffix(release.DownloadURL(v, ""), "/"),
			fmt.Sprintf(`curl -fsSL -o "$dir/%[1]s.tar.gz" "$release/%[1]s.tar.gz"`, dist),
			`curl -fsSL -o "$dir/SHA256SUMS" "$release/SHA256SUMS"`,
			`(cd "$dir" && sha256sum --check --ignore-missing SHA256SUMS)`,
			fmt.Sprintf(`tar -xzf "$dir/%s.tar.gz" -C "$dir"`, dist),
			fmt.Sprintf(`got="$("$dir/%s/%s" --version)"`, dist, g.cli),
			fmt.Sprintf(`[ "$got" = "%[1]s version %[2]s" ] || { echo "the release's %[1]s reports \"$got\", not %[1]s version %[2]s" && exit 1; }`, g.cli, v),
			fmt.Sprintf(`echo "$dir/%s" >> "$GITHUB_PATH"`, dist),
		}, "\n"),
	}
}

// installMigrate downloads the release's migration runner,
// superschematic-migrate, a tarball of its own beside the CLI's, checks
// it against the release's SHA256SUMS and puts it on PATH, where a target
// that migrates from the deploying machine finds it (D64). A checkout
// build is no release, so its step fails, as install's does.
func (g github) installMigrate() step {
	v := g.req.Version
	if v == "" {
		return step{
			name: "Install superschematic-migrate",
			run:  `echo "A superschematic built from a checkout generated this workflow, and it names no release of superschematic-migrate to install; generate this workflow again with a released superschematic" && exit 1`,
		}
	}
	dist := fmt.Sprintf("superschematic-migrate_%s_linux-x64", v)
	return step{
		name: "Install superschematic-migrate " + v,
		run: strings.Join([]string{
			"set -euo pipefail",
			`dir="$RUNNER_TEMP/superschematic-migrate"`,
			`mkdir -p "$dir"`,
			"release=" + strings.TrimSuffix(release.DownloadURL(v, ""), "/"),
			fmt.Sprintf(`curl -fsSL -o "$dir/%[1]s.tar.gz" "$release/%[1]s.tar.gz"`, dist),
			`curl -fsSL -o "$dir/SHA256SUMS" "$release/SHA256SUMS"`,
			`(cd "$dir" && sha256sum --check --ignore-missing SHA256SUMS)`,
			fmt.Sprintf(`tar -xzf "$dir/%s.tar.gz" -C "$dir"`, dist),
			fmt.Sprintf(`echo "$dir/%s" >> "$GITHUB_PATH"`, dist),
		}, "\n"),
	}
}

// archives downloads the release's static archives for the runner's
// platform, checks them against the digest the binary that wrote the
// workflow names, and points cgo's linker at them: a server links
// superscalar's archive, and the version graph's, through cgo (D3), and
// no module the module proxy serves carries them (D47, amended). A binary
// the release workflow did not build names no digest, so its step fails.
func (g github) archives() step {
	name := "Install the static archives"
	a, ok := g.req.Archives[runnerPlatform]
	if !ok {
		return step{
			name: name,
			run:  fmt.Sprintf(`echo "The superschematic %s that generated this workflow names no digest of its release's static archives for %s, as a binary its release workflow did not build; generate this workflow again with the release's own binary" && exit 1`, g.req.Version, runnerPlatform),
		}
	}
	return step{
		name: name,
		run: strings.Join([]string{
			"set -euo pipefail",
			`dir="$RUNNER_TEMP/superschematic-archives"`,
			`mkdir -p "$dir"`,
			fmt.Sprintf(`curl -fsSL -o "$dir/archives.tar.gz" %s`, a.URL),
			fmt.Sprintf(`echo "%s  $dir/archives.tar.gz" | sha256sum --check -`, a.SHA256),
			`tar -xzf "$dir/archives.tar.gz" -C "$dir" --strip-components=1`,
			`echo "CGO_LDFLAGS=-L$dir/lib" >> "$GITHUB_ENV"`,
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
			setupBun(),
			{name: "Install the schemas root's packages", dir: root, run: "bun install --frozen-lockfile"},
		}
	case registry.PackageManagerNPM:
		return []step{
			setupNode(),
			{name: "Install the schemas root's packages", dir: root, run: "npm ci"},
		}
	}
	return nil
}

// scriptlessPackages installs the schemas root's packages as packages
// does, running none of their scripts: what a cloud job installs, which
// runs no project code (D47, amended).
func (g github) scriptlessPackages() []step {
	steps := g.packages()
	for i := range steps {
		if steps[i].run != "" {
			steps[i].name = "Install the schemas root's packages, running none of their scripts"
			steps[i].run += " --ignore-scripts"
		}
	}
	return steps
}

// prepare is the job that runs, with no credentials, all the project code
// the cloud jobs of environments need (D47, amended): it installs the
// schemas root's packages, builds every service, installs the output
// root's workspace when an environment builds a site or a bundle, and runs
// `stack prepare` for the environments, which evaluates the sidecar
// documents, writes each image's build context, and builds each site and
// bundle, into a directory it uploads as an artifact. It holds no
// identity token and no secret. It runs on every event a cloud job runs
// on: on a pull request from the repository, its closing too when the
// stack has a preview, where it builds nothing and prepares the documents
// a destroy reads.
func (g github) prepare(environments []string, previews bool) job {
	st := g.req.Stack
	steps := []step{g.checkout(), g.install()}
	steps = append(steps, g.packages()...)
	bun := false
	for _, env := range g.req.Environments {
		if slices.Contains(environments, env.Environment.Environment) && buildsWithBun(env) {
			bun = true
		}
	}
	if bun && st.PackageManager != registry.PackageManagerBun {
		steps = append(steps, setupBun())
	}
	var builds []step
	builds = append(builds, g.buildSteps()...)
	if bun {
		builds = append(builds, g.typeScriptWorkspace())
	}
	args := append([]string{g.cli, "stack", "prepare"}, environments...)
	args = append(args, "--stack", st.Dir, "--dir", `"$RUNNER_TEMP/prepared"`)
	builds = append(builds, step{name: "Prepare " + strings.Join(environments, ", "), run: strings.Join(args, " ")})
	cond := fmt.Sprintf("github.event_name != 'pull_request' || (%s && %s)", notClosed, sameRepository)
	if previews {
		cond = fmt.Sprintf("github.event_name != 'pull_request' || %s", sameRepository)
		for i := range builds {
			builds[i].cond = notClosed
		}
		builds = append(builds, step{
			name: "Prepare the documents of " + strings.Join(environments, ", ") + " for the destroy",
			cond: closed,
			run:  strings.Join(append(args, "--no-build"), " "),
		})
	}
	steps = append(steps, builds...)
	steps = append(steps, step{
		name: "Upload what prepare made",
		uses: &actionUpload,
		// A day is enough for the jobs of the run, the only ones that
		// read it; a deploy whose reviewers approve it later fails to
		// download, and the run is started again.
		with: [][2]string{{"name", preparedArtifact}, {"path", preparedDir}, {"if-no-files-found", "error"}, {"retention-days", "1"}},
	})
	return job{id: "prepare", name: "prepare", cond: cond, steps: steps}
}

// setupBun installs Bun at the release tools.env pins, the one a
// TypeScript server's image runs on, downloaded every time: setup-bun
// otherwise keeps the binary in the repository's Actions cache, which a
// job that runs the project's dependencies (check's, prepare's) can write,
// so a cloud job would run a binary they planted before it signs in (D47,
// amended).
func setupBun() step {
	return step{name: "Set up Bun", uses: &actionSetupBun, with: [][2]string{{"bun-version", servergen.BunVersion}, {"no-cache", "true"}}}
}

// NodeVersion is the Node.js release a job sets up for a project whose
// schemas root installs with npm: tools.env's NODE_VERSION, the release
// this repository's own CI runs Node on, which a test holds it to. A
// major release, as tools.env names it, which actions/setup-node resolves
// to its latest patch.
const NodeVersion = "24"

// setupNode installs Node.js at the release tools.env pins, so a job's
// npm ci runs on one release from run to run rather than whichever the
// current LTS is. It restores no package manager's cache, which setup-node
// otherwise does for a project that names its package manager, and which
// a job that runs the project's dependencies can write (D47, amended).
func setupNode() step {
	return step{name: "Set up Node.js", uses: &actionSetupNode, with: [][2]string{{"node-version", NodeVersion}, {"package-manager-cache", "false"}}}
}

// hasSite reports whether env deploys a static site, which `stack
// prepare` builds on the runner with Bun after a frozen install of the
// output root's workspace (D55; D47, amended): the prepare job sets up Bun
// and installs the workspace from the committed lockfile first. A job, a
// worker or a bucket needs nothing of the workflow: the deploy builds
// their images on the target, as a server's, and a bucket is a node of
// the graph.
func hasSite(env registry.CIEnvironment) bool {
	return slices.ContainsFunc(env.Environment.Deployables, func(d *ir.ResolvedDeployable) bool { return d.Kind == ir.DeployableSite })
}

// hasBundle reports whether env places a server, a job or a worker on a
// platform whose artifact is a bundle (D64), which `stack prepare` builds
// on the runner with `bun bundle.ts` after a frozen install of the output
// root's workspace, as it builds a site.
func hasBundle(env registry.CIEnvironment) bool {
	return slices.ContainsFunc(env.Environment.Deployables, func(d *ir.ResolvedDeployable) bool { return d.HasBundle() })
}

// buildsWithBun reports whether a deploy of env needs a site or a bundle
// built on the runner with Bun, so the prepare job sets up Bun and
// installs the workspace from the committed lockfile first.
func buildsWithBun(env registry.CIEnvironment) bool { return hasSite(env) || hasBundle(env) }

// bundled are the stack's servers, jobs and workers some environment
// places on a platform whose artifact is a bundle, each sorted by name in
// its kind, which check bundles as a deploy does (D64).
func (g github) bundled() []deployable {
	var out []deployable
	for _, kind := range []ir.DeployableKind{ir.DeployableServer, ir.DeployableJob, ir.DeployableWorker} {
		var names []string
		for _, env := range g.req.Environments {
			for _, d := range env.Environment.Deployables {
				if d.Kind == kind && d.HasBundle() && !slices.Contains(names, d.Name) {
					names = append(names, d.Name)
				}
			}
		}
		slices.Sort(names)
		for _, name := range names {
			out = append(out, deployable{kind, name})
		}
	}
	return out
}

// build builds every service of the services root, the stack's included,
// to the default output root: it resolves every environment and checks its
// graph, and writes each server's entrypoint and Dockerfile, which a
// deploy builds the server's image from.
func (g github) build() step {
	return step{name: "Build the services", run: g.cli + " build-all " + g.req.Stack.ServicesRoot}
}

// buildSteps are the steps of a cloud job that builds the services before
// its command: the build, after the Rust release tools.env pins for a
// stack with a Rust server, job or worker, so the build's cargo update
// --workspace runs on the release check ran it on and leaves the lockfile
// check accepted as it is (D57). The runner's own cargo may be another
// release.
func (g github) buildSteps() []step {
	if len(g.deployables(registry.APILanguageRust)) == 0 {
		return []step{g.build()}
	}
	return []step{setupRust(), g.build()}
}

// stackCommand is one of the stack commands on the stack, which the job
// names since it runs from the repository root, reading what the prepare
// job made (D47, amended).
func (g github) stackCommand(command, environment string, flags ...string) string {
	args := append([]string{g.cli, "stack", command, environment, "--stack", g.req.Stack.Dir, preparedFlag}, flags...)
	return strings.Join(args, " ")
}

// deployable is a server, job, worker, deriver, app or engine runner of
// the stack whose entrypoint the build writes at server/<stack>/<name>
// under the output root: a Go module, a package of the output root's Bun
// workspace (D51, D52, D53, D74), or a crate of its Cargo workspace (D57,
// D65, D78). It is its kind and name.
type deployable struct {
	kind ir.DeployableKind
	name string
}

// deployables are the stack's servers in language, then its jobs, then
// its workers, then its derivers (D74), then its apps (D78), then its
// engine runners (D58), each sorted by name. No environment changes them.
func (g github) deployables(language string) []deployable {
	var out []deployable
	for _, kind := range []ir.DeployableKind{ir.DeployableServer, ir.DeployableJob, ir.DeployableWorker, ir.DeployableDeriver, ir.DeployableApp, ir.DeployableRunner} {
		for _, name := range g.deployablesIn(kind, language) {
			out = append(out, deployable{kind, name})
		}
	}
	return out
}

// deployablesIn are the stack's deployables of kind in language, sorted.
func (g github) deployablesIn(kind ir.DeployableKind, language string) []string {
	var names []string
	for _, env := range g.req.Environments {
		for _, d := range env.Environment.Deployables {
			if d.Kind == kind && d.Language == language && !slices.Contains(names, d.Name) {
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

// signIn is how a job signs in as role with identity: Workload Identity
// Federation's step, which asks GitHub for an identity token, or a
// Cloudflare token and the state's passphrase, which the env of the steps
// that run the stack's commands reads from the workflow's secrets with no
// action (D64).
func signIn(identity *registry.CIIdentity, role registry.CIRole) (signedIn, error) {
	switch identity.Kind {
	case gcpWorkloadIdentity:
		provider, account := identity.Fields["provider"], identity.Fields["account"]
		if provider == "" || account == "" {
			return signedIn{}, fmt.Errorf("a %s identity needs a provider and an account", identity.Kind)
		}
		return signedIn{idToken: true, step: &step{
			name: "Sign in to Google Cloud as " + string(role),
			uses: &actionGoogleAuth,
			with: [][2]string{{"workload_identity_provider", provider}, {"service_account", account}},
		}}, nil
	case cloudflareAPIToken:
		token, passphrase := identity.Fields["token"], identity.Fields["passphrase"]
		if !secretName.MatchString(token) || !secretName.MatchString(passphrase) {
			return signedIn{}, fmt.Errorf("a %s identity needs a token and a passphrase, each the name of a GitHub Actions secret, not %q and %q", identity.Kind, token, passphrase)
		}
		env := [][2]string{
			{"CLOUDFLARE_API_TOKEN", "${{ secrets." + token + " }}"},
			{"PULUMI_CONFIG_PASSPHRASE", "${{ secrets." + passphrase + " }}"},
		}
		// A field env.<VARIABLE> sets that variable from the secret it
		// names: a deploy job's origin connection string of a database on
		// Hyperdrive, which its migrations read (D92).
		var vars []string
		for field := range identity.Fields {
			if strings.HasPrefix(field, "env.") {
				vars = append(vars, field)
			}
		}
		slices.Sort(vars)
		for _, field := range vars {
			name, secret := strings.TrimPrefix(field, "env."), identity.Fields[field]
			if !secretName.MatchString(name) || !secretName.MatchString(secret) {
				return signedIn{}, fmt.Errorf("a %s identity's %s sets a variable from a GitHub Actions secret, each named in upper case, not %q from %q", identity.Kind, field, name, secret)
			}
			env = append(env, [2]string{name, "${{ secrets." + secret + " }}"})
		}
		return signedIn{env: env}, nil
	}
	return signedIn{}, fmt.Errorf("the %s CI renderer cannot sign in with a CI identity of kind %q", GitHubRenderer, identity.Kind)
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
	if len(s.env) > 0 {
		w.line(4, "env:")
		for _, kv := range s.env {
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
