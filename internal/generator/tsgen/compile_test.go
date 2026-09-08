package tsgen

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedPackagesCompile generates the TypeScript type packages for
// the fixture services into a temp tree wired against the real scalar-lib
// runtime and runs `tsc --noEmit` on each. This is the cheap end-to-end
// compile check for the tsgen port. Skips when bun is unavailable.
func TestGeneratedPackagesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	bunPath, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun not available; skipping TypeScript compile check")
	}

	paths := testpaths.Local(t)

	// Resolve symlinks (macOS /var -> /private/var) so the relative file:
	// spec computed against the temp dir resolves correctly at install time.
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve temp dir: %v", err)
	}
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}

	cases := []struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}{
		{name: "fixture-db", schema: dbSchema},
		{name: "fixture-api", schema: apiSchema, deps: map[string]*ir.Schema{"fixture-db": dbSchema}},
	}

	for _, tc := range cases {
		output, err := Generate(tc.schema, Options{
			SchemaName:   tc.name,
			Dependencies: tc.deps,
			Clock:        fixedClock,
		})
		if err != nil {
			t.Fatalf("generate %s: %v", tc.name, err)
		}

		outDir := filepath.Join(tempRoot, tc.name)
		if err := SetScalarLibSpec(output, paths, outDir); err != nil {
			t.Fatalf("set scalar-lib spec for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}

	// Install and build in dependency order: fixture-api resolves the
	// file:../fixture-db package through its compiled dist/ declarations.
	for _, tc := range cases {
		outDir := filepath.Join(tempRoot, tc.name)

		install := exec.Command(bunPath, "install")
		install.Dir = outDir
		if out, err := install.CombinedOutput(); err != nil {
			t.Skipf("bun install failed for %s (likely offline): %v\n%s", tc.name, err, out)
		}

		build := exec.Command(bunPath, "x", "tsc")
		build.Dir = outDir
		if out, err := build.CombinedOutput(); err != nil {
			t.Errorf("generated package %s does not type-check: %v\n%s", tc.name, err, out)
		}
	}
}
