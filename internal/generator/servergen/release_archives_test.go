package servergen_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/release"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestALocalGoBuildLinksTheArchivesTheReleaseFetches: in a project whose
// naming file names no superscalar checkout, Storefront's module, built as
// a release builds it, links once its go build takes the CGO_LDFLAGS that
// stack dev and `superschematic env` give it, and fails at the link
// without them (D47, amended). A file server stands in for the release's
// page, holding a tarball packed as a release packs it from the archive
// this checkout built for this machine, and the release fetches it into a
// cache of the test's own. superscalar's Go binding comes from the module
// proxy at the version this binary links. It runs outside -short, where
// the checkout's archive is built.
func TestALocalGoBuildLinksTheArchivesTheReleaseFetches(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping go build in -short mode")
	}
	platform := release.Platform(runtime.GOOS, runtime.GOARCH)
	if platform == "" {
		t.Skipf("a release ships no static archives for %s/%s", runtime.GOOS, runtime.GOARCH)
	}
	archive := filepath.Join(testpaths.Local(t).ScalarGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperscalar_ffi.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("no superscalar archive is built for this machine (make setup builds it): %v", err)
	}
	repoRoot := dockerRepo(t)
	f := loadFixture(t, servicesRoot)
	r := release.Release{Version: "1.2.3", ScalarGo: scalarGoVersion(t), Archives: map[string]string{}}
	page := t.TempDir()
	r.Archives[platform] = packArchives(t, filepath.Join(page, release.ArchiveName(r.Version, platform)), archive)

	// The project: the runtime modules from its checkout, superscalar
	// from the module proxy, and no superscalar checkout at all.
	if err := os.RemoveAll(filepath.Join(repoRoot, "third_party")); err != nil {
		t.Fatal(err)
	}
	paths := fakePaths(repoRoot)
	paths.ScalarGo = ""
	for _, name := range append(slices.Clone(apis), cloudStack) {
		opts := f.options(repoRoot, paths)
		opts.Release = &r
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	module := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), cloudStack, "Storefront")
	build := func(ldflags string) (string, error) {
		cmd := exec.Command("go", "build", "-o", filepath.Join(t.TempDir(), "storefront"), ".")
		cmd.Dir = module
		cmd.Env = append(os.Environ(), "GOWORK=off", "GOFLAGS=-mod=mod", "CGO_ENABLED=1", "CGO_LDFLAGS="+ldflags)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := build(""); err == nil || !strings.Contains(out, "superscalar_ffi") {
		t.Fatalf("go build with no CGO_LDFLAGS = %v, want it to fail linking superscalar's archive:\n%s", err, out)
	}

	srv := httptest.NewServer(http.FileServer(http.Dir(page)))
	t.Cleanup(srv.Close)
	dir, err := r.FetchArchives(context.Background(), platform, filepath.Join(t.TempDir(), "cache"), srv.URL)
	if err != nil {
		t.Fatal(err)
	}
	if out, err := build(release.LinkerFlags(dir, "")); err != nil {
		t.Fatalf("go build with the fetched archives' CGO_LDFLAGS: %v\n%s", err, out)
	}
}
