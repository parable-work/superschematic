package shop_test

import (
	"context"
	"database/sql"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	shopapi "example.com/acme/api/shop-api"
	shoporders "example.com/acme/api/shop-orders"
	db "example.com/acme/types/go/shop-db"
	scalars "github.com/parable-work/superscalar/go"
	"github.com/parable-work/superschematic/runtime/http/go/identity"
	_ "modernc.org/sqlite"
)

// users are the shop's users as the tests serve them: the identity
// runtime's config, at a low password cost, and a store both APIs' identity
// services read, as the stack's servers share shop-db. The store is in
// memory, since the tests serve the ORM's no-op database. The runtime's
// SQLStore runs over Postgres in TestStackDevRunsTheShop, and over SQLite,
// on shop-db's identity tables alone, in TestEverySDKCallsTheRustServer,
// whose server is another process.
type users struct {
	store identity.Store
	cfg   identity.Config
}

// testIdentityConfig hashes at a low cost and trusts the storefront's
// origin, which may sign in with a cookie from across origins.
const testIdentityConfig = `{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}, "trustedOrigins": ["https://storefront.example.com"]}`

func newUsers(t *testing.T) *users {
	t.Helper()
	cfg, err := identity.ParseConfig([]byte(testIdentityConfig))
	if err != nil {
		t.Fatal(err)
	}
	return &users{store: newMemoryStore(), cfg: cfg}
}

// newSQLiteUsers are users in a new SQLite database at path, which holds
// shop-db's identity tables as rust-server/identity.sql creates them, so the
// Rust server reads the same users and sessions.
func newSQLiteUsers(t *testing.T, path string) *users {
	t.Helper()
	ddl, err := os.ReadFile("../rust-server/identity.sql")
	if err != nil {
		t.Fatal(err)
	}
	database, err := sql.Open("sqlite", "file:"+path+"?_pragma=foreign_keys(1)&_pragma=busy_timeout(5000)")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = database.Close() })
	if _, err := database.Exec(string(ddl)); err != nil {
		t.Fatalf("create the identity tables: %v", err)
	}
	store, err := identity.NewSQLStore(database, identity.SQLite, []byte(db.IdentityDescriptor))
	if err != nil {
		t.Fatal(err)
	}
	u := newUsers(t)
	u.store = store
	return u
}

