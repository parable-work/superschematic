package verify

import (
	"encoding/json"
	"fmt"
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// fenceSchema is a DB schema with a Job two parties write: a planner,
// whose writes of label, priority and owner advance version, and a
// worker, whose writes of claimedBy and progress advance executionVersion
// (D118). Its owner is a relation, its tasks a list of another table's
// rows, which stores no column, and its site a POINT. The input lists
// both dialects.
func fenceSchema() (*ir.Schema, Input) {
	schema := ir.NewSchema("jobs-db", ir.SchemaKindDB)
	for name, sqlType := range map[string]string{
		"Identity.UUID": "UUID", "Generic.Int64": "BIGINT", "Generic.Int32": "INTEGER", "Generic.Int16": "SMALLINT",
		"Geo.Point": "POINT", "Temporal.DateTime": "TIMESTAMPTZ",
	} {
		schema.Scalars[name] = &ir.ScalarDef{Name: name, TypeMappings: map[string]string{"sql": sqlType}}
	}
	id := func() *ir.FieldDef {
		return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: "Identity.UUID"}, Required: true, Key: true}
	}
	schema.Types["Owner"] = &ir.TypeDef{Name: "Owner", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{id()}}
	schema.Types["Task"] = &ir.TypeDef{Name: "Task", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
		id(), {Name: "job", TypeRef: ir.TypeRef{Name: "Job"}, Required: true},
	}}
	zero := "0"
	counter := func(name string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true, Default: &zero}
	}
	schema.Types["Job"] = &ir.TypeDef{
		Name: "Job",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			id(),
			{Name: "label", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			counter("priority"),
			{Name: "owner", TypeRef: ir.TypeRef{Name: "Owner"}},
			counter("version"),
			{Name: "claimedBy", TypeRef: ir.TypeRef{Name: "string"}},
			counter("progress"),
			counter("executionVersion"),
			{Name: "note", TypeRef: ir.TypeRef{Name: "string"}},
			{Name: "site", TypeRef: ir.TypeRef{Name: "Geo.Point"}},
			{Name: "tasks", TypeRef: ir.TypeRef{Name: "Task", IsArray: true}},
		},
		Fences: []*ir.Fence{
			{Field: "version", Covers: []string{"label", "priority", "owner"}},
			{Field: "executionVersion", Covers: []string{"claimedBy", "progress"}},
		},
	}
	return schema, Input{Dialects: []string{"postgres", "sqlite"}}
}

// fenceErrors runs the fences' checks alone and returns what they refuse.
func fenceErrors(schema *ir.Schema, in Input) []string {
	r := &Result{}
	checkFences(schema, in, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	return got
}

// wantFenceErrors holds the checks of a schema to the errors listed.
func wantFenceErrors(t *testing.T, schema *ir.Schema, in Input, want ...string) {
	t.Helper()
	if got := fenceErrors(schema, in); !slices.Equal(got, want) {
		t.Errorf("errors =\n%q\nwant\n%q", got, want)
	}
}

// Shorthands for the fixture's parts.
func jobOf(schema *ir.Schema) *ir.TypeDef { return schema.Types["Job"] }

// oneFence leaves the Job one fence, on version, with no covers.
func oneFence(schema *ir.Schema) *ir.TypeDef {
	job := jobOf(schema)
	job.Fences = []*ir.Fence{{Field: "version"}}
	return job
}

// TestFencesVerify: two fences that each list their covers, a relation
// among them, and one fence with none pass, on each integer column.
func TestFencesVerify(t *testing.T) {
	schema, in := fenceSchema()
	wantFenceErrors(t, schema, in)

	schema, in = fenceSchema()
	oneFence(schema)
	wantFenceErrors(t, schema, in)

	for _, scalar := range []string{"Generic.Int32", "Generic.Int16"} {
		schema, in = fenceSchema()
		fieldNamed(jobOf(schema), "version").TypeRef.Name = scalar
		wantFenceErrors(t, schema, in)
	}
}

func TestFenceRefusesAFieldTheTypeLacks(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).Fences[0].Field = "revision"
	wantFenceErrors(t, schema, in, "@fenced on Job.revision: Job has no field revision; name the integer field of Job every covered write advances")
}

