package verify_test

import (
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// constantsSource is the core's Constants, whose ONE_WAY_BOOLEANS and
// ONE_WAY_NUMBERS are the types a oneWay field may have (D104).
const constantsSource = "runtime/engine/rust/rules-behaviors/src/constants.rs"

// TestOneWayTypesAreTheCores (D104): the build's oneWay types are the
// core's ONE_WAY_BOOLEANS and ONE_WAY_NUMBERS, in order, so the build
// refuses the oneWay fields the core refuses at a command, in the same
// words, and accepts the rest.
func TestOneWayTypesAreTheCores(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), filepath.FromSlash(constantsSource)))
	if err != nil {
		t.Fatal(err)
	}
	for name, build := range map[string][]string{"ONE_WAY_BOOLEANS": verify.OneWayBooleans, "ONE_WAY_NUMBERS": verify.OneWayNumbers} {
		match := regexp.MustCompile(`pub const ` + name + `: \[&str; \d+\] = \[([^\]]*)\];`).FindSubmatch(data)
		if match == nil {
			t.Errorf("%s declares no %s", constantsSource, name)
			continue
		}
		var core []string
		for _, quoted := range regexp.MustCompile(`"([^"]*)"`).FindAllSubmatch(match[1], -1) {
			core = append(core, string(quoted[1]))
		}
		if !slices.Equal(build, core) {
			t.Errorf("the build's %s = %v, the core's = %v (%s)", name, build, core, constantsSource)
		}
	}
}

