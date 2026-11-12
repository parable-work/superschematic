package loader

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D104's first step: a database whose Question's status
// Workflow names who makes its transitions (by on its askedOf, on its
// createdBy, and beside a permission), with an overridePermission and
// repeat, beside a review Workflow without repeat; and an API that serves
// its behavior operations and an update that takes askedOf.
const (
	principalsDBFixture  = "tsreader/testdata/services/fixture-workflow-principals-db"
	principalsAPIFixture = "tsreader/testdata/services/fixture-workflow-principals-api"
)

// writableAskedOf is the warning the API's reassignQuestion draws: it takes
// askedOf, which status's by names, through a @source(Question) view
// (D104).
const writableAskedOf = "fixture-workflow-principals-api: QuestionAdmin.reassignQuestion takes Question.askedOf (QuestionAssignment is @source(Question)), and the Workflow on Question.status names askedOf in by for open to answered and answered to closed: a caller who may call reassignQuestion can set askedOf to itself and then make that move. Keep askedOf out of the operation's input, or freeze it: list it in the fields of Question's Constants (D104)"

// TestWorkflowPrincipalsFixturesLoad: both fixtures load; the API's one
// warning is that reassignQuestion can write a field status's by names;
// each Workflow's transition takes the state its caller read (from),
// optional and typed as its states as to is, and answers unchanged, an
// optional boolean; its status's config permissions name the
// overridePermission beside the transitions'; and the expanded IR of both
// is held to its golden (D104).
func TestWorkflowPrincipalsFixturesLoad(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(principalsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	question := db.Types["Question"]
	if question == nil || question.IsEngineType() || len(question.FieldBehaviors()) != 2 {
		t.Fatalf("Question = %s", marshal(t, question))
	}
	compareExpanded(t, db, "fixture-workflow-principals-db.expanded.json")

	var warnings []string
	api, err := LoadService(filepath.FromSlash(principalsAPIFixture), WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) }))
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{writableAskedOf}; !slices.Equal(warnings, want) {
		t.Errorf("the API's warnings =\n%q\nwant\n%q", warnings, want)
	}
	for _, use := range []string{"Status", "Review"} {
		state := "Question" + use + "State"
		input, result := api.Types["Question"+use+"TransitionInput"], api.Types["Question"+use+"TransitionResult"]
		if input == nil || result == nil {
			t.Fatalf("Question's %s transition input or result was not added", use)
		}
		var fields []string
		for _, fd := range input.Fields {
			fields = append(fields, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
		}
		if want := []string{"from:" + state + ":optional", "to:" + state + ":required"}; !slices.Equal(fields, want) {
			t.Errorf("%s = %v, want %v", input.Name, fields, want)
		}
		fields = nil
		for _, fd := range result.Fields {
			fields = append(fields, fd.Name+":"+fd.TypeRef.Name+":"+map[bool]string{true: "required", false: "optional"}[fd.Required])
		}
		if want := []string{"from:" + state + ":required", "to:" + state + ":required", "unchanged:boolean:optional"}; !slices.Equal(fields, want) {
			t.Errorf("%s = %v, want %v", result.Name, fields, want)
		}
	}
	permissions := map[string][]string{}
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.BehaviorOperation != nil {
				permissions[set.Name+"."+op.Name] = op.BehaviorOperation.Permissions
			}
		}
	}
	if want := map[string][]string{
		"QuestionActions.statusTransition": {"questions.close", "questions.moderate"},
		"QuestionActions.reviewTransition": nil,
	}; !reflect.DeepEqual(permissions, want) {
		t.Errorf("the operations' config permissions = %v, want %v", permissions, want)
	}
	compareExpanded(t, api, "fixture-workflow-principals-api.expanded.json")
}

