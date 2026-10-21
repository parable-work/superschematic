package stackdeploy_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// noProjectCode is a RunCommand that fails the test: a stack command given
// a prepared directory runs no install and no build (D47, amended).
func noProjectCode(t *testing.T) stackdeploy.RunCommand {
	return func(_ context.Context, dir string, _ io.Writer, name string, args ...string) error {
		t.Errorf("ran %s %s in %s, with a prepared directory", name, strings.Join(args, " "), dir)
		return nil
	}
}

// TestPrepareThenDeployASite: stack prepare builds the site, with the
// workspace's install, into an archive of the files' tar stream; a deploy
// given the directory runs neither, serves the same files, under the same
// digest a deploy that builds them records, and places them beside the
// program.
func TestPrepareThenDeployASite(t *testing.T) {
	f := newFixture(t)
	env := f.siteEnv(t, "Staging")
	f.ready(t, env)
	src := newSiteSources(t)
	dir := filepath.Join(t.TempDir(), "prepared")
	p, err := stackdeploy.Prepare(context.Background(), stackdeploy.PrepareOptions{
		Stack: env.Stack, Environments: []*ir.ResolvedEnvironment{env}, Sources: src.sources(), Dir: dir, Log: io.Discard,
	})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(src.ran, "; "); got != "schemas/dist: bun install --frozen-lockfile; web/shop-web: bun run build" {
		t.Errorf("prepare ran %s", got)
	}
	site := p.Sites["shop-web"]
	if site == nil || site.Archive != "sites/shop-web.tar" || site.Files != 2 {
		t.Fatalf("prepare kept the site as %+v", site)
	}

	read, err := stackdeploy.ReadPrepared(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := read.CheckFor(env.Stack, env.Environment, true); err != nil {
		t.Fatal(err)
	}
	program := t.TempDir()
	o := f.options(t, env, nil)
	o.Dir = program
	m, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: o, Images: images(1), Sources: &stackdeploy.Sources{Prepared: read, Run: noProjectCode(t)}, Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, f.log)
	}
	if m.Sites["shop-web"] != site.Digest {
		t.Errorf("the deploy records the site's files as %s, want %s", m.Sites["shop-web"], site.Digest)
	}
	if got := strings.Join(f.ext.Sites.Files("shop-web", site.Digest), ","); got != "assets/app.js,index.html" {
		t.Errorf("published files = %s", got)
	}
	data, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, "shop-web", strings.TrimPrefix(site.Digest, "sha256:"), "index.html"))
	if err != nil || string(data) != "<main>v1</main>" {
		t.Errorf("the placed index.html holds %q: %v", data, err)
	}

	// A deploy that builds the same files records the same digest.
	src.ran = nil
	built, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: o, Images: images(1), Sources: src.sources(), Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err != nil {
		t.Fatal(err)
	}
	if built.Sites["shop-web"] != site.Digest {
		t.Errorf("a deploy that builds the site records %s, prepare %s", built.Sites["shop-web"], site.Digest)
	}

	// A prepared archive that changed is refused.
	if err := os.WriteFile(filepath.Join(dir, "sites", "shop-web.tar"), []byte("not the files"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err = stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: o, Images: images(1), Sources: &stackdeploy.Sources{Prepared: read, Run: noProjectCode(t)}, Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err == nil || !strings.Contains(err.Error(), "has digest") {
		t.Errorf("a deploy of a changed archive = %v", err)
	}
}

// TestPrepareThenDeployImages: stack prepare writes each server's and
// job's build context; a deploy given the directory builds the images from
// them, and again only the ones whose context changed.
func TestPrepareThenDeployImages(t *testing.T) {
	f := newFixture(t)
	env := f.env(t, "Staging")
	f.ready(t, env)
	src := sources(t, "shop-api", "Orders", stacktest.ShipOrdersJob)
	prepare := func() *stackdeploy.Prepared {
		t.Helper()
		dir := filepath.Join(src.RepositoryRoot, "..", "prepared")
		if _, err := stackdeploy.Prepare(context.Background(), stackdeploy.PrepareOptions{
			Stack: env.Stack, Environments: []*ir.ResolvedEnvironment{env}, Sources: src, Dir: dir,
		}); err != nil {
			t.Fatal(err)
		}
		p, err := stackdeploy.ReadPrepared(dir)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	p := prepare()
	if got := slices.Sorted(func(yield func(string) bool) {
		for name := range p.Contexts {
			if !yield(name) {
				return
			}
		}
	}); !slices.Equal(got, []string{"Orders", "shop-api", stacktest.ShipOrdersJob}) {
		t.Fatalf("prepare wrote the contexts of %v", got)
	}
	o := deployOptions(f, t, env, nil, &planner{to: 1}, nil)
	o.Sources = &stackdeploy.Sources{Prepared: p, Run: noProjectCode(t)}
	m, err := stackdeploy.Deploy(context.Background(), o)
	if err != nil {
		t.Fatal(err)
	}
	if b := builds(f.calls(0)); len(b) != 3 {
		t.Fatalf("the deploy built %v", b)
	}
	if got := f.ext.Builder.Context("shop-api"); !slices.Contains(got, "go/shop-api/implementation.go") {
		t.Errorf("shop-api's context holds %q", got)
	}
	if m.Contexts["shop-api"] != p.Contexts["shop-api"].Digest {
		t.Errorf("the manifest records context %s, prepare wrote %s", m.Contexts["shop-api"], p.Contexts["shop-api"].Digest)
	}

	touch(t, src, "shop-api", "package impl // changed\n")
	n := len(f.ext.Provisioner.Calls())
	o.Sources = &stackdeploy.Sources{Prepared: prepare(), Run: noProjectCode(t)}
	if _, err := stackdeploy.Deploy(context.Background(), o); err != nil {
		t.Fatal(err)
	}
	if b := builds(f.calls(n)); len(b) != 1 || !strings.HasPrefix(b[0], "build shop-api:") {
		t.Errorf("built %v, want shop-api alone", b)
	}
}

// TestPreparedDocuments: a prepared document replays what its module
// exported while its sources are as they were, and is refused once one
// changes, or when the directory holds none.
func TestPreparedDocuments(t *testing.T) {
	root := t.TempDir()
	service := filepath.Join(root, "schemas", "services", "shop-stack")
	module := filepath.Join(service, "deploy.values.ts")
	shared := filepath.Join(root, "schemas", "shared", "ports.ts")
	for path, content := range map[string]string{module: "export default {}\n", shared: "export const port = 1\n"} {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	key, err := stackdeploy.DocumentKey(root, service, "deploy.values.ts")
	if err != nil || key != "schemas/services/shop-stack/deploy.values.ts" {
		t.Fatalf("DocumentKey = %q, %v", key, err)
	}
	doc, err := stackdeploy.RecordDocument(root, json.RawMessage(`{"port":1}`), []string{module, shared, "/elsewhere/lib.ts"})
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Sources) != 2 || !slices.Contains(doc.Imports, "/elsewhere/lib.ts") {
		t.Fatalf("recorded %+v", doc)
	}
	dir := t.TempDir()
	if _, err := stackdeploy.Prepare(context.Background(), stackdeploy.PrepareOptions{Stack: "shop-stack", Documents: map[string]*stackdeploy.PreparedDocument{key: doc}, Dir: dir}); err != nil {
		t.Fatal(err)
	}
	p, err := stackdeploy.ReadPrepared(dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := p.CheckFor("shop-stack", "Staging", true); err == nil || !strings.Contains(err.Error(), "documents alone") {
		t.Errorf("a documents-only directory for a build = %v", err)
	}
	if err := p.CheckFor("other-stack", "Staging", false); err == nil {
		t.Error("a directory of another stack was taken")
	}
	got, imports, err := p.Document(root, service, "deploy.values.ts")
	var compact bytes.Buffer
	if err == nil {
		err = json.Compact(&compact, got)
	}
	if err != nil || compact.String() != `{"port":1}` || !slices.Contains(imports, shared) {
		t.Fatalf("Document = %s, %v, %v", got, imports, err)
	}
	if _, _, err := p.Document(root, service, "other.ts"); err == nil || !strings.Contains(err.Error(), "holds no document") {
		t.Errorf("a document the directory lacks = %v", err)
	}
	if err := os.WriteFile(shared, []byte("export const port = 2\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := p.Document(root, service, "deploy.values.ts"); err == nil || !strings.Contains(err.Error(), "schemas/shared/ports.ts changed") {
		t.Errorf("a document whose import changed = %v", err)
	}
}

// TestPrepareRefusesADirectoryOfOtherFiles: prepare replaces an earlier
// prepare's directory, and refuses to empty one that holds other files.
func TestPrepareRefusesADirectoryOfOtherFiles(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "notes.txt"), []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := stackdeploy.Prepare(context.Background(), stackdeploy.PrepareOptions{Stack: "shop-stack", Dir: dir}); err == nil || !strings.Contains(err.Error(), "no directory stack prepare wrote") {
		t.Errorf("prepare into a directory of other files = %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "notes.txt")); err != nil {
		t.Errorf("prepare removed a file it did not write: %v", err)
	}
}

// TestPrepareThenDeployABundle: stack prepare bundles the server with its
// bundle.ts, and a deploy given the directory publishes, pins and places
// the bundle it holds, building nothing.
func TestPrepareThenDeployABundle(t *testing.T) {
	f := newFixture(t)
	env := f.bundleEnv(t, "Staging")
	f.ready(t, env)
	const v1 = "export default { fetch: () => new Response(\"v1\") };\n"
	src := newBundleSources(t, stacktest.BundleScript, v1)
	dir := filepath.Join(t.TempDir(), "prepared")
	if _, err := stackdeploy.Prepare(context.Background(), stackdeploy.PrepareOptions{
		Stack: env.Stack, Environments: []*ir.ResolvedEnvironment{env}, Sources: src.sources(), Dir: dir,
	}); err != nil {
		t.Fatal(err)
	}
	if got, want := strings.Join(src.ran, "; "), "dist: bun install; dist/server/shop-stack/shop-storefront: bun bundle.ts"; got != want {
		t.Errorf("prepare ran %s, want %s", got, want)
	}
	p, err := stackdeploy.ReadPrepared(dir)
	if err != nil {
		t.Fatal(err)
	}
	prepared := p.Bundles[stacktest.StorefrontServer]
	if prepared == nil {
		t.Fatalf("prepare kept no bundle: %+v", p)
	}
	program := t.TempDir()
	o := f.options(t, env, nil)
	o.Dir = program
	m, err := stackdeploy.Deploy(context.Background(), stackdeploy.DeployOptions{
		Options: o, Images: images(1), Sources: &stackdeploy.Sources{Prepared: p, Run: noProjectCode(t)}, Planner: (&planner{to: 1}).plan, Now: fixedNow,
	})
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, f.log)
	}
	bundle := m.Bundles[stacktest.StorefrontServer]
	if bundle == nil || bundle.Digest != prepared.Digest || bundle.Files["main.js"] != sum(v1) {
		t.Fatalf("the deploy records the bundle as %+v, prepare %s", bundle, prepared.Digest)
	}
	data, err := os.ReadFile(filepath.Join(program, ir.ArtifactsDir, stacktest.StorefrontServer, strings.TrimPrefix(bundle.Digest, "sha256:"), "main.js"))
	if err != nil || string(data) != v1 {
		t.Errorf("the placed main.js holds %q: %v", data, err)
	}
}
