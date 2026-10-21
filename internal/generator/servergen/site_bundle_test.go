package servergen_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sitegen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// siteProbe is a page's script that lists a cart of ts-shop through the
// client the site's config hands out, and checks an address through
// superscalar, with a fetch that answers as the site's origin and ts-shop
// would, so the bundle runs where no server is.
const siteProbe = `import { parseContactEmail } from 'superscalar';
import { loadApis } from '../config.generated';

const shop = 'https://ts-shop.example';
const calls: string[] = [];
const answer = async (input: RequestInfo | URL): Promise<Response> => {
  const url = typeof input === 'string' ? input : input instanceof URL ? input.href : input.url;
  calls.push(url);
  if (url === '/__superschematic/config.json') {
    return Response.json({ apis: { 'ts-shop': { url: shop + '/' } } });
  }
  if (url === shop + '/api/carts/tea') {
    return Response.json({ data: { id: 'tea', cents: 300, region: 'eu' }, meta: { requestId: 'probe' } });
  }
  return new Response('{}', { status: 404 });
};
const fetcher = answer as typeof fetch;

const { tsShop } = await loadApis(fetcher);
const cart = await tsShop.client({ fetch: fetcher }).carts.getCart('tea');
console.log(JSON.stringify({ baseUrl: tsShop.baseUrl, cart, email: parseContactEmail(' Ada@Example.COM '), calls }));
`

// TestASiteBundlesItsAPIsSDKForTheBrowser: the typed browser config of the
// fixture's site ts-web hands out a client of ts-shop's TypeScript SDK,
// and the scaffold's package depends on the SDK (D55, amended). The site
// type-checks with no Node types, and `bun build --target=browser`, with
// no plugin, bundles a script that calls ts-shop through that client: the
// bundle imports nothing from node: and carries superscalar's browser
// build with its WebAssembly module inlined. Node runs it as a browser
// would, since it reaches for no Node API: it reads the site's config,
// calls ts-shop at the address the config names, and parses an address
// through superscalar's WebAssembly core.
func TestASiteBundlesItsAPIsSDKForTheBrowser(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the site bundle in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	node, err := exec.LookPath("node")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("node not available: %v", err))
	}
	paths := testpaths.Local(t)
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist", "browser", "backend.js")); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar's browser build is not built (scripts/superscalar-dep.sh): %v", err))
	}
	installTypeScriptRuntime(t, bun, paths)
	repoRoot := testpaths.TempDir(t)
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, paths, append(slices.Clone(tsOrder[:len(tsOrder)-1]), tsSite)...)

	site := naming.Default().SiteImplementationDir(repoRoot, tsSite)
	config, err := os.ReadFile(filepath.Join(site, sitegen.ConfigFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"import { TsShopSDK, type SDKConfig as TsShopSDKConfig } from '@schemas/ts-shop-sdk';",
		"readonly tsShop: ApiEndpoint<TsShopSDK, TsShopSDKConfig>;",
	} {
		if !strings.Contains(string(config), want) {
			t.Errorf("%s lacks %q:\n%s", sitegen.ConfigFile, want, config)
		}
	}
	manifest, err := os.ReadFile(filepath.Join(site, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	want := `"@schemas/ts-shop-sdk": "workspace:*"`
	if !strings.Contains(string(manifest), want) {
		t.Errorf("the scaffold's package.json lacks %s:\n%s", want, manifest)
	}
	// The probe imports superscalar itself, as a site that checks a field
	// before it sends it would; the workspace's root resolves it to the
	// checkout.
	manifest = []byte(strings.Replace(string(manifest), want, want+",\n    \"superscalar\": \"*\"", 1))
	if err := os.WriteFile(filepath.Join(site, "package.json"), manifest, 0o644); err != nil {
		t.Fatal(err)
	}

	install := exec.Command(bun, "install")
	install.Dir = filepath.Join(repoRoot, "schemas", "dist")
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, out))
	}
	if err := os.WriteFile(filepath.Join(site, "src", "probe.ts"), []byte(siteProbe), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(dir, name string, args ...string) string {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("%s %s in %s: %v\n%s", filepath.Base(name), strings.Join(args, " "), dir, err, out)
		}
		return string(out)
	}
	// The scaffold's tsconfig names no types: the SDK, its types package and
	// superscalar type-check without Node's.
	run(site, filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc"), "--noEmit", "-p", "tsconfig.json")
	// The scaffold's own build bundles index.html and its script.
	run(site, bun, "run", "build")

	run(site, bun, "build", "--target=browser", "--outdir=probe", "src/probe.ts")
	bundle, err := os.ReadFile(filepath.Join(site, "probe", "probe.js"))
	if err != nil {
		t.Fatal(err)
	}
	for _, refused := range []string{`"node:`, `'node:`, "require("} {
		if strings.Contains(string(bundle), refused) {
			t.Errorf("the site's bundle holds %s, which no browser loads", refused)
		}
	}
	// \0asm, the WebAssembly module's magic, in base64: superscalar's
	// browser build inlines its module.
	if !strings.Contains(string(bundle), "AGFzbQ") {
		t.Error("the site's bundle does not carry superscalar's WebAssembly module")
	}
	out := run(site, node, filepath.Join("probe", "probe.js"))
	want = `{"baseUrl":"https://ts-shop.example","cart":{"id":"tea","cents":300,"region":"eu"},"email":"ada@example.com","calls":["/__superschematic/config.json","https://ts-shop.example/api/carts/tea"]}`
	if strings.TrimSpace(out) != want {
		t.Errorf("the bundle printed\n%s\nwant\n%s", out, want)
	}
}