// questionsDB is a JSON DB schema whose Question's status Workflow names
// who makes its transitions (D104), for the checks' cases to edit: by on
// askedOf, an Identity.UUID, and on createdBy, its audit field.
func questionsDB() map[string]any {
	return map[string]any{
		"name": "questions-db",
		"kind": "DB",
		"scalars": map[string]any{
			"Identity.UUID":     map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"},
			"Identity.UserID":   map[string]any{"name": "Identity.UserID", "languagePrimitive": "string"},
			"Temporal.DateTime": map[string]any{"name": "Temporal.DateTime", "languagePrimitive": "string"},
		},
		"types": map[string]any{
			"Question": map[string]any{
				"name": "Question",
				"role": "DBTable",
				"behaviors": []any{
					map[string]any{"name": "Workflow", "field": "status", "config": map[string]any{
						"states": []any{"open", "answered", "withdrawn"},
						"transitions": []any{
							map[string]any{"from": "open", "to": "answered", "by": []any{"askedOf"}},
							map[string]any{"from": "open", "to": "withdrawn", "by": []any{"createdBy"}},
						},
						"overridePermission": "questions.moderate",
						"repeat":             "unchanged",
					}},
				},
				"fields": []any{
					questionField("id", "Identity.UUID", map[string]any{"required": true, "key": true, "autoGenerated": true}),
					questionField("prompt", "string", map[string]any{"required": true}),
					questionField("askedOf", "Identity.UUID", nil),
					questionField("createdBy", "Identity.UUID", nil),
				},
			},
		},
	}
}

func questionField(name, typeName string, more map[string]any) map[string]any {
	f := map[string]any{"name": name, "typeRef": map[string]any{"name": typeName}}
	for k, v := range more {
		f[k] = v
	}
	return f
}

