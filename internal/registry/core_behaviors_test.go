package registry

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// coreBehaviorNames are the behaviors New registers, sorted.
var coreBehaviorNames = []string{"Comments", "Dependencies", "Links", "Revisions", "Workflow"}

// A registry with no extension declares the behaviors the engine
// implements, as the core's, under bare names, and holds each config to
// its schema.
func TestCoreBehaviors(t *testing.T) {
	reg := New(naming.Default())
	finalizeWithCoreGenerators(t, reg)
	if got := reg.BehaviorNames(); !slices.Equal(got, coreBehaviorNames) {
		t.Fatalf("BehaviorNames() = %v, want %v", got, coreBehaviorNames)
	}
	for _, name := range coreBehaviorNames {
		if b, _ := reg.Behavior(name); b.Extension != "" {
			t.Errorf("%s is registered by %q, want the core", name, b.Extension)
		}
	}
	workflow, _ := reg.Behavior("Workflow")
	comments, _ := reg.Behavior("Comments")
	revisions, _ := reg.Behavior("Revisions")
	dependencies, _ := reg.Behavior("Dependencies")
	links, _ := reg.Behavior("Links")
	if !workflow.ConfigRequired() || comments.ConfigRequired() || revisions.ConfigRequired() || dependencies.ConfigRequired() || !links.ConfigRequired() {
		t.Errorf("ConfigRequired: Workflow %v, Comments %v, Revisions %v, Dependencies %v, Links %v; want true, false, false, false, true",
			workflow.ConfigRequired(), comments.ConfigRequired(), revisions.ConfigRequired(), dependencies.ConfigRequired(), links.ConfigRequired())
	}
	if !slices.Equal(dependencies.Requires, []string{"Workflow"}) || len(links.Requires) != 0 {
		t.Errorf("requires: Dependencies %v, Links %v; want [Workflow] and none", dependencies.Requires, links.Requires)
	}
	var scopes []string
	for _, op := range links.Operations {
		scopes = append(scopes, op.Name+":"+op.Scope)
	}
	if want := []string{"link:", "unlink:", "listLinked:schema"}; !slices.Equal(scopes, want) {
		t.Errorf("Links operations and scopes = %v, want %v", scopes, want)
	}

	for _, test := range []struct {
		behavior Behavior
		config   string
		want     string
	}{
		{workflow, `{"states": ["draft", "done"], "transitions": [{"from": "draft", "to": "done"}]}`, ""},
		{workflow, `{"states": ["draft", "review", "done"], "initial": "review", "transitions": [{"from": "review", "to": "done", "permission": "documents.publish"}]}`, ""},
		{workflow, ``, "behavior Workflow config: "},
		{workflow, `{"states": [], "transitions": []}`, "behavior Workflow config: "},
		{workflow, `{"states": ["draft", "draft"], "transitions": []}`, "behavior Workflow config: "},
		{workflow, `{"states": ["in review"], "transitions": []}`, "behavior Workflow config: "},
		{workflow, `{"states": ["draft"]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["draft"], "transitions": [], "terminal": ["draft"]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b", "guard": "x"}]}`, "behavior Workflow config: "},
		{workflow, `{"states": ["a", "b"], "transitions": [{"from": "a", "to": "b", "permission": ""}]}`, "behavior Workflow config: "},
		{comments, ``, ""},
		{comments, `{}`, ""},
		{comments, `{"maxLength": 10}`, "behavior Comments takes no config"},
		{revisions, ``, ""},
		{revisions, `{"review": {"permission": "documents.review"}}`, ""},
		{revisions, `{"review": {}}`, "behavior Revisions config: "},
		{revisions, `{"review": {"permission": "documents.review", "quorum": 2}}`, "behavior Revisions config: "},
		{revisions, `{"reviewers": ["alice"]}`, "behavior Revisions config: "},
		{dependencies, ``, ""},
		{dependencies, `{"schemas": ["tasks", "milestones"], "gatedStates": ["done"]}`, ""},
		{dependencies, `{"schemas": []}`, "behavior Dependencies config: "},
		{dependencies, `{"schemas": ["task list"]}`, "behavior Dependencies config: "},
		{dependencies, `{"schemas": ["tasks", "tasks"]}`, "behavior Dependencies config: "},
		{dependencies, `{"gatedStates": []}`, "behavior Dependencies config: "},
		{dependencies, `{"gatedStates": [""]}`, "behavior Dependencies config: "},
		{dependencies, `{"blockers": ["tasks"]}`, "behavior Dependencies config: "},
		{links, `{"links": {"spec": {"schema": "documents", "pinned": true}, "parent": {"schema": "tasks", "required": true}}}`, ""},
		{links, ``, "behavior Links config: "},
		{links, `{"links": {}}`, "behavior Links config: "},
		{links, `{"links": {"Spec": {"schema": "documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec_doc": {"schema": "documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "the documents"}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "documents", "weak": true}}}`, "behavior Links config: "},
		{links, `{"links": {"spec": {"schema": "documents"}}, "cascade": true}`, "behavior Links config: "},
	} {
		err := test.behavior.ValidateConfig(json.RawMessage(test.config))
		switch {
		case test.want == "" && err != nil:
			t.Errorf("%s %s: %v", test.behavior.Name, test.config, err)
		case test.want != "" && (err == nil || !strings.Contains(err.Error(), test.want)):
			t.Errorf("%s %s: err = %v, want %q", test.behavior.Name, test.config, err, test.want)
		}
	}
}

// The core's behaviors name no invocation policy, so a distribution whose
// policy has none of the core's values still finalizes with them: each
// operation takes that policy's default.
func TestCoreBehaviorsUnderAnotherInvocationPolicy(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.Use(policyExtension{name: "vendor", policy: reviewPolicy}); err != nil {
		t.Fatal(err)
	}
	finalizeWithCoreGenerators(t, reg)
	if got := reg.BehaviorNames(); !slices.Equal(got, coreBehaviorNames) {
		t.Fatalf("BehaviorNames() = %v, want %v", got, coreBehaviorNames)
	}
}

// An extension cannot declare a core behavior's name, bare or under its
// own prefix's rule, and a second core registration of one is refused.
func TestCoreBehaviorNamesAreTaken(t *testing.T) {
	reg := New(naming.Default())
	err := reg.RegisterBehavior(BehaviorSpec{Declaration: declaration("Workflow", nil)})
	if err == nil || !strings.Contains(err.Error(), `behavior "Workflow" is already registered`) {
		t.Fatalf("a second Workflow: err = %v", err)
	}
	err = New(naming.Default()).Use(fakeExtension{name: "acme", register: func(r *Registry) error {
		return r.RegisterBehavior(BehaviorSpec{Extension: "acme", Declaration: declaration("Workflow", nil)})
	}})
	if err == nil || !strings.Contains(err.Error(), `behavior name "Workflow" of extension acme must be acme.<Name>`) {
		t.Fatalf("an extension's bare Workflow: err = %v", err)
	}
}
