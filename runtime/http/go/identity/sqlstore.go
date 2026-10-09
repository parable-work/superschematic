package identity

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/google/uuid"
	superscalar "github.com/parable-work/superscalar/go"
)

// Dialect is the SQL a SQLStore writes.
type Dialect string

const (
	// Postgres: $n placeholders, TIMESTAMPTZ timestamps, TEXT[] permissions
	// (read and written as JSON through array_to_json and
	// json_array_elements_text).
	Postgres Dialect = "postgres"
	// SQLite: ? placeholders, timestamps as TEXT in SQLiteTimeLayout, and
	// permissions as a JSON array in TEXT, as sqlite/create.sql declares
	// them.
	SQLite Dialect = "sqlite"
)

// SQLiteTimeLayout is how a SQLite store writes a timestamp: UTC to the
// millisecond, the form the DDL's strftime('%Y-%m-%dT%H:%M:%fZ', 'now')
// default writes, so every value has one width and compares as text in
// time order.
const SQLiteTimeLayout = "2006-01-02T15:04:05.000Z"

// sessionKeyScalar is the key of every table the loader adds,
// AutoGenerate<Identity.UUID>.
const sessionKeyScalar = "Identity.UUID"

// SQLStore is the Store over database/sql, built from a schema's identity
// descriptor. It links no driver: the caller opens db with one (pgx's
// stdlib or lib/pq for Postgres, modernc.org/sqlite for SQLite). It quotes
// every name the descriptor gives, finds a login by equality after the
// login scalar parses it, and leaves the case rule to the column (CITEXT,
// TEXT COLLATE NOCASE). Keys are in their scalar's wire form outside the
// store: an Identity.UUID key is its base62 form, as the generated types
// write it, and hyphenated in the database.
//
// A SQLite connection should set foreign_keys and a busy_timeout; the
// store deletes a role's grants itself, so it does not rely on the
// cascade.
type SQLStore struct {
	db      *sql.DB
	dialect Dialect
	desc    Descriptor

	userKey, roleKey, sessionKey keyCodec
	loginScalar, nameScalar      string
	nameIsLogin                  bool

	t names
}

// SQLStoreOption configures a SQLStore.
type SQLStoreOption func(*SQLStore)

// names are the quoted table and column names the statements use.
type names struct {
	user, userKey, userLogin, userName                                    string
	session, sessionID, sessionUser, tokenHash                            string
	createdAt, expiresAt, lastSeenAt, revokedAt                           string
	credential, credentialUser, passwordHash, passwordChangedAt, disabled string
	role, roleKey, roleName, rolePermissions                              string
	grant, grantUser, grantRole, grantedAt                                string
}

// NewSQLStore builds a store over db from the identity descriptor's JSON.
func NewSQLStore(db *sql.DB, dialect Dialect, descriptor []byte, opts ...SQLStoreOption) (*SQLStore, error) {
	if db == nil {
		return nil, errors.New("identity: NewSQLStore needs a database")
	}
	if dialect != Postgres && dialect != SQLite {
		return nil, fmt.Errorf("identity: unknown dialect %q (postgres or sqlite)", dialect)
	}
	d, err := ParseDescriptor(descriptor)
	if err != nil {
		return nil, err
	}
	if !superscalar.KnownScalar(d.User.LoginScalar) {
		return nil, fmt.Errorf("identity: the login scalar %q is not one superscalar knows", d.User.LoginScalar)
	}
	s := &SQLStore{
		db:          db,
		dialect:     dialect,
		desc:        d,
		userKey:     newKeyCodec(d.User.KeyScalar),
		sessionKey:  newKeyCodec(sessionKeyScalar),
		loginScalar: d.User.LoginScalar,
		nameScalar:  d.User.NameScalar,
		nameIsLogin: d.User.Columns.Name == d.User.Columns.Login,
	}
	if d.Role != nil {
		s.roleKey = newKeyCodec(d.Role.KeyScalar)
	}
	for _, opt := range opts {
		opt(s)
	}
	u, se, c := d.User, d.Session, d.Credential
	s.t = names{
		user: quote(u.Table), userKey: quote(u.Columns.Key), userLogin: quote(u.Columns.Login), userName: quote(u.Columns.Name),
		session: quote(se.Table), sessionID: quote(se.Columns.ID), sessionUser: quote(se.Columns.User), tokenHash: quote(se.Columns.TokenHash),
		createdAt: quote(se.Columns.CreatedAt), expiresAt: quote(se.Columns.ExpiresAt), lastSeenAt: quote(se.Columns.LastSeenAt), revokedAt: quote(se.Columns.RevokedAt),
		credential: quote(c.Table), credentialUser: quote(c.Columns.User), passwordHash: quote(c.Columns.PasswordHash),
		passwordChangedAt: quote(c.Columns.PasswordChangedAt), disabled: quote(c.Columns.DisabledAt),
	}
	if r, g := d.Role, d.RoleGrant; r != nil {
		s.t.role, s.t.roleKey, s.t.roleName, s.t.rolePermissions = quote(r.Table), quote(r.Columns.Key), quote(r.Columns.Name), quote(r.Columns.Permissions)
		s.t.grant, s.t.grantUser, s.t.grantRole, s.t.grantedAt = quote(g.Table), quote(g.Columns.User), quote(g.Columns.Role), quote(g.Columns.GrantedAt)
	}
	return s, nil
}

