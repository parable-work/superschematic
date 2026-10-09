package cli

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/permcatalog"
)

// The fixture the bootstrap tests run on: fixture-user-model-db, whose User
// table logs in with a Contact.Email and whose Role table has the UserRole
// trait, and its identity descriptor and DDL as the identity runtime's
// tests read them (TestIdentityStoreFixtureIsCurrent keeps them current).
const (
	userModelDB        = "fixture-user-model-db"
	identityFixtureDir = "../runtime/http/testdata/identity"
)

// The administrator's password in the tests.
const adminPassword = "correct horse battery staple"

// bootstrapRun is one identity bootstrap's outputs.
type bootstrapRun struct {
	stdout, stderr string
	err            error
}

// runBootstrap runs identity bootstrap with args, standard input stdin.
func runBootstrap(t *testing.T, stdin string, args ...string) bootstrapRun {
	t.Helper()
	var stdout, stderr bytes.Buffer
	root := New(Config{})
	root.SetIn(strings.NewReader(stdin))
	root.SetOut(&stdout)
	root.SetErr(&stderr)
	root.SetArgs(append([]string{"identity", "bootstrap"}, args...))
	err := root.Execute()
	return bootstrapRun{stdout: stdout.String(), stderr: stderr.String(), err: err}
}

// stubRunner is a superschematic-identity that records what bootstrap hands
// it: its arguments, one per line, the descriptor it is given and its
// standard input, in dir. It writes a line to standard output and one to
// standard error, and exits with $STUB_EXIT, 0 by default.
type stubRunner struct{ dir string }

const stubScript = `#!/bin/sh
dir="$(dirname "$0")"
: > "$dir/args"
prev=
for arg in "$@"; do
  printf '%s\n' "$arg" >> "$dir/args"
  if [ "$prev" = --descriptor ]; then cp "$arg" "$dir/descriptor.json"; fi
  prev="$arg"
done
cat > "$dir/stdin"
echo "runner out"
echo "runner err" >&2
exit "${STUB_EXIT:-0}"
`

// newStubRunner writes the stub into a directory of its own and puts it on
// PATH, with SUPERSCHEMATIC_IDENTITY unset.
func newStubRunner(t *testing.T) stubRunner {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("the stub runner is a shell script")
	}
	dir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(dir, IdentityBinary), []byte(stubScript), 0o755))
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv(IdentityEnv, "")
	return stubRunner{dir: dir}
}

// ran reports whether the stub ran.
func (s stubRunner) ran() bool {
	_, err := os.Stat(filepath.Join(s.dir, "args"))
	return err == nil
}

func (s stubRunner) read(t *testing.T, name string) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(s.dir, name))
	require.NoError(t, err)
	return string(data)
}

// args are the arguments the stub ran with.
func (s stubRunner) args(t *testing.T) []string {
	t.Helper()
	return strings.Split(strings.TrimSuffix(s.read(t, "args"), "\n"), "\n")
}

// userModelVariant copies fixture-user-model-db into a schemas root of its
// own, with its schema file's text edited by edit unless edit is nil, and
// returns the service directory.
func userModelVariant(t *testing.T, edit func(string) string) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "schemas", "services", userModelDB)
	copyDir(t, filepath.Join(tsreaderTestdata, userModelDB), dir)
	if edit == nil {
		return dir
	}
	schemaFile := filepath.Join(dir, "src", "users.schema.json")
	text, err := os.ReadFile(schemaFile)
	require.NoError(t, err)
	edited := edit(string(text))
	require.NotEqual(t, string(text), edited, "the edit changed nothing")
	require.NoError(t, os.WriteFile(schemaFile, []byte(edited), 0o644))
	return dir
}

