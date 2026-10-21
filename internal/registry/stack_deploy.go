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
	"time"

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
// 7.3): a GCS bucket and a KMS key on gcp. A store that is also a
// StateLocker keeps each run's deploy lock beside its manifest; with one
// that is not, nothing keeps two deploys of a run apart beyond the
// provisioner's own lock on each of its updates.
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

// StateLocker is a StateStore that keeps a deploy lock per run beside the
// run's manifest (D46, amended): `stack deploy` and `stack destroy` take it
// before they read the manifest and release it when they end, whether they
// succeed or fail, so two of them never run on one run at once. The lock
// is held, not leased: it has no expiry, and only its holder's release or
// a person's `stack unlock` removes it. On gcp it is an object in the
// state bucket, written only where none is (`ifGenerationMatch=0`).
//
// Each method is atomic where the store is: Lock writes only where no lock
// is, and Unlock removes only the lock it names, so a deploy whose lock a
// person removed does not release the lock of the deploy that took it
// next. A run of a parameterized environment has a lock of its own, as it
// has a manifest of its own.
type StateLocker interface {
	// Lock takes the run's lock: where no lock is, it writes lock and
	// returns nil; where one is, it changes nothing and returns a
	// *LockHeldError holding that lock.
	Lock(ctx context.Context, run Run, lock DeployLock) error

	// ReadLock returns the run's lock, or nil when none is held.
	ReadLock(ctx context.Context, run Run) (*DeployLock, error)

	// Unlock removes the run's lock when its ID is id. A run with no lock
	// is not an error; one whose lock has another ID keeps it, and Unlock
	// returns a *LockHeldError holding that lock.
	Unlock(ctx context.Context, run Run, id string) error
}

// DeployLock is the lock a deploy or a destroy of a run holds while it
// runs: who took it, where and when, so the deploy it refuses can say so,
// and a person can tell whether the one that holds it still runs.
type DeployLock struct {
	// ID names this hold of the lock, a random token: the holder releases
	// the lock only while it carries this ID.
	ID string `json:"id"`

	// Run names the run the lock keeps (Run.Name), and Operation what holds
	// it: `deploy` or `destroy`.
	Run       string `json:"run"`
	Operation string `json:"operation"`

	// Holder is who took the lock: the user, or the actor of the CI run.
	// Host is the machine it runs on and PID its process.
	Holder string `json:"holder"`
	Host   string `json:"host"`
	PID    int    `json:"pid,omitempty"`

	// CI is the URL of the CI run that holds the lock, when one does.
	CI string `json:"ci,omitempty"`

	// Time is when the lock was taken, in UTC.
	Time time.Time `json:"time"`
}

// String describes who holds the lock and since when:
// `a deploy by ana on ana-laptop (pid 4242) since 2026-10-19T12:00:00Z`.
func (l DeployLock) String() string {
	var b strings.Builder
	op := l.Operation
	if op == "" {
		op = "deploy"
	}
	fmt.Fprintf(&b, "a %s", op)
	if l.Holder != "" {
		fmt.Fprintf(&b, " by %s", l.Holder)
	}
	if l.Host != "" {
		fmt.Fprintf(&b, " on %s", l.Host)
	}
	if l.PID > 0 {
		fmt.Fprintf(&b, " (pid %d)", l.PID)
	}
	if l.CI != "" {
		fmt.Fprintf(&b, " in CI run %s", l.CI)
	}
	if !l.Time.IsZero() {
		fmt.Fprintf(&b, " since %s", l.Time.UTC().Format(time.RFC3339))
	}
	return b.String()
}

// ErrLockHeld is what a LockHeldError wraps.
var ErrLockHeld = errors.New("the deploy lock is held")

// LockHeldError is a StateLocker's refusal of a run's lock that another
// holds, with that lock.
type LockHeldError struct {
	Lock DeployLock
}

func (e *LockHeldError) Error() string {
	return fmt.Sprintf("the deploy lock of %s is held by %s", e.Lock.Run, e.Lock)
}

