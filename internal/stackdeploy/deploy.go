package stackdeploy

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// DeployOptions is one deploy of a run.
type DeployOptions struct {
	Options

	// Images are the images of the servers the deploy rolls out, by
	// server (ParseImages). A server without one is built when Sources is
	// set and its context changed, and keeps the image the manifest
	// records otherwise; a server with none of them is refused.
	Images map[string]string

	// Sources, when set, says where the stack's build wrote each server's
	// Dockerfile, and the deploy builds through the target's ImageBuilder
	// the image of each server Images names none for whose context
	// changed since the image the manifest records. Nil builds nothing.
	Sources *Sources

	// Planner plans each database's migration. Required when the
	// environment has a database.
	Planner Planner

	// Services holds the IR digest of each service the stack reaches,
	// which the manifest records.
	Services map[string]string

	// Gate is the hazards the deploy refuses unless acknowledged.
	Gate Gate

	// Expected, when set, holds the plan hash of each DB service that
	// `stack plan` showed; a deploy whose plans differ is refused.
	Expected map[string]string

	// Prompter asks for the value of a secret that has none when the
	// deploy reaches its first step after infrastructure. Nil fails
	// instead, naming each secret.
	Prompter Prompter

	// Now returns the time the manifest records; time.Now when nil.
	Now func() time.Time
}

// Deploy deploys a run (docs/stack-model.md, section 11.2) and returns the
// manifest it wrote:
//
//  1. it reads the manifest of the previous deploy, decides where each
//     server's image comes from (--image, a build, the manifest) and plans
//     each database's migration from the schema the manifest records, then
//     checks the plans against the gate and, when given, against the plans
//     `stack plan` showed; then it builds the images it decided to build
//     and pins each server's image (PinImages);
//  2. it runs the deploy order a step at a time: the provisioner applies
//     the infrastructure; every secret then needs a value; the target's
//     migration runner runs the expand phase of each plan (first finishing
//     any phase a failed deploy left part-way); the provisioner rolls the
//     servers out wave by wave, callees first, each wave returning once
//     the platform reports its servers ready; the runner runs the contract
//     phases; the provisioner applies exposure;
//  3. it writes the manifest after each step, and at the end.
//
// A step that fails stops the deploy, and the manifest records the
// failure. A rollout that fails runs no contract step, so the servers of
// the previous version keep the expanded schema, and the manifest records
// the model between the phases: the next deploy plans from it, and its
// plan supersedes the pending contract (D27, amended). A migration phase
// that fails is recorded as pending, and the next deploy finishes it
// before anything else migrates, as the runner requires.
func Deploy(ctx context.Context, o DeployOptions) (*Manifest, error) {
	s, err := open(o.Options)
	if err != nil {
		return nil, err
	}
	if err := s.requireDeploy(); err != nil {
		return nil, err
	}
	now := o.Now
	if now == nil {
		now = time.Now
	}
	prev, err := readManifest(ctx, s.target.State, s.run)
	if err != nil {
		return nil, err
	}

	if _, err := checkImages(s.env, o.Images); err != nil {
		return nil, err
	}
	images, err := s.planImages(prev, o.Images, o.Sources, nil, false)
	defer images.cleanup()
	if err != nil {
		return nil, err
	}
	missing, err := checkImages(s.env, images.wanted())
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, errors.New(describeMissing(missing, o.Sources != nil && s.target.Builder != nil))
	}

	plans, pending, err := planMigrations(s.env, prev, o.Planner)
	if err != nil {
		return nil, err
	}
	if hazards := o.Gate.unallowed(plans); len(hazards) > 0 {
		return nil, &HazardsError{Hazards: hazards}
	}
	if err := checkExpected(plans, o.Expected); err != nil {
		return nil, err
	}
	if s.target.Migrations == nil && (len(pending) > 0 || hasSteps(plans)) {
		return nil, fmt.Errorf("environment %s has migrations to run, and target %s has no migration runner", s.env.Environment, s.target.Name)
	}

	if err := s.runBuilds(ctx, images); err != nil {
		return nil, err
	}
	pinned, err := PinImages(s.env, images.images)
	if err != nil {
		return nil, err
	}
	req, err := s.request(ctx, pinned)
	if err != nil {
		return nil, err
	}
	d := &deploy{session: s, opts: o, now: now, req: req, images: images.images, contexts: images.contexts, plans: plans, pending: pending}
	d.manifest = nextManifest(prev, s.run, o.Services)
	if err := d.checkpoint(ctx, ""); err != nil {
		return nil, err
	}
	for i, step := range s.env.DeployOrder {
		if step.Step != ir.StepInfrastructure && !d.secretsChecked {
			if err := d.ensureSecrets(ctx); err != nil {
				return d.fail(ctx, step, err)
			}
		}
		s.logf("step %d/%d: %s", i+1, len(s.env.DeployOrder), stepName(step))
		if err := d.step(ctx, step); err != nil {
			return d.fail(ctx, step, err)
		}
		if err := d.checkpoint(ctx, stepName(step)); err != nil {
			return nil, err
		}
	}
	if !d.secretsChecked {
		if err := d.ensureSecrets(ctx); err != nil {
			return d.fail(ctx, nil, err)
		}
	}
	d.manifest.Status = StatusDeployed
	if err := d.write(ctx); err != nil {
		return nil, err
	}
	s.logf("deployed %s", s.run.Name())
	return d.manifest, nil
}

