package identity

import (
	ir "github.com/parable-work/superschematic/ir"
)

// The route expansion fills an API's @userSessions and @userAdministration
// sets with the user model's operations and adds the types they take and
// return (D50), as Expand adds a DB schema's tables. ir/identity_routes.go
// names every operation, route, rule and type. Each added operation, type,
// field and enum carries ir.OriginIdentity, so the schema writer writes the
// decorators instead, and each operation carries FieldDef.IdentityOperation,
// so the servers route it to their identity runtime.

// RouteTypes are the types and the enum the route expansion can add, in
// the order it adds them. An API with a route set reserves every name.
var RouteTypes = []string{
	ir.IdentitySessionTransportEnum,
	ir.IdentityLoginInputType,
	ir.IdentityRegisterInputType,
	ir.IdentityLoginResultType,
	ir.IdentitySessionUserType,
	ir.IdentityRoleRefType,
	ir.IdentityCurrentUserType,
	ir.IdentityCapabilitiesType,
	ir.IdentityChangePasswordInputType,
	ir.IdentityCreateUserInputType,
	ir.IdentitySetPasswordInputType,
	ir.IdentityUserType,
	ir.IdentityRoleInputType,
	ir.IdentityRoleType,
}

// Scalars the added types use beside the User and UserRole tables' own.
const passwordScalar = "Auth.Password"