func (e *LockHeldError) Unwrap() error { return ErrLockHeld }

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

	// GitHub asks the bootstrap to store what the generated CI signs in
	// with as the GitHub repository's Actions secrets, at the scopes the
	// target's CI seam names: the cloudflare target's tokens and its
	// state's passphrase, through `gh` (D64). A target whose CI signs in
	// without a secret, as gcp's does, ignores it.
	GitHub bool

	// Log receives progress. Nothing secret is written to it.
	Log io.Writer

	// Reveal receives what the bootstrap shows the person once and keeps
	// nowhere they can read it back: a passphrase it generated, or a token
	// it could not hand to `gh` (D64). The CLI writes it to stdout. A
	// bootstrap that would need it and has none refuses before it makes the
	// value.
	Reveal io.Writer
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

	// Engine reports that the DB service is an engine database (D58), the
	// database of the environment's runner, whose engine every deployable
	// in Servers opens, each as itself. A runner that owns the database's
	// privileges gives them the engine's schema too: a role they share
	// that owns it, which the engine acts as.
	Engine bool
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

	// Outputs reads the applied graph's outputs, by node ID and output
	// name, as the provisioner's Outputs reads them after the steps before
	// this one: a runner that reaches a database by an id its node
	// outputs, as the cloudflare target's reaches a D1 database by its
	// uuid (D64), reads it here rather than looking the database up by
	// name. The deploy sets it; a runner that reads no output ignores it.
	Outputs func(ctx context.Context) (map[string]map[string]any, error)

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

// DeployingMachineMigrations is a MigrationRunner that runs
// superschematic-migrate on the deploying machine rather than as a job on
// the target: the cloudflare target's, against D1's REST API (D64). A
// generated CI job that deploys an environment with a database then
// installs the runner beside superschematic (CIEnvironment.Migrate).
type DeployingMachineMigrations interface {
	MigrationRunner
	MigratesOnTheDeployingMachine()
}

// PrivilegelessMigrations is a MigrationRunner whose databases have no
// roles, so the plan of a phase without steps, which the deploy hands the
// runner when the servers that connect changed, has nothing to grant: the
// cloudflare target's, on D1 (D64). The deploy prepares such a runner
// (MigrationPreparer) only for a run with steps to migrate or a phase
// left pending, not for a change of the servers that connect alone.
type PrivilegelessMigrations interface {
	MigrationRunner
	GrantsNoPrivileges()
}

// MigrationPreparer is a MigrationRunner that readies, before a deploy
// changes anything, what each phase it runs will need, and refuses a run
// it could not migrate: on gcp, the runner's image, which it finds, or
// builds from the release, or refuses with what to set (D46). The deploy
// calls it once, after the migration plans and the hazard gate, when the
// run has a phase to migrate; an error stops the deploy before its first
// step.
type MigrationPreparer interface {
	PrepareMigrations(ctx context.Context, run Run, log io.Writer) error
}