// Descriptor is the descriptor the store was built from.
func (s *SQLStore) Descriptor() Descriptor { return s.desc }

// keyCodec moves a key between its wire form and the database's. A key of
// a scalar whose SQL type is UUID (Identity.UUID, Identity.UserID) is
// base62 on the wire and hyphenated in the database; any other is the
// same text in both, parsed by its scalar on the way in when superscalar
// knows it.
type keyCodec struct {
	scalar string
	uuid   bool
}

func newKeyCodec(scalar string) keyCodec {
	c := keyCodec{scalar: scalar}
	for _, m := range superscalar.SCALAR_METADATA {
		if m.CanonicalName == scalar {
			c.uuid = strings.EqualFold(m.SQLType, "UUID")
		}
	}
	return c
}

// toDB is a wire key's database form, and false for a value that is not a
// key of the scalar, which names no row.
func (c keyCodec) toDB(key string) (string, bool) {
	if c.uuid {
		u, err := superscalar.ParseUUID(key)
		if err != nil {
			return "", false
		}
		return u.ToUUID().String(), true
	}
	if superscalar.KnownScalar(c.scalar) {
		parsed, err := superscalar.Parse(c.scalar, key)
		if err != nil {
			return "", false
		}
		return parsed, true
	}
	return key, key != ""
}

// toWire is a database key's wire form.
func (c keyCodec) toWire(key string) string {
	if c.uuid {
		if u, err := uuid.Parse(key); err == nil {
			return superscalar.FromUUID(u).String()
		}
	}
	return key
}

// parseLogin is the login scalar's parse of login.
func (s *SQLStore) parseLogin(login string) (string, error) {
	parsed, err := superscalar.Parse(s.loginScalar, login)
	if err != nil {
		return "", &InvalidLoginError{Scalar: s.loginScalar, Err: err}
	}
	return parsed, nil
}

