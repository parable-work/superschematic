package ir

import (
	"bytes"
	"encoding/json"
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