// ExpandRoutes fills each route set of api with its operations and adds the
// types they use, reading the users' and roles' shapes from authDB, the
// schema api's authDb names. permissionPrefix is the naming key
// identity_permission_prefix. It assumes the sets and authDB passed
// verification, and does nothing for an API without a route set. It returns
// the scalar names the added types use, so the caller can hydrate any api
// did not already hold; one only authDB declared is copied from it.
func ExpandRoutes(api, authDB *ir.Schema, permissionPrefix string) []string {
	var sets []*ir.OperationSet
	for _, set := range api.OperationSets {
		if set.IsIdentityRoutes() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 || authDB == nil || authDB.UserTable() == nil {
		return nil
	}
	e := newRouteExpansion(api, authDB, permissionPrefix)
	for _, set := range sets {
		switch {
		case set.UserSessions != nil:
			set.Operations = e.sessionOperations(set.UserSessions)
		case set.UserAdministration != nil:
			set.Operations = e.administrationOperations(set.UserAdministration)
		}
	}
	return e.finish()
}

// routeExpansion holds what the expansion reads from the authDb and what
// it has added.
type routeExpansion struct {
	api, db *ir.Schema
	prefix  string

	// The User table's key, login and name types, and whether its name is
	// a field of its own and required.
	userKey, login, name  string
	ownName, nameRequired bool

	// The UserRole table's key and name types: string without one.
	roleKey, roleName string

	added   map[string]bool
	scalars []string
}

func newRouteExpansion(api, db *ir.Schema, prefix string) *routeExpansion {
	user := db.UserTable()
	e := &routeExpansion{api: api, db: db, prefix: prefix, roleKey: "string", roleName: "string", added: map[string]bool{}}
	e.userKey = keyOf(user)
	loginField := fieldOf(user, user.User.Login)
	e.login = loginField.TypeRef.Name
	e.name, e.nameRequired = e.login, true
	if user.User.Name != "" && user.User.Name != user.User.Login {
		nameField := fieldOf(user, user.User.Name)
		e.name, e.nameRequired, e.ownName = nameField.TypeRef.Name, nameField.Required, true
	}
	if role := db.UserRoleTable(); role != nil {
		e.roleKey = keyOf(role)
		e.roleName = fieldOf(role, ir.IdentityRoleNameField).TypeRef.Name
	}
	return e
}

// sessionOperations returns the operations of @userSessions under its
// path: login, logout, me, capabilities and changePassword, me and
// capabilities alone without the login, and register with Register.
func (e *routeExpansion) sessionOperations(cfg *ir.UserSessionsConfig) []*ir.FieldDef {
	path := cfg.UserSessionsPath()
	var ops []*ir.FieldDef
	if !cfg.NoLogin {
		login := e.operation(ir.IdentityOpLogin, "POST", path+"/login", ir.IdentityLoginResultType,
			"Signs a user in with their login and password and starts a session. A bearer session answers its token; a cookie session sets the session cookie and answers none.")
		e.input(login, ir.IdentityLoginInputType)
		public(login)
		rateLimited(login, ir.IdentityLoginRateLimit)
		hiddenTool(login)
		logout := e.operation(ir.IdentityOpLogout, "POST", path+"/logout", "boolean",
			"Ends the caller's session and clears the session cookie. It answers true.")
		logout.Auth = true
		ops = append(ops, login, logout)
	}
	me := e.operation(ir.IdentityOpMe, "GET", path+"/me", ir.IdentityCurrentUserType,
		"The caller's user, the roles they hold and the permissions those roles grant.")
	me.Auth = true
	capabilities := e.operation(ir.IdentityOpCapabilities, "GET", path+"/capabilities", ir.IdentityCapabilitiesType,
		"For each operation of the API an end user may call, keyed by its OpenAPI operation id, whether its route admits the caller.")
	capabilities.Auth = true
	ops = append(ops, me, capabilities)
	if !cfg.NoLogin {
		change := e.operation(ir.IdentityOpChangePassword, "POST", path+"/password", "boolean",
			"Changes the caller's password, given their current one, and ends their other sessions. It answers true.")
		e.input(change, ir.IdentityChangePasswordInputType)
		change.Auth = true
		rateLimited(change, ir.IdentityChangePasswordRateLimit)
		hiddenTool(change)
		ops = append(ops, change)
	}
	if cfg.Register && !cfg.NoLogin {
		register := e.operation(ir.IdentityOpRegister, "POST", path+"/register", ir.IdentityLoginResultType,
			"Creates a user with the login, name and password given and signs them in, as login does.")
		e.input(register, ir.IdentityRegisterInputType)
		public(register)
		rateLimited(register, ir.IdentityRegisterRateLimit)
		hiddenTool(register)
		ops = append(ops, register)
	}
	return ops
}

// administrationOperations returns the operations of @userAdministration
// under its path, each needing one of the four permissions.
func (e *routeExpansion) administrationOperations(cfg *ir.UserAdministrationConfig) []*ir.FieldDef {
	path := cfg.UserAdministrationPath()
	users, user, roles, role := path+"/users", path+"/users/{id}", path+"/roles", path+"/roles/{id}"
	grant := user + "/roles/{roleId}"

	createUser := e.operation(ir.IdentityOpCreateUser, "POST", users, ir.IdentityUserType,
		"Creates a user with the login, name and password given.")
	e.input(createUser, ir.IdentityCreateUserInputType)
	hiddenTool(createUser)
	listUsers := e.operation(ir.IdentityOpListUsers, "GET", users, ir.IdentityUserType,
		"Lists the users and the roles each holds.")
	listUsers.TypeRef.IsArray = true
	getUser := e.operation(ir.IdentityOpGetUser, "GET", user, ir.IdentityUserType,
		"One user and the roles they hold.")
	disableUser := e.operation(ir.IdentityOpDisableUser, "POST", user+"/disable", ir.IdentityUserType,
		"Disables a user, who can no longer sign in, and ends their sessions.")
	enableUser := e.operation(ir.IdentityOpEnableUser, "POST", user+"/enable", ir.IdentityUserType,
		"Enables a disabled user.")
	setPassword := e.operation(ir.IdentityOpSetUserPassword, "PUT", user+"/password", "boolean",
		"Sets a user's password and ends their sessions. It answers true.")
	hiddenTool(setPassword)
	for _, op := range []*ir.FieldDef{getUser, disableUser, enableUser, setPassword} {
		e.pathArgument(op, ir.IdentityIDArgument, e.userKey)
	}
	e.input(setPassword, ir.IdentitySetPasswordInputType)

	listRoles := e.operation(ir.IdentityOpListRoles, "GET", roles, ir.IdentityRoleType,
		"Lists the roles and the permissions each grants.")
	listRoles.TypeRef.IsArray = true
	createRole := e.operation(ir.IdentityOpCreateRole, "POST", roles, ir.IdentityRoleType,
		"Creates a role. The caller's own permissions must cover each permission it grants.")
	e.input(createRole, ir.IdentityRoleInputType)
	updateRole := e.operation(ir.IdentityOpUpdateRole, "PUT", role, ir.IdentityRoleType,
		"Renames a role or replaces its permissions. The caller's own permissions must cover each permission given.")
	e.pathArgument(updateRole, ir.IdentityIDArgument, e.roleKey)
	e.input(updateRole, ir.IdentityRoleInputType)
	deleteRole := e.operation(ir.IdentityOpDeleteRole, "DELETE", role, "boolean",
		"Deletes a role and every grant of it. It answers true.")
	e.pathArgument(deleteRole, ir.IdentityIDArgument, e.roleKey)
	grantRole := e.operation(ir.IdentityOpGrantRole, "PUT", grant, ir.IdentityUserType,
		"Grants a user a role. The caller's own permissions must cover the role's.")
	revokeRole := e.operation(ir.IdentityOpRevokeRole, "DELETE", grant, ir.IdentityUserType,
		"Revokes a role from a user.")
	for _, op := range []*ir.FieldDef{grantRole, revokeRole} {
		e.pathArgument(op, ir.IdentityIDArgument, e.userKey)
		e.pathArgument(op, ir.IdentityRoleIDArgument, e.roleKey)
	}

	e.permission(ir.IdentityPermissionUsersRead, listUsers, getUser)
	e.permission(ir.IdentityPermissionUsersWrite, createUser, disableUser, enableUser, setPassword)
	e.permission(ir.IdentityPermissionRolesRead, listRoles)
	e.permission(ir.IdentityPermissionRolesWrite, createRole, updateRole, deleteRole, grantRole, revokeRole)
	return []*ir.FieldDef{
		createUser, listUsers, getUser, disableUser, enableUser, setPassword,
		listRoles, createRole, updateRole, deleteRole, grantRole, revokeRole,
	}
}

// operation returns the operation named name, at method and route, with
// result, and adds the result's type.
func (e *routeExpansion) operation(name, method, route, result, comment string) *ir.FieldDef {
	e.use(result)
	return &ir.FieldDef{
		Name:              name,
		Comment:           comment,
		TypeRef:           ir.TypeRef{Name: result},
		Required:          true,
		HTTPMethod:        method,
		RestPath:          route,
		IdentityOperation: name,
		Origin:            ir.OriginIdentity,
	}
}

// input gives op its request body, the input type named typeName.
func (e *routeExpansion) input(op *ir.FieldDef, typeName string) {
	e.use(typeName)
	op.Arguments = append(op.Arguments, &ir.ArgumentDef{Name: ir.IdentityInputArgument, TypeRef: ir.TypeRef{Name: typeName}, Required: true})
}

// pathArgument gives op the {name} of its route, of the type typeName.
func (e *routeExpansion) pathArgument(op *ir.FieldDef, name, typeName string) {
	e.useScalar(typeName)
	op.Arguments = append(op.Arguments, &ir.ArgumentDef{Name: name, TypeRef: ir.TypeRef{Name: typeName}, Required: true})
}

// permission makes each op need the prefixed permission.
func (e *routeExpansion) permission(permission string, ops ...*ir.FieldDef) {
	for _, op := range ops {
		op.Permissions = []string{ir.IdentityPermission(e.prefix, permission)}
	}
}

func public(op *ir.FieldDef) { op.Public = true }

func rateLimited(op *ir.FieldDef, perMinute int) {
	op.Middleware = &ir.MiddlewareConfig{RateLimit: &perMinute}
}

// hiddenTool keeps an operation that carries a password out of the
// published tools.
func hiddenTool(op *ir.FieldDef) {
	op.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: ir.IdentityPasswordToolReason}
}

