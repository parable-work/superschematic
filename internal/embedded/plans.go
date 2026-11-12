// Package embedded builds an environment that is one binary (D116): it
// keeps the plans a release records for each database of the environment,
// plans the step a build carries, and builds the environment's crate with
// what the binary carries compiled in.
package embedded

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// PlansDir is the directory of a Stack service that holds the plans its
// releases recorded: `plans/<environment>/<db>/<number>.json`. The files
// are committed, as a project commits its lockfiles (D51, D57): a binary
// carries every one, so an install any number of releases behind is taken
// forward plan by plan.
const PlansDir = "plans"

// RecordedDir returns the directory that holds the plans the releases of
// environment recorded for the DB service named service.
func RecordedDir(stackDir, environment, service string) string {
	return filepath.Join(stackDir, PlansDir, environment, service)
}

// recordedName is a recorded plan's file: its number in the chain, four
// digits or more, from 0001.
var recordedName = regexp.MustCompile(`^([0-9]{4,})\.json$`)

// RecordedName returns the file name of the recorded plan numbered n.
func RecordedName(n int) string { return fmt.Sprintf("%04d.json", n) }

// Recorded is one plan a release recorded.
type Recorded struct {
	// Number is the plan's place in the chain, from 1, and Path its file.
	Number int
	Path   string

	// Plan is the plan, and Document the file's bytes, which the binary
	// carries as they are.
	Plan     *sqlmigrate.Plan
	Document []byte
}

// ReadRecorded reads the plans the releases of environment recorded for
// service, in order. It refuses what is no chain a binary can carry: a
// file of another name, a gap in the numbers, a document that is no plan
// or whose hash is not its content's, which a hand edited, a plan of
// another service or dialect, a first plan that does not start from an
// empty database, and a plan that does not start from the model the one
// before it ends at.
func ReadRecorded(stackDir, environment, service string) ([]*Recorded, error) {
	dir := RecordedDir(stackDir, environment, service)
	entries, err := os.ReadDir(dir)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var chain []*Recorded
	for _, entry := range entries {
		path := filepath.Join(dir, entry.Name())
		m := recordedName.FindStringSubmatch(entry.Name())
		if m == nil || entry.IsDir() {
			return nil, fmt.Errorf("%s is no recorded plan: the directory holds the plans `stack build %s --release` wrote, each named for its number (%s); move it out", path, environment, RecordedName(1))
		}
		number, err := strconv.Atoi(m[1])
		if err != nil || entry.Name() != RecordedName(number) {
			return nil, fmt.Errorf("%s is no recorded plan's name: plan %d is %s", path, number, RecordedName(number))
		}
		document, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		plan, err := readPlan(document)
		if err != nil {
			return nil, fmt.Errorf("recorded plan %s: %w; restore the file as it was committed: a recorded plan is never edited, since an install may have applied it", path, err)
		}
		chain = append(chain, &Recorded{Number: number, Path: path, Plan: plan, Document: document})
	}
	slices.SortFunc(chain, func(a, b *Recorded) int { return a.Number - b.Number })
	for i, r := range chain {
		switch {
		case r.Number != i+1:
			return nil, fmt.Errorf("the plans recorded in %s skip number %d: a binary carries every plan from the first, so restore %s", dir, i+1, RecordedName(i+1))
		case r.Plan.Service != service || r.Plan.Dialect != sqlmigrate.SQLite:
			return nil, fmt.Errorf("recorded plan %s is a %s plan of %s; the directory holds the %s plans of %s", r.Path, r.Plan.Dialect, r.Plan.Service, sqlmigrate.SQLite, service)
		case i == 0 && r.Plan.From != "":
			return nil, fmt.Errorf("recorded plan %s starts from model %s; the first plan of a chain starts from an empty database, which a new install holds", r.Path, r.Plan.From)
		case i > 0 && r.Plan.From != chain[i-1].Plan.To:
			return nil, fmt.Errorf("recorded plan %s starts from model %s, and %s ends at %s: each plan starts from the model the one before it ends at", r.Path, r.Plan.From, chain[i-1].Path, chain[i-1].Plan.To)
		}
	}
	return chain, nil
}

// readPlan decodes a plan document and checks its hash is the hash of its
// content, as the runner checks it.
func readPlan(document []byte) (*sqlmigrate.Plan, error) {
	var raw map[string]any
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.UseNumber()
	if err := decoder.Decode(&raw); err != nil {
		return nil, fmt.Errorf("not a plan document: %w", err)
	}
	recorded, _ := raw["hash"].(string)
	delete(raw, "hash")
	unsealed, err := json.Marshal(raw)
	if err != nil {
		return nil, err
	}
	canonical, err := ir.CanonicalJSON(unsealed)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(canonical)
	if hash := hex.EncodeToString(sum[:]); hash != recorded {
		return nil, fmt.Errorf("its hash is %q, and its content's is %s: the file was changed after it was recorded", recorded, hash)
	}
	plan := &sqlmigrate.Plan{}
	strict := json.NewDecoder(bytes.NewReader(document))
	strict.DisallowUnknownFields()
	if err := strict.Decode(plan); err != nil {
		return nil, fmt.Errorf("not a plan document: %w", err)
	}
	return plan, nil
}

