package generator

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestRustAPIOfIsTheCrateTheAPIGeneratorWrites builds fixture-nested-arrays-api's
// Rust API crate through RustAPIOf, the forward an extension calls, and
// finds the crate generateRustAPI writes: its name, each operation's Args
// struct, arguments, input with the prepare function its check runs, and
// result type. A schema without operations has none.
func TestRustAPIOfIsTheCrateTheAPIGeneratorWrites(t *testing.T) {
	service := "fixture-nested-arrays-api"
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	outputs, err := ParseOutputs(cfg.Outputs)
	if err != nil {
		t.Fatalf("ParseOutputs: %v", err)
	}
	opts := Options{
		OutputRoot:  t.TempDir(),
		ServicePath: filepath.Join(tsFixtures, service),
		Naming:      naming.Default(),
	}
	r, _ := newRun(schema, cfg, outputs, opts, CoreRegistry(opts.Naming))

	output, err := RustAPIOf(r.GenerateContext)
	if err != nil {
		t.Fatalf("RustAPIOf: %v", err)
	}
	if output == nil {
		t.Fatal("RustAPIOf: no crate for an API with operations")
	}
	if want := naming.Default().RustAPICrate(service); output.CrateName != want {
		t.Errorf("crate %q, want %q", output.CrateName, want)
	}
	byName := map[string]int{}
	for i, endpoint := range output.Endpoints {
		byName[endpoint.Namespace+"."+endpoint.Name] = i
	}
	save, ok := byName["grid.saveGrid"]
	if !ok {
		t.Fatalf("no grid.saveGrid among %v", byName)
	}
	endpoint := output.Endpoints[save]
	if endpoint.ArgsName != "GridSaveGridArgs" || endpoint.OutputRustType != "types::GridView" {
		t.Errorf("grid.saveGrid: args %q, result %q", endpoint.ArgsName, endpoint.OutputRustType)
	}
	if endpoint.Input == nil || endpoint.Input.Prepare != "types::validators::prepare_save_grid_input" {
		t.Errorf("grid.saveGrid input: %+v", endpoint.Input)
	}
	labels := output.Endpoints[byName["grid.replaceLabels"]]
	if len(labels.PathArgs) != 1 || labels.PathArgs[0].Field != "id" || len(labels.BodyArgs) != 1 || labels.BodyArgs[0].RustType != "Vec<Vec<String>>" {
		t.Errorf("grid.replaceLabels arguments: path %+v, body %+v", labels.PathArgs, labels.BodyArgs)
	}

	db, dbCfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-db"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	dbOutputs, err := ParseOutputs(dbCfg.Outputs)
	if err != nil {
		t.Fatalf("ParseOutputs: %v", err)
	}
	r, _ = newRun(db, dbCfg, dbOutputs, opts, CoreRegistry(opts.Naming))
	if output, err := RustAPIOf(r.GenerateContext); err != nil || output != nil {
		t.Errorf("RustAPIOf on a DB schema: %v, %v; want no crate", output, err)
	}
}