func TestFenceRefusesANullableField(t *testing.T) {
	schema, in := fenceSchema()
	fieldNamed(jobOf(schema), "version").Required = false
	wantFenceErrors(t, schema, in, "@fenced on Job.version: version is nullable, and a null has no next value; make it required, as Default<Generic.Int64, 0>")
}

// A fence's column is SMALLINT, INTEGER or BIGINT: text, a float, a list
// of integers, a relation's key and a list of another table's rows are
// none.
func TestFenceRefusesAFieldThatIsNoIntegerColumn(t *testing.T) {
	const what = ", and a fence is a whole number each covered write adds one to; type it by a scalar whose column is SMALLINT, INTEGER or BIGINT, as Default<Generic.Int64, 0>"
	schema, in := fenceSchema()
	fieldNamed(jobOf(schema), "version").TypeRef.Name = "string"
	wantFenceErrors(t, schema, in, "@fenced on Job.version: version's column is TEXT"+what)

	schema, in = fenceSchema()
	fieldNamed(jobOf(schema), "version").TypeRef.Name = "number"
	wantFenceErrors(t, schema, in, "@fenced on Job.version: version's column is DOUBLE PRECISION"+what)

	schema, in = fenceSchema()
	fieldNamed(jobOf(schema), "version").TypeRef.IsArray = true
	wantFenceErrors(t, schema, in, "@fenced on Job.version: version's column is BIGINT[]"+what)

	// A nullable text field is refused for both.
	schema, in = fenceSchema()
	jobOf(schema).Fences[1].Field = "note"
	wantFenceErrors(t, schema, in,
		"@fenced on Job.note: note's column is TEXT"+what,
		"@fenced on Job.note: note is nullable, and a null has no next value; make it required, as Default<Generic.Int64, 0>",
	)

	schema, in = fenceSchema()
	job := oneFence(schema)
	job.Fences[0].Field = "owner"
	fieldNamed(job, "owner").Required = true
	wantFenceErrors(t, schema, in, "@fenced on Job.owner: owner is a relation to Owner, whose column holds a key and no count; name an integer field, as Default<Generic.Int64, 0>")

	schema, in = fenceSchema()
	oneFence(schema).Fences[0].Field = "tasks"
	wantFenceErrors(t, schema, in, "@fenced on Job.tasks: tasks is a list of Task, whose rows are kept in a table of their own, so it stores no column to count in; name an integer field, as Default<Generic.Int64, 0>")
}

func TestFenceRefusesACoveredFieldTheTypeLacks(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).Fences[0].Covers = []string{"label", "title"}
	wantFenceErrors(t, schema, in, "@fenced on Job.version: covers lists title, which is no field of Job; list fields of Job")
}

// A list of another table's rows is kept on that table: no write of the
// Job changes it. A to-one relation is its key's column, and is covered.
func TestFenceRefusesACoveredFieldWithNoColumn(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).Fences[0].Covers = []string{"owner", "tasks"}
	wantFenceErrors(t, schema, in, "@fenced on Job.version: covers lists tasks, a list of Task, whose rows are kept in a table of their own, so no write of Job changes it; leave it out")
}

// The trigger tells a write that changes a covered column with IS DISTINCT
// FROM, which has no POINT to compare.
func TestFenceRefusesACoveredColumnNoEqualityCompares(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).Fences[1].Covers = []string{"claimedBy", "site"}
	wantFenceErrors(t, schema, in, "@fenced on Job.executionVersion: covers lists site, a POINT column, which IS DISTINCT FROM cannot compare, so the fence's trigger could not tell a write that changes it; leave it out")

	// A fence with no covers compares no column, and takes the table.
	schema, in = fenceSchema()
	oneFence(schema)
	wantFenceErrors(t, schema, in)
}

// ownerOnDelete gives the Job's owner the delete action given.
func ownerOnDelete(schema *ir.Schema, action string) {
	fieldNamed(jobOf(schema), "owner").Relation = &ir.RelationDef{Type: "Owner", OnDelete: action}
}

