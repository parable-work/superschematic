package cli

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// TestStackPrepare: stack prepare writes the build context of the demo's
// server for every environment off the local target, and a deploy given
// the directory builds its image from that context, with the output root
// gone; a directory prepared with --no-build serves a plan and a destroy,
// and is refused for a deploy that builds (D47, amended).
func TestStackPrepare(t *testing.T) {
	stackDir := demoTree(t)
	schemasRoot := filepath.Dir(filepath.Dir(stackDir))
	serverDir := filepath.Join(schemasRoot, "dist", "server", "demo-stack", "demo-api")
	require.NoError(t, os.MkdirAll(serverDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(serverDir, "Dockerfile"), []byte("FROM scratch\n"), 0o644))
	require.NoError(t, os.WriteFile(filepath.Join(serverDir, "Dockerfile.dockerignore"), []byte("*\n!schemas/dist/server\n"), 0o644))
	ext := &stacktest.Extension{}
	run := func(args ...string) (string, error) {
		t.Helper()
		var stdout, stderr bytes.Buffer
		root := New(Config{}, ext)
		root.SetOut(&stdout)
		root.SetErr(&stderr)
		root.SetIn(strings.NewReader(""))
		root.SetArgs(append([]string{"stack"}, append(args, "--stack", stackDir)...))
		err := root.Execute()
		return stdout.String(), err
	}
	useTerminal(t, &fakeTerminal{answers: []string{"key-1"}})

	dir := filepath.Join(t.TempDir(), "prepared")
	out, err := run("prepare", "--dir", dir)
	require.NoError(t, err)
	assert.Contains(t, out, "prepared demo-stack in "+dir+": 0 documents, 1 contexts, 0 sites, 0 bundles")
	p, err := stackdeploy.ReadPrepared(dir)
	require.NoError(t, err)
	assert.ElementsMatch(t, []string{"Staging", "Preview"}, p.Environments)
	assert.True(t, p.Built)
	assert.Empty(t, ext.Provisioner.Calls(), "prepare reached the target")

	// The output root is gone: the deploy reads the context it holds.
	require.NoError(t, os.RemoveAll(filepath.Join(schemasRoot, "dist")))
	_, err = run("deploy", "Staging", "--prepared", dir)
	require.NoError(t, err)
	calls := ext.Provisioner.Calls()
	require.NotEmpty(t, calls)
	assert.Equal(t, "build demo-api: schemas/dist/server/demo-stack/demo-api/Dockerfile", calls[0])
	assert.Equal(t, []string{"schemas/dist/server/", "schemas/dist/server/demo-stack/", "schemas/dist/server/demo-stack/demo-api/",
		"schemas/dist/server/demo-stack/demo-api/Dockerfile", "schemas/dist/server/demo-stack/demo-api/Dockerfile.dockerignore"},
		ext.Builder.Context("demo-api"))

	_, err = run("prepare", "Dev", "--dir", dir)
	require.NoError(t, err, "a local environment named is prepared")
	_, err = run("deploy", "Staging", "--prepared", dir)
	require.ErrorContains(t, err, "was prepared for Dev, not Staging")

	_, err = run("prepare", "--no-build", "--dir", dir)
	require.NoError(t, err)
	_, err = run("plan", "Staging", "--prepared", dir)
	require.NoError(t, err)
	_, err = run("deploy", "Staging", "--prepared", dir)
	require.ErrorContains(t, err, "holds the documents alone")
	_, err = run("deploy", "Staging", "--prepared", dir, "--no-build")
	require.NoError(t, err)
	_, err = run("destroy", "Staging", "--prepared", dir, "--yes")
	require.NoError(t, err)

	_, err = run("plan", "Staging", "--prepared", filepath.Join(t.TempDir(), "none"))
	require.ErrorContains(t, err, "run `superschematic stack prepare`")
	_, err = run("prepare", "Nowhere", "--dir", dir)
	require.ErrorContains(t, err, "has no environment Nowhere")
}
