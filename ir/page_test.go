package ir

import (
	"bytes"
	"encoding/json"
	"reflect"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// An operation without a page marshals without the key, so the IR of every
// schema written before @paginated existed is the same bytes; one with a
// page writes it right after filterable, both members, and reads back the
// same through JSON and YAML.
func TestOperationPage_KeyPositionOmissionAndRoundTrip(t *testing.T) {
	op := &FieldDef{Name: "listItems", TypeRef: TypeRef{Name: "Item", IsArray: true}, Filterable: true, RequireOwnership: true, HTTPMethod: "GET"}
	data, err := json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"page"`)) {
		t.Fatalf("an operation without a page wrote the key: %s", data)
	}

	op.Page = &OperationPage{DefaultLimit: 2, MaxLimit: 5}
	data, err = json.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	if want := `"filterable":true,"page":{"defaultLimit":2,"maxLimit":5},"requireOwnership":true`; !bytes.Contains(data, []byte(want)) {
		t.Fatalf("marshaled operation = %s\nwant it to contain %s", data, want)
	}
	var back FieldDef
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&back, op) {
		t.Fatalf("JSON round trip = %+v, want %+v", back.Page, op.Page)
	}
	text, err := yaml.Marshal(op)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML FieldDef
	if err := yaml.Unmarshal(text, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(&fromYAML, op) {
		t.Fatalf("YAML round trip = %+v, want %+v\n%s", fromYAML.Page, op.Page, text)
	}
}

func TestOperationPageWithDefaults(t *testing.T) {
	for _, tc := range []struct {
		name      string
		said, got OperationPage
	}{
		{"nothing said", OperationPage{}, OperationPage{DefaultLimit: 20, MaxLimit: 100}},
		{"a max below the default", OperationPage{MaxLimit: 10}, OperationPage{DefaultLimit: 10, MaxLimit: 10}},
		{"a max above the default", OperationPage{MaxLimit: 500}, OperationPage{DefaultLimit: 20, MaxLimit: 500}},
		{"a default alone", OperationPage{DefaultLimit: 50}, OperationPage{DefaultLimit: 50, MaxLimit: 100}},
		{"both said", OperationPage{DefaultLimit: 2, MaxLimit: 5}, OperationPage{DefaultLimit: 2, MaxLimit: 5}},
	} {
		if got := tc.said.WithDefaults(); got != tc.got {
			t.Errorf("%s: WithDefaults(%+v) = %+v, want %+v", tc.name, tc.said, got, tc.got)
		}
	}
}

func TestValidateOperationPage(t *testing.T) {
	if err := ValidateOperationPage("Catalog.listItems", &FieldDef{}); err != nil {
		t.Fatalf("no page: %v", err)
	}
	for _, tc := range []struct {
		page OperationPage
		want string
	}{
		{OperationPage{DefaultLimit: 20, MaxLimit: 100}, ""},
		{OperationPage{DefaultLimit: 1000, MaxLimit: 1000}, ""},
		{OperationPage{DefaultLimit: 1, MaxLimit: 1}, ""},
		{OperationPage{DefaultLimit: 20, MaxLimit: 0}, "Catalog.listItems: @paginated maxLimit must be between 1 and 1000, not 0"},
		{OperationPage{DefaultLimit: 20, MaxLimit: 1001}, "Catalog.listItems: @paginated maxLimit must be between 1 and 1000, not 1001"},
		{OperationPage{DefaultLimit: -1, MaxLimit: 100}, "Catalog.listItems: @paginated defaultLimit must be between 1 and 1000, not -1"},
		{OperationPage{DefaultLimit: 50, MaxLimit: 10}, "Catalog.listItems: @paginated defaultLimit 50 is more than its maxLimit 10"},
	} {
		err := ValidateOperationPage("Catalog.listItems", &FieldDef{Page: &tc.page})
		switch {
		case tc.want == "" && err != nil:
			t.Errorf("%+v: %v", tc.page, err)
		case tc.want != "" && (err == nil || !strings.Contains(err.Error(), tc.want)):
			t.Errorf("%+v = %v, want %s", tc.page, err, tc.want)
		}
	}

	schema := &Schema{Name: "shop", Kind: SchemaKindAPI, Types: map[string]*TypeDef{"Item": {Name: "Item", Role: RoleAPIView}}, OperationSets: []*OperationSet{{
		Name: "Catalog", Operations: []*FieldDef{{Name: "listItems", TypeRef: TypeRef{Name: "Item", IsArray: true}, Page: &OperationPage{DefaultLimit: 0, MaxLimit: 2000}}},
	}}}
	var got []string
	for _, err := range schema.Validate() {
		got = append(got, err.Error())
	}
	want := []string{
		"Catalog.listItems: @paginated maxLimit must be between 1 and 1000, not 2000",
		"Catalog.listItems: @paginated defaultLimit must be between 1 and 1000, not 0",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %q, want %q", got, want)
	}
}