// TestIdentityBootstrapHandsOffToTheRunner: bootstrap builds the service's
// identity descriptor, the one the build writes, and runs the runner on
// PATH with it, the flags it was given but --naming, and its standard
// input, output and error. The descriptor's file is gone afterwards.
func TestIdentityBootstrapHandsOffToTheRunner(t *testing.T) {
	stub := newStubRunner(t)
	namingFile := filepath.Join(t.TempDir(), "superschematic.toml")
	require.NoError(t, os.WriteFile(namingFile, nil, 0o644))
	run := runBootstrap(t, adminPassword+"\n", filepath.Join(tsreaderTestdata, userModelDB),
		"--database-url", "sqlite:users.sqlite", "--dialect", "sqlite", "--config", "identity.json",
		"--login", "alice@example.com", "--name", "Alice Admin", "--permission", "identity", "--permission", "orders.read",
		"--naming", namingFile)
	require.NoError(t, run.err, run.stderr)
	assert.Equal(t, "runner out\n", run.stdout)
	assert.Equal(t, "runner err\n", run.stderr)

	args := stub.args(t)
	require.Len(t, args, 17)
	descriptor := args[2]
	assert.Equal(t, []string{
		"bootstrap", "--descriptor", descriptor, "--database-url", "sqlite:users.sqlite", "--dialect", "sqlite",
		"--login", "alice@example.com", "--name", "Alice Admin", "--config", "identity.json",
		"--permission", "identity", "--permission", "orders.read",
	}, args, "the runner takes the flags given but --naming, and the role's default is its own")
	want, err := os.ReadFile(filepath.Join(identityFixtureDir, userModelDB+".json"))
	require.NoError(t, err)
	assert.Equal(t, string(want), stub.read(t, "descriptor.json"), "the runner read another descriptor than the build writes")
	assert.Equal(t, adminPassword+"\n", stub.read(t, "stdin"))
	_, err = os.Stat(descriptor)
	assert.True(t, errors.Is(err, os.ErrNotExist), "the descriptor's file outlived the run: %v", err)
}

// TestIdentityBootstrapPassesADescriptorThrough: with --descriptor nothing
// loads, and the runner reads the file named.
func TestIdentityBootstrapPassesADescriptorThrough(t *testing.T) {
	stub := newStubRunner(t)
	descriptor := filepath.Join(identityFixtureDir, userModelDB+".json")
	run := runBootstrap(t, adminPassword+"\n", "--descriptor", descriptor, "--login", "alice@example.com", "--role", "owner", "--permission", "identity")
	require.NoError(t, run.err, run.stderr)
	assert.Equal(t, []string{"bootstrap", "--descriptor", descriptor, "--login", "alice@example.com", "--role", "owner", "--permission", "identity"}, stub.args(t))
}

// TestIdentityBootstrapExitsWithTheRunnersStatus: a runner that refuses
// has said why on standard error; bootstrap returns an *ExitError with its
// status, which Exit exits with and prints nothing more for.
func TestIdentityBootstrapExitsWithTheRunnersStatus(t *testing.T) {
	newStubRunner(t)
	for _, code := range []int{1, 2} {
		t.Setenv("STUB_EXIT", strconv.Itoa(code))
		run := runBootstrap(t, adminPassword+"\n", filepath.Join(tsreaderTestdata, userModelDB), "--login", "alice@example.com", "--permission", "identity")
		var exit *ExitError
		require.True(t, errors.As(run.err, &exit), "the error is %v, not an *ExitError", run.err)
		assert.Equal(t, code, exit.Code)
		assert.Contains(t, exit.Error(), "superschematic-identity exited with status")
		assert.Equal(t, "runner err\n", run.stderr)
	}
}

