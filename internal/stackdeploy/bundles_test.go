package stackdeploy_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// bundleEnv resolves an environment of the shop stack with shop-storefront
// on the fake bundle platform in Staging and Preview (D64), without
// shop-orders' worker.
func (f *fixture) bundleEnv(t *testing.T, name string) *ir.ResolvedEnvironment {
	t.Helper()
	env := f.resolve(t, stacktest.WithoutWorkerSettings(stacktest.WithBundle(stacktest.Shop())), stacktest.WithoutWorkers(stacktest.AcmeShop()), name)
	if d := env.Deployable(stacktest.StorefrontServer); d == nil || !d.HasBundle() {
		t.Fatalf("%s's storefront = %+v, want a server that runs a bundle", name, d)
	}
	return env
}

// bundleSources is an output root holding the fixture package of
// shop-storefront, whose bundle.ts the real bun runs. The frozen install
// is recorded and skipped, since the fixture's workspace has no lockfile.
type bundleSources struct {
	root string
	pkg  string
	ran  []string
}

func newBundleSources(t *testing.T, script, source string) *bundleSources {
	t.Helper()
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skip("bun is not on PATH")
	}
	root := t.TempDir()
	pkg, err := stacktest.WriteBundlePackage(filepath.Join(root, "dist"), "shop-stack", stacktest.StorefrontServer, script, source)
	if err != nil {
		t.Fatal(err)
	}
	return &bundleSources{root: root, pkg: pkg}
}

// write replaces the package's main.ts, which the bundle's main.js is.
func (s *bundleSources) write(t *testing.T, source string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(s.pkg, "main.ts"), []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}
}

func (s *bundleSources) sources() *stackdeploy.Sources {
	return &stackdeploy.Sources{
		OutputRoot:         filepath.Join(s.root, "dist"),
		RepositoryRoot:     s.root,
		ImplementationRoot: s.root,
		Run: func(ctx context.Context, dir string, log io.Writer, name string, args ...string) error {
			rel, _ := filepath.Rel(s.root, dir)
			s.ran = append(s.ran, filepath.ToSlash(rel)+": "+name+" "+args[0])
			if args[0] == "install" {
				return nil
			}
			cmd := exec.CommandContext(ctx, name, args...)
			cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
			if err := cmd.Run(); err != nil {
				return fmt.Errorf("%s %s: %w", name, strings.Join(args, " "), err)
			}
			return nil
		},
	}
}

func sum(content string) string {
	s := sha256.Sum256([]byte(content))
	return hex.EncodeToString(s[:])
}

// script returns the rendered script node of shop-storefront.
func (f *fixture) script(t *testing.T) *ir.Resource {
	t.Helper()
	res := f.ext.Provisioner.Rendered().Resources.Resource(stacktest.StorefrontServer + ".script")
	if res == nil {
		t.Fatal("the program has no script for shop-storefront")
	}
	return res
}

// module returns the rendered script's module named name.
func module(t *testing.T, script *ir.Resource, name string) map[string]any {
	t.Helper()
	for _, m := range script.Properties["modules"].([]any) {
		if m := m.(map[string]any); m["name"] == name {
			return m
		}
	}
	t.Fatalf("the script has no module %s: %v", name, script.Properties["modules"])
	return nil
}

