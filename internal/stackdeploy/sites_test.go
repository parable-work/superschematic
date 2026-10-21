package stackdeploy_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// siteEnv resolves an environment of the shop stack with its site,
// shop-web (D55), without shop-orders' worker, whose image the site tests
// do not build.
func (f *fixture) siteEnv(t *testing.T, name string) *ir.ResolvedEnvironment {
	t.Helper()
	env, err := stack.Resolve(f.reg, stack.Input{Stack: stacktest.WithoutWorkerSettings(stacktest.WithSite(stacktest.Shop())), Services: stacktest.WithoutWorkers(stacktest.SiteShop()), Environment: name})
	if err != nil {
		t.Fatal(err)
	}
	if env.DNS != nil && env.DNS.Platform == stacktest.DNSPlatform {
		env.DNS.Credentials = []*ir.DNSCredential{{Secret: dnsToken.Secret, Env: dnsToken.Env, Description: dnsToken.Description}}
	}
	return env
}

// siteSources is a repository whose site shop-web builds with a fake
// `bun run build`, which writes page into its dist, and records each
// command it runs.
type siteSources struct {
	root string
	page string
	ran  []string
}

func newSiteSources(t *testing.T) *siteSources {
	t.Helper()
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "web", "shop-web"), 0o755); err != nil {
		t.Fatal(err)
	}
	return &siteSources{root: root, page: "<main>v1</main>"}
}

func (s *siteSources) sources() *stackdeploy.Sources {
	return &stackdeploy.Sources{
		OutputRoot:         filepath.Join(s.root, "schemas", "dist"),
		RepositoryRoot:     s.root,
		ImplementationRoot: s.root,
		Run: func(_ context.Context, dir string, _ io.Writer, name string, args ...string) error {
			rel, _ := filepath.Rel(s.root, dir)
			s.ran = append(s.ran, filepath.ToSlash(rel)+": "+name+" "+strings.Join(args, " "))
			if strings.Join(args, " ") != "run build" {
				return nil
			}
			dist := filepath.Join(dir, "dist")
			if err := os.MkdirAll(filepath.Join(dist, "assets"), 0o755); err != nil {
				return err
			}
			if err := os.WriteFile(filepath.Join(dist, "index.html"), []byte(s.page), 0o644); err != nil {
				return err
			}
			return os.WriteFile(filepath.Join(dist, "assets", "app.js"), []byte("console.log(1)"), 0o644)
		},
	}
}

// TestDeployInstallsAPartialWorkspaceWithoutSaving: an output root built
// for one stack leaves out the implementations and sites whose packages it
// lacks (D51, amended), whose lockfile, pinning the whole workspace, a
// frozen install refuses; the deploy installs it with --no-save, which
// keeps the lockfile as it is, and says so.
func TestDeployInstallsAPartialWorkspaceWithoutSaving(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Staging")
	f.ready(t, env)
	src := newSiteSources(t)
	sources := src.sources()
	if err := os.MkdirAll(sources.OutputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	manifest := `{"name": "@acme/workspace", "private": true, "workspaces": ["api/*", "../../typescript/*", "../../web/*", "!../../typescript/shop-wishlist"]}` + "\n"
	if err := os.WriteFile(filepath.Join(sources.OutputRoot, "package.json"), []byte(manifest), 0o644); err != nil {
		t.Fatal(err)
	}
	o := f.options(t, env, nil)
	o.Dir = t.TempDir()
	if _, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: o, Images: images(1), Sources: sources, Planner: (&planner{to: 1}).plan, Now: fixedNow,
	}); err != nil {
		t.Fatalf("deploy: %v\n%s", err, f.log)
	}
	if got := strings.Join(src.ran, "; "); got != "schemas/dist: bun install --no-save; web/shop-web: bun run build" {
		t.Errorf("the deploy ran %s", got)
	}
	if want := "the TypeScript workspace leaves out ../../typescript/shop-wishlist"; !strings.Contains(f.log.String(), want) {
		t.Errorf("the deploy's log lacks %q:\n%s", want, f.log)
	}
}

