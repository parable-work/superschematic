package schemafile

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestDecodeRefusesARepeatedObjectKey: the JSON Schema validator and the
// slot check read a repeated key's last value, but the strict decode reads
// every copy into the same map or struct, so the content of an earlier copy,
// which nothing validated, would reach the IR. The reader refuses any
// repeated key and names its JSON pointer.
func TestDecodeRefusesARepeatedObjectKey(t *testing.T) {
	cases := []struct {
		name  string
		input string
		want  string
	}{
		{
			// A type the meta-schema refuses (an unknown role) in the first
			// "types" copy; the second copy holds a valid type.
			"a repeated collection merges its maps",
			`{"types": {"Bad": {"name": "Bad", "role": "Table"}},
			  "types": {"Good": {"name": "Good", "role": "EmbeddedStruct"}}}`,
			`repeated object key "types" at '/types'`,
		},
		{
			// The first "fields" copy gives field 0 an unknown HTTP method;
			// the decode reads the second copy into the same field.
			"a repeated field list merges its elements",
			`{"name": "Item", "role": "EmbeddedStruct",
			  "fields": [{"name": "a", "typeRef": {"name": "String"}, "httpMethod": "FETCH"}],
			  "fields": [{"name": "a", "typeRef": {"name": "String"}}]}`,
			`repeated object key "fields" at '/fields'`,
		},
		{
			// The first "extensions" copy names an extension the core does not
			// link; the slot check and the validator see only the empty copy.
			"a repeated extensions slot merges its entries",
			`{"types": {"Item": {"name": "Item", "role": "EmbeddedStruct",
			  "fields": [{"name": "a", "typeRef": {"name": "String"},
			    "extensions": {"vendor": {"x": 1}}, "extensions": {}}]}}}`,
			`repeated object key "extensions" at '/types/Item/fields/0/extensions'`,
		},
		{
			"a repeated key deep in extension data",
			`{"extensions": {}, "documents": {}, "types": {"Item": {"name": "Item", "role": "EmbeddedStruct",
			  "implements": [{"name": "Named", "configArgs": {"a": {"b": 1, "b": 2}}}]}}}`,
			`repeated object key "b" at '/types/Item/implements/0/configArgs/a/b'`,
		},
		{
			"a repeated scalar key",
			`{"kind": "Enum", "name": "Colour", "values": [], "name": "Color"}`,
			`repeated object key "name" at '/name'`,
		},
		{
			// The pointer escapes "~" and "/" in a key (RFC 6901).
			"the path escapes a key",
			`{"types": {"a/b~c": {"name": "a/b~c", "role": "EmbeddedStruct", "role": "EmbeddedStruct"}}}`,
			`repeated object key "role" at '/types/a~1b~0c/role'`,
		},
		{
			// Keys are compared after unescaping: "descr\u0069ption" is
			// "description".
			"a key written with an escape",
			`{"name": "Item", "role": "EmbeddedStruct", "description": "x", "descr\u0069ption": "y"}`,
			`repeated object key "description" at '/description'`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			doc, err := DecodeWith([]byte(tc.input), "payload", nil)
			if err == nil {
				decoded, _ := json.Marshal(doc)
				t.Fatalf("DecodeWith accepted a repeated key; decoded %s", decoded)
			}
			if !strings.Contains(err.Error(), tc.want) || !strings.HasPrefix(err.Error(), "payload: ") {
				t.Fatalf("error = %q, want the source and %q", err, tc.want)
			}
		})
	}
}

// TestDecodeAcceptsTheSameKeyInDifferentObjects: a key repeats only within
// one object; sibling and nested objects may use the same keys.
func TestDecodeAcceptsTheSameKeyInDifferentObjects(t *testing.T) {
	input := `{"types": {"Item": {"name": "Item", "role": "EmbeddedStruct", "fields": [
		{"name": "a", "typeRef": {"name": "String"}},
		{"name": "b", "typeRef": {"name": "String"}}]}}}`
	doc, err := DecodeWith([]byte(input), "payload", nil)
	if err != nil {
		t.Fatalf("DecodeWith: %v", err)
	}
	if got := len(doc.Types["Item"].Fields); got != 2 {
		t.Fatalf("Item has %d fields, want 2", got)
	}
}
