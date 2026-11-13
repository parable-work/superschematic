package embedded_test

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/embedded"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// notes is a SQLite model of notes-db: a note table with the columns
// named, each TEXT, after its id.
func notes(columns ...string) *sqlmigrate.Model {
	table := &sqlmigrate.Table{
		Name:       "note",
		Kind:       sqlmigrate.TableEntity,
		Origin:     "Note",
		Columns:    []*sqlmigrate.Column{{Name: "id", Origin: "Note.id", Type: "TEXT"}},
		PrimaryKey: &sqlmigrate.Constraint{Name: "note_pkey", Columns: []string{"id"}},
	}
	for _, name := range columns {
		table.Columns = append(table.Columns, &sqlmigrate.Column{Name: name, Origin: "Note." + name, Type: "TEXT", Nullable: true})
	}
	return &sqlmigrate.Model{Version: sqlmigrate.ModelVersion, Dialect: sqlmigrate.SQLite, Service: "notes-db", Tables: []*sqlmigrate.Table{table}}
}

// input plans notes-db of environment Desktop to model, in the Stack
// service at dir.
func input(dir string, model *sqlmigrate.Model, release bool) embedded.PlanInput {
	return embedded.PlanInput{
		StackDir:    dir,
		Environment: "Desktop",
		Databases:   []embedded.Database{{Service: "notes-db"}},
		Model:       func(string) (*sqlmigrate.Model, error) { return model, nil },
		Reads:       func(string, *sqlmigrate.Model) ([]sqlmigrate.Read, error) { return nil, nil },
		Release:     release,
		FailOn:      sqlmigrate.HazardClasses,
	}
}

func mustPlan(t *testing.T, in embedded.PlanInput) *embedded.Planned {
	t.Helper()
	planned, err := embedded.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	return planned
}

func recorded(t *testing.T, dir string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(embedded.RecordedDir(dir, "Desktop", "notes-db"), "*"))
	if err != nil {
		t.Fatal(err)
	}
	for i, file := range files {
		files[i] = filepath.Base(file)
	}
	return files
}

