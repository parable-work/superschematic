package verify

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader/identity"
	ir "github.com/parable-work/superschematic/ir"
)

// checkIdentity validates the User and UserRole traits of the core user
// model (D50) before the loader adds the tables they own
// (identity.Expand). Everything the expansion and the identity runtimes
// rely on is checked here, so the expansion itself cannot fail.
func checkIdentity(schema *ir.Schema, r *Result) {
	bases := codegen.BaseTypeNames(schema)
	var users, roles []*ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td.User == nil && td.UserRole == nil {
			continue
		}
		if td.User != nil && td.UserRole != nil {
			r.errorf(td.Owner, "%s: a table implements User or UserRole, not both", td.Name)
			continue
		}
		trait := "User"
		if td.UserRole != nil {
			trait = "UserRole"
		}
		if !checkIdentityPlacement(schema, td, trait, bases, r) {
			continue
		}
		if td.User != nil {
			users = append(users, td)
		} else {
			roles = append(roles, td)
		}
	}
	for i := 1; i < len(users); i++ {
		r.errorf(users[i].Owner, "%s: a schema has at most one User table, and %s is one", users[i].Name, users[0].Name)
	}
	for i := 1; i < len(roles); i++ {
		r.errorf(roles[i].Owner, "%s: a schema has at most one UserRole table, and %s is one", roles[i].Name, roles[0].Name)
	}
	if len(roles) > 0 && schema.UserTable() == nil {
		r.errorf(roles[0].Owner, "%s: the UserRole trait needs a User table in the same schema", roles[0].Name)
	}
	for _, td := range users {
		checkUserTable(schema, td, r)
	}
	for _, td := range roles {
		checkUserRoleTable(schema, td, r)
	}
	if len(users) > 0 {
		checkIdentityNames(schema, users[0], r)
	}
}

// checkIdentityPlacement reports whether td, which carries trait, is a
// table of a DB schema, and refuses it when it is not.
func checkIdentityPlacement(schema *ir.Schema, td *ir.TypeDef, trait string, bases map[string]bool, r *Result) bool {
	switch {
	case schema.Kind != ir.SchemaKindDB:
		r.errorf(td.Owner, "%s: the %s trait is only allowed in a DB schema (this schema has kind %s)", td.Name, trait, schema.Kind)
	case td.Role != ir.RoleDBTable:
		r.errorf(td.Owner, "%s: the %s trait is only allowed on DB table types (this type has role %s)", td.Name, trait, td.Role)
	case td.JsonField:
		r.errorf(td.Owner, "%s: the %s trait is on a @jsonField type, which is stored as JSON and gets no table", td.Name, trait)
	case bases[td.Name]:
		on := ""
		if tables := extendingTables(schema, td.Name, bases); len(tables) > 0 {
			on = " (" + strings.Join(tables, ", ") + ")"
		}
		r.errorf(td.Owner, "%s: the %s trait is on a base class, which gets no table; implement it on the table that extends %s%s", td.Name, trait, td.Name, on)
	default:
		return true
	}
	return false
}

// checkUserTable validates the User table: its key, the login field users
// sign in with and the field a principal's name comes from.
func checkUserTable(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	checkIdentityKey(td, "User", r)
	cfg := td.User
	var login *ir.FieldDef
	switch {
	case cfg.Login == "":
		r.errorf(td.Owner, "%s: the User trait needs login, the field users sign in with", td.Name)
	case fieldNamed(td, cfg.Login) == nil:
		r.errorf(td.Owner, "%s: the User trait's login %q is not a field of %s", td.Name, cfg.Login, td.Name)
	default:
		login = fieldNamed(td, cfg.Login)
		checkLoginField(schema, td, login, r)
	}

	if cfg.Name != "" {
		switch name := fieldNamed(td, cfg.Name); {
		case name == nil:
			r.errorf(td.Owner, "%s: the User trait's name %q is not a field of %s", td.Name, cfg.Name, td.Name)
		case !isTextField(schema, name):
			r.errorf(td.Owner, "%s.%s: the User trait's name must be text (a string, or a scalar whose values are strings), and %s is not", td.Name, name.Name, typeRefString(name.TypeRef))
		}
		return
	}
	// Without a name, a principal's name is its login.
	if login != nil && isCaseInsensitiveScalar(schema, login) && !isTextField(schema, login) {
		r.errorf(td.Owner, "%s.%s: the User trait has no name, so a principal's name is the login, which must then be text (a string, or a scalar whose values are strings), and %s is not; set name", td.Name, login.Name, typeRefString(login.TypeRef))
	}
}