// loadQuestions writes the edited schema as a JSON service and loads it.
func loadQuestions(t *testing.T, edit func(doc, question map[string]any)) (*ir.Schema, error) {
	t.Helper()
	doc := questionsDB()
	question := doc["types"].(map[string]any)["Question"].(map[string]any)
	if edit != nil {
		edit(doc, question)
	}
	data, err := json.MarshalIndent(doc, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	dir := writeService(t, map[string]string{
		"schema.config.json":        `{"name": "questions-db", "kind": "DB", "outputs": {}}`,
		"src/questions.schema.json": string(data),
	})
	return LoadService(dir)
}

// statusConfig is Question's status config, to edit.
func statusConfig(question map[string]any) map[string]any {
	return question["behaviors"].([]any)[0].(map[string]any)["config"].(map[string]any)
}

// answeredBy sets the by of status's first transition, open to answered.
func answeredBy(question map[string]any, by any) {
	statusConfig(question)["transitions"].([]any)[0].(map[string]any)["by"] = by
}

// addField appends a field to Question.
func addField(question map[string]any, field map[string]any) {
	question["fields"] = append(question["fields"].([]any), field)
}

// TestWorkflowByIsHeldToTheTable (D104): a by entry names an own field of
// the table that holds one subject, string, Identity.UUID or
// Identity.UserID, a to-one relation's key field among them, or createdBy;
// the build refuses a seat, a relation by its name, another audit field, a
// name that is no field, a list, an encrypted field and a field of another
// type, in these words, the seat, the missing field and the type in the
// core's; and the configSchema refuses a repeat that is not unchanged, an
// empty overridePermission and an empty by, in the registry's words.
func TestWorkflowByIsHeldToTheTable(t *testing.T) {
	const prefix = `type Question: field status: behavior Workflow config: the transition from "open" to "answered" names `
	person := func(doc map[string]any) {
		doc["types"].(map[string]any)["Person"] = map[string]any{"name": "Person", "role": "DBTable", "fields": []any{
			questionField("id", "Identity.UUID", map[string]any{"required": true, "key": true, "autoGenerated": true}),
		}}
	}
	for _, tc := range []struct {
		name string
		edit func(doc, question map[string]any)
		want string
	}{
		{"a seat", func(_, q map[string]any) { answeredBy(q, []any{"seat:reviewer"}) },
			prefix + `"seat:reviewer" in by, a seat of Seats, which Question does not implement`},
		{"a relation by its name", func(doc, q map[string]any) {
			person(doc)
			addField(q, questionField("owner", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			answeredBy(q, []any{"owner"})
		}, prefix + `"owner" in by, a relation: name the field that holds its key, ownerId, when its target's key is a subject (D104)`},
		{"another audit field", func(_, q map[string]any) {
			addField(q, questionField("updatedBy", "Identity.UUID", nil))
			answeredBy(q, []any{"updatedBy"})
		}, prefix + `"updatedBy" in by, an audit field the writes stamp: by takes createdBy among the audit fields (D104)`},
		{"no field", func(_, q map[string]any) { answeredBy(q, []any{"askee"}) },
			prefix + `"askee" in by, which is not a field of Question (its fields: prompt, askedOf, createdBy)`},
		{"a field a behavior owns", func(_, q map[string]any) { answeredBy(q, []any{"statusEnteredAt"}) },
			prefix + `"statusEnteredAt" in by, which is not a field of Question (its fields: prompt, askedOf, createdBy)`},
		{"a list", func(_, q map[string]any) {
			addField(q, map[string]any{"name": "watchers", "typeRef": map[string]any{"name": "string", "isArray": true}})
			answeredBy(q, []any{"watchers"})
		}, prefix + `"watchers" in by, a list: by names a field that holds one subject (string, Identity.UUID or Identity.UserID)`},
		{"an encrypted field", func(_, q map[string]any) {
			addField(q, questionField("delegate", "string", map[string]any{"encryptedAtRest": true}))
			answeredBy(q, []any{"delegate"})
		}, prefix + `"delegate" in by, which is encrypted at rest: the core reads no value of it`},
		{"a number field", func(_, q map[string]any) {
			addField(q, questionField("priority", "number", nil))
			answeredBy(q, []any{"priority"})
		}, prefix + `"priority" in by, a number field of Question: by names a field that holds a subject (string, Identity.UUID or Identity.UserID)`},
		{"a date-time field", func(_, q map[string]any) {
			addField(q, questionField("dueAt", "Temporal.DateTime", nil))
			answeredBy(q, []any{"dueAt"})
		}, prefix + `"dueAt" in by, a Temporal.DateTime field of Question: by names a field that holds a subject (string, Identity.UUID or Identity.UserID)`},
		{"the first problem alone", func(_, q map[string]any) {
			answeredBy(q, []any{"askedOf", "askee", "seat:reviewer"})
		}, prefix + `"askee" in by, which is not a field of Question (its fields: prompt, askedOf, createdBy)`},
		{"a repeat that is not unchanged", func(_, q map[string]any) { statusConfig(q)["repeat"] = "always" },
			`type "Question": behavior Workflow config: jsonschema validation failed with 'superschematic://behaviors/Workflow/config.json#'
- at '/repeat': value must be 'unchanged'`},
		{"an empty overridePermission", func(_, q map[string]any) { statusConfig(q)["overridePermission"] = "" },
			`type "Question": behavior Workflow config: jsonschema validation failed with 'superschematic://behaviors/Workflow/config.json#'
- at '/overridePermission': minLength: got 0, want 1`},
		{"an empty by", func(_, q map[string]any) { answeredBy(q, []any{}) },
			`type "Question": behavior Workflow config: jsonschema validation failed with 'superschematic://behaviors/Workflow/config.json#'
- at '/transitions/0/by': minItems: got 0, want 1`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadQuestions(t, tc.edit)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want an error holding %q", err, tc.want)
			}
			if got := strings.Count(err.Error(), "behavior Workflow config"); got != 1 {
				t.Errorf("LoadService = %v, want one problem of Workflow's config", err)
			}
		})
	}

	for _, tc := range []struct {
		name string
		edit func(doc, question map[string]any)
	}{
		{"the fixture's askedOf and createdBy", nil},
		{"a string field", func(_, q map[string]any) { answeredBy(q, []any{"prompt"}) }},
		{"an Identity.UserID field", func(_, q map[string]any) {
			addField(q, questionField("reviewer", "Identity.UserID", nil))
			answeredBy(q, []any{"reviewer", "createdBy"})
		}},
		{"a relation's key", func(doc, q map[string]any) {
			person(doc)
			addField(q, questionField("owner", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			answeredBy(q, []any{"ownerId"})
		}},
	} {
		t.Run("accepts "+tc.name, func(t *testing.T) {
			if _, err := loadQuestions(t, tc.edit); err != nil {
				t.Fatalf("LoadService = %v", err)
			}
		})
	}
}

// TestEngineTypesRefuseWhatOnlyATablesWorkflowTakes (D104, D113): an engine
// type's @behavior("Workflow", ...) naming a transition's by, an
// overridePermission, repeat or finished, which the shared declaration's
// configSchema accepts, is refused, one problem per member, naming the
// table form, as the engine refuses each.
func TestEngineTypesRefuseWhatOnlyATablesWorkflowTakes(t *testing.T) {
	const member = "type Note: @behavior Workflow names %s, which a table's Workflow takes (status: Workflow<{ ... }> on a DB table, D104); an engine type's Workflow takes none of by, overridePermission, repeat and finished, so leave it out"
	engineType := func(config map[string]any) func(doc, _ map[string]any) {
		return func(doc, _ map[string]any) {
			doc["types"].(map[string]any)["Note"] = map[string]any{"name": "Note", "role": "DBTable", "behaviors": []any{
				map[string]any{"name": "Workflow", "config": config},
			}, "fields": []any{questionField("id", "Identity.UUID", map[string]any{"required": true, "key": true})}}
		}
	}
	transitions := func(by ...any) []any {
		out := []any{}
		for i, entry := range by {
			move := map[string]any{"from": []string{"draft", "sent"}[i], "to": []string{"sent", "read"}[i]}
			if entry != nil {
				move["by"] = entry
			}
			out = append(out, move)
		}
		return out
	}
	states := []any{"draft", "sent", "read"}
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   []string
	}{
		{"by", map[string]any{"states": states, "transitions": transitions([]any{"author"}, nil)},
			[]string{`by on the transition from "draft" to "sent"`}},
		{"by on two transitions", map[string]any{"states": states, "transitions": transitions([]any{"author"}, []any{"reader"})},
			[]string{`by on the transition from "draft" to "sent"`, `by on the transition from "sent" to "read"`}},
		{"overridePermission", map[string]any{"states": states, "transitions": transitions(nil), "overridePermission": "notes.moderate"},
			[]string{"overridePermission"}},
		{"repeat", map[string]any{"states": states, "transitions": transitions(nil), "repeat": "unchanged"},
			[]string{"repeat"}},
		{"finished", map[string]any{"states": states, "transitions": transitions(nil, nil), "finished": map[string]any{"sent": "success"}},
			[]string{"finished"}},
		{"all four", map[string]any{"states": states, "transitions": transitions([]any{"author"}, nil), "overridePermission": "notes.moderate", "repeat": "unchanged", "finished": map[string]any{"sent": "success"}},
			[]string{`by on the transition from "draft" to "sent"`, "overridePermission", "repeat", "finished"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := loadQuestions(t, engineType(tc.config))
			if err == nil {
				t.Fatal("LoadService succeeded")
			}
			last := -1
			for _, m := range tc.want {
				want := strings.Replace(member, "%s", m, 1)
				at := strings.Index(err.Error(), want)
				if at < 0 || at < last {
					t.Fatalf("LoadService = %v, want %q, after the problems before it", err, want)
				}
				last = at
			}
			if got := strings.Count(err.Error(), "@behavior Workflow names"); got != len(tc.want) {
				t.Errorf("LoadService = %v, want %d problems", err, len(tc.want))
			}
		})
	}
	// An engine type's Workflow without them loads.
	if _, err := loadQuestions(t, engineType(map[string]any{"states": states, "transitions": transitions(nil)})); err != nil {
		t.Errorf("an engine type's Workflow without the table's members: %v", err)
	}
}

// TestWritableByWarnsOfAnUpdateOfAByField (D104): the build warns of each
// operation, use and field when an API has an operation that updates a row
// of a table (a PUT or a PATCH, or another method but GET that takes the
// table's key) and takes a @source view of the table carrying a field a
// Workflow transition's by names, and of nothing else: not of createdBy,
// which no input carries, not of a GET, not of a create, which takes no
// key, and not of an operation whose input is no view of the table. It is a
// warning: the API loads. A set of the table's behavior operations whose
// every required permission covers the Workflow's overridePermission draws
// a warning too, since by then names no one its routes admit.
func TestWritableByWarnsOfAnUpdateOfAByField(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(principalsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	load := WithDependencyLoader(func(name string) (*ir.Schema, error) {
		if name != db.Name {
			return nil, errors.New("no such service")
		}
		return db, nil
	})
	const view = `{"name": "QuestionEdit", "role": "APIView", "source": {"target": "fixture-workflow-principals-db.Question", "omittedFromSource": ["id", "prompt"]},
	  "fields": [{"name": "askedOf", "typeRef": {"name": "Identity.UUID"}, "required": true}, {"name": "createdBy", "typeRef": {"name": "Identity.UUID"}, "required": true}]}`
	const plain = `{"name": "QuestionNote", "role": "APIInput", "fields": [{"name": "askedOf", "typeRef": {"name": "Identity.UUID"}, "required": true}]}`
	operation := func(name, method, input string) string {
		return `{"name": "` + name + `", "typeRef": {"name": "Question"}, "required": true, "httpMethod": "` + method + `", "restPath": "questions/{id}/` + name + `",
		  "arguments": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true}, {"name": "input", "typeRef": {"name": "` + input + `"}, "required": true}]}`
	}
	actions := func(permissions string) string {
		return `{"name": "QuestionActions", "operations": [], "behaviorOperations": {"type": "Question"}, "auth": true` + permissions + `}`
	}
	edits := `{"name": "QuestionEdits", "operations": [` + operation("edit", "PUT", "QuestionEdit") + `, ` + operation("reassign", "POST", "QuestionEdit") + `, ` +
		operation("note", "POST", "QuestionNote") + `, ` + operation("look", "GET", "QuestionEdit") + `, ` +
		`{"name": "peek", "typeRef": {"name": "QuestionEdit"}, "required": true, "httpMethod": "GET", "restPath": "questions/{id}/peek", "arguments": [{"name": "id", "typeRef": {"name": "Identity.UUID"}, "required": true}]}, ` +
		`{"name": "ask", "typeRef": {"name": "Question"}, "required": true, "httpMethod": "POST", "restPath": "questions", "arguments": [{"name": "input", "typeRef": {"name": "QuestionEdit"}, "required": true}]}]}`
	write := func(sets string) string {
		dir := filepath.Join(t.TempDir(), "questions-api")
		for rel, contents := range map[string]string{
			"schema.config.json": `{"name": "questions-api", "kind": "API", "dependencies": [{"name": "fixture-workflow-principals-db", "kind": "DB"}], "outputs": {}}`,
			"src/questions.schema.json": `{"imports": [{"package": "@schemas/fixture-workflow-principals-db", "types": ["Question"]}],
			  "scalars": {"Identity.UUID": {"name": "Identity.UUID", "languagePrimitive": "string"}},
			  "types": {"QuestionEdit": ` + view + `, "QuestionNote": ` + plain + `}, "operationSets": [` + sets + `]}`,
		} {
			path := filepath.Join(dir, filepath.FromSlash(rel))
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		return dir
	}
	writable := func(op string) string {
		return "questions-api: QuestionEdits." + op + " takes Question.askedOf (QuestionEdit is @source(Question)), and the Workflow on Question.status names askedOf in by for open to answered and answered to closed: a caller who may call " + op + " can set askedOf to itself and then make that move. Keep askedOf out of the operation's input, or freeze it: list it in the fields of Question's Constants (D104)"
	}
	covered := func(held string) string {
		return "QuestionActions: every caller its routes admit holds " + held + " questions.moderate, the overridePermission of the Workflow on Question.status, so each moves its rows past every transition's by, which names no one for them; require a permission that does not cover questions.moderate, or name another overridePermission (D104)"
	}
	for _, tc := range []struct {
		name, sets string
		want       []string
	}{
		{"an update through a view", actions("") + ", " + edits, []string{writable("edit"), writable("reassign")}},
		{"a permission that covers overridePermission", actions(`, "permissions": ["questions"]`), []string{covered("questions, which covers")}},
		{"permissions that each cover it", actions(`, "permissions": ["questions", "questions.moderate"]`), []string{covered("one of questions, questions.moderate, each of which covers")}},
		{"a permission that does not cover it", actions(`, "permissions": ["questions.close"]`), nil},
		{"a permission of several that does not", actions(`, "permissions": ["questions", "support"]`), nil},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var warnings []string
			if _, err := LoadService(write(tc.sets), load, WithWarnings(func(d *verify.Diagnostic) {
				if strings.Contains(d.Msg, "(D104)") {
					warnings = append(warnings, d.Msg)
				}
			})); err != nil {
				t.Fatalf("LoadService: %v", err)
			}
			if !slices.Equal(warnings, tc.want) {
				t.Errorf("warnings =\n%q\nwant\n%q", warnings, tc.want)
			}
		})
	}
}

// TestWritableByWarnsWithoutTheTablesOperations (D104): an API in
// TypeScript that serves none of a table's behavior operations, and so
// reads no database, still warns of an update that takes a field a by of
// the table names: the compiler resolves the @source target with its
// behaviors' configs. A PATCH and a POST that takes the table's key are
// updates; a POST that takes no key is a create, not warned of.
func TestWritableByWarnsWithoutTheTablesOperations(t *testing.T) {
	root := testpaths.RepoRoot(t)
	base := filepath.ToSlash(filepath.Join(root, "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "questions-admin-api")
	for rel, contents := range map[string]string{
		"schema.config.json": `{"name": "questions-admin-api", "kind": "API", "dependencies": [{"name": "fixture-workflow-principals-db", "kind": "DB"}], "outputs": {}}`,
		"tsconfig.json":      `{"extends": "` + base + `", "include": ["src/**/*.ts"]}`,
		"src/questions.schema.ts": `import { Identity } from "superscalar";
import { HttpMethod, requirePermission, rest, source } from "@superschematic/api";
import { Question } from "@schemas/fixture-workflow-principals-db";

@source(Question)
export abstract class QuestionAssignment {
  askedOf: Identity.UUID;
}

@source(Question)
export abstract class QuestionDraft {
  prompt: string;
  askedOf: Identity.UUID;
}

export class QuestionSupport {
  @requirePermission(["support"])
  @rest(HttpMethod.PATCH, "questions/{id}/asked-of")
  reassign(id: Identity.UUID, input: QuestionAssignment): Question {
    throw new Error("schema declaration only");
  }

  @requirePermission(["support"])
  @rest(HttpMethod.POST, "questions/{id}/hand-over")
  handOver(id: Identity.UUID, input: QuestionAssignment): Question {
    throw new Error("schema declaration only");
  }

  @requirePermission(["support"])
  @rest(HttpMethod.POST, "questions")
  ask(input: QuestionDraft): Question {
    throw new Error("schema declaration only");
  }
}
`,
	} {
		path := filepath.Join(dir, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var warnings []string
	if _, err := LoadService(dir, WithWarnings(func(d *verify.Diagnostic) {
		if strings.Contains(d.Msg, "(D104)") {
			warnings = append(warnings, d.Msg)
		}
	})); err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	writable := func(op string) string {
		return "questions-admin-api: QuestionSupport." + op + " takes Question.askedOf (QuestionAssignment is @source(Question)), and the Workflow on Question.status names askedOf in by for open to answered and answered to closed: a caller who may call " + op + " can set askedOf to itself and then make that move. Keep askedOf out of the operation's input, or freeze it: list it in the fields of Question's Constants (D104)"
	}
	if want := []string{writable("reassign"), writable("handOver")}; !slices.Equal(warnings, want) {
		t.Errorf("warnings =\n%q\nwant\n%q", warnings, want)
	}
}
