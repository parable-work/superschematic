package loader

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// fencedFixture is a database of both dialects whose Parcel has one fence
// with no covers, whose Delivery has two, each over its own fields, a
// relation among them, and whose Depot's fence covers the column a soft
// delete sets (D118).
const fencedFixture = "tsreader/testdata/services/fixture-fenced-db"

// TestFencedFixtureLoads: the fixture loads and verifies, each table's
// fences as written, and the loaded IR is held to its golden (D118).
func TestFencedFixtureLoads(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(fencedFixture))
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]*ir.Fence{
		"Courier": nil,
		"Parcel":  {{Field: "version"}},
		"Delivery": {
			{Field: "planVersion", Covers: []string{"address", "courier"}},
			{Field: "runVersion", Covers: []string{"attempts", "proof"}},
		},
		"Depot": {{Field: "revision", Covers: []string{"name", "deletedAt"}}},
	} {
		if td := db.Types[name]; td == nil || !reflect.DeepEqual(td.Fences, want) {
			t.Errorf("%s = %s", name, marshal(t, td))
		}
	}
	// The whole-row fence stands on a table with no _version: on SQLite an
	// @optimistic table takes no fence without covers.
	if db.Types["Parcel"].Optimistic {
		t.Error("Parcel is @optimistic")
	}
	compareExpanded(t, db, "fixture-fenced-db.expanded.json")
}

