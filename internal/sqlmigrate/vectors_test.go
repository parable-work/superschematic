package sqlmigrate

import (
	"bytes"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	ir "github.com/parable-work/superschematic/ir"
)

// vectorsDir holds the plans the runner's tests apply
// (runtime/migrate/README.md): one directory per case, one plan per file,
// applied in name order. The first plan of a case starts from an empty
// database and each later one from the model the one before it ends at.
const vectorsDir = "../../runtime/migrate/testdata/plans"

// vectorPlan is one plan of a vector case: the change it makes to the
// schema the plan before it ended at.
type vectorPlan struct {
	name    string
	change  func(s *ir.Schema)
	renames []Rename
}

// vectorCases are kept small: the runner applies each plan once per step
// to test resuming.
var vectorCases = map[string][]vectorPlan{
	// Columns: an add with a concurrent index, a rename, and a drop in
	// contract.
	"columns": {
		{name: "create", change: func(s *ir.Schema) {
			s.Types["Account"] = &ir.TypeDef{Name: "Account", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "name", TypeRef: stringRef, Required: true},
				{Name: "balance", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			}}
		}},
		{name: "add-column-and-index", change: func(s *ir.Schema) {
			addField(s, "Account", &ir.FieldDef{Name: "email", TypeRef: stringRef})
			typeNamed(s, "Account").Indexes = []ir.IndexDef{{Keys: []string{"email"}}}
		}},
		{name: "rename-column", change: func(s *ir.Schema) {
			fieldNamed(s, "Account", "name").Name = "displayName"
		}, renames: []Rename{{From: "account.name", To: "account.display_name"}}},
		{name: "drop-column", change: func(s *ir.Schema) {
			dropField(s, "Account", "email")
			typeNamed(s, "Account").Indexes = nil
		}},
	},
	// Constraints: a foreign key and a unique constraint added online to
	// tables that exist, and a column made NOT NULL in contract.
	"constraints": {
		{name: "create", change: func(s *ir.Schema) {
			s.Scalars["Identity.UUID"] = &ir.ScalarDef{
				Name: "Identity.UUID", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "UUID"},
			}
			s.Types["Account"] = &ir.TypeDef{Name: "Account", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "name", TypeRef: stringRef, Required: true},
			}}
			s.Types["Entry"] = &ir.TypeDef{Name: "Entry", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "accountId", TypeRef: uuidRef},
				{Name: "memo", TypeRef: stringRef},
			}}
		}},
		{name: "constraints", change: func(s *ir.Schema) {
			fieldNamed(s, "Account", "name").Unique = true
			fieldNamed(s, "Entry", "accountId").Relation = &ir.RelationDef{Type: "Account", OnDelete: "RESTRICT"}
			fieldNamed(s, "Entry", "memo").Required = true
		}},
	},
	// Versioned: a table that becomes @versioned, with its history seeded.
	"versioned": {
		{name: "create", change: func(s *ir.Schema) {
			s.Types["Account"] = &ir.TypeDef{Name: "Account", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "name", TypeRef: stringRef, Required: true},
			}}
		}},
		{name: "versioned", change: func(s *ir.Schema) { versioned(s, "Account", nil) }},
	},
}

// sqliteVectorCases are the vectors planned for SQLite.
var sqliteVectorCases = map[string][]vectorPlan{
	// Columns in place: ADD COLUMN with an index, RENAME COLUMN, and DROP
	// COLUMN after its index.
	"sqlite-columns": {
		{name: "create", change: func(s *ir.Schema) {
			s.Types["Account"] = &ir.TypeDef{Name: "Account", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "name", TypeRef: stringRef, Required: true},
				{Name: "balance", TypeRef: ir.TypeRef{Name: "number"}, Required: true},
			}}
		}},
		{name: "add-column-and-index", change: func(s *ir.Schema) {
			addField(s, "Account", &ir.FieldDef{Name: "email", TypeRef: stringRef})
			typeNamed(s, "Account").Indexes = []ir.IndexDef{{Keys: []string{"email"}}}
		}},
		{name: "rename-column", change: func(s *ir.Schema) {
			fieldNamed(s, "Account", "name").Name = "displayName"
		}, renames: []Rename{{From: "account.name", To: "account.display_name"}}},
		{name: "drop-column", change: func(s *ir.Schema) {
			dropField(s, "Account", "email")
			typeNamed(s, "Account").Indexes = nil
		}},
	},
	// Rebuilds: the parent of an ON DELETE CASCADE child rebuilt in
	// expand, with a unique index, and the child rebuilt in contract.
	"sqlite-rebuild": {
		{name: "create", change: func(s *ir.Schema) {
			s.Types["Account"] = &ir.TypeDef{Name: "Account", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "name", TypeRef: stringRef, Required: true},
			}}
			s.Types["Entry"] = &ir.TypeDef{Name: "Entry", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "account", TypeRef: ir.TypeRef{Name: "Account"}, Required: true},
				{Name: "memo", TypeRef: stringRef},
			}}
		}},
		{name: "rebuild", change: func(s *ir.Schema) {
			fieldNamed(s, "Account", "name").Required = false
			fieldNamed(s, "Account", "name").Unique = true
			fieldNamed(s, "Entry", "memo").Required = true
		}},
	},
}

