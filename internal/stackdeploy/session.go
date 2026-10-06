// Package stackdeploy is the cloud half of the `stack` commands
// (docs/stack-model.md, sections 7.3, 11.1 and 11.2): bootstrap, secrets
// set, plan, deploy, destroy and outputs. It is target-neutral. It maps a
// resolved environment's deploy order onto the seams its target and
// provisioner register (internal/registry): the provisioner applies each
// step's resources, the target's migration runner runs each database's
// migration between them, its secret store holds secret values and its
// state store the deploy manifest.
//
// Each step of the deploy order is one targeted update of the
// provisioner's program: infrastructure, the migrations' expand steps, the
// servers wave by wave, callees first, the contract steps, and exposure.
// Migrations run between updates, outside the resource graph, since their
// plans depend on what the manifest records and the graph is a pure
// function of the schemas (D45).
package stackdeploy

import (
	"context"
	"fmt"
	"io"
	"maps"
	"slices"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Options is what every operation reads.
type Options struct {
	// Registry is the assembled registry, with the environment's target
	// and provisioner.
	Registry *registry.Registry

	// Run is the environment and its parameters' values.
	Run registry.Run

	// Dir is where the provisioner's program is rendered.
	Dir string

	// Log receives progress. No secret value is ever written to it.
	Log io.Writer
}

// session is one operation's view of the run: its target and provisioner.
type session struct {
	reg    *registry.Registry
	run    registry.Run
	env    *ir.ResolvedEnvironment
	target registry.TargetSpec
	prov   registry.Provisioner
	dir    string
	log    io.Writer
}

// open opens a session over a run, whose parameters it checks.
func open(o Options) (*session, error) {
	if err := o.Run.Check(); err != nil {
		return nil, err
	}
	return openEnvironment(o)
}

// openEnvironment opens a session over an environment, for an operation
// that is not a run's, such as bootstrap and `secrets set`: it reads no
// parameter values.
func openEnvironment(o Options) (*session, error) {
	if o.Registry == nil {
		return nil, fmt.Errorf("stack: no registry")
	}
	env := o.Run.Environment
	if env == nil {
		return nil, fmt.Errorf("stack: no environment")
	}
	target, ok := o.Registry.Target(env.Target)
	if !ok {
		return nil, fmt.Errorf("environment %s has target %s, which this binary does not link (registered targets: %v)", env.Environment, env.Target, o.Registry.Targets())
	}
	s := &session{reg: o.Registry, run: o.Run, env: env, target: target, dir: o.Dir, log: o.Log}
	if s.log == nil {
		s.log = io.Discard
	}
	if env.Provisioner != "" {
		spec, ok := o.Registry.Provisioner(env.Provisioner)
		if !ok {
			return nil, fmt.Errorf("environment %s is applied by provisioner %s, which this binary does not link", env.Environment, env.Provisioner)
		}
		s.prov = spec.Provisioner
	}
	return s, nil
}

// requireDeploy refuses a target that does not deploy: one with no
// provisioner or no state store.
func (s *session) requireDeploy() error {
	if s.prov == nil {
		return fmt.Errorf("target %s names no provisioner, so environment %s does not deploy", s.target.Name, s.env.Environment)
	}
	if s.target.State == nil {
		return fmt.Errorf("target %s keeps no deploy state, so environment %s does not deploy", s.target.Name, s.env.Environment)
	}
	if s.dir == "" {
		return fmt.Errorf("stack: no directory to render the provisioner's program in")
	}
	return nil
}

func (s *session) logf(format string, args ...any) {
	_, _ = fmt.Fprintf(s.log, format+"\n", args...)
}

// request renders env, which may be s.env with its images pinned, and
// returns the provisioner's request over it: the target's state backend,
// and the credentials of the environment's DNS platform.
func (s *session) request(ctx context.Context, env *ir.ResolvedEnvironment) (registry.ProvisionRequest, error) {
	backend, err := s.target.State.Backend(ctx, env)
	if err != nil {
		return registry.ProvisionRequest{}, fmt.Errorf("the state backend of %s: %w", s.run.Name(), err)
	}
	creds, err := s.credentialEnv(ctx)
	if err != nil {
		return registry.ProvisionRequest{}, err
	}
	if err := s.prov.Render(env, s.dir); err != nil {
		return registry.ProvisionRequest{}, fmt.Errorf("render the program of %s: %w", s.run.Name(), err)
	}
	return registry.ProvisionRequest{
		Environment: env,
		Parameters:  maps.Clone(s.run.Parameters),
		Dir:         s.dir,
		Backend:     backend,
		Env:         creds,
	}, nil
}

// dnsCredentials returns the credentials the environment's DNS platform
// declares.
func (s *session) dnsCredentials() (platform string, creds []registry.Credential) {
	if s.env.DNS == nil {
		return "", nil
	}
	return s.env.DNS.Platform, s.reg.Credentials(s.env.DNS.Platform)
}

// credentialEnv reads each credential of the environment's DNS platform
// that its provider reads from an environment variable.
func (s *session) credentialEnv(ctx context.Context) (map[string]string, error) {
	platform, creds := s.dnsCredentials()
	var out map[string]string
	for _, c := range creds {
		if c.Env == "" {
			continue
		}
		if s.target.Secrets == nil {
			return nil, fmt.Errorf("DNS platform %s needs credential %s, and target %s stores no secrets", platform, c.Name, s.target.Name)
		}
		value, err := s.target.Secrets.Get(ctx, s.env, registry.CredentialID(platform, c.Name))
		if err != nil {
			return nil, fmt.Errorf("read credential %s of DNS platform %s (`stack bootstrap %s` stores it): %w", c.Name, platform, s.env.Environment, err)
		}
		if out == nil {
			out = map[string]string{}
		}
		out[c.Env] = string(value)
	}
	return out, nil
}

// missingSecrets returns the environment's secrets that have no value.
func (s *session) missingSecrets(ctx context.Context) ([]*ir.StackSecret, error) {
	if len(s.env.Secrets) == 0 {
		return nil, nil
	}
	if s.target.Secrets == nil {
		return nil, fmt.Errorf("environment %s has secrets, and target %s stores none", s.env.Environment, s.target.Name)
	}
	var missing []*ir.StackSecret
	for _, secret := range s.env.Secrets {
		ok, err := s.target.Secrets.Exists(ctx, s.env, secret.ID)
		if err != nil {
			return nil, fmt.Errorf("secret %s: %w", secret.ID, err)
		}
		if !ok {
			missing = append(missing, secret)
		}
	}
	return missing, nil
}

// stepName names a step in messages and the manifest: `infrastructure`,
// `migrate expand`, `rollout 2`, `exposure`.
func stepName(step *ir.DeployStep) string {
	switch {
	case step.Step == ir.StepMigrate:
		return "migrate " + string(step.Migration)
	case step.Wave > 0:
		return fmt.Sprintf("%s %d", step.Step, step.Wave)
	}
	return string(step.Step)
}

func sortedKeys[V any](m map[string]V) []string {
	return slices.Sorted(maps.Keys(m))
}