// use adds the type or enum named name, and every one its fields use, once.
// A primitive or a scalar is recorded as a scalar the types use.
func (e *routeExpansion) use(name string) {
	if e.added[name] {
		return
	}
	if name == ir.IdentitySessionTransportEnum {
		e.added[name] = true
		e.api.Enums[name] = &ir.EnumDef{
			Name:    name,
			Owner:   e.api.Name,
			Comment: "How a session travels: bearer answers its token for the Authorization header; cookie sets the session cookie and answers no token.",
			Values: []ir.EnumValueDef{
				{Name: "Bearer", SerializedAs: ir.IdentitySessionBearer},
				{Name: "Cookie", SerializedAs: ir.IdentitySessionCookie},
			},
			Origin: ir.OriginIdentity,
		}
		return
	}
	td := e.typeDef(name)
	if td == nil {
		e.useScalar(name)
		return
	}
	e.added[name] = true
	td.Owner = e.api.Name
	td.Origin = ir.OriginIdentity
	for _, fd := range td.Fields {
		fd.Origin = ir.OriginIdentity
		e.use(fd.TypeRef.Name)
	}
	e.api.Types[name] = td
}

// useScalar records a scalar the added types use; a primitive is none.
func (e *routeExpansion) useScalar(name string) {
	switch name {
	case "string", "number", "boolean":
		return
	}
	for _, seen := range e.scalars {
		if seen == name {
			return
		}
	}
	e.scalars = append(e.scalars, name)
}

