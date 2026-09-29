package generator

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestTypeScriptEnvLoader generates fixture-env the way build does, so its
// @envVars class gets config.ts in the TypeScript types package, then runs
// testdata/typescript-env-loader/loader.test.ts under Bun. That test imports
// the loader through the package's "./config" export and loads it against
// env maps: parsing, defaults, missing and invalid variables named in the
// error, and secrets absent from every serialized or inspected form.
func TestTypeScriptEnvLoader(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TypeScript gate in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	names := naming.Default()

	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-env"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	// Resolve symlinks (macOS /var -> /private/var) so the relative file:
	// superscalar spec resolves at install time.
	outputRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	result, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Paths: paths, Naming: names})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	typesDir := TypesDir(outputRoot, LangTypeScript, "fixture-env")
	if result.Outputs["env-config-typescript"] != typesDir {
		t.Fatalf("env-config-typescript output = %q, want %q (outputs %v)",
			result.Outputs["env-config-typescript"], typesDir, result.Outputs)
	}

	install := exec.Command(bunPath, "install")
	install.Dir = typesDir
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed for fixture-env types (likely offline): %v\n%s", err, out))
	}

	consumer := filepath.Join(outputRoot, "consumer")
	packageLink := filepath.Join(consumer, "node_modules", names.NpmTypesPackage("fixture-env"))
	if err := os.MkdirAll(filepath.Dir(packageLink), 0o755); err != nil {
		t.Fatalf("create consumer node_modules: %v", err)
	}
	if err := os.Symlink(typesDir, packageLink); err != nil {
		t.Fatalf("link fixture-env types into the consumer: %v", err)
	}
	loaderTest, err := os.ReadFile(filepath.Join("testdata", "typescript-env-loader", "loader.test.ts"))
	if err != nil {
		t.Fatalf("read loader test: %v", err)
	}
	if err := os.WriteFile(filepath.Join(consumer, "loader.test.ts"), loaderTest, 0o644); err != nil {
		t.Fatalf("write loader test: %v", err)
	}

	run := exec.Command(bunPath, "test", "./loader.test.ts")
	run.Dir = consumer
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("generated TypeScript env loader failed its behaviour tests: %v\n%s", err, out)
	}
	t.Logf("%s", out)

	typecheck := exec.Command(bunPath, "x", "tsc", "--noEmit")
	typecheck.Dir = typesDir
	if out, err := typecheck.CombinedOutput(); err != nil {
		t.Fatalf("generated fixture-env package with config.ts does not type-check: %v\n%s", err, out)
	}
}
