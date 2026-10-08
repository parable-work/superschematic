package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path"
	"regexp"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The seams the cloud half of the `stack` commands drives
// (docs/stack-model.md, sections 7.3, 11.1 and 11.2): a target keeps its
// deploy state, stores secret values and platform credentials, bootstraps
// a cloud project and runs migration plans on its databases. Each is an
// interface a TargetSpec carries; the core's deploy (internal/stackdeploy)
// calls them, and none of them knows the others.

// Run is one run of an environment: the resolved environment, and the
// values of its parameters for this run. A plain environment has one run;
// a parameterized one has a run per set of values (section 5.4).
type Run struct {
	Environment *ir.ResolvedEnvironment

	// Parameters hold a value for each of the environment's parameters.
	Parameters map[string]string
}

// runValuePattern is the shape of a parameter's value in a run: what a
// provisioner's stack name and an object name both carry.
var runValuePattern = regexp.MustCompile(`^[A-Za-z0-9_.-]+$`)

// Check refuses a run with no environment, or whose parameters are not
// exactly the environment's, each with a value of letters, digits,
// hyphens, underscores and dots.
func (r Run) Check() error {
	env := r.Environment
	if env == nil {
		return errors.New("the run has no environment")
	}
	for _, param := range env.Parameters {
		value, ok := r.Parameters[param]
		if !ok {
			return fmt.Errorf("environment %s takes parameter %s; give its value (--param %s=<value>)", env.Environment, param, param)
		}
		if !runValuePattern.MatchString(value) {
			return fmt.Errorf("parameter %s of environment %s is %q; a value is letters, digits, hyphens, underscores and dots", param, env.Environment, value)
		}
	}
	for param := range r.Parameters {
		if !containsString(env.Parameters, param) {
			return fmt.Errorf("environment %s has no parameter %s (its parameters: %v)", env.Environment, param, env.Parameters)
		}
	}
	return nil
}

// Name names the run: the environment's name, then each parameter's name
// and value in the environment's order. Staging is `Staging`; Preview with
// parameter pr at 123 is `Preview.pr-123`. Call it after Check.
func (r Run) Name() string {
	var b strings.Builder
	b.WriteString(r.Environment.Environment)
	for _, param := range r.Environment.Parameters {
		fmt.Fprintf(&b, ".%s-%s", param, r.Parameters[param])
	}
	return b.String()
}

// StateStore keeps a target's deploy state (section 11.2): where its
// provisioner keeps each environment's state, and each run's deploy
// manifest. The target's bootstrap creates what the store reads (section
// 7.3): a GCS bucket and a KMS key on gcp.
type StateStore interface {
	// Backend returns where the provisioner keeps env's state.
	Backend(ctx context.Context, env *ir.ResolvedEnvironment) (StateBackend, error)

	// ReadManifest returns the run's deploy manifest, or an error that
	// wraps ErrNoManifest when the run has none yet.
	ReadManifest(ctx context.Context, run Run) ([]byte, error)

	// WriteManifest replaces the run's deploy manifest.
	WriteManifest(ctx context.Context, run Run, manifest []byte) error

	// DeleteManifest removes the run's deploy manifest. A run without one
	// is not an error.
	DeleteManifest(ctx context.Context, run Run) error
}

// ErrNoManifest is what a StateStore's ReadManifest wraps for a run that
// was never deployed.
var ErrNoManifest = errors.New("no deploy manifest")

// SecretStore keeps the values of an environment's secrets, keyed by an
// ID: an application secret's StackSecret ID, its declaring type and field
// (`PaymentsSecrets.STRIPE_KEY`, section 4.2), from which the store
// derives where the value lives; or a platform credential's Secret, the
// store's own name for it, which holds no dot. Secret Manager on gcp. A
// value goes in and is never written to a file or a log.
type SecretStore interface {
	// Exists reports whether the secret has a value.
	Exists(ctx context.Context, env *ir.ResolvedEnvironment, id string) (bool, error)

	// List returns the IDs, sorted, of env's secrets and of its
	// credentials that have a value.
	List(ctx context.Context, env *ir.ResolvedEnvironment) ([]string, error)

	// Set stores a new value of the secret. It returns an error that
	// wraps ErrSecretNotCreated when the secret's storage does not exist
	// yet: the deploy's infrastructure step creates an application
	// secret's, and bootstrap a credential's.
	Set(ctx context.Context, env *ir.ResolvedEnvironment, id string, value []byte) error

	// Get returns the secret's value. The deploy reads a platform
	// credential with it, to hand the provisioner; servers read their
	// application secrets at run time, and the deploy never does.
	Get(ctx context.Context, env *ir.ResolvedEnvironment, id string) ([]byte, error)
}

// ErrSecretNotCreated is what a SecretStore's Set wraps when the secret's
// storage does not exist yet.
var ErrSecretNotCreated = errors.New("the secret's storage does not exist yet")

