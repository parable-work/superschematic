package generator

import (
	_ "embed"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/wiring_probe_sqlite_go.go
var wiringProbeSQLite string

// TestGoServerWiresTheRuntimeOnSQLite builds fixture-agent-runs-go-api and
// its stack over fixture-agent-runs-db listing sqlite first (#717): the
// server and each of its four workers open the database's SQLite file, the
// API package's Wire, Authenticator, Routes and queues take the ORM's
// SQLiteDatabase, the indexing worker claims its queue with
// New<Queue>SQLiteQueue, and every entrypoint compiles over the
// scaffolded implementation.
func TestGoServerWiresTheRuntimeOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go server's wiring in -short mode")
	}
	repoRoot := testpaths.TempDir(t)
	outputRoot := buildWiringStackOn(t, repoRoot, "sqlite", "postgres")
	wiring, err := os.ReadFile(filepath.Join(APIDir(outputRoot, wiringAPI), "wiring.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func Wire(deps Deps, db *orm.SQLiteDatabase, wiring Wiring) (Deps, error)",
		"func Authenticator(deps Deps, db *orm.SQLiteDatabase, wiring Wiring, inner func(http.Handler) http.Handler)",
		"func Routes(deps Deps, db *orm.SQLiteDatabase, wiring Wiring, auth func(http.Handler) http.Handler) *chi.Mux",
		"return session.GrantAuthenticator(db, inner, nil)",
		"queue := orm.NewSearchIndexingSQLiteQueue(db.SQL())",
	} {
		if !strings.Contains(string(wiring), want) {
			t.Errorf("wiring.go lacks %s:\n%s", want, wiring)
		}
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	dirs := []string{servergen.ServerDir(outputRoot, wiringStack, wiringServer)}
	for _, deployable := range wiringWorkers {
		dirs = append(dirs, servergen.ServerDir(outputRoot, wiringStack, deployable))
	}
	for _, dir := range dirs {
		main, err := os.ReadFile(filepath.Join(dir, servergen.MainFile))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(main), "sqlitefile.Open(") || strings.Contains(string(main), "pgxpool") {
			t.Errorf("%s does not open the SQLite file alone:\n%s", dir, main)
		}
		for _, args := range [][]string{{"mod", "tidy"}, {"vet", "."}} {
			goIn(t, dir, env, args...)
		}
	}
}

// TestGoServerWiresTheRuntimeOnSQLiteEndToEnd is
// TestGoServerWiresTheRuntimeEndToEnd over a SQLite file (#717): the
// generated server and its four workers run as processes over
// fixture-agent-runs-db's file, which the build's SQLite DDL made, with no
// hand wiring, and every case of wiringCases passes as on Postgres: the
// notifier's worker texts the notice and the dispatcher's worker writes
// the reviewer's run, an ack on the relay's callback lands, an unknown
// sender lands in triage, a grant's token is the caller of the API's
// routes, and search stays current through the indexing worker. The
// app's side is testdata/wiring_probe_sqlite_go.go. It needs no database
// server; it skips in -short mode.
func TestGoServerWiresTheRuntimeOnSQLiteEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go server's wiring on SQLite end to end in -short mode")
	}
	runWiringEndToEnd(t, wiringDialect{
		dialects: []any{"sqlite", "postgres"},
		probe:    wiringProbeSQLite,
		open: func(t *testing.T, outputRoot string, probe func(database string, args ...string) string) string {
			database := "sqlite:" + filepath.Join(t.TempDir(), wiringDB+".sqlite")
			probe(database, "setup", filepath.Join(SQLDir(outputRoot, wiringDB), SQLiteSubdir, "create.sql"))
			return database
		},
	})
}
