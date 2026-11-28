// Package accessgrantsops adds the active grants to an API as a route
// (#735). An API marks a class with no members
// @accessGrantsOperations({ permission? }), from @superschematic/api; its
// database declares the @accessGrants table (D156):
//
//	@accessGrantsOperations({ permission: "access.admin" })
//	export abstract class Grants {}
//
// Once the set passes its checks (verify.CheckAccessGrantsOperations), and
// before the rest of the verification pass, the loader fills it here with
// one operation, activeGrants: GET grants/active, whose query arguments
// holder, targetType and targetId keep the grants whose members hold them,
// answering the grants active now as AccessGrant, a type it adds with no
// token's hash. The operation needs a caller (Auth), takes the class's
// @requirePermission as its own, which names none unless the class does,
// and carries the marker ir.FieldDef.AccessGrantsOperation: the grants
// table, the permission whose holder lists every active grant, and the
// member that names the owner of each table's rows, which a server's
// generated handler reads to tell a target's owner, who lists the grants
// on it. It is an MCP tool, active_grants. So every check the pass makes of
// an authored operation holds for it, and the OpenAPI document, the SDKs,
// the permission catalog and the tool documents read it as any other; the
// TypeScript, Go and Rust servers route it to a generated handler.
package accessgrantsops

import (
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The scalars the added type and the arguments name beside the primitives.
const (
	uuidScalar     = "Identity.UUID"
	dateTimeScalar = "Temporal.DateTime"
)

// auditCreatedBy is the field a table's creator is stamped in, whose
// holder owns a row of a table with no @audience owner.
const auditCreatedBy = "createdBy"

// description says what the operation does, as its comment, its @docs
// description and its tool's.
const description = "Lists the grants active now: started, not ended by their date or by their target reaching its end state, and not revoked. holder keeps a holder's grants, and targetType with targetId the grants on one row. A caller who holds the administrator's permission lists every active grant; the owner of the row targetType and targetId name lists the grants on it; and a holder lists its own, with holder its own id. Any other request is refused."

// Plan is what the expansion adds for the API's @accessGrantsOperations
// set: its operation, and the type it lists.
type Plan struct {
	Operation *ir.FieldDef
	Types     []*ir.TypeDef
	// Scalars are the scalar names the operation and the type name.
	Scalars []string
}

// PlanSet works out what the expansion adds for set, given db, the API's
// database. It assumes the set passed verify.CheckAccessGrantsOperations:
// db declares the @accessGrants table.
func PlanSet(set *ir.OperationSet, db *ir.Schema, reg *registry.Registry) (*Plan, error) {
	grants := db.AccessGrantsTable()
	if grants == nil {
		return nil, fmt.Errorf("%s declares no @accessGrants table", db.Name)
	}
	marker := &ir.AccessGrantsOperation{
		Database:   db.Name,
		Grants:     grants.Name,
		Permission: set.AccessGrantsOperations.AdminPermission(),
		Owners:     Owners(db),
	}
	policy := reg.ToolInvocationPolicy()
	op := &ir.FieldDef{
		Name:        ir.AccessGrantsOpActive,
		Comment:     description,
		Required:    true,
		HTTPMethod:  "GET",
		RestPath:    ir.AccessGrantsActivePath,
		Auth:        true,
		Permissions: slices.Clone(set.Permissions),
		Docs: &ir.OperationDocs{
			Title:         "Active grants",
			Description:   description,
			Capability:    "access.grants.active",
			Lifecycle:     ir.DocsLifecycleActive,
			Visibility:    ir.DocsVisibilityPublic,
			MappingStatus: ir.DocsMappingStatusMapped,
			ReplayMode:    ir.DocsReplayModeReadOnly,
		},
		Origin:                ir.OriginAccessGrantsOperations,
		AccessGrantsOperation: marker,
		Arguments: []*ir.ArgumentDef{
			{Name: ir.AccessGrantsHolderArgument, Comment: "Keeps the grants this principal holds: a party's or an agent run's id.", TypeRef: ir.TypeRef{Name: uuidScalar}, IsQuery: true},
			{Name: ir.AccessGrantsTargetTypeArgument, Comment: "Keeps the grants on rows of this table, by its type's name.", TypeRef: ir.TypeRef{Name: "string"}, IsQuery: true},
			{Name: ir.AccessGrantsTargetIDArgument, Comment: "Keeps the grants on the row of this key, beside targetType.", TypeRef: ir.TypeRef{Name: uuidScalar}, IsQuery: true},
		},
		TypeRef: ir.TypeRef{Name: ir.AccessGrantViewType, IsArray: true},
	}
	op.MCP = &ir.OperationMCP{Handle: ir.AccessGrantsHandle, Invocation: ir.MCPInvocation{Key: policy.Key, Value: policy.Default}}
	if callers := ir.EffectiveServiceCallers(set, op); callers != nil && callers.Mode == ir.ServiceCallersRequire {
		// Only a calling service reaches the route, so no end user's agent
		// can call the tool (D37).
		op.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: "Only the services that @requireService on " + set.Name + " admits call it (D37)."}
	}
	return &Plan{Operation: op, Types: []*ir.TypeDef{grantView()}, Scalars: []string{uuidScalar, dateTimeScalar}}, nil
}