// deploy is one Deploy under way.
type deploy struct {
	*session
	opts     DeployOptions
	now      func() time.Time
	req      registry.ProvisionRequest
	images   map[string]string
	contexts map[string]string
	plans    []*DatabasePlan
	pending  map[string][]*PendingMigration
	manifest *Manifest

	secretsChecked bool
}

func hasSteps(plans []*DatabasePlan) bool {
	for _, plan := range plans {
		if plan.ExpandSteps+plan.ContractSteps > 0 {
			return true
		}
	}
	return false
}

// step runs one step of the deploy order.
func (d *deploy) step(ctx context.Context, step *ir.DeployStep) error {
	switch step.Step {
	case ir.StepInfrastructure, ir.StepExposure:
		return d.prov.Apply(ctx, d.req, *step)
	case ir.StepRollout:
		if err := d.prov.Apply(ctx, d.req, *step); err != nil {
			return err
		}
		for _, server := range step.Deployables {
			d.manifest.setImage(server, d.images[server], d.contexts[server])
		}
		return nil
	case ir.StepMigrate:
		for _, database := range step.Deployables {
			if step.Migration == ir.MigrationExpand {
				if err := d.finishPending(ctx, database); err != nil {
					return err
				}
			}
			if err := d.migrate(ctx, database, step.Migration); err != nil {
				return err
			}
		}
		return nil
	}
	return fmt.Errorf("the deploy order has a step of kind %q", step.Step)
}

// finishPending runs, on database, each phase a failed deploy left
// part-way, expand phases first, and records the schema each leaves.
func (d *deploy) finishPending(ctx context.Context, database string) error {
	for _, phase := range []ir.MigrationPhase{ir.MigrationExpand, ir.MigrationContract} {
		var plans []*DatabasePlan
		for _, p := range d.pending[database] {
			if p.Phase == phase {
				plans = append(plans, p.Plan)
			}
		}
		if len(plans) == 0 {
			continue
		}
		d.logf("  finish the %s phase a previous deploy left part-way on %s", phase, database)
		if err := d.runPhase(ctx, database, phase, plans); err != nil {
			return fmt.Errorf("finish the pending %s phase on %s: %w", phase, database, err)
		}
	}
	return nil
}

// migrate runs one phase of the new plans on database: each plan with
// steps in the phase, and in the expand phase each plan whose connecting
// servers changed since the runner last ran on its DB service, so the
// runner can give a new server its privileges before the server rolls out
// and take a removed one's back before its database user goes (D46).
func (d *deploy) migrate(ctx context.Context, database string, phase ir.MigrationPhase) error {
	var plans []*DatabasePlan
	for _, plan := range d.plans {
		if plan.Database != database {
			continue
		}
		var servers []string
		if prev := d.manifest.applied(database, plan.Service); prev != nil {
			servers = prev.Servers
		}
		changed := d.target.Migrations != nil && phase == ir.MigrationExpand &&
			!slices.Equal(servers, connecting(d.env, database, plan.Service))
		if plan.Steps(phase) == 0 && !changed {
			// Nothing to run: the database holds what the phase would
			// leave, so record it.
			if phase == ir.MigrationExpand || plan.ContractSteps > 0 {
				applied := plan.after(phase)
				applied.Servers = servers
				d.manifest.setApplied(database, plan.Service, applied)
			}
			continue
		}
		plans = append(plans, plan)
	}
	if len(plans) == 0 {
		return nil
	}
	return d.runPhase(ctx, database, phase, plans)
}

