package cloudflare_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// bundleFiles are the files a server's bundle.ts writes, by name, as the
// fixture's stand-in for bun writes them: content that depends on the
// version.
func bundleFiles(version string) map[string]string {
	return map[string]string{
		cloudflare.MainModule: "export default { fetch: () => new Response(\"" + version + "\") };\n",
		cloudflare.SourceMap:  `{"version":3,"sources":["fetch.ts"],"mappings":""}` + "\n",
		cloudflare.WasmModule: "\x00asm\x01\x00\x00\x00" + version,
	}
}

// writeFiles writes files into dir, and tool as an executable file when
// it is set.
func writeFiles(t *testing.T, dir string, files map[string]string, tool string) {
	t.Helper()
	for name, content := range files {
		p := filepath.Join(dir, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if tool != "" {
		p := filepath.Join(dir, "bin", "tool")
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(tool), 0o755); err != nil {
			t.Fatal(err)
		}
	}
}

func hexSum(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

// storefrontRun is a run of the storefront's Staging over the fixture.
func storefrontRun(t *testing.T, f *fixture) registry.Run {
	t.Helper()
	env, errs := resolveOver(t, f.reg, storefrontStack(), stacktest.AcmeShop(), "Staging")
	if errs != nil {
		t.Fatal(errs)
	}
	return registry.Run{Environment: env}
}

// TestBundleStore publishes a bundle's files to the state bucket under the
// digest, each with its SHA-256 and mode and the marker last, and fetches
// them back, an executable file executable; a second publish of the digest
// puts nothing. A digest no publish finished, a file whose content does
// not match its SHA-256, and a deployable that runs no bundle are refused.
func TestBundleStore(t *testing.T) {
	f := newFixture(t)
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	store := f.target(t).Bundles
	fetcher := store.(registry.ArtifactFetcher)
	run := storefrontRun(t, f)
	dir := t.TempDir()
	files := bundleFiles("v1")
	writeFiles(t, dir, files, "#!/bin/sh\n")
	digest := "sha256:" + strings.Repeat("ab", 32)
	prefix := cloudflare.BundleObjects("storefront-stack", "shop-storefront", digest)
	if want := "superschematic/bundles/storefront-stack/shop-storefront/" + strings.Repeat("ab", 32) + "/"; prefix != want {
		t.Fatalf("the bundle's prefix is %s, want %s", prefix, want)
	}

	from := len(f.cloud.Changes())
	if err := store.Publish(ctx, run, "shop-storefront", dir, digest); err != nil {
		t.Fatal(err)
	}
	changes := f.cloud.Changes()[from:]
	if len(changes) != 5 || changes[4] != "put "+cloudflare.StateBucket+"/"+prefix+"__superschematic/files" {
		t.Fatalf("the publish changed %q, want the four files and the marker last", changes)
	}
	for name, content := range files {
		data, meta, err := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, prefix+name)
		if err != nil || string(data) != content || meta["sha256"] != hexSum(content) || meta["mode"] != "644" {
			t.Errorf("%s is %q with %v, %v", name, data, meta, err)
		}
	}
	if _, meta, _ := f.cloud.GetObject(ctx, accountID, cloudflare.StateBucket, prefix+"bin/tool"); meta["mode"] != "755" {
		t.Errorf("the executable is kept with %v", meta)
	}

	from = len(f.cloud.Changes())
	if err := store.Publish(ctx, run, "shop-storefront", dir, digest); err != nil {
		t.Fatal(err)
	}
	if changes := f.cloud.Changes()[from:]; len(changes) != 0 {
		t.Errorf("a second publish of the digest changed %q", changes)
	}

	dest := t.TempDir()
	if err := fetcher.Fetch(ctx, run, "shop-storefront", digest, dest); err != nil {
		t.Fatal(err)
	}
	for name, content := range files {
		data, err := os.ReadFile(filepath.Join(dest, name))
		if err != nil || string(data) != content {
			t.Errorf("the fetched %s is %q, %v", name, data, err)
		}
		if info, _ := os.Stat(filepath.Join(dest, name)); info.Mode().Perm() != 0o644 {
			t.Errorf("the fetched %s has mode %v", name, info.Mode())
		}
	}
	if info, err := os.Stat(filepath.Join(dest, "bin", "tool")); err != nil || info.Mode().Perm() != 0o755 {
		t.Errorf("the fetched executable is %v, %v", info, err)
	}
	if _, err := os.Stat(filepath.Join(dest, "__superschematic")); err == nil {
		t.Error("the fetch wrote the marker")
	}

	other := "sha256:" + strings.Repeat("cd", 32)
	if err := fetcher.Fetch(ctx, run, "shop-storefront", other, t.TempDir()); err == nil || !strings.Contains(err.Error(), "no files under "+other) {
		t.Errorf("a fetch of a digest never published = %v", err)
	}
	if err := f.cloud.PutObject(ctx, accountID, cloudflare.StateBucket, prefix+cloudflare.MainModule, []byte("tampered"), map[string]string{"sha256": hexSum(files[cloudflare.MainModule]), "mode": "644"}); err != nil {
		t.Fatal(err)
	}
	if err := fetcher.Fetch(ctx, run, "shop-storefront", digest, t.TempDir()); err == nil || !strings.Contains(err.Error(), "main.js has SHA-256 "+hexSum("tampered")) {
		t.Errorf("a fetch of a file that changed = %v", err)
	}
	if err := store.Publish(ctx, run, "nobody", dir, digest); err == nil || !strings.Contains(err.Error(), "no deployable nobody that runs a bundle") {
		t.Errorf("a publish for no deployable = %v", err)
	}
}

// bundleBuild stands in for bun in a deploy's Sources: the frozen install
// does nothing, and `bun bundle.ts <dir>` writes the files of the
// current version into dir.
type bundleBuild struct {
	version string
	ran     []string
}

func (b *bundleBuild) run(_ context.Context, dir string, _ io.Writer, name string, args ...string) error {
	b.ran = append(b.ran, filepath.Base(dir)+": "+name+" "+strings.Join(args[:1], " "))
	if len(args) == 2 && args[0] == "bundle.ts" {
		for file, content := range bundleFiles(b.version) {
			if err := os.WriteFile(filepath.Join(args[1], file), []byte(content), 0o644); err != nil {
				return err
			}
		}
	}
	return nil
}

// TestDeployPublishesAndFetchesTheBundle deploys the storefront's Staging
// on the cloudflare target: the deploy runs bundle.ts, publishes the
// bundle to the state bucket before its first step, pins its digest and
// each file's SHA-256 into the Worker's version, and places the files
// under the program; a deploy with --no-build fetches them back from R2.
func TestDeployPublishesAndFetchesTheBundle(t *testing.T) {
	f := newFixture(t)
	f.shell[cloudflare.PassphraseEnv] = "the-passphrase"
	ctx := context.Background()
	if _, err := f.cloud.EnsureBucket(ctx, accountID, cloudflare.StateBucket); err != nil {
		t.Fatal(err)
	}
	run := storefrontRun(t, f)
	root := t.TempDir()
	pkg := filepath.Join(root, "dist", "server", "storefront-stack", "shop-storefront")
	if err := os.MkdirAll(pkg, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(pkg, "bundle.ts"), []byte("// written by the server generator\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	build := &bundleBuild{version: "v1"}
	program := t.TempDir()
	log := &bytes.Buffer{}
	deploy := func(sources *stack.Sources) *stack.Manifest {
		t.Helper()
		m, err := stack.Deploy(ctx, stack.DeployOptions{
			Options: stack.Options{Registry: f.reg, Run: run, Dir: program, Log: log},
			Sources: sources,
		})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, log)
		}
		return m
	}

	m := deploy(&stack.Sources{OutputRoot: filepath.Join(root, "dist"), RepositoryRoot: root, ImplementationRoot: root, Run: build.run})
	bundle := m.Bundles["shop-storefront"]
	if bundle == nil || bundle.Files[cloudflare.MainModule] != hexSum(bundleFiles("v1")[cloudflare.MainModule]) || len(bundle.Files) != 3 {
		t.Fatalf("the manifest records the bundle as %+v", bundle)
	}
	if got := strings.Join(build.ran, "; "); got != "dist: bun install; shop-storefront: bun bundle.ts" {
		t.Errorf("the deploy ran %s", got)
	}
	prefix := cloudflare.BundleObjects("storefront-stack", "shop-storefront", bundle.Digest)
	keys, err := f.cloud.ListObjects(ctx, accountID, cloudflare.StateBucket, prefix)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{prefix + "__superschematic/files", prefix + "main.js", prefix + "main.js.map", prefix + "superscalar_wasm_bg.wasm"}; !slices.Equal(keys, want) {
		t.Errorf("the state bucket holds %v, want %v", keys, want)
	}
	version := f.prov.Rendered().Resources.Resource("shop-storefront.version")
	hexDigest := strings.TrimPrefix(bundle.Digest, "sha256:")
	for _, m := range version.Properties["modules"].([]any) {
		m := m.(map[string]any)
		name := m["name"].(string)
		if m["contentFile"] != "artifacts/shop-storefront/"+hexDigest+"/"+name || m["contentSha256"] != hexSum(bundleFiles("v1")[name]) {
			t.Errorf("the rendered version reads %s as %v", name, m)
		}
		placed, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, "shop-storefront", hexDigest, name))
		if err != nil || string(placed) != bundleFiles("v1")[name] {
			t.Errorf("the placed %s holds %q, %v", name, placed, err)
		}
	}

	if err := os.RemoveAll(filepath.Join(program, ir.ArtifactsDir)); err != nil {
		t.Fatal(err)
	}
	from := len(f.cloud.Changes())
	again := deploy(nil)
	if again.Bundles["shop-storefront"].Digest != bundle.Digest {
		t.Errorf("--no-build runs %s, want the manifest's %s", again.Bundles["shop-storefront"].Digest, bundle.Digest)
	}
	for _, change := range f.cloud.Changes()[from:] {
		if strings.Contains(change, "/bundles/") {
			t.Errorf("--no-build changed %s", change)
		}
	}
	if placed, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, "shop-storefront", hexDigest, cloudflare.WasmModule)); err != nil || string(placed) != bundleFiles("v1")[cloudflare.WasmModule] {
		t.Errorf("--no-build placed superscalar_wasm_bg.wasm as %q, %v", placed, err)
	}
}
