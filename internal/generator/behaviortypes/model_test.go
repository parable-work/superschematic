package behaviortypes_test

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/behaviortypes/behaviortypestest"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

var update = flag.Bool("update", false, "rewrite testdata/fixture-behavior-types.json")

// modelFile is the model of a type as the golden holds it: the structs and
// enums the languages that name their types write.
type modelFile struct {
	Name      string                 `json:"name"`
	Behaviors string                 `json:"behaviors"`
	Structs   []behaviortypes.Struct `json:"structs"`
	Enums     []behaviortypes.Enum   `json:"enums"`
}

// TestBuildGolden keeps testdata/fixture-behavior-types.json equal to the
// model Build makes of the fixture's types. Regenerate with:
// go test ./internal/generator/behaviortypes -run TestBuildGolden -update
func TestBuildGolden(t *testing.T) {
	reg := behaviortypestest.Registry(t)
	types, err := behaviortypes.Build(behaviortypestest.Schema(t, reg), reg)
	if err != nil {
		t.Fatal(err)
	}
	files := make([]modelFile, 0, len(types))
	for _, typ := range types {
		files = append(files, modelFile{Name: typ.Name, Behaviors: typ.BehaviorsName, Structs: typ.Structs, Enums: typ.Enums})
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(files); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join("testdata", "fixture-behavior-types.json")
	if *update {
		if err := os.MkdirAll("testdata", 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
			t.Fatal(err)
		}
		return
	}
	committed, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (run with -update)", path, err)
	}
	if !bytes.Equal(committed, buf.Bytes()) {
		t.Errorf("%s is stale; run go test ./internal/generator/behaviortypes -run TestBuildGolden -update", path)
	}
}

// MemberName is the rule every language that cannot hold the JSON key
// names a behavior, a field, a config's key and an enum's value by.
func TestMemberName(t *testing.T) {
	for name, want := range map[string]string{
		"Workflow":      "Workflow",
		"acme.Rating":   "AcmeRating",
		"my-ext.Score":  "MyExtScore",
		"in-progress":   "InProgress",
		"in_review":     "InReview",
		"invalidOutput": "InvalidOutput",
		"cpuSeconds":    "CpuSeconds",
		"1st":           "N1st",
		"-":             "Value",
		"":              "Value",
	} {
		if got := behaviortypes.MemberName(name); got != want {
			t.Errorf("MemberName(%q) = %q, want %q", name, got, want)
		}
	}
}

// loadDocument loads one General schema document from its JSON text with
// the fixture's registry.
func loadDocument(t *testing.T, text string) (*ir.Schema, *registry.Registry) {
	t.Helper()
	reg := behaviortypestest.Registry(t)
	doc, err := jsonreader.ReadWith([]byte(text), "doc.schema.json", reg)
	if err != nil {
		t.Fatal(err)
	}
	schema, err := loader.LoadDocument(doc, "doc.schema.json", loader.WithRegistry(reg))
	if err != nil {
		t.Fatal(err)
	}
	return schema, reg
}

// withBehaviors is a schema whose type Item composes the behaviors given,
// beside the types extra declares (members of the types object, each with
// a trailing comma) and the enums enums declares.
func withBehaviors(behaviors, extra, enums string) string {
	if enums == "" {
		enums = "{}"
	}
	return `{"kind": "General", "name": "items", "enums": ` + enums + `, "types": {` + extra + `
  "Item": {"name": "Item", "role": "EmbeddedStruct", "behaviors": ` + behaviors + `,
    "fields": [{"name": "title", "typeRef": {"name": "string"}, "required": true}]}}}`
}

const workflow = `{"name": "Workflow", "config": {"states": %s, "transitions": [{"from": "a", "to": "b"}]}}`

// Build refuses a name it would write that the schema declares, and two
// members of a struct or an enum that one name would take; a schema with
// no behavior has no model, and one with behaviors needs a registry.
func TestBuildRefusesCollidingNames(t *testing.T) {
	for _, tc := range []struct {
		name, behaviors, extra, enums, want string
	}{
		{
			name:      "a type the schema declares",
			behaviors: `[` + fmt.Sprintf(workflow, `["a", "b"]`) + `]`,
			extra:     `"ItemWorkflow": {"name": "ItemWorkflow", "role": "EmbeddedStruct", "fields": [{"name": "w", "typeRef": {"name": "number"}}]},`,
			want:      "the behaviors of type Item would name a type ItemWorkflow, which the schema's type ItemWorkflow already names",
		},
		{
			name:      "an enum the schema declares",
			behaviors: `[` + fmt.Sprintf(workflow, `["a", "b"]`) + `]`,
			enums:     `{"ItemState": {"name": "ItemState", "values": [{"name": "x"}]}}`,
			want:      "the behaviors of type Item would name a type ItemState, which the schema's enum ItemState already names",
		},
		{
			name:      "two states of one name",
			behaviors: `[` + fmt.Sprintf(workflow, `["a", "b", "in-review", "inReview"]`) + `]`,
			want:      "type Item: behavior Workflow: field status: the values in-review and inReview of ItemState would both be named InReview",
		},
		{
			name: "two deadlines of one name",
			behaviors: `[{"name": "Timers", "config": {"deadlines": {
				"re-spond": {"start": {"create": true}, "afterMs": 1000},
				"reSpond": {"start": {"create": true}, "afterMs": 2000}}}}]`,
			want: "type Item: behavior Timers: field deadlines: re-spond and reSpond would both be the member ReSpond of ItemTimersDeadlines",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema, reg := loadDocument(t, withBehaviors(tc.behaviors, tc.extra, tc.enums))
			_, err := behaviortypes.Build(schema, reg)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Build: err = %v, want %q", err, tc.want)
			}
		})
	}

	plain, reg := loadDocument(t, `{"kind": "General", "name": "items", "types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "fields": [{"name": "title", "typeRef": {"name": "string"}}]}}}`)
	if types, err := behaviortypes.Build(plain, reg); err != nil || types != nil {
		t.Fatalf("Build of a schema with no behavior = %v, %v", types, err)
	}
	composed, _ := loadDocument(t, withBehaviors(`[{"name": "Comments"}]`, "", ""))
	if _, err := behaviortypes.Build(composed, nil); err == nil || err.Error() != "type Item composes behavior Comments, and no registry declares the behaviors" {
		t.Fatalf("Build without a registry: err = %v", err)
	}
}
