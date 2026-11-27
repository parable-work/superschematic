package tsgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
)

// featureRun is one of the platform features' end-to-end tests, the
// notifier's (D153), the inbound path's (D157) and the agent dispatcher's
// (D158), which runs its cases on Postgres and on SQLite (D189): its
// fixture, its test file in testdata/orm and the files there it needs
// beside it, the stubs it copies in, the files tsc checks the package
// against on each dialect, the cases bun must pass, and the text that must
// not reach the test's output.
type featureRun struct {
	fixture string
	test    string
	files   []string
	stubs   map[string]string
	check   map[string]string
	// sqliteCheck is what tsc checks the package against on SQLite: the
	// feature over the ./orm/sqlite entry and the ./d1 entry's exports.
	sqliteCheck map[string]string
	cases       []string
	env         []string
	keep        string
	secrets     []string
}

// runFeature generates run's fixture's TypeScript package, type-checks it,
// and runs its test with bun: on Postgres over the generated create.sql in
// a schema of its own at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, which it
// skips without; on SQLite with ORM_DIALECT sqlite over a file the
// database's SQLite create.sql makes, which needs no server. Every case of
// run.cases must pass, and none of run.secrets may reach bun's output.
func runFeature(t *testing.T, run featureRun, sqlite bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping the generated repositories' end-to-end check in -short mode")
	}
	if !sqlite && os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Skipf("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run %s against Postgres", run.test)
	}
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, run.fixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", run.fixture, err)
	}
	files := map[string]string{}
	check := run.check
	if sqlite {
		model, err := sqlmigrate.BuildModel(schema, sqlgen.Options{SchemaName: run.fixture}, sqlmigrate.SQLite)
		if err != nil {
			t.Fatalf("build %s's SQLite model: %v", run.fixture, err)
		}
		createSQL, err := sqlmigrate.CreateSQL(model)
		if err != nil {
			t.Fatalf("write %s's SQLite create.sql: %v", run.fixture, err)
		}
		files["sqlite/create.sql"] = createSQL
		check = run.sqliteCheck
	} else {
		files["create.sql"] = createSQLOf(t, schema, run.fixture)
	}
	for _, name := range append([]string{"harness.ts", "dialect.ts", run.test}, run.files...) {
		source, err := ormTests.ReadFile("testdata/orm/" + name)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	for name, path := range run.stubs {
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(source)
	}
	typesRoot, bunPath := buildTSPackages(t, []tsPackageCase{{
		name:     run.fixture,
		schema:   schema,
		registry: reg,
		sqlite:   sqlite,
		extra:    check,
	}})
	dir := filepath.Join(typesRoot, run.fixture)
	for name, text := range files {
		path := filepath.Join(dir, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if keep := os.Getenv(run.keep); run.keep != "" && keep != "" {
		_ = exec.Command("cp", "-R", dir, keep).Run()
	}
	dialect := "postgres"
	if sqlite {
		dialect = "sqlite"
	}
	report := filepath.Join(dir, strings.TrimSuffix(run.test, ".test.ts")+"-"+dialect+".junit.xml")
	cmd := exec.Command(bunPath, "test", "./"+run.test, "--timeout=120000", "--reporter=junit", "--reporter-outfile="+report)
	cmd.Dir = dir
	cmd.Env = append(append(os.Environ(), "ORM_DIALECT="+dialect, "AGENT_RUNNER_BUN="+bunPath), run.env...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("the end-to-end test on %s failed: %v\n%s", dialect, err, out)
	}
	junit, err := os.ReadFile(report)
	if err != nil {
		t.Fatalf("read bun's report: %v\n%s", err, out)
	}
	cases, err := junitTestcases(junit)
	if err != nil {
		t.Fatalf("parse bun's report: %v\n%s", err, junit)
	}
	for _, name := range run.cases {
		tc, ok := cases[name]
		switch {
		case !ok:
			t.Errorf("bun did not run %q on %s:\n%s", name, dialect, junit)
		case tc.Failure != nil || tc.Skipped != nil:
			t.Errorf("%q did not pass on %s:\n%s", name, dialect, junit)
		}
	}
	if len(cases) != len(run.cases) {
		t.Errorf("bun ran %d cases on %s, and the test names %d:\n%s", len(cases), dialect, len(run.cases), junit)
	}
	for _, secret := range run.secrets {
		if strings.Contains(string(out), secret) {
			t.Errorf("%q reached the test's output:\n%s", secret, out)
		}
	}
	t.Logf("bun ran the end-to-end test on %s:\n%s", dialect, out)
}