// checkLoginField validates the User table's login: one required, @unique
// value of a scalar declared case-insensitive. The scalar's normalize step
// and its column type carry the case rule; the model adds none.
func checkLoginField(schema *ir.Schema, td *ir.TypeDef, login *ir.FieldDef, r *Result) {
	if !login.Unique {
		r.errorf(td.Owner, "%s.%s: the User trait's login must be @unique", td.Name, login.Name)
	}
	if !login.Required {
		r.errorf(td.Owner, "%s.%s: the User trait's login must be required", td.Name, login.Name)
	}
	switch {
	case login.TypeRef.IsMap:
		r.errorf(td.Owner, "%s.%s: the User trait's login must be one value, not a map", td.Name, login.Name)
	case login.TypeRef.IsArray:
		r.errorf(td.Owner, "%s.%s: the User trait's login must be one value, not a list", td.Name, login.Name)
	case !isCaseInsensitiveScalar(schema, login):
		r.errorf(td.Owner, "%s.%s: the User trait's login must be typed by a scalar declared case-insensitive, and %s is not one", td.Name, login.Name, login.TypeRef.Name)
	}
}

// checkUserRoleTable validates the UserRole table: its key, its @unique
// text name and its list of permissions.
func checkUserRoleTable(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	checkIdentityKey(td, "UserRole", r)
	switch name := fieldNamed(td, ir.IdentityRoleNameField); {
	case name == nil:
		r.errorf(td.Owner, "%s: the UserRole trait needs a %s field, the role's @unique text name", td.Name, ir.IdentityRoleNameField)
	default:
		if !name.Unique {
			r.errorf(td.Owner, "%s.%s: the UserRole trait's %s must be @unique", td.Name, name.Name, ir.IdentityRoleNameField)
		}
		if !isTextField(schema, name) {
			r.errorf(td.Owner, "%s.%s: the UserRole trait's %s must be text (a string, or a scalar whose values are strings), and %s is not", td.Name, name.Name, ir.IdentityRoleNameField, typeRefString(name.TypeRef))
		}
	}
	switch perms := fieldNamed(td, ir.IdentityRolePermissionsField); {
	case perms == nil:
		r.errorf(td.Owner, "%s: the UserRole trait needs a %s field, a list of strings", td.Name, ir.IdentityRolePermissionsField)
	case perms.TypeRef.Name != "string" || perms.TypeRef.IsMap || perms.TypeRef.ArrayDepth() != 1:
		r.errorf(td.Owner, "%s.%s: the UserRole trait's %s must be a list of strings, and it is %s", td.Name, perms.Name, ir.IdentityRolePermissionsField, typeRefString(perms.TypeRef))
	}
}

// checkIdentityKey checks the one @key a User or UserRole table needs: the
// tables the expansion adds relate to it.
func checkIdentityKey(td *ir.TypeDef, trait string, r *Result) {
	keys := 0
	for _, fd := range td.Fields {
		if fd.Key {
			keys++
		}
	}
	if keys != 1 {
		r.errorf(td.Owner, "%s: the %s trait requires exactly one @key field, found %d", td.Name, trait, keys)
	}
}

// checkIdentityNames refuses a definition or a table that takes a name of a
// table the expansion adds beside user, the schema's User table, and an
// authored index whose identifier equals one it adds.
func checkIdentityNames(schema *ir.Schema, user *ir.TypeDef, r *Result) {
	for _, table := range identity.Tables {
		if definedName(schema, table) {
			owner := user.Owner
			if td := schema.Types[table]; td != nil {
				owner = td.Owner
			}
			r.errorf(owner, "%s: the User trait of %s adds a table named %s, which the schema already defines; rename it", table, user.Name, table)
			continue
		}
		if td := dbTableNamed(schema, codegen.ToSnakeCase(table)); td != nil {
			r.errorf(td.Owner, "%s: its table %s is the table of %s, which the User trait of %s adds; rename it", td.Name, codegen.ToSnakeCase(table), table, user.Name)
		}
	}
	generated := identity.Indexes(schema)
	if len(generated) == 0 {
		return
	}
	authored := authoredIndexNames(schema)
	for _, gen := range generated {
		id, err := sqlutil.IndexName(codegen.ToSnakeCase(gen.Type), gen.Index.Keys, gen.Index.Name, gen.Index.Unique)
		if err != nil {
			continue
		}
		if owner, ok := authored[id]; ok {
			td := schema.Types[owner]
			r.errorf(td.Owner, "%s: index %s collides with the index the user model adds to %s; give it another name", td.Name, id, gen.Type)
		}
	}
}

// isCaseInsensitiveScalar reports whether fd is typed by a scalar whose
// definition declares it case-insensitive (ScalarDef.CaseInsensitive). A
// string is not one.
func isCaseInsensitiveScalar(schema *ir.Schema, fd *ir.FieldDef) bool {
	scalar := schema.Scalars[fd.TypeRef.Name]
	return scalar != nil && scalar.CaseInsensitive
}

// isTextField reports whether fd holds one text value: a string, or a
// scalar whose language primitive is string and whose values are strings,
// not any JSON value, an object or an array.
func isTextField(schema *ir.Schema, fd *ir.FieldDef) bool {
	if fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.JsonField {
		return false
	}
	if fd.TypeRef.Name == "string" {
		return true
	}
	scalar := schema.Scalars[fd.TypeRef.Name]
	if scalar == nil || scalar.LanguagePrimitive != ir.LanguageString {
		return false
	}
	switch scalar.TypeMappings["json_schema"] {
	case ir.JSONSchemaAnyType, ir.JSONSchemaObjectType, ir.JSONSchemaArrayType:
		return false
	}
	return true
}
