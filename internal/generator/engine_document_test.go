package generator

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/enginedoc"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// metaSchemaStandIn is the engine document's meta-schema in the goldens,
// so they do not change whenever the IR does.
const metaSchemaStandIn = `"<the JSON Schema superschematic json-schema prints>"`

// withStandIn returns the engine document data with its meta-schema
// replaced by the stand-in, and checks that the meta-schema is the
// registry's: what superschematic json-schema prints.
func withStandIn(t *testing.T, data []byte, reg *registry.Registry) []byte {
	t.Helper()
	var doc enginedoc.Document
	if err := json.Unmarshal(data, &doc); err != nil {
		t.Fatalf("decode the engine document: %v", err)
	}
	definition, err := schemafile.DefinitionFor(reg)
	if err != nil {
		t.Fatal(err)
	}
	var got, want any
	if err := json.Unmarshal(doc.MetaSchema, &got); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(definition, &want); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("the engine document's meta-schema is not what superschematic json-schema prints for the registry")
	}
	doc.MetaSchema = json.RawMessage(metaSchemaStandIn)
	standIn, err := doc.JSON()
	if err != nil {
		t.Fatal(err)
	}
	return standIn
}

// TestEngineDocumentFixture builds the engine fixture and holds its engine
// document, engine/fixture-engine-db.json in the Go types module, and the
// constants the Go, TypeScript and Rust types hold it as, with the files
// that wire them in, to the goldens in testdata/engine-document. The
// meta-schema is checked against the registry's and stands in as a string
// in the goldens. Rewrite them with:
//
//	go test ./internal/generator -run TestEngineDocumentFixture -update
func TestEngineDocumentFixture(t *testing.T) {
	root, reg, result := buildEngineFixture(t)
	documentPath := EngineDocumentPath(root, engineFixture)
	if result.Outputs["engine"] != documentPath {
		t.Fatalf("Outputs[engine] = %q, want %q (outputs %v)", result.Outputs["engine"], documentPath, result.Outputs)
	}
	data, err := os.ReadFile(documentPath)
	if err != nil {
		t.Fatal(err)
	}
	standIn := withStandIn(t, data, reg)
	text := strings.TrimSuffix(string(data), "\n")
	standInText := strings.TrimSuffix(string(standIn), "\n")

	goldens := map[string][]byte{engineFixture + ".json": standIn}
	// Each language's constant holds the document's text as it is: with
	// the stand-in in its place, the constant is the golden.
	for name, path := range map[string]string{
		"engine.go.golden": filepath.Join(TypesDir(root, LangGo, engineFixture), "engine.go"),
		"engine.ts.golden": filepath.Join(TypesDir(root, LangTypeScript, engineFixture), "engine.ts"),
		"engine.rs.golden": filepath.Join(TypesDir(root, LangRust, engineFixture), "src", "engine.rs"),
	} {
		constant, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(constant), text) {
			t.Fatalf("%s does not hold the engine document's text", path)
		}
		goldens[name] = []byte(strings.Replace(string(constant), text, standInText, 1))
	}
	for name, path := range map[string]string{
		"types-go-README.md.golden":    filepath.Join(TypesDir(root, LangGo, engineFixture), "README.md"),
		"types-rust-lib.rs.golden":     filepath.Join(TypesDir(root, LangRust, engineFixture), "src", "lib.rs"),
		"types-ts-package.json.golden": filepath.Join(TypesDir(root, LangTypeScript, engineFixture), "package.json"),
	} {
		content, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		goldens[name] = content
	}
	checkEngineGoldens(t, goldens)

	// index.ts does not re-export the document, so a client of the types
	// does not load it; "./engine" does.
	index, err := os.ReadFile(filepath.Join(TypesDir(root, LangTypeScript, engineFixture), "index.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(index), "engineDocument") || strings.Contains(string(index), "'./engine'") {
		t.Errorf("index.ts names the engine document:\n%s", index)
	}
}

