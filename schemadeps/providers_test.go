package schemadeps

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAddProvidersLinksTheAPIPackages (D159, #608): a runtime provider
// package an API's config lists enters the graph in each language whose
// API package of that service links it, with the ID <name>-provider, kind
// provider, its manifest's name, its directory outside the output root as
// its path and no service, and the API package gains an edge to it. Two
// directories of one ID are refused.
func TestAddProvidersLinksTheAPIPackages(t *testing.T) {
	repo := t.TempDir()
	dist := filepath.Join(repo, "schemas", "dist")
	write := func(rel, file, content string) {
		t.Helper()
		dir := filepath.Join(dist, filepath.FromSlash(rel))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("api/notes-api", "package.json", `{"name": "@schemas/notes-api-api"}`)
	write("api/jobs-api", "go.mod", "module "+goModulePrefix()+"api/jobs-api\n\ngo 1.27.3\n")
	relay := filepath.Join(repo, "providers", "sms-relay")
	if err := os.MkdirAll(relay, 0o755); err != nil {
		t.Fatal(err)
	}
	producers := map[string]string{"api/notes-api": "notes-api", "api/jobs-api": "jobs-api"}
	providers := []Provider{
		{Service: "notes-api", Name: "sms-relay", Dir: relay, Package: "@example/sms-relay", Module: "example.com/sms-relay", Crate: "sms-relay"},
		{Service: "jobs-api", Name: "sms-relay", Dir: relay, Package: "@example/sms-relay", Module: "example.com/sms-relay"},
	}

	g, err := CollectFromDist(dist, producers)
	if err != nil {
		t.Fatal(err)
	}
	if err := g.AddProviders(dist, providers); err != nil {
		t.Fatal(err)
	}
	for lang, name := range map[string]string{"typescript": "@example/sms-relay", "go": "example.com/sms-relay"} {
		p := mustPkg(t, g, lang, "sms-relay-provider")
		if p.Kind != ProviderKind || p.Name != name || p.Path != "../../providers/sms-relay" || p.Service != "" {
			t.Errorf("%s provider package = %+v, want kind provider, name %s, path ../../providers/sms-relay, no service", lang, p, name)
		}
	}
	if got := strings.Join(mustPkg(t, g, "typescript", "notes-api-api").Deps, ","); got != "sms-relay-provider" {
		t.Errorf("notes-api's TypeScript API package deps = [%s], want [sms-relay-provider]", got)
	}
	if got := strings.Join(mustPkg(t, g, "go", "jobs-api-api").Deps, ","); got != "sms-relay-provider" {
		t.Errorf("jobs-api's Go API package deps = [%s], want [sms-relay-provider]", got)
	}
	if _, ok := g.ByLanguage("rust")["sms-relay-provider"]; ok {
		t.Error("the graph holds the provider's crate, which no Rust API package links")
	}
	if closure, err := g.Closure("typescript", []string{"notes-api-api"}); err != nil || strings.Join(closure, ",") != "sms-relay-provider,notes-api-api" {
		t.Errorf("Closure = %v, %v", closure, err)
	}

	other := filepath.Join(repo, "vendor", "sms-relay")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	g, err = CollectFromDist(dist, producers)
	if err != nil {
		t.Fatal(err)
	}
	err = g.AddProviders(dist, []Provider{providers[0], {Service: "jobs-api", Name: "sms-relay", Dir: other, Module: "example.com/other"}, {Service: "notes-api", Name: "sms-relay", Dir: other, Package: "@example/other"}})
	if err == nil || !strings.Contains(err.Error(), "runtime provider sms-relay of notes-api at ../../vendor/sms-relay gives the typescript package sms-relay-provider, which ../../providers/sms-relay holds too") {
		t.Errorf("two directories of one provider name: %v", err)
	}
}