// TestDeployBuildsABundle: a deploy runs bundle.ts after the frozen
// install, digests the files it wrote, publishes them before any step,
// pins the bundle's digest and each file's SHA-256 in the server's node,
// places the files under the program, where the fake provisioner reads
// them at each apply, and records the bundle. A second deploy of the same
// code publishes and fetches nothing; a change publishes the new files;
// --bundle runs the first files again, fetched with no build; and
// --no-build fetches the manifest's.
func TestDeployBuildsABundle(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	f.ready(t, env)
	const v1, v2 = "export default { fetch: () => new Response(\"v1\") };\n", "export default { fetch: () => new Response(\"v2\") };\n"
	src := newBundleSources(t, stacktest.BundleScript, v1)
	program := t.TempDir()
	deploy := func(bundles map[string]string, sources *stackdeploy.Sources) *stackdeploy.Manifest {
		t.Helper()
		o := f.options(t, env, nil)
		o.Dir = program
		m, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
			Options: o, Images: images(1), Bundles: bundles, Sources: sources, Planner: (&planner{to: 1}).plan, Now: fixedNow,
		})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, f.log)
		}
		return m
	}
	placed := func(digest, file string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, stacktest.StorefrontServer, strings.TrimPrefix(digest, "sha256:"), file))
		if err != nil {
			t.Fatalf("the files of the bundle are not under the program: %v", err)
		}
		return string(data)
	}

	from := len(f.ext.Provisioner.Calls())
	first := deploy(nil, src.sources())
	bundle := first.Bundles[stacktest.StorefrontServer]
	if bundle == nil || !strings.HasPrefix(bundle.Digest, "sha256:") {
		t.Fatalf("the manifest records the bundle as %+v", bundle)
	}
	if bundle.Files["main.js"] != sum(v1) || len(bundle.Files) != 2 {
		t.Errorf("the manifest records the bundle's files as %v, want main.js at %s and its map", bundle.Files, sum(v1))
	}
	if _, ok := first.Images[stacktest.StorefrontServer]; ok {
		t.Errorf("the manifest records an image for shop-storefront: %v", first.Images)
	}
	if got, want := strings.Join(src.ran, "; "), "dist: bun install; dist/server/shop-stack/shop-storefront: bun bundle.ts"; got != want {
		t.Errorf("the deploy ran %s, want %s", got, want)
	}
	calls := strings.Join(f.calls(from), "\n")
	publish := "publish bundle shop-storefront: " + bundle.Digest + ", upload 2 files"
	if !strings.Contains(calls, publish) || strings.Index(calls, publish) > strings.Index(calls, "apply infrastructure") {
		t.Fatalf("the deploy did not publish the bundle before its first step:\n%s", calls)
	}
	main := module(t, f.script(t), "main.js")
	hexDigest := strings.TrimPrefix(bundle.Digest, "sha256:")
	if main["contentFile"] != "artifacts/shop-storefront/"+hexDigest+"/main.js" || main["contentSha256"] != sum(v1) {
		t.Errorf("the rendered script reads main.js as %v", main)
	}
	if got := placed(bundle.Digest, "main.js"); got != v1 {
		t.Errorf("the placed main.js holds %q", got)
	}
	if got := strings.Join(f.ext.Bundles.Files(stacktest.StorefrontServer, bundle.Digest), ","); got != "main.js,main.js.map" {
		t.Errorf("the store keeps %s", got)
	}

	t.Run("the same code publishes nothing", func(t *testing.T) {
		from := len(f.ext.Provisioner.Calls())
		again := deploy(nil, src.sources())
		if again.Bundles[stacktest.StorefrontServer].Digest != bundle.Digest {
			t.Errorf("the same code has digest %s, not %s", again.Bundles[stacktest.StorefrontServer].Digest, bundle.Digest)
		}
		if calls := strings.Join(f.calls(from), "\n"); strings.Contains(calls, "bundle shop-storefront") {
			t.Errorf("a deploy of the same code published or fetched the bundle:\n%s", calls)
		}
	})

	src.write(t, v2)
	second := deploy(nil, src.sources())
	changed := second.Bundles[stacktest.StorefrontServer]
	if changed.Digest == bundle.Digest || changed.Files["main.js"] != sum(v2) {
		t.Fatalf("a change kept the bundle %+v", changed)
	}
	if got := placed(changed.Digest, "main.js"); got != v2 {
		t.Errorf("the placed main.js holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(program, ir.ArtifactsDir, stacktest.StorefrontServer, hexDigest)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the first bundle's files stayed under the program: %v", err)
	}

	t.Run("--bundle rolls back with no build", func(t *testing.T) {
		src.ran = nil
		from := len(f.ext.Provisioner.Calls())
		back := deploy(map[string]string{stacktest.StorefrontServer: bundle.Digest}, src.sources())
		if got := back.Bundles[stacktest.StorefrontServer]; got.Digest != bundle.Digest || got.Files["main.js"] != sum(v1) || len(src.ran) != 0 {
			t.Errorf("the rollback runs %+v after running %v, want %s with no build", got, src.ran, bundle.Digest)
		}
		calls := strings.Join(f.calls(from), "\n")
		if fetch := "fetch bundle shop-storefront: " + bundle.Digest; !strings.Contains(calls, fetch) || strings.Contains(calls, "publish bundle") {
			t.Errorf("the rollback did not fetch the first bundle, or published one:\n%s", calls)
		}
		if got := placed(bundle.Digest, "main.js"); got != v1 {
			t.Errorf("the placed main.js holds %q", got)
		}
		if main := module(t, f.script(t), "main.js"); main["contentSha256"] != sum(v1) {
			t.Errorf("the rendered script reads main.js as %v", main)
		}
	})

	t.Run("--no-build fetches the manifest's bundle", func(t *testing.T) {
		from := len(f.ext.Provisioner.Calls())
		kept := deploy(nil, nil)
		if kept.Bundles[stacktest.StorefrontServer].Digest != bundle.Digest {
			t.Errorf("--no-build runs %s, want the manifest's %s", kept.Bundles[stacktest.StorefrontServer].Digest, bundle.Digest)
		}
		if calls := strings.Join(f.calls(from), "\n"); !strings.Contains(calls, "fetch bundle shop-storefront: "+bundle.Digest) {
			t.Errorf("--no-build did not fetch the manifest's bundle:\n%s", calls)
		}
	})

	t.Run("outputs and destroy render the deployed bundle", func(t *testing.T) {
		from := len(f.ext.Provisioner.Calls())
		o := f.options(t, env, nil)
		if _, err := stackdeploy.Outputs(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		if main := module(t, f.script(t), "main.js"); main["contentSha256"] != sum(v1) {
			t.Errorf("outputs rendered the script reading main.js as %v", main)
		}
		if calls := strings.Join(f.calls(from), "\n"); strings.Contains(calls, "fetch bundle") {
			t.Errorf("outputs fetched the bundle, which it does not place:\n%s", calls)
		}
		if err := stackdeploy.Destroy(context.Background(), o); err != nil {
			t.Fatal(err)
		}
		calls := strings.Join(f.calls(from), "\n")
		fetch, destroy := strings.Index(calls, "fetch bundle shop-storefront: "+bundle.Digest), strings.Index(calls, "destroy Staging")
		if fetch < 0 || destroy < fetch {
			t.Errorf("destroy did not place the bundle before it ran:\n%s", calls)
		}
	})
}