// TestDeployPublishesASite: a deploy builds the site after a frozen
// install of the workspace, publishes its files and its config, shop-api's
// public address, before the wave that rolls it out, pins the site's node
// to the files' digest, and records the digest. The fake site publisher
// fetches files, so the deploy places them beside the program too (D64). A
// second deploy of the same files uploads nothing; a change uploads the
// new files; --site serves the first files again with no build, fetched
// to be placed.
func TestDeployPublishesASite(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Staging")
	f.ready(t, env)
	src := newSiteSources(t)
	program := t.TempDir()
	deploy := func(sites map[string]string, sources *stackdeploy.Sources) *stackdeploy.Manifest {
		t.Helper()
		o := f.options(t, env, nil)
		o.Dir = program
		m, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
			Options: o, Images: images(1), Sites: sites, Sources: sources, Planner: (&planner{to: 1}).plan, Now: fixedNow,
		})
		if err != nil {
			t.Fatalf("deploy: %v\n%s", err, f.log)
		}
		return m
	}

	from := len(f.ext.Provisioner.Calls())
	first := deploy(nil, src.sources())
	if got := strings.Join(src.ran, "; "); got != "schemas/dist: bun install --frozen-lockfile; web/shop-web: bun run build" {
		t.Errorf("the deploy ran %s", got)
	}
	digest := first.Sites["shop-web"]
	if !strings.HasPrefix(digest, "sha256:") {
		t.Fatalf("the manifest records the site's files as %q", digest)
	}
	calls := strings.Join(f.calls(from), "\n")
	publish := `publish site shop-web: ` + digest + `, upload 2 files, config {"apis":{"shop-api":{"url":"https://shop-api.staging.acme.dev"}}}`
	if !strings.Contains(calls, publish) {
		t.Fatalf("the deploy did not publish the site:\n%s", calls)
	}
	if strings.Index(calls, publish) > strings.Index(calls, "apply rollout 2") || strings.Index(calls, "apply rollout 1") > strings.Index(calls, publish) {
		t.Errorf("the site was not published between shop-api's wave and its own:\n%s", calls)
	}
	if got := strings.Join(f.ext.Sites.Files("shop-web", digest), ","); got != "assets/app.js,index.html" {
		t.Errorf("published files = %s", got)
	}
	site := f.ext.Provisioner.Rendered().Resources.Resource("shop-web.site")
	if want := "/" + strings.TrimPrefix(digest, "sha256:") + "/"; site.Properties["prefix"] != want {
		t.Errorf("the rendered site serves from %v, want %s", site.Properties["prefix"], want)
	}
	placed := func(digest string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, "shop-web", strings.TrimPrefix(digest, "sha256:"), "index.html"))
		if err != nil {
			t.Fatalf("the site's files are not beside the program: %v", err)
		}
		return string(data)
	}
	if got := placed(digest); got != "<main>v1</main>" {
		t.Errorf("the placed index.html holds %q", got)
	}

	from = len(f.ext.Provisioner.Calls())
	if again := deploy(nil, src.sources()); again.Sites["shop-web"] != digest {
		t.Errorf("the same files have digest %s, not %s", again.Sites["shop-web"], digest)
	}
	if calls := strings.Join(f.calls(from), "\n"); strings.Contains(calls, "upload") || !strings.Contains(calls, "publish site shop-web: "+digest+", config") {
		t.Errorf("a deploy of the same files uploaded them, or wrote no config:\n%s", calls)
	}

	src.page = "<main>v2</main>"
	second := deploy(nil, src.sources())
	if second.Sites["shop-web"] == digest {
		t.Fatal("new files kept the old digest")
	}

	src.ran = nil
	from = len(f.ext.Provisioner.Calls())
	back := deploy(map[string]string{"shop-web": digest}, src.sources())
	if back.Sites["shop-web"] != digest || len(src.ran) != 0 {
		t.Errorf("the rollback serves %s after running %v, want %s with no site build", back.Sites["shop-web"], src.ran, digest)
	}
	if calls := strings.Join(f.calls(from), "\n"); strings.Contains(calls, "upload") || !strings.Contains(calls, "fetch site shop-web: "+digest) {
		t.Errorf("the rollback uploaded files, or fetched none:\n%s", calls)
	}
	if got := placed(digest); got != "<main>v1</main>" {
		t.Errorf("the rollback placed index.html holding %q", got)
	}

	// Outputs renders the program the last deploy applied: the site
	// serves the files the manifest records.
	if _, err := stackdeploy.Outputs(context.Background(), f.options(t, env, nil)); err != nil {
		t.Fatal(err)
	}
	site = f.ext.Provisioner.Rendered().Resources.Resource("shop-web.site")
	if want := "/" + strings.TrimPrefix(digest, "sha256:") + "/"; site.Properties["prefix"] != want {
		t.Errorf("outputs rendered the site serving %v, want %s", site.Properties["prefix"], want)
	}
}

