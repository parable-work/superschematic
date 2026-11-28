package schemaconfig

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// TestAProviderIsADirectoryOrAnNpmName (D159, #608): an API's config lists
// each provider by its directory, which starts with "./" or "../", or by
// its npm name, and refuses an entry of neither form.
func TestAProviderIsADirectoryOrAnNpmName(t *testing.T) {
	const config = `{"name": "notes-api", "kind": "API", "providers": [%s], "outputs": {}}`
	listed := []string{"../../providers/sms-relay", "./relay", "@example/sms-relay", "model-stub"}
	cfg, err := ReadFile(writeConfig(t, "schema.config.json", strings.Replace(config, "%s", `"`+strings.Join(listed, `", "`)+`"`, 1)), coreKinds)
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	if !slices.Equal(cfg.Providers, listed) {
		t.Errorf("providers = %v, want %v", cfg.Providers, listed)
	}
	for _, entry := range listed {
		if got, want := IsProviderPackageName(entry), !strings.HasPrefix(entry, "."); got != want {
			t.Errorf("IsProviderPackageName(%q) = %v, want %v", entry, got, want)
		}
	}

	for entry, want := range map[string]string{
		"providers/relay":    `"providers/relay" is no npm name; a provider is its package's npm name (@scope/name or name, lower-case) or its directory, which starts with "./" or "../"`,
		"@Example/Relay":     `"@Example/Relay" is no npm name`,
		"/opt/relay":         `"/opt/relay" is an absolute path`,
		"./a/../relay":       `"./a/../relay" is no clean path; write "./relay"`,
		`..\\relay`:          "is no path in slash form",
		"./relay\", \"relay": "lists provider ./relay more than once",
	} {
		_, err := ReadFile(writeConfig(t, "schema.config.json", strings.Replace(config, "%s", `"./relay", "`+entry+`"`, 1)), coreKinds)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("provider %q: %v, want %q", entry, err, want)
		}
	}
}

// TestResolveProviderWalksUpToNodeModules (#608): an npm name resolves
// from the first node_modules in the service's directory or one above it,
// and a directory from the service's directory.
func TestResolveProviderWalksUpToNodeModules(t *testing.T) {
	root := t.TempDir()
	service := filepath.Join(root, "schemas", "services", "notes-api")
	near := filepath.Join(root, "schemas", "services", "node_modules", "@example", "sms-relay")
	far := filepath.Join(root, "node_modules", "@example", "sms-relay")
	for _, dir := range []string{service, near, far} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
	}
	if got, err := ResolveProvider(service, "@example/sms-relay"); err != nil || got != near {
		t.Errorf("ResolveProvider = %s, %v, want the nearest, %s", got, err, near)
	}
	if got, err := ResolveProvider(service, "../../providers/relay"); err != nil || got != filepath.Join(root, "schemas", "providers", "relay") {
		t.Errorf("ResolveProvider of a directory = %s, %v", got, err)
	}
	_, err := ResolveProvider(service, "absent")
	if err == nil || !strings.Contains(err.Error(), "no package absent in the service's node_modules or one above it (looked in "+filepath.Join(service, "node_modules")+", ") {
		t.Errorf("ResolveProvider of a missing package: %v", err)
	}
}