// TestPlanFetchesABundle: a plan builds nothing. It fetches the bundle the
// manifest records, or the one --bundle names, and places it under the
// program; before any deploy it lists the server as having no bundle yet.
func TestPlanFetchesABundle(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	f.ready(t, env)
	plan := func(bundles map[string]string) (*stackdeploy.PlanResult, string) {
		t.Helper()
		o := f.options(t, env, nil)
		result, err := stackdeploy.Plan(context.Background(), stackdeploy.PlanOptions{
			Options: o, Images: images(1), Bundles: bundles, Planner: (&planner{to: 1}).plan,
		})
		if err != nil {
			t.Fatalf("plan: %v\n%s", err, f.log)
		}
		return result, o.Dir
	}

	from := len(f.ext.Provisioner.Calls())
	if result, _ := plan(nil); strings.Join(result.Unbundled, ",") != stacktest.StorefrontServer {
		t.Errorf("a plan before any deploy lists %v with no bundle", result.Unbundled)
	}
	if calls := strings.Join(f.calls(from), "\n"); strings.Contains(calls, "bundle") {
		t.Errorf("a plan with no bundle fetched or published one:\n%s", calls)
	}

	const source = "export default {};\n"
	src := newBundleSources(t, stacktest.BundleScript, source)
	m, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: f.options(t, env, nil), Images: images(1), Sources: src.sources(), Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, f.log)
	}
	deployed := m.Bundles[stacktest.StorefrontServer].Digest

	src.ran = nil
	from = len(f.ext.Provisioner.Calls())
	result, dir := plan(nil)
	if len(result.Unbundled) != 0 || len(src.ran) != 0 {
		t.Errorf("the plan lists %v with no bundle after running %v", result.Unbundled, src.ran)
	}
	calls := strings.Join(f.calls(from), "\n")
	if !strings.Contains(calls, "fetch bundle shop-storefront: "+deployed) || strings.Contains(calls, "publish bundle") {
		t.Errorf("the plan did not fetch the manifest's bundle, or published one:\n%s", calls)
	}
	data, err := os.ReadFile(filepath.Join(dir, ir.ArtifactsDir, stacktest.StorefrontServer, strings.TrimPrefix(deployed, "sha256:"), "main.js"))
	if err != nil || string(data) != source {
		t.Errorf("the plan placed main.js as %q, %v", data, err)
	}

	if _, err := stackdeploy.Plan(context.Background(), stackdeploy.PlanOptions{
		Options: f.options(t, env, nil), Images: images(1), Bundles: map[string]string{stacktest.StorefrontServer: digest(7)}, Planner: (&planner{to: 1}).plan,
	}); err == nil || !strings.Contains(err.Error(), "no files under "+digest(7)) {
		t.Errorf("a plan of a bundle the store does not keep = %v", err)
	}
}

