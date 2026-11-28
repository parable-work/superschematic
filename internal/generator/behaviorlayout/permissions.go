package behaviorlayout

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	ir "github.com/parable-work/superschematic/ir"
)

// Permissions is where an adapter reads the permissions a person holds
// now, the core's permissions read (D200, ReadRequest::Permissions): the
// identity runtime's tables of the schema, which its store reads a
// principal's roles from (identitydesc). A user is answered with the
// permissions of every role a grant gives them, and none while their
// credential is disabled or pending; a subject no user row holds is left
// out, which the core holds to no permission. Each table and column is
// unquoted.
type Permissions struct {
	// Users is the user table, and User its key column, which a subject's
	// text binds as (a UUID in either form as its column's).
	Users string `json:"users"`
	User  Column `json:"user"`
	// Credentials is the UserCredential table: CredentialUser its user's
	// column, DisabledAt and PendingAt the columns that, set, hold the
	// user to no permission.
	Credentials    string `json:"credentials"`
	CredentialUser string `json:"credentialUser"`
	DisabledAt     string `json:"disabledAt"`
	PendingAt      string `json:"pendingAt"`
	// Grants is the UserRoleGrant table, GrantUser and GrantRole its
	// user's and its role's columns.
	Grants    string `json:"grants"`
	GrantUser string `json:"grantUser"`
	GrantRole string `json:"grantRole"`
	// Roles is the UserRole table, RoleKey its key column and
	// RolePermissions its list of permissions (TEXT[] on Postgres, the
	// JSON text of one on SQLite).
	Roles           string `json:"roles"`
	RoleKey         string `json:"roleKey"`
	RolePermissions string `json:"rolePermissions"`
}

// permissionsOf is the layout's Permissions of schema: its identity
// tables when a table composes Approvals, whose recheckPermission reads a
// signer's permissions again (D200), and the schema has a UserRole table;
// nil otherwise.
func permissionsOf(schema *ir.Schema) (*Permissions, error) {
	approvals := false
	for _, td := range schema.Types {
		for _, ref := range td.TableBehaviors() {
			if ref.Name == "Approvals" {
				approvals = true
			}
		}
	}
	if !approvals {
		return nil, nil
	}
	desc, ok, err := identitydesc.Describe(schema)
	if err != nil {
		return nil, fmt.Errorf("behaviorlayout: the identity tables a permissions read reads: %w", err)
	}
	if !ok || desc.Role == nil || desc.RoleGrant == nil {
		return nil, nil
	}
	return &Permissions{
		Users:           desc.User.Table,
		User:            Column{Field: desc.User.Columns.Key, Column: desc.User.Columns.Key, Type: desc.User.KeyScalar, Convert: ConvertOf(desc.User.KeyScalar)},
		Credentials:     desc.Credential.Table,
		CredentialUser:  desc.Credential.Columns.User,
		DisabledAt:      desc.Credential.Columns.DisabledAt,
		PendingAt:       desc.Credential.Columns.PendingAt,
		Grants:          desc.RoleGrant.Table,
		GrantUser:       desc.RoleGrant.Columns.User,
		GrantRole:       desc.RoleGrant.Columns.Role,
		Roles:           desc.Role.Table,
		RoleKey:         desc.Role.Columns.Key,
		RolePermissions: desc.Role.Columns.Permissions,
	}, nil
}
