package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/queryverify"
	"github.com/parable-work/superschematic/internal/stack/local"
)

// queriesFixture is the DB service whose declared queries (D86) the
// queries tests verify: a household ledger's dashboard reports.
const queriesFixture = "../internal/loader/tsreader/testdata/services/fixture-queries-db"

// prepareQueriesService copies fixture-queries-db into a schemas root of
// its own, its tsconfig pointing at this checkout's packages, and returns
// the service's directory.
func prepareQueriesService(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas")
	serviceDir := filepath.Join(root, "services", "fixture-queries-db")
	copyDir(t, queriesFixture, serviceDir)
	baseConfig, err := os.ReadFile("../internal/loader/tsreader/testdata/tsconfig.base.json")
	require.NoError(t, err)
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	text := strings.ReplaceAll(string(baseConfig), "../../../../", filepath.ToSlash(repoRoot)+"/")
	require.NoError(t, os.WriteFile(filepath.Join(root, "tsconfig.base.json"), []byte(text), 0o644))
	return serviceDir
}

func runCommand(t *testing.T, args ...string) (stdout, stderr string, err error) {
	t.Helper()
	out, errOut := new(bytes.Buffer), new(bytes.Buffer)
	root := New(Config{})
	root.SetOut(out)
	root.SetErr(errOut)
	root.SetArgs(args)
	err = root.Execute()
	return out.String(), errOut.String(), err
}

// queriesDatabase is the Postgres the queries tests prepare statements on,
// with the migration runner built from runtime/migrate/go and named by
// SUPERSCHEMATIC_MIGRATE. They skip in -short mode and without a database.
func queriesDatabase(t *testing.T) string {
	t.Helper()
	if testing.Short() {
		t.Skip("-short: the queries tests build the migration runner and prepare statements on Postgres")
	}
	url := os.Getenv("SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set SUPERSCHEMATIC_MIGRATE_TEST_DATABASE_URL to a Postgres whose role may create a database")
	}
	runner := filepath.Join(t.TempDir(), local.MigrateBinary)
	build := exec.Command("go", "build", "-o", runner, "./cmd/superschematic-migrate")
	build.Dir = filepath.Join("..", "runtime", "migrate", "go")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOWORK=off")
	out, err := build.CombinedOutput()
	require.NoError(t, err, string(out))
	t.Setenv(local.MigrateEnv, runner)
	t.Setenv(QueriesDatabaseEnv, "")
	return url
}

// TestQueriesVerifyAndARenamedColumn is F1's acceptance (D86): queries
// verify prepares fixture-queries-db's queries on Postgres and writes the
// lock the fixture commits, which check and build then hold the queries
// to with no database. Renaming line_item.amount makes the lock stale for
// check and build, and fails queries verify and migrate plan, each naming
// the queries that read the column.
func TestQueriesVerifyAndARenamedColumn(t *testing.T) {
	url := queriesDatabase(t)
	serviceDir := prepareQueriesService(t)
	lockPath := filepath.Join(serviceDir, queryverify.LockFile)
	require.NoError(t, os.Remove(lockPath))

	_, _, err := runCommand(t, "check", serviceDir)
	require.Error(t, err, "check refuses queries no lock verified")

	stdout, stderr, err := runCommand(t, "queries", "verify", serviceDir, "--database-url", url)
	require.NoError(t, err, stderr)
	assert.Contains(t, stdout, "verified 6 queries of fixture-queries-db")
	written, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	committed, err := os.ReadFile(filepath.Join(queriesFixture, queryverify.LockFile))
	require.NoError(t, err)
	assert.Equal(t, string(committed), string(written), "the fixture's queries.lock is the one queries verify writes")

	_, stderr, err = runCommand(t, "check", serviceDir)
	require.NoError(t, err, stderr)
	_, stderr, err = runCommand(t, "build", serviceDir, "--out", t.TempDir())
	require.NoError(t, err, stderr)

	ledger := filepath.Join(serviceDir, "src", "ledger.schema.ts")
	source, err := os.ReadFile(ledger)
	require.NoError(t, err)
	renamed := strings.Replace(string(source), "  amount: Finance.SignedMoney;", "  total: Finance.SignedMoney;", 1)
	require.NotEqual(t, string(source), renamed)
	require.NoError(t, os.WriteFile(ledger, []byte(renamed), 0o644))

	stdout, _, err = runCommand(t, "check", serviceDir)
	require.Error(t, err)
	assert.Contains(t, stdout, "query categoryTotals changed since it was verified")
	_, _, err = runCommand(t, "build", serviceDir, "--out", t.TempDir())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "query categoryTotals changed since it was verified")

	_, _, err = runCommand(t, "queries", "verify", serviceDir, "--database-url", url)
	require.Error(t, err)
	for _, want := range []string{
		"queries verify: the queries of fixture-queries-db do not hold against its model",
		"query categoryTotals: Postgres refuses it: column li.amount does not exist",
		"query transactionList (first page): Postgres refuses it: column li.amount does not exist",
		"query transferCandidates: Postgres refuses it: column o.amount does not exist",
	} {
		assert.Contains(t, err.Error(), want)
	}
	// Budgets reads no line item, and holds.
	assert.NotContains(t, err.Error(), "query budgets")
	stale, err := os.ReadFile(lockPath)
	require.NoError(t, err)
	assert.Equal(t, string(written), string(stale), "a verify that fails leaves the lock as it was")

	_, _, err = runCommand(t, "migrate", "plan", serviceDir, "--database-url", url)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "migrate plan: the queries of fixture-queries-db do not hold against its model")
	assert.Contains(t, err.Error(), "query categoryTotals: Postgres refuses it: column li.amount does not exist")
}