// TestABundleThatFailsStopsTheDeploy: when bundle.ts fails, the deploy
// stops before any step and writes no manifest.
func TestABundleThatFailsStopsTheDeploy(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	f.ready(t, env)
	src := newBundleSources(t, stacktest.FailingBundleScript, "")
	from := len(f.ext.Provisioner.Calls())
	_, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: f.options(t, env, nil), Images: images(1), Sources: src.sources(), Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err == nil || !strings.Contains(err.Error(), "bundle server shop-storefront") {
		t.Fatalf("a deploy whose bundle.ts fails = %v", err)
	}
	if !strings.Contains(f.log.String(), "main.ts does not build") {
		t.Errorf("the log does not hold bundle.ts's output:\n%s", f.log)
	}
	if calls := f.calls(from); len(calls) != 0 {
		t.Errorf("the deploy ran %v", calls)
	}
	if _, err := f.ext.State.ReadManifest(context.Background(), registry.Run{Environment: env}); !errors.Is(err, registry.ErrNoManifest) {
		t.Errorf("the deploy wrote a manifest: %v", err)
	}
}

// TestABundleNeedsFiles: a deploy with no build refuses a server the
// manifest records no bundle for, and --bundle refuses a name that runs
// no bundle, and a digest the store does not keep.
func TestABundleNeedsFiles(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	f.ready(t, env)
	for _, tc := range []struct {
		name    string
		bundles map[string]string
		want    string
	}{
		{"none", nil, "no bundle for server shop-storefront: the manifest records none"},
		{"an image's server", map[string]string{"shop-api": digest(1)}, "--bundle names shop-api, which is no server, job or worker of environment Staging that runs a bundle"},
		{"files the store does not keep", map[string]string{stacktest.StorefrontServer: digest(2)}, "no files under " + digest(2)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			from := len(f.ext.Provisioner.Calls())
			_, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
				Options: f.options(t, env, nil), Images: images(1), Bundles: tc.bundles, Planner: (&planner{to: 1}).plan, Now: fixedNow,
			})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("deploy = %v, want an error containing %q", err, tc.want)
			}
			if calls := strings.Join(f.calls(from), "\n"); strings.Contains(calls, "apply") {
				t.Errorf("the refused deploy applied a step:\n%s", calls)
			}
		})
	}
	t.Run("an image for the bundle's server", func(t *testing.T) {
		imgs := images(1)
		imgs[stacktest.StorefrontServer] = image(stacktest.StorefrontServer, 1)
		_, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
			Options: f.options(t, env, nil), Images: imgs, Planner: (&planner{to: 1}).plan, Now: fixedNow,
		})
		if err == nil || !strings.Contains(err.Error(), "--image names shop-storefront, which is no server or job of environment Staging that runs an image") {
			t.Errorf("deploy = %v", err)
		}
	})
}

// TestBuildPublishesABundle: stack build builds the bundle and publishes
// its files, which run nowhere yet, and prints them as a --bundle flag; a
// second build of the same code publishes nothing, unless forced.
func TestBuildPublishesABundle(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	src := newBundleSources(t, stacktest.BundleScript, "export default {};\n")
	build := func(force bool) *stackdeploy.BuildResult {
		t.Helper()
		result, err := stackdeploy.Build(context.Background(), stackdeploy.BuildOptions{
			Options: f.options(t, env, nil), Sources: *src.sources(), Deployables: []string{stacktest.StorefrontServer}, Force: force,
		})
		if err != nil {
			t.Fatalf("build: %v\n%s", err, f.log)
		}
		return result
	}
	result := build(false)
	flags := result.BundleFlags()
	if len(flags) != 1 || !strings.HasPrefix(flags[0], "shop-storefront=sha256:") || len(result.Images) != 0 || strings.Join(result.Built, ",") != stacktest.StorefrontServer {
		t.Fatalf("build made images %v, bundles %v and built %v", result.Images, flags, result.Built)
	}
	digest := result.Bundles[stacktest.StorefrontServer]
	if calls := strings.Join(f.ext.Provisioner.Calls(), "\n"); !strings.Contains(calls, "publish bundle shop-storefront: "+digest+", upload 2 files") {
		t.Errorf("build did not publish the bundle:\n%s", calls)
	}
	from := len(f.ext.Provisioner.Calls())
	if again := build(true); again.Bundles[stacktest.StorefrontServer] != digest {
		t.Errorf("a forced build of the same code has digest %s", again.Bundles[stacktest.StorefrontServer])
	}
	if calls := strings.Join(f.calls(from), "\n"); !strings.Contains(calls, "publish bundle shop-storefront: "+digest+", there already") {
		t.Errorf("a forced build did not publish the bundle again:\n%s", calls)
	}
}

