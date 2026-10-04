package sqlmigrate

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"

	ir "github.com/parable-work/superschematic/ir"
)

// PlanVersion is the version of the Plan JSON form, which
// runtime/migrate/README.md specifies for the runner.
const PlanVersion = 1

// Phase is when a step runs relative to the rollout of the new servers.
type Phase string

const (
	// Expand steps run before the new servers roll out and keep the
	// previous version's servers working, except where a step carries
	// HazardCompat.
	Expand Phase = "expand"
	// Contract steps run after the rollout: they remove what the previous
	// version's servers used or tighten what they wrote.
	Contract Phase = "contract"
)

// HazardClass is one of the seven hazard classes of D27.
type HazardClass string

const (
	// HazardDestructive: the step deletes data the new version cannot
	// recover.
	HazardDestructive HazardClass = "destructive"
	// HazardBlocking: the step holds a lock that blocks writes, or reads,
	// for time that grows with the table.
	HazardBlocking HazardClass = "blocking"
	// HazardCompat: the step breaks a server built from the previous
	// version, which may still be running.
	HazardCompat HazardClass = "compat"
	// HazardDataDependent: the step fails at apply when existing rows
	// violate it.
	HazardDataDependent HazardClass = "data-dependent"
	// HazardCopyTable: the step rebuilds the table by copying it (SQLite).
	HazardCopyTable HazardClass = "copy-table"
	// HazardAPIBreaking: the step drops, renames or retypes a column a
	// reader live at the step's phase reads, or changes the columns a
	// projection view publishes.
	HazardAPIBreaking HazardClass = "api-breaking"
	// HazardHistory: the step changes the shape of rows a history table
	// keeps.
	HazardHistory HazardClass = "history"
)

// HazardClasses lists every class in the order D27 gives them.
var HazardClasses = []HazardClass{
	HazardDestructive, HazardBlocking, HazardCompat, HazardDataDependent,
	HazardCopyTable, HazardAPIBreaking, HazardHistory,
}

// Plan is the migration from one model to another. Its JSON form is the
// contract the runner reads (runtime/migrate/README.md).
type Plan struct {
	Version int     `json:"version"`
	Dialect Dialect `json:"dialect"`
	Service string  `json:"service"`

	// From is the hash of the model the plan starts from; empty means an
	// empty database. To is the hash of the model it ends at.
	From string `json:"from"`
	To   string `json:"to"`

	// ToModel is the canonical JSON of the model the plan ends at. The
	// runner records it as the database's applied model.
	ToModel json.RawMessage `json:"toModel"`

	// Renames are the renames the plan was given (--rename), as written.
	Renames []string `json:"renames,omitempty"`

	// Steps run in order: every expand step, then every contract step.
	Steps []*Step `json:"steps"`

	// Hash is the lowercase hex SHA-256 of the plan's canonical JSON with
	// Hash itself left out ([Plan.Seal]).
	Hash string `json:"hash,omitempty"`
}

// Step is one change, applied as a unit.
type Step struct {
	// Index is the step's 1-based position in Plan.Steps.
	Index int   `json:"index"`
	Phase Phase `json:"phase"`

	// Op names the operation in lowerCamelCase ("addColumn",
	// "createIndex", "copyTable"). The runner does not read it.
	Op string `json:"op"`

	// Subject is the object the step changes, as a path:
	//   table/<table>
	//   table/<table>/column/<column>
	//   table/<table>/index/<index>
	//   table/<table>/constraint/<constraint>
	//   function/<function>
	//   trigger/<table>/<trigger>
	//   view/<schema>.<view>
	//   schema/<schema>
	//   extension/<extension>
	// Names are the database's, unquoted, in the new version for a create
	// or a rename and in the previous one for a drop.
	Subject string `json:"subject"`

	// Statements are complete SQL statements without trailing semicolons,
	// run in order. The runner runs each with one Exec, so a statement may
	// contain semicolons inside a dollar-quoted body.
	Statements []string `json:"statements"`

	// Transactional steps run in one transaction with the runner's log row
	// for them. A step that is not (CREATE INDEX CONCURRENTLY) runs each
	// statement on its own.
	Transactional bool `json:"transactional"`

	// Recovery runs before a non-transactional step that started and did
	// not finish is run again, such as dropping the invalid index a failed
	// concurrent build leaves. Statements as above.
	Recovery []string `json:"recovery,omitempty"`

	// ForeignKeysOff asks the runner to turn foreign key enforcement off
	// around the step's transaction and to check every foreign key before
	// its commit (SQLite's copy-table rebuild).
	ForeignKeysOff bool `json:"foreignKeysOff,omitempty"`

	Hazards []*Hazard `json:"hazards,omitempty"`
}

// Hazard is one hazard of a step.
type Hazard struct {
	// ID is [HazardID] of the class, the subject and the reader. It stays
	// the same across plans of the same change, so --allow can name it.
	ID      string      `json:"id"`
	Class   HazardClass `json:"class"`
	Subject string      `json:"subject"`
	// Reader is the reader an api-breaking hazard breaks, as
	// "<service>/<View>.<field>" (or "<schema>.<view>" for a projection's
	// readers); empty for every other class.
	Reader string `json:"reader,omitempty"`
	// Reason says why, in one sentence a reviewer can act on, naming the
	// schema field where there is one ("Order.total changes from Int64 to
	// Decimal: BIGINT to NUMERIC rewrites the table").
	Reason string `json:"reason"`
}

// HazardID is "<class>:<subject>", with "@<reader>" appended when reader
// is set.
func HazardID(class HazardClass, subject, reader string) string {
	id := string(class) + ":" + subject
	if reader != "" {
		id += "@" + reader
	}
	return id
}

// Read is one table column a reader reads.
type Read struct {
	// Reader is the service that reads it ("shop-api").
	Reader string `json:"reader"`
	// Via is the view field that reads it ("OrderView.total").
	Via    string `json:"via"`
	Table  string `json:"table"`
	Column string `json:"column"`
}

// Rename is one --rename: a table ("purchase" to "order") or a column
// ("order.total" to "order.amount"). From names the previous version's
// table or table.column, To the new version's.
type Rename struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// Options carries what a plan takes besides the two models.
type Options struct {
	// Renames turn a drop and an add into a rename.
	Renames []Rename
	// ReadersBefore are the readers deployed before the rollout; expand
	// steps are checked against them. ReadersAfter are those deployed after
	// it; contract steps are checked against them.
	ReadersBefore []Read
	ReadersAfter  []Read
}

// CanonicalJSON returns the plan's canonical JSON: ir.CanonicalJSON of its
// JSON encoding.
func (p *Plan) CanonicalJSON() ([]byte, error) {
	raw, err := json.Marshal(p)
	if err != nil {
		return nil, err
	}
	return ir.CanonicalJSON(raw)
}

// Seal sets Hash to the SHA-256 of the plan's canonical JSON without Hash.
func (p *Plan) Seal() error {
	p.Hash = ""
	canonical, err := p.CanonicalJSON()
	if err != nil {
		return err
	}
	sum := sha256.Sum256(canonical)
	p.Hash = hex.EncodeToString(sum[:])
	return nil
}

// Hazards returns every step's hazards in step order.
func (p *Plan) Hazards() []*Hazard {
	var out []*Hazard
	for _, step := range p.Steps {
		out = append(out, step.Hazards...)
	}
	return out
}