// BootstrapRequest is one bootstrap of the cloud project an environment
// deploys to.
type BootstrapRequest struct {
	// Environment is the resolved environment.
	Environment *ir.ResolvedEnvironment

	// Repository is the repository the generated CI runs in, as the git
	// remote names it (`acme/shop` on GitHub); empty when there is none.
	Repository string

	// Credentials are the environment's platform credentials, each named
	// once. Bootstrap creates each one's storage, readable by the accounts
	// that deploy and by nothing else; the core then asks for each value
	// that is missing and stores it.
	Credentials []Credential

	// Provisioner applies what the bootstrap provisions through it, a
	// program it renders under Dir.
	Provisioner Provisioner
	Dir         string

	// Log receives progress. Nothing secret is written to it.
	Log io.Writer
}

// Bootstrapper prepares the cloud project an environment deploys to, once,
// with an owner's credentials (section 7.3). It is idempotent: a second
// run changes nothing that the first one made. It returns what it read
// that the schema should hold, or nil for nothing.
type Bootstrapper interface {
	Bootstrap(ctx context.Context, req BootstrapRequest) (*BootstrapResult, error)
}

// BootstrapResult is what a bootstrap read from the cloud that the
// environment's declaration should hold.
type BootstrapResult struct {
	// Values are target values for the core to record in the schema
	// source (D47), each key once.
	Values []BootstrapValue
}

// BootstrapValue is a target value that only the cloud knows, such as the
// GCP project's number, which bootstrap reads. The core records it in the
// target values of the environment whose declaration sets Beside, the
// value it comes from, so an environment that inherits that value inherits
// this one too; it writes it when the schema has none, writes it again and
// says so when it differs, and leaves the schema alone when it matches. A
// person may correct it by hand like any value.
type BootstrapValue struct {
	// Key is the value's key in the target's values (`projectNumber`),
	// and Value the value, a string.
	Key   string
	Value string

	// Beside is the key of the value it belongs with (`project`); a new
	// property is written after that one's.
	Beside string
}

// MigrationPlan is one DB service's migration plan, as the plan document
// `superschematic migrate plan --out` writes (D27): the runner applies it
// and never computes one.
type MigrationPlan struct {
	// Service is the DB service the plan migrates.
	Service string

	// Plan is the plan document.
	Plan json.RawMessage

	// Steps is the number of the plan's steps in the request's phase. It
	// is zero for a plan the deploy hands the runner only for what the
	// runner owns besides the steps: the privileges of the servers that
	// connect (Servers), which changed since the runner last ran.
	Steps int

	// Servers are the server deployables that connect to the DB service,
	// as the environment's sql edges name them, sorted. A runner that owns
	// the database's privileges (D46) gives each what its APIs read and
	// write, after the phase's steps, and takes them back from any server
	// it gave them to that is no longer here.
	Servers []string
}

// MigrationRequest runs one phase of the migration plans of one database:
// the plan of each DB service it hosts that has steps in the phase, and
// in the expand phase also each plan whose connecting servers changed.
type MigrationRequest struct {
	Run Run

	// Database is the database deployable.
	Database string

	// Phase is the phase to run: expand before the rollout, contract
	// after it (section 5.3).
	Phase ir.MigrationPhase

	// Plans are the plans to run, in service order.
	Plans []MigrationPlan

	// Log receives progress.
	Log io.Writer
}

// MigrationRunner runs migration plans on a target's databases, where the
// runner (`superschematic-migrate`, D27) reaches them: a job on the
// target. It returns when the phase has run, or with the runner's error;
// the runner resumes a plan a failed run left part-way.
type MigrationRunner interface {
	Migrate(ctx context.Context, req MigrationRequest) error
}

// BuildRequest is one image build (docs/stack-model.md, section 11.2): a
// server or a job, its Dockerfile and its context. The deploy writes the
// context: the build context directory as the Dockerfile's ignore file cuts
// it down, in a gzipped tarball whose entries carry no time, owner or mode
// of the machine that wrote it, so the same files give the same archive.
type BuildRequest struct {
	Run Run

	// Deployable is the server or job the image is for (D52): a deployable
	// whose kind has an image (ir.DeployableKind.HasImage).
	Deployable string

	// Context is the path of the gzipped tarball of the build context.
	// ContextDigest names it: `sha256:` and the hex SHA-256 of its tar
	// stream before compression.
	Context       string
	ContextDigest string

	// Dockerfile is the Dockerfile's path inside the context,
	// slash-separated (`schemas/dist/server/Shop/shop-api/Dockerfile`).
	Dockerfile string

	// Log receives progress.
	Log io.Writer
}

