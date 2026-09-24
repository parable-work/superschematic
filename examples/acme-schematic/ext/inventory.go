package ext

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"

	"github.com/parable-work/superschematic/registry"
)

// InventoryPath is where the inventory hook writes: one file for the whole
// schemas root, outside every service's output directories.
func InventoryPath(outputRoot string) string {
	return filepath.Join(outputRoot, "acme", "inventory.json")
}

// Inventory is every service's manifest, in build order.
type Inventory struct {
	Services []Manifest `json:"services"`
}

// registerInventory adds a build-all hook. It needs every service at once,
// which no per-service generator has, so it runs after build-all has put
// every service's output in place.
//
// It reads each manifest from the service's output directories, not from
// the IR: when a service is restored from the build cache or already up to
// date, build-all does not load it and SchemaFor has nothing, but its output
// directories hold what the manifest generator wrote. A manifest that is
// missing there is an error rather than a gap in the inventory.
func registerInventory(r *registry.Registry) error {
	return r.RegisterBuildAllHook(registry.BuildAllHook{
		Name:      "acmeInventory",
		Extension: Name,
		Run: func(_ context.Context, bc registry.BuildAllContext) error {
			inventory := Inventory{Services: []Manifest{}}
			for _, service := range bc.Services {
				dir := ManifestDir(bc.OutputRoot, service.Name)
				// acmeManifest runs on every kind, so its directory is
				// one of every service's output directories.
				if !slices.Contains(service.OutputDirs, dir) {
					return fmt.Errorf("%s: %s is not one of its output directories", service.Name, dir)
				}
				data, err := os.ReadFile(filepath.Join(dir, "manifest.json"))
				if err != nil {
					return fmt.Errorf("%s: %w", service.Name, err)
				}
				var manifest Manifest
				if err := json.Unmarshal(data, &manifest); err != nil {
					return fmt.Errorf("%s: %w", service.Name, err)
				}
				inventory.Services = append(inventory.Services, manifest)
			}
			path := InventoryPath(bc.OutputRoot)
			if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
				return err
			}
			b, err := json.MarshalIndent(inventory, "", "  ")
			if err != nil {
				return err
			}
			if err := os.WriteFile(path, append(b, '\n'), 0o644); err != nil {
				return err
			}
			_, _ = fmt.Fprintf(bc.Log, "  Wrote %s (%d services)\n", path, len(inventory.Services))
			return nil
		},
	})
}
