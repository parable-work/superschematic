package release

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
	"strings"
	"sync/atomic"
	"testing"
)

// tarball packs entries, name to content, a name ending in / a directory,
// as a gzipped tarball, and returns it with its hex SHA-256.
func tarball(t *testing.T, entries [][2]string) ([]byte, string) {
	t.Helper()
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	for _, e := range entries {
		hdr := &tar.Header{Name: e[0], Mode: 0o644, Size: int64(len(e[1])), Typeflag: tar.TypeReg}
		if strings.HasSuffix(e[0], "/") {
			hdr.Mode, hdr.Size, hdr.Typeflag = 0o755, 0, tar.TypeDir
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write([]byte(e[1])); err != nil && hdr.Typeflag == tar.TypeReg {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return buf.Bytes(), hex.EncodeToString(sum[:])
}

// releasePage stands in for a release's page: it serves files by name and
// counts the requests.
func releasePage(t *testing.T, files map[string][]byte) (*httptest.Server, *atomic.Int32) {
	t.Helper()
	var hits atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits.Add(1)
		data, ok := files[strings.TrimPrefix(r.URL.Path, "/")]
		if !ok {
			http.NotFound(w, r)
			return
		}
		_, _ = w.Write(data)
	}))
	t.Cleanup(srv.Close)
	return srv, &hits
}