// bind turns a statement written with ? placeholders into the dialect's,
// leaving quoted names and literals alone.
func (s *SQLStore) bind(query string) string {
	if s.dialect != Postgres {
		return query
	}
	var b strings.Builder
	n := 0
	var quoteChar byte
	for i := 0; i < len(query); i++ {
		c := query[i]
		switch {
		case quoteChar != 0:
			if c == quoteChar {
				quoteChar = 0
			}
		case c == '"' || c == '\'':
			quoteChar = c
		case c == '?':
			n++
			b.WriteString("$" + strconv.Itoa(n))
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

// timeArg is a timestamp as the dialect stores it.
func (s *SQLStore) timeArg(t time.Time) any {
	t = t.UTC()
	if s.dialect == SQLite {
		return t.Format(SQLiteTimeLayout)
	}
	return t
}

// permissionsRead is the expression that reads a role's permissions as a
// JSON array, and permissionsWrite the one that writes them from one.
func (s *SQLStore) permissionsRead(alias string) string {
	if s.dialect == Postgres {
		return "array_to_json(" + alias + "." + s.t.rolePermissions + ")::text"
	}
	return alias + "." + s.t.rolePermissions
}

func (s *SQLStore) permissionsWrite() string {
	if s.dialect == Postgres {
		return "ARRAY(SELECT e FROM json_array_elements_text(CAST(? AS json)) WITH ORDINALITY AS p(e, n) ORDER BY n)"
	}
	return "?"
}

func permissionsJSON(permissions []string) string {
	if permissions == nil {
		permissions = []string{}
	}
	b, _ := json.Marshal(permissions)
	return string(b)
}

// timeScan scans a timestamp in any form the drivers return one: a
// time.Time, or text in RFC 3339 or SQLite's own layouts.
type timeScan struct {
	t     time.Time
	valid bool
}

func (ts *timeScan) Scan(src any) error {
	switch v := src.(type) {
	case nil:
		ts.valid = false
		return nil
	case time.Time:
		ts.t, ts.valid = v.UTC(), true
		return nil
	case []byte:
		return ts.parse(string(v))
	case string:
		return ts.parse(v)
	default:
		return fmt.Errorf("identity: cannot read a timestamp from %T", src)
	}
}

func (ts *timeScan) parse(v string) error {
	for _, layout := range []string{time.RFC3339Nano, "2006-01-02 15:04:05.999999999Z07:00", "2006-01-02 15:04:05.999999999", "2006-01-02T15:04:05.999999999"} {
		if t, err := time.Parse(layout, v); err == nil {
			ts.t, ts.valid = t.UTC(), true
			return nil
		}
	}
	return fmt.Errorf("identity: cannot read %q as a timestamp", v)
}

func (ts *timeScan) ptr() *time.Time {
	if !ts.valid {
		return nil
	}
	t := ts.t
	return &t
}

// querier is a *sql.DB or a *sql.Tx.
type querier interface {
	ExecContext(ctx context.Context, query string, args ...any) (sql.Result, error)
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

// inTx runs fn in a transaction, committing when it returns nil.
func (s *SQLStore) inTx(ctx context.Context, fn func(tx *sql.Tx) error) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("identity: begin: %w", err)
	}
	if err := fn(tx); err != nil {
		_ = tx.Rollback()
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("identity: commit: %w", err)
	}
	return nil
}

// exists reports whether a row of table has key.
func (s *SQLStore) exists(ctx context.Context, q querier, table, column, key string) (bool, error) {
	var one int
	err := q.QueryRowContext(ctx, s.bind("SELECT 1 FROM "+table+" WHERE "+column+" = ?"), key).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, fmt.Errorf("identity: look up %s: %w", table, err)
	}
	return true, nil
}

// userSelect selects a user's key, login, name, disabledAt and password
// hash, the credential joined when there is one.
func (s *SQLStore) userSelect() string {
	t := s.t
	return "SELECT u." + t.userKey + ", u." + t.userLogin + ", u." + t.userName + ", c." + t.disabled + ", c." + t.passwordHash +
		" FROM " + t.user + " u LEFT JOIN " + t.credential + " c ON c." + t.credentialUser + " = u." + t.userKey
}

type scanner interface{ Scan(dest ...any) error }

func (s *SQLStore) scanUser(row scanner) (LoginRecord, error) {
	var key, login, name string
	var disabled timeScan
	var hash sql.NullString
	if err := row.Scan(&key, &login, &name, &disabled, &hash); err != nil {
		return LoginRecord{}, err
	}
	return LoginRecord{
		User:         User{ID: s.userKey.toWire(key), Login: login, Name: name, Disabled: disabled.valid},
		PasswordHash: hash.String,
	}, nil
}

func (s *SQLStore) findUser(ctx context.Context, q querier, where string, arg string) (LoginRecord, error) {
	rec, err := s.scanUser(q.QueryRowContext(ctx, s.bind(s.userSelect()+" WHERE u."+where+" = ?"), arg))
	if errors.Is(err, sql.ErrNoRows) {
		return LoginRecord{}, ErrNotFound
	}
	if err != nil {
		return LoginRecord{}, fmt.Errorf("identity: find a user: %w", err)
	}
	return rec, nil
}

// FindLogin implements Store.
func (s *SQLStore) FindLogin(ctx context.Context, login string) (LoginRecord, error) {
	parsed, err := s.parseLogin(login)
	if err != nil {
		return LoginRecord{}, err
	}
	return s.findUser(ctx, s.db, s.t.userLogin, parsed)
}

// FindCredential implements Store.
func (s *SQLStore) FindCredential(ctx context.Context, userID string) (LoginRecord, error) {
	key, ok := s.userKey.toDB(userID)
	if !ok {
		return LoginRecord{}, ErrNotFound
	}
	return s.findUser(ctx, s.db, s.t.userKey, key)
}

// GetUser implements Store.
func (s *SQLStore) GetUser(ctx context.Context, id string) (User, error) {
	key, ok := s.userKey.toDB(id)
	if !ok {
		return User{}, ErrNotFound
	}
	rec, err := s.findUser(ctx, s.db, s.t.userKey, key)
	if err != nil {
		return User{}, err
	}
	rec.User.Roles, err = s.UserRoles(ctx, rec.User.ID)
	if err != nil {
		return User{}, err
	}
	return rec.User, nil
}

// ListUsers implements Store.
func (s *SQLStore) ListUsers(ctx context.Context) ([]User, error) {
	rows, err := s.db.QueryContext(ctx, s.userSelect())
	if err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	defer rows.Close()
	users := []User{}
	for rows.Next() {
		rec, err := s.scanUser(rows)
		if err != nil {
			return nil, fmt.Errorf("identity: list users: %w", err)
		}
		rec.User.Roles = []Role{}
		users = append(users, rec.User)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: list users: %w", err)
	}
	if s.HasRoles() {
		t := s.t
		grants, err := s.queryRoles(ctx, s.db, "SELECT g."+t.grantUser+", r."+t.roleKey+", r."+t.roleName+", "+s.permissionsRead("r")+
			" FROM "+t.grant+" g JOIN "+t.role+" r ON r."+t.roleKey+" = g."+t.grantRole, true)
		if err != nil {
			return nil, err
		}
		byUser := map[string][]Role{}
		for _, g := range grants {
			byUser[g.user] = append(byUser[g.user], g.role)
		}
		for i := range users {
			if roles := byUser[users[i].ID]; roles != nil {
				sortRoles(roles)
				users[i].Roles = roles
			}
		}
	}
	slices.SortFunc(users, func(a, b User) int { return strings.Compare(a.Login, b.Login) })
	return users, nil
}

// CreateUser implements Store.
func (s *SQLStore) CreateUser(ctx context.Context, nu NewUser) (User, error) {
	login, err := s.parseLogin(nu.Login)
	if err != nil {
		return User{}, err
	}
	name := nu.Name
	if name == "" || s.nameIsLogin {
		name = login
	} else if superscalar.KnownScalar(s.nameScalar) {
		// The name column has its scalar's bounds; a name outside them is
		// the caller's, not a failed write.
		if name, err = superscalar.Parse(s.nameScalar, name); err != nil {
			return User{}, &InvalidNameError{Scalar: s.nameScalar, Err: err}
		}
	}
	t := s.t
	columns, values, args := t.userLogin, "?", []any{login}
	if !s.nameIsLogin {
		columns, values, args = columns+", "+t.userName, values+", ?", append(args, name)
	}
	var user User
	err = s.inTx(ctx, func(tx *sql.Tx) error {
		var key string
		err := tx.QueryRowContext(ctx, s.bind("INSERT INTO "+t.user+" ("+columns+") VALUES ("+values+") ON CONFLICT ("+t.userLogin+") DO NOTHING RETURNING "+t.userKey), args...).Scan(&key)
		if errors.Is(err, sql.ErrNoRows) {
			return ErrLoginTaken
		}
		if err != nil {
			return fmt.Errorf("identity: create a user: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO "+t.credential+" ("+t.credentialUser+", "+t.passwordHash+", "+t.passwordChangedAt+") VALUES (?, ?, ?)"),
			key, nu.PasswordHash, s.timeArg(nu.At)); err != nil {
			return fmt.Errorf("identity: create a credential: %w", err)
		}
		user = User{ID: s.userKey.toWire(key), Login: login, Name: name, Roles: []Role{}}
		return nil
	})
	return user, err
}

// SetPassword implements Store.
func (s *SQLStore) SetPassword(ctx context.Context, userID, passwordHash string, at time.Time, keepSession string) error {
	key, ok := s.userKey.toDB(userID)
	if !ok {
		return ErrNotFound
	}
	var keep string
	if keepSession != "" {
		if keep, ok = s.sessionKey.toDB(keepSession); !ok {
			return ErrNotFound
		}
	}
	t := s.t
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if found, err := s.exists(ctx, tx, t.user, t.userKey, key); err != nil || !found {
			return orNotFound(err)
		}
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO "+t.credential+" ("+t.credentialUser+", "+t.passwordHash+", "+t.passwordChangedAt+") VALUES (?, ?, ?)"+
			" ON CONFLICT ("+t.credentialUser+") DO UPDATE SET "+t.passwordHash+" = excluded."+t.passwordHash+", "+t.passwordChangedAt+" = excluded."+t.passwordChangedAt),
			key, passwordHash, s.timeArg(at)); err != nil {
			return fmt.Errorf("identity: set a password: %w", err)
		}
		return s.revokeUserSessions(ctx, tx, key, keep, at)
	})
}

