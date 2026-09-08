package generator

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/profile"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

type widgetExtension struct {
	generate func(c registry.GenerateContext) error
}

func (widgetExtension) Name() string { return "acme" }

func (e widgetExtension) Register(r *registry.Registry) error {
	return errors.Join(
		r.RegisterKind(registry.KindSpec{Name: "Catalog", Extension: "acme", Pipeline: []string{"types", "widget"}}),
		r.RegisterGenerator(registry.GeneratorSpec{
			Name: "widget", Extension: "acme", Kinds: []string{"Catalog"}, OutputKey: "widget",
			Dirs: func(c registry.GenerateContext) []string {
				return []string{filepath.Join(c.Options.OutputRoot, "widget", c.Config.Name)}
			},
			Enabled: func(c registry.GenerateContext) (bool, string) {
				var o struct {
					Enabled bool `json:"enabled"`
				}
				if err := registry.DecodeOutput(c.Outputs, "widget", &o); err != nil || !o.Enabled {
					return false, "widget"
				}
				return true, ""
			},
			Generate: e.generate,
		}),
	)
}

func writeWidget(c registry.GenerateContext) error {
	dir := filepath.Join(c.Options.OutputRoot, "widget", c.Config.Name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "widget.txt"), []byte(c.Schema.Name+"\n"), 0o644); err != nil {
		return err
	}
	c.Result.Outputs["widget"] = dir
	return nil
}

func extensionRegistry(t *testing.T, exts ...registry.Extension) *registry.Registry {
	t.Helper()
	reg := registry.New(coreNaming())
	if err := RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.Use(exts...); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func catalogService(outputs map[string]any) (*ir.Schema, *schemaconfig.SchemaConfig) {
	schema := ir.NewSchema("shop", ir.SchemaKind("Catalog"))
	cfg := &schemaconfig.SchemaConfig{Name: "shop", Kind: ir.SchemaKind("Catalog"), Outputs: outputs}
	return schema, cfg
}

func TestRunExecutesAnExtensionKindRegisteredThroughUse(t *testing.T) {
	reg := extensionRegistry(t, widgetExtension{generate: writeWidget})
	schema, cfg := catalogService(map[string]any{"widget": map[string]any{"enabled": true}})

	outputRoot := t.TempDir()
	var log bytes.Buffer
	result, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Registry: reg, Log: &log})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	wantDir := filepath.Join(outputRoot, "widget", "shop")
	if result.Outputs["widget"] != wantDir {
		t.Errorf("Outputs[widget] = %q, want %q", result.Outputs["widget"], wantDir)
	}
	data, err := os.ReadFile(filepath.Join(wantDir, "widget.txt"))
	if err != nil {
		t.Fatalf("expected the extension generator's file: %v", err)
	}
	if string(data) != "shop\n" {
		t.Errorf("widget.txt = %q", data)
	}
	if len(result.Skipped) != 0 {
		t.Errorf("Skipped = %v, want none", result.Skipped)
	}
}

func TestRunSkipsDisabledExtensionGeneratorWithItsReason(t *testing.T) {
	reg := extensionRegistry(t, widgetExtension{generate: func(registry.GenerateContext) error {
		t.Fatal("disabled generator must not run")
		return nil
	}})
	schema, cfg := catalogService(map[string]any{"widget": map[string]any{"enabled": false}})

	result, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if !reflect.DeepEqual(result.Skipped, []string{"widget"}) {
		t.Errorf("Skipped = %v, want [widget]", result.Skipped)
	}
}

func TestRunRejectsUnknownKindAndUnknownOutputWithTodaysText(t *testing.T) {
	schema, cfg := catalogService(nil)

	_, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Naming: coreNaming()})
	if err == nil || err.Error() != `generator: unknown schema kind "Catalog"` {
		t.Fatalf("unknown kind error = %v", err)
	}

	reg := extensionRegistry(t, widgetExtension{generate: writeWidget})
	schema, cfg = catalogService(map[string]any{"graphql": map[string]any{"enabled": true}})
	_, err = Run(schema, cfg, Options{OutputRoot: t.TempDir(), Registry: reg})
	want := `schema config for shop: outputs block has unknown key "graphql" (expected types, api, sdk, widget)`
	if err == nil || err.Error() != want {
		t.Fatalf("unknown output error = %v, want %q", err, want)
	}

	_, err = Run(schema, cfg, Options{OutputRoot: t.TempDir(), Naming: coreNaming()})
	want = `schema config for shop: outputs block has unknown key "graphql" (expected types, api, sdk)`
	if err == nil || err.Error() != want {
		t.Fatalf("core-only unknown output error = %v, want %q", err, want)
	}
}

type overlapExtension struct{}

func (overlapExtension) Name() string { return "overlap" }

func (overlapExtension) Register(r *registry.Registry) error {
	sameDir := func(c registry.GenerateContext) []string {
		return []string{filepath.Join(c.Options.OutputRoot, "shared")}
	}
	mustNotRun := func(registry.GenerateContext) error {
		return errors.New("generator ran despite the overlap")
	}
	return errors.Join(
		r.RegisterKind(registry.KindSpec{Name: "Clash", Extension: "overlap", Pipeline: []string{"left", "right"}}),
		r.RegisterGenerator(registry.GeneratorSpec{Name: "left", Extension: "overlap", Dirs: sameDir, Generate: mustNotRun}),
		r.RegisterGenerator(registry.GeneratorSpec{Name: "right", Extension: "overlap", Dirs: sameDir, Generate: mustNotRun}),
	)
}

