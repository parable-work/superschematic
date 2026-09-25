package cli

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/buildcache"
)

// TestNewAppliesToolDigest: New pins the build cache's tool digest from
// Config, and a Config without one restores the executable hash.
func TestNewAppliesToolDigest(t *testing.T) {
	t.Cleanup(func() { buildcache.SetToolDigest("") })

	New(Config{})
	executableHash := buildcache.ToolDigest()
	require.NotEmpty(t, executableHash)

	New(Config{ToolDigest: "acme-sources-1"})
	assert.Equal(t, "acme-sources-1", buildcache.ToolDigest())

	New(Config{})
	assert.Equal(t, executableHash, buildcache.ToolDigest())
}

// TestToolDigestSharesCacheAcrossExecutables links two binaries whose bytes
// differ and runs build-all with each against one cache root. With the same
// ToolDigest the second restores what the first stored; with a different
// ToolDigest it builds; with none, the key is the executable's hash as
// before, so only the same executable restores.
func TestToolDigestSharesCacheAcrossExecutables(t *testing.T) {
	if testing.Short() {
		t.Skip("links two binaries")
	}
	// The two links run side by side.
	bins := t.TempDir()
	exes := [2]string{filepath.Join(bins, "a", "superschematic"), filepath.Join(bins, "b", "superschematic")}
	var links [2]*exec.Cmd
	var linkOutput [2]bytes.Buffer
	for i, checkout := range []string{"a", "b"} {
		links[i] = exec.Command("go", "build", "-ldflags=-X main.checkout="+checkout, "-o", exes[i], "./testdata/tooldigest")
		links[i].Stdout, links[i].Stderr = &linkOutput[i], &linkOutput[i]
		require.NoError(t, links[i].Start())
	}
	for i, cmd := range links {
		require.NoError(t, cmd.Wait(), "go build: %s", linkOutput[i].String())
	}
	exeA, exeB := exes[0], exes[1]
	bytesA, err := os.ReadFile(exeA)
	require.NoError(t, err)
	bytesB, err := os.ReadFile(exeB)
	require.NoError(t, err)
	require.False(t, bytes.Equal(bytesA, bytesB), "the two builds must differ byte for byte")

	servicesRoot := prepareJSONServicesRoot(t)
	outDir := filepath.Join(t.TempDir(), "dist")
	cacheRoot := t.TempDir()
	run := func(exe, digest string) string {
		t.Helper()
		// Dropping the outputs forces a restore or a build.
		require.NoError(t, os.RemoveAll(outDir))
		cmd := exec.Command(exe, "build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot)
		cmd.Env = append(os.Environ(), "SUPERSCHEMATIC_TEST_TOOL_DIGEST="+digest)
		output, err := cmd.CombinedOutput()
		require.NoError(t, err, "%s build-all: %s", exe, output)
		return string(output)
	}
	const restored = "OK: fixture-db (restored from cache)"
	const built = "OK: fixture-db (built"

	assert.Contains(t, run(exeA, "sources-1"), built)
	assert.Contains(t, run(exeB, "sources-1"), restored, "the same ToolDigest shares entries across executables")
	assert.Contains(t, run(exeB, "sources-2"), built, "a different ToolDigest shares nothing")

	assert.Contains(t, run(exeA, ""), built, "the executable hash is a key of its own")
	assert.Contains(t, run(exeA, ""), restored, "without a ToolDigest the same executable restores")
	assert.Contains(t, run(exeB, ""), built, "without a ToolDigest a different executable builds")
}