// RehashPassword implements Store.
func (s *SQLStore) RehashPassword(ctx context.Context, userID, oldHash, newHash string) error {
	key, ok := s.userKey.toDB(userID)
	if !ok {
		return ErrNotFound
	}
	t := s.t
	if _, err := s.db.ExecContext(ctx, s.bind("UPDATE "+t.credential+" SET "+t.passwordHash+" = ? WHERE "+t.credentialUser+" = ? AND "+t.passwordHash+" = ?"),
		newHash, key, oldHash); err != nil {
		return fmt.Errorf("identity: rehash a password: %w", err)
	}
	return nil
}

// SetDisabled implements Store.
func (s *SQLStore) SetDisabled(ctx context.Context, userID string, disabled bool, at time.Time) error {
	key, ok := s.userKey.toDB(userID)
	if !ok {
		return ErrNotFound
	}
	t := s.t
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if found, err := s.exists(ctx, tx, t.user, t.userKey, key); err != nil || !found {
			return orNotFound(err)
		}
		if !disabled {
			if _, err := tx.ExecContext(ctx, s.bind("UPDATE "+t.credential+" SET "+t.disabled+" = NULL WHERE "+t.credentialUser+" = ?"), key); err != nil {
				return fmt.Errorf("identity: enable a user: %w", err)
			}
			return nil
		}
		// A user without a credential gets one with no password, which
		// matches none, so the user still reads as disabled.
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO "+t.credential+" ("+t.credentialUser+", "+t.passwordHash+", "+t.passwordChangedAt+", "+t.disabled+") VALUES (?, '', ?, ?)"+
			" ON CONFLICT ("+t.credentialUser+") DO UPDATE SET "+t.disabled+" = excluded."+t.disabled),
			key, s.timeArg(at), s.timeArg(at)); err != nil {
			return fmt.Errorf("identity: disable a user: %w", err)
		}
		return s.revokeUserSessions(ctx, tx, key, "", at)
	})
}

