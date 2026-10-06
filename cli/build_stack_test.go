package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// stackFixtures holds acme-shop-shaped services and shop-stack, a stack
// over them authored in TypeScript, with the environment.json goldens a
// build writes for it.
const stackFixtures = "../internal/generator/stackgen/testdata"

// prepareStackServicesRoot copies the stack fixture's services into a
// schemas root of their own, with the base tsconfig's paths made absolute,
// and returns the services root.
func prepareStackServicesRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "schemas")
	servicesRoot := filepath.Join(root, "services")
	copyDir(t, filepath.Join(stackFixtures, "services"), servicesRoot)
	baseConfig, err := os.ReadFile(filepath.Join(stackFixtures, "tsconfig.base.json"))
	require.NoError(t, err)
	repoRoot, err := filepath.Abs("..")
	require.NoError(t, err)
	text := strings.ReplaceAll(string(baseConfig), "../../../../", filepath.ToSlash(repoRoot)+"/")
	require.NoError(t, os.WriteFile(filepath.Join(root, "tsconfig.base.json"), []byte(text), 0o644))
	return servicesRoot
}

// TestEveryBuildWritesAStacksEnvironments: each build command resolves the
// stack over the services it reaches, which a single build loads from the
// stack's siblings and the others from discovery, and writes each
// environment as stacktest's hand-built stack resolves it.
func TestEveryBuildWritesAStacksEnvironments(t *testing.T) {
	for _, args := range [][]string{
		{"build", "SERVICES/shop-stack"},
		{"build", "--with-deps", "SERVICES/shop-stack"},
		{"build-all", "SERVICES"},
	} {
		t.Run(strings.Join(args[:len(args)-1], " "), func(t *testing.T) {
			servicesRoot := prepareStackServicesRoot(t)
			out := t.TempDir()
			argv := append([]string(nil), args...)
			argv[len(argv)-1] = strings.Replace(argv[len(argv)-1], "SERVICES", servicesRoot, 1)
			buf := new(bytes.Buffer)
			root := New(Config{}, &stacktest.Extension{})
			root.SetOut(buf)
			root.SetErr(buf)
			root.SetArgs(append(argv, "--out", out))
			require.NoError(t, root.Execute(), buf.String())

			for _, env := range []string{"Preview", "Production", "Staging"} {
				got, err := os.ReadFile(stack.EnvironmentPath(out, "shop-stack", env))
				require.NoError(t, err, buf.String())
				want, err := os.ReadFile(stack.EnvironmentPath(filepath.Join(stackFixtures, "golden"), "shop-stack", env))
				require.NoError(t, err)
				require.Equal(t, string(want), string(got), env)
			}
		})
	}
}
