package platform_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/platform"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/loader"
	"github.com/parable-work/superschematic/registry"
)

func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), platform.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	return reg
}

func persisted(t *testing.T, schema *ir.Schema) string {
	t.Helper()
	b, err := json.MarshalIndent(schema, "", "  ")
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The whole extension in one pass: the TypeScript fixture loads through the
// real frontend, @platform's Apply lands each platform in the type's
// extension slot, the data form produces the same IR, and the kind's
// pipeline writes the catalog.
func TestPlatformExtensionEndToEnd(t *testing.T) {
	reg := assemble(t)

	schema, cfg, err := loader.LoadServiceWithConfig("testdata/services/fleet", loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig(fleet): %v", err)
	}
	if schema.Kind != platform.Kind || cfg.Kind != platform.Kind {
		t.Fatalf("kind = %q / %q, want %s", schema.Kind, cfg.Kind, platform.Kind)
	}

	core, ok, err := platform.Of(schema.Types["Core"])
	if err != nil || !ok {
		t.Fatalf("Of(Core) = %v, %v", ok, err)
	}
	if core.Visibility != "public" || strings.Join(core.Services, ",") != "api,db" {
		t.Errorf("Core = %+v", core)
	}
	if got := core.Shared["database"]; strings.Join(got, ",") != "api,db" {
		t.Errorf("Core.Shared[database] = %v", got)
	}
	if _, ok, _ := platform.Of(schema.Types["Observability"]); !ok {
		t.Error("Observability has no platform")
	}

	// The data form: same platforms under the extension slot, same IR once
	// the source file names are normalized (owners name the file).
	yamlSchema, _, err := loader.LoadServiceWithConfig("testdata/services/fleet-yaml", loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadServiceWithConfig(fleet-yaml): %v", err)
	}
	tsOut := persisted(t, schema)
	yamlOut := strings.ReplaceAll(persisted(t, yamlSchema), ".schema.yaml", ".schema.ts")
	if yamlOut != tsOut {
		t.Errorf("YAML and TS forms produced different IR\nts:\n%s\nyaml:\n%s", tsOut, yamlOut)
	}

	outputRoot := t.TempDir()
	result, err := registry.Generate(schema, cfg, registry.Options{
		OutputRoot:  outputRoot,
		ServicePath: "testdata/services/fleet",
		Registry:    reg,
	})
	if err != nil {
		t.Fatalf("Generate: %v", err)
	}
	dir := platform.OutDir(outputRoot, "fleet")
	if got := result.Outputs["platformCatalog"]; got != dir {
		t.Errorf("Result.Outputs[platformCatalog] = %q, want %q", got, dir)
	}
	catalog, err := os.ReadFile(filepath.Join(dir, "catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `{
  "service": "fleet",
  "platforms": {
    "Core": {
      "description": "Customer-facing API and its database",
      "visibility": "public",
      "services": [
        "api",
        "db"
      ],
      "shared": {
        "database": [
          "api",
          "db"
        ]
      }
    },
    "Observability": {
      "visibility": "internal",
      "services": [
        "metrics"
      ]
    }
  }
}
`
	if string(catalog) != want {
		t.Errorf("catalog.json:\n%s\nwant:\n%s", catalog, want)
	}
}

// KindSpec.Verify runs on every load of a Platform schema: sharing a
// resource with a service that is not a member fails the load, naming the
// platform, the resource and the stranger.
func TestVerifyRejectsSharingWithANonMember(t *testing.T) {
	reg := assemble(t)
	_, err := loader.LoadService("testdata/services/broken-fleet", loader.WithRegistry(reg))
	if err == nil {
		t.Fatal("broken-fleet loaded")
	}
	for _, want := range []string{"platform Core", "shared cache", `"worker"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err, want)
		}
	}
}