// CreateSession implements Store.
func (s *SQLStore) CreateSession(ctx context.Context, ns NewSession) (Session, error) {
	key, ok := s.userKey.toDB(ns.UserID)
	if !ok {
		return Session{}, ErrNotFound
	}
	t := s.t
	var id string
	if err := s.db.QueryRowContext(ctx, s.bind("INSERT INTO "+t.session+" ("+t.sessionUser+", "+t.tokenHash+", "+t.createdAt+", "+t.expiresAt+") VALUES (?, ?, ?, ?) RETURNING "+t.sessionID),
		key, ns.TokenHash, s.timeArg(ns.CreatedAt), s.timeArg(ns.ExpiresAt)).Scan(&id); err != nil {
		return Session{}, fmt.Errorf("identity: create a session: %w", err)
	}
	return Session{ID: s.sessionKey.toWire(id), UserID: ns.UserID, CreatedAt: ns.CreatedAt.UTC(), ExpiresAt: ns.ExpiresAt.UTC()}, nil
}

// FindSession implements Store.
func (s *SQLStore) FindSession(ctx context.Context, tokenHash string) (SessionRecord, error) {
	t := s.t
	query := "SELECT s." + t.sessionID + ", s." + t.sessionUser + ", s." + t.createdAt + ", s." + t.expiresAt + ", s." + t.lastSeenAt + ", s." + t.revokedAt +
		", u." + t.userLogin + ", u." + t.userName + ", c." + t.disabled +
		" FROM " + t.session + " s JOIN " + t.user + " u ON u." + t.userKey + " = s." + t.sessionUser +
		" LEFT JOIN " + t.credential + " c ON c." + t.credentialUser + " = s." + t.sessionUser +
		" WHERE s." + t.tokenHash + " = ?"
	var id, user, login, name string
	var created, expires, lastSeen, revoked, disabled timeScan
	err := s.db.QueryRowContext(ctx, s.bind(query), tokenHash).Scan(&id, &user, &created, &expires, &lastSeen, &revoked, &login, &name, &disabled)
	if errors.Is(err, sql.ErrNoRows) {
		return SessionRecord{}, ErrNotFound
	}
	if err != nil {
		return SessionRecord{}, fmt.Errorf("identity: find a session: %w", err)
	}
	userID := s.userKey.toWire(user)
	return SessionRecord{
		Session: Session{
			ID: s.sessionKey.toWire(id), UserID: userID, CreatedAt: created.t, ExpiresAt: expires.t,
			LastSeenAt: lastSeen.ptr(), RevokedAt: revoked.ptr(),
		},
		User: User{ID: userID, Login: login, Name: name, Disabled: disabled.valid},
	}, nil
}

