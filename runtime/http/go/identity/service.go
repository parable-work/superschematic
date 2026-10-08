package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
	"github.com/parable-work/superschematic/runtime/http/go/session"
)

// The administration routes' permissions, under the naming key
// identity_permission_prefix ("identity" by default): the prefix, a dot,
// and one of these.
const (
	PermissionUsersRead  = "users.read"
	PermissionUsersWrite = "users.write"
	PermissionRolesRead  = "roles.read"
	PermissionRolesWrite = "roles.write"
)

// DefaultPermissionPrefix is identity_permission_prefix's default.
const DefaultPermissionPrefix = "identity"

// Service is the user model's logic over a Store, a Config and a clock:
// signing users in and out, resolving a request's session to a principal,
// and managing users, roles and grants. Its methods return
// *apperror.AppError problems (and validation errors for a refused input),
// which WriteError renders.
type Service struct {
	store   Store
	cfg     Config
	now     func() time.Time
	random  io.Reader
	matcher session.Matcher
	prefix  string
	routes  []Route
	cop     *http.CrossOriginProtection
	params  Argon2Params
	dummy   string
}

// Option configures a Service.
type Option func(*Service)

// WithClock sets the clock sessions are created, expired and touched by.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithRandom sets where session tokens' random bytes come from
// (crypto/rand by default), for tests.
func WithRandom(r io.Reader) Option {
	return func(s *Service) { s.random = r }
}

// WithMatcher replaces the permission matcher capabilities and the
// administration routes admit callers by (session.AnyRoleCoversAny),
// for a project whose server routes run another.
func WithMatcher(m session.Matcher) Option {
	return func(s *Service) { s.matcher = m }
}

// WithPermissionPrefix sets identity_permission_prefix, the prefix of the
// administration routes' permissions.
func WithPermissionPrefix(prefix string) Option {
	return func(s *Service) { s.prefix = prefix }
}

// WithRoutes sets the route requirements of every operation of the API,
// which capabilities answers for.
func WithRoutes(routes []Route) Option {
	return func(s *Service) { s.routes = append([]Route(nil), routes...) }
}

// New builds a Service. It validates cfg and hashes the dummy password an
// unknown login verifies against, at cfg's cost.
func New(store Store, cfg Config, opts ...Option) (*Service, error) {
	if store == nil {
		return nil, errors.New("identity: New needs a store")
	}
	if err := cfg.Validate(); err != nil {
		return nil, err
	}
	s := &Service{
		store:   store,
		cfg:     cfg.WithDefaults(),
		now:     time.Now,
		matcher: session.AnyRoleCoversAny,
		prefix:  DefaultPermissionPrefix,
	}
	for _, opt := range opts {
		opt(s)
	}
	if !ValidPermission(s.prefix) {
		return nil, fmt.Errorf("identity: the permission prefix %q is not dotted segments of letters, digits, '_' and '-'", s.prefix)
	}
	cop, err := newCrossOriginProtection(s.cfg.TrustedOrigins)
	if err != nil {
		return nil, err
	}
	s.cop = cop
	s.params = s.cfg.Argon2Params()
	if s.dummy, err = dummyHash(s.params); err != nil {
		return nil, err
	}
	return s, nil
}

// Config is the config the service runs with, its defaults filled in.
func (s *Service) Config() Config { return s.cfg }

// Permission is the full name of an administration permission
// (PermissionUsersRead, ...) under the service's prefix.
func (s *Service) Permission(name string) string { return s.prefix + "." + name }

// Principal is an authenticated caller: the user and their roles, and the
// session the request carried.
type Principal struct {
	ID    string
	Login string
	Name  string
	// Roles are the user's roles, by name, as session.Role.
	Roles []session.Role
	// Permissions are the roles' own, without repeats (EffectivePermissions).
	Permissions []string
	SessionID   string
	Transport   Transport
}

