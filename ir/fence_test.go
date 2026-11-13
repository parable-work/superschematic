package ir

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// jobSchema is a DB schema with a Job two parties write: a planner, whose
// writes of label and priority advance version, and a worker, whose writes
// of claimedBy and progress advance executionVersion (D118).
func jobSchema() *Schema {
	s := NewSchema("jobs-db", SchemaKindDB)
	s.Scalars["Identity.UUID"] = &ScalarDef{Name: "Identity.UUID", LanguagePrimitive: LanguageString}
	s.Scalars["Identity.Name"] = &ScalarDef{Name: "Identity.Name", LanguagePrimitive: LanguageString}
	s.Scalars["Generic.Int64"] = &ScalarDef{Name: "Generic.Int64", LanguagePrimitive: LanguageNumber}
	s.Enums["JobState"] = &EnumDef{Name: "JobState", Values: []EnumValueDef{{Name: "QUEUED"}, {Name: "DONE"}}}
	zero := "0"
	s.Types["Job"] = &TypeDef{
		Name: "Job",
		Role: RoleDBTable,
		Fields: []*FieldDef{
			{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Required: true, Key: true},
			{Name: "label", TypeRef: TypeRef{Name: "Identity.Name"}, Required: true},
			{Name: "priority", TypeRef: TypeRef{Name: "Generic.Int64"}, Required: true, Default: &zero},
			{Name: "version", TypeRef: TypeRef{Name: "Generic.Int64"}, Required: true, Default: &zero},
			{Name: "claimedBy", TypeRef: TypeRef{Name: "Identity.Name"}},
			{Name: "progress", TypeRef: TypeRef{Name: "Generic.Int64"}, Required: true, Default: &zero},
			{Name: "executionVersion", TypeRef: TypeRef{Name: "Generic.Int64"}, Required: true, Default: &zero},
			{Name: "state", TypeRef: TypeRef{Name: "JobState"}, Required: true},
		},
		Fences: []*Fence{
			{Field: "version", Covers: []string{"label", "priority"}},
			{Field: "executionVersion", Covers: []string{"claimedBy", "progress"}},
		},
	}
	return s
}

// planFence and executionFence are the fixture's two fences.
func planFence(s *Schema) *Fence      { return s.Types["Job"].Fences[0] }
func executionFence(s *Schema) *Fence { return s.Types["Job"].Fences[1] }

func TestFenceDeclarationsValidate(t *testing.T) {
	if errs := jobSchema().Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want no errors", errs)
	}

	// One fence with no covers counts every update of the row.
	s := jobSchema()
	s.Types["Job"].Fences = []*Fence{{Field: "version"}}
	if errs := s.Validate(); len(errs) != 0 {
		t.Fatalf("Validate() = %v, want no errors", errs)
	}
}

func TestFenceRefusesAFenceWithNoField(t *testing.T) {
	s := jobSchema()
	planFence(s).Field = ""
	wantOneError(t, s.Validate(), "@fenced 0 on Job names no field; name the integer field every covered write advances")

	s = jobSchema()
	s.Types["Job"].Fences[1] = nil
	wantOneError(t, s.Validate(), "@fenced 1 on Job is null; write { field }, or { field, covers }")
}

func TestFenceRefusesTwoFencesOnAField(t *testing.T) {
	s := jobSchema()
	executionFence(s).Field = "version"
	wantOneError(t, s.Validate(), "@fenced on Job.version: Job declares two fences on version; a field is one fence, so list what it covers in one declaration")
}

// A field's value is a state a machine moves or a count a fence advances.
func TestFenceRefusesAFieldAStateMachineGoverns(t *testing.T) {
	s := jobSchema()
	job := s.Types["Job"]
	job.Fences = []*Fence{{Field: "state"}}
	job.StateMachines = []*StateMachine{{Field: "state", Transitions: []*StateTransition{{From: "QUEUED", To: []string{"DONE"}}}, Terminal: []string{"DONE"}}}
	wantOneError(t, s.Validate(), "@fenced on Job.state: state is the field a @stateMachine of Job governs, which holds a state and no count; name an integer field of its own")
}

func TestFenceRefusesSeveralFencesWhereOneHasNoCovers(t *testing.T) {
	s := jobSchema()
	executionFence(s).Covers = nil
	wantOneError(t, s.Validate(), "@fenced on Job.executionVersion: Job has more than one fence, and this one lists no covers, so every update would advance it, a write of another fence's fields among them; list in covers the fields each fence counts")
}

