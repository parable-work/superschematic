package loader

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestAProviderDirectoryHoldsAGoModule: a provider's directory holds a Go
// module when it has a go.mod, whose module path the loader reads (D184);
// a provider of Go alone declares itself in superschematic.json, and one
// with a package.json too declares one provider in both.
func TestAProviderDirectoryHoldsAGoModule(t *testing.T) {
	write := func(dir, name, text string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(dir, name), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	const declaration = `{"name": "relay-c", "implements": "channel", "config": {"URL": {}, "TOKEN": {"secret": true}}, "role": ["messages.relay"]}`

	goAlone := t.TempDir()
	write(goAlone, "superschematic.json", `{"provider": `+declaration+`}`)
	write(goAlone, "go.mod", "module example.com/providers/relay-c // the relay\n\ngo 1.27.3\n")
	p, err := readProvider(goAlone)
	if err != nil {
		t.Fatal(err)
	}
	if p.Name != "relay-c" || p.Module != "example.com/providers/relay-c" || p.Package != "" || len(p.Operations) != 2 || p.Path != "" {
		t.Fatalf("a Go provider: %+v", p)
	}

	both := t.TempDir()
	write(both, "superschematic.json", `{"provider": `+declaration+`}`)
	write(both, "package.json", `{"name": "@example/relay-c", "superschematic": {"provider": `+declaration+`}}`)
	write(both, "go.mod", "module example.com/providers/relay-c\n")
	if p, err = readProvider(both); err != nil || p.Package != "@example/relay-c" || p.Module != "example.com/providers/relay-c" {
		t.Fatalf("a package and a module: %+v, %v", p, err)
	}
	write(both, "package.json", `{"name": "@example/relay-c", "superschematic": {"provider": {"name": "relay-c", "implements": "channel"}}}`)
	if _, err := readProvider(both); err == nil || !strings.Contains(err.Error(), "declare different providers") {
		t.Fatalf("two declarations that differ: %v", err)
	}

	typescript := t.TempDir()
	write(typescript, "package.json", `{"name": "@example/relay-c", "superschematic": {"provider": `+declaration+`}}`)
	if p, err = readProvider(typescript); err != nil || p.Module != "" {
		t.Fatalf("a provider of TypeScript alone: %+v, %v", p, err)
	}

	bad := t.TempDir()
	write(bad, "superschematic.json", `{"provider": {"name": "relay-c", "implements": "payments"}}`)
	if _, err := readProvider(bad); err == nil || !strings.Contains(err.Error(), "no runtime interface") {
		t.Fatalf("a declaration of no interface: %v", err)
	}
	write(bad, "superschematic.json", `{"provider": `+declaration+`}`)
	write(bad, "go.mod", "go 1.27.3\n")
	if _, err := readProvider(bad); err == nil || !strings.Contains(err.Error(), "go.mod names no module") {
		t.Fatalf("a go.mod with no module: %v", err)
	}
}
