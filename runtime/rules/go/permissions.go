package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Permissions is where the adapter answers the core's permissions read
// (D200, ReadKindPermissions): the identity tables of the layout's
// database, the user table and its key column, the credential's disabled
// and pending columns, the role grants and the roles' permissions, each
// unquoted. behaviorlayout.Permissions writes it.
type Permissions struct {
	Users           string `json:"users"`
	User            Column `json:"user"`
	Credentials     string `json:"credentials"`
	CredentialUser  string `json:"credentialUser"`
	DisabledAt      string `json:"disabledAt"`
	PendingAt       string `json:"pendingAt"`
	Grants          string `json:"grants"`
	GrantUser       string `json:"grantUser"`
	GrantRole       string `json:"grantRole"`
	Roles           string `json:"roles"`
	RoleKey         string `json:"roleKey"`
	RolePermissions string `json:"rolePermissions"`
	// Agents is the agents' definitions table (#625), Agent its key
	// column and AgentRole the column of the role each acts with, which
	// names a row of Roles by RoleName: where an Agent's permissions are
	// read. Empty for a database with no agents.
	Agents    string  `json:"agents,omitempty"`
	Agent     *Column `json:"agent,omitempty"`
	AgentRole string  `json:"agentRole,omitempty"`
	RoleName  string  `json:"roleName,omitempty"`
}

// readPermissions answers a permissions read (D200): the permissions each
// subject a user row holds has now, through every role a grant gives the
// user, each once in the order the roles give them, and none while the
// user's credential is disabled or pending. A subject no user row holds,
// or that is no key of the user table, is left out of the answer, and the
// core holds it to no permission; so is every subject of a layout that
// names no identity tables (Layout.Permissions nil), whose database the
// adapter cannot answer for.
func readPermissions(ctx context.Context, store Store, from *Permissions, subjects, agents []string) (map[string][]string, error) {
	out, err := readAgentPermissions(ctx, store, from, agents)
	if err != nil {
		return nil, err
	}
	if from == nil || len(subjects) == 0 {
		return out, nil
	}
	dialect := store.Dialect()
	users := &Table{Type: from.Users, Table: from.Users, Key: from.User}
	// Each subject by the key its user row would hold, in its wire form.
	bySubject := map[string]string{}
	var keys []any
	var placeholders []string
	for _, subject := range subjects {
		key, err := keyValue(users, subject)
		if err != nil {
			continue
		}
		wire, err := permissionsKey(key, from.User, users)
		if err != nil {
			continue
		}
		bySubject[subject] = wire
		keys = append(keys, key)
		placeholders = append(placeholders, placeholder(dialect, len(keys)))
	}
	if len(keys) == 0 {
		return out, nil
	}
	enabled := fmt.Sprintf("c.%s IS NULL AND c.%s IS NULL", quote(from.DisabledAt), quote(from.PendingAt))
	text := fmt.Sprintf(
		"SELECT u.%s, r.%s FROM %s AS u LEFT JOIN %s AS c ON c.%s = u.%s LEFT JOIN %s AS g ON g.%s = u.%s AND %s LEFT JOIN %s AS r ON r.%s = g.%s WHERE u.%s IN (%s) ORDER BY u.%s, r.%s",
		quote(from.User.Column), quote(from.RolePermissions),
		quote(from.Users),
		quote(from.Credentials), quote(from.CredentialUser), quote(from.User.Column),
		quote(from.Grants), quote(from.GrantUser), quote(from.User.Column), enabled,
		quote(from.Roles), quote(from.RoleKey), quote(from.GrantRole),
		quote(from.User.Column), strings.Join(placeholders, ", "),
		quote(from.User.Column), quote(from.RoleKey),
	)
	rows, err := store.Query(ctx, text, keys...)
	if err != nil {
		return nil, err
	}
	held, err := heldOf(rows, from, from.User, users)
	if err != nil {
		return nil, err
	}
	for subject, wire := range bySubject {
		if permissions, ok := held[wire]; ok {
			out[subject] = permissions
		}
	}
	return out, nil
}