// The operations' inputs and results, by the contract's field names
// (ir/identity_routes.go).
type (
	LoginInput struct {
		Login    string    `json:"login"`
		Password string    `json:"password"`
		Session  Transport `json:"session,omitempty"`
	}
	RegisterInput struct {
		Login    string    `json:"login"`
		Name     string    `json:"name,omitempty"`
		Password string    `json:"password"`
		Session  Transport `json:"session,omitempty"`
	}
	LoginResult struct {
		User      SessionUser `json:"user"`
		ExpiresAt time.Time   `json:"expiresAt"`
		// Token is absent for a cookie session, which no script reads.
		Token *string `json:"token,omitempty"`
	}
	SessionUser struct {
		ID    string `json:"id"`
		Login string `json:"login"`
		Name  string `json:"name"`
	}
	RoleRef struct {
		ID   string `json:"id"`
		Name string `json:"name"`
	}
	CurrentUser struct {
		User        SessionUser `json:"user"`
		Roles       []RoleRef   `json:"roles"`
		Permissions []string    `json:"permissions"`
	}
	Capabilities struct {
		Operations map[string]bool `json:"operations"`
	}
	ChangePasswordInput struct {
		Current  string `json:"current"`
		Password string `json:"password"`
	}
	CreateUserInput struct {
		Login    string `json:"login"`
		Name     string `json:"name,omitempty"`
		Password string `json:"password"`
	}
	SetPasswordInput struct {
		Password string `json:"password"`
	}
	IdentityUser struct {
		ID       string    `json:"id"`
		Login    string    `json:"login"`
		Name     string    `json:"name"`
		Disabled bool      `json:"disabled"`
		Roles    []RoleRef `json:"roles"`
	}
	RoleInput struct {
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
	IdentityRole struct {
		ID          string   `json:"id"`
		Name        string   `json:"name"`
		Permissions []string `json:"permissions"`
	}
)

// IssuedSession is a session login or register created: the result the
// route answers, and the token and transport the HTTP layer sets the
// cookie from.
type IssuedSession struct {
	Result    LoginResult
	Token     string
	Transport Transport
	SessionID string
	ExpiresAt time.Time
}

// transportOf is a login's transport: bearer when absent.
func transportOf(t Transport, f *fieldErrors) Transport {
	switch t {
	case "", TransportBearer:
		return TransportBearer
	case TransportCookie:
		return TransportCookie
	default:
		f.add("session", "enum", `session must be "bearer" or "cookie"`)
		return ""
	}
}

func checkPasswordField(f *fieldErrors, field, password string) {
	if password == "" {
		f.add(field, "required", field+" is required")
		return
	}
	if err := CheckPassword(password); err != nil {
		f.add(field, "length", err.Error())
	}
}

// Login signs a user in: it verifies the password and creates a session.
// Every failure, an unknown login, a login the scalar refuses, a user
// without a password and a disabled user included, is 401
// invalid_credentials after a verify of the same cost, so neither the
// answer nor its timing tells which. A hash at an older cost is written
// again at the config's.
func (s *Service) Login(ctx context.Context, in LoginInput) (IssuedSession, error) {
	var f fieldErrors
	transport := transportOf(in.Session, &f)
	if in.Login == "" {
		f.add("login", "required", "login is required")
	}
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return IssuedSession{}, err
	}

	rec, err := s.store.FindLogin(ctx, in.Login)
	var invalidLogin *InvalidLoginError
	switch {
	case errors.Is(err, ErrNotFound) || errors.As(err, &invalidLogin):
		_, _, _ = VerifyPassword(s.dummy, in.Password, s.params)
		return IssuedSession{}, invalidCredentials(err)
	case err != nil:
		return IssuedSession{}, err
	}
	if rec.PasswordHash == "" {
		_, _, _ = VerifyPassword(s.dummy, in.Password, s.params)
		return IssuedSession{}, invalidCredentials(errors.New("the user has no password"))
	}
	ok, rehash, err := VerifyPassword(rec.PasswordHash, in.Password, s.params)
	if err != nil || !ok {
		return IssuedSession{}, invalidCredentials(err)
	}
	if rec.User.Disabled {
		return IssuedSession{}, invalidCredentials(errors.New("the user is disabled"))
	}
	if rehash {
		hash, err := HashPassword(in.Password, s.params)
		if err != nil {
			return IssuedSession{}, err
		}
		if err := s.store.RehashPassword(ctx, rec.User.ID, rec.PasswordHash, hash); err != nil {
			return IssuedSession{}, err
		}
	}
	return s.issue(ctx, rec.User, transport)
}