// Adoption is a database's one adoption, as its settings name it (D116):
// the file of the model a database built by hand is recorded at, and the
// statement that must answer one row holding 1 for the file to be adopted.
type Adoption struct {
	ModelPath string
	Check     string
}

// Database is one DB schema an environment's binary holds a file of.
type Database struct {
	Service  string
	Adoption *Adoption
}

// PlanInput is what the plans a binary carries are planned from.
type PlanInput struct {
	// StackDir is the Stack service's directory, which holds the recorded
	// plans, and Environment the environment.
	StackDir    string
	Environment string

	// Databases are the DB schemas the environment's databases host, by
	// service.
	Databases []Database

	// Model returns the model the schema of a DB service declares now, on
	// SQLite, and Reads the columns its readers read of a model, which a
	// plan's hazards name.
	Model func(service string) (*sqlmigrate.Model, error)
	Reads func(service string, to *sqlmigrate.Model) ([]sqlmigrate.Read, error)

	// Release records each new plan beside the ones recorded before it,
	// and refuses one with a hazard of a class in FailOn that Allow does
	// not name. A build that is no release records nothing.
	Release bool
	FailOn  []sqlmigrate.HazardClass
	Allow   []string
}

// Step is one plan a build planned: of Service, numbered Number in its
// chain, at Path when a release recorded it.
type Step struct {
	Service string
	Number  int
	Plan    *sqlmigrate.Plan
	Path    string
}

// Planned is what a build's binary carries for its databases, and the
// steps the build planned for them.
type Planned struct {
	Databases []servergen.EmbeddedCarriedDatabase
	Steps     []*Step
}

// HazardsError is a release's refusal of the plans it would record: the
// hazards no --allow names, by service.
type HazardsError struct {
	Environment string
	Hazards     map[string][]*sqlmigrate.Hazard
}

func (e *HazardsError) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "the release of %s would record a plan with hazards no --allow names, and recorded nothing:", e.Environment)
	for _, service := range slices.Sorted(maps.Keys(e.Hazards)) {
		for _, h := range e.Hazards[service] {
			fmt.Fprintf(&b, "\n  %s: %s\n    %s", service, h.ID, h.Reason)
		}
	}
	b.WriteString("\nread each, and pass --allow <id> for the ones the release means")
	return b.String()
}

// Plan plans what a build's binary carries for each database (D116): the
// plans its releases recorded, and the step from the model the last of
// them ends at to the model the schema declares now, when they differ. A
// release records that step, numbered after the last, and a build that is
// no release carries it without recording it.
//
// A database with an adoption and no recorded plan gets a first plan from
// an empty database to the adoption's model, so a new install builds what
// a file built by hand holds, and the step to the schema's model follows
// it. An adoption's model must be one a carried plan starts from or the
// one the chain ends at: a file adopted at any other could not be taken
// forward.
//
// A release plans every database before it records any, so one refused
// hazard records nothing.
func Plan(in PlanInput) (*Planned, error) {
	type pending struct {
		database Database
		chain    []*Recorded
		steps    []*sqlmigrate.Plan
		adoption *servergen.EmbeddedCarriedAdoption
	}
	var all []*pending
	hazards := map[string][]*sqlmigrate.Hazard{}
	for _, d := range in.Databases {
		chain, err := ReadRecorded(in.StackDir, in.Environment, d.Service)
		if err != nil {
			return nil, err
		}
		to, err := in.Model(d.Service)
		if err != nil {
			return nil, fmt.Errorf("the model of %s: %w", d.Service, err)
		}
		p := &pending{database: d, chain: chain}
		var from *sqlmigrate.Model
		if len(chain) > 0 {
			from = &sqlmigrate.Model{}
			if err := decodeModel(chain[len(chain)-1].Plan.ToModel, from); err != nil {
				return nil, fmt.Errorf("the model recorded plan %s ends at: %w", chain[len(chain)-1].Path, err)
			}
		}
		var adopted *sqlmigrate.Model
		if d.Adoption != nil {
			if adopted, p.adoption, err = readAdoption(d); err != nil {
				return nil, err
			}
			if len(chain) == 0 {
				// The first plan builds what a file built by hand holds.
				seed, err := sqlmigrate.Diff(nil, adopted, sqlmigrate.Options{})
				if err != nil {
					return nil, fmt.Errorf("plan %s from an empty database to its adoption's model: %w", d.Service, err)
				}
				p.steps, from = append(p.steps, seed), adopted
			}
		}
		same, err := sameModel(from, to)
		if err != nil {
			return nil, err
		}
		if !same {
			reads, err := in.Reads(d.Service, to)
			if err != nil {
				return nil, err
			}
			step, err := sqlmigrate.Diff(from, to, sqlmigrate.Options{ReadersAfter: reads})
			if err != nil {
				return nil, fmt.Errorf("plan %s: %w", d.Service, err)
			}
			p.steps = append(p.steps, step)
		}
		if adopted != nil {
			if err := checkAdoption(d, adopted, chain, p.steps); err != nil {
				return nil, err
			}
		}
		for _, step := range p.steps {
			hazards[d.Service] = append(hazards[d.Service], step.Unallowed(in.FailOn, in.Allow)...)
		}
		if len(hazards[d.Service]) == 0 {
			delete(hazards, d.Service)
		}
		all = append(all, p)
	}
	if in.Release && len(hazards) > 0 {
		return nil, &HazardsError{Environment: in.Environment, Hazards: hazards}
	}
	out := &Planned{}
	for _, p := range all {
		carried := servergen.EmbeddedCarriedDatabase{Service: p.database.Service, Adoption: p.adoption}
		for _, r := range p.chain {
			carried.Plans = append(carried.Plans, r.Document)
		}
		for i, plan := range p.steps {
			document, err := planDocument(plan)
			if err != nil {
				return nil, err
			}
			step := &Step{Service: p.database.Service, Number: len(p.chain) + i + 1, Plan: plan}
			if in.Release {
				step.Path = filepath.Join(RecordedDir(in.StackDir, in.Environment, p.database.Service), RecordedName(step.Number))
				if err := os.MkdirAll(filepath.Dir(step.Path), 0o755); err != nil {
					return nil, err
				}
				if err := os.WriteFile(step.Path, document, 0o644); err != nil {
					return nil, err
				}
			}
			carried.Plans = append(carried.Plans, document)
			out.Steps = append(out.Steps, step)
		}
		out.Databases = append(out.Databases, carried)
	}
	return out, nil
}