// vectorRun is one vector case planned: its dialect, its plans by file
// name, in order, and the schema and model the last one ends at.
type vectorRun struct {
	dialect Dialect
	files   []string
	plans   []*Plan
	schema  *ir.Schema
	model   *Model
}

// planVectors plans every vector case.
func planVectors(t *testing.T) map[string]vectorRun {
	t.Helper()
	runs := map[string]vectorRun{}
	for name, plans := range vectorCases {
		runs[name] = planVector(t, name, Postgres, plans)
	}
	for name, plans := range sqliteVectorCases {
		runs[name] = planVector(t, name, SQLite, plans)
	}
	return runs
}

// planVector plans one vector case in dialect.
func planVector(t *testing.T, name string, dialect Dialect, plans []vectorPlan) vectorRun {
	t.Helper()
	run := vectorRun{dialect: dialect, schema: ir.NewSchema("ledger-db", ir.SchemaKindDB)}
	for i, vp := range plans {
		vp.change(run.schema)
		to, err := BuildModel(run.schema, sqlgen.Options{SchemaName: "ledger-db"}, dialect)
		if err != nil {
			t.Fatalf("%s/%s: %v", name, vp.name, err)
		}
		plan, err := Diff(run.model, to, Options{Renames: vp.renames})
		if err != nil {
			t.Fatalf("%s/%s: %v", name, vp.name, err)
		}
		run.files = append(run.files, fmt.Sprintf("%02d-%s.plan.json", i+1, vp.name))
		run.plans = append(run.plans, plan)
		run.model = to
	}
	return run
}

// vectorFiles returns each vector's path under vectorsDir and its
// contents: the plan's canonical JSON, indented.
func vectorFiles(t *testing.T) map[string][]byte {
	t.Helper()
	files := map[string][]byte{}
	for name, run := range planVectors(t) {
		for i, plan := range run.plans {
			canonical, err := plan.CanonicalJSON()
			if err != nil {
				t.Fatal(err)
			}
			files[filepath.Join(name, run.files[i])] = indentedJSON(t, canonical)
		}
	}
	return files
}

// TestRunnerVectors keeps runtime/migrate/testdata/plans equal to what the
// planner writes, and fails when a file there is stale or left over.
// Regenerate with:
// go test ./internal/sqlmigrate -run TestRunnerVectors -update
func TestRunnerVectors(t *testing.T) {
	files := vectorFiles(t)
	var existing []string
	if err := filepath.WalkDir(vectorsDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".plan.json") {
			rel, err := filepath.Rel(vectorsDir, path)
			if err != nil {
				return err
			}
			existing = append(existing, rel)
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}

	if *update {
		for _, rel := range existing {
			if _, ok := files[rel]; !ok {
				if err := os.Remove(filepath.Join(vectorsDir, rel)); err != nil {
					t.Fatal(err)
				}
			}
		}
		for rel, data := range files {
			writeFile(t, filepath.Join(vectorsDir, rel), data)
		}
		return
	}

	sort.Strings(existing)
	for _, rel := range existing {
		if _, ok := files[rel]; !ok {
			t.Errorf("%s is not a vector the planner writes (run with -update to remove it)", rel)
		}
	}
	for rel, want := range files {
		got, err := os.ReadFile(filepath.Join(vectorsDir, rel))
		if err != nil {
			t.Errorf("%s is missing (run with -update to write it)", rel)
			continue
		}
		if !bytes.Equal(got, want) {
			t.Errorf("%s is stale (run with -update to rewrite it)", rel)
		}
	}
}
