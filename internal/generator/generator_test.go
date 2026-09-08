package generator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/profile"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

const tsFixtures = "../loader/tsreader/testdata/services"

func TestRunDBSchemaReportsKindImpliedOutputs(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-db"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}

	outputRoot := t.TempDir()
	result, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Naming: naming.Default()})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// fixture-db enables go + typescript types; sql and orm are implied by
	// the DB kind. All DB-schema outputs are now ported, so nothing is
	// skipped.
	if len(result.Skipped) != 0 {
		t.Errorf("expected no skipped outputs, got %v", result.Skipped)
	}

	wantSQLDir := SQLDir(outputRoot, "fixture-db")
	if result.Outputs["sql"] != wantSQLDir {
		t.Errorf("expected sql output at %q, got %q", wantSQLDir, result.Outputs["sql"])
	}
	if _, err := os.Stat(filepath.Join(wantSQLDir, "create.sql")); err != nil {
		t.Errorf("expected generated create.sql: %v", err)
	}

	wantORMDir := ORMDir(outputRoot, "fixture-db")
	if result.Outputs["orm"] != wantORMDir {
		t.Errorf("expected orm output at %q, got %q", wantORMDir, result.Outputs["orm"])
	}
	if _, err := os.Stat(filepath.Join(wantORMDir, "repository_tenant.go")); err != nil {
		t.Errorf("expected generated repository_tenant.go: %v", err)
	}

	wantTypesDir := TypesDir(outputRoot, "go", "fixture-db")
	if result.Outputs["types-go"] != wantTypesDir {
		t.Errorf("expected types-go output at %q, got %q", wantTypesDir, result.Outputs["types-go"])
	}
	if _, err := os.Stat(filepath.Join(wantTypesDir, "types.go")); err != nil {
		t.Errorf("expected generated types.go: %v", err)
	}

	wantTSDir := TypesDir(outputRoot, "typescript", "fixture-db")
	if result.Outputs["types-typescript"] != wantTSDir {
		t.Errorf("expected types-typescript output at %q, got %q", wantTSDir, result.Outputs["types-typescript"])
	}
	if _, err := os.Stat(filepath.Join(wantTSDir, "types", "types.ts")); err != nil {
		t.Errorf("expected generated types/types.ts: %v", err)
	}
}

