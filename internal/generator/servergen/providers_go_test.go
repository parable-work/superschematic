package servergen_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestAGoServerReadsItsAPIsProviders: the entrypoint of a Go server whose
// API's Deps hold runtime providers (D159, D184) reads them from the API's
// config with the API package's ProvidersOf, stops when they do not read,
// and passes them in Deps; its go.mod requires each provider's module and
// replaces it with the provider's directory.
func TestAGoServerReadsItsAPIsProviders(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	paths := naming.LocalPaths{
		ScalarGo:      filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		HTTPRuntimeGo: filepath.Join(repoRoot, "runtime", "http", "go"),
		RulesGo:       filepath.Join(repoRoot, "runtime", "rules", "go"),
	}
	in := goBehaviorInput(repoRoot, paths)
	providerDir := filepath.Join(repoRoot, "providers", "sms-relay-a")
	in.APIs[0].Output.Deps.Providers = &apigen.DepsProviders{
		Slots:   []apigen.DepsProviderSlot{{Slot: "SMS", Interface: "channel"}},
		Modules: []apigen.DepsProviderModule{{Name: "sms-relay-a", Module: "example.com/providers/sms-relay-a", Alias: "smsrelayaprovider", Dir: providerDir}},
	}
	in.Modules = append(in.Modules, servergen.Module{Path: "example.com/providers/sms-relay-a", Dir: providerDir})
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	main, err := os.ReadFile(filepath.Join(in.Dir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []*regexp.Regexp{
		regexp.MustCompile(`fixtureWorkflowApiProviders, err := fixtureworkflowapi\.ProvidersOf\(\*fixtureWorkflowApiConfig, fixtureWorkflowApiLogger\)\n\s+if err != nil \{\n\s+return fmt\.Errorf\("providers of fixture-workflow-api: %w", err\)`),
		regexp.MustCompile(`Providers: +fixtureWorkflowApiProviders,`),
	} {
		if !want.Match(main) {
			t.Errorf("main.go has no %s:\n%s", want, main)
		}
	}
	mod, err := os.ReadFile(filepath.Join(in.Dir, servergen.ModFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(mod), "example.com/providers/sms-relay-a v0.0.0-00010101000000-000000000000 // indirect") || !regexp.MustCompile(`example\.com/providers/sms-relay-a => \S*providers/sms-relay-a`).Match(mod) {
		t.Errorf("go.mod neither requires nor replaces the provider's module:\n%s", mod)
	}
}