// TestFetchArchivesDownloadsChecksAndKeeps: a release's tarball for a
// platform downloads from the base URL, unpacks without its top directory
// into a directory of the cache named for its digest, whose lib/ holds the
// archives, and a second fetch reads the cache and downloads nothing. Its
// LinkerFlags point cgo's linker at that lib/, after a person's flags.
func TestFetchArchivesDownloadsChecksAndKeeps(t *testing.T) {
	dir := "superschematic-archives_1.2.3_linux-x64"
	data, sum := tarball(t, [][2]string{
		{dir + "/", ""},
		{dir + "/lib/", ""},
		{dir + "/lib/libsuperscalar_ffi.a", "scalar"},
		{dir + "/lib/libsuperschematic_versiongraph.a", "graph"},
		{dir + "/include/superschematic_engine.h", "header"},
	})
	srv, hits := releasePage(t, map[string][]byte{dir + ".tar.gz": data})
	r := Release{Version: "1.2.3", Archives: map[string]string{"linux-x64": sum}}
	cache := filepath.Join(t.TempDir(), "archives")

	got, err := r.FetchArchives(context.Background(), "linux-x64", cache, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if got != filepath.Join(cache, sum) {
		t.Errorf("FetchArchives = %s, want the cache's directory named for the digest", got)
	}
	for name, want := range map[string]string{"lib/libsuperscalar_ffi.a": "scalar", "lib/libsuperschematic_versiongraph.a": "graph", "include/superschematic_engine.h": "header"} {
		if data, err := os.ReadFile(filepath.Join(got, name)); err != nil || string(data) != want {
			t.Errorf("%s holds %q, %v; want %q", name, data, err, want)
		}
	}
	entries, err := os.ReadDir(cache)
	if err != nil || len(entries) != 1 {
		t.Errorf("the cache holds %v, %v; want the one directory, no download or unpacking left behind", entries, err)
	}

	again, err := r.FetchArchives(context.Background(), "linux-x64", cache, srv.URL)
	if err != nil || again != got || hits.Load() != 1 {
		t.Errorf("a second fetch = %s, %v after %d downloads; want %s from the cache after one", again, err, hits.Load(), got)
	}

	if flags := LinkerFlags(got, ""); flags != "-L"+filepath.Join(got, "lib") {
		t.Errorf("LinkerFlags = %q", flags)
	}
	if flags := LinkerFlags(got, " -L/opt/lib "); flags != "-L/opt/lib -L"+filepath.Join(got, "lib") {
		t.Errorf("LinkerFlags after a person's = %q", flags)
	}
	if flags := LinkerFlags("/a b", ""); flags != `"-L/a b/lib"` {
		t.Errorf("LinkerFlags of a path with a space = %q", flags)
	}
}

// TestFetchArchivesRefuses: a binary built from a checkout or not by the
// release workflow names no digest, a tarball that is not the one the
// digest names is refused and nothing of it is kept, a page that has no
// such file fails, and a tarball with an entry outside its directory, or
// one that is no file or directory, is refused.
func TestFetchArchivesRefuses(t *testing.T) {
	ctx := context.Background()
	cache := t.TempDir()
	for _, tc := range []struct {
		r    Release
		want string
	}{
		{Release{}, "built from a checkout, which is no release"},
		{Release{Version: "1.2.3"}, "superschematic 1.2.3, which its release workflow did not build, names no digest of its static archives for linux-x64"},
	} {
		if _, err := tc.r.FetchArchives(ctx, "linux-x64", cache, "http://127.0.0.1:9"); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("FetchArchives = %v, want %q", err, tc.want)
		}
	}

	dir := "superschematic-archives_1.2.3_linux-x64"
	good, _ := tarball(t, [][2]string{{dir + "/lib/libsuperscalar_ffi.a", "scalar"}})
	escape, escapeSum := tarball(t, [][2]string{{dir + "/../../escaped", "x"}})
	noLib, noLibSum := tarball(t, [][2]string{{dir + "/include/h.h", "x"}})
	srv, _ := releasePage(t, map[string][]byte{dir + ".tar.gz": good})
	other, _ := releasePage(t, map[string][]byte{dir + ".tar.gz": escape})
	third, _ := releasePage(t, map[string][]byte{dir + ".tar.gz": noLib})

	wrong := Release{Version: "1.2.3", Archives: map[string]string{"linux-x64": strings.Repeat("0", 64)}}
	if _, err := wrong.FetchArchives(ctx, "linux-x64", cache, srv.URL); err == nil || !strings.Contains(err.Error(), "it is not the release's, so nothing of it is kept") {
		t.Errorf("FetchArchives of a tarball of another digest = %v", err)
	}
	if _, err := wrong.FetchArchives(ctx, "linux-x64", cache, srv.URL+"/missing"); err == nil || !strings.Contains(err.Error(), "404 Not Found") {
		t.Errorf("FetchArchives from a page without the file = %v", err)
	}
	if _, err := (Release{Version: "1.2.3", Archives: map[string]string{"linux-x64": escapeSum}}).FetchArchives(ctx, "linux-x64", cache, other.URL); err == nil || !strings.Contains(err.Error(), "lies outside the tarball's directory") {
		t.Errorf("FetchArchives of a tarball with an entry outside it = %v", err)
	}
	if _, err := (Release{Version: "1.2.3", Archives: map[string]string{"linux-x64": noLibSum}}).FetchArchives(ctx, "linux-x64", cache, third.URL); err == nil || !strings.Contains(err.Error(), "holds no lib/ directory") {
		t.Errorf("FetchArchives of a tarball without lib/ = %v", err)
	}
	if entries, err := os.ReadDir(cache); err != nil || len(entries) != 0 {
		t.Errorf("the cache holds %v, %v after refusals; want nothing", entries, err)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(cache), "escaped")); err == nil {
		t.Error("an entry outside the tarball's directory was written")
	}
}

// TestArchivesBaseIsTheReleasePageUnlessAMirrorIsNamed: the assets come
// from the release's page, or from the base MirrorEnv names, without a
// trailing slash.
func TestArchivesBaseIsTheReleasePageUnlessAMirrorIsNamed(t *testing.T) {
	r := Release{Version: "1.2.3"}
	t.Setenv(MirrorEnv, "")
	if got, want := r.ArchivesBase(), "https://"+Module+"/releases/download/v1.2.3"; got != want {
		t.Errorf("ArchivesBase = %s, want %s", got, want)
	}
	t.Setenv(MirrorEnv, "http://127.0.0.1:8080/mirror/")
	if got := r.ArchivesBase(); got != "http://127.0.0.1:8080/mirror" {
		t.Errorf("ArchivesBase with %s set = %s", MirrorEnv, got)
	}
}
