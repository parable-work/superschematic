package ir

// The active grants as a route (#735). An API lists the grants active now
// by marking a class with no members @accessGrantsOperations({ ... }), from
// @superschematic/api:
//
//	@accessGrantsOperations({ permission: "access.admin" })
//	export abstract class Grants {}
//
// The reader records the decorator as OperationSet.AccessGrantsOperations
// and the class's @auth, @requirePermission and @publicRoute as the set's
// Auth, Permissions and Public. The loader reads the API's database
// (Schema.Database), which declares the @accessGrants table, and fills the
// set with one operation, activeGrants: GET grants/active, with the query
// arguments holder, targetType and targetId, answering the grants active
// now (the runtimes' activeGrants) as AccessGrant views, which hold no
// token's hash. It has Origin OriginAccessGrantsOperations and
// FieldDef.AccessGrantsOperation set, and the loader adds AccessGrant with
// the same Origin.
//
// Who may list (#735): a caller who holds the declaration's permission
// lists every active grant the query keeps; the owner of the target row a
// query names by targetType and targetId lists the active grants on it;
// and a holder lists its own, a query whose holder is the caller. Any
// other caller is refused 403. The route needs a caller, and the server's
// handler, which the TypeScript, Go and Rust servers generate, decides
// which of the three the caller is.

// OriginAccessGrantsOperations is the Origin of the operation and the type
// the loader adds for an @accessGrantsOperations set, so the schema writer
// writes the decorator in their place.
const OriginAccessGrantsOperations = "accessGrantsOperations"

// AccessGrantsDefaultPermission is the permission whose holder lists every
// active grant when the declaration names none.
const AccessGrantsDefaultPermission = "access.admin"

// AccessGrantsOpActive is the one operation of an @accessGrantsOperations
// set: the grants active now.
const AccessGrantsOpActive = "activeGrants"

// AccessGrantsActivePath is the operation's route below the API's prefix.
const AccessGrantsActivePath = "grants/active"

// AccessGrantsHandle is the operation's MCP tool.
const AccessGrantsHandle = "active_grants"

// The query arguments of the active grants, each keeping the grants whose
// member of that name holds the value.
const (
	AccessGrantsHolderArgument     = "holder"
	AccessGrantsTargetTypeArgument = "targetType"
	AccessGrantsTargetIDArgument   = "targetId"
)

// AccessGrantViewType is the type the loader adds that the operation lists:
// a grant as the API shows it, with no token's hash.
const AccessGrantViewType = "AccessGrant"

// AccessGrantsOperationsConfig is @accessGrantsOperations on an operation
// set.
type AccessGrantsOperationsConfig struct {
	// Permission is the permission whose holder lists every active grant
	// (a grant of a permission that covers it does too). Empty is
	// AccessGrantsDefaultPermission.
	Permission string `json:"permission,omitempty" yaml:"permission,omitempty"`
}

// AdminPermission is the permission whose holder lists every active grant:
// the config's, else AccessGrantsDefaultPermission.
func (c *AccessGrantsOperationsConfig) AdminPermission() string {
	if c == nil || c.Permission == "" {
		return AccessGrantsDefaultPermission
	}
	return c.Permission
}

// AccessGrantsOperation marks the operation the loader added for an
// @accessGrantsOperations set: the grants it lists and who may list them.
type AccessGrantsOperation struct {
	// Database is the DB service whose @accessGrants table holds the
	// grants, the API's database.
	Database string `json:"database" yaml:"database"`

	// Grants is the type name of the @accessGrants table.
	Grants string `json:"grants" yaml:"grants"`

	// Permission is the permission whose holder lists every active grant.
	Permission string `json:"permission" yaml:"permission"`

	// Owners are the tables of the database a grant may target whose rows
	// name an owner, each with the member that holds it: the target's
	// owner lists the grants on it. A table with none has no owner who
	// lists them. Sorted by type.
	Owners []AccessGrantOwner `json:"owners,omitempty" yaml:"owners,omitempty"`
}

// OwnerOf is the owner of targetType's rows, and false for a type with
// none.
func (o *AccessGrantsOperation) OwnerOf(targetType string) (AccessGrantOwner, bool) {
	if o == nil {
		return AccessGrantOwner{}, false
	}
	for _, owner := range o.Owners {
		if owner.Type == targetType {
			return owner, true
		}
	}
	return AccessGrantOwner{}, false
}

// AccessGrantOwner is the member of a table's rows that holds the row's
// owner, as the active grants' route reads it: the @audience owner, a
// field that holds a UUID or a to-one relation, else createdBy.
type AccessGrantOwner struct {
	// Type is the table's type name.
	Type string `json:"type" yaml:"type"`

	// Field is the field that holds the owner: a field that holds a UUID,
	// or a to-one relation, whose key does.
	Field string `json:"field" yaml:"field"`

	// Relation reports whether Field is a to-one relation, whose key the
	// row holds as its member <field>Id.
	Relation bool `json:"relation,omitempty" yaml:"relation,omitempty"`
}

// Member is the row's member that holds the owner's id, as the TypeScript
// and Rust repositories and a Go row's JSON name it: the field, or
// <field>Id of a relation.
func (o AccessGrantOwner) Member() string {
	if o.Relation {
		return o.Field + "Id"
	}
	return o.Field
}

// IsAccessGrantsOperations reports whether the set is
// @accessGrantsOperations, whose operation the loader adds.
func (s *OperationSet) IsAccessGrantsOperations() bool {
	return s != nil && s.AccessGrantsOperations != nil
}
