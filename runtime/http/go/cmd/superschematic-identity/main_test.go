package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"runtime/debug"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"github.com/parable-work/superschematic/runtime/http/go/identity"
)

// fixtureDir holds fixture-user-model-db's identity descriptor and DDL
// (runtime/http/testdata/identity), which the identity store's tests read
// too.
var fixtureDir = filepath.Join("..", "..", "..", "testdata", "identity")

// postgresURLEnv names the Postgres the bootstrap also runs against, as the
// identity store's tests do; without it the tests run on SQLite alone.
const postgresURLEnv = "SUPERSCHEMATIC_IDENTITY_TEST_DATABASE_URL"

// adminPassword is the administrator's password in the tests, which no
// output may hold.
const adminPassword = "correct horse battery staple"

// invocation is one run of the binary's commands.
type invocation struct {
	code           int
	stdout, stderr string
}

// output is everything the run wrote.
func (i invocation) output() string { return i.stdout + i.stderr }

// invoke runs the binary's command line with standard input stdin, the
// environment env, and term as the terminal when it is not nil.
func invoke(t *testing.T, stdin string, env map[string]string, term prompter, args ...string) invocation {
	t.Helper()
	var stdout, stderr bytes.Buffer
	o := options{
		stdin:  strings.NewReader(stdin),
		stdout: &stdout,
		stderr: &stderr,
		getenv: func(key string) string { return env[key] },
		terminal: func(io.Reader, io.Writer) prompter {
			if term == nil {
				return nil
			}
			return term
		},
	}
	code := runWith(context.Background(), args, o)
	t.Logf("%s %s: exit %d\n%s%s", programName, strings.Join(args, " "), code, stdout.String(), stderr.String())
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (i invocation) expect(t *testing.T, code int, out ...string) invocation {
	t.Helper()
	if i.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", i.code, code, i.stdout, i.stderr)
	}
	for _, want := range out {
		if !strings.Contains(i.output(), want) {
			t.Fatalf("the output does not say %q\nstdout: %s\nstderr: %s", want, i.stdout, i.stderr)
		}
	}
	return i
}

// noSecret fails when the run's output holds the password or a hash.
func (i invocation) noSecret(t *testing.T, passwords ...string) {
	t.Helper()
	for _, p := range passwords {
		if strings.Contains(i.output(), p) {
			t.Errorf("the password reached the output:\n%s", i.output())
		}
	}
	if strings.Contains(i.output(), "$argon2id$") {
		t.Errorf("a password hash reached the output:\n%s", i.output())
	}
}

func readFixture(t *testing.T, name string) []byte {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(fixtureDir, filepath.FromSlash(name)))
	if err != nil {
		t.Fatal(err)
	}
	return data
}

// descriptorPath is the fixture's descriptor; descriptorFile writes it,
// edited by edit, to a file of its own.
var descriptorPath = filepath.Join(fixtureDir, "fixture-user-model-db.json")

func descriptorFile(t *testing.T, edit func(map[string]any)) string {
	t.Helper()
	var d map[string]any
	if err := json.Unmarshal(readFixture(t, "fixture-user-model-db.json"), &d); err != nil {
		t.Fatal(err)
	}
	edit(d)
	data, err := json.Marshal(d)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "descriptor.json")
	if err := os.WriteFile(path, data, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// lowCostConfig writes an identity config whose argon2 cost is the least
// argon2 takes, so a test hashes and verifies quickly, and returns its path
// and the config.
func lowCostConfig(t *testing.T) (string, identity.Config) {
	t.Helper()
	const text = `{"password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	path := filepath.Join(t.TempDir(), "identity.json")
	if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := identity.ParseConfig([]byte(text))
	if err != nil {
		t.Fatal(err)
	}
	return path, cfg
}

// newSQLite creates a SQLite file holding the fixture's tables and returns
// its path.
func newSQLite(t *testing.T) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "users.sqlite")
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if _, err := db.Exec(string(readFixture(t, "sqlite/create.sql"))); err != nil {
		t.Fatal(err)
	}
	return path
}

// count counts the rows of the users, roles and grants at path.
func count(t *testing.T, path string) (users, roles, grants int) {
	t.Helper()
	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM "user"), (SELECT count(*) FROM "role"), (SELECT count(*) FROM "user_role_grant")`).Scan(&users, &roles, &grants); err != nil {
		t.Fatal(err)
	}
	return users, roles, grants
}

