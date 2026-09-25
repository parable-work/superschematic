package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
)

// sharedImportDocument is an extension with one sidecar document,
// manifest.yaml, whose loader reports <schemas root>/shared/values.yaml as
// an authoring import, the way a document module that imports a sibling
// file of the schemas tree does.
type sharedImportDocument struct{}

func (sharedImportDocument) Name() string { return "shared-import" }

func (sharedImportDocument) Register(r *registry.Registry) error {
	return r.RegisterDocument(registry.DocumentSpec{
		Name:      "manifest",
		Extension: "shared-import",
		File:      "manifest.yaml",
		Loader: func(_ context.Context, lc registry.LoadContext) (json.RawMessage, []string, error) {
			doc, err := lc.DecodeData("manifest.yaml", nil)
			if err != nil {
				return nil, nil, err
			}
			servicePath, err := filepath.Abs(lc.ServicePath)
			if err != nil {
				return nil, nil, err
			}
			shared := filepath.Join(filepath.Dir(filepath.Dir(servicePath)), "shared", "values.yaml")
			return doc, []string{shared}, nil
		},
	})
}

// writeDocumentRepo lays out <repo>/defs/services/svc, a General service
// with a manifest.yaml sidecar, next to <repo>/defs/shared/values.yaml. The
// schemas root is named defs, not schemas.
func writeDocumentRepo(t *testing.T) (repo, servicesRoot string) {
	t.Helper()
	repo, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	servicesRoot = filepath.Join(repo, "defs", "services")
	for path, content := range map[string]string{
		filepath.Join(servicesRoot, "svc", "schema.config.json"):   `{"name": "svc", "kind": "General", "outputs": {}}`,
		filepath.Join(servicesRoot, "svc", "src", "a.schema.json"): `{"name": "Widget", "role": "EmbeddedStruct", "fields": [{"name": "id", "typeRef": {"name": "string"}}]}`,
		filepath.Join(servicesRoot, "svc", "manifest.yaml"):        "replicas: 2\n",
		filepath.Join(repo, "defs", "shared", "values.yaml"):       "size: 1\n",
	} {
		require.NoError(t, os.MkdirAll(filepath.Dir(path), 0o755))
		require.NoError(t, os.WriteFile(path, []byte(content), 0o644))
	}
	return repo, servicesRoot
}

// TestEveryBuildCommandWritesTheAuthoringImportsUnderItsSchemasRoot: the
// build cache reads a service's authoring-import depfile from
// <schemas root>/dist/.authoring-imports, so every command that builds a
// service writes it there: build, build --with-deps and build-all, whatever
// the schemas root is called and wherever --out puts the generated output.
func TestEveryBuildCommandWritesTheAuthoringImportsUnderItsSchemasRoot(t *testing.T) {
	for _, tc := range []struct {
		name string
		args func(servicesRoot, out string) []string
	}{
		{"build", func(servicesRoot, _ string) []string {
			return []string{"build", filepath.Join(servicesRoot, "svc")}
		}},
		{"build --out", func(servicesRoot, out string) []string {
			return []string{"build", filepath.Join(servicesRoot, "svc"), "--out", out}
		}},
		{"build --with-deps", func(servicesRoot, out string) []string {
			return []string{"build", "--with-deps", filepath.Join(servicesRoot, "svc"), "--out", out}
		}},
		{"build-all", func(servicesRoot, out string) []string {
			return []string{"build-all", servicesRoot, "--out", out}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, servicesRoot := writeDocumentRepo(t)
			// Two levels below the repository root, so an output root is
			// not mistaken for <repo>/<schemas root>/dist.
			out := filepath.Join(repo, "build", "generated", "dist")

			root := New(Config{}, sharedImportDocument{})
			root.SetOut(new(bytes.Buffer))
			root.SetErr(new(bytes.Buffer))
			root.SetArgs(tc.args(servicesRoot, out))
			require.NoError(t, root.Execute())

			data, err := os.ReadFile(filepath.Join(repo, "defs", "dist", ".authoring-imports", "svc.json"))
			require.NoError(t, err, "the depfile belongs under the schemas root the command was given")
			var imports []string
			require.NoError(t, json.Unmarshal(data, &imports))
			assert.Equal(t, []string{"defs/shared/values.yaml"}, imports)

			assert.NoDirExists(t, filepath.Join(repo, "schemas"), "nothing is written under a schemas root the repository does not have")
			for _, dir := range []string{"schemas", "defs"} {
				assert.NoDirExists(t, filepath.Join(repo, "build", dir), "the depfile location does not follow --out")
			}
		})
	}
}
