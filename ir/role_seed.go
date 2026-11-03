package ir

// RoleSeed is one role a UserRole table's @seedRoles declares (D88): a row
// of the table the database holds from its first migration on, so a user
// an administrator approves into a default role can use what the role
// permits with no administrator's step first. create.sql inserts each; a
// migration plan inserts a new one, fills one an earlier registration
// created with no permissions, and sets the permissions of one whose
// declaration changes. A plan never deletes a role a seed no longer names.
type RoleSeed struct {
	// Name is the role's name, the value of the table's
	// IdentityRoleNameField.
	Name string `json:"name" yaml:"name"`

	// Permissions are the permissions the role carries, each dotted
	// segments as the identity runtimes check a role's ("finances.read").
	Permissions []string `json:"permissions" yaml:"permissions"`
}
