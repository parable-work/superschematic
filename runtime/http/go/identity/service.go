package identity

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
	"unicode/utf8"

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

// The bounds of createApiKey's input (D114): the most characters a key's
// name holds, and the most permissions a key names.
const (
	APIKeyNameMaxLength  = 100
	APIKeyMaxPermissions = 100
)

// Service is the user model's logic over a Store, a Config and a clock:
// signing users in and out, resolving a request's session or API key to a
// principal, and managing users, roles, grants and API keys. Its methods return
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

	registration Registration

	apiKeys, apiKeyAdministration bool
}

// Registration is how Register admits a user (D81), as the API's
// @userSessions({ registration, defaultRoles, firstUser }) declares it.
type Registration struct {
	// Approval creates the user pending: Register starts no session, and
	// Login refuses them with 403 pending_approval until ApproveUser.
	Approval bool
	// DefaultRoles are the roles each new user is granted, by name, each
	// created with no permissions when no role has the name. A pending
	// user holds them from registration, and they take effect at approval.
	DefaultRoles []string
	// FirstUser, when set, makes the registration that finds no role grant
	// the first administrator: active even under Approval, and holding the
	// role beside DefaultRoles.
	FirstUser *FirstUser
}

// Option configures a Service.
type Option func(*Service)

// WithClock sets the clock sessions are created, expired and touched by.
func WithClock(now func() time.Time) Option {
	return func(s *Service) { s.now = now }
}

// WithRandom sets where the random bytes of session tokens and API keys
// come from (crypto/rand by default), for tests.
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

// WithRegistration sets how Register admits users (D81).
func WithRegistration(r Registration) Option {
	return func(s *Service) {
		s.registration = Registration{Approval: r.Approval, DefaultRoles: append([]string(nil), r.DefaultRoles...)}
		if r.FirstUser != nil {
			s.registration.FirstUser = &FirstUser{Role: r.FirstUser.Role, Permissions: append([]string(nil), r.FirstUser.Permissions...)}
		}
	}
}

// WithAPIKeys turns on the session set's API key operations (D114), as the
// API's @userSessions({ apiKeys: true }) declares them: createApiKey,
// listApiKeys and revokeApiKey, which Routes leaves out without it. A
// request authenticates with an API key whether they are on or not,
// whenever the store has the table.
func WithAPIKeys() Option {
	return func(s *Service) { s.apiKeys = true }
}

// WithAPIKeyAdministration turns on the administration set's API key
// operations (D114), as the API's @userAdministration({ apiKeys: true })
// declares them: listUserApiKeys and revokeUserApiKey, which Routes leaves
// out without it.
func WithAPIKeyAdministration() Option {
	return func(s *Service) { s.apiKeyAdministration = true }
}

