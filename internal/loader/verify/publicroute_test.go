package verify

import (
	"slices"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestPublicRouteContradictionsAreRefused: in a schema authored as IR an
// @publicRoute operation that also requires a caller is refused; in IR an
// Authenticated set is already folded into auth.
func TestPublicRouteContradictionsAreRefused(t *testing.T) {
	schema := ir.NewSchema("things", ir.SchemaKindAPI)
	schema.OperationSets = []*ir.OperationSet{{
		Name: "ThingQueries",
		Operations: []*ir.FieldDef{
			{Name: "open", Public: true},
			{Name: "authed", Public: true, Auth: true},
			{Name: "permitted", Public: true, Permissions: []string{"things.read"}},
			{Name: "owned", Public: true, RequireOwnership: true},
			{Name: "guarded", Auth: true, Permissions: []string{"things.read"}},
		},
	}}
	r := &Result{}
	checkPublicRoutes(schema, r)
	var got []string
	for _, d := range r.Errors {
		got = append(got, d.Msg)
	}
	want := []string{
		"ThingQueries.authed: @publicRoute contradicts @auth: a public route needs no caller",
		"ThingQueries.permitted: @publicRoute contradicts @requirePermission: a public route needs no caller",
		"ThingQueries.owned: @publicRoute contradicts @requireOwnership: a public route needs no caller",
	}
	if !slices.Equal(got, want) {
		t.Errorf("errors = %q, want %q", got, want)
	}
}
