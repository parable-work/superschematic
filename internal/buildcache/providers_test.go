package buildcache

import (
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	ir "github.com/parable-work/superschematic/ir"
)

// TestAProvidersTreeJoinsTheKey (D159, #608): an API's key covers the
// trees of the runtime provider packages its config lists, their manifests
// and their sources, by a directory outside its tree or by an npm name in
// node_modules, and so does the key of a stack that deploys it. What a
// build writes in a provider's directory, an installed package's own
// node_modules, and a package no API lists leave every key as it was.
func TestAProvidersTreeJoinsTheKey(t *testing.T) {
	tree := newReferenceTree(t)
	relay := filepath.Join(tree.repo, "providers", "sms-relay")
	vault := filepath.Join(tree.schemasRoot, "node_modules", "@example", "vault")
	unlisted := filepath.Join(tree.schemasRoot, "node_modules", "@example", "unlisted")
	writeFile(t, filepath.Join(relay, "package.json"), `{"name": "@example/sms-relay"}`)
	writeFile(t, filepath.Join(relay, "src", "index.ts"), "export default {};")
	writeFile(t, filepath.Join(vault, "package.json"), `{"name": "@example/vault"}`)
	writeFile(t, filepath.Join(vault, "dist", "index.js"), "export default {};")
	writeFile(t, filepath.Join(unlisted, "package.json"), `{"name": "@example/unlisted"}`)
	for _, s := range tree.services {
		if s.Name == "orders" {
			s.Config.Providers = []string{"../../../providers/sms-relay", "@example/vault"}
		}
	}
	require.NoError(t, WriteSchemaReferences(tree.schemasRoot, "stack", []ir.ServiceRef{{Name: "orders", Kind: ir.SchemaKindAPI}}, nil))
	before := tree.hashes(t)

	writeFile(t, filepath.Join(relay, "dist", "index.js"), "export default {};")
	writeFile(t, filepath.Join(relay, "node_modules", "dep", "index.js"), "export {};")
	writeFile(t, filepath.Join(vault, "node_modules", "dep", "index.js"), "export {};")
	writeFile(t, filepath.Join(unlisted, "package.json"), `{"name": "@example/unlisted", "version": "2.0.0"}`)
	assert.Equal(t, before, tree.hashes(t), "a directory's build output, an installed package's own dependencies and an unlisted package are no input")

	for name, edit := range map[string]func(){
		"a directory's source file": func() {
			writeFile(t, filepath.Join(relay, "src", "index.ts"), "export default { changed: true };")
		},
		"a directory's new source file": func() {
			writeFile(t, filepath.Join(relay, "src", "send.ts"), "export const send = 1;")
		},
		"an npm package's code": func() {
			writeFile(t, filepath.Join(vault, "dist", "index.js"), "export default { changed: true };")
		},
		"a directory's package.json": func() {
			writeFile(t, filepath.Join(relay, "package.json"), `{"name": "@example/sms-relay", "version": "2.0.0"}`)
		},
		"a directory's new Cargo.toml": func() {
			writeFile(t, filepath.Join(relay, "Cargo.toml"), "[package]\nname = \"sms-relay\"\n")
		},
		"an npm package's package.json": func() {
			writeFile(t, filepath.Join(vault, "package.json"), `{"name": "@example/vault", "version": "2.0.0"}`)
		},
	} {
		previous := tree.hashes(t)
		edit()
		after := tree.hashes(t)
		assert.NotEqual(t, previous["orders"], after["orders"], "%s: the API that lists it", name)
		assert.NotEqual(t, previous["stack"], after["stack"], "%s: the stack that deploys the API", name)
		assert.Equal(t, previous["shop-api"], after["shop-api"], "%s: an API that lists none", name)
		assert.Equal(t, previous["other"], after["other"], "%s: a service that references none", name)
	}
}
