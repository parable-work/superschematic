package ir

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

type shelfExt struct {
	Shelf *shelf `json:"shelf,omitempty"`
}

type shelf struct {
	Aisle int `json:"aisle"`
}

// The persisted-IR compatibility promise: a node with no extension data
// marshals without an "extensions" (or "documents") key, whether the map is
// nil or allocated and empty.
func TestExtensions_EmptyMapsDoNotSerialize(t *testing.T) {
	s := NewSchema("svc", SchemaKindDB)
	s.Extensions = map[string]json.RawMessage{}
	s.Documents = map[string]json.RawMessage{}
	s.Types["T"] = &TypeDef{
		Name:       "T",
		Role:       RoleDBTable,
		Extensions: map[string]json.RawMessage{},
		Fields: []*FieldDef{
			{Name: "id", TypeRef: TypeRef{Name: "Identity.UUID"}, Extensions: map[string]json.RawMessage{}},
		},
	}
	s.OperationSets = []*OperationSet{{Name: "Ops", Extensions: map[string]json.RawMessage{}}}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	for _, key := range []string{`"extensions"`, `"documents"`} {
		if bytes.Contains(data, []byte(key)) {
			t.Errorf("empty %s map serialized: %s", key, data)
		}
	}
}

func TestExtensions_PopulatedRoundTripJSON(t *testing.T) {
	s := NewSchema("svc", SchemaKindDB)
	s.Extensions = map[string]json.RawMessage{"acme": json.RawMessage(`{"catalog":true}`)}
	s.Documents = map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{"shelves":3}`)}
	s.Types["T"] = &TypeDef{
		Name:       "T",
		Role:       RoleDBTable,
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"tagged":true}`)},
		Fields: []*FieldDef{{
			Name:       "slug",
			TypeRef:    TypeRef{Name: "Identity.Slug"},
			Unique:     true,
			Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"shelf":{"aisle":3}}`)},
		}},
	}
	s.OperationSets = []*OperationSet{{
		Name:       "Ops",
		Extensions: map[string]json.RawMessage{"acme": json.RawMessage(`{"audited":true}`)},
	}}

	data, err := json.Marshal(s)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	var decoded Schema
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}

	if got := string(decoded.Extensions["acme"]); got != `{"catalog":true}` {
		t.Errorf("Schema.Extensions[acme] = %s", got)
	}
	if got := string(decoded.Documents["catalogConfig"]); got != `{"shelves":3}` {
		t.Errorf("Schema.Documents[catalogConfig] = %s", got)
	}
	if got := string(decoded.Types["T"].Extensions["acme"]); got != `{"tagged":true}` {
		t.Errorf("TypeDef.Extensions[acme] = %s", got)
	}
	if got := string(decoded.Types["T"].Fields[0].Extensions["acme"]); got != `{"shelf":{"aisle":3}}` {
		t.Errorf("FieldDef.Extensions[acme] = %s", got)
	}
	if got := string(decoded.OperationSets[0].Extensions["acme"]); got != `{"audited":true}` {
		t.Errorf("OperationSet.Extensions[acme] = %s", got)
	}

	// The typed decorators sit beside the extension object on the same
	// node, as in the design doc's @shelf example.
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		t.Fatalf("Unmarshal raw: %v", err)
	}
	field := raw["types"].(map[string]any)["T"].(map[string]any)["fields"].([]any)[0].(map[string]any)
	if field["unique"] != true {
		t.Errorf("field.unique = %v", field["unique"])
	}
	ext, ok := field["extensions"].(map[string]any)
	if !ok || ext["acme"] == nil {
		t.Errorf("field.extensions = %v", field["extensions"])
	}
}

func TestGetSetExtension(t *testing.T) {
	f := &FieldDef{Name: "slug"}

	_, ok, err := GetExtension[shelfExt](f, "acme")
	if err != nil || ok {
		t.Fatalf("GetExtension on absent key: ok=%v err=%v", ok, err)
	}

	if err := UpdateExtension(f, "acme", func(e *shelfExt) { e.Shelf = &shelf{Aisle: 3} }); err != nil {
		t.Fatalf("UpdateExtension: %v", err)
	}
	if got := string(f.Extensions["acme"]); got != `{"shelf":{"aisle":3}}` {
		t.Errorf("Extensions[acme] = %s", got)
	}

	v, ok, err := GetExtension[shelfExt](f, "acme")
	if err != nil || !ok {
		t.Fatalf("GetExtension: ok=%v err=%v", ok, err)
	}
	if v.Shelf == nil || v.Shelf.Aisle != 3 {
		t.Errorf("decoded = %+v", v)
	}

	// Storing a value that encodes to {} removes the key so the node
	// marshals without "extensions".
	if err := SetExtension(f, "acme", shelfExt{}); err != nil {
		t.Fatalf("SetExtension empty: %v", err)
	}
	if len(f.Extensions) != 0 {
		t.Errorf("Extensions after empty set = %v", f.Extensions)
	}

	f.Extensions = map[string]json.RawMessage{"acme": json.RawMessage(`{"shelf":`)}
	if _, _, err := GetExtension[shelfExt](f, "acme"); err == nil {
		t.Error("GetExtension accepted malformed extension data")
	}
	if err := UpdateExtension(f, "acme", func(e *shelfExt) {}); err == nil {
		t.Error("UpdateExtension accepted malformed extension data")
	}
}

func TestExtensionHolders(t *testing.T) {
	holders := []ExtensionHolder{&Schema{}, &TypeDef{}, &FieldDef{}, &OperationSet{}}
	for _, h := range holders {
		if err := SetExtension(h, "acme", map[string]bool{"on": true}); err != nil {
			t.Fatalf("%T: SetExtension: %v", h, err)
		}
		got, ok, err := GetExtension[map[string]bool](h, "acme")
		if err != nil || !ok || !got["on"] {
			t.Errorf("%T: GetExtension = %v ok=%v err=%v", h, got, ok, err)
		}
	}
}

func TestGetSetDocument(t *testing.T) {
	type catalog struct {
		Shelves int `json:"shelves"`
	}
	s := NewSchema("svc", SchemaKindDB)

	if _, ok, err := GetDocument[catalog](s, "catalogConfig"); ok || err != nil {
		t.Fatalf("GetDocument on absent key: ok=%v err=%v", ok, err)
	}
	if err := SetDocument(s, "catalogConfig", catalog{Shelves: 3}); err != nil {
		t.Fatalf("SetDocument: %v", err)
	}
	got, ok, err := GetDocument[catalog](s, "catalogConfig")
	if err != nil || !ok {
		t.Fatalf("GetDocument: ok=%v err=%v", ok, err)
	}
	if !reflect.DeepEqual(got, catalog{Shelves: 3}) {
		t.Errorf("document = %+v", got)
	}
	// A document that encodes to {} stays: its presence says the document
	// exists, unlike an extension object with no decorators.
	if err := SetDocument(s, "catalogConfig", struct{}{}); err != nil {
		t.Fatalf("SetDocument empty: %v", err)
	}
	if got := string(s.Documents["catalogConfig"]); got != `{}` {
		t.Errorf("Documents after empty set = %v", s.Documents)
	}

	s.Documents = map[string]json.RawMessage{"catalogConfig": json.RawMessage(`{`)}
	if _, _, err := GetDocument[catalog](s, "catalogConfig"); err == nil {
		t.Error("GetDocument accepted malformed document data")
	}
}

// Every entry point that stores an extension value (SetExtension here, the
// data-form readers through CanonicalizeExtensions) produces the same bytes
// for the same value: compact, object keys sorted, number literals as
// written, array order kept.
func TestCanonicalJSON(t *testing.T) {
	cases := map[string]string{
		`{"b": 1, "a": {"d": true, "c": null}}`:          `{"a":{"c":null,"d":true},"b":1}`,
		`[3, 1, {"z": 2, "y": 1}]`:                       `[3,1,{"y":1,"z":2}]`,
		`{"n": 123456789012345678901234567890}`:          `{"n":123456789012345678901234567890}`,
		`{"f": 1.0, "e": 1e3, "z": -0, "s": "a: \"b\""}`: `{"e":1e3,"f":1.0,"s":"a: \"b\"","z":-0}`,
		` "text" `: `"text"`,
	}
	for in, want := range cases {
		got, err := CanonicalJSON(json.RawMessage(in))
		if err != nil {
			t.Fatalf("CanonicalJSON(%s): %v", in, err)
		}
		if string(got) != want {
			t.Errorf("CanonicalJSON(%s) = %s, want %s", in, got, want)
		}
	}
	for _, bad := range []string{`{"a":`, `{} {}`, ``} {
		if _, err := CanonicalJSON(json.RawMessage(bad)); err == nil {
			t.Errorf("CanonicalJSON(%q) accepted malformed input", bad)
		}
	}
}

type orderedExt struct {
	Zeta  int  `json:"zeta"`
	Alpha bool `json:"alpha"`
}

// A struct marshals in field order; the stored bytes are still key-sorted so
// a TS-authored extension and its data-form twin persist identically.
func TestSetExtensionCanonicalizes(t *testing.T) {
	f := &FieldDef{Name: "slug"}
	if err := SetExtension(f, "acme", orderedExt{Zeta: 1, Alpha: true}); err != nil {
		t.Fatalf("SetExtension: %v", err)
	}
	if got := string(f.Extensions["acme"]); got != `{"alpha":true,"zeta":1}` {
		t.Errorf("Extensions[acme] = %s", got)
	}
	s := NewSchema("svc", SchemaKindDB)
	if err := SetDocument(s, "cfg", json.RawMessage(`{ "zeta": 1, "alpha": true }`)); err != nil {
		t.Fatalf("SetDocument: %v", err)
	}
	if got := string(s.Documents["cfg"]); got != `{"alpha":true,"zeta":1}` {
		t.Errorf("Documents[cfg] = %s", got)
	}
}

func TestCanonicalizeExtensionsAndDocuments(t *testing.T) {
	exts := map[string]json.RawMessage{
		"acme":  json.RawMessage(`{"b": 1, "a": 2}`),
		"empty": json.RawMessage(` { } `),
	}
	if err := CanonicalizeExtensions(exts); err != nil {
		t.Fatalf("CanonicalizeExtensions: %v", err)
	}
	if len(exts) != 1 || string(exts["acme"]) != `{"a":2,"b":1}` {
		t.Errorf("extensions = %v", exts)
	}

	docs := map[string]json.RawMessage{
		"cfg":   json.RawMessage(`{"b": 1, "a": 2}`),
		"empty": json.RawMessage(` { } `),
	}
	if err := CanonicalizeDocuments(docs); err != nil {
		t.Fatalf("CanonicalizeDocuments: %v", err)
	}
	if len(docs) != 2 || string(docs["cfg"]) != `{"a":2,"b":1}` || string(docs["empty"]) != `{}` {
		t.Errorf("documents = %v", docs)
	}

	bad := map[string]json.RawMessage{"acme": json.RawMessage(`{`)}
	err := CanonicalizeExtensions(bad)
	if err == nil || !strings.Contains(err.Error(), `extension "acme"`) {
		t.Errorf("CanonicalizeExtensions malformed: err=%v", err)
	}
	if err := CanonicalizeDocuments(map[string]json.RawMessage{"cfg": json.RawMessage(`]`)}); err == nil {
		t.Error("CanonicalizeDocuments accepted malformed input")
	}
}
