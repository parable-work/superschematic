package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestAllowUnknownFieldsContradictionsAreRefused: a type is never both
// @allowUnknownFields and @strictJSON or @denyUnknownFields (D87), and a
// @projection class carries neither.
func TestAllowUnknownFieldsContradictionsAreRefused(t *testing.T) {
	schema := ir.NewSchema("hooks", ir.SchemaKindAPI)
	for _, td := range []*ir.TypeDef{
		{Name: "Lenient", AllowUnknownFields: true},
		{Name: "Strict", AllowUnknownFields: true, StrictJSON: true},
		{Name: "Denied", AllowUnknownFields: true, DenyUnknownFields: true},
		{Name: "Closed", StrictJSON: true, DenyUnknownFields: true},
	} {
		schema.Types[td.Name] = td
	}
	r := &Result{}
	checkUnknownFields(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"Denied: @allowUnknownFields contradicts @denyUnknownFields: one accepts a key the type does not declare, the other refuses it",
		"Strict: @allowUnknownFields contradicts @strictJSON: one accepts a key the type does not declare, the other refuses it",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}

	projection := &Result{}
	checkProjectionClass(&ir.TypeDef{Name: "Row", AllowUnknownFields: true}, projection)
	if len(projection.Errors) != 1 || projection.Errors[0].Msg != "Row: a @projection class carries only @projection and @join" {
		t.Errorf("projection errors = %v", projection.Errors)
	}
}
