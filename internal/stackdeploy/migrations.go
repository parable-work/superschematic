package stackdeploy

import (
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// A deploy plans each database's migration offline, from the model the
// deploy manifest records to the schema now (docs/stack-model.md, section
// 8.4, and D27). The runner applies the plan's expand steps before the
// rollout and its contract steps after it.

// Hazard is one hazard of a plan's step (D27).
type Hazard struct {
	ID     string `json:"id"`
	Class  string `json:"class"`
	Reason string `json:"reason"`
}

// DatabasePlan is the migration plan of one DB service, as the deploy
// reads it. PlanOf makes one from a sqlmigrate plan; a test may write one
// by hand, since only Document reaches the runner.
type DatabasePlan struct {
	// Database is the database deployable that hosts Service; the deploy
	// sets it.
	Database string `json:"database"`
	Service  string `json:"service"`
	Dialect  string `json:"dialect"`

	// From is the hash of the model the plan starts from, empty for an
	// empty database; To is the hash of the model it ends at, and ToModel
	// that model's canonical JSON. Expanded and ExpandedModel are the
	// model between the phases, set when the plan has contract steps.
	From          string          `json:"from,omitempty"`
	To            string          `json:"to"`
	ToModel       json.RawMessage `json:"toModel"`
	Expanded      string          `json:"expanded,omitempty"`
	ExpandedModel json.RawMessage `json:"expandedModel,omitempty"`

	// ExpandSteps and ContractSteps count the steps of each phase.
	ExpandSteps   int `json:"expandSteps"`
	ContractSteps int `json:"contractSteps"`

	// Hazards are the hazards of every step, in step order.
	Hazards []Hazard `json:"hazards,omitempty"`

	// Hash is the plan's hash, and Document the plan the runner applies.
	Hash     string          `json:"hash"`
	Document json.RawMessage `json:"document"`

	// Text is the plan for a person to read; `stack plan` prints it.
	Text string `json:"-"`
}

// Steps returns the number of the plan's steps in phase.
func (p *DatabasePlan) Steps(phase ir.MigrationPhase) int {
	if phase == ir.MigrationContract {
		return p.ContractSteps
	}
	return p.ExpandSteps
}

// after returns the schema the database holds once phase of the plan has
// run: the model between the phases after expand when the plan has
// contract steps, and the plan's last model otherwise.
func (p *DatabasePlan) after(phase ir.MigrationPhase) *AppliedSchema {
	if phase == ir.MigrationExpand && p.ContractSteps > 0 {
		return &AppliedSchema{Dialect: p.Dialect, Hash: p.Expanded, Model: p.ExpandedModel}
	}
	return &AppliedSchema{Dialect: p.Dialect, Hash: p.To, Model: p.ToModel}
}

// check refuses a plan the deploy cannot run or record.
func (p *DatabasePlan) check() error {
	switch {
	case p.Service == "":
		return fmt.Errorf("a migration plan names no service")
	case p.To == "" || len(p.ToModel) == 0:
		return fmt.Errorf("the migration plan of %s has no model it ends at", p.Service)
	case p.ContractSteps > 0 && (p.Expanded == "" || len(p.ExpandedModel) == 0):
		return fmt.Errorf("the migration plan of %s has contract steps but no model between its phases", p.Service)
	case p.ExpandSteps+p.ContractSteps > 0 && len(p.Document) == 0:
		return fmt.Errorf("the migration plan of %s has steps but no document for the runner", p.Service)
	}
	return nil
}

// PlanOf reads a sqlmigrate plan as the deploy reads it, with its
// Markdown as the text a person reads.
func PlanOf(plan *sqlmigrate.Plan) (*DatabasePlan, error) {
	if plan.Hash == "" {
		if err := plan.Seal(); err != nil {
			return nil, err
		}
	}
	doc, err := plan.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	out := &DatabasePlan{
		Service:       plan.Service,
		Dialect:       string(plan.Dialect),
		From:          plan.From,
		To:            plan.To,
		ToModel:       plan.ToModel,
		Expanded:      plan.Expanded,
		ExpandedModel: plan.ExpandedModel,
		Hash:          plan.Hash,
		Document:      doc,
		Text:          plan.Markdown(),
	}
	for _, step := range plan.Steps {
		if step.Phase == sqlmigrate.Contract {
			out.ContractSteps++
		} else {
			out.ExpandSteps++
		}
	}
	for _, h := range plan.Hazards() {
		out.Hazards = append(out.Hazards, Hazard{ID: h.ID, Class: string(h.Class), Reason: h.Reason})
	}
	return out, nil
}

// Planner plans the migration of a DB service's database in dialect, from
// the model a deploy recorded (nil for an empty database) to the schema
// now. The CLI's planner loads the schemas and calls sqlmigrate.Diff, with
// the readers of the schemas root as the readers after the rollout.
type Planner func(service, dialect string, from json.RawMessage) (*DatabasePlan, error)

// planMigrations plans the migration of every DB service each database of
// env hosts, from what prev records. A plan with a pending phase plans
// from the model that phase ends at, since the deploy finishes it first.
// It returns the plans in database and service order, and the pending
// migrations by database.
func planMigrations(env *ir.ResolvedEnvironment, prev *Manifest, planner Planner) ([]*DatabasePlan, map[string][]*PendingMigration, error) {
	var plans []*DatabasePlan
	pending := map[string][]*PendingMigration{}
	for _, d := range env.Deployables {
		if d.Kind != ir.DeployableDatabase {
			continue
		}
		if planner == nil {
			return nil, nil, fmt.Errorf("database %s: the deploy has no migration planner", d.Name)
		}
		for _, svc := range d.Services {
			var from json.RawMessage
			if applied := prev.applied(d.Name, svc.Name); applied != nil {
				if applied.Dialect != "" && applied.Dialect != d.Dialect {
					return nil, nil, fmt.Errorf("database %s: the manifest records %s's schema in %s, and the environment runs %s", d.Name, svc.Name, applied.Dialect, d.Dialect)
				}
				from = applied.Model
				if p := applied.Pending; p != nil {
					pending[d.Name] = append(pending[d.Name], p)
					from = p.Plan.after(p.Phase).Model
				}
			}
			plan, err := planner(svc.Name, d.Dialect, from)
			if err != nil {
				return nil, nil, fmt.Errorf("plan the migration of %s on database %s: %w", svc.Name, d.Name, err)
			}
			if plan.Service != svc.Name {
				return nil, nil, fmt.Errorf("the planner planned %s for service %s", plan.Service, svc.Name)
			}
			if err := plan.check(); err != nil {
				return nil, nil, err
			}
			plan.Database = d.Name
			plans = append(plans, plan)
		}
	}
	return plans, pending, nil
}

// Gate is the hazards a deploy refuses unless each is acknowledged
// (docs/stack-model.md, section 8.4): those of the classes in FailOn whose
// IDs Allow does not list. A nil FailOn is every class.
type Gate struct {
	FailOn []string
	Allow  []string
}

// unallowed returns the hazards of plans the gate stops on.
func (g Gate) unallowed(plans []*DatabasePlan) []Hazard {
	failOn := g.FailOn
	if failOn == nil {
		for _, class := range sqlmigrate.HazardClasses {
			failOn = append(failOn, string(class))
		}
	}
	var out []Hazard
	for _, plan := range plans {
		for _, h := range plan.Hazards {
			if slices.Contains(failOn, h.Class) && !slices.Contains(g.Allow, h.ID) {
				out = append(out, h)
			}
		}
	}
	return out
}

// HazardsError is a deploy refused by its gate.
type HazardsError struct {
	Hazards []Hazard
}

func (e *HazardsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the migration plans have %d hazard(s) no --allow acknowledges:", len(e.Hazards))
	for _, h := range e.Hazards {
		fmt.Fprintf(&b, "\n  %s\n    %s", h.ID, h.Reason)
	}
	b.WriteString("\nReview each, then acknowledge it with --allow '<id>'.")
	return b.String()
}

// checkExpected refuses plans that are not those `stack plan` showed:
// expected holds each DB service's plan hash.
func checkExpected(plans []*DatabasePlan, expected map[string]string) error {
	if expected == nil {
		return nil
	}
	var problems []string
	seen := map[string]bool{}
	for _, plan := range plans {
		seen[plan.Service] = true
		want, ok := expected[plan.Service]
		switch {
		case !ok:
			problems = append(problems, fmt.Sprintf("%s has plan %s, which the expected plans lack", plan.Service, plan.Hash))
		case want != plan.Hash:
			problems = append(problems, fmt.Sprintf("%s has plan %s, not the expected %s", plan.Service, plan.Hash, want))
		}
	}
	for _, service := range sortedKeys(expected) {
		if !seen[service] {
			problems = append(problems, fmt.Sprintf("the expected plans have %s, which this deploy does not migrate", service))
		}
	}
	if len(problems) > 0 {
		return fmt.Errorf("the migration plans are not the ones the plan showed; plan again:\n  %s", strings.Join(problems, "\n  "))
	}
	return nil
}