// checkAdministrator signs the bootstrapped administrator in through the
// identity runtime's service, as a server would, and checks what they hold
// and that they administer: identity covers identity.users.read.
func checkAdministrator(t *testing.T, dialect identity.Dialect, url string, cfg identity.Config) {
	t.Helper()
	db, err := openDatabase(dialect, url)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	store, err := identity.NewSQLStore(db, dialect, readFixture(t, "fixture-user-model-db.json"))
	if err != nil {
		t.Fatal(err)
	}
	svc, err := identity.New(store, cfg)
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	issued, err := svc.Login(ctx, identity.LoginInput{Login: "ALICE@example.com", Password: adminPassword})
	if err != nil {
		t.Fatalf("the bootstrapped administrator cannot sign in: %v", err)
	}
	p, err := svc.AuthenticateToken(ctx, issued.Token, identity.TransportBearer)
	if err != nil {
		t.Fatal(err)
	}
	if p.Login != "alice@example.com" || p.Name != "Alice Admin" || len(p.Roles) != 1 || p.Roles[0].Name != "admin" ||
		!slices.Equal(p.Permissions, []string{"identity", "orders.read"}) {
		t.Errorf("the administrator signs in as %+v", p)
	}
	users, err := svc.ListUsers(ctx, p)
	if err != nil || len(users) != 1 || users[0].ID != p.ID {
		t.Errorf("the administrator lists the users %+v, %v", users, err)
	}
}

// adminArgs are the flags of the tests' administrator on url, with the
// low-cost config at config.
func adminArgs(url, config string) []string {
	return []string{
		"bootstrap", "--descriptor", descriptorPath, "--database-url", url, "--config", config,
		"--login", " Alice@Example.com ", "--name", "Alice Admin",
		"--permission", "identity", "--permission", "orders.read", "--permission", "identity",
	}
}

func TestUsage(t *testing.T) {
	invoke(t, "", nil, nil).expect(t, exitUsage, "usage:")
	invoke(t, "", nil, nil, "grant").expect(t, exitUsage, `unknown command "grant"`)
	invoke(t, "", nil, nil, "help").expect(t, exitOK, "superschematic-identity bootstrap --descriptor")
	invoke(t, "", nil, nil, "bootstrap", "-h").expect(t, exitOK, "-permission")
}

func TestVersion(t *testing.T) {
	invoke(t, "", nil, nil, "version").expect(t, exitOK, "superschematic-identity ")
	if got := binaryVersion("1.2.3", nil); got != "1.2.3" {
		t.Errorf("a stamped binary reports %s", got)
	}
	if got := binaryVersion("", &debug.BuildInfo{Main: debug.Module{Version: "v0.4.0"}}); got != "0.4.0" {
		t.Errorf("an installed binary reports %s", got)
	}
	if got := binaryVersion("", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}); got != "(devel)" {
		t.Errorf("a checkout build reports %s", got)
	}
}

// TestBootstrapOnSQLite: bootstrap creates the role, the user and the
// grant, prints them without the password, and the user signs in through
// the identity runtime and administers. A second run creates nothing.
func TestBootstrapOnSQLite(t *testing.T) {
	config, cfg := lowCostConfig(t)
	path := newSQLite(t)
	run := invoke(t, adminPassword+"\n", nil, nil, adminArgs("sqlite:"+path, config)...).
		expect(t, exitOK, "created role admin (", ") with identity, orders.read\n",
			"created user alice@example.com (", ", named Alice Admin, who holds role admin\n")
	if run.stderr != "" {
		t.Errorf("stderr: %s", run.stderr)
	}
	run.noSecret(t, adminPassword)
	checkAdministrator(t, identity.SQLite, path, cfg)

	again := invoke(t, "another password\n", nil, nil, append(adminArgs("sqlite:"+path, config), "--login", "bob@example.com", "--role", "second")...).
		expect(t, exitFailed, "holds a role grant already")
	again.noSecret(t, "another password")
	if users, roles, grants := count(t, path); users != 1 || roles != 1 || grants != 1 {
		t.Errorf("after a second bootstrap: %d users, %d roles, %d grants", users, roles, grants)
	}
}

