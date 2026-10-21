package cloudflare_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// workerdBinary is the workerd the HTTP runtime's harness pins
// (runtime/http/typescript/test/workerd), which `bun install
// --frozen-lockfile` there installs. Without it the test is skipped, and
// fails when SUPERSCHEMATIC_REQUIRE_TS_CHECKS is set, as CI sets it.
func workerdBinary(t *testing.T) string {
	t.Helper()
	bin, err := filepath.Abs(filepath.Join("..", "..", "runtime", "http", "typescript", "test", "workerd", "node_modules", ".bin", "workerd"))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(bin); err != nil {
		msg := "workerd is not installed: run bun install --frozen-lockfile in runtime/http/typescript/test/workerd"
		if os.Getenv("SUPERSCHEMATIC_REQUIRE_TS_CHECKS") != "" {
			t.Fatal(msg)
		}
		t.Skip(msg)
	}
	return bin
}

// capnpText is a string as the contents of a Cap'n Proto text literal.
func capnpText(s string) string {
	return strings.NewReplacer(`\`, `\\`, `"`, `\"`, "\n", `\n`).Replace(s)
}

// TestSiteScriptUnderWorkerd runs the script every site's Worker runs,
// the bytes the extension embeds, under the runtime's pinned workerd with
// the web fixture's Staging config, beside a stand-in for the static
// assets: testdata/workerd/site's tester reads the config, uncached, a
// directory's index.html, the fallback at 200 for a route of the page, 404
// for one without a fallback, and 405 for a POST (section 18.8).
func TestSiteScriptUnderWorkerd(t *testing.T) {
	bin := workerdBinary(t)
	env := resolveWeb(t, assemble(t), "Staging")
	config, ok := siteBinding(node(t, env, "storefront-web.version"), cloudflare.SiteConfigBinding)["text"].(string)
	if !ok {
		t.Fatal("Staging's site config is not a string")
	}
	dir := t.TempDir()
	for _, file := range []string{"assets.js", "tester.js", "site.capnp"} {
		data, err := os.ReadFile(filepath.Join("testdata", "workerd", "site", file))
		if err != nil {
			t.Fatal(err)
		}
		if file == "site.capnp" {
			data = []byte(strings.ReplaceAll(string(data), "$SITE_CONFIG", capnpText(config)))
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, cloudflare.SiteModule), []byte(cloudflare.SiteScript()), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(bin, "test", "site.capnp")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workerd test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "the site script under workerd: the config, a directory, the fallback and a 404") {
		t.Errorf("the tester did not report its checks:\n%s", out)
	}
}
