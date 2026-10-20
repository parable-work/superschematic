package pulumi_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/pulumi"
)

// callsEnvironment reads the cloudflare target's golden Preview of its
// calls fixture, a storefront Worker calling an internal pricing Worker
// through a service binding and listing an R2 bucket (D64), and pins both
// Workers' bundles as a deploy does, to bundles of fixed files.
func callsEnvironment(t *testing.T) *ir.ResolvedEnvironment {
	t.Helper()
	data, err := os.ReadFile(stack.EnvironmentPath(cloudflareGoldenRoot, "calls-stack", "Preview"))
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	bundles := map[string]stack.Bundle{}
	for n, server := range []string{"shop-pricing", "shop-storefront"} {
		files := map[string]string{}
		for i, name := range []string{"main.js", "main.js.map", "superscalar_wasm_bg.wasm"} {
			files[name] = strings.Repeat(string(rune('a'+3*n+i)), 64)
		}
		bundles[server] = stack.Bundle{Digest: "sha256:" + strings.Repeat(string(rune('0'+n)), 64), Files: files}
	}
	pinned, err := stack.PinBundles(env, bundles)
	if err != nil {
		t.Fatal(err)
	}
	return pinned
}

// TestRenderCloudflareCalls renders the calls fixture's Preview with the
// cloudflare and tls providers pinned as the binary pins them, and
// compares it with its golden copy: the edge's key, the storefront's
// Worker after the pricing Worker its service binding names, the service
// and r2_bucket bindings, the bucket token's resources interpolating the
// bucket's name, and each member's names under the parameter. The program
// exports the token's id, which a plain binding reads, and neither the
// token's value nor the key's private key, which secret bindings read.
func TestRenderCloudflareCalls(t *testing.T) {
	p := pulumi.Extension{ProviderVersions: map[string]string{
		cloudflare.Package:      cloudflare.ProviderVersion,
		stack.GenericKeyPackage: stack.GenericKeyProviderVersion,
	}}.Provisioner()
	env := callsEnvironment(t)
	dir := t.TempDir()
	if err := p.Render(env, dir); err != nil {
		t.Fatal(err)
	}
	got, err := os.ReadFile(filepath.Join(dir, pulumi.ProgramFile))
	if err != nil {
		t.Fatal(err)
	}
	checkGolden(t, filepath.Join("testdata", "render", "cloudflare-calls-preview", pulumi.ProgramFile), got)

	exports, err := pulumi.Exports(env)
	if err != nil {
		t.Fatal(err)
	}
	keys := map[string]bool{}
	for _, exp := range exports {
		keys[exp.Key] = true
	}
	for key, want := range map[string]bool{
		"shop-storefront.r2.shop-media.token.id":                    true,
		"shop-storefront.r2.shop-media.token.value":                 false,
		"shop-storefront.calls.shop-pricing.key.publicKeyPem":       true,
		"shop-storefront.calls.shop-pricing.key.privateKeyPemPkcs8": false,
	} {
		if keys[key] != want {
			t.Errorf("the program exports %s: %v, want %v", key, keys[key], want)
		}
	}
}
