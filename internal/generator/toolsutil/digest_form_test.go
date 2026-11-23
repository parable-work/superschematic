package toolsutil

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
)

// TestDigestJSONWritesTheInternalAnyTypeWhenAsked: without
// ToolKeys.DigestInternalTypes the digest's encoding is the document's
// (D14, amended). With it, each property of the internal any type, at
// every depth and nullable or not, encodes "type": "any" in the place the
// struct gives it, and every other property encodes as before.
func TestDigestJSONWritesTheInternalAnyTypeWhenAsked(t *testing.T) {
	scalars := map[string]apigen.ScalarJSONSchemaInfo{
		"Generic.JSON": {CanonicalName: "Generic.JSON", Type: "any", Description: "Any valid JSON root"},
	}
	fields := map[string][]apigen.Param{
		"Input": {
			{Name: "payload", Type: "Generic.JSON", Required: true},
			{Name: "maybePayload", Type: "Generic.JSON"},
			{Name: "payloads", Type: "Generic.JSON", IsArray: true},
			{Name: "payloadByName", Type: "Generic.JSON", IsMap: true, Required: true},
			{Name: "name", Type: "string", Required: true},
		},
	}
	schema := BuildParametersSchema(nil, nil, true, "Input", fields,
		nil, nil, nil, false, scalars, byName, apigen.DefaultToolKeys(), nil, nil)

	written, err := schema.DigestJSON(false)
	if err != nil {
		t.Fatal(err)
	}
	marshaled, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if string(written) != string(marshaled) {
		t.Errorf("DigestJSON(false) = %s\nwant the document's encoding %s", written, marshaled)
	}

	internal, err := schema.DigestJSON(true)
	if err != nil {
		t.Fatal(err)
	}
	const item = `{"x-superschematic-scalar":"Generic.JSON","type":"any","description":"Any valid JSON root"}`
	for _, want := range []string{
		`"payload":` + item,
		`"maybePayload":` + item,
		`"payloads":{"description":"Array of Generic.JSON values","items":` + item + `,"type":["array","null"]}`,
		`"payloadByName":{"additionalProperties":` + item + `,"type":"object","description":"Map of Generic.JSON values"}`,
		`"name":{"type":"string","description":"A string value"}`,
	} {
		if !strings.Contains(string(internal), want) {
			t.Errorf("DigestJSON(true) lacks %s:\n%s", want, internal)
		}
	}
	// The document itself is unchanged.
	again, err := json.Marshal(schema)
	if err != nil {
		t.Fatal(err)
	}
	if string(again) != string(marshaled) {
		t.Errorf("DigestJSON(true) changed the schema it encoded:\n%s\nwant\n%s", again, marshaled)
	}
}
