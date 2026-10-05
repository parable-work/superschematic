package main

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// invocation is one run of the binary's commands.
type invocation struct {
	code           int
	stdout, stderr string
}

func invoke(t *testing.T, env map[string]string, args ...string) invocation {
	t.Helper()
	var stdout, stderr bytes.Buffer
	code := runWith(context.Background(), args, options{
		stdout: &stdout,
		stderr: &stderr,
		getenv: func(key string) string { return env[key] },
		waits:  []time.Duration{time.Millisecond},
	})
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

// TestCommands runs apply, status and adopt end to end on SQLite, through a
// path, a sqlite: URL and a file: URI to one file, and on Postgres.
func TestCommands(t *testing.T) {
	for _, dialect := range testdb.Dialects {
		t.Run(string(dialect), func(t *testing.T) {
			url := testdb.New(t, dialect)
			urls := []string{url, url, url}
			if dialect == migrate.SQLite {
				urls = []string{url, "sqlite://" + url, "file:" + url + "?mode=rw"}
			}
			create, evolve := readFixture(t, dialect, "01-create"), readFixture(t, dialect, "02-evolve")
			env := map[string]string{"DATABASE_URL": urls[1]}

			invoke(t, env, "status", "--service", "shop").expect(t, exitOK, "no state recorded")
			invoke(t, env, "status", "--service", "shop", "--model").expect(t, exitFailed, "has no applied model")

			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "01-create"), "--database-url", urls[0]).
				expect(t, exitOK, "step 1/", "plan "+create.Hash+" applied")
			invoke(t, env, "apply", "--plan", fixturePath(dialect, "01-create")).
				expect(t, exitOK, "already applied")

			invoke(t, env, "apply", "--plan", fixturePath(dialect, "02-evolve"), "--phase", "expand").
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

			invoke(t, env, "apply", "--plan", fixturePath(dialect, "02-evolve"), "--phase", "contract").
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
	for _, dialect := range testdb.Dialects {
		t.Run(string(dialect), func(t *testing.T) {
			url := testdb.New(t, dialect)
			other := migrate.Postgres
			if dialect == migrate.Postgres {
				other = migrate.SQLite
			}
			invoke(t, nil, "apply", "--plan", fixturePath(other, "01-create"), "--database-url", url).
				expect(t, exitFailed, "the plan is for "+string(other)+" and the database URL selects "+string(dialect))
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "02-evolve"), "--database-url", url).
				expect(t, exitFailed, "superschematic-migrate status --model")
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "02-evolve"), "--phase", "contract", "--database-url", url).
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
			testdb.Exec(t, url, "CREATE TABLE customer (id INTEGER)")
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "01-create"), "--database-url", url).
				expect(t, exitFailed, "step 1 (table/customer) failed", "statement:\nCREATE TABLE customer")
			invoke(t, nil, "status", "--service", "shop", "--database-url", url).
				expect(t, exitOK, "plan in progress: "+readFixture(t, dialect, "01-create").Hash)
		})
	}
}
