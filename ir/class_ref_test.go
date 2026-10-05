package ir

import (
	"encoding/json"
	"testing"
)

// A class reference is an object whose only key is "class", holding a
// string; anything else is data of the extension's own.
func TestAsClassRef(t *testing.T) {
	for _, tc := range []struct {
		json string
		want string
		ok   bool
	}{
		{`{"class": "Accessory"}`, "Accessory", true},
		{`{"class": ""}`, "", true},
		{`{"class": "Accessory", "note": "x"}`, "", false},
		{`{"class": 3}`, "", false},
		{`{"class": {"class": "Accessory"}}`, "", false},
		{`{"name": "Accessory"}`, "", false},
		{`"Accessory"`, "", false},
		{`["Accessory"]`, "", false},
		{`null`, "", false},
	} {
		var v any
		if err := json.Unmarshal([]byte(tc.json), &v); err != nil {
			t.Fatal(err)
		}
		got, ok := AsClassRef(v)
		if ok != tc.ok || got.Class != tc.want {
			t.Errorf("AsClassRef(%s) = %+v, %v; want %q, %v", tc.json, got, ok, tc.want, tc.ok)
		}
	}
}

// The Go value encodes to the shape AsClassRef reads, so an extension that
// keeps a ClassRef in its slot struct stores what the data forms write.
func TestClassRefEncodesToItsShape(t *testing.T) {
	raw, err := json.Marshal(struct {
		Of ClassRef `json:"of"`
	}{Of: ClassRef{Class: "Accessory"}})
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != `{"of":{"class":"Accessory"}}` {
		t.Fatalf("encoded = %s", raw)
	}
	var back map[string]any
	if err := json.Unmarshal(raw, &back); err != nil {
		t.Fatal(err)
	}
	if ref, ok := AsClassRef(back["of"]); !ok || ref.Class != "Accessory" {
		t.Fatalf("AsClassRef(%v) = %+v, %v", back["of"], ref, ok)
	}
}
