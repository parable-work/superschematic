package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"runtime/debug"
	"strings"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/d1"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// invocation is one run of the binary's commands.
type invocation struct {
	code           int
	stdout, stderr string
}

// invoke runs the binary's command line. A d1:// database URL, in
// --database-url or DATABASE_URL, reaches the fake D1 server testdb started
// for it, and CLOUDFLARE_API_TOKEN is that server's token unless env sets
// it.
func invoke(t *testing.T, env map[string]string, args ...string) invocation {
	t.Helper()
	var stdout, stderr bytes.Buffer
	o := options{
		stdout: &stdout,
		stderr: &stderr,
		getenv: func(key string) string {
			if value, ok := env[key]; ok || key != d1.TokenEnv {
				return value
			}
			return testdb.D1Token
		},
		waits: []time.Duration{time.Millisecond},
	}
	dbURL := env["DATABASE_URL"]
	for i, arg := range args {
		if arg == "--database-url" && i+1 < len(args) {
			dbURL = args[i+1]
		}
	}
	if d1.IsURL(dbURL) {
		o.d1 = testdb.D1Options(t, dbURL)
	}
	code := runWith(context.Background(), args, o)
	t.Logf("superschematic-migrate %s: exit %d\n%s%s", strings.Join(args, " "), code, stdout.String(), stderr.String())
	return invocation{code: code, stdout: stdout.String(), stderr: stderr.String()}
}

func (i invocation) expect(t *testing.T, code int, out ...string) invocation {
	t.Helper()
	if i.code != code {
		t.Fatalf("exit %d, want %d\nstdout: %s\nstderr: %s", i.code, code, i.stdout, i.stderr)
	}
	for _, want := range out {
		if !strings.Contains(i.stdout+i.stderr, want) {
			t.Fatalf("the output does not say %q\nstdout: %s\nstderr: %s", want, i.stdout, i.stderr)
		}
	}
	return i
}

func fixturePath(dialect migrate.Dialect, name string) string {
	return filepath.Join("..", "..", "testdata", string(dialect), name+".plan.json")
}

// evolveFixture names the chain's second fixture on db: on D1, which cannot
// run 02-evolve's rebuild with foreign keys off, evolve-fk-on, the same
// models with the rebuild D27's amendment makes.
func evolveFixture(db testdb.Backend) string {
	if db == testdb.D1 {
		return "evolve-fk-on"
	}
	return "02-evolve"
}