func TestRunRejectsPipelineWhoseGeneratorsShareAnOutputDir(t *testing.T) {
	reg := extensionRegistry(t, overlapExtension{})
	schema := ir.NewSchema("clash", ir.SchemaKind("Clash"))
	cfg := &schemaconfig.SchemaConfig{Name: "clash", Kind: ir.SchemaKind("Clash")}

	outputRoot := t.TempDir()
	_, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Registry: reg})
	want := "generator: left and right both write " + filepath.Join(outputRoot, "shared")
	if err == nil || err.Error() != want {
		t.Fatalf("overlap error = %v, want %q", err, want)
	}
}

func TestCoreRegistryPipelinesMatchTheFormerKindSwitch(t *testing.T) {
	reg := CoreRegistry(coreNaming())
	names := func(specs []registry.GeneratorSpec) []string {
		var out []string
		for _, spec := range specs {
			out = append(out, spec.Name)
		}
		return out
	}
	want := map[string][]string{
		"DB":      {"sql", "orm", "types"},
		"API":     {"types", "api", "sdks"},
		"General": {"types", "envConfig"},
	}
	for kind, pipeline := range want {
		if got := names(reg.Pipeline(kind)); !reflect.DeepEqual(got, pipeline) {
			t.Errorf("Pipeline(%s) = %v, want %v", kind, got, pipeline)
		}
	}
	if got := reg.OutputKeys(); !reflect.DeepEqual(got, []string{"types", "api", "sdk"}) {
		t.Errorf("OutputKeys() = %v", got)
	}
}

// TestCoreRegistryCarriesNoDocumentsOrHooks pins the W5 split: the core
// registers no sidecar documents and no build-all hooks (the deploy family
// registers both from the Parable extension), and a core-only run leaves a
// document it has no spec for untouched instead of failing on it.
func TestCoreRegistryCarriesNoDocumentsOrHooks(t *testing.T) {
	reg := CoreRegistry(coreNaming())
	if docs := reg.Documents(); len(docs) != 0 {
		t.Errorf("Documents() = %+v, want none", docs)
	}
	if hooks := reg.BuildAllHooks(); len(hooks) != 0 {
		t.Errorf("BuildAllHooks() = %+v, want none", hooks)
	}

	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-general"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	if err := ir.SetDocument(schema, "deployValues", map[string]any{"services": []string{"web-api"}}); err != nil {
		t.Fatal(err)
	}
	outputRoot := t.TempDir()
	result, err := Run(schema, cfg, Options{OutputRoot: outputRoot, Registry: reg})
	if err != nil {
		t.Fatalf("Run with an unclaimed document: %v", err)
	}
	for key := range result.Outputs {
		if strings.HasPrefix(key, "helm") || strings.HasPrefix(key, "argo") || strings.HasPrefix(key, "resources") {
			t.Errorf("Outputs[%s] recorded by a core-only run", key)
		}
	}
	if _, err := os.Stat(filepath.Join(outputRoot, "deploy")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("deploy/ written by a core-only run: stat err %v", err)
	}
}

// TestRunKeepsProfilePhaseNames pins the phase names the kind switch emitted:
// the kind wrapper, the per-output phases the generators own, and no phase
// for the pipeline names that never had one (types, sdks).
func TestRunKeepsProfilePhaseNames(t *testing.T) {
	schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, "fixture-db"))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig: %v", err)
	}
	var profiles bytes.Buffer
	if _, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Naming: coreNaming(), Profile: profile.New("fixture-db", &profiles)}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	out := profiles.String()
	for _, phase := range []string{"generator.kind.DB", "generator.output.sql", "generator.output.orm", "generator.output.types-go"} {
		if !strings.Contains(out, "phase="+phase+" ") {
			t.Errorf("missing profile phase %s:\n%s", phase, out)
		}
	}
	for _, phase := range []string{"generator.output.types ", "generator.output.sdks", "generator.output.envConfig"} {
		if strings.Contains(out, "phase="+phase) {
			t.Errorf("unexpected profile phase %s:\n%s", phase, out)
		}
	}
}

// TestRunKindWithoutPipelineLogsNoOutputsWithoutAKindPhase: a registered
// kind with no Pipeline (a grouping kind an extension registers without
// generators) produces nothing, logs why, and opens no kind phase.
func TestRunKindWithoutPipelineLogsNoOutputsWithoutAKindPhase(t *testing.T) {
	reg := registry.New(coreNaming())
	if err := RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterKind(registry.KindSpec{Name: "Grouping", Extension: "test"}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	schema := ir.NewSchema("grouping", ir.SchemaKind("Grouping"))
	cfg := &schemaconfig.SchemaConfig{Name: "grouping", Kind: ir.SchemaKind("Grouping")}
	var log, profiles bytes.Buffer
	result, err := Run(schema, cfg, Options{OutputRoot: t.TempDir(), Log: &log, Profile: profile.New("grouping", &profiles), Registry: reg})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if len(result.Outputs) != 0 || len(result.Skipped) != 0 {
		t.Errorf("Grouping produced %v / skipped %v", result.Outputs, result.Skipped)
	}
	if !strings.Contains(log.String(), "No generator outputs for schema kind Grouping") {
		t.Errorf("log = %q", log.String())
	}
	if strings.Contains(profiles.String(), "generator.kind.Grouping") {
		t.Errorf("Grouping must not open a kind phase:\n%s", profiles.String())
	}
}
