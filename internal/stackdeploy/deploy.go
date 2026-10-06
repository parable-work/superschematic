package stackdeploy

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// DeployOptions is one deploy of a run.
type DeployOptions struct {
	Options

	// Images are the images of the servers the deploy rolls out, by
	// server (ParseImages). A server without one keeps the image the
	// manifest records; a server with neither is refused.
	Images map[string]string

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
//  1. it reads the manifest of the previous deploy, pins each server's
//     image (PinImages) and plans each database's migration from the
//     schema the manifest records, then checks the plans against the gate
//     and, when given, against the plans `stack plan` showed;
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

	images := map[string]string{}
	if prev != nil {
		for server, image := range prev.Images {
			if d := s.env.Deployable(server); d != nil && d.Kind == ir.DeployableServer {
				images[server] = image
			}
		}
	}
	maps.Copy(images, o.Images)
	missing, err := checkImages(s.env, images)
	if err != nil {
		return nil, err
	}
	if len(missing) > 0 {
		return nil, fmt.Errorf("no image for server %s: the manifest records none, so pass --image <server>=<repository>@sha256:<digest> for each", strings.Join(missing, ", "))
	}
	pinned, err := PinImages(s.env, images)
	if err != nil {
		return nil, err
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

	req, err := s.request(ctx, pinned)
	if err != nil {
		return nil, err
	}
	d := &deploy{session: s, opts: o, now: now, req: req, images: images, plans: plans, pending: pending}
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
			if d.manifest.Images == nil {
				d.manifest.Images = map[string]string{}
			}
			d.manifest.Images[server] = d.images[server]
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

// migrate runs one phase of the new plans on database.
func (d *deploy) migrate(ctx context.Context, database string, phase ir.MigrationPhase) error {
	var plans []*DatabasePlan
	for _, plan := range d.plans {
		if plan.Database != database {
			continue
		}
		if plan.Steps(phase) == 0 {
			// Nothing to run: the database holds what the phase would
			// leave, so record it.
			if phase == ir.MigrationExpand || plan.ContractSteps > 0 {
				d.manifest.setApplied(database, plan.Service, plan.after(phase))
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

// runPhase runs one phase of plans on database, and records the schema it
// leaves; when the runner fails, it records each plan's phase as pending
// on the schema the database held before.
func (d *deploy) runPhase(ctx context.Context, database string, phase ir.MigrationPhase, plans []*DatabasePlan) error {
	req := registry.MigrationRequest{Run: d.run, Database: database, Phase: phase, Log: d.log}
	var names []string
	for _, plan := range plans {
		req.Plans = append(req.Plans, registry.MigrationPlan{Service: plan.Service, Plan: plan.Document})
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
		d.manifest.setApplied(database, plan.Service, plan.after(phase))
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
