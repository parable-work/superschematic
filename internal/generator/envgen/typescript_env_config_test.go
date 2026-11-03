package envgen_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestTypeScriptEnvConfigGolden pins the TypeScript EnvConfig (D51): the
// settings of the @envVars type, a field per edge and the callers field,
// each read through the HTTP runtime's readers with every refusal reported
// together; and that of an API with a job, whose loadBackgroundEnvConfig,
// which the job's entrypoint calls, leaves the callers field without
// issuers (D65). The schema is the Rust EnvConfig's (rustEnvSchema), with
// the options the TypeScript API's build passes. Regenerate with:
//
//	go test ./internal/generator/envgen -run TestTypeScriptEnvConfigGolden -update
func TestTypeScriptEnvConfigGolden(t *testing.T) {
	withJobs := rustEnvSchema()
	withJobs.Jobs = []*ir.Job{{Name: "Reindex"}}
	for name, schema := range map[string]*ir.Schema{"typescript-env-config": rustEnvSchema(), "typescript-env-config-background": withJobs} {
		output, err := envgen.GenerateWithOptions(schema, envgen.Options{SchemaName: "shop-api", Naming: naming.Default(), Derived: true, Callers: true})
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := envgen.WriteTypeScriptEnvConfig(output, dir); err != nil {
			t.Fatal(err)
		}
		got, err := os.ReadFile(filepath.Join(dir, envgen.TypeScriptEnvConfigFile))
		if err != nil {
			t.Fatal(err)
		}
		golden := filepath.Join("testdata", "golden", name, envgen.TypeScriptEnvConfigFile)
		if *update {
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		if err != nil {
			t.Fatalf("%v (run with -update to write it)", err)
		}
		if string(got) != string(want) {
			t.Errorf("%s differs from golden (run with -update to accept):\n%s", name, got)
		}
	}
}

// TestDeriverMakesTheConfigBackground: an API whose only background
// deployable is a deriver gets the background loaders too (D74), as one
// with a job does, which the deriver's entrypoint calls: the same config.ts
// as the API with a job.
func TestDeriverMakesTheConfigBackground(t *testing.T) {
	withDeriver := rustEnvSchema()
	withDeriver.Derivers = []*ir.Deriver{{Name: "TriageTickets"}}
	output, err := envgen.GenerateWithOptions(withDeriver, envgen.Options{SchemaName: "shop-api", Naming: naming.Default(), Derived: true, Callers: true})
	if err != nil {
		t.Fatal(err)
	}
	if !output.Background {
		t.Fatal("an API with a deriver is not background")
	}
	dir := t.TempDir()
	if err := envgen.WriteTypeScriptEnvConfig(output, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, envgen.TypeScriptEnvConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	want, err := os.ReadFile(filepath.Join("testdata", "golden", "typescript-env-config-background", envgen.TypeScriptEnvConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string(want) {
		t.Errorf("the deriver's API's config.ts differs from the background golden:\n%s", got)
	}
}