// outputs.engine.retention is the engine's RetentionOptions, checked when
// the build reads the config as the engine checks them when it opens, so
// that no entrypoint opens an engine that refuses its retention: an age, a
// count or both, every member an integer in its range, no other member.
// The servergen goldens carry desk-db's into every entrypoint.
func TestEngineRetentionSetting(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(engineFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	for _, retention := range []map[string]any{
		{"maxAgeMs": 2592000000},
		{"maxEvents": 1},
		{"maxAgeMs": 86400000, "maxEvents": 100000, "maxHoldMs": 3600000, "everyMs": 1000, "batchSize": 10000},
	} {
		cfg.Outputs["engine"] = map[string]any{"retention": retention}
		if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Naming: naming.Default()}); err != nil {
			t.Errorf("retention %v: %v", retention, err)
		}
	}
	for _, retention := range []any{
		map[string]any{},
		map[string]any{"maxHoldMs": 1000},
		map[string]any{"maxAgeMs": 0},
		map[string]any{"maxAgeMs": 1.5},
		map[string]any{"maxEvents": "100"},
		map[string]any{"maxEvents": 10, "everyMs": 999},
		map[string]any{"maxEvents": 10, "batchSize": 10001},
		map[string]any{"maxEvents": 10, "keep": "all"},
		"30d",
	} {
		cfg.Outputs["engine"] = map[string]any{"retention": retention}
		if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Naming: naming.Default()}); err == nil || !strings.Contains(err.Error(), "outputs.engine") {
			t.Errorf("retention %v: err = %v, want outputs.engine refused", retention, err)
		}
	}
	delete(cfg.Outputs, "engine")
}

// outputs.engine.namespace names the namespace; a namespace the engine
// would refuse fails the build before any generator runs, naming the
// setting; a build that no longer has engine types removes the document.
func TestEngineNamespaceSetting(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(engineFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Outputs["engine"] = map[string]any{"namespace": "support"}
	root := t.TempDir()
	if _, err := Run(schema, cfg, Options{OutputRoot: root, Naming: naming.Default()}); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(EngineDocumentPath(root, engineFixture))
	if err != nil {
		t.Fatal(err)
	}
	var doc enginedoc.Document
	if err := json.Unmarshal(data, &doc); err != nil || doc.Namespace != "support" {
		t.Fatalf("namespace = %q (%v), want support", doc.Namespace, err)
	}

	cfg.Outputs["engine"] = map[string]any{"namespace": "Support"}
	refused := t.TempDir()
	if _, err := Run(schema, cfg, Options{OutputRoot: refused, Naming: naming.Default()}); err == nil ||
		!strings.HasPrefix(err.Error(), `generator: outputs.engine.namespace "Support" is not an engine namespace name`) {
		t.Fatalf("Run: err = %v, want the namespace refused", err)
	}
	if _, err := os.Stat(SQLDir(refused, engineFixture)); !os.IsNotExist(err) {
		t.Errorf("sql ran before the namespace was refused: %v", err)
	}
	cfg.Outputs["engine"] = map[string]any{"namespaces": "support"}
	if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Naming: naming.Default()}); err == nil || !strings.Contains(err.Error(), "outputs.engine") {
		t.Fatalf("Run: err = %v, want outputs.engine refused", err)
	}

	for _, td := range schema.Types {
		td.Behaviors = nil
	}
	delete(cfg.Outputs, "engine")
	if _, err := Run(schema, cfg, Options{OutputRoot: root, Naming: naming.Default()}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Dir(EngineDocumentPath(root, engineFixture))); !os.IsNotExist(err) {
		t.Errorf("the stale engine document is still there: %v", err)
	}
	if _, err := os.Stat(filepath.Join(TypesDir(root, LangGo, engineFixture), "engine.go")); !os.IsNotExist(err) {
		t.Errorf("the stale engine.go is still there: %v", err)
	}
}

// The engine fixture's Go types module, which holds EngineDocument, and
// its ORM, which has no repository for an engine type, compile.
func TestEngineFixtureGoModulesCompile(t *testing.T) {
	if testing.Short() {
		t.Skip("compiles generated modules")
	}
	schema, cfg, err := loader.LoadServiceWithConfig(engineFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	root := testpaths.TempDir(t)
	if _, err := Run(schema, cfg, Options{OutputRoot: root, Paths: testpaths.Local(t), Naming: naming.Default()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	for _, dir := range []string{TypesDir(root, LangGo, engineFixture), ORMDir(root, engineFixture)} {
		for _, args := range [][]string{{"mod", "tidy"}, {"build", "./..."}, {"vet", "./..."}} {
			cmd := exec.Command("go", args...)
			cmd.Dir = dir
			if out, err := cmd.CombinedOutput(); err != nil {
				t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
			}
		}
	}
}
