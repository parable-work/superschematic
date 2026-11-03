package cloudflare_test

import (
	"encoding/json"
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

// TestSiteOriginUnderWorkerd runs the site script serving its origin (D91)
// under the runtime's pinned workerd, with the origin web fixture's Staging
// config and SITE_ORIGIN, and SITE_ORIGIN naming one more API, at a public
// address of another provider: testdata/workerd/origin's tester signs in
// with a cookie session through the site's Worker and calls the API on the
// site's one origin, which the TypeScript identity runtime's own
// cross-origin check passes over its defaults, no trusted origin and no
// cookie domain, and its session cookie stays host-only. The stand-in API
// is bundled from the runtime's source with bun.
func TestSiteOriginUnderWorkerd(t *testing.T) {
	bin := workerdBinary(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		if os.Getenv("SUPERSCHEMATIC_REQUIRE_TS_CHECKS") != "" {
			t.Fatal("bun is not on PATH")
		}
		t.Skip("bun is not on PATH")
	}
	env, errs := resolveOver(t, assemble(t), originWebStack(), webServices(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	version := node(t, env, "storefront-web.version")
	config, _ := siteBinding(version, cloudflare.SiteConfigBinding)["text"].(string)
	originText, _ := siteBinding(version, cloudflare.SiteOriginBinding)["text"].(string)
	var origin []map[string]string
	if err := json.Unmarshal([]byte(originText), &origin); err != nil || config == "" {
		t.Fatalf("Staging's site config %q and origin %q: %v", config, originText, err)
	}
	origin = append(origin, map[string]string{"path": "/api/shop-pricing", "url": "https://shop-pricing.other.test/v1"})
	withPricing, err := json.Marshal(origin)
	if err != nil {
		t.Fatal(err)
	}
	siteURL, _ := env.Deployable("storefront-web").PublicAddress.(string)
	dir := t.TempDir()
	for _, file := range []string{"origin/origin.capnp", "origin/tester.js", "origin/upstream.js", "site/assets.js"} {
		data, err := os.ReadFile(filepath.Join("testdata", "workerd", filepath.FromSlash(file)))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.NewReplacer("$SITE_CONFIG", capnpText(config), "$SITE_ORIGIN", capnpText(string(withPricing)), "$SITE_URL", capnpText(siteURL)).Replace(string(data)))
		if err := os.WriteFile(filepath.Join(dir, filepath.Base(file)), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, cloudflare.SiteModule), []byte(cloudflare.SiteScript()), 0o644); err != nil {
		t.Fatal(err)
	}
	api, err := filepath.Abs(filepath.Join("testdata", "workerd", "origin", "api.ts"))
	if err != nil {
		t.Fatal(err)
	}
	build := exec.Command(bun, "build", "--target=browser", "--external=node:*", "--outfile", filepath.Join(dir, "api.js"), api)
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("bun build the stand-in API: %v\n%s", err, out)
	}
	cmd := exec.Command(bin, "test", "origin.capnp")
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workerd test: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "the site's origin under workerd: a cookie login and an API call on one origin") {
		t.Errorf("the tester did not report its checks:\n%s", out)
	}
}
