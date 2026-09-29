package generator

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/graphdesc"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/loader"
)

// versionGraphFixtureDir holds the fixture the version-graph scenarios run
// against (runtime/versiongraph/testdata/scenarios): the Recipe graph's
// descriptor and the Postgres DDL of fixture-version-graph-db.
const versionGraphFixtureDir = "../../runtime/versiongraph/testdata/fixture"

// TestVersionGraphScenarioFixtureIsCurrent builds fixture-version-graph-db's
// Recipe descriptor and its DDL, and fails when the checked-in copies the
// scenario runners read differ. Rewrite them with:
//
//	go test ./internal/generator -run TestVersionGraphScenarioFixtureIsCurrent -update
func TestVersionGraphScenarioFixtureIsCurrent(t *testing.T) {
	const fixture = "fixture-version-graph-db"
	schema, err := loader.LoadService(filepath.Join(tsFixtures, fixture))
	if err != nil {
		t.Fatalf("load %s: %v", fixture, err)
	}
	graphs, err := graphdesc.Graphs(schema)
	if err != nil {
		t.Fatalf("describe the graphs: %v", err)
	}
	if len(graphs) != 1 {
		t.Fatalf("%s declares %d graphs, want the Recipe graph alone", fixture, len(graphs))
	}
	descriptor, err := graphs[0].Descriptor.JSON()
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: fixture})
	if err != nil {
		t.Fatalf("generate the DDL: %v", err)
	}
	ddlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write the DDL: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(ddlDir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}

	for name, want := range map[string][]byte{
		graphs[0].FileName + ".json": descriptor,
		"create.sql":                 createSQL,
	} {
		path := filepath.Join(versionGraphFixtureDir, name)
		if *updateNamingGolden {
			if err := os.MkdirAll(versionGraphFixtureDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(path, want, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		got, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v (rewrite it with -update)", path, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s is stale: the compiler writes another %s for %s; rewrite it with -update", path, name, fixture)
		}
	}
}
