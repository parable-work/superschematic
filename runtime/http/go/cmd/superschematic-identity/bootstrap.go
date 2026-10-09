package main

import (
	"bufio"
	"context"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"slices"
	"strings"
	"time"

	// The drivers the store opens: pgx for Postgres, and the pure-Go
	// SQLite the store's tests and the migration runner use.
	_ "github.com/jackc/pgx/v5/stdlib"
	_ "modernc.org/sqlite"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// bootstrapInputs are bootstrap's flag values, checked before the password
// is read or the database opened.
type bootstrapInputs struct {
	descriptor  identity.Descriptor
	raw         []byte
	databaseURL string
	dialect     identity.Dialect
	login       string
	name        string
	role        string
	permissions []string
	params      identity.Argon2Params
}

// bootstrap creates a database's first administrator: the role, the user
// with the password standard input holds, and the grant, in one
// transaction (Store.Bootstrap).
func bootstrap(ctx context.Context, args []string, o options) error {
	in, err := parseBootstrap(args, o)
	if err != nil {
		return err
	}
	password, err := readPassword(in.login, o)
	if err != nil {
		return err
	}
	hash, err := identity.HashPassword(password, in.params)
	if err != nil {
		return err
	}
	db, err := openDatabase(in.dialect, in.databaseURL)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	store, err := identity.NewSQLStore(db, in.dialect, in.raw)
	if err != nil {
		return err
	}
	role, user, err := store.Bootstrap(ctx, identity.NewBootstrap{
		Role:        in.role,
		Permissions: in.permissions,
		User:        identity.NewUser{Login: in.login, Name: in.name, PasswordHash: hash, At: time.Now()},
	})
	if err != nil {
		return bootstrapError(err, in)
	}
	if _, err := fmt.Fprintf(o.stdout, "created role %s (%s) with %s\n", role.Name, role.ID, strings.Join(role.Permissions, ", ")); err != nil {
		return err
	}
	named := ""
	if user.Name != user.Login {
		named = ", named " + user.Name
	}
	_, err = fmt.Fprintf(o.stdout, "created user %s (%s)%s, who holds role %s\n", user.Login, user.ID, named, role.Name)
	return err
}

// parseBootstrap reads bootstrap's flags and the descriptor, and refuses
// what needs neither the password nor the database.
func parseBootstrap(args []string, o options) (bootstrapInputs, error) {
	fs := flag.NewFlagSet(programName+" bootstrap", flag.ContinueOnError)
	fs.SetOutput(o.stderr)
	descriptorPath := fs.String("descriptor", "", "the identity descriptor `file` the DB build writes, identity/<schema>.json")
	databaseURL := fs.String("database-url", "", "the database `URL`; defaults to $DATABASE_URL")
	dialect := fs.String("dialect", "", "postgres or sqlite, in place of the dialect the URL selects")
	login := fs.String("login", "", "the administrator's login, a value of the User table's login scalar")
	name := fs.String("name", "", "the administrator's display name, for a User table that names a name field; the login when empty")
	role := fs.String("role", "admin", "the name of the role the administrator holds")
	configPath := fs.String("config", "", "an identity config `file` whose password.argon2 sets the hash's cost")
	var permissions repeated
	fs.Var(&permissions, "permission", "a permission the role carries (repeatable; at least one)")
	if err := fs.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return bootstrapInputs{}, err
		}
		return bootstrapInputs{}, usageErrorf("%v", err)
	}
	if fs.NArg() > 0 {
		return bootstrapInputs{}, usageErrorf("bootstrap takes no arguments; got %q", fs.Args())
	}

	in := bootstrapInputs{login: *login, name: *name, role: *role}
	switch {
	case *descriptorPath == "":
		return in, usageErrorf("bootstrap needs --descriptor")
	case strings.TrimSpace(in.login) == "":
		return in, usageErrorf("bootstrap needs --login")
	case strings.TrimSpace(in.role) == "":
		return in, usageErrorf("--role is empty")
	case len(permissions) == 0:
		return in, usageErrorf("bootstrap needs a --permission")
	}
	var malformed []string
	for _, p := range permissions {
		switch {
		case !identity.ValidPermission(p):
			malformed = append(malformed, fmt.Sprintf("%q", p))
		case !slices.Contains(in.permissions, p):
			in.permissions = append(in.permissions, p)
		}
	}
	if len(malformed) > 0 {
		return in, usageErrorf("--permission %s: a permission is dotted segments of letters, digits, _ and -, such as orders.read", strings.Join(malformed, ", "))
	}
	in.databaseURL = *databaseURL
	if in.databaseURL == "" {
		in.databaseURL = o.getenv("DATABASE_URL")
	}
	if in.databaseURL == "" {
		return in, usageErrorf("no database: pass --database-url or set DATABASE_URL")
	}
	switch d := identity.Dialect(*dialect); d {
	case "":
		in.dialect = urlDialect(in.databaseURL)
	case identity.Postgres, identity.SQLite:
		in.dialect = d
	default:
		return in, usageErrorf("--dialect is postgres or sqlite; got %q", *dialect)
	}

	cfg := identity.Config{}.WithDefaults()
	if *configPath != "" {
		data, err := os.ReadFile(*configPath)
		if err != nil {
			return in, fmt.Errorf("--config: %w", err)
		}
		if cfg, err = identity.ParseConfig(data); err != nil {
			return in, fmt.Errorf("--config %s: %w", *configPath, err)
		}
	}
	in.params = cfg.Argon2Params()

	raw, err := os.ReadFile(*descriptorPath)
	if err != nil {
		return in, fmt.Errorf("--descriptor: %w", err)
	}
	if in.descriptor, err = identity.ParseDescriptor(raw); err != nil {
		return in, fmt.Errorf("--descriptor %s: %w", *descriptorPath, err)
	}
	in.raw = raw
	user := in.descriptor.User
	if in.descriptor.Role == nil {
		return in, errors.New("the descriptor names no UserRole table, so there is no role to grant: give one DB table of the schema the UserRole trait (implements UserRole)")
	}
	if in.name != "" && user.Columns.Name == user.Columns.Login {
		return in, fmt.Errorf("--name: the User trait of table %s names no name field, so a user's name is their login", user.Type)
	}
	return in, nil
}