// TestASiteNeedsFiles: a deploy with no build refuses a site the manifest
// records no files for, and --site refuses a name that is no site, or
// files the target does not hold.
func TestASiteNeedsFiles(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Staging")
	f.ready(t, env)
	_, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: f.options(t, env, nil), Images: images(1), Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err == nil || !strings.Contains(err.Error(), "no files for site shop-web") {
		t.Errorf("a deploy with no files for the site = %v", err)
	}
	_, err = stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: f.options(t, env, nil), Images: images(1), Sites: map[string]string{"shop-api": digest(1)}, Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err == nil || !strings.Contains(err.Error(), "--site names shop-api, which is no site") {
		t.Errorf("--site naming a server = %v", err)
	}
	_, err = stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: f.options(t, env, nil), Images: images(1), Sites: map[string]string{"shop-web": digest(2)}, Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err == nil || !strings.Contains(err.Error(), "no files under "+digest(2)) {
		t.Errorf("--site naming files the target does not hold = %v", err)
	}
}

// TestBuildUploadsASite: stack build builds the site and uploads its
// files, which serve nothing yet, and prints them as a --site flag.
func TestBuildUploadsASite(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Staging")
	src := newSiteSources(t)
	result, err := stackdeploy.Build(context.Background(), stackdeploy.BuildOptions{Options: f.options(t, env, nil), Sources: *src.sources(), Deployables: []string{"shop-web"}})
	if err != nil {
		t.Fatalf("build: %v\n%s", err, f.log)
	}
	flags := result.SiteFlags()
	if len(flags) != 1 || !strings.HasPrefix(flags[0], "shop-web=sha256:") || len(result.Images) != 0 {
		t.Fatalf("build made images %v and sites %v", result.Images, flags)
	}
	digest := result.Sites["shop-web"]
	if calls := strings.Join(f.ext.Provisioner.Calls(), "\n"); !strings.Contains(calls, "publish site shop-web: "+digest+", upload 2 files") || strings.Contains(calls, "config") {
		t.Errorf("build did not upload the site's files alone:\n%s", calls)
	}
}

