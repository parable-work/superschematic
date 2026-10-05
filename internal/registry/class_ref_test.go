package registry

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// An Args schema built on ClassRefSchema takes a class where it uses it and
// refuses a name in a string there.
func TestClassRefSchemaInArgs(t *testing.T) {
	reg := New(naming.Naming{})
	err := reg.RegisterDecorator(DecoratorSpec{
		Name: "setting", Extension: "acme", Packages: []string{"@acme/schematic"}, Target: TargetType,
		Args:  json.RawMessage(`{"type":"object","required":["of"],"additionalProperties":false,"properties":{"of":` + string(ClassRefSchema) + `}}`),
		Apply: func(Node, []any, Site) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	spec, _ := reg.Decorator("setting", TargetType)
	if err := spec.ValidateArgs([]any{map[string]any{"of": map[string]any{"class": "Backend"}}}); err != nil {
		t.Errorf("a class reference was rejected: %v", err)
	}
	for _, bad := range []any{
		"Backend",
		map[string]any{"class": ""},
		map[string]any{"class": "Backend", "kind": "API"},
		map[string]any{"name": "Backend"},
	} {
		if err := spec.ValidateArgs([]any{map[string]any{"of": bad}}); err == nil || !strings.HasPrefix(err.Error(), "@setting argument:") {
			t.Errorf("of = %#v: error = %v", bad, err)
		}
	}
}

// DecodeClassRef reads a class reference from any form Apply can see it in:
// the decoded map, the Go value, or raw JSON.
func TestDecodeClassRef(t *testing.T) {
	for _, v := range []any{
		map[string]any{"class": "Backend"},
		ClassRef{Class: "Backend"},
		json.RawMessage(`{"class":"Backend"}`),
	} {
		name, err := DecodeClassRef(v)
		if err != nil || name != "Backend" {
			t.Errorf("DecodeClassRef(%#v) = %q, %v", v, name, err)
		}
	}
	for _, v := range []any{
		"Backend",
		nil,
		map[string]any{"class": ""},
		map[string]any{"class": "Backend", "kind": "API"},
		map[string]any{"name": "Backend", "kind": "API"},
	} {
		if name, err := DecodeClassRef(v); err == nil || !strings.Contains(err.Error(), "want a class") {
			t.Errorf("DecodeClassRef(%#v) = %q, %v; want an error", v, name, err)
		}
	}
}