// TestConstantsConfigIsHeldToTheTable (D104): each of a table's Constants'
// fields, and each oneWay member, names an own field of the table, in the
// core's words for a field the table lacks; a oneWay field is not in
// fields and holds a boolean, a number or a date-time, in the core's words;
// and the build refuses, in its own, a field a behavior owns, a relation by
// its name, an audit field, a oneWay list, a Generic.Decimal, and a field
// the core reads no value of (encrypted at rest, a list of date-times) in
// fields or oneWay. The first problem alone is reported: fields in order,
// then oneWay in the canonical config's order. The configSchema refuses a
// direction that is neither up nor down and an empty oneWay, in the
// registry's words.
func TestConstantsConfigIsHeldToTheTable(t *testing.T) {
	const prefix = "type Task: behavior Constants config: "
	constants := func(task map[string]any) map[string]any { return config(task, "Constants") }
	oneWay := func(moves map[string]any) func(_, task map[string]any) {
		return func(_, task map[string]any) { constants(task)["oneWay"] = moves }
	}
	holdRefusals(t, "Constants", []refusalCase{
		{"a field the table lacks", func(_, task map[string]any) { constants(task)["fields"] = []any{"owner", "assignee"} },
			prefix + "fields: assignee is not a field of Task (its fields: " + taskFields + ")"},
		{"a field Workflow owns", func(_, task map[string]any) { constants(task)["fields"] = []any{"statusEnteredAt"} },
			prefix + "fields: statusEnteredAt is a field Workflow owns, which only its operations change: Constants keeps the table's own fields"},
		{"the status", func(_, task map[string]any) { constants(task)["fields"] = []any{"status"} },
			prefix + "fields: status is a field Workflow owns, which only its operations change: Constants keeps the table's own fields"},
		{"a relation by its name", func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			constants(task)["fields"] = []any{"sponsor"}
		}, prefix + "fields: sponsor is a relation: name the field that holds its key, sponsorId"},
		{"an audit field", func(_, task map[string]any) { constants(task)["fields"] = []any{"createdBy"} },
			prefix + "fields: createdBy is an audit field the writes stamp, which no update changes"},
		{"a field encrypted at rest", func(_, task map[string]any) {
			addField(task, taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
			constants(task)["fields"] = []any{"owner", "secret"}
		}, prefix + "fields: secret is encrypted at rest: the core reads no value of it"},
		{"a list of date-times", func(_, task map[string]any) {
			addField(task, map[string]any{"name": "slots", "typeRef": map[string]any{"name": "Temporal.DateTime", "isArray": true}})
			constants(task)["fields"] = []any{"slots"}
		}, prefix + "fields: slots is a list of date-times, which no adapter converts: the core reads no value of it"},
		{"a oneWay field the table lacks", oneWay(map[string]any{"retries": "up"}),
			prefix + "oneWay: retries is not a field of Task (its fields: " + taskFields + ")"},
		{"a oneWay field fields lists", func(_, task map[string]any) {
			addField(task, taskField("rank", "number", nil))
			constants(task)["fields"] = []any{"owner", "rank"}
			constants(task)["oneWay"] = map[string]any{"rank": "up"}
		}, prefix + "oneWay: rank is in fields, which keep the value their create gives them: a field moves one way or stays, not both"},
		{"a oneWay string", oneWay(map[string]any{"title": "up"}),
			prefix + "oneWay: title is a string field of Task: a field that moves one way holds a boolean, a number or a date-time"},
		{"a oneWay subject", oneWay(map[string]any{"reviewer": "down"}),
			prefix + "oneWay: reviewer is a Identity.UUID field of Task: a field that moves one way holds a boolean, a number or a date-time"},
		{"a oneWay list", func(_, task map[string]any) {
			addField(task, map[string]any{"name": "scores", "typeRef": map[string]any{"name": "number", "isArray": true}})
			constants(task)["oneWay"] = map[string]any{"scores": "up"}
		}, prefix + "oneWay: scores is a list: a field that moves one way holds one boolean, number or date-time"},
		{"a oneWay decimal", func(_, task map[string]any) {
			addField(task, taskField("budget", "Generic.Decimal", nil))
			constants(task)["oneWay"] = map[string]any{"budget": "down"}
		}, prefix + "oneWay: budget is a Generic.Decimal field of Task, which reaches the core as text: a field that moves one way holds a boolean, a number or a date-time"},
		{"a oneWay field encrypted at rest", func(_, task map[string]any) {
			addField(task, taskField("secret", "string", map[string]any{"encryptedAtRest": true}))
			constants(task)["oneWay"] = map[string]any{"secret": "up"}
		}, prefix + "oneWay: secret is encrypted at rest: the core reads no value of it"},
		{"a oneWay field Workflow owns", oneWay(map[string]any{"statusEnteredAt": "up"}),
			prefix + "oneWay: statusEnteredAt is a field Workflow owns, which only its operations change: Constants keeps the table's own fields"},
		{"a oneWay audit field", oneWay(map[string]any{"createdBy": "up"}),
			prefix + "oneWay: createdBy is an audit field the writes stamp, which no update changes"},
		{"fields before oneWay", func(_, task map[string]any) {
			constants(task)["fields"] = []any{"assignee"}
			constants(task)["oneWay"] = map[string]any{"title": "up"}
		}, prefix + "fields: assignee is not a field of Task (its fields: " + taskFields + ")"},
		{"oneWay in the canonical order", oneWay(map[string]any{"zone": "up", "title": "up", "urgent": "up"}),
			prefix + "oneWay: title is a string field of Task: a field that moves one way holds a boolean, a number or a date-time"},
		{"a direction neither up nor down", oneWay(map[string]any{"urgent": "sideways"}),
			`type "Task": behavior Constants config: jsonschema validation failed with 'superschematic://behaviors/Constants/config.json#'
- at '/oneWay/urgent': value must be one of 'up', 'down'`},
		{"an empty oneWay", oneWay(map[string]any{}),
			`type "Task": behavior Constants config: jsonschema validation failed with 'superschematic://behaviors/Constants/config.json#'
- at '/oneWay': minProperties: got 0, want 1`},
	})
	holdAccepted(t, map[string]func(doc, task map[string]any){
		"the fixture's urgent and attempts": nil,
		"a boolean and a number down, and a date-time": func(_, task map[string]any) {
			addField(task, taskField("dueAt", "Temporal.DateTime", nil))
			constants(task)["oneWay"] = map[string]any{"urgent": "down", "attempts": "down", "dueAt": "up"}
		},
		"an Int64 and a Float64": func(_, task map[string]any) {
			addField(task, taskField("budget", "Generic.Int64", nil))
			addField(task, taskField("score", "Generic.Float64", nil))
			constants(task)["oneWay"] = map[string]any{"budget": "down", "score": "up"}
		},
		"a relation's key": func(doc, task map[string]any) {
			person(doc)
			addField(task, taskField("sponsor", "Person", map[string]any{"relation": map[string]any{"type": "Person"}}))
			constants(task)["fields"] = []any{"owner", "sponsorId"}
		},
		"no oneWay": func(_, task map[string]any) { delete(constants(task), "oneWay") },
		"a list of strings": func(_, task map[string]any) {
			addField(task, map[string]any{"name": "tags", "typeRef": map[string]any{"name": "string", "isArray": true}})
			constants(task)["fields"] = []any{"owner", "tags"}
		},
	})
}