// Register creates a user with a password and signs them in, as Login
// does. A taken login is 409 conflict.
func (s *Service) Register(ctx context.Context, in RegisterInput) (IssuedSession, error) {
	var f fieldErrors
	transport := transportOf(in.Session, &f)
	if in.Login == "" {
		f.add("login", "required", "login is required")
	}
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return IssuedSession{}, err
	}
	user, err := s.createUser(ctx, in.Login, in.Name, in.Password)
	if err != nil {
		return IssuedSession{}, err
	}
	return s.issue(ctx, user, transport)
}

func (s *Service) createUser(ctx context.Context, login, name, password string) (User, error) {
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return User{}, err
	}
	user, err := s.store.CreateUser(ctx, NewUser{Login: login, Name: name, PasswordHash: hash, At: s.now()})
	var invalidLogin *InvalidLoginError
	switch {
	case errors.As(err, &invalidLogin):
		var f fieldErrors
		f.add("login", "parse", invalidLogin.Err.Error())
		return User{}, f.err()
	case errors.Is(err, ErrLoginTaken):
		return User{}, conflict("The login is taken")
	case err != nil:
		return User{}, err
	}
	return user, nil
}

// issue creates a session for user.
func (s *Service) issue(ctx context.Context, user User, transport Transport) (IssuedSession, error) {
	token, err := NewToken(s.random)
	if err != nil {
		return IssuedSession{}, err
	}
	// To the millisecond, which both dialects keep, so the answer is what
	// the row holds.
	now := s.now().UTC().Truncate(time.Millisecond)
	expires := now.Add(s.cfg.SessionTTL())
	sess, err := s.store.CreateSession(ctx, NewSession{UserID: user.ID, TokenHash: HashToken(token), CreatedAt: now, ExpiresAt: expires})
	if err != nil {
		return IssuedSession{}, err
	}
	result := LoginResult{User: SessionUser{ID: user.ID, Login: user.Login, Name: user.Name}, ExpiresAt: expires}
	if transport == TransportBearer {
		result.Token = &token
	}
	return IssuedSession{Result: result, Token: token, Transport: transport, SessionID: sess.ID, ExpiresAt: expires}, nil
}

// Authenticate resolves a request's session to its principal. The
// credential is the Authorization bearer token, or the session cookie
// without an Authorization header (ExtractCredential). A cookie request
// with a method other than GET, HEAD or OPTIONS passes the cross-origin
// check first (403 cross_origin). Then the session must exist, be neither
// revoked, expired nor idle, and belong to a user who is not disabled;
// every other outcome is 401 unauthorized. The session's lastSeenAt is
// written when it is older than the touch interval.
func (s *Service) Authenticate(r *http.Request) (Principal, error) {
	p, _, err := s.authenticate(r)
	return p, err
}

// authenticate is Authenticate, also reporting the transport the request
// carried, so a refused cookie can be cleared.
func (s *Service) authenticate(r *http.Request) (Principal, Transport, error) {
	cred, outcome := ExtractCredential(r.Header, s.cfg.CookieName())
	switch outcome {
	case NoCredential:
		return Principal{}, "", unauthenticated(errors.New("the request carries no session"))
	case InvalidCredential:
		return Principal{}, cred.Transport, unauthenticated(fmt.Errorf("the %s credential is not a session token", cred.Transport))
	}
	if cred.Transport == TransportCookie {
		if err := s.cop.Check(r); err != nil {
			return Principal{}, cred.Transport, crossOrigin(err)
		}
	}
	p, err := s.AuthenticateToken(r.Context(), cred.Token, cred.Transport)
	return p, cred.Transport, err
}