// WithRoutes sets the route requirements of every operation of the API,
// which capabilities answers for. A server lists the API key operations
// among them only when its API declares them.
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
	if err := s.registration.check(); err != nil {
		return nil, err
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

// check refuses a registration whose roles a store could not write: an
// empty role name, or a first administrator without a role or with no
// permission or one of the wrong form.
func (r Registration) check() error {
	for _, role := range r.DefaultRoles {
		if role == "" {
			return errors.New("identity: a default role has no name")
		}
	}
	if f := r.FirstUser; f != nil {
		if f.Role == "" || len(f.Permissions) == 0 {
			return errors.New("identity: the first administrator needs a role and at least one permission")
		}
		for _, p := range f.Permissions {
			if !ValidPermission(p) {
				return fmt.Errorf("identity: the first administrator's permission %q is not dotted segments of letters, digits, '_' and '-'", p)
			}
		}
	}
	return nil
}

// Registration is how the service's Register admits users.
func (s *Service) Registration() Registration { return s.registration }

// APIKeys reports whether the session set's API key operations are turned
// on (WithAPIKeys).
func (s *Service) APIKeys() bool { return s.apiKeys }

// APIKeyAdministration reports whether the administration set's API key
// operations are turned on (WithAPIKeyAdministration).
func (s *Service) APIKeyAdministration() bool { return s.apiKeyAdministration }

// Config is the config the service runs with, its defaults filled in.
func (s *Service) Config() Config { return s.cfg }

// Permission is the full name of an administration permission
// (PermissionUsersRead, ...) under the service's prefix.
func (s *Service) Permission(name string) string { return s.prefix + "." + name }

// Principal is an authenticated caller: the user and their roles, and the
// session or the API key the request carried.
type Principal struct {
	ID    string
	Login string
	Name  string
	// Roles are the user's roles, by name, as session.Role. An API key
	// made with permissions holds them narrowed to those (NarrowRoles).
	Roles []session.Role
	// Permissions are the roles' own, without repeats (EffectivePermissions).
	Permissions []string
	// SessionID is the session's id, and empty for an API key's principal.
	SessionID string
	// APIKeyID is the API key's id (D114), and empty for a session's
	// principal.
	APIKeyID  string
	Transport Transport
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
	// RegisterResult is register's answer under approval (D81): pending
	// and no session, or a session as LoginResult has one.
	RegisterResult struct {
		Pending   bool        `json:"pending"`
		User      SessionUser `json:"user"`
		ExpiresAt *time.Time  `json:"expiresAt,omitempty"`
		Token     *string     `json:"token,omitempty"`
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
		Pending  bool      `json:"pending"`
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
	// CreateAPIKeyInput is the contract's CreateApiKeyInput (D114).
	CreateAPIKeyInput struct {
		Name string `json:"name"`
		// ExpiresInSeconds is how long the key lasts. Absent means the
		// config's apiKeyMaxTtlSeconds, which also bounds it.
		ExpiresInSeconds *int64 `json:"expiresInSeconds,omitempty"`
		// Permissions are the permissions the key names. Absent, or null,
		// means every permission its user holds.
		Permissions []string `json:"permissions,omitempty"`
	}
	// IdentityAPIKey is the contract's ApiKey (D114): an API key as the
	// lists show one, never its token or its hash.
	IdentityAPIKey struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Permissions is absent, never null, for a key that holds every
		// permission its user does: nil is left out, and a list is written,
		// an empty one included.
		Permissions []string  `json:"permissions,omitzero"`
		CreatedAt   time.Time `json:"createdAt"`
		ExpiresAt   time.Time `json:"expiresAt"`
		// LastUsedAt is absent until the key's first request.
		LastUsedAt *time.Time `json:"lastUsedAt,omitempty"`
	}
	// CreatedAPIKey is the contract's CreatedApiKey (D114): createApiKey's
	// answer, the one time the key's token is shown.
	CreatedAPIKey struct {
		ID   string `json:"id"`
		Name string `json:"name"`
		// Permissions is absent as IdentityAPIKey's is.
		Permissions []string  `json:"permissions,omitzero"`
		CreatedAt   time.Time `json:"createdAt"`
		ExpiresAt   time.Time `json:"expiresAt"`
		Token       string    `json:"token"`
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

// Registered is what Register did: started a session (Issued), or, for a
// user who waits for approval (Pending), none (D81).
type Registered struct {
	User    SessionUser
	Pending bool
	Issued  *IssuedSession
}

// RegisterResult is r as register answers it under approval.
func (r Registered) RegisterResult() RegisterResult {
	out := RegisterResult{Pending: r.Pending, User: r.User}
	if r.Issued != nil {
		expires := r.Issued.Result.ExpiresAt
		out.ExpiresAt, out.Token = &expires, r.Issued.Result.Token
	}
	return out
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
// answer nor its timing tells which. A user whose password verifies and
// who waits for approval is 403 pending_approval (D81). A hash at an older
// cost is written again at the config's.
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
	if rec.User.Pending {
		return IssuedSession{}, pendingApproval()
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

// Register creates a user with a password, with the registration's
// default roles, and signs them in, as Login does. Under approval the user
// waits for an administrator and no session starts, unless they are the
// first administrator (D81). A taken login is 409 conflict.
func (s *Service) Register(ctx context.Context, in RegisterInput) (Registered, error) {
	var f fieldErrors
	transport := transportOf(in.Session, &f)
	if in.Login == "" {
		f.add("login", "required", "login is required")
	}
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return Registered{}, err
	}
	reg := s.registration
	user, err := s.createUser(ctx, NewUser{Login: in.Login, Name: in.Name, Pending: reg.Approval, Roles: reg.DefaultRoles, First: reg.FirstUser}, in.Password)
	if err != nil {
		return Registered{}, err
	}
	out := Registered{User: SessionUser{ID: user.ID, Login: user.Login, Name: user.Name}, Pending: user.Pending}
	if user.Pending {
		return out, nil
	}
	issued, err := s.issue(ctx, user, transport)
	if err != nil {
		return Registered{}, err
	}
	out.Issued = &issued
	return out, nil
}

// createUser hashes password and creates nu with the hash.
func (s *Service) createUser(ctx context.Context, nu NewUser, password string) (User, error) {
	hash, err := HashPassword(password, s.params)
	if err != nil {
		return User{}, err
	}
	nu.PasswordHash, nu.At = hash, s.now()
	user, err := s.store.CreateUser(ctx, nu)
	var invalidLogin *InvalidLoginError
	var invalidName *InvalidNameError
	switch {
	case errors.As(err, &invalidLogin):
		var f fieldErrors
		f.add("login", "parse", invalidLogin.Err.Error())
		return User{}, f.err()
	case errors.As(err, &invalidName):
		var f fieldErrors
		f.add("name", "parse", invalidName.Err.Error())
		return User{}, f.err()
	case errors.Is(err, ErrLoginTaken):
		return User{}, conflict("The login is taken")
	case errors.Is(err, ErrNoRoles):
		return User{}, storeError(err, "Role")
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

// Authenticate resolves a request's session or API key to its principal.
// The credential is the Authorization bearer token, a session's or an API
// key, or the session cookie without an Authorization header
// (ExtractCredential). A cookie request with a method other than GET, HEAD
// or OPTIONS passes the cross-origin check first (403 cross_origin). Then
// the session must exist, be neither revoked, expired nor idle, and belong
// to a user who is not disabled; every other outcome is 401 unauthorized.
// The session's lastSeenAt is written when it is older than the touch
// interval. An API key is held to the same rules but the idle one, and
// its lastUsedAt is written as lastSeenAt is (D114).
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
		return Principal{}, cred.Transport, unauthenticated(fmt.Errorf("the %s credential is not a token the transport carries", cred.Transport))
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
// Authenticate does once it has read the token from a request. A token
// that is an API key is resolved as one with the bearer transport, the
// only one that carries it (D114).
func (s *Service) AuthenticateToken(ctx context.Context, token string, transport Transport) (Principal, error) {
	if transport == TransportBearer && isAPIKey(token) {
		return s.authenticateAPIKey(ctx, token)
	}
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
	case rec.User.Pending:
		return Principal{}, unauthenticated(errors.New("the user waits for approval"))
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

// authenticateAPIKey resolves an API key to its principal (D114). In this
// order, the key must exist, be neither revoked nor expired, and belong to
// a user who is neither disabled nor pending; every other outcome is the
// 401 unauthorized a session's is. No idle timeout ends a key. Its
// lastUsedAt is written when it is null or older than the touch interval,
// as a session's lastSeenAt is. A key made with permissions holds its
// user's roles narrowed to them (NarrowRoles); one made with none holds
// them whole, as a session does.
func (s *Service) authenticateAPIKey(ctx context.Context, token string) (Principal, error) {
	rec, err := s.store.FindAPIKey(ctx, HashToken(token))
	if errors.Is(err, ErrNotFound) {
		return Principal{}, unauthenticated(errors.New("no API key has the token"))
	}
	if err != nil {
		return Principal{}, err
	}
	now := s.now().UTC()
	key := rec.APIKey
	switch {
	case key.RevokedAt != nil:
		return Principal{}, unauthenticated(errors.New("the API key is revoked"))
	case !now.Before(key.ExpiresAt):
		return Principal{}, unauthenticated(errors.New("the API key is expired"))
	case rec.User.Disabled:
		return Principal{}, unauthenticated(errors.New("the user is disabled"))
	case rec.User.Pending:
		return Principal{}, unauthenticated(errors.New("the user waits for approval"))
	}
	if interval := s.cfg.TouchInterval(); key.LastUsedAt == nil || now.Sub(*key.LastUsedAt) >= interval {
		if err := s.store.TouchAPIKey(ctx, key.ID, now, now.Add(-interval)); err != nil {
			return Principal{}, err
		}
	}
	roles, err := s.store.UserRoles(ctx, rec.User.ID)
	if err != nil {
		return Principal{}, err
	}
	sessionRoles := toSessionRoles(roles)
	if key.Permissions != nil {
		sessionRoles = NarrowRoles(sessionRoles, key.Permissions)
	}
	return Principal{
		ID: rec.User.ID, Login: rec.User.Login, Name: rec.User.Name,
		Roles: sessionRoles, Permissions: EffectivePermissions(sessionRoles),
		APIKeyID: key.ID, Transport: TransportBearer,
	}, nil
}

func toSessionRoles(roles []Role) []session.Role {
	out := make([]session.Role, len(roles))
	for i, r := range roles {
		out[i] = session.Role{ID: r.ID, Name: r.Name, Permissions: r.Permissions}
	}
	return out
}

// sessionOnly refuses a caller an API key authenticated, with 403
// forbidden (D114). A key does not act on its own account: it neither
// makes, lists nor revokes keys, signs out nor changes the password, so a
// leaked one does none of them.
func sessionOnly(p Principal) error {
	if p.APIKeyID != "" {
		return forbidden(sessionOnlyMessage, nil)
	}
	return nil
}

// Logout revokes the caller's session. An API key's principal has none to
// revoke and is 403 forbidden.
func (s *Service) Logout(ctx context.Context, p Principal) error {
	if err := sessionOnly(p); err != nil {
		return err
	}
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
// The user's API keys live on. An API key's principal is 403 forbidden.
func (s *Service) ChangePassword(ctx context.Context, p Principal, in ChangePasswordInput) error {
	if err := sessionOnly(p); err != nil {
		return err
	}
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

// storeError turns a store's not-found, no-roles and no-API-keys errors
// into problems.
func storeError(err error, what string) error {
	switch {
	case errors.Is(err, ErrNotFound):
		return notFound(what)
	case errors.Is(err, ErrNoRoles):
		return problem(apperror.ErrCodeNotFound, CodeNotFound, "The schema has no roles", err)
	case errors.Is(err, ErrNoAPIKeys):
		return noAPIKeys()
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
	return IdentityUser{ID: u.ID, Login: u.Login, Name: u.Name, Disabled: u.Disabled, Pending: u.Pending, Roles: roles}
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
	user, err := s.createUser(ctx, NewUser{Login: in.Login, Name: in.Name}, in.Password)
	if err != nil {
		return IdentityUser{}, err
	}
	return identityUser(user), nil
}

// ListUsers lists the users, by login (users.read): every one when pending
// is nil, else those who wait for approval when it is true and the others
// when it is false (D81).
func (s *Service) ListUsers(ctx context.Context, p Principal, pending *bool) ([]IdentityUser, error) {
	if err := s.require(p, PermissionUsersRead); err != nil {
		return nil, err
	}
	users, err := s.store.ListUsers(ctx)
	if err != nil {
		return nil, err
	}
	out := make([]IdentityUser, 0, len(users))
	for _, u := range users {
		if pending == nil || u.Pending == *pending {
			out = append(out, identityUser(u))
		}
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

// DisableUser disables a user and revokes their sessions and their API
// keys (users.write). The caller's permissions must cover each of the
// user's.
func (s *Service) DisableUser(ctx context.Context, p Principal, id string) (IdentityUser, error) {
	return s.setDisabled(ctx, p, id, true)
}

// EnableUser enables a disabled user (users.write), which brings back
// neither a session nor an API key the disable revoked. The caller's
// permissions must cover each of the user's.
func (s *Service) EnableUser(ctx context.Context, p Principal, id string) (IdentityUser, error) {
	return s.setDisabled(ctx, p, id, false)
}

func (s *Service) setDisabled(ctx context.Context, p Principal, id string, disabled bool) (IdentityUser, error) {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return IdentityUser{}, err
	}
	if err := s.covers(ctx, p, id); err != nil {
		return IdentityUser{}, err
	}
	if err := s.store.SetDisabled(ctx, id, disabled, s.now()); err != nil {
		return IdentityUser{}, storeError(err, "User")
	}
	return s.getUser(ctx, id)
}

// ApproveUser approves a user who waits for approval, who can then sign in
// (users.write, D81). The caller's permissions must cover each of the
// user's, the roles register granted them included; a user who is not
// pending is 409 not_pending.
func (s *Service) ApproveUser(ctx context.Context, p Principal, id string) (IdentityUser, error) {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return IdentityUser{}, err
	}
	if err := s.covers(ctx, p, id); err != nil {
		return IdentityUser{}, err
	}
	if err := s.store.ApproveUser(ctx, id); err != nil {
		if errors.Is(err, ErrNotPending) {
			return IdentityUser{}, notPending()
		}
		return IdentityUser{}, storeError(err, "User")
	}
	return s.getUser(ctx, id)
}

// SetUserPassword sets a user's password and revokes their sessions
// (users.write). The user's API keys live on. The caller's permissions
// must cover each of the user's.
func (s *Service) SetUserPassword(ctx context.Context, p Principal, id string, in SetPasswordInput) error {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return err
	}
	var f fieldErrors
	checkPasswordField(&f, "password", in.Password)
	if err := f.err(); err != nil {
		return err
	}
	if err := s.covers(ctx, p, id); err != nil {
		return err
	}
	hash, err := HashPassword(in.Password, s.params)
	if err != nil {
		return err
	}
	return storeError(s.store.SetPassword(ctx, id, hash, s.now(), ""), "User")
}

// covers refuses a write to the user with id unless the caller's
// permissions cover every permission the user holds through their roles
// (UncoveredUser): 404 for no such user, 403 listing the ones not
// covered. Whoever sets a user's password signs in as them, and whoever
// enables or disables them gives or takes their permissions, so no one
// does either to a user who holds what they do not.
func (s *Service) covers(ctx context.Context, p Principal, id string) error {
	user, err := s.store.GetUser(ctx, id)
	if err != nil {
		return storeError(err, "User")
	}
	if uncovered := UncoveredUser(p.Permissions, toSessionRoles(user.Roles)); len(uncovered) > 0 {
		return forbidden(notCoveredMessage, map[string]any{"permissions": uncovered})
	}
	return nil
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

// ownAPIKeys admits the caller of an API key operation of the session set
// (D114): an authenticated one (401) signed in with a session, not an API
// key (403), over a store that has API keys (404).
func (s *Service) ownAPIKeys(p Principal) error {
	if p.ID == "" {
		return unauthenticated(errors.New("the caller is not authenticated"))
	}
	if err := sessionOnly(p); err != nil {
		return err
	}
	return s.haveAPIKeys()
}

// haveAPIKeys is the 404 of an API key operation over a store whose
// descriptor names no API key table, and nil over one that has it.
func (s *Service) haveAPIKeys() error {
	if !s.store.HasAPIKeys() {
		return noAPIKeys()
	}
	return nil
}

func identityAPIKey(k APIKey) IdentityAPIKey {
	return IdentityAPIKey{ID: k.ID, Name: k.Name, Permissions: k.Permissions, CreatedAt: k.CreatedAt, ExpiresAt: k.ExpiresAt, LastUsedAt: k.LastUsedAt}
}

// CreateAPIKey makes the caller an API key (D114) and answers its token,
// this once. The caller is signed in with a session. The name, trimmed of
// surrounding white space, is 1 to APIKeyNameMaxLength characters;
// expiresInSeconds, when present, is 1 to the config's apiKeyMaxTtlSeconds,
// which is also the lifetime without it; permissions, when present, are 1
// to APIKeyMaxPermissions of them (400). Each has a permission's form and
// is named once (422), and the caller holds a permission covering it (403,
// a key names only what its user holds).
func (s *Service) CreateAPIKey(ctx context.Context, p Principal, in CreateAPIKeyInput) (CreatedAPIKey, error) {
	if err := s.ownAPIKeys(p); err != nil {
		return CreatedAPIKey{}, err
	}
	var f fieldErrors
	name := strings.TrimSpace(in.Name)
	switch n := utf8.RuneCountInString(name); {
	case n == 0:
		f.add("name", "required", "name is required")
	case n > APIKeyNameMaxLength:
		f.add("name", "maxLength", fmt.Sprintf("name must be at most %d characters", APIKeyNameMaxLength))
	}
	seconds := s.cfg.apiKeyMaxTTLSeconds()
	if in.ExpiresInSeconds != nil {
		switch given := *in.ExpiresInSeconds; {
		case given < 1:
			f.add("expiresInSeconds", "min", "expiresInSeconds must be at least 1")
		case given > seconds:
			f.add("expiresInSeconds", "max", fmt.Sprintf("expiresInSeconds must be at most %d", seconds))
		default:
			seconds = given
		}
	}
	if in.Permissions != nil {
		switch n := len(in.Permissions); {
		case n == 0:
			f.add("permissions", "listMin", "permissions must contain at least 1 items")
		case n > APIKeyMaxPermissions:
			f.add("permissions", "listMax", fmt.Sprintf("permissions must contain at most %d items", APIKeyMaxPermissions))
		}
	}
	if err := f.err(); err != nil {
		return CreatedAPIKey{}, err
	}
	// A permission that has not a permission's form, and each one that
	// repeats an earlier one, in the order given.
	var refused []string
	seen := map[string]bool{}
	for _, perm := range in.Permissions {
		if !ValidPermission(perm) || seen[perm] {
			refused = append(refused, perm)
		}
		seen[perm] = true
	}
	if len(refused) > 0 {
		return CreatedAPIKey{}, invalidPermissions(refused)
	}
	if uncovered := Uncovered(p.Permissions, in.Permissions); len(uncovered) > 0 {
		return CreatedAPIKey{}, forbidden(apiKeyNotHeldMessage, map[string]any{"permissions": uncovered})
	}
	token, err := NewAPIKey(s.random)
	if err != nil {
		return CreatedAPIKey{}, err
	}
	// To the millisecond, as a session's times are.
	now := s.now().UTC().Truncate(time.Millisecond)
	key, err := s.store.CreateAPIKey(ctx, NewAPIKeyRow{
		UserID: p.ID, Name: name, TokenHash: HashToken(token), Permissions: in.Permissions,
		CreatedAt: now, ExpiresAt: now.Add(time.Duration(seconds) * time.Second),
	})
	if errors.Is(err, ErrNotFound) {
		return CreatedAPIKey{}, unauthenticated(err)
	}
	if err != nil {
		return CreatedAPIKey{}, storeError(err, "User")
	}
	return CreatedAPIKey{ID: key.ID, Name: key.Name, Permissions: key.Permissions, CreatedAt: key.CreatedAt, ExpiresAt: key.ExpiresAt, Token: token}, nil
}

// ListAPIKeys lists the caller's API keys that are not revoked, expired
// ones included, newest first (D114). The caller is signed in with a
// session.
func (s *Service) ListAPIKeys(ctx context.Context, p Principal) ([]IdentityAPIKey, error) {
	if err := s.ownAPIKeys(p); err != nil {
		return nil, err
	}
	return s.listAPIKeys(ctx, p.ID)
}

func (s *Service) listAPIKeys(ctx context.Context, userID string) ([]IdentityAPIKey, error) {
	keys, err := s.store.ListAPIKeys(ctx, userID)
	if err != nil {
		return nil, storeError(err, "User")
	}
	out := make([]IdentityAPIKey, len(keys))
	for i, k := range keys {
		out[i] = identityAPIKey(k)
	}
	return out, nil
}

// RevokeAPIKey revokes the caller's API key with keyID (D114). A key that
// does not exist, is another user's or is revoked already is 404. The
// caller is signed in with a session.
func (s *Service) RevokeAPIKey(ctx context.Context, p Principal, keyID string) error {
	if err := s.ownAPIKeys(p); err != nil {
		return err
	}
	return storeError(s.store.RevokeAPIKey(ctx, p.ID, keyID, s.now()), "API key")
}

// ListUserAPIKeys lists a user's API keys as ListAPIKeys lists the
// caller's (users.read, D114). A user that does not exist is 404.
func (s *Service) ListUserAPIKeys(ctx context.Context, p Principal, userID string) ([]IdentityAPIKey, error) {
	if err := s.require(p, PermissionUsersRead); err != nil {
		return nil, err
	}
	if err := s.haveAPIKeys(); err != nil {
		return nil, err
	}
	if _, err := s.store.GetUser(ctx, userID); err != nil {
		return nil, storeError(err, "User")
	}
	return s.listAPIKeys(ctx, userID)
}

// RevokeUserAPIKey revokes a user's API key with keyID (users.write,
// D114). A key that does not exist, is another user's or is revoked
// already is 404.
func (s *Service) RevokeUserAPIKey(ctx context.Context, p Principal, userID, keyID string) error {
	if err := s.require(p, PermissionUsersWrite); err != nil {
		return err
	}
	if err := s.haveAPIKeys(); err != nil {
		return err
	}
	return storeError(s.store.RevokeAPIKey(ctx, userID, keyID, s.now()), "API key")
}
