package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"path"
	"regexp"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The generated CI (docs/stack-model.md, section 11.3, D47): a CI renderer
// is the sixth stack registration, which writes a stack's workflow from its
// resolved environments, and a target says through its CI seam how a CI job
// signs in to each of them. A stack opts in with outputs.ci in its config,
// keyed by renderer; the Stack kind's `ci` generator renders.

// CIRole is the account a CI job signs in to an environment as: the
// accounts a target's bootstrap creates (section 7.3).
type CIRole string

const (
	// CIPlanner is the read-only account `stack plan` runs as.
	CIPlanner CIRole = "planner"

	// CIDeployer is the account `stack deploy` and `stack destroy` run as.
	CIDeployer CIRole = "deployer"
)

// CIIdentity is how a CI job signs in to an environment as one role: a
// kind, which a renderer turns into its own steps, and the kind's fields.
// The kinds are the targets': gcp's is `gcp-workload-identity`, whose
// fields are `provider` and `account`. A renderer refuses a kind it does
// not know.
type CIIdentity struct {
	Kind   string
	Fields map[string]string
}

// CIIdentities is a target's CI seam: how a CI job signs in to the
// target's environments (D47).
type CIIdentities interface {
	// Identity returns how a CI job signs in to env as role, or nil when
	// it cannot yet: on gcp, before bootstrap records the project's
	// number. It is pure.
	Identity(env *ir.ResolvedEnvironment, role CIRole) *CIIdentity
}

// CLITool is a command-line tool a provisioner runs, which a CI job
// installs before it plans or deploys: the pulumi CLI at the release of
// the Pulumi SDK the provisioner was built with.
type CLITool struct {
	Name    string
	Version string
}

// CIStack is the stack a CI renderer writes a workflow for, and where it
// lives. Each path is slash-separated and relative to the repository root,
// where a CI job starts.
type CIStack struct {
	// Name is the stack's name: its service's name.
	Name string

	// Dir is the Stack service's directory; ServicesRoot is its parent and
	// SchemasRoot the parent of that.
	Dir          string
	ServicesRoot string
	SchemasRoot  string

	// OutputRoot is where the workflow's build writes and the stack
	// commands read: the CLI's default output root, <schemas-root>/dist.
	OutputRoot string

	// PackageManager installs the schemas root's packages, as its lockfile
	// says: PackageManagerBun, PackageManagerNPM, or empty for none.
	PackageManager string

	// TypeScriptImplementations are the directories of the TypeScript
	// implementations of the APIs the stack's TypeScript servers serve, and
	// of the APIs whose jobs and workers its TypeScript job and worker
	// packages run (D52, D53), by service: the naming file's
	// [implementation_paths] typescript template (D51). A check job
	// type-checks each after it installs the output root's Bun workspace
	// from its lockfile, which the project commits (D51, amended).
	TypeScriptImplementations map[string]string

	// Sites are the stack's static sites (D55), by name: the directory of
	// each one's package and its build script. `stack deploy` builds each
	// site with Bun after a frozen install of the output root's workspace,
	// so a job that deploys a site sets up Bun and installs the workspace,
	// and a check job builds each site.
	Sites map[string]CISite

	// EngineArchives are the project engine crates of the stack's engine
	// databases whose types compose a behavior the core's engine archive
	// does not run, the work queue's, a commerce one or an extension's
	// (D58), by database: the directory of the crate's ffi member, which
	// exports the engine's C ABI over the project's behaviors, under the
	// output root at engine/rust/<db>/ffi, where the build writes it. A check job builds
	// each one's archive after the build and compiles each Go server on
	// the database against it, in place of the core's the release ships.
	EngineArchives map[string]string

	// EngineCheckout is the superschematic checkout the naming file's
	// [paths] engine keys name (D58), relative to the repository root,
	// when some environment of the stack runs an engine and the checkout
	// lies in the repository; empty otherwise. The checkout's engine
	// crates reach superscalar's core by path, in a checkout of its own
	// under the checkout's third_party/, which git does not hold, and its
	// TypeScript packages export a dist/ no checkout holds either. So a
	// check job stands up that superscalar checkout before it builds a
	// project's engine archive, and, for a stack with TypeScript
	// deployables, builds the checkout's superscalar binding, HTTP runtime
	// and engine packages, which the output root's workspace takes in
	// place of the published ones, before it type-checks against them: what
	// make setup builds on a developer's machine.
	EngineCheckout string

	// EngineCheckoutTypeScript is true when the naming file's [paths]
	// engine_typescript names the engine package of EngineCheckout, so the
	// output root's workspace takes the checkout's packages: only then does
	// a check job build their dist/.
	EngineCheckoutTypeScript bool
}