func readFixture(t *testing.T, dialect migrate.Dialect, name string) *migrate.Plan {
	t.Helper()
	doc, err := os.ReadFile(fixturePath(dialect, name))
	if err != nil {
		t.Fatal(err)
	}
	plan, err := migrate.ReadPlan(doc)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

// TestUsage: a command line the binary cannot run is exit code 2, and help
// is 0.
func TestUsage(t *testing.T) {
	plan := fixturePath(migrate.SQLite, "01-create")
	db := filepath.Join(t.TempDir(), "app.db")
	for _, c := range []struct {
		args []string
		code int
		say  string
	}{
		{nil, exitUsage, "usage:"},
		{[]string{"migrate"}, exitUsage, `unknown command "migrate"`},
		{[]string{"apply"}, exitUsage, "apply needs --plan"},
		{[]string{"apply", "--plan", plan, "--phase", "sideways", "--database-url", db}, exitUsage, "--phase is expand, contract or all"},
		{[]string{"apply", "--plan", plan}, exitUsage, "pass --database-url or set DATABASE_URL"},
		{[]string{"apply", "--plan", plan, "--database-url", db, "extra"}, exitUsage, "takes no arguments"},
		{[]string{"apply", "--unknown"}, exitUsage, "flag provided but not defined"},
		{[]string{"status", "--database-url", db}, exitUsage, "status needs --service"},
		{[]string{"adopt", "--database-url", db}, exitUsage, "adopt needs --model"},
		{[]string{"help"}, exitOK, "usage:"},
		{[]string{"apply", "-h"}, exitOK, "-plan"},
	} {
		invoke(t, nil, c.args...).expect(t, c.code, c.say)
	}
	if _, err := os.Stat(db); !os.IsNotExist(err) {
		t.Fatalf("a usage error opened the database: %v", err)
	}
}

// TestVersion: version prints the version a release stamps, else the module
// version go install recorded, else (devel).
func TestVersion(t *testing.T) {
	stamped := version
	t.Cleanup(func() { version = stamped })
	version = "0.1.0-alpha.1"
	for _, arg := range []string{"version", "-version", "--version"} {
		got := invoke(t, nil, arg).expect(t, exitOK)
		if got.stdout != "superschematic-migrate 0.1.0-alpha.1\n" || got.stderr != "" {
			t.Fatalf("%s printed %q and %q", arg, got.stdout, got.stderr)
		}
	}
	installed := &debug.BuildInfo{Main: debug.Module{Version: "v0.1.0-alpha.1"}}
	for _, c := range []struct {
		stamped string
		info    *debug.BuildInfo
		want    string
	}{
		{"0.1.0", installed, "0.1.0"},
		{"", installed, "0.1.0-alpha.1"},
		{"", &debug.BuildInfo{Main: debug.Module{Version: "(devel)"}}, "(devel)"},
		{"", nil, "(devel)"},
	} {
		if got := binaryVersion(c.stamped, c.info); got != c.want {
			t.Errorf("binaryVersion(%q, %v) = %q, want %q", c.stamped, c.info, got, c.want)
		}
	}
}

// TestCommands runs apply, status and adopt end to end on SQLite, through a
// path, a sqlite: URL and a file: URI to one file, on Postgres, and on a
// fake D1 through a d1:// URL.
func TestCommands(t *testing.T) {
	for _, db := range testdb.Backends {
		t.Run(string(db), func(t *testing.T) {
			dialect, evolveName := db.Dialect(), evolveFixture(db)
			url := testdb.New(t, db)
			urls := []string{url, url, url}
			if db == testdb.SQLite {
				urls = []string{url, "sqlite://" + url, "file:" + url + "?mode=rw"}
			}
			create, evolve := readFixture(t, dialect, "01-create"), readFixture(t, dialect, evolveName)
			env := map[string]string{"DATABASE_URL": urls[1]}

			invoke(t, env, "status", "--service", "shop").expect(t, exitOK, "no state recorded")
			invoke(t, env, "status", "--service", "shop", "--model").expect(t, exitFailed, "has no applied model")

			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "01-create"), "--database-url", urls[0]).
				expect(t, exitOK, "step 1/", "plan "+create.Hash+" applied")
			invoke(t, env, "apply", "--plan", fixturePath(dialect, "01-create")).
				expect(t, exitOK, "already applied")

			invoke(t, env, "apply", "--plan", fixturePath(dialect, evolveName), "--phase", "expand").
				expect(t, exitOK, "expand is done")
			invoke(t, env, "status", "--service", "shop").
				expect(t, exitOK, "applied model:    "+evolve.Expanded, "plan in progress: "+evolve.Hash+" (expand done; contract pending; ",
					"steps logged:", "1 expand table/order/column/note: started ")
			// Between the phases the applied model is the plan's expanded
			// model, which a plan from it starts from.
			between := invoke(t, env, "status", "--service", "shop", "--model").expect(t, exitOK)
			if between.stdout != string(evolve.BetweenPhases().Canonical)+"\n" {
				t.Fatalf("status --model between the phases printed %q, want the plan's expanded model", between.stdout)
			}

			modelFile := filepath.Join(t.TempDir(), "model.json")
			if err := os.WriteFile(modelFile, create.ToModel, 0o644); err != nil {
				t.Fatal(err)
			}
			invoke(t, nil, "adopt", "--model", modelFile, "--database-url", urls[2]).
				expect(t, exitFailed, "in progress")

			invoke(t, env, "apply", "--plan", fixturePath(dialect, evolveName), "--phase", "contract").
				expect(t, exitOK, "applied: service shop is at model "+evolve.To)

			printed := invoke(t, env, "status", "--service", "shop", "--model").expect(t, exitOK)
			if printed.stdout != string(evolve.Model().Canonical)+"\n" {
				t.Fatalf("status --model printed %q, want the canonical model", printed.stdout)
			}
			if model, err := migrate.ReadModel([]byte(printed.stdout)); err != nil || model.Hash != evolve.To {
				t.Fatalf("the printed model hashes to %v, %v; want %s", model, err, evolve.To)
			}

			invoke(t, nil, "adopt", "--model", modelFile, "--database-url", urls[2]).
				expect(t, exitOK, "adopted model "+create.To+" for service shop; it replaces "+evolve.To)
			invoke(t, env, "status", "--service", "shop").expect(t, exitOK, "applied model:    "+create.To, "plan in progress: none")
		})
	}
}