func TestRunAPISchemaSelectsAPIOutputs(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-api"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}

	outputRoot := t.TempDir()
	result, err := Run(schema, cfg, Options{
		OutputRoot: outputRoot,
		Naming:     naming.Default(),
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(tsFixtures, name))
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// fixture-api enables typescript types and the Go REST API; nothing is
	// skipped now that the API generators are ported.
	if len(result.Skipped) != 0 {
		t.Errorf("expected no skipped outputs, got %v", result.Skipped)
	}
	if _, ok := result.Outputs["sql"]; ok {
		t.Errorf("API schema must not select DB outputs, got %v", result.Outputs)
	}

	wantAPIDir := APIDir(outputRoot, "fixture-api")
	if result.Outputs["api"] != wantAPIDir {
		t.Errorf("expected api output at %q, got %q", wantAPIDir, result.Outputs["api"])
	}
	if _, err := os.Stat(filepath.Join(wantAPIDir, "routes.go")); err != nil {
		t.Errorf("expected generated routes.go: %v", err)
	}

	wantTSDir := TypesDir(outputRoot, "typescript", "fixture-api")
	if result.Outputs["types-typescript"] != wantTSDir {
		t.Errorf("expected types-typescript output at %q, got %q", wantTSDir, result.Outputs["types-typescript"])
	}

	wantSDKDir := SDKDir(outputRoot, "typescript", "fixture-api")
	if result.Outputs["sdk-typescript"] != wantSDKDir {
		t.Errorf("expected sdk-typescript output at %q, got %q", wantSDKDir, result.Outputs["sdk-typescript"])
	}
	if _, err := os.Stat(filepath.Join(wantSDKDir, "tools", "anthropic.json")); err != nil {
		t.Errorf("expected generated tools/anthropic.json: %v", err)
	}
}

func TestExpectedOutputDirsIncludesStandaloneEnvConfigDir(t *testing.T) {
	cfg := &schemaconfig.SchemaConfig{
		Name: "fixture-env",
		Kind: ir.SchemaKindGeneral,
		Outputs: map[string]any{
			"types": map[string]any{
				"go": map[string]any{"enabled": true},
			},
		},
	}

	outputRoot := t.TempDir()
	dirs, err := ExpectedOutputDirs(outputRoot, cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("ExpectedOutputDirs: %v", err)
	}

	want := []string{
		TypesDir(outputRoot, "go", "fixture-env"),
		APIDir(outputRoot, "fixture-env"),
	}
	if strings.Join(dirs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("expected output dirs %v, got %v", want, dirs)
	}
}

func TestRunMemoizesDependencyLoadsWithinRun(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-api"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}

	calls := make(map[string]int)
	_, err = Run(schema, cfg, Options{
		OutputRoot: t.TempDir(),
		Naming:     naming.Default(),
		LoadDependency: func(name string) (*ir.Schema, error) {
			calls[name]++
			return loader.LoadService(filepath.Join(tsFixtures, name))
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	if calls["fixture-db"] != 1 {
		t.Fatalf("expected fixture-db to load once, got %d calls: %v", calls["fixture-db"], calls)
	}
	if len(calls) != 1 {
		t.Fatalf("expected only fixture-db dependency load, got %v", calls)
	}
}

func TestRunMemoizesAPIOutputWithinRun(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-api"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}

	var profiles bytes.Buffer
	_, err = Run(schema, cfg, Options{
		OutputRoot: t.TempDir(),
		Naming:     naming.Default(),
		Profile:    profile.New("fixture-api", &profiles),
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(tsFixtures, name))
		},
	})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	count := strings.Count(profiles.String(), "phase=generator.build-api-output")
	if count != 1 {
		t.Fatalf("expected one build-api-output profile line, got %d:\n%s", count, profiles.String())
	}
	if !strings.Contains(profiles.String(), "phase=generator.output.types-typescript.write") {
		t.Fatalf("expected typescript writer sub-phase profile line:\n%s", profiles.String())
	}
	if !strings.Contains(profiles.String(), "phase=generator.codegen.format.typescript") {
		t.Fatalf("expected aggregate typescript format profile line:\n%s", profiles.String())
	}
	if !strings.Contains(profiles.String(), "phase=generator.output.types-typescript.codegen.format.typescript") {
		t.Fatalf("expected output-scoped typescript format profile line:\n%s", profiles.String())
	}
	if !strings.Contains(profiles.String(), "phase=generator.output.api.codegen.format.go") {
		t.Fatalf("expected output-scoped go API format profile line:\n%s", profiles.String())
	}
	if !strings.Contains(profiles.String(), "phase=generator.output.sdk-typescript.write") {
		t.Fatalf("expected typescript sdk writer sub-phase profile line:\n%s", profiles.String())
	}
}

func TestGenerateGoAPIDoesNotMutateCachedAPIOutput(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-api"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	outputs, err := ParseOutputs(cfg.Outputs)
	if err != nil {
		t.Fatalf("ParseOutputs: %v", err)
	}

	opts := Options{
		OutputRoot:    t.TempDir(),
		ServicePath:   filepath.Join(tsFixtures, "fixture-api"),
		ScalarLibPath: filepath.Join(t.TempDir(), "scalar-lib"),
		Naming:        naming.Default(),
		LoadDependency: func(name string) (*ir.Schema, error) {
			return loader.LoadService(filepath.Join(tsFixtures, name))
		},
	}
	r, memo := newRun(schema, cfg, outputs, opts, CoreRegistry(opts.Naming))

	cachedOutput, err := r.APIOutput()
	if err != nil {
		t.Fatalf("APIOutput: %v", err)
	}
	if cachedOutput == nil {
		t.Fatal("expected fixture-api to produce API output")
	}

	if err := r.generateGoAPI(); err != nil {
		t.Fatalf("generateGoAPI: %v", err)
	}
	if memo.apiOutput != cachedOutput {
		t.Fatal("expected cached API output pointer to remain unchanged")
	}
	if cachedOutput.EnvConfig != nil {
		t.Fatal("expected cached API output EnvConfig to stay nil")
	}
	if cachedOutput.ScalarLibReplacePath != "" ||
		cachedOutput.SchemaIRReplacePath != "" ||
		cachedOutput.HTTPRuntimeReplacePath != "" ||
		cachedOutput.SchemaRuntimeReplacePath != "" ||
		cachedOutput.PtrReplacePath != "" {
		t.Fatalf("expected cached API output replace paths to stay empty: %+v", cachedOutput)
	}
}

func TestRunLoadDependencyErrorNotCached(t *testing.T) {
	wantErr := errors.New("load failed")
	depSchema := ir.NewSchema("fixture-db", ir.SchemaKindDB)
	calls := 0

	r, _ := newRun(nil, nil, nil, Options{
		LoadDependency: func(name string) (*ir.Schema, error) {
			calls++
			if calls == 1 {
				return nil, wantErr
			}
			return depSchema, nil
		},
	}, nil)

	if _, err := r.LoadDependency("fixture-db"); !errors.Is(err, wantErr) {
		t.Fatalf("expected first load error %v, got %v", wantErr, err)
	}

	loaded, err := r.LoadDependency("fixture-db")
	if err != nil {
		t.Fatalf("expected second load to retry and succeed: %v", err)
	}
	if loaded != depSchema {
		t.Fatalf("expected loaded schema pointer %p, got %p", depSchema, loaded)
	}
	if calls != 2 {
		t.Fatalf("expected failed load not to be cached, got %d calls", calls)
	}

	loaded, err = r.LoadDependency("fixture-db")
	if err != nil {
		t.Fatalf("expected cached load to succeed: %v", err)
	}
	if loaded != depSchema {
		t.Fatalf("expected cached schema pointer %p, got %p", depSchema, loaded)
	}
	if calls != 2 {
		t.Fatalf("expected successful load to be cached, got %d calls", calls)
	}
}

func TestRunRequiresOutputRoot(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-db"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	if _, err := Run(schema, cfg, Options{}); err == nil {
		t.Fatal("expected error for missing output root")
	}
}

// TestExpectedOutputDirsFollowsPipelineEnabled pins that the cache surface is
// the registry pipeline filtered by each generator's Enabled check: an API
// kind whose api output is off contributes no api directory, and the api
// directory precedes the sdk directory because that is the pipeline order.
func TestExpectedOutputDirsFollowsPipelineEnabled(t *testing.T) {
	outputRoot := t.TempDir()

	cfg := &schemaconfig.SchemaConfig{
		Name: "fixture-api",
		Kind: ir.SchemaKindAPI,
		Outputs: map[string]any{
			"types": map[string]any{"go": map[string]any{"enabled": true}},
			"api":   map[string]any{"enabled": false},
			"sdk":   map[string]any{"typescript": map[string]any{"enabled": true}},
		},
	}
	dirs, err := ExpectedOutputDirs(outputRoot, cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("ExpectedOutputDirs: %v", err)
	}
	want := []string{
		TypesDir(outputRoot, "go", "fixture-api"),
		SDKDir(outputRoot, "typescript", "fixture-api"),
	}
	if strings.Join(dirs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("api disabled: expected output dirs %v, got %v", want, dirs)
	}

	cfg.Outputs["api"] = map[string]any{"enabled": true}
	dirs, err = ExpectedOutputDirs(outputRoot, cfg, t.TempDir(), nil)
	if err != nil {
		t.Fatalf("ExpectedOutputDirs: %v", err)
	}
	want = []string{
		TypesDir(outputRoot, "go", "fixture-api"),
		APIDir(outputRoot, "fixture-api"),
		SDKDir(outputRoot, "typescript", "fixture-api"),
	}
	if strings.Join(dirs, "\n") != strings.Join(want, "\n") {
		t.Fatalf("api enabled: expected output dirs %v, got %v", want, dirs)
	}
}

// documentRegistry is the core plus one sidecar document spec whose Generate
// records its call and writes a marker file.
func documentRegistry(t *testing.T, calls *[]string) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Default())
	if err := RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	err := reg.RegisterDocument(registry.DocumentSpec{
		Name:      "manifest",
		Extension: "acme",
		File:      "manifest.config.ts",
		Loader: func(context.Context, registry.LoadContext) (json.RawMessage, []string, error) {
			return json.RawMessage(`{"replicas":2}`), nil, nil
		},
		Dirs: func(c registry.GenerateContext) []string {
			return []string{filepath.Join(c.Options.OutputRoot, "manifest", c.Config.Name)}
		},
		Generate: func(c registry.GenerateContext, doc json.RawMessage) error {
			*calls = append(*calls, string(doc))
			dir := filepath.Join(c.Options.OutputRoot, "manifest", c.Config.Name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			c.Done("manifest", dir)
			return os.WriteFile(filepath.Join(dir, "manifest.json"), doc, 0o644)
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return reg
}

// TestRunExecutesRegisteredDocumentGenerators pins the presence-driven
// document dispatch: a registered document present in Schema.Documents runs
// its Generate after the kind pipeline, and an absent one does not.
func TestRunExecutesRegisteredDocumentGenerators(t *testing.T) {
	var calls []string
	reg := documentRegistry(t, &calls)
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-general"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}

	outputRoot := t.TempDir()
	result, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(calls) != 0 {
		t.Fatalf("document generator ran without a document: %v", calls)
	}
	if _, ok := result.Outputs["manifest"]; ok {
		t.Fatal("manifest output recorded without a document")
	}

	if err := ir.SetDocument(schema, "manifest", map[string]any{"replicas": 2}); err != nil {
		t.Fatal(err)
	}
	result, err = Run(schema, cfg, Options{OutputRoot: outputRoot, Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	wantDir := filepath.Join(outputRoot, "manifest", "fixture-general")
	if len(calls) != 1 || calls[0] != `{"replicas":2}` {
		t.Errorf("document generator calls = %v, want the canonical document once", calls)
	}
	if result.Outputs["manifest"] != wantDir {
		t.Errorf("Outputs[manifest] = %q, want %q", result.Outputs["manifest"], wantDir)
	}
	if _, err := os.Stat(filepath.Join(wantDir, "manifest.json")); err != nil {
		t.Errorf("expected the document generator's file: %v", err)
	}
}

// TestExpectedOutputDirsIncludesRegisteredDocumentDirs pins that a service
// carrying a registered document's sidecar reports the document's Dirs as
// cache surface, and a service without the sidecar does not.
func TestExpectedOutputDirsIncludesRegisteredDocumentDirs(t *testing.T) {
	var calls []string
	reg := documentRegistry(t, &calls)
	cfg := &schemaconfig.SchemaConfig{Name: "fixture-env", Kind: ir.SchemaKindGeneral}
	outputRoot := t.TempDir()
	want := filepath.Join(outputRoot, "manifest", "fixture-env")

	without, err := ExpectedOutputDirs(outputRoot, cfg, t.TempDir(), reg)
	if err != nil {
		t.Fatalf("ExpectedOutputDirs: %v", err)
	}
	if slices.Contains(without, want) {
		t.Fatalf("output dirs %v include the document dir without its sidecar", without)
	}

	serviceDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(serviceDir, "manifest.config.ts"), []byte("export default {};\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	with, err := ExpectedOutputDirs(outputRoot, cfg, serviceDir, reg)
	if err != nil {
		t.Fatalf("ExpectedOutputDirs: %v", err)
	}
	if !slices.Contains(with, want) {
		t.Fatalf("expected %q in output dirs, got %v", want, with)
	}
	if with[0] != want {
		t.Errorf("document dirs come first (the cache surface order the deploy family had): got %v", with)
	}
}