// TestBootstrapDefaults: the role is admin and the cost the runtime's
// default; $DATABASE_URL names the database without --database-url; and
// --dialect overrides the dialect the URL selects.
func TestBootstrapDefaults(t *testing.T) {
	path := newSQLite(t)
	invoke(t, adminPassword+"\n", map[string]string{"DATABASE_URL": path}, nil,
		"bootstrap", "--descriptor", descriptorPath, "--dialect", "sqlite",
		"--login", "alice@example.com", "--name", "Alice Admin", "--permission", "identity", "--permission", "orders.read").
		expect(t, exitOK, "created role admin (").noSecret(t, adminPassword)
	checkAdministrator(t, identity.SQLite, path, identity.Config{})

	db, err := sql.Open("sqlite", path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = db.Close() }()
	var hash string
	if err := db.QueryRow(`SELECT "password_hash" FROM "user_credential"`).Scan(&hash); err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(hash, "$argon2id$v=19$m=19456,t=2,p=1$") {
		t.Errorf("the hash %q is not at the runtime's default cost", hash)
	}
}

// fakeTerminal answers each prompt with the next value.
type fakeTerminal struct {
	answers []string
	prompts []string
}

func (f *fakeTerminal) Secret(prompt string) ([]byte, error) {
	f.prompts = append(f.prompts, prompt)
	if len(f.answers) == 0 {
		return nil, errors.New("no more answers")
	}
	answer := f.answers[0]
	f.answers = f.answers[1:]
	return []byte(answer), nil
}

// TestBootstrapAtATerminal: at a terminal the password is asked for twice,
// and two that differ create nothing.
func TestBootstrapAtATerminal(t *testing.T) {
	config, cfg := lowCostConfig(t)
	path := newSQLite(t)
	args := adminArgs(path, config)

	term := &fakeTerminal{answers: []string{adminPassword, adminPassword + "!"}}
	invoke(t, "", nil, term, args...).expect(t, exitFailed, "the two passwords differ").noSecret(t, adminPassword)
	if users, _, _ := count(t, path); users != 0 {
		t.Fatalf("two passwords that differ created %d users", users)
	}

	term = &fakeTerminal{answers: []string{adminPassword, adminPassword}}
	invoke(t, "", nil, term, args...).expect(t, exitOK, "created user alice@example.com").noSecret(t, adminPassword)
	if want := []string{"Password for Alice@Example.com: ", "Repeat the password: "}; !slices.Equal(term.prompts, want) {
		t.Errorf("prompts %q, want %q", term.prompts, want)
	}
	checkAdministrator(t, identity.SQLite, path, cfg)
}

