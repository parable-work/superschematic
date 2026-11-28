package behaviorlayout

import (
	"fmt"

	"github.com/parable-work/superschematic/internal/generator/identitydesc"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
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
	// Agents is the agents' definitions table (#625, the
	// @agents("definitions") class), Agent its key column and AgentRole
	// the column of the role each acts with, which names a row of Roles by
	// RoleName: where an Approvals requirement with agentRole reads an
	// Agent's permissions. Empty for a database with no agents.
	Agents    string  `json:"agents,omitempty"`
	Agent     *Column `json:"agent,omitempty"`
	AgentRole string  `json:"agentRole,omitempty"`
	RoleName  string  `json:"roleName,omitempty"`
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
	out := &Permissions{
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
	}
	if agents := schema.AgentsTable(ir.AgentDefinitions); agents != nil {
		key, role := agentColumns(schema, agents)
		if key != nil && role != "" {
			out.Agents, out.Agent, out.AgentRole, out.RoleName = sqlutil.TableName(agents), key, role, desc.Role.Columns.Name
		}
	}
	return out, nil
}

// agentColumns are the agents' definitions table's key column and the
// column of the role each agent acts with (D158's role field).
func agentColumns(schema *ir.Schema, agents *ir.TypeDef) (*Column, string) {
	var key *Column
	role := ""
	for _, fd := range agents.Fields {
		switch {
		case fd == nil:
		case fd.Key:
			column := sqlutil.ColumnNameIn(schema, fd)
			key = &Column{Field: fd.Name, Column: column, Type: fd.TypeRef.Name, Convert: ConvertOf(fd.TypeRef.Name)}
		case fd.Name == "role":
			role = sqlutil.ColumnNameIn(schema, fd)
		}
	}
	if key == nil && sqlutil.HasIDColumn(agents) {
		// The id sqlgen adds to a table with no key.
		key = &Column{Field: "id", Column: "id", Type: "Identity.UUID", Convert: ConvertOf("Identity.UUID")}
	}
	return key, role
}