// TestIdentityBootstrapFindsTheRunner: $SUPERSCHEMATIC_IDENTITY names the
// runner off PATH, and without either bootstrap says how to install it,
// before it loads anything.
func TestIdentityBootstrapFindsTheRunner(t *testing.T) {
	stub := newStubRunner(t)
	// The system's directories alone, which the stub's commands need.
	t.Setenv("PATH", "/usr/bin:/bin")
	t.Setenv(IdentityEnv, filepath.Join(stub.dir, IdentityBinary))
	run := runBootstrap(t, adminPassword+"\n", filepath.Join(tsreaderTestdata, userModelDB), "--login", "alice@example.com", "--permission", "identity")
	require.NoError(t, run.err, run.stderr)
	assert.True(t, stub.ran())

	t.Setenv(IdentityEnv, "")
	missing := runBootstrap(t, adminPassword+"\n", filepath.Join(t.TempDir(), "no-such-service"), "--login", "alice@example.com", "--permission", "identity")
	require.Error(t, missing.err)
	assert.Contains(t, missing.err.Error(), "superschematic-identity is not on PATH, and SUPERSCHEMATIC_IDENTITY names no runner")
	assert.Contains(t, missing.err.Error(), "go build -o")
}

// TestIdentityBootstrapRefusesBeforeTheRunner: what the compiler can tell
// from the flags and the schema is refused without running the runner.
func TestIdentityBootstrapRefusesBeforeTheRunner(t *testing.T) {
	noRoles := userModelVariant(t, func(text string) string {
		start := strings.Index(text, `    "Role": {`)
		require.Positive(t, start)
		return strings.TrimRight(text[:start], " \n,") + "\n  }\n}\n"
	})
	fixture := filepath.Join(tsreaderTestdata, userModelDB)
	cases := map[string]struct {
		args []string
		want string
	}{
		"no tables":         {[]string{"--login", "a@example.com", "--permission", "identity"}, "name the DB service directory, or the identity descriptor with --descriptor"},
		"both tables":       {[]string{fixture, "--descriptor", "x.json", "--login", "a@example.com", "--permission", "identity"}, "a service directory and --descriptor both name the tables"},
		"no UserRole table": {[]string{noRoles, "--login", "a@example.com", "--permission", "identity"}, "fixture-user-model-db has no UserRole table, so there is no role to grant"},
		"no service":        {[]string{filepath.Join(t.TempDir(), "absent"), "--login", "a@example.com", "--permission", "identity"}, "service directory not found"},
		"no permission":     {[]string{fixture, "--login", "a@example.com"}, `required flag(s) "permission" not set`},
		"no login":          {[]string{fixture, "--permission", "identity"}, `required flag(s) "login" not set`},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			stub := newStubRunner(t)
			run := runBootstrap(t, adminPassword+"\n", c.args...)
			require.Error(t, run.err)
			assert.Contains(t, run.err.Error(), c.want)
			assert.False(t, stub.ran(), "the runner ran")
		})
	}
}

// TestIdentityBootstrapWarnsOfUncataloguedPermissions: with the catalogs a
// build wrote, a permission no API whose authDb is the service checks, and
// that covers none of their permissions, draws a warning before the runner
// runs and is handed to it all the same; a catalog of another authDb does
// not count.
func TestIdentityBootstrapWarnsOfUncataloguedPermissions(t *testing.T) {
	stub := newStubRunner(t)
	service := userModelVariant(t, nil)
	dist := filepath.Join(filepath.Dir(filepath.Dir(service)), "dist", "api")
	writeCatalog := func(api, authDB string, ops ...permcatalog.Operation) {
		catalog, ok := permcatalog.Build(api, authDB, ops)
		require.True(t, ok)
		data, err := catalog.JSON()
		require.NoError(t, err)
		require.NoError(t, os.MkdirAll(filepath.Join(dist, api), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(dist, api, permcatalog.FileName), data, 0o644))
	}
	writeCatalog("users-api", userModelDB,
		permcatalog.Operation{ID: "AdminListUsersHandler", Permissions: []string{"identity.users.read"}, Identity: true},
		permcatalog.Operation{ID: "OrdersListHandler", Permissions: []string{"orders.read"}})
	writeCatalog("other-api", "other-db", permcatalog.Operation{ID: "ReportsHandler", Permissions: []string{"reports.read"}})

	run := runBootstrap(t, adminPassword+"\n", service, "--login", "alice@example.com",
		"--permission", "identity", "--permission", "orders", "--permission", "orders.read",
		"--permission", "reports.read", "--permission", "orders.reed")
	require.NoError(t, run.err, run.stderr)
	assert.Equal(t,
		"identity bootstrap: warning: --permission reports.read is no permission the operations of users-api check, and covers none; the role carries it all the same\n"+
			"identity bootstrap: warning: --permission orders.reed is no permission the operations of users-api check, and covers none; the role carries it all the same\n"+
			"runner err\n",
		run.stderr)
	assert.Equal(t, []string{"identity", "orders", "orders.read", "reports.read", "orders.reed"}, permissionArgs(stub.args(t)))
}