// A SET NULL is an update the database itself makes of each row that
// names a deleted row. It changes a covered column and advances nothing,
// so the trigger would refuse it, and the delete with it: a fence covers
// no such relation.
func TestFenceRefusesACoveredRelationTheDatabaseRewrites(t *testing.T) {
	const what = ", so the database sets owner's column itself when the Owner it names is deleted, an update that advances no fence, which the fence's trigger would refuse, and the delete with it; give the relation another onDelete (CASCADE, RESTRICT or NO ACTION), or leave it out of covers"
	schema, in := fenceSchema()
	ownerOnDelete(schema, "SET NULL")
	wantFenceErrors(t, schema, in, "@fenced on Job.version: covers lists owner, a relation to Owner whose onDelete is SET NULL"+what)

	// The rule lists the actions that leave the column, so one the IR
	// does not have, which ir.Schema.Validate refuses, is held to it too.
	schema, in = fenceSchema()
	ownerOnDelete(schema, "SET DEFAULT")
	wantFenceErrors(t, schema, in, "@fenced on Job.version: covers lists owner, a relation to Owner whose onDelete is SET DEFAULT"+what)

	// A key held in a column of its own, which a relation names the table
	// of, is rewritten the same way.
	schema, in = fenceSchema()
	job := jobOf(schema)
	job.Fields = append(job.Fields, &ir.FieldDef{Name: "reviewerId", TypeRef: ir.TypeRef{Name: "Identity.UUID"},
		Relation: &ir.RelationDef{Type: "Owner", OnDelete: "SET NULL"}})
	job.Fences[0].Covers = []string{"label", "reviewerId"}
	wantFenceErrors(t, schema, in, "@fenced on Job.version: covers lists reviewerId, a relation to Owner whose onDelete is SET NULL, so the database sets reviewerId's column itself when the Owner it names is deleted, an update that advances no fence, which the fence's trigger would refuse, and the delete with it; give the relation another onDelete (CASCADE, RESTRICT or NO ACTION), or leave it out of covers")

	// A relation no fence covers is the database's to clear: the write
	// changes no covered column and keeps both fences.
	schema, in = fenceSchema()
	ownerOnDelete(schema, "SET NULL")
	jobOf(schema).Fences[0].Covers = []string{"label", "priority"}
	wantFenceErrors(t, schema, in)
}

// A fence with no covers counts every update of the row, the database's
// own among them: each relation of the table it rewrites is refused.
func TestFenceRefusesNoCoversBesideARelationTheDatabaseRewrites(t *testing.T) {
	const what = "@fenced on Job.version: the fence lists no covers, so it counts every update of Job, and %[1]s is a relation to Owner whose onDelete is %[2]s, so the database sets %[1]s's column itself when the Owner it names is deleted, an update that advances no fence, which the fence's trigger would refuse, and the delete with it; give the relation another onDelete (CASCADE, RESTRICT or NO ACTION), or list in covers the fields the fence counts, without %[1]s"
	schema, in := fenceSchema()
	oneFence(schema)
	ownerOnDelete(schema, "SET NULL")
	wantFenceErrors(t, schema, in, fmt.Sprintf(what, "owner", "SET NULL"))

	schema, in = fenceSchema()
	job := oneFence(schema)
	ownerOnDelete(schema, "SET NULL")
	job.Fields = append(job.Fields, &ir.FieldDef{Name: "reviewer", TypeRef: ir.TypeRef{Name: "Owner"},
		Relation: &ir.RelationDef{Type: "Owner", OnDelete: "SET DEFAULT"}})
	wantFenceErrors(t, schema, in, fmt.Sprintf(what, "owner", "SET NULL"), fmt.Sprintf(what, "reviewer", "SET DEFAULT"))

	// The same table with the relation left out of a fence's covers.
	schema, in = fenceSchema()
	job = oneFence(schema)
	ownerOnDelete(schema, "SET NULL")
	job.Fences[0].Covers = []string{"label", "priority"}
	wantFenceErrors(t, schema, in)
}

// CASCADE, the default, deletes the row, and RESTRICT and NO ACTION
// refuse the delete: none updates it, so a fence covers such a relation,
// and a fence with no covers stands beside it.
func TestFenceTakesARelationTheDatabaseDoesNotRewrite(t *testing.T) {
	for _, action := range []string{"", "CASCADE", "RESTRICT", "NO ACTION"} {
		schema, in := fenceSchema()
		ownerOnDelete(schema, action)
		wantFenceErrors(t, schema, in)

		schema, in = fenceSchema()
		oneFence(schema)
		ownerOnDelete(schema, action)
		wantFenceErrors(t, schema, in)
	}
}

