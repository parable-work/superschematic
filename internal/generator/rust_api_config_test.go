package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRustAPIWithEnvVarsCompilesItsConfigModule builds a Rust API whose
// schema declares an @envVars class (fixture-env's, added to
// fixture-nested-arrays-api) through Run, as a build does, and runs cargo
// test on the crate: lib.rs declares the src/config.rs envgen writes beside
// it, the crate depends on dotenvy, and load_config reports the first
// required variable it lacks. The build also writes the crate's
// openapi.json.
func TestRustAPIWithEnvVarsCompilesItsConfigModule(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargoPath, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust API build")
	}
	paths := testpaths.Local(t)

	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-nested-arrays-api"))
	if err != nil {
		t.Fatal(err)
	}
	envSchema, err := loader.LoadService(filepath.Join(tsFixtures, "fixture-env"))
	if err != nil {
		t.Fatal(err)
	}
	for name, def := range envSchema.Types {
		if def.EnvVars {
			schema.Types[name] = def
		}
	}
	for name, def := range envSchema.Enums {
		schema.Enums[name] = def
	}
	for name, def := range envSchema.Scalars {
		if _, ok := schema.Scalars[name]; !ok {
			schema.Scalars[name] = def
		}
	}
	cfg.Outputs = map[string]any{
		"types": map[string]any{"rust": map[string]any{"enabled": true}},
		"api":   map[string]any{"enabled": true, "language": "RUST"},
	}

	outputRoot := testpaths.TempDir(t)
	if _, err := Run(schema, cfg, Options{
		OutputRoot: outputRoot,
		Naming:     naming.Default(),
		Paths:      paths,
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(tsFixtures, name))
		},
	}); err != nil {
		t.Fatalf("Run: %v", err)
	}

	apiDir := APIDir(outputRoot, cfg.Name)
	for _, file := range []string{"openapi.json", filepath.Join("src", "config.rs")} {
		if _, err := os.Stat(filepath.Join(apiDir, file)); err != nil {
			t.Fatalf("the build wrote no %s: %v", file, err)
		}
	}
	lib, err := os.ReadFile(filepath.Join(apiDir, "src", "lib.rs"))
	if err != nil || !strings.Contains(string(lib), "pub mod config;") {
		t.Fatalf("lib.rs = %q, %v; want it to declare config", lib, err)
	}

	crate := strings.ReplaceAll(naming.Default().RustAPICrate(cfg.Name), "-", "_")
	test := `#[test]
fn load_config_names_the_first_required_variable_it_lacks() {
    let err = ` + crate + `::config::load_config().unwrap_err();
    assert!(err.to_string().contains("SERVICE_NAME"), "{err}");
}
`
	if err := os.MkdirAll(filepath.Join(apiDir, "tests"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "tests", "config.rs"), []byte(test), 0o644); err != nil {
		t.Fatal(err)
	}
	targetDir := os.Getenv("CARGO_TARGET_DIR")
	if targetDir == "" {
		targetDir = filepath.Join(t.TempDir(), "target")
	}
	cmd := exec.Command(cargoPath, "test", "--quiet", "--test", "config")
	cmd.Dir = apiDir
	cmd.Env = append(os.Environ(), "CARGO_TARGET_DIR="+targetDir)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("cargo test: %v\n%s", err, out)
	}
}