// permissionArgs are the values of the --permission flags in args.
func permissionArgs(args []string) []string {
	var out []string
	for i, arg := range args {
		if arg == "--permission" && i+1 < len(args) {
			out = append(out, args[i+1])
		}
	}
	return out
}

// TestIdentityBootstrapRunsTheRunner builds superschematic-identity from
// runtime/http/go and runs bootstrap through it: a refusal's message and
// status reach the caller, and on a SQLite database it creates the
// administrator and prints them without the password. The database tests
// proper are the runner's (runtime/http/go/cmd/superschematic-identity).
func TestIdentityBootstrapRunsTheRunner(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: builds superschematic-identity")
	}
	runner := filepath.Join(t.TempDir(), IdentityBinary)
	build := exec.Command("go", "build", "-o", runner, "./cmd/"+IdentityBinary)
	build.Dir = filepath.Join("..", "runtime", "http", "go")
	build.Env = append(os.Environ(), "GOWORK=off")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Setenv(IdentityEnv, runner)
	fixture := filepath.Join(tsreaderTestdata, userModelDB)

	short := runBootstrap(t, "pw12345\n", fixture, "--database-url", "sqlite:"+filepath.Join(t.TempDir(), "x.sqlite"), "--login", "alice@example.com", "--permission", "identity")
	var exit *ExitError
	require.True(t, errors.As(short.err, &exit), "the error is %v", short.err)
	assert.Equal(t, 1, exit.Code)
	assert.Contains(t, short.stderr, "superschematic-identity: the password is not an Auth.Password")
	malformed := runBootstrap(t, adminPassword+"\n", fixture, "--database-url", "sqlite:x.sqlite", "--login", "alice@example.com", "--permission", "orders read")
	require.True(t, errors.As(malformed.err, &exit), "the error is %v", malformed.err)
	assert.Equal(t, 2, exit.Code)

	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 is not on PATH to create the database")
	}
	dbPath := filepath.Join(t.TempDir(), "users.sqlite")
	create := exec.Command(sqlite3, dbPath)
	create.Stdin, err = os.Open(filepath.Join(identityFixtureDir, "sqlite", "create.sql"))
	require.NoError(t, err)
	out, err = create.CombinedOutput()
	require.NoError(t, err, string(out))
	run := runBootstrap(t, adminPassword+"\n", fixture, "--database-url", "sqlite:"+dbPath,
		"--login", "Alice@Example.com", "--name", "Alice Admin", "--permission", "identity", "--permission", "orders.read")
	require.NoError(t, run.err, run.stderr)
	assert.Contains(t, run.stdout, "created role admin (")
	assert.Contains(t, run.stdout, "created user alice@example.com (")
	for _, output := range []string{run.stdout, run.stderr} {
		assert.NotContains(t, output, adminPassword)
		assert.NotContains(t, output, "$argon2id$")
	}
	rows, err := exec.Command(sqlite3, dbPath, `SELECT u.email, r.name, r.permissions FROM user_role_grant g JOIN "user" u ON u.id = g.user_id JOIN role r ON r.id = g.role_id`).Output()
	require.NoError(t, err)
	assert.Equal(t, "alice@example.com|admin|[\"identity\",\"orders.read\"]\n", string(rows))
}