// connecting returns the servers whose sql edges reach service on
// database, sorted.
func connecting(env *ir.ResolvedEnvironment, database, service string) []string {
	var out []string
	for _, e := range env.Edges {
		if e.Kind == ir.EdgeSQL && e.To == database && e.Service.Name == service && !slices.Contains(out, e.From) {
			out = append(out, e.From)
		}
	}
	slices.Sort(out)
	return out
}

// runPhase runs one phase of plans on database, and records the schema it
// leaves and the servers the runner saw connect; when the runner fails,
// it records each plan's phase as pending on the schema the database
// held before.
func (d *deploy) runPhase(ctx context.Context, database string, phase ir.MigrationPhase, plans []*DatabasePlan) error {
	req := registry.MigrationRequest{Run: d.run, Database: database, Phase: phase, Log: d.log}
	var names []string
	for _, plan := range plans {
		req.Plans = append(req.Plans, registry.MigrationPlan{
			Service: plan.Service,
			Plan:    plan.Document,
			Steps:   plan.Steps(phase),
			Servers: connecting(d.env, database, plan.Service),
		})
		names = append(names, plan.Service)
	}
	d.logf("  migrate %s: %s phase of %s", database, phase, strings.Join(names, ", "))
	if err := d.target.Migrations.Migrate(ctx, req); err != nil {
		for _, plan := range plans {
			applied := d.manifest.applied(database, plan.Service)
			if applied == nil {
				// An empty database: what the plan starts from.
				applied = &AppliedSchema{Dialect: plan.Dialect}
			} else {
				copied := *applied
				applied = &copied
			}
			applied.Pending = &PendingMigration{Phase: phase, Plan: plan}
			d.manifest.setApplied(database, plan.Service, applied)
		}
		return err
	}
	for _, plan := range plans {
		applied := plan.after(phase)
		applied.Servers = connecting(d.env, database, plan.Service)
		d.manifest.setApplied(database, plan.Service, applied)
	}
	return nil
}

// ensureSecrets checks, once, that every secret of the environment has a
// value, asking the prompter for each one that has none.
func (d *deploy) ensureSecrets(ctx context.Context) error {
	d.secretsChecked = true
	missing, err := d.missingSecrets(ctx)
	if err != nil {
		return err
	}
	if len(missing) == 0 {
		return nil
	}
	if d.opts.Prompter == nil {
		var ids []string
		for _, secret := range missing {
			ids = append(ids, secret.ID)
		}
		return fmt.Errorf("secret %s has no value: run `stack secrets set %s`, then deploy again", strings.Join(ids, ", "), d.env.Environment)
	}
	for _, secret := range missing {
		if err := promptSecret(ctx, d.session, d.opts.Prompter, secret.ID, secretPrompt(secret)); err != nil {
			return err
		}
	}
	return nil
}

// checkpoint records that the deploy finished a step.
func (d *deploy) checkpoint(ctx context.Context, step string) error {
	if step != "" {
		d.manifest.Step = step
	}
	return d.write(ctx)
}

func (d *deploy) write(ctx context.Context) error {
	d.manifest.Time = d.now().UTC()
	data, err := d.manifest.Marshal()
	if err != nil {
		return err
	}
	if err := d.target.State.WriteManifest(ctx, d.run, data); err != nil {
		return fmt.Errorf("write the deploy manifest of %s: %w", d.run.Name(), err)
	}
	return nil
}

// fail records a failed step in the manifest and returns the error.
func (d *deploy) fail(ctx context.Context, step *ir.DeployStep, err error) (*Manifest, error) {
	name := "secrets"
	if step != nil {
		name = stepName(step)
	}
	err = fmt.Errorf("deploy %s, step %s: %w", d.run.Name(), name, err)
	d.manifest.Status = StatusFailed
	d.manifest.Step = name
	d.manifest.Error = err.Error()
	if werr := d.write(ctx); werr != nil {
		return d.manifest, errors.Join(err, werr)
	}
	return d.manifest, err
}