// fencedService writes a DB service of the dialects given whose Job, in a
// JSON schema file, takes the keys given beside its fields: its fences,
// and whatever else a case declares.
func fencedService(t *testing.T, dialects, job string) string {
	t.Helper()
	return writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {"sql": {"dialects": ` + dialects + `}}}`,
		"src/job.schema.json": `{
			"scalars": {
				"Generic.Int64": {"name": "Generic.Int64", "languagePrimitive": "number"}
			},
			"enums": {
				"JobState": {"name": "JobState", "values": [{"name": "QUEUED"}, {"name": "DONE"}]}
			},
			"types": {
				"Owner": {
					"name": "Owner",
					"role": "DBTable",
					"fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}]
				},
				"Task": {
					"name": "Task",
					"role": "DBTable",
					"fields": [
						{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
						{"name": "job", "typeRef": {"name": "Job"}, "required": true, "relation": {"type": "Job"}}
					]
				},
				"Job": {
					"name": "Job",
					"role": "DBTable",
					"fields": [
						{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
						{"name": "label", "typeRef": {"name": "string"}, "required": true},
						{"name": "priority", "typeRef": {"name": "Generic.Int64"}, "required": true},
						{"name": "owner", "typeRef": {"name": "Owner"}, "relation": {"type": "Owner"}},
						{"name": "planVersion", "typeRef": {"name": "Generic.Int64"}, "required": true, "default": "0"},
						{"name": "claimedBy", "typeRef": {"name": "string"}},
						{"name": "progress", "typeRef": {"name": "Generic.Int64"}, "required": true},
						{"name": "executionVersion", "typeRef": {"name": "Generic.Int64"}, "required": true, "default": "0"},
						{"name": "estimate", "typeRef": {"name": "Generic.Int64"}},
						{"name": "ratio", "typeRef": {"name": "number"}, "required": true},
						{"name": "state", "typeRef": {"name": "JobState"}, "required": true},
						{"name": "tasks", "typeRef": {"name": "Task", "isArray": true}, "hasMany": true}
					],
					` + job + `
				}
			}
		}`,
	})
}

const bothDialects = `["postgres", "sqlite"]`

// TestFencesLoadFromADataForm: a JSON and a YAML schema file write a
// type's fences under fences, each a field and the fields it covers, a
// relation among them (D118).
func TestFencesLoadFromADataForm(t *testing.T) {
	want := []*ir.Fence{
		{Field: "planVersion", Covers: []string{"label", "priority", "owner"}},
		{Field: "executionVersion", Covers: []string{"claimedBy", "progress"}},
	}
	schema, err := LoadService(fencedService(t, bothDialects, `"fences": [
		{"field": "planVersion", "covers": ["label", "priority", "owner"]},
		{"field": "executionVersion", "covers": ["claimedBy", "progress"]}
	]`))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if job := schema.Types["Job"]; !reflect.DeepEqual(job.Fences, want) {
		t.Errorf("Job = %s", marshal(t, job))
	}

	// One fence with no covers, on a table with a _version, where the
	// service runs on Postgres alone.
	schema, err = LoadService(fencedService(t, `["postgres"]`, `"optimistic": true, "fences": [{"field": "planVersion"}]`))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if job := schema.Types["Job"]; !reflect.DeepEqual(job.Fences, []*ir.Fence{{Field: "planVersion"}}) {
		t.Errorf("Job = %s", marshal(t, job))
	}

	schema, err = LoadService(writeService(t, map[string]string{
		"schema.config.yaml": "name: temp-db\nkind: DB\noutputs:\n  sql:\n    dialects: [postgres, sqlite]\n",
		"src/job.schema.yaml": `scalars:
  Generic.Int64: { name: Generic.Int64, languagePrimitive: number }
types:
  Job:
    name: Job
    role: DBTable
    fields:
      - { name: id, typeRef: { name: string }, required: true, key: true }
      - { name: label, typeRef: { name: string }, required: true }
      - { name: planVersion, typeRef: { name: Generic.Int64 }, required: true, default: "0" }
      - { name: progress, typeRef: { name: Generic.Int64 }, required: true }
      - { name: executionVersion, typeRef: { name: Generic.Int64 }, required: true, default: "0" }
    fences:
      - field: planVersion
        covers: [label]
      - field: executionVersion
        covers:
          - progress
`,
	}))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	wantYAML := []*ir.Fence{{Field: "planVersion", Covers: []string{"label"}}, {Field: "executionVersion", Covers: []string{"progress"}}}
	if job := schema.Types["Job"]; !reflect.DeepEqual(job.Fences, wantYAML) {
		t.Errorf("Job = %s", marshal(t, job))
	}
}

// TestFencesRefusedInADataForm: what the build refuses of a fence a JSON
// schema file writes (D118): the JSON Schema an empty covers,
// ir.Schema.Validate what the declarations decide alone, and verification
// what needs the fields, the table and the service's dialects.
func TestFencesRefusedInADataForm(t *testing.T) {
	for _, tc := range []struct {
		name     string
		dialects string
		job      string
		want     string
	}{
		{"an empty covers", bothDialects, `"fences": [{"field": "planVersion", "covers": []}]`, "minItems: got 0, want 1"},
		// ir.Schema.Validate's.
		{"two fences on a field", bothDialects, `"fences": [{"field": "planVersion", "covers": ["label"]}, {"field": "planVersion", "covers": ["priority"]}]`,
			"temp-db: @fenced on Job.planVersion: Job declares two fences on planVersion; a field is one fence, so list what it covers in one declaration"},
		{"a fence a state machine governs", bothDialects, `"fences": [{"field": "state"}],
			"stateMachines": [{"field": "state", "terminal": ["DONE"], "transitions": [{"from": "QUEUED", "to": ["DONE"]}]}]`,
			"temp-db: @fenced on Job.state: state is the field a @stateMachine of Job governs, which holds a state and no count; name an integer field of its own"},
		{"several fences where one has no covers", bothDialects, `"fences": [{"field": "planVersion", "covers": ["label"]}, {"field": "executionVersion"}]`,
			"temp-db: @fenced on Job.executionVersion: Job has more than one fence, and this one lists no covers, so every update would advance it, a write of another fence's fields among them; list in covers the fields each fence counts"},
		{"a covered field listed twice", bothDialects, `"fences": [{"field": "planVersion", "covers": ["label", "label"]}]`,
			"temp-db: @fenced on Job.planVersion: covers lists label twice; list each field once"},
		{"a fence that covers itself", bothDialects, `"fences": [{"field": "planVersion", "covers": ["label", "planVersion"]}]`,
			"temp-db: @fenced on Job.planVersion: covers lists planVersion, the fence itself, which a covered write advances; leave it out"},
		{"a fence that covers another fence", bothDialects, `"fences": [{"field": "planVersion", "covers": ["executionVersion"]}, {"field": "executionVersion", "covers": ["progress"]}]`,
			"temp-db: @fenced on Job.planVersion: covers lists executionVersion, the field of another fence of Job, which that fence's writes advance; leave it out"},
		{"a field two fences cover", bothDialects, `"fences": [{"field": "planVersion", "covers": ["label", "owner"]}, {"field": "executionVersion", "covers": ["progress", "owner"]}]`,
			"temp-db: @fenced on Job.executionVersion: covers lists owner, which the fence on Job.planVersion covers; one fence covers a field, so a write of it advances one counter"},
		// Verification's.
		{"a fence the type lacks", bothDialects, `"fences": [{"field": "revision"}]`,
			"@fenced on Job.revision: Job has no field revision; name the integer field of Job every covered write advances"},
		{"a nullable fence", bothDialects, `"fences": [{"field": "estimate"}]`,
			"@fenced on Job.estimate: estimate is nullable, and a null has no next value; make it required, as Default<Generic.Int64, 0>"},
		{"a text fence", bothDialects, `"fences": [{"field": "label"}]`,
			"@fenced on Job.label: label's column is TEXT, and a fence is a whole number each covered write adds one to; type it by a scalar whose column is SMALLINT, INTEGER or BIGINT, as Default<Generic.Int64, 0>"},
		{"a float fence", bothDialects, `"fences": [{"field": "ratio"}]`,
			"@fenced on Job.ratio: ratio's column is DOUBLE PRECISION, and a fence is a whole number each covered write adds one to"},
		{"a covered field the type lacks", bothDialects, `"fences": [{"field": "planVersion", "covers": ["title"]}]`,
			"@fenced on Job.planVersion: covers lists title, which is no field of Job; list fields of Job"},
		{"a covered field with no column", bothDialects, `"fences": [{"field": "planVersion", "covers": ["label", "tasks"]}]`,
			"@fenced on Job.planVersion: covers lists tasks, a list of Task, whose rows are kept in a table of their own, so no write of Job changes it; leave it out"},
		{"a @jsonField type", bothDialects, `"jsonField": true, "fences": [{"field": "planVersion"}]`,
			"@fenced on Job: Job is a @jsonField type, kept as JSON in a column of another table, so it has no row of its own to fence; declare the fence on the table that holds it"},
		{"an @optimistic table of a sqlite service", bothDialects, `"optimistic": true, "fences": [{"field": "planVersion"}]`,
			`@fenced on Job.planVersion: Job is @optimistic and temp-db lists the sqlite dialect, where the bump of _version is an update of its own that sets _version alone, which a fence with no covers would refuse; list in covers the fields the fence counts, or build temp-db for postgres alone (outputs.sql.dialects: ["postgres"])`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadService(fencedService(t, tc.dialects, tc.job))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestFencedReadsTheDialectsOfAYAMLConfig: the dialects a fence is held to
// are read from a YAML config as from a JSON one (D118): the same table
// loads for postgres alone and is refused once the config lists sqlite.
func TestFencedReadsTheDialectsOfAYAMLConfig(t *testing.T) {
	service := func(dialects string) string {
		return writeService(t, map[string]string{
			"schema.config.yaml": "name: temp-db\nkind: DB\noutputs:\n  sql:\n    dialects: " + dialects + "\n",
			"src/shelf.schema.yaml": `scalars:
  Generic.Int64: { name: Generic.Int64, languagePrimitive: number }
types:
  Shelf:
    name: Shelf
    role: DBTable
    optimistic: true
    fields:
      - { name: id, typeRef: { name: string }, required: true, key: true }
      - { name: label, typeRef: { name: string }, required: true }
      - { name: revision, typeRef: { name: Generic.Int64 }, required: true }
    fences:
      - field: revision
`,
		})
	}
	if _, err := LoadService(service("[postgres]")); err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	_, err := LoadService(service("[postgres, sqlite]"))
	const want = "@fenced on Shelf.revision: Shelf is @optimistic and temp-db lists the sqlite dialect, where the bump of _version is an update of its own that sets _version alone, which a fence with no covers would refuse"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("LoadService = %v, want an error containing %q", err, want)
	}
}

// TestFencedRefusedBesideABehavior: the behaviors' core writes the rows of
// a table that composes a behavior, the table's own fields among them,
// with statements that advance no fence, so the build refuses every fence
// of such a table, once for the class (D118): with a field-anchored use
// and with an implements one, with covers of the table's own fields, with
// none, and with the behavior's field among them. The same table with no
// behavior loads.
func TestFencedRefusedBesideABehavior(t *testing.T) {
	const workflow = `"behaviors": [{"name": "Workflow", "field": "status", "config": {
		"states": ["open", "closed"], "transitions": [{"from": "open", "to": "closed"}]}}], `
	const timers = `"behaviors": [{"name": "Timers", "implements": true, "config": {
		"deadlines": {"respond": {"start": {"create": true}, "afterMs": 1500}}}}], `
	const refusal = " behavior, so the behaviors' core writes its rows, the table's own fields among them, with statements of its own, which advance no fence; a fence on a table that composes a behavior is not built"
	const ownFields = `[{"field": "planVersion", "covers": ["label", "owner"]}]`
	for _, tc := range []struct {
		name     string
		behavior string
		fences   string
		want     string
	}{
		{"a field use, covers of the table's own fields", workflow, ownFields, "@fenced on Job: Job composes the Workflow" + refusal},
		{"a field use, no covers", workflow, `[{"field": "planVersion"}]`, "@fenced on Job: Job composes the Workflow" + refusal},
		{"a field use, a covers that names the behavior's field", workflow, `[{"field": "planVersion", "covers": ["label", "status"]}]`, "@fenced on Job: Job composes the Workflow" + refusal},
		{"a field use, two fences", workflow, `[{"field": "planVersion", "covers": ["label"]}, {"field": "executionVersion", "covers": ["progress"]}]`, "@fenced on Job: Job composes the Workflow" + refusal},
		{"an implements use, covers of the table's own fields", timers, ownFields, "@fenced on Job: Job composes the Timers" + refusal},
		{"an implements use, no covers", timers, `[{"field": "planVersion"}]`, "@fenced on Job: Job composes the Timers" + refusal},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadService(fencedService(t, bothDialects, tc.behavior+`"fences": `+tc.fences))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, tc.want)
			}
			// Once for the class, and nothing of a fence of its own.
			if n := strings.Count(err.Error(), "@fenced on Job"); n != 1 {
				t.Errorf("LoadService reports %d fence errors, want 1: %v", n, err)
			}
		})
	}
	schema, err := LoadService(fencedService(t, bothDialects, `"fences": `+ownFields))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	if job := schema.Types["Job"]; len(job.Fences) != 1 || len(job.TableBehaviors()) != 0 {
		t.Errorf("Job = %s", marshal(t, job))
	}
}

// TestFencedRefusedInTypeScript: what the compiler takes of a @fenced and
// verification refuses (D118): a fence typed by a plain number, whose
// column holds no whole number exactly, and a fence with no covers on an
// @optimistic table of a service whose config lists sqlite, which the
// loader reads the dialects of.
func TestFencedRefusedInTypeScript(t *testing.T) {
	for service, want := range map[string]string{
		"broken-fenced-number-field":      "@fenced on Job.version: version's column is DOUBLE PRECISION, and a fence is a whole number each covered write adds one to; type it by a scalar whose column is SMALLINT, INTEGER or BIGINT, as Default<Generic.Int64, 0>",
		"broken-fenced-optimistic-sqlite": `@fenced on Shelf.revision: Shelf is @optimistic and broken-fenced-optimistic-sqlite lists the sqlite dialect, where the bump of _version is an update of its own that sets _version alone, which a fence with no covers would refuse; list in covers the fields the fence counts, or build broken-fenced-optimistic-sqlite for postgres alone (outputs.sql.dialects: ["postgres"])`,
	} {
		t.Run(service, func(t *testing.T) {
			_, err := LoadService(filepath.Join("tsreader", "testdata", "services", service))
			if err == nil || !strings.Contains(err.Error(), want) {
				t.Fatalf("LoadService = %v, want an error containing %q", err, want)
			}
		})
	}
}

// TestSQLDialectsReadsTheConfig: the dialects verification holds a fence
// to are the ones a DB service's config lists, postgres alone when it
// lists none, and none for a service of another kind (D118).
func TestSQLDialectsReadsTheConfig(t *testing.T) {
	db := func(outputs map[string]any) *schemaconfig.SchemaConfig {
		return &schemaconfig.SchemaConfig{Name: "temp-db", Kind: ir.SchemaKindDB, Outputs: outputs}
	}
	for _, tc := range []struct {
		name string
		cfg  *schemaconfig.SchemaConfig
		want []string
	}{
		{"both dialects", db(map[string]any{"sql": map[string]any{"dialects": []any{"postgres", "sqlite"}}}), []string{"postgres", "sqlite"}},
		{"no dialects", db(map[string]any{"sql": map[string]any{"sourceSchema": "app"}}), []string{"postgres"}},
		{"no sql output", db(map[string]any{}), []string{"postgres"}},
		{"an API", &schemaconfig.SchemaConfig{Name: "temp-api", Kind: ir.SchemaKindAPI, Outputs: map[string]any{}}, nil},
		{"no config", nil, nil},
	} {
		if got := sqlDialects(tc.cfg); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s: sqlDialects = %v, want %v", tc.name, got, tc.want)
		}
	}
}
