package apigen_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// rowFeaturesFixtures are fixture-references-api, whose
// @rowFeatureOperations sets serve fixture-references-db's Ticket and
// Note (#723), and its database.
var rowFeaturesFixtures = behaviorFixtures{"fixture-references-api", "fixture-references-db"}

// rowFeaturesBucket is the Deps field of fixture-references-api's one
// bucket, as the dispatch layer names it.
func rowFeaturesBucket(output *apigen.APIOutput) {
	output.Deps.Buckets = []apigen.DepsBucket{{Service: "fixture-references-files", Field: "FixtureReferencesFiles"}}
}

// generateRowFeaturesAPI is the API output of fixture-references-api over
// its database, its bucket in Deps.
func generateRowFeaturesAPI(t *testing.T) *apigen.APIOutput {
	t.Helper()
	api, db := loadFixturePair(t, rowFeaturesFixtures)
	output := generatePairAPI(t, rowFeaturesFixtures, api, db)
	rowFeaturesBucket(output)
	return output
}

// TestRowFeaturesOnAGoServer: fixture-references-api's references and
// files are the build's to serve (#723), so the implementation's interface
// has getTicket alone, and rowfeatures.go serves each operation through
// the ORM's References() and Attachments(), the file operations over
// Config.Attachments. The module's files are held to their golden copies.
// Regenerate with:
// go test ./internal/generator/apigen -run TestRowFeaturesOnAGoServer -update
func TestRowFeaturesOnAGoServer(t *testing.T) {
	output := generateRowFeaturesAPI(t)
	if !output.HasRowFeatureOperations() || !output.HasRowFeatureEndpoints() || !output.TakesDatabase() {
		t.Fatal("the output has no row feature operation")
	}
	if got := output.RowFeatureBucket(); got != "fixture-references-files" {
		t.Errorf("RowFeatureBucket = %q", got)
	}
	view, err := apigen.ImplementedOutput(output)
	if err != nil {
		t.Fatal(err)
	}
	var implemented, served []string
	for _, ep := range view.Endpoints {
		implemented = append(implemented, ep.Name)
	}
	for _, ep := range view.RowFeatureEndpoints {
		served = append(served, ep.RowFeatureOperation().Type+"."+ep.Name)
	}
	if !slices.Equal(implemented, []string{"getTicket"}) {
		t.Errorf("the implementation's interface holds %v, want getTicket alone", implemented)
	}
	if len(served) != 19 || !slices.Contains(served, "Ticket.removeAttachment") || slices.Contains(served, "Note.removeAttachment") {
		t.Errorf("rowfeatures.go serves %v", served)
	}

	paths := naming.LocalPaths{
		ScalarGo:        "/repo/third_party/superscalar/go",
		SchemaIR:        "/repo/ir",
		SchemaRuntimeGo: "/repo/runtime/schema/go",
		HTTPRuntimeGo:   "/repo/runtime/http/go",
		RulesGo:         "/repo/runtime/rules/go",
	}
	if err := apigen.SetReplacePaths(output, paths, "/repo/schemas/dist/api/"+rowFeaturesFixtures.api); err != nil {
		t.Fatalf("set replace paths: %v", err)
	}
	outDir := t.TempDir()
	if err := apigen.WriteAPI(output, outDir); err != nil {
		t.Fatalf("write api: %v", err)
	}
	goldenDir := filepath.Join("testdata", "golden", rowFeaturesFixtures.api)
	for _, name := range []string{"routes.go", "interfaces.go", "rowfeatures.go", "go.mod", "openapi.json", "permissions.json"} {
		got, err := os.ReadFile(filepath.Join(outDir, name))
		if err != nil {
			t.Fatalf("read generated %s: %v", name, err)
		}
		goldenPath := filepath.Join(goldenDir, name)
		if *update {
			if err := os.MkdirAll(goldenDir, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(goldenPath, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(goldenPath)
		if err != nil {
			t.Fatalf("read golden %s: %v", name, err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept)", name)
		}
	}
}

// TestRowFeaturesAPIWithoutItsBucket: a Go server whose Deps hold no field
// for the bucket the file operations read is refused.
func TestRowFeaturesAPIWithoutItsBucket(t *testing.T) {
	api, db := loadFixturePair(t, rowFeaturesFixtures)
	output := generatePairAPI(t, rowFeaturesFixtures, api, db)
	if _, err := apigen.ImplementedOutput(output); err == nil || !strings.Contains(err.Error(), "its Deps hold no field for it") {
		t.Errorf("ImplementedOutput without the bucket = %v", err)
	}
}

// TestRowFeaturesAPICompiles generates fixture-references-db's Go types
// and ORM and fixture-references-api's Go types and API module into a
// temporary tree shaped as the dist, and builds and vets the API module:
// rowfeatures.go's methods call the ORM's References() and Attachments()
// with the runtime's inputs.
func TestRowFeaturesAPICompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	api, db := loadFixturePair(t, rowFeaturesFixtures)
	apiDir, _ := writeFixtureModulesWith(t, rowFeaturesFixtures, api, db, rowFeaturesBucket)
	for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
}