// TestRefusalsAndFailures: a refused plan or a failed step is exit code 1,
// with the reason on stderr.
func TestRefusalsAndFailures(t *testing.T) {
	for _, db := range testdb.Backends {
		t.Run(string(db), func(t *testing.T) {
			dialect, evolveName := db.Dialect(), evolveFixture(db)
			url := testdb.New(t, db)
			other := migrate.Postgres
			if dialect == migrate.Postgres {
				other = migrate.SQLite
			}
			invoke(t, nil, "apply", "--plan", fixturePath(other, "01-create"), "--database-url", url).
				expect(t, exitFailed, "the plan is for "+string(other)+" and the database URL selects "+string(dialect))
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, evolveName), "--database-url", url).
				expect(t, exitFailed, "superschematic-migrate status --model")
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, evolveName), "--phase", "contract", "--database-url", url).
				expect(t, exitFailed, "service shop has no applied model")

			doc, err := os.ReadFile(fixturePath(dialect, "01-create"))
			if err != nil {
				t.Fatal(err)
			}
			edited := filepath.Join(t.TempDir(), "edited.plan.json")
			if err := os.WriteFile(edited, bytes.Replace(doc, []byte("CREATE TABLE customer"), []byte("CREATE TABLE client"), 1), 0o644); err != nil {
				t.Fatal(err)
			}
			invoke(t, nil, "apply", "--plan", edited, "--database-url", url).
				expect(t, exitFailed, "changed after it was written")
			invoke(t, nil, "apply", "--plan", filepath.Join(t.TempDir(), "missing.json"), "--database-url", url).
				expect(t, exitFailed, "no such file")

			// A database that already has the customer table fails step 1.
			// D1 names no statement of the batch, but the table.
			testdb.Exec(t, url, "CREATE TABLE customer (id INTEGER)")
			says := "statement:\nCREATE TABLE customer"
			if db == testdb.D1 {
				says = "table customer already exists"
			}
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "01-create"), "--database-url", url).
				expect(t, exitFailed, "step 1 (table/customer) failed", says)
			invoke(t, nil, "status", "--service", "shop", "--database-url", url).
				expect(t, exitOK, "plan in progress: "+readFixture(t, dialect, "01-create").Hash)
		})
	}
}

// TestD1Token: a d1:// database without CLOUDFLARE_API_TOKEN is a usage
// error naming the variable, and opens nothing; a wrong token fails with
// the API's error.
func TestD1Token(t *testing.T) {
	url := testdb.NewD1(t)
	plan := fixturePath(migrate.SQLite, "01-create")
	for _, args := range [][]string{
		{"apply", "--plan", plan, "--database-url", url},
		{"status", "--service", "shop", "--database-url", url},
	} {
		invoke(t, map[string]string{d1.TokenEnv: ""}, args...).expect(t, exitUsage, "set CLOUDFLARE_API_TOKEN")
	}
	invoke(t, map[string]string{d1.TokenEnv: "wrong"}, "apply", "--plan", plan, "--database-url", url).
		expect(t, exitFailed, "HTTP 403: Authentication error")
	if got := testdb.Strings(t, url, `SELECT count(*) FROM sqlite_master`); got[0] != "0" {
		t.Fatalf("a refused run created %s tables", got[0])
	}
}