// finish adds each scalar the types use that api does not hold, copied
// from the authDb when it declares it, and returns their names.
func (e *routeExpansion) finish() []string {
	for _, name := range e.scalars {
		if e.api.Scalars[name] != nil {
			continue
		}
		def := &ir.ScalarDef{Name: name}
		if declared := e.db.Scalars[name]; declared != nil {
			def = cloneScalar(declared)
		}
		e.api.Scalars[name] = def
	}
	return e.scalars
}

// typeDef returns the definition of the type named name, or nil when it
// names none of the expansion's.
func (e *routeExpansion) typeDef(name string) *ir.TypeDef {
	login := func() *ir.FieldDef {
		return field("login", e.login, true, "The user's login, as their User row holds it.")
	}
	password := func(name, comment string) *ir.FieldDef {
		fd := field(name, passwordScalar, true, comment)
		fd.Secret = true
		return fd
	}
	session := func() *ir.FieldDef {
		return field("session", ir.IdentitySessionTransportEnum, false, "How the session travels; bearer when absent.")
	}
	inputName := func() []*ir.FieldDef {
		if !e.ownName {
			return nil
		}
		return []*ir.FieldDef{field("name", e.name, false, "The user's display name; the login when absent.")}
	}
	roleRefs := func(comment string) *ir.FieldDef {
		fd := field("roles", ir.IdentityRoleRefType, true, comment)
		fd.TypeRef.IsArray = true
		return fd
	}
	permissions := func(comment string) *ir.FieldDef {
		fd := field("permissions", "string", true, comment)
		fd.TypeRef.IsArray = true
		return fd
	}
	id := func(typeName string) *ir.FieldDef {
		return field("id", typeName, true, "")
	}

	var td *ir.TypeDef
	switch name {
	case ir.IdentityLoginInputType:
		td = input(name, "What login takes: the user's login and password, and how the session travels.",
			login(), password("password", "The user's password."), session())
	case ir.IdentityRegisterInputType:
		fields := append([]*ir.FieldDef{login()}, inputName()...)
		fields = append(fields, password("password", "The new user's password."), session())
		td = input(name, "What register takes: the new user's login, name and password, and how the session travels.", fields...)
	case ir.IdentityLoginResultType:
		token := field("token", "string", false, "The session's bearer token; absent for a cookie session.")
		token.Secret = true
		td = output(name, "A session login or register started.",
			field("user", ir.IdentitySessionUserType, true, "The signed-in user."),
			field("expiresAt", "Temporal.DateTime", true, "When the session ends."),
			token)
	case ir.IdentitySessionUserType:
		td = output(name, "A signed-in user: their key, login and display name.",
			id(e.userKey), login(), field("name", e.name, e.nameRequired, "The user's display name."))
	case ir.IdentityRoleRefType:
		td = output(name, "A role a user holds.",
			id(e.roleKey), field("name", e.roleName, true, ""))
	case ir.IdentityCurrentUserType:
		td = output(name, "The caller: their user, the roles they hold and the permissions those roles grant.",
			field("user", ir.IdentitySessionUserType, true, ""),
			roleRefs("The roles the user holds."),
			permissions("The permissions the roles grant, each once."))
	case ir.IdentityCapabilitiesType:
		operations := field("operations", "boolean", true, "Whether each operation's route admits the caller, keyed by its OpenAPI operation id.")
		operations.TypeRef.IsMap = true
		td = output(name, "What the caller may call.", operations)
	case ir.IdentityChangePasswordInputType:
		td = input(name, "What changePassword takes: the caller's current password and the new one.",
			password("current", "The caller's current password."), password("password", "The new password."))
	case ir.IdentityCreateUserInputType:
		fields := append([]*ir.FieldDef{login()}, inputName()...)
		fields = append(fields, password("password", "The new user's password."))
		td = input(name, "What createUser takes: the new user's login, name and password.", fields...)
	case ir.IdentitySetPasswordInputType:
		td = input(name, "What setUserPassword takes: the user's new password.", password("password", "The new password."))
	case ir.IdentityUserType:
		td = output(name, "A user as the administration routes show it.",
			id(e.userKey), login(), field("name", e.name, e.nameRequired, "The user's display name."),
			field("disabled", "boolean", true, "Whether the user is disabled, and so cannot sign in."),
			roleRefs("The roles the user holds."))
	case ir.IdentityRoleInputType:
		td = input(name, "What createRole and updateRole take: the role's name and the permissions it grants.",
			field("name", e.roleName, true, ""), permissions("The permissions the role grants."))
	case ir.IdentityRoleType:
		td = output(name, "A role and the permissions it grants.",
			id(e.roleKey), field("name", e.roleName, true, ""), permissions("The permissions the role grants."))
	}
	return td
}