// A class with no table has no row to fence: reported once for the class,
// whatever its fences.
func TestFenceRefusesAClassThatIsNoTable(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).JsonField = true
	wantFenceErrors(t, schema, in, "@fenced on Job: Job is a @jsonField type, kept as JSON in a column of another table, so it has no row of its own to fence; declare the fence on the table that holds it")

	schema, in = fenceSchema()
	jobOf(schema).Role = ir.RoleEmbeddedStruct
	wantFenceErrors(t, schema, in, "@fenced on Job: Job is no table of the database, so it has no row a write updates; declare the fence on a table")

	schema, in = fenceSchema()
	schema.Types["Batch"] = &ir.TypeDef{Name: "Batch", Role: ir.RoleDBTable, Extends: "Job"}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job is a base class, which has no table of its own; declare the fence on each table that extends it")
}

func TestFenceRefusesASchemaThatIsNoDBService(t *testing.T) {
	for _, kind := range []ir.SchemaKind{ir.SchemaKindAPI, ir.SchemaKindGeneral} {
		schema, in := fenceSchema()
		schema.Kind = kind
		jobOf(schema).Role = ir.RoleEmbeddedStruct
		wantFenceErrors(t, schema, in, "@fenced on Job: a fence is a trigger on a table of a DB service, and jobs-db is a "+string(kind)+" service; declare it on the table, in the DB service's schema")
	}
}

func TestFenceRefusesAQueueClass(t *testing.T) {
	schema, in := fenceSchema()
	job := jobOf(schema)
	job.Role, job.Queue = ir.RoleEmbeddedStruct, &ir.QueueDef{}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job is a @queue class, whose rows are messages the queue's clients write with statements of their own, which advance no fence; declare the fence on a table")
}

// A DB service's engine type has no table for a trigger to watch.
func TestFenceRefusesAnEngineType(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).Behaviors = []ir.BehaviorRef{{Name: "Workflow"}}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job composes a behavior with @behavior, so the engine keeps its instances in tables of its own, which no fence's trigger watches and no repository writes; declare the fence on a table")
}

// A version graph's engines write a member's rows.
func TestFenceRefusesAVersionGraphMember(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).GraphMember = &ir.GraphMemberConfig{Graph: "Plan"}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job is a member of the version graph Plan, whose engines write its rows with statements of their own, which advance no fence; a fence on a version graph's member is not built")

	// The graph's root is a table the repositories write, and keeps its
	// fence.
	schema, in = fenceSchema()
	jobOf(schema).VersionGraph = &ir.VersionGraphConfig{}
	wantFenceErrors(t, schema, in)
}

// The behaviors' core writes the rows of a table that composes a
// behavior, the table's own fields among them, with statements that
// advance no fence. So such a table takes no fence, with covers or
// without, whether its use is anchored on a field or an implements one:
// reported once for the class, whatever its fences.
func TestFenceRefusesATableThatComposesABehavior(t *testing.T) {
	const want = " behavior, so the behaviors' core writes its rows, the table's own fields among them, with statements of its own, which advance no fence; a fence on a table that composes a behavior is not built"
	workflow := ir.BehaviorRef{Name: "Workflow", Field: "status"}
	timers := ir.BehaviorRef{Name: "Timers", Implements: true}

	// Two fences, each over fields of the table's own.
	schema, in := fenceSchema()
	jobOf(schema).Behaviors = []ir.BehaviorRef{workflow}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job composes the Workflow"+want)

	schema, in = fenceSchema()
	jobOf(schema).Behaviors = []ir.BehaviorRef{timers}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job composes the Timers"+want)

	// One fence with no covers. The loader gives such a table its
	// _version (D103), and the class's refusal is the one reported, not
	// the @optimistic rule of a sqlite service.
	schema, in = fenceSchema()
	job := oneFence(schema)
	job.Optimistic = true
	job.Behaviors = []ir.BehaviorRef{workflow}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job composes the Workflow"+want)

	schema, in = fenceSchema()
	oneFence(schema).Behaviors = []ir.BehaviorRef{timers}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job composes the Timers"+want)

	// The first use the class lists is the one named.
	schema, in = fenceSchema()
	jobOf(schema).Behaviors = []ir.BehaviorRef{timers, workflow}
	wantFenceErrors(t, schema, in, "@fenced on Job: Job composes the Timers"+want)
}

