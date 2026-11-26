package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestTheRelationFilterSuffixNamesTheMember: go.orm_relation_filter_suffix
// renames the filter member that reaches through a to-one relation, and
// only that: TenantUserFilter's Tenant becomes TenantWhere, and its
// condition is the same subquery. The default keeps Tenant. The suffixed
// ORM builds, and a test inside it reads the condition.
func TestTheRelationFilterSuffixNamesTheMember(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/fixture-db"
	generate := func(n naming.Naming) *ORMOutput {
		output, err := Generate(schema, Options{
			SchemaName:  "fixture-db",
			ModulePath:  "example.com/schemas/orm/fixture-db",
			TypesModule: typesModule,
			Naming:      n,
			Clock:       fixedClock,
		})
		if err != nil {
			t.Fatal(err)
		}
		return output
	}
	member := func(output *ORMOutput) []string {
		var names []string
		for _, repo := range output.Repositories {
			if repo.TypeName == "TenantUser" {
				for _, rel := range repo.PathRelations {
					names = append(names, rel.GoField)
				}
			}
		}
		return names
	}
	if got := strings.Join(member(generate(naming.Naming{})), " "); got != "Tenant" {
		t.Errorf("default TenantUserFilter path members = %q, want Tenant", got)
	}
	where := generate(naming.Naming{Go: naming.GoConfig{ORMRelationFilterSuffix: "Where"}})
	if got := strings.Join(member(where), " "); got != "TenantWhere" {
		t.Errorf("suffixed TenantUserFilter path members = %q, want TenantWhere", got)
	}

	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", "fixture-db")
	ormDir := filepath.Join(root, "orm", "fixture-db")
	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: "fixture-db", ModulePath: typesModule, Clock: fixedClock})
	if err != nil {
		t.Fatal(err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatal(err)
	}
	if err := SetReplacePaths(where, paths, ormDir); err != nil {
		t.Fatal(err)
	}
	if err := WriteORM(where, ormDir); err != nil {
		t.Fatal(err)
	}
	query, err := os.ReadFile(filepath.Join(ormDir, "query.go"))
	if err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`(?m)^\s*TenantWhere \*TenantFilter$`).Match(query) || regexp.MustCompile(`(?m)^\s*Tenant \*TenantFilter$`).Match(query) {
		t.Fatal("query.go does not declare TenantUserFilter.TenantWhere in place of Tenant")
	}
	code := `package orm

import (
	"strings"
	"testing"
)

func TestTenantWhereReachesThroughTheRelation(t *testing.T) {
	slug := "acme"
	var args []interface{}
	clause := (&TenantUserFilter{TenantWhere: &TenantFilter{Slug: &StringFilter{Eq: &slug}}}).buildWhereClause(&args, 1)
	if !strings.Contains(clause, " IN (SELECT ") || len(args) != 1 || args[0] != "acme" {
		t.Fatalf("clause %q with args %v, want a subquery on the tenant's slug", clause, args)
	}
}
`
	if err := os.WriteFile(filepath.Join(ormDir, "relation_filter_test.go"), []byte(code), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{typesDir, ormDir} {
		tidy := exec.Command("go", "mod", "tidy")
		tidy.Dir = dir
		if out, err := tidy.CombinedOutput(); err != nil {
			t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
		}
	}
	run := exec.Command("go", "test", "-count=1", "-run", "TestTenantWhereReachesThroughTheRelation", ".")
	run.Dir = ormDir
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("generated ORM: %v\n%s", err, out)
	}
}