// TouchSession implements Store.
func (s *SQLStore) TouchSession(ctx context.Context, sessionID string, at, staleBefore time.Time) error {
	id, ok := s.sessionKey.toDB(sessionID)
	if !ok {
		return ErrNotFound
	}
	t := s.t
	if _, err := s.db.ExecContext(ctx, s.bind("UPDATE "+t.session+" SET "+t.lastSeenAt+" = ? WHERE "+t.sessionID+" = ? AND ("+t.lastSeenAt+" IS NULL OR "+t.lastSeenAt+" < ?)"),
		s.timeArg(at), id, s.timeArg(staleBefore)); err != nil {
		return fmt.Errorf("identity: touch a session: %w", err)
	}
	return nil
}

// RevokeSession implements Store.
func (s *SQLStore) RevokeSession(ctx context.Context, sessionID string, at time.Time) error {
	id, ok := s.sessionKey.toDB(sessionID)
	if !ok {
		return ErrNotFound
	}
	t := s.t
	if _, err := s.db.ExecContext(ctx, s.bind("UPDATE "+t.session+" SET "+t.revokedAt+" = ? WHERE "+t.sessionID+" = ? AND "+t.revokedAt+" IS NULL"),
		s.timeArg(at), id); err != nil {
		return fmt.Errorf("identity: revoke a session: %w", err)
	}
	return nil
}

// RevokeUserSessions implements Store.
func (s *SQLStore) RevokeUserSessions(ctx context.Context, userID, exceptSession string, at time.Time) error {
	key, ok := s.userKey.toDB(userID)
	if !ok {
		return ErrNotFound
	}
	var keep string
	if exceptSession != "" {
		if keep, ok = s.sessionKey.toDB(exceptSession); !ok {
			return ErrNotFound
		}
	}
	return s.revokeUserSessions(ctx, s.db, key, keep, at)
}

// revokeUserSessions revokes the live sessions of the user with the
// database key key, but the session with the database key keep.
func (s *SQLStore) revokeUserSessions(ctx context.Context, q querier, key, keep string, at time.Time) error {
	t := s.t
	query := "UPDATE " + t.session + " SET " + t.revokedAt + " = ? WHERE " + t.sessionUser + " = ? AND " + t.revokedAt + " IS NULL"
	args := []any{s.timeArg(at), key}
	if keep != "" {
		query += " AND " + t.sessionID + " <> ?"
		args = append(args, keep)
	}
	if _, err := q.ExecContext(ctx, s.bind(query), args...); err != nil {
		return fmt.Errorf("identity: revoke a user's sessions: %w", err)
	}
	return nil
}

// HasRoles implements Store.
func (s *SQLStore) HasRoles() bool { return s.desc.Role != nil }

// grantedRole is a role and the database key of a user who holds it.
type grantedRole struct {
	user string
	role Role
}

// queryRoles reads roles: key, name and permissions, after the holder's
// key when withUser.
func (s *SQLStore) queryRoles(ctx context.Context, q querier, query string, withUser bool, args ...any) ([]grantedRole, error) {
	rows, err := q.QueryContext(ctx, s.bind(query), args...)
	if err != nil {
		return nil, fmt.Errorf("identity: read roles: %w", err)
	}
	defer rows.Close()
	var out []grantedRole
	for rows.Next() {
		var user, key, name string
		var permissions sql.NullString
		dest := []any{&key, &name, &permissions}
		if withUser {
			dest = append([]any{&user}, dest...)
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, fmt.Errorf("identity: read roles: %w", err)
		}
		role := Role{ID: s.roleKey.toWire(key), Name: name, Permissions: []string{}}
		if permissions.Valid && permissions.String != "" {
			if err := json.Unmarshal([]byte(permissions.String), &role.Permissions); err != nil {
				return nil, fmt.Errorf("identity: read role %s's permissions: %w", name, err)
			}
			if role.Permissions == nil {
				role.Permissions = []string{}
			}
		}
		out = append(out, grantedRole{user: s.userKey.toWire(user), role: role})
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("identity: read roles: %w", err)
	}
	return out, nil
}

