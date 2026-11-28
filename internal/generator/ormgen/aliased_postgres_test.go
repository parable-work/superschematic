package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// aliasedORMCases are the cases the generated module's test runs over
// fixture-aliased-db, by name (D147).
var aliasedORMCases = []string{
	"a name another row holds is refused naming nothing of that row",
	"rename keeps the old name as an alias and resolve finds it",
	"a removed alias and a deleted row's names are freed",
	"creates of one name at once: one takes it, the rest are refused",
	"a merge moves the aliases of the row merged away, its name among them",
}

// TestAliasedTablesOnPostgres holds the generated Go ORM of a database
// whose Company and Tag carry Aliased on fields to the rules module's
// adapter on Postgres (D147): a create gives the name, and one another row
// holds, as a name or an alias, is refused, naming neither that row's key
// nor its fields (D156); a rename
// keeps the old name as an alias, which Resolve finds; a removed alias and
// a deleted row's names are freed for another row; creates of one name at
// once leave it one row's; and MergeInto moves the aliases of the row
// merged away, its name among them, and its names, to the row that stays.
// It loads fixture-aliased-db with the core's registry, generates its Go
// types, its ORM and its DDL into a temporary tree, and builds, vets and
// tests the ORM module with aliasedORMTest, whose every case must pass on
// the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links
// the core's static archive, which scripts/rules-archive.sh stages; the
// test skips without it, and in -short mode.
func TestAliasedTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Aliased ORM's end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated Aliased ORM against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, aliasedFixture)
	for _, name := range []string{"repository_company_name_aliases.go", "repository_company_name_names.go"} {
		if _, err := os.Stat(filepath.Join(ormDir, name)); err == nil {
			t.Errorf("the generated ORM holds %s, a repository of Aliased's own tables, which have none", name)
		}
	}
	if err := os.WriteFile(filepath.Join(ormDir, "aliased_test.go"), []byte(aliasedORMTest), 0o644); err != nil {
		t.Fatalf("write the Aliased test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range aliasedORMCases {
		if want := "--- PASS: TestAliasedTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// aliasedORMTest is the generated module's test: companies, contacts and
// tags through the generated repositories, in a schema of their own, read
// back past them with SQL.
const aliasedORMTest = `package orm

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-aliased-db"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

// taken is the veto a refusal carries and the members of its details,
// which name the name alone and nothing of the row that holds it (D156),
// or "" and nil when err is no veto.
func taken(err error) (string, []string) {
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		return "", nil
	}
	var details struct {
		VetoCode    string                     ` + "`" + `json:"vetoCode"` + "`" + `
		VetoDetails map[string]json.RawMessage ` + "`" + `json:"vetoDetails"` + "`" + `
	}
	_ = json.Unmarshal(refusal.Details, &details)
	var members []string
	for member := range details.VetoDetails {
		members = append(members, member)
	}
	sort.Strings(members)
	return details.VetoCode, members
}

// nameOnly reports whether a refusal of name_taken, err, whose details
// hold members, tells nothing of the row of key that holds the name: its
// details are the name and its normalized form, and neither they nor its
// message hold the key.
func nameOnly(err error, members []string, key string) bool {
	return fmt.Sprint(members) == "[name normalized]" && !strings.Contains(err.Error(), key)
}

func TestAliasedTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Aliased tables against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_aliased_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	company := func(t *testing.T, name string) *types.Company {
		t.Helper()
		row, err := db.Company.CreateOne(ctx, &CompanyCreate{Name: name, City: "Oslo"})
		if err != nil {
			t.Fatalf("create %s: %v", name, err)
		}
		if row.Name != name {
			t.Fatalf("the company was created with the name %q, want %q", row.Name, name)
		}
		return row
	}
	resolve := func(t *testing.T, name string) *CompanyNameResolveResultRow {
		t.Helper()
		out, err := db.Company.Name().Resolve(ctx, CompanyNameResolveParams{Name: name})
		if err != nil {
			t.Fatalf("resolve %s: %v", name, err)
		}
		return out.Row
	}
	aliases := func(t *testing.T, of *types.Company) []string {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT alias FROM company_name_aliases WHERE company_id = $1 ORDER BY normalized", of.Id.ToUUID().String())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var alias string
			if err := rows.Scan(&alias); err != nil {
				t.Fatal(err)
			}
			out = append(out, alias)
		}
		return out
	}

	t.Run("a name another row holds is refused naming nothing of that row", func(t *testing.T) {
		acme := company(t, "Acme")
		if _, err := db.Company.Name().AddAlias(ctx, *acme.Id, CompanyNameAddAliasParams{Alias: "Acme Corp"}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Company.CreateOne(ctx, &CompanyCreate{Name: "ACME", City: "Bergen"})
		if code, members := taken(err); code != "name_taken" || !nameOnly(err, members, acme.Id.String()) {
			t.Fatalf("a create of ACME = %v (%s, %v), want name_taken naming nothing of %s", err, code, members, acme.Id.String())
		}
		globex := company(t, "Globex")
		_, err = db.Company.Name().Rename(ctx, *globex.Id, CompanyNameRenameParams{Name: "  acme   corp"})
		if code, members := taken(err); code != "name_taken" || !nameOnly(err, members, acme.Id.String()) {
			t.Fatalf("a rename to acme corp = %v (%s, %v), want name_taken naming nothing of %s", err, code, members, acme.Id.String())
		}
		if _, err := db.Tag.CreateOne(ctx, &TagCreate{Label: "Q3 Plan"}); err != nil {
			t.Fatal(err)
		}
		_, err = db.Tag.CreateOne(ctx, &TagCreate{Label: "q3-plan"})
		if code, _ := taken(err); code != "name_taken" {
			t.Fatalf("a tag q3-plan = %v, want name_taken: a slug reads both as one", err)
		}
	})

	t.Run("rename keeps the old name as an alias and resolve finds it", func(t *testing.T) {
		initech := company(t, "Initech")
		out, err := db.Company.Name().Rename(ctx, *initech.Id, CompanyNameRenameParams{Name: "Initech Group"})
		if err != nil || out.Name != "Initech Group" || out.Alias == nil || *out.Alias != "Initech" {
			t.Fatalf("Rename = %+v, %v", out, err)
		}
		if got := aliases(t, initech); fmt.Sprint(got) != "[Initech]" {
			t.Fatalf("the aliases are %v, want [Initech]", got)
		}
		row, err := db.Company.GetOne(ctx, *initech.Id, nil)
		if err != nil || row.Name != "Initech Group" {
			t.Fatalf("GetOne = %+v, %v", row, err)
		}
		if found := resolve(t, "INITECH"); found == nil || found.Key != initech.Id.String() || !found.Alias || found.Name != "Initech Group" {
			t.Fatalf("resolve INITECH = %+v", found)
		}
		if found := resolve(t, "Hooli"); found != nil {
			t.Fatalf("resolve Hooli = %+v, want none", found)
		}
	})

	t.Run("a removed alias and a deleted row's names are freed", func(t *testing.T) {
		umbrella := company(t, "Umbrella")
		if _, err := db.Company.Name().AddAlias(ctx, *umbrella.Id, CompanyNameAddAliasParams{Alias: "Umbrella Corp"}); err != nil {
			t.Fatal(err)
		}
		_, err := db.Company.Name().RemoveAlias(ctx, *umbrella.Id, CompanyNameRemoveAliasParams{Alias: "umbrella"})
		if code, _ := taken(err); code != "is_name" {
			t.Fatalf("removeAlias of the name = %v, want is_name", err)
		}
		if out, err := db.Company.Name().RemoveAlias(ctx, *umbrella.Id, CompanyNameRemoveAliasParams{Alias: "UMBRELLA CORP"}); err != nil || out.Alias != "Umbrella Corp" {
			t.Fatalf("RemoveAlias = %+v, %v", out, err)
		}
		corp := company(t, "Umbrella Corp")
		if err := db.Company.DeleteOne(ctx, *umbrella.Id); err != nil {
			t.Fatal(err)
		}
		if _, err := db.Company.Name().AddAlias(ctx, *corp.Id, CompanyNameAddAliasParams{Alias: "Umbrella"}); err != nil {
			t.Fatalf("an alias the deleted row's name was: %v", err)
		}
		if found := resolve(t, "umbrella"); found == nil || found.Key != corp.Id.String() {
			t.Fatalf("resolve umbrella = %+v", found)
		}
	})

	t.Run("creates of one name at once: one takes it, the rest are refused", func(t *testing.T) {
		var wg sync.WaitGroup
		errs := make([]error, 4)
		for i := range errs {
			wg.Add(1)
			go func() {
				defer wg.Done()
				_, errs[i] = db.Company.CreateOne(ctx, &CompanyCreate{Name: "Soylent", City: fmt.Sprint(i)})
			}()
		}
		wg.Wait()
		created := 0
		for _, err := range errs {
			if err == nil {
				created++
				continue
			}
			if code, _ := taken(err); code != "name_taken" {
				t.Fatalf("a create of Soylent = %v, want name_taken", err)
			}
		}
		if created != 1 {
			t.Fatalf("%d creates of Soylent were made, want 1", created)
		}
	})

	t.Run("a merge moves the aliases of the row merged away, its name among them", func(t *testing.T) {
		from := company(t, "Hooli")
		if _, err := db.Company.Name().AddAlias(ctx, *from.Id, CompanyNameAddAliasParams{Alias: "Hooli XYZ"}); err != nil {
			t.Fatal(err)
		}
		into := company(t, "Nucleus")
		contact, err := db.Contact.CreateOne(ctx, &types.Contact{Company: *from, Email: "gavin@hooli.example"})
		if err != nil {
			t.Fatal(err)
		}
		result, err := db.Company.MergeInto(ctx, *from.Id, *into.Id)
		if err != nil {
			t.Fatalf("MergeInto: %v", err)
		}
		if fmt.Sprint(result.Repointed) != "map[CompanyNameAliases:1 CompanyNameNames:2 Contact:1]" || len(result.Deleted) != 0 {
			t.Fatalf("MergeInto = %+v", result)
		}
		if got := aliases(t, into); fmt.Sprint(got) != "[Hooli Hooli XYZ]" {
			t.Fatalf("the aliases of the row that stays are %v, want [Hooli Hooli XYZ]", got)
		}
		for _, name := range []string{"hooli", "Hooli XYZ", "nucleus"} {
			if found := resolve(t, name); found == nil || found.Key != into.Id.String() {
				t.Fatalf("resolve %s = %+v, want the row that stays", name, found)
			}
		}
		moved, err := db.Contact.GetOne(ctx, *contact.Id, nil)
		if err != nil || moved.Company.Id == nil || *moved.Company.Id != *into.Id {
			t.Fatalf("the contact = %+v, %v, want the row that stays", moved, err)
		}
		_, err = db.Company.CreateOne(ctx, &CompanyCreate{Name: "Hooli", City: "Elsewhere"})
		if code, members := taken(err); code != "name_taken" || !nameOnly(err, members, into.Id.String()) {
			t.Fatalf("a create of Hooli after the merge = %v (%s, %v)", err, code, members)
		}
	})
}
`
