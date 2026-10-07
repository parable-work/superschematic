package release

import (
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

// digest is a made-up hex SHA-256.
var digest = strings.Repeat("ab", 32)

// TestParseDigests: the release workflow's platform=digest pairs, and what
// is refused: a pair without =, a platform no release ships, a platform
// named twice, and a digest that is not a lower-case hex SHA-256.
func TestParseDigests(t *testing.T) {
	got, err := ParseDigests("linux-x64=" + digest + ",darwin-arm64=" + strings.Repeat("0", 64))
	if err != nil {
		t.Fatal(err)
	}
	if want := map[string]string{"linux-x64": digest, "darwin-arm64": strings.Repeat("0", 64)}; !maps.Equal(got, want) {
		t.Errorf("ParseDigests = %v, want %v", got, want)
	}
	if got, err := ParseDigests(""); err != nil || got != nil {
		t.Errorf("ParseDigests(\"\") = %v, %v; want none", got, err)
	}
	for in, want := range map[string]string{
		"linux-x64":             "is not platform=digest",
		"windows-x64=" + digest: "no platform a release ships",
		"linux-x64=" + digest + ",linux-x64=" + digest: "two archive digests for linux-x64",
		"linux-x64=abc":                                     "is not a lower-case hex SHA-256",
		"linux-x64=" + strings.ToUpper(digest):              "is not a lower-case hex SHA-256",
		"linux-x64=" + strings.Repeat("g", 64):              "is not a lower-case hex SHA-256",
		"linux-x64=" + digest + "," + "linux-arm64=" + "xy": "is not a lower-case hex SHA-256",
	} {
		if _, err := ParseDigests(in); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ParseDigests(%q) = %v, want an error containing %q", in, err, want)
		}
	}
}

// TestPlatform: the release names a platform as its assets do, and ships
// nothing for another.
func TestPlatform(t *testing.T) {
	for _, tc := range []struct{ goos, goarch, want string }{
		{"linux", "amd64", "linux-x64"},
		{"linux", "arm64", "linux-arm64"},
		{"darwin", "arm64", "darwin-arm64"},
		{"darwin", "amd64", "darwin-x64"},
		{"windows", "amd64", ""},
		{"linux", "386", ""},
	} {
		if got := Platform(tc.goos, tc.goarch); got != tc.want {
			t.Errorf("Platform(%s, %s) = %q, want %q", tc.goos, tc.goarch, got, tc.want)
		}
	}
}

// TestArchive: a release names the URL and digest of each platform's
// archives, and its modules' version; a checkout build names neither.
func TestArchive(t *testing.T) {
	r := Release{Version: "1.2.3", Archives: map[string]string{"linux-x64": digest}}
	if got, want := DownloadURL(r.Version, ArchiveName(r.Version, "linux-x64")), "https://github.com/parable-work/superschematic/releases/download/v1.2.3/superschematic-archives_1.2.3_linux-x64.tar.gz"; got != want {
		t.Errorf("the archives' URL = %q, want %q", got, want)
	}
	if got, ok := r.Archive("linux-x64"); !ok || got != digest {
		t.Errorf("Archive(linux-x64) = %q, %v", got, ok)
	}
	if _, ok := r.Archive("linux-arm64"); ok {
		t.Error("Archive(linux-arm64) names a digest the release does not")
	}
	if got := r.ModuleVersion(); got != "v1.2.3" {
		t.Errorf("ModuleVersion = %q", got)
	}
	checkout := Release{Archives: r.Archives}
	if _, ok := checkout.Archive("linux-x64"); ok || checkout.ModuleVersion() != "" {
		t.Error("a checkout build names archives or a module version")
	}
}

// TestCurrent: a binary built from a checkout, as a test binary is, is no
// release, and names the archives' digests the release workflow links into
// it. This test binary links no superscalar, so it names no version of its
// Go binding; the CLI's does.
func TestCurrent(t *testing.T) {
	old := archiveDigests
	t.Cleanup(func() { archiveDigests = old })
	archiveDigests = "linux-arm64=" + digest
	r := Current()
	if r.Version != "" || r.ScalarGo != "" {
		t.Errorf("a test binary is release %q linking superscalar %q", r.Version, r.ScalarGo)
	}
	if !maps.Equal(r.Archives, map[string]string{"linux-arm64": digest}) {
		t.Errorf("Archives = %v", r.Archives)
	}
	archiveDigests = "linux-arm64=nope"
	if r := Current(); r.Archives != nil {
		t.Errorf("malformed digests give Archives %v", r.Archives)
	}
}

// TestTheReleaseLinksTheDigests: the release workflow links each CLI with
// every platform's archives digest, in the variable Current reads, and
// packs each tarball under the name ArchiveName gives, as
// scripts/release-archives.sh does.
func TestTheReleaseLinksTheDigests(t *testing.T) {
	_, file, _, _ := runtime.Caller(0)
	root := filepath.Join(filepath.Dir(file), "..", "..")
	read := func(name string) string {
		data, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(name)))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	_ = archiveDigests // the variable the workflow's -X names
	workflow := read(".github/workflows/release.yml")
	symbol := reflect.TypeOf(Release{}).PkgPath() + ".archiveDigests"
	for _, want := range []string{
		"-X " + symbol + "=$digests",
		"for platform in " + strings.Join([]string{"linux-x64", "linux-arm64", "darwin-arm64", "darwin-x64"}, " ") + "; do",
		`tarball="archives/` + ArchiveName("${VERSION}", "${platform}") + `"`,
		"for bin in superschematic superschematic-migrate superschematic-archives; do",
		`scripts/release-archives.sh "$VERSION" "$NAME" dist`,
	} {
		if !strings.Contains(workflow, want) {
			t.Errorf(".github/workflows/release.yml lacks %q", want)
		}
	}
	for _, platform := range Platforms {
		if !strings.Contains(workflow, "name: "+platform+"\n") {
			t.Errorf(".github/workflows/release.yml builds no archives or CLI for %s", platform)
		}
	}
	script := read("scripts/release-archives.sh")
	if want := `name="` + strings.TrimSuffix(ArchiveName("${VERSION}", "${PLATFORM}"), ".tar.gz") + `"`; !strings.Contains(script, want) {
		t.Errorf("scripts/release-archives.sh lacks %q", want)
	}
}
