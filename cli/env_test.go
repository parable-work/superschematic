package cli

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

// standInRelease stands in a release its workflow built for the running
// binary: release 1.2.3, whose archives for this machine's platform a
// stand-in release page serves, which SUPERSCHEMATIC_RELEASE names. The
// user's cache directory is the test's own. It returns the directory
// FetchArchives keeps the archives in.
func standInRelease(t *testing.T) string {
	t.Helper()
	platform := release.Platform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		t.Skipf("a release ships no static archives for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	name := release.ArchiveName("1.2.3", platform)
	top := strings.TrimSuffix(name, ".tar.gz")
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, entry := range []string{top + "/lib/", top + "/lib/libsuperscalar_ffi.a"} {
		hdr := &tar.Header{Name: entry, Mode: 0o644, Typeflag: tar.TypeReg, Size: 4}
		if strings.HasSuffix(entry, "/") {
			hdr.Mode, hdr.Typeflag, hdr.Size = 0o755, tar.TypeDir, 0
		}
		require.NoError(t, tw.WriteHeader(hdr))
		if hdr.Size > 0 {
			_, err := tw.Write([]byte("!ar\n"))
			require.NoError(t, err)
		}
	}
	require.NoError(t, tw.Close())
	require.NoError(t, gz.Close())
	sum := sha256.Sum256(buf.Bytes())
	digest := hex.EncodeToString(sum[:])
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/"+name {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(buf.Bytes())
	}))
	t.Cleanup(srv.Close)
	t.Setenv(release.MirrorEnv, srv.URL)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CACHE_HOME", filepath.Join(home, ".cache"))
	saved := currentRelease
	currentRelease = func() release.Release {
		return release.Release{Version: "1.2.3", Archives: map[string]string{platform: digest}}
	}
	t.Cleanup(func() { currentRelease = saved })
	cache, err := release.ArchivesCacheDir()
	require.NoError(t, err)
	return filepath.Join(cache, digest)
}

// goEnvironment is an environment with a Go server, and noGo one with a
// TypeScript server alone.
var (
	goEnvironment = &ir.ResolvedEnvironment{Environment: "Dev", Deployables: []*ir.ResolvedDeployable{
		{Name: "orders", Kind: ir.DeployableServer, Language: registry.APILanguageGo},
	}}
	noGo = &ir.ResolvedEnvironment{Environment: "Dev", Deployables: []*ir.ResolvedDeployable{
		{Name: "storefront", Kind: ir.DeployableServer, Language: registry.APILanguageTypeScript},
		{Name: "shop-db", Kind: ir.DeployableDatabase},
	}}
)

