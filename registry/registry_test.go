package registry_test

import (
	"testing"

	"github.com/parable-work/superschematic/registry"
)

type noop struct{}

func (noop) Name() string                        { return "noop" }
func (noop) Register(r *registry.Registry) error { return nil }

func TestPublicAliasesAcceptAnExtensionAndReachTheCoreKinds(t *testing.T) {
	reg := registry.New(registry.DefaultNaming())
	if err := reg.Use(noop{}); err != nil {
		t.Fatalf("Use: %v", err)
	}
	kinds := reg.Kinds()
	if len(kinds) == 0 {
		t.Fatal("core kinds missing through the public alias")
	}
	if _, ok := reg.Kind("DB"); !ok {
		t.Fatalf("kind DB not registered; kinds = %v", kinds)
	}
	if registry.TargetField.String() != "a field" {
		t.Fatalf("TargetField.String() = %q", registry.TargetField.String())
	}
}
