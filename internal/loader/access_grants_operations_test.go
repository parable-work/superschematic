package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// accessGrantsAPIFixture is the API whose @accessGrantsOperations set lists
// the active grants of fixture-access-db (#735), and accessGrantsDBFixture
// its database.
const (
	accessGrantsAPIFixture = "tsreader/testdata/services/fixture-access-api"
	accessGrantsDBFixture  = "tsreader/testdata/services/fixture-access-db"
)

// loadAccessGrantsAPI loads the API at dir over fixture-access-db.
func loadAccessGrantsAPI(t *testing.T, dir string) (*ir.Schema, error) {
	t.Helper()
	db, err := LoadService(filepath.FromSlash(accessGrantsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	return LoadService(dir, WithDependencyLoader(func(string) (*ir.Schema, error) { return db, nil }))
}

// TestAccessGrantsOperationsExpand: the API loads with its
// @accessGrantsOperations set filled from its database: the route, the
// three query arguments, the list of AccessGrant, the caller it needs and
// the permissions it takes (none but the class's), the tool, and the
// marker, which names the grants table, the administrator's permission and
// the owner of each table a grant may target.
func TestAccessGrantsOperationsExpand(t *testing.T) {
	schema, err := loadAccessGrantsAPI(t, filepath.FromSlash(accessGrantsAPIFixture))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	var set *ir.OperationSet
	for _, s := range schema.OperationSets {
		if s.Name == "Grants" {
			set = s
		}
	}
	if set == nil || !set.IsAccessGrantsOperations() {
		t.Fatalf("no @accessGrantsOperations set Grants: %s", marshal(t, schema.OperationSets))
	}
	if len(set.Operations) != 1 {
		t.Fatalf("Grants has %d operations, want 1: %s", len(set.Operations), marshal(t, set.Operations))
	}
	op := set.Operations[0]
	if op.Name != ir.AccessGrantsOpActive || op.HTTPMethod != "GET" || op.RestPath != "grants/active" || op.Origin != ir.OriginAccessGrantsOperations || !op.Auth || len(op.Permissions) != 0 {
		t.Errorf("activeGrants: %s %s %s, origin %q, auth %v, permissions %v", op.Name, op.HTTPMethod, op.RestPath, op.Origin, op.Auth, op.Permissions)
	}
	var args []string
	for _, a := range op.Arguments {
		args = append(args, a.Name+":"+a.TypeRef.Name)
		if !a.IsQuery || a.Required {
			t.Errorf("argument %s: query %v, required %v; want an optional query argument", a.Name, a.IsQuery, a.Required)
		}
	}
	if want := []string{"holder:Identity.UUID", "targetType:string", "targetId:Identity.UUID"}; !slices.Equal(args, want) {
		t.Errorf("arguments %v, want %v", args, want)
	}
	if op.TypeRef.Name != ir.AccessGrantViewType || !op.TypeRef.IsArray {
		t.Errorf("result %+v, want AccessGrant[]", op.TypeRef)
	}
	if op.MCP == nil || op.MCP.Hidden || op.MCP.Handle != "active_grants" {
		t.Errorf("mcp %+v, want the tool active_grants", op.MCP)
	}
	marker := op.AccessGrantsOperation
	if marker == nil || marker.Database != "fixture-access-db" || marker.Grants != "Grant" || marker.Permission != "access.admin" {
		t.Fatalf("marker %+v", marker)
	}
	owners := map[string]string{}
	for _, owner := range marker.Owners {
		owners[owner.Type] = owner.Member()
	}
	// Folder's @audience names owner; Document's and Request's owner is
	// their createdBy; Contract, Unit and ContractParty name none.
	for typ, member := range map[string]string{"Folder": "owner", "Document": "createdBy", "Request": "createdBy"} {
		if owners[typ] != member {
			t.Errorf("owner of %s is %q, want %q (owners %v)", typ, owners[typ], member, owners)
		}
	}
	for _, typ := range []string{"Contract", "Unit", "ContractParty", "Grant", "AccessEntry"} {
		if _, ok := owners[typ]; ok {
			t.Errorf("%s has an owner %q, and its rows name none", typ, owners[typ])
		}
	}
	view := schema.Types[ir.AccessGrantViewType]
	if view == nil || view.Origin != ir.OriginAccessGrantsOperations {
		t.Fatalf("no AccessGrant type the set adds: %+v", view)
	}
	var fields []string
	for _, f := range view.Fields {
		fields = append(fields, f.Name)
	}
	if slices.Contains(fields, ir.AccessGrantTokenHashField) || !slices.Contains(fields, ir.AccessGrantHolderField) {
		t.Errorf("AccessGrant holds %v: the holder, and no token's hash", fields)
	}
}

// TestAccessGrantsOperationsRefusals: the checks the loader runs before it
// fills an @accessGrantsOperations set refuse what the expansion could not
// serve: an authored operation, a public set, a second set, a permission
// that is no permission's name, a database with no @accessGrants table, a
// name the API takes and a route another operation takes.
func TestAccessGrantsOperationsRefusals(t *testing.T) {
	db, err := LoadService(filepath.FromSlash(accessGrantsDBFixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name   string
		change func(api *ir.Schema, set *ir.OperationSet, db *ir.Schema)
		want   string
	}{
		{"an authored operation", func(_ *ir.Schema, set *ir.OperationSet, _ *ir.Schema) {
			set.Operations = []*ir.FieldDef{{Name: "extra", TypeRef: ir.TypeRef{Name: "string"}}}
		}, "Grants: an @accessGrantsOperations class has no members"},
		{"a public set", func(_ *ir.Schema, set *ir.OperationSet, _ *ir.Schema) {
			set.Public = true
		}, "Grants: @publicRoute on an @accessGrantsOperations class would list the active grants to anyone"},
		{"a second set", func(api *ir.Schema, set *ir.OperationSet, _ *ir.Schema) {
			again := *set
			again.Name = "MoreGrants"
			api.OperationSets = append(api.OperationSets, &again)
		}, "MoreGrants: @accessGrantsOperations appears on Grants too"},
		{"a permission that is no name", func(_ *ir.Schema, set *ir.OperationSet, _ *ir.Schema) {
			set.AccessGrantsOperations.Permission = "access admin"
		}, `Grants: @accessGrantsOperations names the permission "access admin", which is no permission name`},
		{"a database with no grants", func(_ *ir.Schema, _ *ir.OperationSet, db *ir.Schema) {
			for _, td := range db.Types {
				td.AccessGrants = nil
			}
		}, "Grants: @accessGrantsOperations lists the grants of fixture-access-db, which declares no @accessGrants table"},
		{"a name the API takes", func(api *ir.Schema, _ *ir.OperationSet, _ *ir.Schema) {
			api.Types["AccessGrant"] = &ir.TypeDef{Name: "AccessGrant", Role: ir.RoleEmbeddedStruct}
		}, "Grants: @accessGrantsOperations adds the type AccessGrant, and fixture-access-api already declares or imports one of that name"},
		{"a route another operation takes", func(api *ir.Schema, _ *ir.OperationSet, _ *ir.Schema) {
			for _, other := range api.OperationSets {
				if other.Name == "Access" {
					other.Operations = append(other.Operations, &ir.FieldDef{Name: "grantsElsewhere", HTTPMethod: "GET", RestPath: "grants/active", Required: true, TypeRef: ir.TypeRef{Name: "GrantView", IsArray: true}})
				}
			}
		}, "Grants: the operation activeGrants it adds is routed at GET /api/grants/active, which Access.grantsElsewhere's GET /api/grants/active also matches"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			api, err := loadAccessGrantsAPI(t, filepath.FromSlash(accessGrantsAPIFixture))
			if err != nil {
				t.Fatal(err)
			}
			var set *ir.OperationSet
			for _, each := range api.OperationSets {
				if each.IsAccessGrantsOperations() {
					each.Operations = nil
					set = each
				}
			}
			for name, td := range api.Types {
				if td.Origin == ir.OriginAccessGrantsOperations {
					delete(api.Types, name)
				}
			}
			database := db
			if tc.name == "a database with no grants" {
				database, err = LoadService(filepath.FromSlash(accessGrantsDBFixture))
				if err != nil {
					t.Fatal(err)
				}
			}
			tc.change(api, set, database)
			result := verify.CheckAccessGrantsOperations(api, verify.Input{Database: database})
			var got []string
			for _, d := range result.Errors {
				got = append(got, d.Msg)
			}
			found := false
			for _, message := range got {
				found = found || strings.Contains(message, tc.want)
			}
			if !found {
				t.Errorf("CheckAccessGrantsOperations = %q, want %q among them", got, tc.want)
			}
		})
	}
}
