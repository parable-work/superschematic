package sdkgen

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// requireOrSkipTSTooling decides what a missing TypeScript toolchain means.
// Locally it stays a skip so `go test ./...` runs without bun. CI sets
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1, where a skip is exactly the failure being guarded
// against: TestGeneratedSDKCompiles skipped silently on a bad superscalar path.
func requireOrSkipTSTooling(t *testing.T, reason string) {
	t.Helper()
	if os.Getenv("SUPERSCHEMATIC_REQUIRE_TS_CHECKS") == "1" {
		t.Fatalf("SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1 requires this TypeScript gate to run: %s", reason)
	}
	t.Skipf("skipping TypeScript gate: %s", reason)
}

// TestGeneratedSDKCompiles generates the fixture-api TypeScript SDK wired
// against a locally built types package and runs tsc on it. This catches
// broken imports and invalid syntax in SDK templates that golden comparison
// alone would bless. Skips when bun is unavailable.
func TestGeneratedSDKCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, fmt.Sprintf("bun not available: %v", err))
	}

	paths := testpaths.Local(t)

	// Resolve symlinks (macOS /var -> /private/var) so the relative file:
	// spec computed against the temp dir resolves correctly at install time.
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	schema, apiOutput, parseable := loadFixtureAPI(t)

	upstream, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}

	// Build the type packages the SDK resolves its peer dependency against,
	// in dependency order: fixture-api's compiled dist/ needs fixture-db's.
	typesCases := []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{name: "fixture-db", schema: upstream},
		{name: "fixture-api", schema: schema, deps: map[string]*ir.Schema{"fixture-db": upstream}},
	}
	for _, tc := range typesCases {
		tsOutput, err := tsgen.Generate(tc.schema, tsgen.Options{
			SchemaName:   tc.name,
			Dependencies: tc.deps,
			DependencyPackages: map[string]string{
				"fixture-db": naming.Default().NpmTypesPackage("fixture-db"),
			},
			Clock: fixedClock,
		})
		if err != nil {
			t.Fatalf("tsgen.Generate %s: %v", tc.name, err)
		}
		typesDir := filepath.Join(tempRoot, "types", "typescript", tc.name)
		if err := tsgen.SetScalarLibSpec(tsOutput, paths, typesDir); err != nil {
			t.Fatalf("set superscalar spec for %s: %v", tc.name, err)
		}
		if err := tsgen.WriteTypes(tsOutput, typesDir); err != nil {
			t.Fatalf("write types %s: %v", tc.name, err)
		}
		// Mirror the output layout: types/typescript is a Bun workspace root.
		if err := tsgen.WriteWorkspaceRoot(filepath.Dir(typesDir), naming.Naming{}); err != nil {
			t.Fatalf("write workspace root: %v", err)
		}

		install := exec.Command(bunPath, "install")
		install.Dir = typesDir
		if out, err := install.CombinedOutput(); err != nil {
			requireOrSkipTSTooling(t, fmt.Sprintf("bun install failed for types %s (likely offline): %v\n%s", tc.name, err, out))
		}
		build := exec.Command(bunPath, "x", "tsc")
		build.Dir = typesDir
		if out, err := build.CombinedOutput(); err != nil {
			t.Fatalf("types package %s does not type-check: %v\n%s", tc.name, err, out)
		}
	}

	sdkOutput, err := Generate(apiOutput, parseable, fixedClock)
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	// The SDK package.json carries a file: dependency on the types package
	// at the dist-relative location, so the temp tree mirrors the dist
	// layout: sdk/typescript/<name> resolves ../../../types/typescript/<name>.
	sdkDir := filepath.Join(tempRoot, "sdk", "typescript", "fixture-api")
	if err := WriteSDKWithTools(sdkOutput, apiOutput, sdkDir, fixedClock); err != nil {
		t.Fatalf("WriteSDKWithTools: %v", err)
	}

	if err := CompileSDK(sdkDir, filepath.Join(tempRoot, "types", "typescript", "fixture-api")); err != nil {
		t.Errorf("generated SDK does not type-check: %v", err)
	}
}