// TestBuildBuildsASiteInItsMirror (D65, amended): from an output root
// other than the default one, whose Bun workspace lists the mirrors under
// impl/, a site builds in its mirror there, which the site's build into
// that root wrote, after the root's install, and its package is left as it
// is; without the mirror, the build stops.
func TestBuildBuildsASiteInItsMirror(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Staging")
	src := newSiteSources(t)
	sources := src.sources()
	sources.OutputRoot = filepath.Join(src.root, "out")
	if err := os.MkdirAll(sources.OutputRoot, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(sources.OutputRoot, "package.json"), []byte(`{"name": "w", "workspaces": ["api/*", "impl/*"]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	build := func() error {
		_, err := stackdeploy.Build(context.Background(), stackdeploy.BuildOptions{Options: f.options(t, env, nil), Sources: *sources, Deployables: []string{"shop-web"}})
		return err
	}
	if err := build(); err == nil || !strings.Contains(err.Error(), "holds no mirror of shop-web") {
		t.Fatalf("a build without the site's mirror = %v", err)
	}
	mirror := filepath.Join(sources.OutputRoot, "impl", "shop-web")
	if err := os.MkdirAll(mirror, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(mirror, "package.json"), []byte("{}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	src.ran = nil
	if err := build(); err != nil {
		t.Fatalf("build: %v\n%s", err, f.log)
	}
	if got, want := strings.Join(src.ran, "\n"), "out: bun install --frozen-lockfile\nout/impl/shop-web: bun run build"; got != want {
		t.Errorf("the build ran\n%s\nwant\n%s", got, want)
	}
	if _, err := os.Stat(filepath.Join(src.root, "web", "shop-web", "dist")); !os.IsNotExist(err) {
		t.Errorf("the build wrote into the site's package: %v", err)
	}
}

// TestDigestSite: the digest is the files', whatever their times, and a
// site's output holds files and no link.
func TestDigestSite(t *testing.T) {
	write := func(dir, name, content string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	a, b := t.TempDir(), t.TempDir()
	for _, dir := range []string{a, b} {
		write(dir, "index.html", "<main></main>")
		write(dir, "assets/app.js", "1")
	}
	da, err := stackdeploy.DigestSite(a)
	if err != nil {
		t.Fatal(err)
	}
	db, err := stackdeploy.DigestSite(b)
	if err != nil {
		t.Fatal(err)
	}
	if da.Digest != db.Digest || da.Files != 2 {
		t.Fatalf("digests %s and %s of the same %d files", da.Digest, db.Digest, da.Files)
	}
	write(b, "assets/app.js", "2")
	if dc, _ := stackdeploy.DigestSite(b); dc.Digest == da.Digest {
		t.Error("a changed file kept the digest")
	}
	if err := os.Symlink(filepath.Join(a, "index.html"), filepath.Join(b, "link.html")); err != nil {
		t.Fatal(err)
	}
	if _, err := stackdeploy.DigestSite(b); err == nil || !strings.Contains(err.Error(), "link.html is a symbolic link") {
		t.Errorf("a link = %v", err)
	}
	if _, err := stackdeploy.DigestSite(t.TempDir()); err == nil || !strings.Contains(err.Error(), "wrote no file") {
		t.Errorf("an empty output = %v", err)
	}
}

// TestParseSites reads --site values.
func TestParseSites(t *testing.T) {
	got, err := stackdeploy.ParseSites([]string{"shop-web=" + digest(3)})
	if err != nil || got["shop-web"] != digest(3) {
		t.Fatalf("ParseSites = %v, %v", got, err)
	}
	for _, bad := range []string{"shop-web", "shop-web=abc", "=" + digest(3)} {
		if _, err := stackdeploy.ParseSites([]string{bad}); err == nil {
			t.Errorf("ParseSites accepted %q", bad)
		}
	}
	if _, err := stackdeploy.ParseSites([]string{"a=" + digest(1), "a=" + digest(2)}); err == nil {
		t.Error("ParseSites accepted a site twice")
	}
}

// TestSiteConfigOfAPreviewMember: a member's config resolves the
// parameter in the API's public address.
func TestSiteConfigOfAPreviewMember(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Preview")
	got, err := stackdeploy.SiteConfigOf(env, "shop-web", map[string]string{"pr": "42"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"apis":{"shop-api":{"url":"https://shop-api-42.staging.acme.dev"}}}` + "\n"; string(got) != want {
		t.Errorf("config = %s, want %s", got, want)
	}
}