func (s *SQLStore) roleSelect() string {
	return "SELECT r." + s.t.roleKey + ", r." + s.t.roleName + ", " + s.permissionsRead("r") + " FROM " + s.t.role + " r"
}

func rolesOf(granted []grantedRole) []Role {
	roles := make([]Role, len(granted))
	for i, g := range granted {
		roles[i] = g.role
	}
	sortRoles(roles)
	return roles
}

// sortRoles orders roles by name, in byte order, the same in every
// dialect and database collation.
func sortRoles(roles []Role) {
	slices.SortFunc(roles, func(a, b Role) int { return strings.Compare(a.Name, b.Name) })
}

// UserRoles implements Store.
func (s *SQLStore) UserRoles(ctx context.Context, userID string) ([]Role, error) {
	if !s.HasRoles() {
		return []Role{}, nil
	}
	key, ok := s.userKey.toDB(userID)
	if !ok {
		return nil, ErrNotFound
	}
	t := s.t
	granted, err := s.queryRoles(ctx, s.db, "SELECT r."+t.roleKey+", r."+t.roleName+", "+s.permissionsRead("r")+
		" FROM "+t.grant+" g JOIN "+t.role+" r ON r."+t.roleKey+" = g."+t.grantRole+" WHERE g."+t.grantUser+" = ?", false, key)
	if err != nil {
		return nil, err
	}
	return rolesOf(granted), nil
}

// ListRoles implements Store.
func (s *SQLStore) ListRoles(ctx context.Context) ([]Role, error) {
	if !s.HasRoles() {
		return nil, ErrNoRoles
	}
	granted, err := s.queryRoles(ctx, s.db, s.roleSelect(), false)
	if err != nil {
		return nil, err
	}
	return rolesOf(granted), nil
}

// GetRole implements Store.
func (s *SQLStore) GetRole(ctx context.Context, id string) (Role, error) {
	if !s.HasRoles() {
		return Role{}, ErrNoRoles
	}
	key, ok := s.roleKey.toDB(id)
	if !ok {
		return Role{}, ErrNotFound
	}
	return s.getRole(ctx, s.db, key)
}

func (s *SQLStore) getRole(ctx context.Context, q querier, key string) (Role, error) {
	granted, err := s.queryRoles(ctx, q, s.roleSelect()+" WHERE r."+s.t.roleKey+" = ?", false, key)
	if err != nil {
		return Role{}, err
	}
	if len(granted) == 0 {
		return Role{}, ErrNotFound
	}
	return granted[0].role, nil
}

// CreateRole implements Store.
func (s *SQLStore) CreateRole(ctx context.Context, name string, permissions []string) (Role, error) {
	if !s.HasRoles() {
		return Role{}, ErrNoRoles
	}
	t := s.t
	var key string
	err := s.db.QueryRowContext(ctx, s.bind("INSERT INTO "+t.role+" ("+t.roleName+", "+t.rolePermissions+") VALUES (?, "+s.permissionsWrite()+") ON CONFLICT ("+t.roleName+") DO NOTHING RETURNING "+t.roleKey),
		name, permissionsJSON(permissions)).Scan(&key)
	if errors.Is(err, sql.ErrNoRows) {
		return Role{}, ErrRoleNameTaken
	}
	if err != nil {
		return Role{}, fmt.Errorf("identity: create a role: %w", err)
	}
	return Role{ID: s.roleKey.toWire(key), Name: name, Permissions: slices.Clone(permissions)}, nil
}

