package providerhooks_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestRoutesConfigPrepareRunsBeforeValidate: the provider's statements on
// cfg open RegisterRoutes, before it validates cfg, in a public API and in
// one that is not.
func TestRoutesConfigPrepareRunsBeforeValidate(t *testing.T) {
	for _, public := range []bool{true, false} {
		_, dir := writeFixtureAPIWith(t, public)
		data, err := os.ReadFile(filepath.Join(dir, "routes.go"))
		if err != nil {
			t.Fatal(err)
		}
		routes := string(data)
		start := strings.Index(routes, "func RegisterRoutes(r chi.Router, cfg Config) error {")
		prepare := strings.Index(routes, "cfg.StepGuard = defaultStepGuard")
		validate := strings.Index(routes, "if err := cfg.Validate(); err != nil {")
		if start < 0 || prepare < start || validate < prepare {
			t.Errorf("public %v: RegisterRoutes at %d, the prepare step at %d, Validate at %d", public, start, prepare, validate)
		}
	}
}