// urlDialect is the dialect a database URL selects, as superschematic-migrate
// reads one: a postgres:// or postgresql:// URL is Postgres, anything else
// SQLite.
func urlDialect(url string) identity.Dialect {
	lower := strings.ToLower(url)
	if strings.HasPrefix(lower, "postgres://") || strings.HasPrefix(lower, "postgresql://") {
		return identity.Postgres
	}
	return identity.SQLite
}

// readPassword reads the administrator's password from standard input:
// twice with the echo off at a terminal, else one line. It refuses one
// outside Auth.Password's rule.
func readPassword(login string, o options) (string, error) {
	var password string
	if p := o.terminal(o.stdin, o.stderr); p != nil {
		first, err := p.Secret("Password for " + strings.TrimSpace(login) + ": ")
		if err != nil {
			return "", err
		}
		again, err := p.Secret("Repeat the password: ")
		if err != nil {
			return "", err
		}
		if string(first) != string(again) {
			return "", errors.New("the two passwords differ")
		}
		password = string(first)
	} else {
		line, err := bufio.NewReader(o.stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("read the password from standard input: %w", err)
		}
		if line == "" {
			return "", errors.New("standard input holds no password: pipe it in as one line, or run at a terminal to be asked for it")
		}
		password = strings.TrimSuffix(strings.TrimSuffix(line, "\n"), "\r")
	}
	if err := identity.CheckPassword(password); err != nil {
		return "", fmt.Errorf("the password is not an %s, 8 to 128 characters: %w", identity.PasswordScalar, err)
	}
	return password, nil
}

// openDatabase opens the database url names in dialect. A SQLite database
// must exist, and its connection enforces foreign keys and waits for a
// lock rather than failing at once.
func openDatabase(dialect identity.Dialect, url string) (*sql.DB, error) {
	if dialect == identity.Postgres {
		db, err := sql.Open("pgx", url)
		if err != nil {
			return nil, fmt.Errorf("open the database: %w", err)
		}
		return db, nil
	}
	dsn := sqliteDSN(url)
	if !strings.HasPrefix(strings.ToLower(dsn), "file:") {
		if _, err := os.Stat(dsn); err != nil {
			return nil, fmt.Errorf("no SQLite database at %s: migrate it to the schema first", dsn)
		}
	}
	db, err := sql.Open("sqlite", dsn)
	if err != nil {
		return nil, fmt.Errorf("open the database: %w", err)
	}
	// One connection, so the pragmas hold for every statement.
	db.SetMaxOpenConns(1)
	if _, err := db.Exec("PRAGMA foreign_keys = ON; PRAGMA busy_timeout = 5000"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("open the database: %w", err)
	}
	return db, nil
}

// sqliteDSN is the name the SQLite driver opens for a database URL, as
// superschematic-migrate reads one: sqlite:PATH and sqlite://PATH are PATH,
// or a file: URI when PATH carries a query; a file: URI and a path are kept
// as they are.
func sqliteDSN(url string) string {
	if len(url) < len("sqlite:") || !strings.EqualFold(url[:len("sqlite:")], "sqlite:") {
		return url
	}
	path := strings.TrimPrefix(url[len("sqlite:"):], "//")
	if strings.Contains(path, "?") {
		return "file:" + path
	}
	return path
}

// bootstrapError says why the store refused the bootstrap.
func bootstrapError(err error, in bootstrapInputs) error {
	var invalidLogin *identity.InvalidLoginError
	var invalidName *identity.InvalidNameError
	switch {
	case errors.Is(err, identity.ErrGrantExists):
		return errors.New("the database holds a role grant already, so its first administrator exists: bootstrap runs once per database, and the administration routes manage users and roles after it")
	case errors.Is(err, identity.ErrRoleNameTaken):
		return fmt.Errorf("a role named %s exists: pick another --role", in.role)
	case errors.Is(err, identity.ErrLoginTaken):
		return fmt.Errorf("a user with the login %s exists: pick another --login", in.login)
	case errors.As(err, &invalidLogin):
		return fmt.Errorf("--login %s is not a %s: %v", in.login, invalidLogin.Scalar, invalidLogin.Err)
	case errors.As(err, &invalidName):
		return fmt.Errorf("--name %s is not a %s: %v", in.name, invalidName.Scalar, invalidName.Err)
	}
	return err
}