// planDocument is the plan's document, as a recorded plan's file holds
// it: its canonical JSON, sealed, and a newline.
func planDocument(plan *sqlmigrate.Plan) ([]byte, error) {
	if plan.Hash == "" {
		if err := plan.Seal(); err != nil {
			return nil, err
		}
	}
	canonical, err := plan.CanonicalJSON()
	if err != nil {
		return nil, err
	}
	return append(canonical, '\n'), nil
}

// decodeModel decodes a model document strictly.
func decodeModel(document []byte, model *sqlmigrate.Model) error {
	decoder := json.NewDecoder(bytes.NewReader(document))
	decoder.DisallowUnknownFields()
	return decoder.Decode(model)
}

// sameModel reports whether from, nil for an empty database, is the model
// to.
func sameModel(from, to *sqlmigrate.Model) (bool, error) {
	if from == nil {
		return false, nil
	}
	a, err := from.Hash()
	if err != nil {
		return false, err
	}
	b, err := to.Hash()
	if err != nil {
		return false, err
	}
	return a == b, nil
}

// readAdoption reads the model a database's adoption names, a SQLite
// model of the database's service, and returns it with what the binary
// carries of the adoption: the model's canonical JSON and the check.
func readAdoption(d Database) (*sqlmigrate.Model, *servergen.EmbeddedCarriedAdoption, error) {
	where := fmt.Sprintf("the adoption of %s", d.Service)
	document, err := os.ReadFile(d.Adoption.ModelPath)
	if err != nil {
		return nil, nil, fmt.Errorf("%s: its model: %w; write it with `superschematic migrate plan <db-service> --dialect sqlite --from-database <file> --print-model`", where, err)
	}
	model := &sqlmigrate.Model{}
	if err := decodeModel(document, model); err != nil {
		return nil, nil, fmt.Errorf("%s: %s is no model document: %w", where, d.Adoption.ModelPath, err)
	}
	if model.Service != d.Service || model.Dialect != sqlmigrate.SQLite {
		return nil, nil, fmt.Errorf("%s: %s is a %s model of %s; an adoption names the %s model of the file of %s", where, d.Adoption.ModelPath, model.Dialect, model.Service, sqlmigrate.SQLite, d.Service)
	}
	canonical, err := model.CanonicalJSON()
	if err != nil {
		return nil, nil, err
	}
	return model, &servergen.EmbeddedCarriedAdoption{Model: canonical, Check: d.Adoption.Check}, nil
}

// checkAdoption refuses an adoption whose model no carried plan starts
// from and the chain does not end at.
func checkAdoption(d Database, adopted *sqlmigrate.Model, chain []*Recorded, steps []*sqlmigrate.Plan) error {
	hash, err := adopted.Hash()
	if err != nil {
		return err
	}
	last := ""
	for _, r := range chain {
		if r.Plan.From == hash {
			return nil
		}
		last = r.Plan.To
	}
	for _, step := range steps {
		if step.From == hash {
			return nil
		}
		last = step.To
	}
	if last == hash {
		return nil
	}
	return fmt.Errorf("the adoption of %s: its model %s, of %s, is no model a recorded plan of the database starts from, nor the one its chain ends at, so a file adopted at it could not be taken forward; an adoption is named before the database's first release, which records the plan that builds its model", d.Service, hash, d.Adoption.ModelPath)
}