// CISite is a static site a CI job builds: its package's directory,
// relative to the repository root, and the package.json script that
// builds it.
type CISite struct {
	Dir   string
	Build string
}

// The package managers CIStack.PackageManager names.
const (
	PackageManagerBun = "bun"
	PackageManagerNPM = "npm"
)

// CIEnvironment is one environment of the stack as a CI renderer sees it.
type CIEnvironment struct {
	Environment *ir.ResolvedEnvironment

	// HasSeam is whether the environment's target has a CI seam. Without
	// one no bootstrap gives the environment an identity.
	HasSeam bool

	// Planner and Deployer are the identities a CI job signs in as, each
	// nil when the target gives none.
	Planner  *CIIdentity
	Deployer *CIIdentity

	// Tools are the command-line tools the environment's provisioner runs.
	Tools []CLITool

	// Migrate is whether a job that deploys the environment runs the
	// migration runner itself: the environment holds a database, and its
	// target's runner runs on the deploying machine
	// (DeployingMachineMigrations), so the job installs
	// superschematic-migrate (D64).
	Migrate bool
}

// Identity returns the environment's identity for role.
func (e CIEnvironment) Identity(role CIRole) *CIIdentity {
	if role == CIPlanner {
		return e.Planner
	}
	return e.Deployer
}

// CIOptions are a renderer's options from a stack's config,
// outputs.ci.<renderer>.
type CIOptions struct {
	// Branch is the branch pull requests target and pushes deploy from.
	// Empty is DefaultCIBranch.
	Branch string `json:"branch,omitempty"`

	// Install is the directory, relative to the repository root, the
	// workflow is installed into when it exists. Empty is the renderer's
	// Dir.
	Install string `json:"install,omitempty"`
}

// DefaultCIBranch is the branch a renderer's options default to.
const DefaultCIBranch = "main"

// WithDefaults returns o with its defaults for the renderer spec.
func (o CIOptions) WithDefaults(spec CIRendererSpec) CIOptions {
	if o.Branch == "" {
		o.Branch = DefaultCIBranch
	}
	if o.Install == "" {
		o.Install = spec.Dir
	}
	return o
}

// CIRequest is what a CI renderer renders: the stack, its resolved
// environments in declaration order, the renderer's options and the
// release of superschematic the workflow installs.
type CIRequest struct {
	Stack        CIStack
	Environments []CIEnvironment
	Options      CIOptions

	// Version is the release that renders the workflow, without its `v`.
	// Empty for a binary built from a checkout, which is no release: the
	// workflow's install step then fails and says to render it again with
	// a release.
	Version string

	// Archives are the static archives the release ships beside the
	// binary, by platform (`linux-x64`), which a job that compiles a Go
	// server downloads and links: superscalar's and the version graph's
	// (D47, amended). Empty for a binary the release workflow did not
	// build, which names no digests.
	Archives map[string]CIArchive

	// RootApps are the apps of the services root's other stacks that need
	// an extension (D78). Every job builds the whole services root
	// (build-all), which builds them, so the workflow's binary must link
	// what they need as well as what the stack's own apps need.
	RootApps []CIApp
}

// CIApp is an app (D78) of a stack of the services root: the stack, and
// the app as the stack resolves it, with the extensions its build needs.
type CIApp struct {
	Stack string
	App   ir.ResolvedApp
}

// CIArchive is one platform's static archives in a release: a tarball at
// URL whose hex SHA-256 is SHA256, holding one directory whose lib/ holds
// the archives.
type CIArchive struct {
	URL    string
	SHA256 string
}

// CIFile is one file a CI renderer writes.
type CIFile struct {
	// Path is relative to the install directory, slash-separated.
	Path string
	Data []byte
}

// CIRendererSpec registers a CI renderer: GitHub Actions is the core's
// (D47).
type CIRendererSpec struct {
	// Name is the registry key and the key of outputs.ci (`github`).
	Name string

	// Extension is the registering extension's Name().
	Extension string

	// Dir is the default install directory, relative to the repository
	// root and slash-separated (`.github/workflows`).
	Dir string

	// Render returns the files of the stack's workflow. It is pure: the
	// same request gives the same bytes, with no timestamp.
	Render func(CIRequest) ([]CIFile, error)
}

