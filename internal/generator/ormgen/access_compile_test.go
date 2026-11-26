package ormgen

import (
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestHeldRepositoriesCompile generates the ORM of fixtures that cover the
// repository's other shapes, a versioned table's history, a vector's
// Nearest, a Mergeable's MergeInto, a projection, a table of behaviors
// and encrypted fields, as a database whose reads keep to the caller of
// the operation (D156, D180), and builds and vets each: the access
// conditions every read and write of those shapes carries compile there
// too. TestGoAccessEndToEnd runs them on Postgres for the access fixture.
func TestHeldRepositoriesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	for _, tc := range []struct {
		fixture string
		rules   bool
	}{
		{"fixture-db", false},
		{"fixture-vectors-db", false},
		{mergeFixture, true},
		{"fixture-queries-db", false},
		{workflowFixture, true},
		{"fixture-revisions-db", true},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			var opts []loader.Option
			var reg *registry.Registry
			if tc.rules {
				reg = registry.New(naming.Default())
				opts = append(opts, loader.WithRegistry(reg))
			}
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.fixture), opts...)
			if err != nil {
				t.Fatalf("load %s: %v", tc.fixture, err)
			}
			clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
			typesModule := "example.com/schemas/types/go/" + tc.fixture
			root := t.TempDir()
			typesDir := filepath.Join(root, "types", "go", tc.fixture)
			ormDir := filepath.Join(root, "orm", tc.fixture)
			typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: tc.fixture, ModulePath: typesModule, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
				t.Fatal(err)
			}
			if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
				t.Fatal(err)
			}
			output, err := Generate(schema, Options{SchemaName: tc.fixture, ModulePath: "example.com/schemas/orm/" + tc.fixture, TypesModule: typesModule, Registry: reg, Clock: clock})
			if err != nil {
				t.Fatal(err)
			}
			// The database's reads keep to the caller, with no table of its
			// own an audience holds.
			output.Access = &AccessModel{Held: true}
			for _, repo := range output.Repositories {
				output.Access.Tables = append(output.Access.Tables, AccessTable{Type: repo.TypeName, Key: repo.QualifiedKey, KeyKind: "uuid"})
			}
			if err := SetReplacePaths(output, paths, ormDir); err != nil {
				t.Fatal(err)
			}
			if err := WriteORM(output, ormDir); err != nil {
				t.Fatal(err)
			}
			tidyGeneratedModule(t, ormDir)
			for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = ormDir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s in the held ORM of %s: %v\n%s", strings.Join(args, " "), tc.fixture, err, out)
				}
			}
		})
	}
}