// TestAReleaseRecordsEachStepAndABuildCarriesTheChain: a release plans a
// database from the model the last recorded plan ends at to the schema's
// model and writes the plan, numbered, into the Stack service's
// plans/<environment>/<db>/; a build that is no release plans the same
// step in memory and records nothing; and either's binary carries every
// recorded plan, in order (D116).
func TestAReleaseRecordsEachStepAndABuildCarriesTheChain(t *testing.T) {
	dir := t.TempDir()

	// No release yet: a build carries the plan of its own build alone.
	dev := mustPlan(t, input(dir, notes("title"), false))
	if len(dev.Steps) != 1 || dev.Steps[0].Path != "" || len(dev.Databases[0].Plans) != 1 || len(recorded(t, dir)) != 0 {
		t.Fatalf("a build that is no release planned %+v and recorded %v, want one step in memory", dev.Steps, recorded(t, dir))
	}

	first := mustPlan(t, input(dir, notes("title"), true))
	if got := recorded(t, dir); len(got) != 1 || got[0] != "0001.json" {
		t.Fatalf("the first release recorded %v, want 0001.json", got)
	}
	if first.Steps[0].Plan.From != "" || first.Steps[0].Number != 1 {
		t.Errorf("the first plan starts from %q as number %d, want an empty database as 1", first.Steps[0].Plan.From, first.Steps[0].Number)
	}

	// The schema did not move: a release has nothing to record, and a
	// build carries the recorded plan as it is.
	again := mustPlan(t, input(dir, notes("title"), true))
	if len(again.Steps) != 0 || len(recorded(t, dir)) != 1 {
		t.Errorf("a release of an unchanged schema planned %d step(s) and holds %v", len(again.Steps), recorded(t, dir))
	}
	document, err := os.ReadFile(filepath.Join(embedded.RecordedDir(dir, "Desktop", "notes-db"), "0001.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(again.Databases[0].Plans[0]) != string(document) {
		t.Errorf("the binary does not carry the recorded plan's bytes")
	}

	// The schema moved: a build carries the step, and a release records
	// it from the model the last recorded plan ends at.
	moved := mustPlan(t, input(dir, notes("title", "body"), false))
	if len(moved.Steps) != 1 || moved.Steps[0].Number != 2 || len(moved.Databases[0].Plans) != 2 || len(recorded(t, dir)) != 1 {
		t.Fatalf("a build after a change planned %+v over %v", moved.Steps, recorded(t, dir))
	}
	second := mustPlan(t, input(dir, notes("title", "body"), true))
	if got := recorded(t, dir); len(got) != 2 || got[1] != "0002.json" {
		t.Fatalf("the second release recorded %v, want 0002.json after 0001.json", got)
	}
	if second.Steps[0].Plan.From != first.Steps[0].Plan.To {
		t.Errorf("the second plan starts from %s, want the model the first ends at, %s", second.Steps[0].Plan.From, first.Steps[0].Plan.To)
	}
	chain, err := embedded.ReadRecorded(dir, "Desktop", "notes-db")
	if err != nil || len(chain) != 2 || chain[1].Plan.Hash != second.Steps[0].Plan.Hash {
		t.Errorf("the recorded chain = %v, %v", chain, err)
	}
}

// TestAReleaseRefusesAHazardNoAllowNames: a release records nothing while
// a plan of it has a hazard no --allow names, and records the plan once
// each is named (D116).
func TestAReleaseRefusesAHazardNoAllowNames(t *testing.T) {
	dir := t.TempDir()
	mustPlan(t, input(dir, notes("title", "body"), true))

	// Dropping a column destroys what it holds.
	in := input(dir, notes("title"), true)
	_, err := embedded.Plan(in)
	var hazards *embedded.HazardsError
	if !errors.As(err, &hazards) || len(hazards.Hazards["notes-db"]) == 0 {
		t.Fatalf("a release that drops a column = %v, want its hazards refused", err)
	}
	if !strings.Contains(err.Error(), "--allow") || len(recorded(t, dir)) != 1 {
		t.Errorf("the refusal %q recorded %v, want it to name --allow and record nothing", err, recorded(t, dir))
	}
	// A build that is no release carries the step all the same: nothing
	// is recorded for an install to apply.
	if dev := mustPlan(t, input(dir, notes("title"), false)); len(dev.Steps) != 1 {
		t.Errorf("a build that is no release planned %d step(s), want the step with its hazards", len(dev.Steps))
	}
	for _, h := range hazards.Hazards["notes-db"] {
		in.Allow = append(in.Allow, h.ID)
	}
	mustPlan(t, in)
	if got := recorded(t, dir); len(got) != 2 {
		t.Errorf("the release with each hazard allowed recorded %v, want its plan", got)
	}
}

// TestARecordedPlanIsNeverChanged: a build refuses a recorded plan a hand
// edited, a chain with a plan missing, and a file that is no plan's, each
// by its path (D116, as D102 holds a projection's stamp).
func TestARecordedPlanIsNeverChanged(t *testing.T) {
	record := func(t *testing.T) (string, string) {
		dir := t.TempDir()
		mustPlan(t, input(dir, notes("title"), true))
		mustPlan(t, input(dir, notes("title", "body"), true))
		return dir, embedded.RecordedDir(dir, "Desktop", "notes-db")
	}
	t.Run("edited", func(t *testing.T) {
		dir, plans := record(t)
		file := filepath.Join(plans, "0001.json")
		data, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(strings.Replace(string(data), "title", "titel", 1)), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err = embedded.Plan(input(dir, notes("title", "body"), false))
		if err == nil || !strings.Contains(err.Error(), file) || !strings.Contains(err.Error(), "changed after it was recorded") {
			t.Errorf("a build over an edited plan = %v, want it refused by its path", err)
		}
	})
	t.Run("missing", func(t *testing.T) {
		dir, plans := record(t)
		if err := os.Remove(filepath.Join(plans, "0001.json")); err != nil {
			t.Fatal(err)
		}
		_, err := embedded.Plan(input(dir, notes("title", "body"), false))
		if err == nil || !strings.Contains(err.Error(), "skip number 1") {
			t.Errorf("a build over a chain without its first plan = %v, want the gap refused", err)
		}
	})
	t.Run("another file", func(t *testing.T) {
		dir, plans := record(t)
		if err := os.WriteFile(filepath.Join(plans, "notes.md"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := embedded.Plan(input(dir, notes("title", "body"), false))
		if err == nil || !strings.Contains(err.Error(), "notes.md") {
			t.Errorf("a build over a directory with another file = %v, want the file refused", err)
		}
	})
}

// TestAnAdoptionIsThePlanAChainStartsWith: a database that names an
// adoption before its first release gets a first plan that builds the
// adoption's model from an empty database, so a new install holds what a
// file built by hand holds, and the step to the schema's model after it;
// the binary carries the adoption's model and check. An adoption whose
// model is no model of the chain is refused (D116).
func TestAnAdoptionIsThePlanAChainStartsWith(t *testing.T) {
	dir := t.TempDir()
	byHand, err := notes("title").CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(dir, "adopted.json")
	if err := os.WriteFile(model, byHand, 0o644); err != nil {
		t.Fatal(err)
	}
	in := input(dir, notes("title", "body"), true)
	in.Databases[0].Adoption = &embedded.Adoption{ModelPath: model, Check: "SELECT user_version = 7 FROM pragma_user_version"}
	planned := mustPlan(t, in)
	if got := recorded(t, dir); len(got) != 2 {
		t.Fatalf("the first release of an adopted database recorded %v, want the plan that builds its model and the step after it", got)
	}
	hash, err := notes("title").Hash()
	if err != nil {
		t.Fatal(err)
	}
	if planned.Steps[0].Plan.From != "" || planned.Steps[0].Plan.To != hash || planned.Steps[1].Plan.From != hash {
		t.Errorf("the chain runs %q -> %s -> ...; want an empty database to the adoption's model %s, then on", planned.Steps[0].Plan.From, planned.Steps[0].Plan.To, hash)
	}
	carried := planned.Databases[0].Adoption
	if carried == nil || string(carried.Model) != string(byHand) || carried.Check != in.Databases[0].Adoption.Check {
		t.Errorf("the binary carries adoption %+v, want the model and its check", carried)
	}

	// The adoption stays in the chain through later releases.
	in.Model = func(string) (*sqlmigrate.Model, error) { return notes("title", "body", "tag"), nil }
	if later := mustPlan(t, in); len(later.Steps) != 1 || later.Databases[0].Adoption == nil {
		t.Errorf("a later release planned %d step(s) with adoption %v", len(later.Steps), later.Databases[0].Adoption)
	}

	// A model the chain never held cannot be adopted.
	stray, err := notes("subject").CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, stray, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := embedded.Plan(in); err == nil || !strings.Contains(err.Error(), "the adoption of notes-db") {
		t.Errorf("an adoption outside the chain = %v, want it refused by name", err)
	}

	// Nor a model of another service.
	other := notes("title")
	other.Service = "other-db"
	foreign, err := other.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(model, foreign, 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := embedded.Plan(in); err == nil || !strings.Contains(err.Error(), "other-db") {
		t.Errorf("an adoption of another service's model = %v, want it refused", err)
	}
}