// readAgentPermissions answers a permissions read's Agents (#625): the
// permissions of the role each Agent's definition row names, by the
// roles' name column. An Agent no definition row holds, or that is no key
// of the definitions table, is left out, and the core holds it to none;
// so is every Agent of a layout that names no agents table.
func readAgentPermissions(ctx context.Context, store Store, from *Permissions, agents []string) (map[string][]string, error) {
	out := map[string][]string{}
	if from == nil || from.Agents == "" || from.Agent == nil || len(agents) == 0 {
		return out, nil
	}
	dialect := store.Dialect()
	table := &Table{Type: from.Agents, Table: from.Agents, Key: *from.Agent}
	byAgent := map[string]string{}
	var keys []any
	var placeholders []string
	for _, agent := range agents {
		key, err := keyValue(table, agent)
		if err != nil {
			continue
		}
		wire, err := permissionsKey(key, *from.Agent, table)
		if err != nil {
			continue
		}
		byAgent[agent] = wire
		keys = append(keys, key)
		placeholders = append(placeholders, placeholder(dialect, len(keys)))
	}
	if len(keys) == 0 {
		return out, nil
	}
	text := fmt.Sprintf(
		"SELECT a.%s, r.%s FROM %s AS a LEFT JOIN %s AS r ON r.%s = a.%s WHERE a.%s IN (%s) ORDER BY a.%s",
		quote(from.Agent.Column), quote(from.RolePermissions),
		quote(from.Agents), quote(from.Roles), quote(from.RoleName), quote(from.AgentRole),
		quote(from.Agent.Column), strings.Join(placeholders, ", "), quote(from.Agent.Column),
	)
	rows, err := store.Query(ctx, text, keys...)
	if err != nil {
		return nil, err
	}
	held, err := heldOf(rows, from, *from.Agent, table)
	if err != nil {
		return nil, err
	}
	for agent, wire := range byAgent {
		if permissions, ok := held[wire]; ok {
			out[agent] = permissions
		}
	}
	return out, nil
}

// heldOf is the permissions each key of rows holds: rows of a key and a
// role's list of permissions, which may be null for a key no role gives
// any, each permission once in the order the rows give them.
func heldOf(rows [][]any, from *Permissions, key Column, table *Table) (map[string][]string, error) {
	held := map[string][]string{}
	list := Column{Field: from.RolePermissions, Column: from.RolePermissions, Type: "string", List: true}
	roles := &Table{Type: from.Roles, Table: from.Roles}
	for _, row := range rows {
		if len(row) != 2 {
			return nil, Internal("a permissions read answered %d values a row, not 2", len(row))
		}
		wire, err := permissionsKey(row[0], key, table)
		if err != nil {
			return nil, err
		}
		if _, ok := held[wire]; !ok {
			held[wire] = []string{}
		}
		raw, ok, err := fromColumn(row[1], list, roles)
		if err != nil {
			return nil, err
		}
		if !ok {
			continue
		}
		var permissions []string
		if err := json.Unmarshal(raw, &permissions); err != nil {
			return nil, Internal("%s.%s: %s is no list of permissions", from.Roles, from.RolePermissions, raw)
		}
		for _, p := range permissions {
			if !contains(held[wire], p) {
				held[wire] = append(held[wire], p)
			}
		}
	}
	return held, nil
}

// permissionsKey is a user key, bound or read, as the text the adapter
// matches subjects by: a UUID's wire form, any other key's text.
func permissionsKey(value any, column Column, table *Table) (string, error) {
	raw, ok, err := fromColumn(value, column, table)
	if err != nil {
		return "", err
	}
	if !ok {
		return "", Internal("%s's key is null", table.Type)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return string(raw), nil
	}
	return text, nil
}
