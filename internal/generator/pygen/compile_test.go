package pygen

import (
	"fmt"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestGeneratedPackagesCompile generates the Python type packages for the
// fixture services into a temp tree, byte-compiles them, and import-smokes
// each module with the sibling packages on sys.path. This is the cheap
// end-to-end check for the pygen port. Skips when python3 (or pydantic, for
// the import smoke) is unavailable.
func TestGeneratedPackagesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}

	pythonPath, err := exec.LookPath("python3")
	if err != nil {
		t.Skip("python3 not available; skipping Python compile check")
	}

	tempRoot := t.TempDir()
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))

	dbSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatalf("load fixture-db: %v", err)
	}
	apiSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-api"))
	if err != nil {
		t.Fatalf("load fixture-api: %v", err)
	}
	generalSchema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-general"))
	if err != nil {
		t.Fatalf("load fixture-general: %v", err)
	}

	type fixtureCase struct {
		name   string
		schema *ir.Schema
		deps   map[string]*ir.Schema
	}
	cases := []fixtureCase{
		{name: "fixture-db", schema: dbSchema},
		{name: "fixture-api", schema: apiSchema, deps: map[string]*ir.Schema{"fixture-db": dbSchema}},
		{name: "fixture-general", schema: generalSchema},
	}
	for _, name := range []string{"fixture-nested-arrays", "fixture-nested-arrays-db", "fixture-nested-arrays-api"} {
		schema, err := loader.LoadService(filepath.Join(fixturesDir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		cases = append(cases, fixtureCase{name: name, schema: schema})
	}

	var importPaths []string
	var moduleNames []string
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
		if err := WriteTypes(output, outDir); err != nil {
			t.Fatalf("write %s: %v", tc.name, err)
		}
		importPaths = append(importPaths, outDir)
		moduleNames = append(moduleNames, output.PythonModuleName)
	}

	compile := exec.Command(pythonPath, "-m", "compileall", "-q", tempRoot)
	if out, err := compile.CombinedOutput(); err != nil {
		t.Fatalf("generated packages do not byte-compile: %v\n%s", err, out)
	}

	probe := exec.Command(pythonPath, "-c", "import pydantic")
	if err := probe.Run(); err != nil {
		t.Skip("pydantic not available; skipping import smoke test")
	}

	for _, moduleName := range moduleNames {
		smoke := exec.Command(pythonPath, "-c", buildImportSmokeScript(importPaths, moduleName))
		if out, err := smoke.CombinedOutput(); err != nil {
			t.Errorf("import smoke test failed for %s: %v\n%s", moduleName, err, out)
		}
	}

	// fixture-db's Tenant.metadata is a required Generic.JSON field.
	jsonProbe := exec.Command(pythonPath, "-c", buildGenericJSONProbeScript(importPaths, moduleNames[0]))
	if out, err := jsonProbe.CombinedOutput(); err != nil {
		t.Errorf("Generic.JSON runtime probe failed: %v\n%s", err, out)
	}
}

// buildGenericJSONProbeScript checks the generated Generic.JSON field: every
// JSON root, null included, is a valid value that validate_all accepts; an
// absent required field is rejected; host values JSON cannot represent (a
// set, a tuple, a non-string key, NaN, infinity, an arbitrary object) are
// rejected.
func buildGenericJSONProbeScript(importPaths []string, moduleName string) string {
	var b strings.Builder
	b.WriteString("import datetime\nimport sys\n")
	b.WriteString("from pydantic import ValidationError\n")
	for _, importPath := range importPaths {
		fmt.Fprintf(&b, "sys.path.insert(0, %q)\n", importPath)
	}
	fmt.Fprintf(&b, "from %s.types import Tenant\n", moduleName)
	b.WriteString("base = dict(createdAt=datetime.datetime(2026, 1, 2, tzinfo=datetime.timezone.utc), name='Acme', slug='acme', email='agent@example.com')\n")
	b.WriteString("for root in [{'k': 1}, [1, 2], 'text', 42, 1.5, True, None]:\n")
	b.WriteString("    model = Tenant(**base, metadata=root)\n")
	b.WriteString("    assert 'metadata' not in model.validate_all().errors, root\n")
	b.WriteString("try:\n")
	b.WriteString("    Tenant(**base)\n")
	b.WriteString("except ValidationError:\n")
	b.WriteString("    pass\n")
	b.WriteString("else:\n")
	b.WriteString("    raise AssertionError('missing required Generic.JSON field was accepted')\n")
	b.WriteString("for value in [{1, 2}, (1, 2), {1: 'value'}, float('nan'), float('inf'), object()]:\n")
	b.WriteString("    try:\n")
	b.WriteString("        Tenant(**base, metadata=value)\n")
	b.WriteString("    except ValidationError:\n")
	b.WriteString("        pass\n")
	b.WriteString("    else:\n")
	b.WriteString("        raise AssertionError(f'invalid Generic.JSON value was accepted: {type(value).__name__}')\n")
	return b.String()
}

func buildImportSmokeScript(importPaths []string, moduleName string) string {
	var b strings.Builder
	b.WriteString("import importlib\nimport sys\n")
	for _, importPath := range importPaths {
		fmt.Fprintf(&b, "sys.path.insert(0, %q)\n", importPath)
	}
	fmt.Fprintf(&b, "importlib.import_module(%q)\n", moduleName)
	return b.String()
}
