package registry

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"regexp"
	"sort"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The seams the cloud half of the `stack` commands drives
// (docs/stack-model.md, sections 7.3, 11.1 and 11.2): a target keeps its
// deploy state, stores secret values, bootstraps a cloud project and runs
// migration plans on its databases, and a DNS platform declares the
// credentials it needs. Each is an interface a TargetSpec or a
// DNSPlatformSpec carries; the core's deploy (internal/stackdeploy) calls
// them, and none of them knows the others.

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

// SecretStore keeps the values of an environment's secrets, keyed by the
// secret's identity: a StackSecret's ID (`PaymentsSecrets.STRIPE_KEY`,
// section 4.2), or a platform credential's (CredentialID). The store
// names where each value lives; Secret Manager on gcp. A value goes in
// and is never written to a file or a log.
type SecretStore interface {
	// Exists reports whether the secret has a value.
	Exists(ctx context.Context, env *ir.ResolvedEnvironment, id string) (bool, error)

	// List returns the IDs, sorted, of env's secrets and of its DNS
	// platform's credentials that have a value.
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

	// Credentials are the IDs (CredentialID) of the credentials the
	// environment's DNS platform declares. Bootstrap creates their
	// storage, readable by the accounts that deploy; the core then asks
	// for each value that is missing and stores it.
	Credentials []string

	// Provisioner applies what the bootstrap provisions through it, a
	// program it renders under Dir.
	Provisioner Provisioner
	Dir         string

	// Log receives progress. Nothing secret is written to it.
	Log io.Writer
}

// Bootstrapper prepares the cloud project an environment deploys to, once,
// with an owner's credentials (section 7.3). It is idempotent: a second
// run changes nothing that the first one made.
type Bootstrapper interface {
	Bootstrap(ctx context.Context, req BootstrapRequest) error
}

// MigrationPlan is one DB service's migration plan, as the plan document
// `superschematic migrate plan --out` writes (D27): the runner applies it
// and never computes one.
type MigrationPlan struct {
	// Service is the DB service the plan migrates.
	Service string

	// Plan is the plan document.
	Plan json.RawMessage
}

// MigrationRequest runs one phase of the migration plans of one database:
// the plan of each DB service it hosts that has steps in the phase.
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

// Credential is a secret a DNS platform needs to write records, entered at
// bootstrap: the Cloudflare DNS platform's API token (section 6.9).
type Credential struct {
	// Name names the credential in upper snake case (`API_TOKEN`).
	Name string

	// Description says what to enter; bootstrap prompts with it.
	Description string

	// Env is the environment variable the provisioner's provider reads
	// the credential from (`CLOUDFLARE_API_TOKEN`). A deploy reads the
	// value from the target's secret store and hands it to the
	// provisioner there. Empty when the provider reads it otherwise.
	Env string
}

// CredentialID is the identity a credential's value is stored under:
// the DNS platform and the credential's name (`cloudflare.dns:API_TOKEN`).
// It cannot be a StackSecret's ID, which holds no colon.
func CredentialID(platform, name string) string { return platform + ":" + name }

var (
	credentialNamePattern = regexp.MustCompile(`^[A-Z][A-Z0-9_]*$`)
	envNamePattern        = regexp.MustCompile(`^[A-Z_][A-Z0-9_]*$`)
)

// checkCredentials refuses a DNS platform's credential with a name that is
// not upper snake case or repeats, no description, or an Env that is not
// an environment variable's name.
func checkCredentials(platform string, creds []Credential) error {
	seen := map[string]bool{}
	for _, c := range creds {
		if !credentialNamePattern.MatchString(c.Name) {
			return fmt.Errorf("registry: DNS platform %q declares credential %q; a credential's name is upper snake case (API_TOKEN)", platform, c.Name)
		}
		if seen[c.Name] {
			return fmt.Errorf("registry: DNS platform %q declares credential %s twice", platform, c.Name)
		}
		seen[c.Name] = true
		if strings.TrimSpace(c.Description) == "" {
			return fmt.Errorf("registry: DNS platform %q credential %s has no description to prompt with", platform, c.Name)
		}
		if c.Env != "" && !envNamePattern.MatchString(c.Env) {
			return fmt.Errorf("registry: DNS platform %q credential %s names environment variable %q, which is not one", platform, c.Name, c.Env)
		}
	}
	return nil
}

// checkDeploySeams refuses a target that carries a deploy seam it cannot
// use: State, Bootstrap or Migrations without a provisioner, and
// Bootstrap or Migrations without State, which a bootstrap creates and a
// deploy records each migration in.
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
	if len(named) > 0 && spec.Provisioner == "" {
		return fmt.Errorf("registry: target %q has %s but names no provisioner to deploy with", spec.Name, strings.Join(named, ", "))
	}
	if (spec.Bootstrap != nil || spec.Migrations != nil) && spec.State == nil {
		return fmt.Errorf("registry: target %q has Bootstrap or Migrations but no State: a bootstrap creates the deploy state, and a deploy records each migration in it", spec.Name)
	}
	return nil
}

// Credentials returns the credentials the DNS platform named name
// declares, sorted by name, or none for an unknown platform or
// ir.ManualDNS.
func (r *Registry) Credentials(name string) []Credential {
	spec, ok := r.stack.dnsPlatforms[name]
	if !ok {
		return nil
	}
	out := append([]Credential(nil), spec.Credentials...)
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}