// TestStackDevLinksTheReleaseArchives: in a project whose naming file
// names no superscalar checkout, stack dev and stack run fetch the
// release's static archives for this machine from its page, keep them in
// the user's cache and hand the go builds CGO_LDFLAGS pointing at them,
// after any the caller set (D47, amended). They set nothing for an
// environment without Go, a project that names a checkout, a naming file
// that renames superscalar's Go module, or a binary built from a checkout.
// A release page that lacks the archives fails the run, unless the caller
// set CGO_LDFLAGS, which the builds then take as it is.
func TestStackDevLinksTheReleaseArchives(t *testing.T) {
	dir := standInRelease(t)
	t.Setenv("CGO_LDFLAGS", "")
	ctx := context.Background()
	var out bytes.Buffer
	env, err := releaseBuildEnv(ctx, naming.Default(), goEnvironment, &out)
	require.NoError(t, err)
	assert.Equal(t, map[string]string{"CGO_LDFLAGS": "-L" + filepath.Join(dir, "lib")}, env)
	assert.FileExists(t, filepath.Join(dir, "lib", "libsuperscalar_ffi.a"))
	assert.Contains(t, out.String(), "The Go builds link the static archives of superschematic 1.2.3 for "+release.Platform(runtime.GOOS, runtime.GOARCH)+", in "+dir)

	t.Setenv("CGO_LDFLAGS", "-L/opt/mine")
	env, err = releaseBuildEnv(ctx, naming.Default(), goEnvironment, &out)
	require.NoError(t, err)
	assert.Equal(t, "-L/opt/mine -L"+filepath.Join(dir, "lib"), env["CGO_LDFLAGS"])

	checkout := naming.Default()
	checkout.Paths.ScalarGo = "third_party/superscalar/go"
	renamed := naming.Default()
	renamed.ScalarGoModule = "example.com/scalars/go"
	for name, names := range map[string]naming.Naming{"a checkout": checkout, "a renamed module": renamed} {
		env, err := releaseBuildEnv(ctx, names, goEnvironment, &out)
		require.NoError(t, err, name)
		assert.Nil(t, env, name)
	}
	env, err = releaseBuildEnv(ctx, naming.Default(), noGo, &out)
	require.NoError(t, err)
	assert.Nil(t, env, "an environment without Go")

	saved := currentRelease
	currentRelease = func() release.Release { return release.Release{} }
	env, err = releaseBuildEnv(ctx, naming.Default(), goEnvironment, &out)
	currentRelease = saved
	require.NoError(t, err)
	assert.Nil(t, env, "a binary built from a checkout")

	// A release page without the archives, and a cache without them.
	t.Setenv(release.MirrorEnv, "http://127.0.0.1:9/nothing")
	require.NoError(t, os.RemoveAll(dir))
	out.Reset()
	env, err = releaseBuildEnv(ctx, naming.Default(), goEnvironment, &out)
	require.NoError(t, err)
	assert.Nil(t, env)
	assert.Contains(t, out.String(), "the Go builds take CGO_LDFLAGS as it is set")
	t.Setenv("CGO_LDFLAGS", "")
	_, err = releaseBuildEnv(ctx, naming.Default(), goEnvironment, &out)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "the Go servers, jobs and workers of environment Dev link the static archives of superschematic 1.2.3, since the naming file's [paths] scalar_go names no superscalar checkout")
	assert.Contains(t, err.Error(), "set CGO_LDFLAGS to -L and a directory that holds them, or [paths] scalar_go to a superscalar checkout")
}

// TestEnvPrintsCGOLinkerFlags: superschematic env fetches the release's
// archives as stack dev does and prints CGO_LDFLAGS as a line a shell
// evaluates, or with its name, its value alone; it refuses a variable it
// does not print, and a binary built from a checkout.
func TestEnvPrintsCGOLinkerFlags(t *testing.T) {
	dir := standInRelease(t)
	run := func(args ...string) (string, error) {
		var buf bytes.Buffer
		root := New(Config{})
		root.SetOut(&buf)
		root.SetErr(&buf)
		root.SetArgs(append([]string{"env"}, args...))
		err := root.Execute()
		return buf.String(), err
	}
	out, err := run()
	require.NoError(t, err)
	assert.Equal(t, "# The static archives of superschematic 1.2.3 for "+release.Platform(runtime.GOOS, runtime.GOARCH)+"\n"+
		"export CGO_LDFLAGS='-L"+filepath.Join(dir, "lib")+"'\n", out)
	out, err = run("CGO_LDFLAGS")
	require.NoError(t, err)
	assert.Equal(t, "-L"+filepath.Join(dir, "lib")+"\n", out)

	_, err = run("GOPATH")
	require.Error(t, err)
	assert.Contains(t, err.Error(), "env prints CGO_LDFLAGS, not GOPATH")

	saved := currentRelease
	currentRelease = func() release.Release { return release.Release{} }
	defer func() { currentRelease = saved }()
	_, err = run()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "this superschematic is built from a checkout, which is no release, and ships no static archives")
}

// TestShellQuoteQuotesASingleQuote: a value with a single quote stays one
// word a shell reads back as it was.
func TestShellQuoteQuotesASingleQuote(t *testing.T) {
	assert.Equal(t, `'-L/a b/it'\''s/lib'`, shellQuote("-L/a b/it's/lib"))
}