// TestPinBundles: the digest and each file's SHA-256 replace their tokens
// in the deployable's own nodes; a file the bundle does not hold, and a
// deployable whose nodes hold no digest token, are refused.
func TestPinBundles(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	files := map[string]string{"main.js": sum("a"), "main.js.map": sum("b")}
	pinned, err := stackdeploy.PinBundles(env, map[string]stackdeploy.Bundle{stacktest.StorefrontServer: {Digest: digest(3), Files: files}})
	if err != nil {
		t.Fatal(err)
	}
	script := pinned.Resources.Resource(stacktest.StorefrontServer + ".script")
	if m := module(t, script, "main.js.map"); m["contentFile"] != "artifacts/shop-storefront/"+strings.TrimPrefix(digest(3), "sha256:")+"/main.js.map" || m["contentSha256"] != sum("b") {
		t.Errorf("the pinned script reads main.js.map as %v", m)
	}
	if m := module(t, env.Resources.Resource(stacktest.StorefrontServer+".script"), "main.js"); m["contentSha256"] != ir.BundleFileToken("main.js") {
		t.Errorf("PinBundles changed the environment it was given: %v", m)
	}
	if _, err := stackdeploy.PinBundles(env, map[string]stackdeploy.Bundle{stacktest.StorefrontServer: {Digest: digest(3), Files: map[string]string{"main.js": sum("a")}}}); err == nil ||
		!strings.Contains(err.Error(), "its nodes name file main.js.map of its bundle") {
		t.Errorf("a bundle without a file its nodes name = %v", err)
	}
	if _, err := stackdeploy.PinBundles(env, map[string]stackdeploy.Bundle{"shop-api": {Digest: digest(3), Files: files}}); err == nil ||
		!strings.Contains(err.Error(), "no property of its nodes holds {bundle-digest}") {
		t.Errorf("a bundle for a server that runs an image = %v", err)
	}
}

// TestParseBundles reads --bundle values.
func TestParseBundles(t *testing.T) {
	got, err := stackdeploy.ParseBundles([]string{"shop-storefront=" + digest(3)})
	if err != nil || got["shop-storefront"] != digest(3) {
		t.Fatalf("ParseBundles = %v, %v", got, err)
	}
	for _, bad := range []string{"shop-storefront", "shop-storefront=abc", "=" + digest(3)} {
		if _, err := stackdeploy.ParseBundles([]string{bad}); err == nil {
			t.Errorf("ParseBundles accepted %q", bad)
		}
	}
	if _, err := stackdeploy.ParseBundles([]string{"a=" + digest(1), "a=" + digest(2)}); err == nil {
		t.Error("ParseBundles accepted a deployable twice")
	}
}

// TestDestroyReadiesTheRun: stack destroy has the target's DestroyPreparer
// ready the run before the provisioner destroys it, and its failure stops
// the destroy (D64).
func TestDestroyReadiesTheRun(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	if _, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: f.options(t, env, nil), Images: images(1), Planner: (&planner{to: 1}).plan, Now: fixedNow,
	}); err != nil {
		t.Fatalf("deploy: %v\n%s", err, f.log)
	}

	f.ext.PreDestroy.Fail = errors.New("the bucket is not empty")
	from := len(f.ext.Provisioner.Calls())
	if err := stackdeploy.Destroy(context.Background(), f.options(t, env, nil)); err == nil || !strings.Contains(err.Error(), "ready Staging for its destroy: the bucket is not empty") {
		t.Fatalf("destroy = %v", err)
	}
	for _, call := range f.calls(from) {
		if call == "destroy Staging" {
			t.Errorf("a failed preparation let the destroy run: %v", f.calls(from))
		}
	}
	if _, err := f.ext.State.ReadManifest(context.Background(), registry.Run{Environment: env}); err != nil {
		t.Errorf("a failed preparation removed the manifest: %v", err)
	}

	f.ext.PreDestroy.Fail = nil
	from = len(f.ext.Provisioner.Calls())
	if err := stackdeploy.Destroy(context.Background(), f.options(t, env, nil)); err != nil {
		t.Fatal(err)
	}
	calls := f.calls(from)
	if got := strings.Join(calls[len(calls)-2:], "; "); got != "prepare destroy Staging; destroy Staging" {
		t.Errorf("destroy ran %s, want the preparation then the destroy", got)
	}
}
