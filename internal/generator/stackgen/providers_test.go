package stackgen

import (
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRuntimeFeaturesOfReadsTheAPIsDatabases (D159): a registered feature
// decides from the API and the databases it connects to, which are loaded
// only when some feature is registered; Service reads the API's slots and
// providers.
func TestRuntimeFeaturesOfReadsTheAPIsDatabases(t *testing.T) {
	api := &ir.Schema{Name: "notes-api", Kind: ir.SchemaKindAPI, AuthDB: "users-db", Dependencies: []ir.ServiceRef{{Name: "notes-db", Kind: ir.SchemaKindDB}, {Name: "shared", Kind: ir.SchemaKindGeneral}}}
	var loaded []string
	load := func(name string) (*ir.Schema, error) {
		loaded = append(loaded, name)
		s := &ir.Schema{Name: name, Kind: ir.SchemaKindDB, Types: map[string]*ir.TypeDef{}}
		if name == "notes-db" {
			s.Types["Notice"] = &ir.TypeDef{Name: "Notice"}
		}
		return s, nil
	}
	reg := registry.New(naming.Naming{})
	if got, err := RuntimeFeaturesOf(reg, api, load); err != nil || got != nil || loaded != nil {
		t.Fatalf("with no feature: %v, %v, loaded %v", got, err, loaded)
	}
	if err := reg.RegisterRuntimeFeature(registry.RuntimeFeature{Name: "notices", Interface: ir.RuntimeChannel, Uses: func(in registry.RuntimeFeatureInput) bool {
		return in.Databases["notes-db"] != nil && in.Databases["notes-db"].Types["Notice"] != nil
	}}); err != nil {
		t.Fatal(err)
	}
	got, err := RuntimeFeaturesOf(reg, api, load)
	if err != nil || len(got) != 1 || got[0] != (ir.RuntimeFeatureUse{Feature: "notices", Interface: ir.RuntimeChannel}) {
		t.Fatalf("RuntimeFeaturesOf = %v, %v", got, err)
	}
	if len(loaded) != 2 || loaded[0] != "users-db" || loaded[1] != "notes-db" {
		t.Fatalf("loaded %v, want the authDb and the DB dependency", loaded)
	}
}
