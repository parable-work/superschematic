package generator

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestRunRendersTheRegisteredCatalogsRawBodyChecks: the registered scalar
// catalog's raw-body checks reach the Go routes the api output writes, with
// no core edit: the catalog declares a check for Generic.JSON, and the
// route of raw-body-check-api's saveNote calls it on the fields of that
// scalar.
func TestRunRendersTheRegisteredCatalogsRawBodyChecks(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join("apigen", "testdata", "services", "raw-body-check-api"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	cfg.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}},
		"api":   map[string]any{"enabled": true},
	}

	catalog, err := registry.ScalarCatalogWithRawBodyChecks(registry.CoreScalars(), map[string]registry.ScalarRawBodyCheck{
		"Generic.JSON": {ImportPath: "example.com/checks/jsonkeys", PackageName: "jsonkeys", Func: "DuplicateKeyErrors"},
	})
	if err != nil {
		t.Fatalf("ScalarCatalogWithRawBodyChecks: %v", err)
	}
	reg := registry.New(naming.Default())
	if err := RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterScalars("checks", catalog); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}

	outputRoot := t.TempDir()
	if _, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Registry: reg}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	routes, err := os.ReadFile(filepath.Join(APIDir(outputRoot, "raw-body-check-api"), "routes.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`jsonkeys "example.com/checks/jsonkeys"`,
		`if checkErrors := jsonkeys.DuplicateKeyErrors(rawInput, "body", "meta"); checkErrors.HasErrors() {`,
	} {
		if !strings.Contains(string(routes), want) {
			t.Errorf("routes.go has no %s", want)
		}
	}
}
