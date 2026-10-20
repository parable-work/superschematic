package servergen_test

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	ir "github.com/parable-work/superschematic/ir"
)

// buildOverTSShop builds ts-stack once, which scaffolds ts-shop's
// implementation, and returns the implementation's directory and a build
// of ts-stack over code in place of the scaffold, which fails the test
// when the build writes into the implementation.
func buildOverTSShop(t *testing.T) (string, func(code string) error) {
	t.Helper()
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), append(slices.Clone(tsAPIs), "ts-stack")...)
	dir := naming.Default().TypeScriptImplementationDir(repoRoot, "ts-shop")
	file := filepath.Join(dir, tsrestgen.ImplementationFile)
	return dir, func(code string) error {
		t.Helper()
		if err := os.WriteFile(file, []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
		_, err := generator.Run(f.schemas["ts-stack"], f.configs["ts-stack"], f.options(repoRoot, tsFakePaths(repoRoot)))
		if got, rerr := os.ReadFile(file); rerr != nil || string(got) != code {
			t.Errorf("a build wrote %s: %q, %v", file, got, rerr)
		}
		return err
	}
}

// TestATypeScriptImplementationThatPredatesItsManualRoutesFails: ts-shop
// declares a @manualRouteRegistration operation, exportCart, and its
// implementation, which exists and so is the engineer's, exports no
// createManualRoutes, whose handlers the server's main.ts hands
// buildRouter (D51). The build writes nothing into it and fails, saying
// what to add; with it exported, it builds.
func TestATypeScriptImplementationThatPredatesItsManualRoutesFails(t *testing.T) {
	dir, build := buildOverTSShop(t)
	before := "// The engineer's code, from before the manual route.\nexport const create = () => ({});\nexport const createJobs = () => ({});\nexport const createWorkers = () => ({});\n"
	err := build(before)
	for _, want := range []string{
		"stack ts-stack: ts-shop declares @manualRouteRegistration operations, and its implementation at " + dir + ", which the build no longer writes into, exports no createManualRoutes",
		"export const createManualRoutes: ManualRoutesConstructor = deps => ({",
		"async exportCart(c, ctx) {\n      // answer GET /api/carts/{id}/export with a Response",
		"importing ManualRoutesConstructor from @schemas/ts-shop-api",
		"(docs/stack-model.md, section 8.6)",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	if err := build(before + "export const createManualRoutes = () => ({});\n"); err != nil {
		t.Fatalf("a build over an implementation that exports createManualRoutes = %v", err)
	}
}

// TestATypeScriptImplementationThatPredatesItsJobsFails: ts-shop declares
// a job and a worker, and its implementation, which exists and so is the
// engineer's, exports neither createJobs nor createWorkers. The build
// writes nothing into it and fails, saying what to add (D52); with
// createJobs added, it fails for createWorkers, the message's queue named
// (D53); with both, it builds.
func TestATypeScriptImplementationThatPredatesItsJobsFails(t *testing.T) {
	dir, build := buildOverTSShop(t)
	before := "// The engineer's code, from before the job.\nexport const create = () => ({});\nexport const createManualRoutes = () => ({});\n"
	err := build(before)
	for _, want := range []string{
		"stack ts-stack: ts-shop declares jobs, and its implementation at " + dir + ", which the build no longer writes into, exports no createJobs",
		"export const createJobs: JobsConstructor = deps => ({",
		"async sweepCarts(signal) {",
		"importing JobsConstructor from @schemas/ts-shop-api",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	err = build(before + "\nconst jobs = () => ({});\nexport { jobs as createJobs };\n")
	for _, want := range []string{
		"stack ts-stack: ts-shop declares workers, and its implementation at " + dir + ", which the build no longer writes into, exports no createWorkers",
		"export const createWorkers: WorkersConstructor = deps => ({",
		"async remindShoppers(message, signal) {\n      // handle a message of CartAbandoned",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	if err := build(before + "export const createJobs = () => ({});\nexport async function createWorkers() {\n  return {};\n}\n"); err != nil {
		t.Fatalf("a build over an implementation that exports both = %v", err)
	}
}

// TestATypeScriptWorkerOfAQueueTheClientCannotHoldFails: a TypeScript
// worker whose queue's message holds a value the TypeScript queue client
// cannot write and read back yet, a Temporal.Duration, which Postgres holds
// as an INTERVAL, is refused, naming the field (D53).
func TestATypeScriptWorkerOfAQueueTheClientCannotHoldFails(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	db := f.schemas["ts-db"]
	db.Scalars["Temporal.Duration"] = &ir.ScalarDef{Name: "Temporal.Duration", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"sql": "INTERVAL"}}
	queue := db.Types["CartAbandoned"]
	queue.Fields = append(queue.Fields, &ir.FieldDef{Name: "idleFor", TypeRef: ir.TypeRef{Name: "Temporal.Duration"}, Required: true})
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsAPIs...)
	_, err := generator.Run(f.schemas["ts-stack"], f.configs["ts-stack"], f.options(repoRoot, tsFakePaths(repoRoot)))
	want := "stack ts-stack: TypeScript worker ts-shop-remind-shoppers claims queue CartAbandoned of ts-db, which the TypeScript queue client cannot hold yet: its field idleFor is a Temporal.Duration, an INTERVAL column"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("build = %v, want it to say %q", err, want)
	}
}