// AuthenticateToken resolves a session token to its principal, as
// Authenticate does once it has read the token from a request.
func (s *Service) AuthenticateToken(ctx context.Context, token string, transport Transport) (Principal, error) {
	rec, err := s.store.FindSession(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return Principal{}, unauthenticated(errors.New("no session has the token"))
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now().UTC()
	sess := rec.Session
	switch {
	case sess.RevokedAt != nil:
		return Principal{}, unauthenticated(errors.New("the session is revoked"))
	case !now.Before(sess.ExpiresAt):
		return Principal{}, unauthenticated(errors.New("the session is expired"))
	case rec.User.Disabled:
		return Principal{}, unauthenticated(errors.New("the user is disabled"))
	}
	lastSeen := sess.CreatedAt
	if sess.LastSeenAt != nil {
		lastSeen = *sess.LastSeenAt
	}
	if idle := s.cfg.IdleTimeout(); idle > 0 && now.Sub(lastSeen) >= idle {
		return Principal{}, unauthenticated(errors.New("the session is idle"))
	}
	if interval := s.cfg.TouchInterval(); sess.LastSeenAt == nil || now.Sub(*sess.LastSeenAt) >= interval {
		if err := s.store.TouchSession(ctx, sess.ID, now, now.Add(-interval)); err != nil {
			return Principal{}, err
		}
	}
	roles, err := s.store.UserRoles(ctx, rec.User.ID)
	if err != nil {
		return Principal{}, err
	}
	sessionRoles := toSessionRoles(roles)
	return Principal{
		ID: rec.User.ID, Login: rec.User.Login, Name: rec.User.Name,
		Roles: sessionRoles, Permissions: EffectivePermissions(sessionRoles),
		SessionID: sess.ID, Transport: transport,
	}, nil
}

func toSessionRoles(roles []Role) []session.Role {
	out := make([]session.Role, len(roles))
	for i, r := range roles {
		out[i] = session.Role{ID: r.ID, Name: r.Name, Permissions: r.Permissions}
	}
	return out
}

// Logout revokes the caller's session.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	if p.SessionID == "" {
		return unauthenticated(errors.New("the caller has no session"))
	}
	return s.store.RevokeSession(ctx, p.SessionID, s.now())
}

// Me answers the caller's user, roles and permissions.
func (s *Service) Me(p Principal) CurrentUser {
	roles := make([]RoleRef, len(p.Roles))
	for i, r := range p.Roles {
		roles[i] = RoleRef{ID: r.ID, Name: r.Name}
	}
	permissions := p.Permissions
	if permissions == nil {
		permissions = []string{}
	}
	return CurrentUser{User: SessionUser{ID: p.ID, Login: p.Login, Name: p.Name}, Roles: roles, Permissions: permissions}
}

// Capabilities answers, for each operation of the API an end user may
// call, whether its route admits the caller (CapabilitiesOf over the
// routes WithRoutes set).
func (s *Service) Capabilities(p Principal) Capabilities {
	return Capabilities{Operations: CapabilitiesOf(s.routes, p.Roles, s.matcher)}
}

// ChangePassword sets the caller's password once current verifies (401
// invalid_credentials otherwise), and revokes the user's other sessions.
func (s *Service) ChangePassword(ctx context.Context, p Principal, in ChangePasswordInput) error {
	var f fieldErrors
	checkPasswordField(&f, "current", in.Current)
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return err
	}
	rec, err := s.store.FindCredential(ctx, p.ID)
	if errors.Is(err, ErrNotFound) {
		return unauthenticated(err)
	}
	if err != nil {
		return err
	}
	stored := rec.PasswordHash
	if stored == "" {
		stored = s.dummy
	}
	ok, _, err := VerifyPassword(stored, in.Current, s.params)
	if err != nil || !ok || rec.PasswordHash == "" {
		return invalidCredentials(err)
	}
	hash, err := HashPassword(in.Password, s.params)
	if err != nil {
		return err
	}
	return s.store.SetPassword(ctx, p.ID, hash, s.now(), p.SessionID)
}

// require refuses a caller the matcher does not give the administration
// permission name.
func (s *Service) require(p Principal, name string) error {
	if p.ID == "" {
		return unauthenticated(errors.New("the caller is not authenticated"))
	}
	if !s.matcher(p.Roles, []string{s.Permission(name)}) {
		return forbidden("Insufficient permissions", nil)
	}
	return nil
}