// grantView is the type the operation lists: a grant without its token's
// hash and its revocation, which an active grant has none of.
func grantView() *ir.TypeDef {
	field := func(name, typeName string, required bool, comment string) *ir.FieldDef {
		return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: required, Origin: ir.OriginAccessGrantsOperations}
	}
	return &ir.TypeDef{
		Name:    ir.AccessGrantViewType,
		Comment: "A grant active now: whom it lets reach which row, with which role, and until when (D156).",
		Role:    ir.RoleEmbeddedStruct,
		Origin:  ir.OriginAccessGrantsOperations,
		Display: &ir.TypeDisplay{Noun: "Grant", Plural: "Grants", TitleField: "role", Icon: "key", SummaryFields: []string{"holder", "role", "targetType", "targetId", "endsAt"}},
		Fields: []*ir.FieldDef{
			field("id", uuidScalar, true, "The grant's id."),
			field(ir.AccessGrantHolderField, uuidScalar, true, "The principal the grant lets reach rows: a party's or an agent run's id."),
			field(ir.AccessGrantHolderKindField, "string", true, "The kind of that principal: party or agent."),
			field(ir.AccessGrantRoleField, "string", true, "The name of the role whose permissions the holder acts with."),
			field(ir.AccessGrantTargetTypeField, "string", true, "The table of the target row, by its type's name."),
			field(ir.AccessGrantTargetIDField, uuidScalar, true, "The target row's key."),
			field(ir.AccessGrantStartsAtField, dateTimeScalar, true, "When the grant started."),
			field(ir.AccessGrantEndsAtField, dateTimeScalar, false, "When the grant ends; null for no date."),
			field(ir.AccessGrantEndsWhenFieldField, "string", false, "The target's field whose state ends the grant; null for no such end."),
			field(ir.AccessGrantEndsWhenStateField, "string", false, "The state of that field that ends the grant."),
		},
	}
}

// Owners are the tables of db a grant may target, keyed by one UUID, whose
// rows name an owner, each with the field that holds it: an @audience
// table's owner, a field that holds a UUID or a to-one relation, else the
// table's createdBy when it holds a UUID. Sorted by type.
func Owners(db *ir.Schema) []ir.AccessGrantOwner {
	var out []ir.AccessGrantOwner
	for _, td := range db.Types {
		if td == nil || td.Role != ir.RoleDBTable || td.Queue != nil || td.Projection != nil || td.JsonField || td.AccessGrants != nil || td.AccessLog != nil {
			continue
		}
		keys := 0
		uuidKey := false
		for _, fd := range td.Fields {
			if fd != nil && fd.Key {
				keys++
				uuidKey = fd.TypeRef.Name == uuidScalar && !fd.TypeRef.IsArray
			}
		}
		if keys != 1 || !uuidKey {
			continue
		}
		if owner, ok := ownerOf(td); ok {
			out = append(out, owner)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Type < out[j].Type })
	return out
}

// ownerOf is the field that holds td's owner, as @audience names it (a
// relation by its name, or its key's member <name>Id), else createdBy.
func ownerOf(td *ir.TypeDef) (ir.AccessGrantOwner, bool) {
	name := auditCreatedBy
	if td.Audience != nil && td.Audience.Owner != "" {
		name = td.Audience.Owner
		if fd := fieldNamed(td, strings.TrimSuffix(name, "Id")); fd != nil && fd.Relation != nil && !fd.TypeRef.IsArray {
			return ir.AccessGrantOwner{Type: td.Name, Field: fd.Name, Relation: true}, true
		}
	}
	fd := fieldNamed(td, name)
	switch {
	case fd == nil || fd.TypeRef.IsArray:
		return ir.AccessGrantOwner{}, false
	case fd.Relation != nil:
		return ir.AccessGrantOwner{Type: td.Name, Field: fd.Name, Relation: true}, true
	case fd.TypeRef.Name == uuidScalar:
		return ir.AccessGrantOwner{Type: td.Name, Field: fd.Name}, true
	}
	return ir.AccessGrantOwner{}, false
}

func fieldNamed(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd != nil && fd.Name == name {
			return fd
		}
	}
	return nil
}

// Expand fills the @accessGrantsOperations set of api with the route of
// the active grants of db, the API's database, and adds the type it lists
// (#735). It assumes the set passed verify.CheckAccessGrantsOperations,
// and does nothing for an API without one. It returns the scalar names the
// added type and operation use, so the caller can hydrate any api did not
// hold; one only db declares is copied from it. It may run again on its
// own output.
func Expand(api, db *ir.Schema, reg *registry.Registry) ([]string, error) {
	var set *ir.OperationSet
	for _, candidate := range api.OperationSets {
		if candidate.IsAccessGrantsOperations() {
			set = candidate
			break
		}
	}
	if set == nil || db == nil {
		return nil, nil
	}
	plan, err := PlanSet(set, db, reg)
	if err != nil {
		return nil, fmt.Errorf("accessgrantsops: %s: %w", set.Name, err)
	}
	set.Operations = []*ir.FieldDef{plan.Operation}
	if api.Types == nil {
		api.Types = map[string]*ir.TypeDef{}
	}
	for _, td := range plan.Types {
		td.Owner = api.Name
		api.Types[td.Name] = td
	}
	if api.Scalars == nil {
		api.Scalars = map[string]*ir.ScalarDef{}
	}
	for _, name := range plan.Scalars {
		if api.Scalars[name] != nil {
			continue
		}
		def := &ir.ScalarDef{Name: name}
		if declared := db.Scalars[name]; declared != nil {
			copied := *declared
			copied.TypeMappings = maps.Clone(declared.TypeMappings)
			copied.ReservedWords = slices.Clone(declared.ReservedWords)
			def = &copied
		}
		api.Scalars[name] = def
	}
	return plan.Scalars, nil
}