// RegisterCIRenderer adds a CI renderer. It refuses a malformed or
// duplicate name, a missing Render, and a Dir that is not a relative,
// slash-separated path inside the repository.
func (r *Registry) RegisterCIRenderer(spec CIRendererSpec) error {
	if err := r.registrable("CI renderer " + spec.Name); err != nil {
		return err
	}
	if err := checkStackKey("CI renderer", spec.Name); err != nil {
		return err
	}
	if _, dup := r.stack.ciRenderers[spec.Name]; dup {
		return fmt.Errorf("registry: CI renderer %q is already registered", spec.Name)
	}
	if spec.Render == nil {
		return fmt.Errorf("registry: CI renderer %q has no Render function", spec.Name)
	}
	if err := checkRepositoryPath(spec.Dir); err != nil {
		return fmt.Errorf("registry: CI renderer %q Dir: %w", spec.Name, err)
	}
	r.stack.ciRenderers[spec.Name] = spec
	r.noteExtension(spec.Extension)
	return nil
}

// CIRenderer returns the CI renderer registered under name.
func (r *Registry) CIRenderer(name string) (CIRendererSpec, bool) {
	spec, ok := r.stack.ciRenderers[name]
	return spec, ok
}

// CIRenderers returns the registered CI renderer names, sorted.
func (r *Registry) CIRenderers() []string { return keysOf(r.stack.ciRenderers) }

// CIEnvironment returns env as a CI renderer sees it: the identities its
// target's CI seam gives each role, and the tools its provisioner runs.
func (r *Registry) CIEnvironment(env *ir.ResolvedEnvironment) CIEnvironment {
	out := CIEnvironment{Environment: env}
	if target, ok := r.stack.targets[env.Target]; ok && target.CI != nil {
		out.HasSeam = true
		out.Planner = target.CI.Identity(env, CIPlanner)
		out.Deployer = target.CI.Identity(env, CIDeployer)
	}
	if target, ok := r.stack.targets[env.Target]; ok {
		if _, local := target.Migrations.(DeployingMachineMigrations); local {
			for _, d := range env.Deployables {
				if d.Kind == ir.DeployableDatabase {
					out.Migrate = true
				}
			}
		}
	}
	if provisioner, ok := r.stack.provisioners[env.Provisioner]; ok {
		out.Tools = append([]CLITool(nil), provisioner.Tools...)
	}
	return out
}

// checkRepositoryPath refuses a path that is empty, absolute, not
// slash-separated and clean, or outside the repository.
func checkRepositoryPath(p string) error {
	switch {
	case p == "":
		return fmt.Errorf("is empty")
	case strings.HasPrefix(p, "/") || strings.Contains(p, `\`) || path.Clean(p) != p || p == "." || p == ".." || strings.HasPrefix(p, "../"):
		return fmt.Errorf("%q is not a slash-separated path inside the repository", p)
	}
	return nil
}

// ciBranchPattern is a branch outputs.ci may name: a plain branch name, no
// pattern.
var ciBranchPattern = regexp.MustCompile(`^[A-Za-z0-9._-]+(/[A-Za-z0-9._-]+)*$`)

// parseCIOutputs reads outputs.ci strictly: each key names a registered CI
// renderer, each section holds branch and install only, the branch is a
// plain branch name and the install directory a path inside the
// repository.
func parseCIOutputs(section json.RawMessage, reg *Registry) (map[string]CIOptions, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(section, &raw); err != nil {
		return nil, fmt.Errorf("outputs.ci: %w", err)
	}
	out := make(map[string]CIOptions, len(raw))
	for _, name := range keysOf(raw) {
		if _, ok := reg.CIRenderer(name); !ok {
			return nil, fmt.Errorf("outputs.ci names CI renderer %q, which is not registered (registered CI renderers: %s)", name, strings.Join(reg.CIRenderers(), ", "))
		}
		dec := json.NewDecoder(bytes.NewReader(raw[name]))
		dec.DisallowUnknownFields()
		var opts CIOptions
		if err := dec.Decode(&opts); err != nil {
			return nil, fmt.Errorf("outputs.ci.%s: %w", name, err)
		}
		if opts.Branch != "" && !ciBranchPattern.MatchString(opts.Branch) {
			return nil, fmt.Errorf("outputs.ci.%s.branch %q is not a branch name", name, opts.Branch)
		}
		if opts.Install != "" {
			if err := checkRepositoryPath(opts.Install); err != nil {
				return nil, fmt.Errorf("outputs.ci.%s.install: %w", name, err)
			}
		}
		out[name] = opts
	}
	return out, nil
}