// A fence that names or covers a field a behavior owns is on a table
// that composes the behavior, so the class's refusal is the one reported:
// as the loader adds the field to the table, and as the use alone names
// it when the behaviors' checks failed and it was not added.
func TestFenceRefusesAFenceOnOrOverAFieldABehaviorOwns(t *testing.T) {
	const want = "@fenced on Job: Job composes the Workflow behavior, so the behaviors' core writes its rows, the table's own fields among them, with statements of its own, which advance no fence; a fence on a table that composes a behavior is not built"
	config := json.RawMessage(`{"states":["open","closed"],"transitions":[{"from":"open","to":"closed"}]}`)
	status := &ir.FieldDef{Name: "status", TypeRef: ir.TypeRef{Name: "string"}, Required: true,
		Behavior: &ir.FieldBehavior{Use: "status", Behavior: "Workflow", Field: "state", Value: true}}

	schema, in := fenceSchema()
	job := jobOf(schema)
	job.Behaviors = []ir.BehaviorRef{{Name: "Workflow", Field: "status", Config: config}}
	job.Fields = append(job.Fields, status)
	job.Fences[0].Covers = []string{"label", "status"}
	wantFenceErrors(t, schema, in, want)

	schema, in = fenceSchema()
	job = jobOf(schema)
	job.Behaviors = []ir.BehaviorRef{{Name: "Workflow", Field: "status", Config: config}}
	job.Fences[0].Covers = []string{"label", "status"}
	wantFenceErrors(t, schema, in, want)

	// A counter a behavior owns, named as the fence.
	schema, in = fenceSchema()
	job = jobOf(schema)
	job.Behaviors = []ir.BehaviorRef{{Name: "Workflow", Field: "status"}}
	job.Fields = append(job.Fields, &ir.FieldDef{Name: "statusSeq", TypeRef: ir.TypeRef{Name: "Generic.Int64"}, Required: true,
		Behavior: &ir.FieldBehavior{Use: "status", Behavior: "Workflow", Field: "seq"}})
	job.Fences[1].Field = "statusSeq"
	wantFenceErrors(t, schema, in, want)
}

// On SQLite an @optimistic table's bump of _version is an update of its
// own, which a fence with no covers would refuse. The rule reads the
// dialects the service lists.
func TestFenceRefusesNoCoversOnAnOptimisticTableOfASQLiteService(t *testing.T) {
	schema, in := fenceSchema()
	oneFence(schema).Optimistic = true
	wantFenceErrors(t, schema, in, `@fenced on Job.version: Job is @optimistic and jobs-db lists the sqlite dialect, where the bump of _version is an update of its own that sets _version alone, which a fence with no covers would refuse; list in covers the fields the fence counts, or build jobs-db for postgres alone (outputs.sql.dialects: ["postgres"])`)

	// On Postgres alone the bump is part of the write, and the fence
	// stands; so does one with covers on both dialects, and one on a
	// table with no _version.
	wantFenceErrors(t, schema, Input{Dialects: []string{"postgres"}})
	wantFenceErrors(t, schema, Input{})

	schema, in = fenceSchema()
	jobOf(schema).Optimistic = true
	wantFenceErrors(t, schema, in)

	schema, in = fenceSchema()
	oneFence(schema)
	wantFenceErrors(t, schema, in)
}

// TestRunChecksFences: the pass runs the fences' checks.
func TestRunChecksFences(t *testing.T) {
	schema, in := fenceSchema()
	jobOf(schema).Fences[0].Field = "revision"
	res := Run(schema, in)
	const want = "@fenced on Job.revision: Job has no field revision; name the integer field of Job every covered write advances"
	if !slices.ContainsFunc(res.Errors, func(d *Diagnostic) bool { return d.Msg == want }) {
		t.Errorf("Run did not report %q; errors = %v", want, res.Err())
	}
}
