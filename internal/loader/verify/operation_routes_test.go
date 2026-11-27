package verify

import (
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// routeErrors are the route refusals of a pass over an API of sets.
func routeErrors(sets ...*ir.OperationSet) []string {
	schema := ir.NewSchema("things", ir.SchemaKindAPI)
	schema.OperationSets = sets
	var got []string
	for _, d := range Run(schema, Input{}).Errors {
		if strings.Contains(d.Msg, "is routed at") {
			got = append(got, d.Msg)
		}
	}
	return got
}

func routed(name, method, path string, args ...*ir.ArgumentDef) *ir.FieldDef {
	return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "boolean"}, Required: true, HTTPMethod: method, RestPath: path, Arguments: args}
}

func pathArgument(name string) *ir.ArgumentDef {
	return &ir.ArgumentDef{Name: name, TypeRef: ir.TypeRef{Name: "string"}, Required: true}
}

// TestTwoOperationsOnOneRouteAreRefused: two operations of an API with one
// HTTP method whose paths are one route, the same text segment for
// segment with a {parameter} of any name in the same places, are refused,
// naming both operations, whichever sets declare them and whether the
// method and the path are @rest's or the defaults a generator gives. A
// path that only overlaps another, a parameter where the other has text,
// is two routes, as is the same path under another method.
func TestTwoOperationsOnOneRouteAreRefused(t *testing.T) {
	const tail = ": a route serves one operation, and a request would reach only one of the two; give one another method or path"
	for _, test := range []struct {
		name string
		sets []*ir.OperationSet
		want []string
	}{
		{
			"the same @rest in two sets",
			[]*ir.OperationSet{
				{Name: "LoginQueries", Operations: []*ir.FieldDef{routed("listLogins", "GET", "logins")}},
				{Name: "VaultQueries", Operations: []*ir.FieldDef{routed("savedLogins", "get", "/logins/")}},
			},
			[]string{"LoginQueries.listLogins is routed at GET /api/logins, and so is VaultQueries.savedLogins" + tail},
		},
		{
			"the same @rest twice in one set",
			[]*ir.OperationSet{{Name: "LoginQueries", Operations: []*ir.FieldDef{routed("listLogins", "GET", "logins"), routed("allLogins", "GET", "logins")}}},
			[]string{"LoginQueries.listLogins is routed at GET /api/logins, and so is LoginQueries.allLogins" + tail},
		},
		{
			"parameters of two names",
			[]*ir.OperationSet{{Name: "TenantQueries", Operations: []*ir.FieldDef{
				routed("getTenant", "GET", "tenants/{id}", pathArgument("id")),
				routed("readTenant", "GET", "tenants/{tenantId}", pathArgument("tenantId")),
			}}},
			[]string{"TenantQueries.getTenant is routed at GET /api/tenants/{id}, and so is TenantQueries.readTenant, at /api/tenants/{tenantId}" + tail},
		},
		{
			"a default route and a @rest that names it",
			[]*ir.OperationSet{
				{Name: "TenantMutations", Operations: []*ir.FieldDef{routed("archiveTenant", "", "", pathArgument("id"))}},
				{Name: "AdminActions", Operations: []*ir.FieldDef{routed("archive", "POST", "tenant/archive-tenant/{tenant}", pathArgument("tenant"))}},
			},
			[]string{"TenantMutations.archiveTenant is routed at POST /api/tenant/archive-tenant/{id}, and so is AdminActions.archive, at /api/tenant/archive-tenant/{tenant}" + tail},
		},
		{
			"three operations on one route",
			[]*ir.OperationSet{{Name: "LoginQueries", Operations: []*ir.FieldDef{routed("a", "GET", "logins"), routed("b", "GET", "logins"), routed("c", "GET", "logins")}}},
			[]string{
				"LoginQueries.a is routed at GET /api/logins, and so is LoginQueries.b" + tail,
				"LoginQueries.a is routed at GET /api/logins, and so is LoginQueries.c" + tail,
			},
		},
		{
			"another method",
			[]*ir.OperationSet{{Name: "LoginQueries", Operations: []*ir.FieldDef{routed("listLogins", "GET", "logins"), routed("addLogin", "POST", "logins")}}},
			nil,
		},
		{
			"a parameter where the other has text",
			[]*ir.OperationSet{{Name: "TenantQueries", Operations: []*ir.FieldDef{
				routed("getTenant", "GET", "tenants/{id}", pathArgument("id")),
				routed("currentTenant", "GET", "tenants/me"),
			}}},
			nil,
		},
		{
			"another count of segments",
			[]*ir.OperationSet{{Name: "TenantQueries", Operations: []*ir.FieldDef{
				routed("getTenant", "GET", "tenants/{id}", pathArgument("id")),
				routed("listTenants", "GET", "tenants"),
			}}},
			nil,
		},
		{
			"default routes of two names",
			[]*ir.OperationSet{{Name: "TenantQueries", Operations: []*ir.FieldDef{routed("listTenants", "", ""), routed("countTenants", "", "")}}},
			nil,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := routeErrors(test.sets...); !slices.Equal(got, test.want) {
				t.Errorf("errors = %q, want %q", got, test.want)
			}
		})
	}
}
