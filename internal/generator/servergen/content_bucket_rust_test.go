package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/servergen"
)

// TestRustContentAddressedBucketWiring: with rs-media's config saying
// contentAddressed, rs-stock's Deps holds the runtime's ContentBucket for
// it, which the server's, the job's and the worker's main.rs each build
// over the one bucket they open, with the config's maxBytes, 32 MiB
// without one; a plain bucket's Deps keep the Arc<dyn Bucket> (D142).
func TestRustContentAddressedBucketWiring(t *testing.T) {
	read := func(repoRoot string, parts ...string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(append([]string{repoRoot, "schemas", "dist"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	main := func(repoRoot, server string) string {
		t.Helper()
		return read(repoRoot, "server", rustStack, server, filepath.FromSlash(servergen.RustMainFile))
	}

	plain := t.TempDir()
	loadRustFixture(t).build(t, plain, rustFakePaths(plain), rustOrder...)
	if deps := read(plain, "api", "rs-stock", "src", "deps.rs"); !strings.Contains(deps, "    pub rs_media: Arc<dyn Bucket>,\n") || strings.Contains(deps, "ContentBucket") {
		t.Errorf("rs-stock's Deps of a plain bucket:\n%s", deps)
	}
	if got := main(plain, "rs-stock"); !strings.Contains(got, "        rs_media: Arc::clone(&rs_media_bucket),\n") || strings.Contains(got, "ContentBucket") {
		t.Errorf("rs-stock's main.rs of a plain bucket:\n%s", got)
	}

	for _, test := range []struct {
		maxBytes int64
		want     string
	}{{1 << 20, "1048576"}, {0, "33554432"}} {
		content := t.TempDir()
		f := loadRustFixture(t)
		f.configs["rs-media"].ContentAddressed = true
		f.configs["rs-media"].MaxBytes = test.maxBytes
		f.build(t, content, rustFakePaths(content), rustOrder...)
		deps := read(content, "api", "rs-stock", "src", "deps.rs")
		if !strings.Contains(deps, "    pub rs_media: ContentBucket,\n") || !strings.Contains(deps, test.want+" bytes under\n    /// the SHA-256") ||
			strings.Contains(deps, "Arc<dyn Bucket>") || !strings.Contains(deps, "bucket::ContentBucket") {
			t.Errorf("rs-stock's Deps of a content-addressed bucket of %d bytes:\n%s", test.maxBytes, deps)
		}
		server := main(content, "rs-stock")
		if !strings.Contains(server, "        rs_media: ContentBucket::new(Arc::clone(&rs_media_bucket), "+test.want+"),\n") ||
			!strings.Contains(server, "::bucket::{Bucket, ContentBucket};") || strings.Count(server, "let rs_media_bucket = open_bucket(") != 1 {
			t.Errorf("rs-stock's main.rs of a content-addressed bucket of %d bytes:\n%s", test.maxBytes, server)
		}
		for _, background := range []string{rustStockJob, rustStockWorker} {
			if got := main(content, background); !strings.Contains(got, "        rs_media: ContentBucket::new(rs_media_bucket, "+test.want+"),\n") {
				t.Errorf("%s's main.rs of a content-addressed bucket:\n%s", background, got)
			}
		}
	}
}
