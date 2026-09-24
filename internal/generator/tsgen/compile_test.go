package tsgen

import (
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedPackagesCompile generates the TypeScript type packages for
// the fixture services into a temp tree wired against the real superscalar
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
			t.Fatalf("set superscalar spec for %s: %v", tc.name, err)
		}
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
	}
	// Mirror the output layout: the directory holding the packages is a Bun
	// workspace root, so fixture-api's file:../fixture-db dependency and
	// fixture-db's file: superscalar spec resolve from either package.
	if err := WriteWorkspaceRoot(tempRoot, naming.Naming{}); err != nil {
		t.Fatalf("write workspace root: %v", err)
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

// TestNullableObjectMapParserCompiles type-checks a generated package with
// an optional map of a nested type. Its values are `T | null` in the public
// type, so parse<Type>FromJSON must map a null or absent entry to null; it
// returned the raw `unknown` entry, which does not type-check.
func TestNullableObjectMapParserCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		requireOrSkipTSTooling(t, "bun unavailable")
	}
	paths := testpaths.Local(t)
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-maps"))
	if err != nil {
		t.Fatal(err)
	}
	container := schema.Types["MapContainer"]
	container.Fields = append(container.Fields, &ir.FieldDef{
		Name: "receipts", TypeRef: ir.TypeRef{Name: "MapValue", IsMap: true},
	})
	output, err := Generate(schema, Options{SchemaName: "fixture-maps", Clock: codegen.FixedClock(time.Unix(0, 0).UTC())})
	if err != nil {
		t.Fatal(err)
	}
	tempRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tempRoot, "fixture-maps")
	if err := SetScalarLibSpec(output, paths, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteTypes(output, dir); err != nil {
		t.Fatal(err)
	}
	if err := WriteWorkspaceRoot(tempRoot, naming.Naming{}); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bunPath, "install")
	install.Dir = dir
	if out, err := install.CombinedOutput(); err != nil {
		requireOrSkipTSTooling(t, "bun install failed (likely offline): "+err.Error()+"\n"+string(out))
	}
	check := exec.Command(bunPath, "x", "tsc", "--noEmit")
	check.Dir = dir
	if out, err := check.CombinedOutput(); err != nil {
		t.Fatalf("generated nullable object map parser does not type-check: %v\n%s", err, out)
	}
}