// storeError turns a store's not-found and no-roles errors into problems.
func storeError(err error, what string) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return notFound(what)
	case errors.Is(err, ErrNoRoles):
		return problem(apperror.ErrCodeNotFound, CodeNotFound, "The schema has no roles", err)
	case errors.Is(err, ErrRoleNameTaken):
		return conflict("The role name is taken")
	}
	return err
}

func identityUser(u User) IdentityUser {
	roles := make([]RoleRef, len(u.Roles))
	for i, r := range u.Roles {
		roles[i] = RoleRef{ID: r.ID, Name: r.Name}
	}
	return IdentityUser{ID: u.ID, Login: u.Login, Name: u.Name, Disabled: u.Disabled, Roles: roles}
}

func identityRole(r Role) IdentityRole {
	permissions := r.Permissions
	if permissions == nil {
		permissions = []string{}
	}
	return IdentityRole{ID: r.ID, Name: r.Name, Permissions: permissions}
}

// CreateUser creates a user with a password (users.write).
func (s *Service) CreateUser(ctx context.Context, p Principal, in CreateUserInput) (IdentityUser, error) {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return IdentityUser{}, err
	}
	var f fieldErrors
	if in.Login == "" {
		f.add("login", "required", "login is required")
	}
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return IdentityUser{}, err
	}
	user, err := s.createUser(ctx, in.Login, in.Name, in.Password)
	if err != nil {
		return IdentityUser{}, err
	}
	return identityUser(user), nil
}

// ListUsers lists every user, by login (users.read).
func (s *Service) ListUsers(ctx context.Context, p Principal) ([]IdentityUser, error) {
	if err := s.require(p, PermissionUsersRead); err != nil {
		return nil, err
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IdentityUser, len(users))
	for i, u := range users {
		out[i] = identityUser(u)
	}
	return out, nil
}

// GetUser answers one user (users.read).
func (s *Service) GetUser(ctx context.Context, p Principal, id string) (IdentityUser, error) {
	if err := s.require(p, PermissionUsersRead); err != nil {
		return IdentityUser{}, err
	}
	return s.getUser(ctx, id)
}

func (s *Service) getUser(ctx context.Context, id string) (IdentityUser, error) {
	user, err := s.store.GetUser(ctx, id)
	if err != nil {
		return IdentityUser{}, storeError(err, "User")
	}
	return identityUser(user), nil
}

// DisableUser disables a user and revokes their sessions (users.write).
func (s *Service) DisableUser(ctx context.Context, p Principal, id string) (IdentityUser, error) {
	return s.setDisabled(ctx, p, id, true)
}

// EnableUser enables a disabled user (users.write).
func (s *Service) EnableUser(ctx context.Context, p Principal, id string) (IdentityUser, error) {
	return s.setDisabled(ctx, p, id, false)
}

func (s *Service) setDisabled(ctx context.Context, p Principal, id string, disabled bool) (IdentityUser, error) {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return IdentityUser{}, err
	}
	if err := s.store.SetDisabled(ctx, id, disabled, s.now()); err != nil {
		return IdentityUser{}, storeError(err, "User")
	}
	return s.getUser(ctx, id)
}

// SetUserPassword sets a user's password and revokes their sessions
// (users.write).
func (s *Service) SetUserPassword(ctx context.Context, p Principal, id string, in SetPasswordInput) error {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return err
	}
	var f fieldErrors
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return err
	}
	hash, err := HashPassword(in.Password, s.params)
	if err != nil {
		return err
	}
	return storeError(s.store.SetPassword(ctx, id, hash, s.now(), ""), "User")
}

// ListRoles lists every role, by name (roles.read).
func (s *Service) ListRoles(ctx context.Context, p Principal) ([]IdentityRole, error) {
	if err := s.require(p, PermissionRolesRead); err != nil {
		return nil, err
	}
	roles, err := s.store.ListRoles(ctx)
	if err != nil {
		return nil, storeError(err, "Role")
	}
	out := make([]IdentityRole, len(roles))
	for i, r := range roles {
		out[i] = identityRole(r)
	}
	return out, nil
}