// TestBootstrapRefuses: bootstrap refuses, writing nothing and printing no
// password, flags it cannot run with (exit 2), and a password outside
// Auth.Password's rule, standard input without one, a descriptor without a
// UserRole table, a --name for a User trait without a name field, a login
// the scalar refuses and a database that does not exist (exit 1).
func TestBootstrapRefuses(t *testing.T) {
	config, _ := lowCostConfig(t)
	path := newSQLite(t)
	common := []string{"--database-url", path, "--config", config}
	noRoles := descriptorFile(t, func(d map[string]any) {
		delete(d, "role")
		delete(d, "roleGrant")
	})
	loginIsName := descriptorFile(t, func(d map[string]any) {
		d["user"].(map[string]any)["columns"].(map[string]any)["name"] = "email"
	})
	const shortPassword = "pw12345"
	cases := map[string]struct {
		stdin string
		args  []string
		code  int
		want  string
	}{
		"a malformed permission": {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "orders read", "--permission", "identity", "--permission", ".x"},
			exitUsage, `--permission "orders read", ".x": a permission is dotted segments`},
		"no descriptor":      {adminPassword + "\n", []string{"--login", "alice@example.com", "--permission", "identity"}, exitUsage, "bootstrap needs --descriptor"},
		"no login":           {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--permission", "identity"}, exitUsage, "bootstrap needs --login"},
		"no permission":      {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com"}, exitUsage, "bootstrap needs a --permission"},
		"an empty role":      {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity", "--role", " "}, exitUsage, "--role is empty"},
		"an unknown dialect": {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity", "--dialect", "mysql"}, exitUsage, `--dialect is postgres or sqlite; got "mysql"`},
		"an argument":        {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity", "extra"}, exitUsage, "bootstrap takes no arguments"},
		"an unknown flag":    {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--password", "x"}, exitUsage, "flag provided but not defined: -password"},
		"a short password": {shortPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity"},
			exitFailed, "the password is not an Auth.Password, 8 to 128 characters"},
		"an empty password": {"\n", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity"},
			exitFailed, "the password is not an Auth.Password"},
		"no standard input": {"", []string{"--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity"},
			exitFailed, "standard input holds no password"},
		"no UserRole table": {adminPassword + "\n", []string{"--descriptor", noRoles, "--login", "alice@example.com", "--permission", "identity"},
			exitFailed, "the descriptor names no UserRole table, so there is no role to grant"},
		"a name without a name field": {adminPassword + "\n", []string{"--descriptor", loginIsName, "--login", "alice@example.com", "--permission", "identity", "--name", "Alice"},
			exitFailed, "--name: the User trait of table User names no name field"},
		"a login the scalar refuses": {adminPassword + "\n", []string{"--descriptor", descriptorPath, "--login", "alice", "--permission", "identity"},
			exitFailed, "--login alice is not a Contact.Email"},
		"a missing descriptor": {adminPassword + "\n", []string{"--descriptor", filepath.Join(t.TempDir(), "none.json"), "--login", "alice@example.com", "--permission", "identity"},
			exitFailed, "--descriptor: open"},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			args := append(append([]string{"bootstrap"}, c.args...), common...)
			run := invoke(t, c.stdin, nil, nil, args...).expect(t, c.code, c.want)
			if run.stdout != "" {
				t.Errorf("a refused bootstrap wrote %q", run.stdout)
			}
			run.noSecret(t, adminPassword, shortPassword)
		})
	}
	invoke(t, adminPassword+"\n", nil, nil, "bootstrap", "--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity").
		expect(t, exitUsage, "no database: pass --database-url or set DATABASE_URL")
	invoke(t, adminPassword+"\n", nil, nil, "bootstrap", "--descriptor", descriptorPath, "--login", "alice@example.com", "--permission", "identity",
		"--database-url", "sqlite:"+filepath.Join(t.TempDir(), "missing.sqlite"), "--config", config).
		expect(t, exitFailed, "no SQLite database at")
	if users, roles, grants := count(t, path); users+roles+grants != 0 {
		t.Errorf("refused bootstraps wrote %d users, %d roles and %d grants", users, roles, grants)
	}
}

// TestBootstrapOnPostgres runs the bootstrap against the Postgres
// postgresURLEnv names, in a schema of its own, and signs the user in
// through the identity runtime.
func TestBootstrapOnPostgres(t *testing.T) {
	base := os.Getenv(postgresURLEnv)
	if base == "" {
		t.Skipf("%s is unset: the bootstrap runs on SQLite alone", postgresURLEnv)
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatalf("connect to %s: %v", postgresURLEnv, err)
	}
	t.Cleanup(func() { _ = admin.Close(context.Background()) })
	// An extension's name is unique in the database, so the DDL's are
	// created once in public, where every schema's search path finds them.
	for _, ext := range []string{"pgcrypto", "citext"} {
		if _, err := admin.Exec(ctx, "CREATE EXTENSION IF NOT EXISTS "+ext+" SCHEMA public"); err != nil && !strings.Contains(err.Error(), "23505") {
			t.Fatalf("create %s: %v", ext, err)
		}
	}
	schema := fmt.Sprintf("identity_bootstrap_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })

	u, err := url.Parse(base)
	if err != nil {
		t.Fatal(err)
	}
	query := u.Query()
	query.Set("search_path", schema+",public")
	u.RawQuery = query.Encode()
	dbURL := u.String()
	db, err := openDatabase(identity.Postgres, dbURL)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.Exec(string(readFixture(t, "create.sql"))); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	_ = db.Close()

	config, cfg := lowCostConfig(t)
	invoke(t, adminPassword+"\n", nil, nil, adminArgs(dbURL, config)...).
		expect(t, exitOK, "created user alice@example.com").noSecret(t, adminPassword)
	checkAdministrator(t, identity.Postgres, dbURL, cfg)
	invoke(t, adminPassword+"\n", nil, nil, append(adminArgs(dbURL, config), "--login", "bob@example.com")...).
		expect(t, exitFailed, "holds a role grant already")
}
