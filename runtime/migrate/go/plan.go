package migrate

import (
	"encoding/json"
	"fmt"
)

// PlanVersion is the only plan version this runner reads.
const PlanVersion = 1

// Dialect names the database a plan, a model and a driver are for.
type Dialect string

const (
	Postgres Dialect = "postgres"
	SQLite   Dialect = "sqlite"
)

// Phase is when a step runs relative to the rollout of the new servers.
type Phase string

const (
	// Expand steps run before the rollout.
	Expand Phase = "expand"
	// Contract steps run after it.
	Contract Phase = "contract"
	// All asks Apply for both phases. No step has it.
	All Phase = "all"
)

// Plan is a plan document as the runner reads it (runtime/migrate/README.md).
// Read one with ReadPlan, which checks it.
type Plan struct {
	Version int     `json:"version"`
	Dialect Dialect `json:"dialect"`
	Service string  `json:"service"`
	// From is the hash of the model the plan starts from; empty means an
	// empty database. To is the hash of the model it ends at.
	From    string          `json:"from"`
	To      string          `json:"to"`
	ToModel json.RawMessage `json:"toModel"`
	Renames []string        `json:"renames"`
	Steps   []*Step         `json:"steps"`
	Hash    string          `json:"hash"`

	model *Model
}

// Step is one step of a plan.
type Step struct {
	Index          int       `json:"index"`
	Phase          Phase     `json:"phase"`
	Op             string    `json:"op"`
	Subject        string    `json:"subject"`
	Statements     []string  `json:"statements"`
	Transactional  bool      `json:"transactional"`
	Recovery       []string  `json:"recovery"`
	ForeignKeysOff bool      `json:"foreignKeysOff"`
	Hazards        []*Hazard `json:"hazards"`
}

// Hazard is one hazard of a step. The runner does not read it.
type Hazard struct {
	ID      string `json:"id"`
	Class   string `json:"class"`
	Subject string `json:"subject"`
	Reader  string `json:"reader"`
	Reason  string `json:"reason"`
}

// Model is a model document as the runner reads it: the members that key a
// state row and the canonical JSON the row stores. The runner never reads
// the rest of a model.
type Model struct {
	Service   string
	Dialect   Dialect
	Canonical []byte
	Hash      string
}

// Model returns the plan's toModel. It is nil for a plan ReadPlan did not
// return.
func (p *Plan) Model() *Model { return p.model }

// ReadPlan reads a plan document in any JSON encoding and checks it before
// anything runs: its version, its hash against its content, its to against
// the hash of its toModel, and the shape of its steps.
func ReadPlan(doc []byte) (*Plan, error) {
	value, err := decode(doc)
	if err != nil {
		return nil, fmt.Errorf("read the plan: %w", err)
	}
	object, ok := value.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("read the plan: a plan is a JSON object")
	}
	// The version comes first: a later version may change any other member.
	version, ok := object["version"].(json.Number)
	if !ok {
		return nil, refusef("the plan has no version")
	}
	if version.String() != "1" {
		return nil, refusef("the plan is version %s; this runner reads version %d", version, PlanVersion)
	}
	var p Plan
	if err := json.Unmarshal(doc, &p); err != nil {
		return nil, fmt.Errorf("read the plan: %w", err)
	}
	hash, err := planHash(object)
	if err != nil {
		return nil, fmt.Errorf("hash the plan: %w", err)
	}
	if p.Hash == "" {
		return nil, refusef("the plan has no hash")
	}
	if p.Hash != hash {
		return nil, refusef("the plan's hash is %s but its content hashes to %s: it was changed after it was written", p.Hash, hash)
	}
	if len(p.ToModel) == 0 {
		return nil, refusef("the plan has no toModel")
	}
	p.model, err = ReadModel(p.ToModel)
	if err != nil {
		return nil, fmt.Errorf("the plan's toModel: %w", err)
	}
	if p.model.Hash != p.To {
		return nil, refusef("the plan's toModel hashes to %s, not to the plan's to %s", p.model.Hash, p.To)
	}
	if err := p.check(); err != nil {
		return nil, err
	}
	return &p, nil
}

// check refuses a plan the runner cannot run as written.
func (p *Plan) check() error {
	if p.Dialect != Postgres && p.Dialect != SQLite {
		return refusef("the plan's dialect is %q; the runner knows postgres and sqlite", p.Dialect)
	}
	if p.Service == "" {
		return refusef("the plan names no service")
	}
	contract := false
	for i, step := range p.Steps {
		if step == nil {
			return refusef("the plan's step %d is null", i+1)
		}
		if step.Index != i+1 {
			return refusef("the plan's step %d has index %d", i+1, step.Index)
		}
		switch step.Phase {
		case Expand:
			if contract {
				return refusef("step %d is an expand step after a contract step", step.Index)
			}
		case Contract:
			contract = true
		default:
			return refusef("step %d has phase %q; a step is expand or contract", step.Index, step.Phase)
		}
		if p.Dialect == SQLite && !step.Transactional {
			return refusef("step %d is not transactional; SQLite runs every step in a transaction", step.Index)
		}
		if step.ForeignKeysOff && p.Dialect != SQLite {
			return refusef("step %d turns foreign keys off, which only a SQLite step does", step.Index)
		}
	}
	return nil
}

// ReadModel reads a model document: the file adopt records, or a plan's
// toModel. Its service and dialect must be set.
func ReadModel(doc []byte) (*Model, error) {
	canonical, err := CanonicalJSON(doc)
	if err != nil {
		return nil, fmt.Errorf("read the model: %w", err)
	}
	var keys struct {
		Service *string `json:"service"`
		Dialect *string `json:"dialect"`
	}
	if err := json.Unmarshal(canonical, &keys); err != nil {
		return nil, fmt.Errorf("read the model: %w", err)
	}
	if keys.Service == nil || *keys.Service == "" {
		return nil, refusef("the model names no service")
	}
	if keys.Dialect == nil || (Dialect(*keys.Dialect) != Postgres && Dialect(*keys.Dialect) != SQLite) {
		return nil, refusef("the model's dialect is not postgres or sqlite")
	}
	return &Model{
		Service:   *keys.Service,
		Dialect:   Dialect(*keys.Dialect),
		Canonical: canonical,
		Hash:      Hash(canonical),
	}, nil
}

// steps returns the plan's steps of phase.
func (p *Plan) steps(phase Phase) []*Step {
	var out []*Step
	for _, step := range p.Steps {
		if step.Phase == phase {
			out = append(out, step)
		}
	}
	return out
}
