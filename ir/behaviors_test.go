package ir

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

// A type without behaviors marshals without the key, so the IR of every
// schema written before behaviors existed is the same bytes; a type with
// them writes the key right after implements.
func TestBehaviors_KeyPositionAndOmission(t *testing.T) {
	plain := &TypeDef{Name: "T", Role: RoleEmbeddedStruct, Implements: []TraitRef{{Name: "Stamped"}}}
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"behaviors"`)) {
		t.Fatalf("a type without behaviors wrote the key: %s", data)
	}
	plain.Behaviors = []BehaviorRef{}
	if again, _ := json.Marshal(plain); !bytes.Equal(again, data) {
		t.Fatalf("an empty behavior list changed the bytes:\n%s\n%s", data, again)
	}

	withBehaviors := &TypeDef{
		Name:       "T",
		Role:       RoleEmbeddedStruct,
		Implements: []TraitRef{{Name: "Stamped"}},
		Behaviors: []BehaviorRef{
			{Name: "acme.Rating", Config: json.RawMessage(`{"maxStars":5}`)},
			{Name: "Flagged"},
		},
		RawHeritage: &RawHeritage{Implements: []string{"Stamped"}},
	}
	data, err = json.Marshal(withBehaviors)
	if err != nil {
		t.Fatal(err)
	}
	want := `"implements":[{"name":"Stamped"}],"behaviors":[{"name":"acme.Rating","config":{"maxStars":5}},{"name":"Flagged"}],"rawHeritage":`
	if !bytes.Contains(data, []byte(want)) {
		t.Fatalf("marshaled type = %s\nwant it to contain %s", data, want)
	}
	var back TypeDef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	again, err := json.Marshal(&back)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(again, data) {
		t.Fatalf("IR JSON round trip changed the bytes:\n%s\n%s", data, again)
	}
}

// Configs are stored canonically whatever the source's key order and
// whitespace, and {} is stored as no config.
func TestCanonicalizeBehaviors(t *testing.T) {
	refs := []BehaviorRef{
		{Name: "a", Config: json.RawMessage("{ \"z\": [3, 1],\n \"a\": {\"y\": 1.50, \"x\": null} }")},
		{Name: "b", Config: json.RawMessage(`{ }`)},
		{Name: "c"},
	}
	if err := CanonicalizeBehaviors(refs); err != nil {
		t.Fatal(err)
	}
	if got := string(refs[0].Config); got != `{"a":{"x":null,"y":1.50},"z":[3,1]}` {
		t.Errorf("config a = %s", got)
	}
	if refs[1].Config != nil || refs[2].Config != nil {
		t.Errorf("configs b and c = %q, %q, want none", refs[1].Config, refs[2].Config)
	}
	if err := CanonicalizeBehaviors([]BehaviorRef{{Name: "bad", Config: json.RawMessage(`{"a":`)}}); err == nil {
		t.Error("malformed config: want error")
	}
}

func TestFindBehavior(t *testing.T) {
	s := NewSchema("svc", SchemaKindGeneral)
	s.Types["Zed"] = &TypeDef{Name: "Zed", Behaviors: []BehaviorRef{{Name: "Late"}}}
	s.Types["Plain"] = &TypeDef{Name: "Plain"}
	if _, _, found := (&Schema{}).FindBehavior(); found {
		t.Fatal("an empty schema has a behavior")
	}
	s.Types["Alpha"] = &TypeDef{Name: "Alpha", Behaviors: []BehaviorRef{{Name: "First"}, {Name: "Second"}}}
	typeName, behavior, found := s.FindBehavior()
	if !found || typeName != "Alpha" || behavior != "First" {
		t.Fatalf("FindBehavior() = %q, %q, %v", typeName, behavior, found)
	}
}

// A field anchor writes its field between the name and the config, and a
// type anchor writes none, so the IR of every schema written before field
// anchors is the same bytes. An owned field writes its behavior.
func TestBehaviors_FieldAnchorJSON(t *testing.T) {
	refs := []BehaviorRef{
		{Name: "Workflow", Config: json.RawMessage(`{"states":["a"]}`)},
		{Name: "Workflow", Field: "status", Config: json.RawMessage(`{"states":["a"]}`)},
	}
	data, err := json.Marshal(refs)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"Workflow","config":{"states":["a"]}},{"name":"Workflow","field":"status","config":{"states":["a"]}}]`
	if string(data) != want {
		t.Fatalf("marshaled refs = %s\nwant %s", data, want)
	}
	owned := &FieldDef{Name: "statusEnteredAt", TypeRef: TypeRef{Name: "Temporal.DateTime"}, Behavior: &FieldBehavior{Use: "status", Behavior: "Workflow", Field: "enteredAt"}}
	data, err = json.Marshal(owned)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(data, []byte(`"behavior":{"use":"status","behavior":"Workflow","field":"enteredAt"}`)) {
		t.Fatalf("marshaled owned field = %s", data)
	}
	var back FieldDef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !back.Owned() || *back.Behavior != *owned.Behavior {
		t.Fatalf("round trip = %+v", back.Behavior)
	}
}

// A use's id is its field for a field anchor and its behavior for a type
// anchor; a type with only field anchors is no engine type.
func TestBehaviors_Anchors(t *testing.T) {
	onType := BehaviorRef{Name: "Workflow"}
	onField := BehaviorRef{Name: "Workflow", Field: "fulfilment"}
	if onType.FieldAnchored() || onType.UseID() != "Workflow" {
		t.Errorf("type anchor: FieldAnchored %v, UseID %q", onType.FieldAnchored(), onType.UseID())
	}
	if !onField.FieldAnchored() || onField.UseID() != "fulfilment" {
		t.Errorf("field anchor: FieldAnchored %v, UseID %q", onField.FieldAnchored(), onField.UseID())
	}

	table := &TypeDef{
		Name:      "Order",
		Behaviors: []BehaviorRef{{Name: "Workflow", Field: "status"}, {Name: "Workflow", Field: "fulfilment"}},
		Fields: []*FieldDef{
			{Name: "title"},
			{Name: "status", Behavior: &FieldBehavior{Use: "status", Behavior: "Workflow", Field: "status", Value: true}},
			{Name: "statusEnteredAt", Behavior: &FieldBehavior{Use: "status", Behavior: "Workflow", Field: "enteredAt"}},
		},
	}
	if table.IsEngineType() {
		t.Error("a type with only field anchors is an engine type")
	}
	if got := table.FieldBehaviors(); len(got) != 2 || got[1].Field != "fulfilment" {
		t.Errorf("FieldBehaviors() = %+v", got)
	}
	if got := table.TypeBehaviors(); len(got) != 0 {
		t.Errorf("TypeBehaviors() = %+v", got)
	}
	if ref, ok := table.FieldBehavior("fulfilment"); !ok || ref.Name != "Workflow" {
		t.Errorf("FieldBehavior(fulfilment) = %+v, %v", ref, ok)
	}
	if _, ok := table.FieldBehavior("title"); ok {
		t.Error("an own field anchors a behavior")
	}
	if got := table.OwnedFields(); len(got) != 2 || got[0].Name != "status" || !got[0].Behavior.Value {
		t.Errorf("OwnedFields() = %+v", got)
	}
	if table.Fields[0].Owned() {
		t.Error("an own field is owned")
	}

	engine := &TypeDef{Name: "Ticket", Behaviors: []BehaviorRef{onType}}
	if !engine.IsEngineType() {
		t.Error("a type with a type anchor is no engine type")
	}
	var none *TypeDef
	if none.IsEngineType() || none.FieldBehaviors() != nil || none.OwnedFields() != nil {
		t.Error("a nil type composes behaviors")
	}
}

func TestFindFieldBehavior(t *testing.T) {
	s := NewSchema("svc", SchemaKindDB)
	s.Types["Engine"] = &TypeDef{Name: "Engine", Behaviors: []BehaviorRef{{Name: "Workflow"}}}
	if _, _, _, found := s.FindFieldBehavior(); found {
		t.Fatal("a type anchor is a field behavior")
	}
	s.Types["Order"] = &TypeDef{Name: "Order", Behaviors: []BehaviorRef{{Name: "Workflow", Field: "status"}}}
	typeName, field, behavior, found := s.FindFieldBehavior()
	if !found || typeName != "Order" || field != "status" || behavior != "Workflow" {
		t.Fatalf("FindFieldBehavior() = %q, %q, %q, %v", typeName, field, behavior, found)
	}
}

// A field anchor's config that does not parse names its field.
func TestCanonicalizeBehaviors_NamesTheField(t *testing.T) {
	err := CanonicalizeBehaviors([]BehaviorRef{{Name: "Workflow", Field: "status", Config: json.RawMessage(`{"a":`)}})
	if err == nil || !strings.Contains(err.Error(), "on field status") {
		t.Fatalf("err = %v", err)
	}
}
