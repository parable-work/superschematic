package ormgen

import (
	_ "embed"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/sqlite/behavior_reach_results_test.go
var sqliteBehaviorReachResultsTest string

// TestBehaviorOperationsKeepToTheCallerOnSQLite holds the Go repositories'
// behavior operations on a SQLite file to the caller's reach (D156, D182,
// D201), as TestBehaviorOperationsKeepToTheCallerOnPostgres holds them on
// Postgres, case for case: a row operation of a row the caller does not
// see, or for one that writes may not write, is ErrNotFound wrapping the
// core's not_found before the core runs, and writes nothing; a private
// row answers its owner, a named row the principals it names (its list of
// ids written and read through the core's SQLite store), a party the row
// its grant names, and an agent run with read-only access its
// conversation's reads alone; and Pool's pick answers no member the
// caller does not see. It generates fixture-audience-behaviors-db's ORM
// with its repositories on SQLite and runs behavior_reach_results_test.go
// over a SQLite file. The module links the core's static archive, which
// scripts/rules-archive.sh stages; the test skips without it and in -short
// mode.
func TestBehaviorOperationsKeepToTheCallerOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's behavior operations on SQLite in -short mode")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}
	ormDir := generateBehaviorORMModule(t, paths, behaviorReachFixture, func(o *Options) { o.SQLite = true })
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, behaviorReachFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", behaviorReachFixture, err)
	}
	out := runSQLiteTestIn(t, ormDir, schema, behaviorReachFixture, sqliteBehaviorReachResultsTest, "TestBehaviorOperationsKeepToTheCallerOnSQLite")
	for _, name := range behaviorReachORMCases {
		if want := "--- PASS: TestBehaviorOperationsKeepToTheCallerOnSQLite/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}