// UpdateRole implements Store.
func (s *SQLStore) UpdateRole(ctx context.Context, id, name string, permissions []string) (Role, error) {
	if !s.HasRoles() {
		return Role{}, ErrNoRoles
	}
	key, ok := s.roleKey.toDB(id)
	if !ok {
		return Role{}, ErrNotFound
	}
	t := s.t
	var role Role
	err := s.inTx(ctx, func(tx *sql.Tx) error {
		if found, err := s.exists(ctx, tx, t.role, t.roleKey, key); err != nil || !found {
			return orNotFound(err)
		}
		var one int
		err := tx.QueryRowContext(ctx, s.bind("SELECT 1 FROM "+t.role+" WHERE "+t.roleName+" = ? AND "+t.roleKey+" <> ?"), name, key).Scan(&one)
		switch {
		case err == nil:
			return ErrRoleNameTaken
		case !errors.Is(err, sql.ErrNoRows):
			return fmt.Errorf("identity: look up a role name: %w", err)
		}
		if _, err := tx.ExecContext(ctx, s.bind("UPDATE "+t.role+" SET "+t.roleName+" = ?, "+t.rolePermissions+" = "+s.permissionsWrite()+" WHERE "+t.roleKey+" = ?"),
			name, permissionsJSON(permissions), key); err != nil {
			return fmt.Errorf("identity: update a role: %w", err)
		}
		var err2 error
		role, err2 = s.getRole(ctx, tx, key)
		return err2
	})
	return role, err
}

// DeleteRole implements Store.
func (s *SQLStore) DeleteRole(ctx context.Context, id string) error {
	if !s.HasRoles() {
		return ErrNoRoles
	}
	key, ok := s.roleKey.toDB(id)
	if !ok {
		return ErrNotFound
	}
	t := s.t
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if _, err := tx.ExecContext(ctx, s.bind("DELETE FROM "+t.grant+" WHERE "+t.grantRole+" = ?"), key); err != nil {
			return fmt.Errorf("identity: delete a role's grants: %w", err)
		}
		res, err := tx.ExecContext(ctx, s.bind("DELETE FROM "+t.role+" WHERE "+t.roleKey+" = ?"), key)
		if err != nil {
			return fmt.Errorf("identity: delete a role: %w", err)
		}
		if n, err := res.RowsAffected(); err != nil {
			return fmt.Errorf("identity: delete a role: %w", err)
		} else if n == 0 {
			return ErrNotFound
		}
		return nil
	})
}

// GrantRole implements Store.
func (s *SQLStore) GrantRole(ctx context.Context, userID, roleID string, at time.Time) error {
	return s.changeGrant(ctx, userID, roleID, func(tx *sql.Tx, user, role string) error {
		t := s.t
		if _, err := tx.ExecContext(ctx, s.bind("INSERT INTO "+t.grant+" ("+t.grantUser+", "+t.grantRole+", "+t.grantedAt+") VALUES (?, ?, ?) ON CONFLICT ("+t.grantUser+", "+t.grantRole+") DO NOTHING"),
			user, role, s.timeArg(at)); err != nil {
			return fmt.Errorf("identity: grant a role: %w", err)
		}
		return nil
	})
}

// RevokeRole implements Store.
func (s *SQLStore) RevokeRole(ctx context.Context, userID, roleID string) error {
	return s.changeGrant(ctx, userID, roleID, func(tx *sql.Tx, user, role string) error {
		t := s.t
		if _, err := tx.ExecContext(ctx, s.bind("DELETE FROM "+t.grant+" WHERE "+t.grantUser+" = ? AND "+t.grantRole+" = ?"), user, role); err != nil {
			return fmt.Errorf("identity: revoke a role: %w", err)
		}
		return nil
	})
}

// changeGrant runs change in a transaction once the user and the role are
// found, with their database keys.
func (s *SQLStore) changeGrant(ctx context.Context, userID, roleID string, change func(tx *sql.Tx, user, role string) error) error {
	if !s.HasRoles() {
		return ErrNoRoles
	}
	user, ok := s.userKey.toDB(userID)
	if !ok {
		return ErrNotFound
	}
	role, ok := s.roleKey.toDB(roleID)
	if !ok {
		return ErrNotFound
	}
	t := s.t
	return s.inTx(ctx, func(tx *sql.Tx) error {
		if found, err := s.exists(ctx, tx, t.user, t.userKey, user); err != nil || !found {
			return orNotFound(err)
		}
		if found, err := s.exists(ctx, tx, t.role, t.roleKey, role); err != nil || !found {
			return orNotFound(err)
		}
		return change(tx, user, role)
	})
}

// orNotFound is err, or ErrNotFound when err is nil.
func orNotFound(err error) error {
	if err != nil {
		return err
	}
	return ErrNotFound
}

var _ Store = (*SQLStore)(nil)