// contextDigestPattern is the shape of a BuildRequest's ContextDigest.
var contextDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// Check refuses a request with no valid run, no server or job of the run's
// environment, no context archive or digest, or a Dockerfile path that is
// not a relative, slash-separated path inside the context.
func (r BuildRequest) Check() error {
	if err := r.Run.Check(); err != nil {
		return err
	}
	d := r.Run.Environment.Deployable(r.Deployable)
	switch {
	case d == nil || !d.Kind.HasImage():
		return fmt.Errorf("build: environment %s has no server or job %q", r.Run.Environment.Environment, r.Deployable)
	case r.Context == "":
		return fmt.Errorf("build %s: no context archive", r.Deployable)
	case !contextDigestPattern.MatchString(r.ContextDigest):
		return fmt.Errorf("build %s: context digest %q is not sha256:<64 hex digits>", r.Deployable, r.ContextDigest)
	case r.Dockerfile == "" || strings.HasPrefix(r.Dockerfile, "/") || strings.Contains(r.Dockerfile, `\`) ||
		path.Clean(r.Dockerfile) != r.Dockerfile || r.Dockerfile == ".." || strings.HasPrefix(r.Dockerfile, "../"):
		return fmt.Errorf("build %s: Dockerfile %q is not a slash-separated path inside the context", r.Deployable, r.Dockerfile)
	}
	return nil
}

// ImageBuilder builds a server's or a job's image from a context the
// deploy wrote, and pushes it where the deployable's platform reads it:
// the repository path the platform writes into the graph (section 7.2). It
// returns the image by digest, `<repository>@sha256:<digest>`, which the
// deploy pins in the deployable's nodes; one whose repository no node
// holds is refused there.
type ImageBuilder interface {
	Build(ctx context.Context, req BuildRequest) (image string, err error)
}

// JobRunRequest is one run of a deployed job on demand, outside its
// schedule (`superschematic stack run`, docs/stack-model.md, section 8.7,
// D52).
type JobRunRequest struct {
	Run Run

	// Job is the job deployable to run.
	Job string

	// Image is the image the deploy manifest records for the job: what
	// the last deploy rolled out, and so what the run runs.
	Image string

	// Log receives progress, and the run's output where the target reads
	// it.
	Log io.Writer
}

// Check refuses a request with no valid run, no job of the run's
// environment, or no image.
func (r JobRunRequest) Check() error {
	if err := r.Run.Check(); err != nil {
		return err
	}
	d := r.Run.Environment.Deployable(r.Job)
	switch {
	case d == nil || d.Kind != ir.DeployableJob:
		return fmt.Errorf("run: environment %s has no job %q", r.Run.Environment.Environment, r.Job)
	case r.Image == "":
		return fmt.Errorf("run %s: no image", r.Job)
	}
	return nil
}

// JobRunner runs a deployed job once on demand (D52), as its platform runs
// it on its schedule: the job the last deploy applied, with its config,
// its identity, its timeout and its retries. It returns when the run ends:
// nil when a try succeeded, else the last try's error, which says where
// the run's logs are.
type JobRunner interface {
	RunJob(ctx context.Context, req JobRunRequest) error
}

// Credential is a secret a platform needs to write its resources, such
// as the API token of the Cloudflare DNS platform (section 6.9): not an
// application secret a server reads, but one the provisioner hands its
// provider. Bootstrap creates its storage, readable by the accounts that
// deploy, and asks for its value; every run reads it.
type Credential struct {
	// Secret is the secret's name in the target's secret store, as the
	// environment names it (`shop-cloudflare-dns-acme_dev`).
	Secret string

	// Env is the environment variable the provider reads the value from
	// (`CLOUDFLARE_API_TOKEN`). Empty when the provider reads it otherwise.
	Env string

	// Description says what to enter; bootstrap prompts with it.
	Description string
}

// checkDeploySeams refuses a target that carries a deploy seam it cannot
// use: State, Bootstrap, Migrations, Builder, CI or Jobs without a
// provisioner, and Bootstrap, Migrations, Builder, CI or Jobs without
// State, which a bootstrap creates, a deploy records each migration and
// each build in, a CI job plans and deploys from, and a job's run on
// demand reads the image of.
func checkDeploySeams(spec TargetSpec) error {
	var named []string
	if spec.State != nil {
		named = append(named, "State")
	}
	if spec.Bootstrap != nil {
		named = append(named, "Bootstrap")
	}
	if spec.Migrations != nil {
		named = append(named, "Migrations")
	}
	if spec.Builder != nil {
		named = append(named, "Builder")
	}
	if spec.CI != nil {
		named = append(named, "CI")
	}
	if spec.Jobs != nil {
		named = append(named, "Jobs")
	}
	if len(named) > 0 && spec.Provisioner == "" {
		return fmt.Errorf("registry: target %q has %s but names no provisioner to deploy with", spec.Name, strings.Join(named, ", "))
	}
	if (spec.Bootstrap != nil || spec.Migrations != nil || spec.Builder != nil || spec.CI != nil || spec.Jobs != nil) && spec.State == nil {
		return fmt.Errorf("registry: target %q has Bootstrap, Migrations, Builder, CI or Jobs but no State: a bootstrap creates the deploy state, a deploy records each migration and each build in it, a CI job plans and deploys from it, and a job's run on demand runs the image it records", spec.Name)
	}
	return nil
}
