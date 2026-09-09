package ext

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"

	"github.com/parable-work/superschematic/registry"
)

// ManifestDir is where the manifest generator writes for a service.
func ManifestDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "acme", "manifest", service)
}

// Manifest is the generator's output: what the service declares, for an
// inventory tool that does not read the IR.
type Manifest struct {
	Service string   `json:"service"`
	Kind    string   `json:"kind"`
	Region  string   `json:"region,omitempty"`
	Types   []string `json:"types"`
	Enums   []string `json:"enums,omitempty"`
}

// registerManifest adds a generator to kinds the extension did not define.
// A generator that lists kinds is appended to those kinds' pipelines after
// the generators they name, so every DB, API, General and Catalog build
// writes a manifest with no change to the core pipelines. It has no
// OutputKey: schema.config cannot switch it off.
func registerManifest(r *registry.Registry, cfg Config) error {
	return r.RegisterGenerator(registry.GeneratorSpec{
		Name:      "acmeManifest",
		Extension: Name,
		Kinds:     []string{"DB", "API", "General", Kind},
		Dirs: func(c registry.GenerateContext) []string {
			return []string{ManifestDir(c.Options.OutputRoot, c.Config.Name)}
		},
		Generate: func(c registry.GenerateContext) error {
			out := Manifest{
				Service: c.Config.Name,
				Kind:    string(c.Schema.Kind),
				Region:  cfg.Region,
				Types:   sortedTypeNames(c.Schema),
			}
			for name := range c.Schema.Enums {
				out.Enums = append(out.Enums, name)
			}
			sort.Strings(out.Enums)
			dir := ManifestDir(c.Options.OutputRoot, c.Config.Name)
			if err := os.MkdirAll(dir, 0o755); err != nil {
				return err
			}
			b, err := json.MarshalIndent(out, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dir, "manifest.json"), append(b, '\n'), 0o644); err != nil {
				return err
			}
			c.Done("acmeManifest", dir)
			return nil
		},
	})
}