func input(name, comment string, fields ...*ir.FieldDef) *ir.TypeDef {
	return &ir.TypeDef{Name: name, Comment: comment, Role: ir.RoleAPIInput, Fields: fields}
}

func output(name, comment string, fields ...*ir.FieldDef) *ir.TypeDef {
	return &ir.TypeDef{Name: name, Comment: comment, Role: ir.RoleEmbeddedStruct, Fields: fields}
}

func field(name, typeName string, required bool, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: required}
}

// keyOf returns the type of td's key.
func keyOf(td *ir.TypeDef) string {
	for _, fd := range td.Fields {
		if fd.Key {
			return fd.TypeRef.Name
		}
	}
	return "string"
}

func fieldOf(td *ir.TypeDef, name string) *ir.FieldDef {
	for _, fd := range td.Fields {
		if fd.Name == name {
			return fd
		}
	}
	return &ir.FieldDef{Name: name, TypeRef: ir.TypeRef{Name: "string"}, Required: true}
}

// cloneScalar copies def, whose maps and lists the API's hydration may
// write, so the authDb schema, which a build caches and shares, keeps its
// own.
func cloneScalar(def *ir.ScalarDef) *ir.ScalarDef {
	copied := *def
	if def.TypeMappings != nil {
		copied.TypeMappings = make(map[string]string, len(def.TypeMappings))
		for k, v := range def.TypeMappings {
			copied.TypeMappings[k] = v
		}
	}
	if def.ReservedWords != nil {
		copied.ReservedWords = append([]string(nil), def.ReservedWords...)
	}
	return &copied
}
