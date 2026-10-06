package stackdeploy

import (
	"context"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// PlanOptions is one plan of a run.
type PlanOptions struct {
	Options

	// Images, Planner and Gate are a deploy's (DeployOptions).
	Images  map[string]string
	Planner Planner
	Gate    Gate
}

// PlanResult is what a deploy of the run would do.
type PlanResult struct {
	// Run names the run.
	Run string `json:"run"`

	// Changes are the provisioner's plan of the whole program.
	Changes []registry.PlannedChange `json:"changes"`

	// Databases are the migration plans, from the schema the manifest
	// records, by database and then service.
	Databases []*DatabasePlan `json:"databases,omitempty"`

	// Pending are the migration phases a failed deploy left part-way,
	// which the deploy finishes first.
	Pending []PendingNote `json:"pending,omitempty"`

	// Unallowed are the plans' hazards the gate would stop the deploy on.
	Unallowed []Hazard `json:"unallowed,omitempty"`

	// Unpinned are the servers with no image yet, planned at their
	// repository with no digest.
	Unpinned []string `json:"unpinned,omitempty"`

	// MissingSecrets are the secrets with no value.
	MissingSecrets []string `json:"missingSecrets,omitempty"`

	// ManualRecords are the DNS records to create by hand, when the
	// environment's domain has no DNS platform (section 6.9).
	ManualRecords []*ir.DNSRecord `json:"manualRecords,omitempty"`
}

// PendingNote names a migration phase a failed deploy left part-way.
type PendingNote struct {
	Database string            `json:"database"`
	Service  string            `json:"service"`
	Phase    ir.MigrationPhase `json:"phase"`
	Plan     string            `json:"plan"`
}

// Expected returns each DB service's plan hash, which a deploy checks it
// runs (DeployOptions.Expected).
func (r *PlanResult) Expected() map[string]string {
	out := map[string]string{}
	for _, plan := range r.Databases {
		out[plan.Service] = plan.Hash
	}
	return out
}

// Plan returns what a deploy of the run would do, changing nothing: the
// provisioner's plan of the whole program with the images the manifest
// records and Images pinned, and the migration plan of each database from
// the schema the manifest records (docs/stack-model.md, sections 10 and
// 11.2). A cloud preview with the read-only planner account runs it.
func Plan(ctx context.Context, o PlanOptions) (*PlanResult, error) {
	s, err := open(o.Options)
	if err != nil {
		return nil, err
	}
	if err := s.requireDeploy(); err != nil {
		return nil, err
	}
	prev, err := readManifest(ctx, s.target.State, s.run)
	if err != nil {
		return nil, err
	}
	images := map[string]string{}
	if prev != nil {
		for server, image := range prev.Images {
			if d := s.env.Deployable(server); d != nil && d.Kind == ir.DeployableServer {
				images[server] = image
			}
		}
	}
	maps.Copy(images, o.Images)
	unpinned, err := checkImages(s.env, images)
	if err != nil {
		return nil, err
	}
	pinned, err := PinImages(s.env, images)
	if err != nil {
		return nil, err
	}
	plans, pending, err := planMigrations(s.env, prev, o.Planner)
	if err != nil {
		return nil, err
	}
	out := &PlanResult{Run: s.run.Name(), Databases: plans, Unpinned: unpinned, Unallowed: o.Gate.unallowed(plans)}
	for _, database := range sortedKeys(pending) {
		for _, p := range pending[database] {
			out.Pending = append(out.Pending, PendingNote{Database: database, Service: p.Plan.Service, Phase: p.Phase, Plan: p.Plan.Hash})
		}
	}
	missing, err := s.missingSecrets(ctx)
	if err != nil {
		return nil, err
	}
	for _, secret := range missing {
		out.MissingSecrets = append(out.MissingSecrets, secret.ID)
	}
	if s.env.DNS != nil && s.env.DNS.Platform == ir.ManualDNS {
		out.ManualRecords = s.env.DNS.Records
	}
	req, err := s.request(ctx, pinned)
	if err != nil {
		return nil, err
	}
	if out.Changes, err = s.prov.Plan(ctx, req); err != nil {
		return nil, err
	}
	return out, nil
}

// Destroy removes every resource of the run and its deploy manifest.
func Destroy(ctx context.Context, o Options) error {
	s, err := open(o)
	if err != nil {
		return err
	}
	if err := s.requireDeploy(); err != nil {
		return err
	}
	req, err := s.deployedRequest(ctx)
	if err != nil {
		return err
	}
	s.logf("destroy %s", s.run.Name())
	if err := s.prov.Destroy(ctx, req); err != nil {
		return err
	}
	if err := s.target.State.DeleteManifest(ctx, s.run); err != nil {
		return fmt.Errorf("delete the deploy manifest of %s: %w", s.run.Name(), err)
	}
	return nil
}

// Outputs returns the outputs of the run's applied resources, by node ID
// and output name: what the bindings generator reads as outputs.json
// (section 6.6).
func Outputs(ctx context.Context, o Options) (map[string]map[string]any, error) {
	s, err := open(o)
	if err != nil {
		return nil, err
	}
	if err := s.requireDeploy(); err != nil {
		return nil, err
	}
	req, err := s.deployedRequest(ctx)
	if err != nil {
		return nil, err
	}
	return s.prov.Outputs(ctx, req)
}

// deployedRequest is the provisioner's request over the environment with
// the images the manifest records pinned, so the program it renders is
// the one the last deploy applied.
func (s *session) deployedRequest(ctx context.Context) (registry.ProvisionRequest, error) {
	prev, err := readManifest(ctx, s.target.State, s.run)
	if err != nil {
		return registry.ProvisionRequest{}, err
	}
	env := s.env
	if prev != nil {
		images := map[string]string{}
		for server, image := range prev.Images {
			if d := s.env.Deployable(server); d != nil && d.Kind == ir.DeployableServer {
				images[server] = image
			}
		}
		if env, err = PinImages(s.env, images); err != nil {
			return registry.ProvisionRequest{}, err
		}
	}
	return s.request(ctx, env)
}

// BootstrapOptions is one bootstrap of the cloud project an environment
// deploys to.
type BootstrapOptions struct {
	Options

	// Repository is the repository the generated CI runs in, read from
	// the git remote (`acme/shop`).
	Repository string

	// Prompter asks for each credential of the environment that has no
	// value.
	Prompter Prompter
}

// Bootstrap prepares the cloud project an environment deploys to
// (docs/stack-model.md, section 7.3): the target's bootstrap, which also
// creates the storage of each platform credential, then a value for each
// credential the secret store lacks. Both halves are idempotent, so it is
// safe to run again; a credential that has a value is not asked for, and
// one that environments share is asked for once.
func Bootstrap(ctx context.Context, o BootstrapOptions) error {
	s, err := openEnvironment(o.Options)
	if err != nil {
		return err
	}
	creds := s.credentials()
	if len(creds) > 0 && s.target.Secrets == nil {
		return fmt.Errorf("environment %s needs credentials, and target %s stores no secrets", s.env.Environment, s.target.Name)
	}
	if s.target.Bootstrap != nil {
		if err := s.requireDeploy(); err != nil {
			return err
		}
		s.logf("bootstrap target %s for environment %s", s.target.Name, s.env.Environment)
		if err := s.target.Bootstrap.Bootstrap(ctx, registry.BootstrapRequest{
			Environment: s.env,
			Repository:  o.Repository,
			Credentials: creds,
			Provisioner: s.prov,
			Dir:         s.dir,
			Log:         s.log,
		}); err != nil {
			return fmt.Errorf("bootstrap %s: %w", s.env.Environment, err)
		}
	} else {
		s.logf("target %s needs no bootstrap", s.target.Name)
	}
	for _, c := range creds {
		ok, err := s.target.Secrets.Exists(ctx, s.env, c.Secret)
		if err != nil {
			return fmt.Errorf("credential %s: %w", c.Secret, err)
		}
		if ok {
			s.logf("credential %s has a value", c.Secret)
			continue
		}
		if o.Prompter == nil {
			return fmt.Errorf("credential %s has no value, and no terminal to ask for it on: run bootstrap at a terminal", c.Secret)
		}
		if err := promptSecret(ctx, s, o.Prompter, c.Secret, credentialPrompt(c)); err != nil {
			return err
		}
	}
	return nil
}

// SecretsOptions is one `stack secrets set`.
type SecretsOptions struct {
	Options

	// Only names the one secret to set, by its ID: a StackSecret's
	// (`PaymentsSecrets.STRIPE_KEY`) or a credential's secret. Empty sets
	// every secret and credential that has no value.
	Only string

	// Prompter asks for each value.
	Prompter Prompter
}

// SetSecrets asks for and stores secret values (docs/stack-model.md,
// section 4.2): the one Only names, which it asks for whether or not it
// has a value, or every secret and credential of the environment that has
// none. It returns the IDs it stored. A
// value goes from the prompter to the target's secret store and nowhere
// else.
func SetSecrets(ctx context.Context, o SecretsOptions) ([]string, error) {
	s, err := openEnvironment(o.Options)
	if err != nil {
		return nil, err
	}
	if s.target.Secrets == nil {
		return nil, fmt.Errorf("target %s stores no secrets", s.target.Name)
	}
	if o.Prompter == nil {
		return nil, fmt.Errorf("no terminal to ask for secret values on")
	}
	type entry struct{ id, prompt string }
	var all []entry
	for _, secret := range s.env.Secrets {
		all = append(all, entry{secret.ID, secretPrompt(secret)})
	}
	for _, c := range s.credentials() {
		all = append(all, entry{c.Secret, credentialPrompt(c)})
	}
	var todo []entry
	if o.Only != "" {
		i := slices.IndexFunc(all, func(e entry) bool { return e.id == o.Only })
		if i < 0 {
			var ids []string
			for _, e := range all {
				ids = append(ids, e.id)
			}
			if len(ids) == 0 {
				return nil, fmt.Errorf("environment %s has no secret %s; it has no secrets", s.env.Environment, o.Only)
			}
			return nil, fmt.Errorf("environment %s has no secret %s (its secrets: %s)", s.env.Environment, o.Only, strings.Join(ids, ", "))
		}
		todo = []entry{all[i]}
	} else {
		for _, e := range all {
			ok, err := s.target.Secrets.Exists(ctx, s.env, e.id)
			if err != nil {
				return nil, fmt.Errorf("secret %s: %w", e.id, err)
			}
			if !ok {
				todo = append(todo, e)
			}
		}
		if len(todo) == 0 {
			s.logf("every secret of %s has a value; name one to replace it", s.env.Environment)
		}
	}
	var stored []string
	for _, e := range todo {
		if err := promptSecret(ctx, s, o.Prompter, e.id, e.prompt); err != nil {
			return stored, err
		}
		stored = append(stored, e.id)
	}
	return stored, nil
}