// checkRole validates a role's input and returns its permissions without
// repeats: a name is required (400), every permission has a permission's
// form (422), and the caller covers each (403, no one grants what they do
// not hold).
func (s *Service) checkRole(p Principal, in RoleInput) ([]string, error) {
	var f fieldErrors
	if in.Name == "" {
		f.add("name", "required", "name is required")
	}
	if err := f.err(); err != nil {
		return nil, err
	}
	var invalid []string
	permissions := []string{}
	seen := map[string]bool{}
	for _, perm := range in.Permissions {
		if !ValidPermission(perm) {
			invalid = append(invalid, perm)
			continue
		}
		if !seen[perm] {
			seen[perm] = true
			permissions = append(permissions, perm)
		}
	}
	if len(invalid) > 0 {
		return nil, invalidPermissions(invalid)
	}
	if uncovered := Uncovered(p.Permissions, permissions); len(uncovered) > 0 {
		return nil, forbidden("No one grants a permission they do not hold", map[string]any{"permissions": uncovered})
	}
	return permissions, nil
}

// CreateRole creates a role (roles.write). The caller's permissions must
// cover each of the role's.
func (s *Service) CreateRole(ctx context.Context, p Principal, in RoleInput) (IdentityRole, error) {
	if err := s.require(p, PermissionRolesWrite); err != nil {
		return IdentityRole{}, err
	}
	permissions, err := s.checkRole(p, in)
	if err != nil {
		return IdentityRole{}, err
	}
	role, err := s.store.CreateRole(ctx, in.Name, permissions)
	if err != nil {
		return IdentityRole{}, storeError(err, "Role")
	}
	return identityRole(role), nil
}

// UpdateRole rewrites a role's name and permissions (roles.write). The
// caller's permissions must cover each of the new ones.
func (s *Service) UpdateRole(ctx context.Context, p Principal, id string, in RoleInput) (IdentityRole, error) {
	if err := s.require(p, PermissionRolesWrite); err != nil {
		return IdentityRole{}, err
	}
	permissions, err := s.checkRole(p, in)
	if err != nil {
		return IdentityRole{}, err
	}
	role, err := s.store.UpdateRole(ctx, id, in.Name, permissions)
	if err != nil {
		return IdentityRole{}, storeError(err, "Role")
	}
	return identityRole(role), nil
}

// DeleteRole deletes a role and its grants (roles.write).
func (s *Service) DeleteRole(ctx context.Context, p Principal, id string) error {
	if err := s.require(p, PermissionRolesWrite); err != nil {
		return err
	}
	return storeError(s.store.DeleteRole(ctx, id), "Role")
}

// GrantRole grants a user a role (roles.write). The caller's permissions
// must cover each of the role's.
func (s *Service) GrantRole(ctx context.Context, p Principal, userID, roleID string) (IdentityUser, error) {
	if err := s.require(p, PermissionRolesWrite); err != nil {
		return IdentityUser{}, err
	}
	role, err := s.store.GetRole(ctx, roleID)
	if err != nil {
		return IdentityUser{}, storeError(err, "Role")
	}
	if uncovered := Uncovered(p.Permissions, role.Permissions); len(uncovered) > 0 {
		return IdentityUser{}, forbidden("No one grants a permission they do not hold", map[string]any{"permissions": uncovered})
	}
	if err := s.store.GrantRole(ctx, userID, roleID, s.now()); err != nil {
		return IdentityUser{}, storeError(err, "User or role")
	}
	return s.getUser(ctx, userID)
}

// RevokeRole revokes a role from a user (roles.write).
func (s *Service) RevokeRole(ctx context.Context, p Principal, userID, roleID string) (IdentityUser, error) {
	if err := s.require(p, PermissionRolesWrite); err != nil {
		return IdentityUser{}, err
	}
	if err := s.store.RevokeRole(ctx, userID, roleID); err != nil {
		return IdentityUser{}, storeError(err, "User or role")
	}
	return s.getUser(ctx, userID)
}