// add creates a user with password and, with permissions, a role of their
// own that grants them, as staff would through the administration routes.
func (u *users) add(t *testing.T, login, name, password string, permissions ...string) identity.User {
	t.Helper()
	ctx := context.Background()
	hash, err := identity.HashPassword(password, u.cfg.Argon2Params())
	if err != nil {
		t.Fatal(err)
	}
	user, err := u.store.CreateUser(ctx, identity.NewUser{Login: login, Name: name, PasswordHash: hash, At: time.Now()})
	if err != nil {
		t.Fatal(err)
	}
	if len(permissions) > 0 {
		role, err := u.store.CreateRole(ctx, strings.ReplaceAll(strings.Join(permissions, "-"), ".", "-")+"-"+strings.Split(login, "@")[0], permissions)
		if err != nil {
			t.Fatal(err)
		}
		if err := u.store.GrantRole(ctx, user.ID, role.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
	}
	return user
}

// shopAPI and shopOrders are each API's identity service over the store,
// built as the stack's entrypoints build them.
func (u *users) shopAPI(t *testing.T) *identity.Service {
	t.Helper()
	svc, err := shopapi.NewIdentity(u.store, u.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

func (u *users) shopOrders(t *testing.T) *identity.Service {
	t.Helper()
	svc, err := shoporders.NewIdentity(u.store, u.cfg)
	if err != nil {
		t.Fatal(err)
	}
	return svc
}

// memoryStore is identity.Store in memory, with the rules the runtime's
// SQLStore keeps: a login is found in the case its scalar normalizes,
// disabling a user or setting their password revokes their sessions, and a
// role's grants go with it.
type memoryStore struct {
	mu          sync.Mutex
	users       map[string]*memoryUser
	sessions    map[string]*identity.Session
	byTokenHash map[string]string
	roles       map[string]*identity.Role
	grants      map[string][]string
}

type memoryUser struct {
	user identity.User
	hash string
}

var _ identity.Store = (*memoryStore)(nil)

func newMemoryStore() *memoryStore {
	return &memoryStore{
		users:       map[string]*memoryUser{},
		sessions:    map[string]*identity.Session{},
		byTokenHash: map[string]string{},
		roles:       map[string]*identity.Role{},
		grants:      map[string][]string{},
	}
}

func (s *memoryStore) FindLogin(_ context.Context, login string) (identity.LoginRecord, error) {
	normalized, err := scalars.ParseContactEmail(login)
	if err != nil {
		return identity.LoginRecord{}, &identity.InvalidLoginError{Scalar: "Contact.Email", Err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.user.Login == normalized {
			return identity.LoginRecord{User: u.user, PasswordHash: u.hash}, nil
		}
	}
	return identity.LoginRecord{}, identity.ErrNotFound
}

func (s *memoryStore) FindCredential(_ context.Context, userID string) (identity.LoginRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return identity.LoginRecord{}, identity.ErrNotFound
	}
	return identity.LoginRecord{User: u.user, PasswordHash: u.hash}, nil
}

func (s *memoryStore) GetUser(_ context.Context, id string) (identity.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[id]
	if !ok {
		return identity.User{}, identity.ErrNotFound
	}
	return s.withRoles(u.user), nil
}

func (s *memoryStore) ListUsers(context.Context) ([]identity.User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []identity.User{}
	for _, u := range s.users {
		out = append(out, s.withRoles(u.user))
	}
	slices.SortFunc(out, func(a, b identity.User) int { return strings.Compare(a.Login, b.Login) })
	return out, nil
}

func (s *memoryStore) CreateUser(_ context.Context, nu identity.NewUser) (identity.User, error) {
	login, err := scalars.ParseContactEmail(nu.Login)
	if err != nil {
		return identity.User{}, &identity.InvalidLoginError{Scalar: "Contact.Email", Err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, u := range s.users {
		if u.user.Login == login {
			return identity.User{}, identity.ErrLoginTaken
		}
	}
	name := nu.Name
	if name == "" {
		name = login
	}
	user := identity.User{ID: scalars.NewUUID().String(), Login: login, Name: name}
	s.users[user.ID] = &memoryUser{user: user, hash: nu.PasswordHash}
	return user, nil
}

func (s *memoryStore) SetPassword(_ context.Context, userID, passwordHash string, at time.Time, keepSession string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return identity.ErrNotFound
	}
	u.hash = passwordHash
	s.revokeUser(userID, keepSession, at)
	return nil
}

func (s *memoryStore) RehashPassword(_ context.Context, userID, oldHash, newHash string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if u, ok := s.users[userID]; ok && u.hash == oldHash {
		u.hash = newHash
	}
	return nil
}

func (s *memoryStore) SetDisabled(_ context.Context, userID string, disabled bool, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	u, ok := s.users[userID]
	if !ok {
		return identity.ErrNotFound
	}
	u.user.Disabled = disabled
	if disabled {
		s.revokeUser(userID, "", at)
	}
	return nil
}

func (s *memoryStore) CreateSession(_ context.Context, ns identity.NewSession) (identity.Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session := &identity.Session{ID: scalars.NewUUID().String(), UserID: ns.UserID, CreatedAt: ns.CreatedAt, ExpiresAt: ns.ExpiresAt}
	s.sessions[session.ID] = session
	s.byTokenHash[ns.TokenHash] = session.ID
	return *session, nil
}

func (s *memoryStore) FindSession(_ context.Context, tokenHash string) (identity.SessionRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[s.byTokenHash[tokenHash]]
	if !ok {
		return identity.SessionRecord{}, identity.ErrNotFound
	}
	return identity.SessionRecord{Session: *session, User: s.users[session.UserID].user}, nil
}

func (s *memoryStore) TouchSession(_ context.Context, sessionID string, at, staleBefore time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok && (session.LastSeenAt == nil || session.LastSeenAt.Before(staleBefore)) {
		session.LastSeenAt = &at
	}
	return nil
}

func (s *memoryStore) RevokeSession(_ context.Context, sessionID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if session, ok := s.sessions[sessionID]; ok && session.RevokedAt == nil {
		session.RevokedAt = &at
	}
	return nil
}

func (s *memoryStore) RevokeUserSessions(_ context.Context, userID, exceptSession string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.revokeUser(userID, exceptSession, at)
	return nil
}

func (s *memoryStore) revokeUser(userID, exceptSession string, at time.Time) {
	for id, session := range s.sessions {
		if session.UserID == userID && id != exceptSession && session.RevokedAt == nil {
			session.RevokedAt = &at
		}
	}
}

func (s *memoryStore) HasRoles() bool { return true }

func (s *memoryStore) UserRoles(_ context.Context, userID string) ([]identity.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.rolesOf(userID), nil
}

func (s *memoryStore) ListRoles(context.Context) ([]identity.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := []identity.Role{}
	for _, role := range s.roles {
		out = append(out, *role)
	}
	slices.SortFunc(out, func(a, b identity.Role) int { return strings.Compare(a.Name, b.Name) })
	return out, nil
}

func (s *memoryStore) GetRole(_ context.Context, id string) (identity.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	role, ok := s.roles[id]
	if !ok {
		return identity.Role{}, identity.ErrNotFound
	}
	return *role, nil
}

func (s *memoryStore) CreateRole(_ context.Context, name string, permissions []string) (identity.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, role := range s.roles {
		if role.Name == name {
			return identity.Role{}, identity.ErrRoleNameTaken
		}
	}
	role := &identity.Role{ID: scalars.NewUUID().String(), Name: name, Permissions: slices.Clone(permissions)}
	s.roles[role.ID] = role
	return *role, nil
}

func (s *memoryStore) UpdateRole(_ context.Context, id, name string, permissions []string) (identity.Role, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	role, ok := s.roles[id]
	if !ok {
		return identity.Role{}, identity.ErrNotFound
	}
	for otherID, other := range s.roles {
		if otherID != id && other.Name == name {
			return identity.Role{}, identity.ErrRoleNameTaken
		}
	}
	role.Name, role.Permissions = name, slices.Clone(permissions)
	return *role, nil
}

func (s *memoryStore) DeleteRole(_ context.Context, id string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.roles[id]; !ok {
		return identity.ErrNotFound
	}
	delete(s.roles, id)
	for userID, roles := range s.grants {
		s.grants[userID] = slices.DeleteFunc(roles, func(r string) bool { return r == id })
	}
	return nil
}

func (s *memoryStore) GrantRole(_ context.Context, userID, roleID string, _ time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[userID] == nil || s.roles[roleID] == nil {
		return identity.ErrNotFound
	}
	if !slices.Contains(s.grants[userID], roleID) {
		s.grants[userID] = append(s.grants[userID], roleID)
	}
	return nil
}

func (s *memoryStore) RevokeRole(_ context.Context, userID, roleID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.users[userID] == nil || s.roles[roleID] == nil {
		return identity.ErrNotFound
	}
	s.grants[userID] = slices.DeleteFunc(s.grants[userID], func(r string) bool { return r == roleID })
	return nil
}

// Bootstrap creates a role, a user who holds it and the grant at once, and
// refuses when any user holds a role, as the runtime's SQLStore does.
func (s *memoryStore) Bootstrap(_ context.Context, b identity.NewBootstrap) (identity.Role, identity.User, error) {
	login, err := scalars.ParseContactEmail(b.User.Login)
	if err != nil {
		return identity.Role{}, identity.User{}, &identity.InvalidLoginError{Scalar: "Contact.Email", Err: err}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, roles := range s.grants {
		if len(roles) > 0 {
			return identity.Role{}, identity.User{}, identity.ErrGrantExists
		}
	}
	for _, role := range s.roles {
		if role.Name == b.Role {
			return identity.Role{}, identity.User{}, identity.ErrRoleNameTaken
		}
	}
	for _, u := range s.users {
		if u.user.Login == login {
			return identity.Role{}, identity.User{}, identity.ErrLoginTaken
		}
	}
	name := b.User.Name
	if name == "" {
		name = login
	}
	role := &identity.Role{ID: scalars.NewUUID().String(), Name: b.Role, Permissions: slices.Clone(b.Permissions)}
	user := identity.User{ID: scalars.NewUUID().String(), Login: login, Name: name}
	s.roles[role.ID] = role
	s.users[user.ID] = &memoryUser{user: user, hash: b.User.PasswordHash}
	s.grants[user.ID] = []string{role.ID}
	return *role, s.withRoles(user), nil
}

// rolesOf is the roles the user holds, by name.
func (s *memoryStore) rolesOf(userID string) []identity.Role {
	out := []identity.Role{}
	for _, id := range s.grants[userID] {
		out = append(out, *s.roles[id])
	}
	slices.SortFunc(out, func(a, b identity.Role) int { return strings.Compare(a.Name, b.Name) })
	return out
}

func (s *memoryStore) withRoles(u identity.User) identity.User {
	u.Roles = s.rolesOf(u.ID)
	return u
}
