package stubmodel

import (
	"context"
	"testing"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
)

// TestTheManifestIsTheDefinition holds the package.json this directory
// declares the provider in, which the build reads, to the Go definition
// and to a provider it makes (D159, D184).
func TestTheManifestIsTheDefinition(t *testing.T) {
	manifest, err := providers.ReadManifest(".")
	if err != nil {
		t.Fatal(err)
	}
	definition := Definition()
	instance, err := definition.Create(context.Background(), providers.NewSettings("SLOT", map[string]providers.ConfigValue{}), providers.Context{Slot: "SLOT", Provider: definition.Name, Logger: nopLogger()})
	if err != nil {
		t.Fatal(err)
	}
	if err := providers.CheckManifest(manifest, definition, instance); err != nil {
		t.Fatal(err)
	}
}

func nopLogger() *zap.Logger { return zap.NewNop() }