// An empty covers is refused, alone and beside another fence: the IR's
// JSON leaves it out, and the fence read back would count every update.
func TestFenceRefusesAnEmptyCovers(t *testing.T) {
	const want = "@fenced on Job.version: covers is empty, so the fence would count no write; list the fields whose change it counts, or leave covers out and it counts every update of the row"
	s := jobSchema()
	planFence(s).Covers = []string{}
	wantOneError(t, s.Validate(), want)

	s = jobSchema()
	s.Types["Job"].Fences = []*Fence{{Field: "version", Covers: []string{}}}
	wantOneError(t, s.Validate(), want)
}

func TestFenceRefusesACoveredFieldListedTwice(t *testing.T) {
	s := jobSchema()
	planFence(s).Covers = []string{"label", "priority", "label"}
	wantOneError(t, s.Validate(), "@fenced on Job.version: covers lists label twice; list each field once")
}

func TestFenceRefusesCoveringAFence(t *testing.T) {
	s := jobSchema()
	planFence(s).Covers = []string{"label", "version"}
	wantOneError(t, s.Validate(), "@fenced on Job.version: covers lists version, the fence itself, which a covered write advances; leave it out")

	s = jobSchema()
	planFence(s).Covers = []string{"label", "executionVersion"}
	wantOneError(t, s.Validate(), "@fenced on Job.version: covers lists executionVersion, the field of another fence of Job, which that fence's writes advance; leave it out")
}

func TestFenceRefusesAFieldTwoFencesCover(t *testing.T) {
	s := jobSchema()
	executionFence(s).Covers = []string{"claimedBy", "priority"}
	wantOneError(t, s.Validate(), "@fenced on Job.executionVersion: covers lists priority, which the fence on Job.version covers; one fence covers a field, so a write of it advances one counter")
}

func TestFenceRefusesAnEmptyCoveredField(t *testing.T) {
	s := jobSchema()
	planFence(s).Covers = []string{"label", ""}
	wantOneError(t, s.Validate(), "@fenced on Job.version: covers names an empty field; name a field of Job")
}

func TestFenceOfFindsTheFieldsFence(t *testing.T) {
	td := jobSchema().Types["Job"]
	if f := td.FenceOf("executionVersion"); f == nil || f.Field != "executionVersion" || f.CoversRow() {
		t.Fatalf("FenceOf(executionVersion) = %+v", f)
	}
	if f := td.FenceOf("label"); f != nil {
		t.Fatalf("FenceOf(label) = %+v, want none: a covered field is no fence", f)
	}
	if !(&Fence{Field: "version"}).CoversRow() {
		t.Fatal("a fence with no covers does not cover the row")
	}
	var none *Fence
	if none.CoversRow() || (*TypeDef)(nil).FenceOf("version") != nil {
		t.Fatal("no fence covers a row, and no type has one")
	}
}

// A fence is written under fences, with the keys D118 names, in JSON and
// in YAML, and reads back as it was; one with no covers writes its field
// alone.
func TestFenceJSONAndYAMLRoundTrip(t *testing.T) {
	td := jobSchema().Types["Job"]
	raw, err := json.Marshal(td.Fences)
	if err != nil {
		t.Fatal(err)
	}
	const want = `[{"field":"version","covers":["label","priority"]},{"field":"executionVersion","covers":["claimedBy","progress"]}]`
	if string(raw) != want {
		t.Fatalf("JSON = %s\nwant   %s", raw, want)
	}
	if raw, err = json.Marshal(&Fence{Field: "version"}); err != nil || string(raw) != `{"field":"version"}` {
		t.Fatalf("JSON of a fence with no covers = %s, %v", raw, err)
	}
	whole, err := json.Marshal(td)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(whole), `"fences":[{"field":"version"`) {
		t.Fatalf("TypeDef JSON has no fences: %s", whole)
	}
	var fromJSON TypeDef
	if err := json.Unmarshal(whole, &fromJSON); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromJSON.Fences, td.Fences) {
		t.Fatalf("JSON round trip = %+v", fromJSON.Fences)
	}
	text, err := yaml.Marshal(td)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(text), "fences:\n") {
		t.Fatalf("TypeDef YAML has no fences:\n%s", text)
	}
	var fromYAML TypeDef
	if err := yaml.Unmarshal(text, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(fromYAML.Fences, td.Fences) {
		t.Fatalf("YAML round trip = %+v\n%s", fromYAML.Fences, text)
	}
}