// BuildRequest is one image build (docs/stack-model.md, section 11.2): a
// server or a job, its Dockerfile and its context. The deploy writes the
// context: the build context directory as the Dockerfile's ignore file cuts
// it down, in a gzipped tarball whose entries carry no time, owner or mode
// of the machine that wrote it, so the same files give the same archive.
type BuildRequest struct {
	Run Run

	// Deployable is the server, job or worker the image is for (D52, D53):
	// a deployable whose artifact is an image (ir.ResolvedDeployable.HasImage).
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
	case d == nil || !d.HasImage():
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

// SitePublishRequest is one publish of a site's files for a run
// (docs/stack-model.md, section 8.10, D55): the files a site's build wrote,
// named by their digest, and the site's config for the run.
type SitePublishRequest struct {
	Run Run

	// Site is the site deployable.
	Site string

	// Digest names the files: `sha256:` and the hex SHA-256 of their tar
	// stream, every file in path order at the epoch, as a build context is
	// digested (D46). The platform writes where the site serves its files
	// from into the graph with no digest, and the deploy pins it to Digest
	// (stackdeploy.PinSites), so the switch to new files is the
	// provisioner's apply of that node.
	Digest string

	// Dir is the directory the site's build wrote, whose files the
	// publisher puts under Digest unless they are there already. Empty when
	// the deploy builds nothing: the files are there from an earlier
	// publish, as when a deploy is given the digest a previous deploy
	// recorded, to roll the site back to it. Its files' names are
	// slash-separated paths below it.
	Dir string

	// Config is the site's config for the run, the JSON document the site
	// reads at ir.SiteConfigPath: each API it calls by name, with its
	// public address. The publisher writes it beside the files of Digest
	// and serves it uncached. Nil writes none, as `stack build` does,
	// which uploads the files a deploy then takes by their digest.
	Config []byte

	// Log receives progress.
	Log io.Writer
}

// Check refuses a request with no valid run, no site of the run's
// environment, or a digest that is not sha256:<64 hex digits>.
func (r SitePublishRequest) Check() error {
	if err := r.Run.Check(); err != nil {
		return err
	}
	d := r.Run.Environment.Deployable(r.Site)
	switch {
	case d == nil || d.Kind != ir.DeployableSite:
		return fmt.Errorf("publish: environment %s has no site %q", r.Run.Environment.Environment, r.Site)
	case !contextDigestPattern.MatchString(r.Digest):
		return fmt.Errorf("publish %s: digest %q is not sha256:<64 hex digits>", r.Site, r.Digest)
	}
	return nil
}

// SitePublisher puts a site's files where its platform serves them from,
// under their digest, and writes the site's config for a run there (D55).
// It switches nothing: the deploy pins the digest into the site's nodes,
// and the provisioner's apply of them serves the new files at once. It
// keeps the files of every digest it published, so a deploy given an
// earlier digest rolls the site back without a build.
type SitePublisher interface {
	// Publish uploads the files of req.Dir under req.Digest, unless they
	// are there already, then writes req.Config. With no Dir, it refuses a
	// digest whose files are not there.
	Publish(ctx context.Context, req SitePublishRequest) error
}

// BundlePublisher keeps the files of the bundles a deploy builds (D64): the
// files `bun bundle.ts <dir>` wrote in a server's, a job's or a worker's
// package, under their digest, where a deploy that did not build them
// fetches them (ArtifactFetcher). It switches nothing: the deploy pins the
// digest and each file's hash into the deployable's nodes, places the files
// beside the provisioner's program (ir.ArtifactsDir), and the provisioner's
// apply of the nodes uploads them. It keeps the files of every digest it
// published, so a deploy given an earlier digest rolls the deployable back
// without a build. A BundlePublisher must also be an ArtifactFetcher for a
// deploy that builds nothing, a rollback or a plan, to place the files.
type BundlePublisher interface {
	// Publish keeps the files of dir, whose names are slash-separated paths
	// below it, under digest (`sha256:` and the hex SHA-256 of their tar
	// stream, as a site's files are digested), unless they are there
	// already.
	Publish(ctx context.Context, run Run, deployable, dir, digest string) error
}

// ArtifactFetcher is a BundlePublisher or a SitePublisher that writes back
// the files it keeps under a digest (D64): into dest, a directory that
// exists and is empty, each file at its slash-separated path below it. A
// deploy fetches a bundle it did not build, for a plan in CI or a
// `--bundle` rollback, and checks the files against the digest before it
// places them beside the provisioner's program; a SitePublisher that is one
// has each site's files placed there too, for a provider that uploads them
// from the program. It returns an error that says so when it keeps no
// files under digest.
type ArtifactFetcher interface {
	Fetch(ctx context.Context, run Run, deployable, digest, dest string) error
}

// DestroyPreparer readies a run's resources for the provisioner's Destroy,
// which `stack destroy` calls it before (TargetSpec.PreDestroy): the
// cloudflare target empties a member's R2 buckets, which its provider
// deletes only empty (D64). An error stops the destroy before the
// provisioner runs.
type DestroyPreparer interface {
	PrepareDestroy(ctx context.Context, run Run, log io.Writer) error
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
// use: State, Bootstrap, Migrations, Builder, CI, Jobs, Sites, Bundles or
// PreDestroy without a provisioner, and Bootstrap, Migrations, Builder, CI,
// Jobs, Sites, Bundles or PreDestroy without State, which a bootstrap
// creates, a deploy records each migration and each build in, a CI job
// plans and deploys from, a job's run on demand reads the image of, and a
// destroy deletes the manifest of.
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
	if spec.Sites != nil {
		named = append(named, "Sites")
	}
	if spec.Bundles != nil {
		named = append(named, "Bundles")
	}
	if spec.PreDestroy != nil {
		named = append(named, "PreDestroy")
	}
	if len(named) > 0 && spec.Provisioner == "" {
		return fmt.Errorf("registry: target %q has %s but names no provisioner to deploy with", spec.Name, strings.Join(named, ", "))
	}
	if (spec.Bootstrap != nil || spec.Migrations != nil || spec.Builder != nil || spec.CI != nil || spec.Jobs != nil) && spec.State == nil {
		return fmt.Errorf("registry: target %q has Bootstrap, Migrations, Builder, CI or Jobs but no State: a bootstrap creates the deploy state, a deploy records each migration and each build in it, a CI job plans and deploys from it, and a job's run on demand runs the image it records", spec.Name)
	}
	if spec.Sites != nil && spec.State == nil {
		return fmt.Errorf("registry: target %q has Sites but no State: a deploy records the digest of each site's files in it (D55)", spec.Name)
	}
	if spec.Bundles != nil && spec.State == nil {
		return fmt.Errorf("registry: target %q has Bundles but no State: a deploy records the digest of each bundle's files in it, which a rollback and a plan fetch (D64)", spec.Name)
	}
	if spec.PreDestroy != nil && spec.State == nil {
		return fmt.Errorf("registry: target %q has PreDestroy but no State: only a target that deploys destroys (D64)", spec.Name)
	}
	return nil
}