// TestQueriesVerifyNamesWhatDoesNotHold: a row field that is not Nullable
// reading a column that may be null, a type that does not match and a
// column no field reads each fail verification, naming the query and the
// fix.
func TestQueriesVerifyNamesWhatDoesNotHold(t *testing.T) {
	url := queriesDatabase(t)
	serviceDir := prepareQueriesService(t)
	reports := filepath.Join(serviceDir, "src", "reports.schema.ts")
	source, err := os.ReadFile(reports)
	require.NoError(t, err)
	text := string(source)
	for _, edit := range [][2]string{
		// The category's name of an outer join, no longer Nullable.
		{"  categoryName: Nullable<Identity.Name>;", "  categoryName: Identity.Name;"},
		// A computed column the query no longer vouches for.
		{`  notNull: ["daysApart"],`, ""},
		// A sum Postgres reads as numeric, which a SignedMoney is not.
		{"COALESCE(SUM(li.amount) FILTER (WHERE li.amount < 0), 0)::bigint AS spent,\n           COALESCE(SUM(li.amount) FILTER (WHERE li.amount > 0), 0)::bigint AS earned\n    FROM line_item li\n    LEFT JOIN category c", "COALESCE(SUM(li.amount) FILTER (WHERE li.amount < 0), 0) AS spent,\n           COALESCE(SUM(li.amount) FILTER (WHERE li.amount > 0), 0)::bigint AS earned, 1 AS extra\n    FROM line_item li\n    LEFT JOIN category c"},
	} {
		require.Contains(t, text, edit[0])
		text = strings.Replace(text, edit[0], edit[1], 1)
	}
	require.NoError(t, os.WriteFile(reports, []byte(text), 0o644))

	_, _, err = runCommand(t, "queries", "verify", serviceDir, "--database-url", url)
	require.Error(t, err)
	for _, want := range []string{
		"query transactionList (first page): column category_name reads category.name of a table an outer join may leave out, and TransactionListRow.categoryName is not Nullable",
		`query transferCandidates: column days_apart is computed, which Postgres cannot tell is never null, and TransferCandidatesRow.daysApart is not Nullable; declare it Nullable<number>, or vouch that the query never returns null for it with notNull: ["daysApart"] or a -- @notNull daysApart comment`,
		"query categoryTotals: column spent is numeric, and CategoryTotalsRow.spent is Finance.SignedMoney, which a BIGINT column holds; cast it in the SQL (::bigint)",
		"query categoryTotals: it returns column extra, which no field of CategoryTotalsRow reads",
	} {
		assert.Contains(t, err.Error(), want)
	}
}

// TestQueriesVerifyRefusesAServiceOfAnotherKind: only a DB service declares
// queries.
func TestQueriesVerifyRefusesAServiceOfAnotherKind(t *testing.T) {
	servicesRoot := writeSchemasRoot(t, t.TempDir(), map[string]map[string]string{"shop-common": migrateGeneral()})
	_, _, err := runCommand(t, "queries", "verify", filepath.Join(servicesRoot, "shop-common"))
	require.Error(t, err)
	assert.Contains(t, err.Error(), "queries verify: shop-common is of kind General; a DB service declares queries")
}

// TestCheckWatchStampsTheQueriesLock: check --watch re-checks a service
// whose queries.lock queries verify rewrote.
func TestCheckWatchStampsTheQueriesLock(t *testing.T) {
	serviceDir := prepareQueriesService(t)
	c, err := newChecker(&app{}, "", serviceDir)
	require.NoError(t, err)
	stamps := c.snapshot()
	_, ok := stamps[filepath.Join(serviceDir, queryverify.LockFile)]
	assert.True(t, ok, "the snapshot stamps %s", queryverify.LockFile)
}

// TestCheckHoldsQueriesToTheirLock: with no database, check refuses a
// query its service's queries.lock does not hold, or holds for other SQL.
func TestCheckHoldsQueriesToTheirLock(t *testing.T) {
	serviceDir := prepareQueriesService(t)
	_, stderr, err := runCommand(t, "check", serviceDir)
	require.NoError(t, err, stderr)

	reports := filepath.Join(serviceDir, "src", "reports.schema.ts")
	source, err := os.ReadFile(reports)
	require.NoError(t, err)
	require.NoError(t, os.WriteFile(reports, []byte(strings.Replace(string(source), `ORDER BY c."name"`, `ORDER BY c."name" DESC`, 1)), 0o644))
	stdout, _, err := runCommand(t, "check", serviceDir)
	require.Error(t, err)
	assert.Contains(t, stdout, "query budgets changed since it was verified")
	assert.Contains(t, stdout, "run `superschematic queries verify")
	assert.NotContains(t, stdout, "query categoryTotals")

	require.NoError(t, os.Remove(filepath.Join(serviceDir, queryverify.LockFile)))
	stdout, _, err = runCommand(t, "check", serviceDir)
	require.Error(t, err)
	assert.Contains(t, stdout, "query categoryTotals has not been verified: queries.lock holds no entry for it")
}
