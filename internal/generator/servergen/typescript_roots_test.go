package servergen_test

import (
	"archive/tar"
	"compress/gzip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// tsRootServices are the services of the TypeScript fixture a build of
// ts-stack reaches, in build order: ts-db's TypeScript types export the
// queue ts-shop's worker claims (D53), ts-cache's the definitions both
// implementations depend on (D75), and ts-stack deploys the site ts-web
// (D55).
var tsRootServices = append([]string{"ts-db", "ts-cache"}, append(slices.Clone(tsAPIs), tsSite, "ts-stack")...)

// buildTSInto builds services of the TypeScript fixture into the output
// root out of the repository at repoRoot, naming its schemas root as every
// command does, by which a build knows the default output root,
// schemas/dist, from another.
func buildTSInto(f fixture, repoRoot string, paths naming.LocalPaths, out string, services ...string) error {
	opts := f.options(repoRoot, paths)
	opts.OutputRoot = out
	opts.SchemasRoot = filepath.Join(repoRoot, "schemas")
	for _, name := range services {
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			return fmt.Errorf("build %s into %s: %w", name, out, err)
		}
	}
	return nil
}

// projectLinks are the symbolic links under the node_modules of each
// package under the repository's typescript/ and web/, the
// implementations' and the sites', by path, each with the directory it
// names: where an install linked the project's own packages.
func projectLinks(t *testing.T, repoRoot string) map[string]string {
	t.Helper()
	links := map[string]string{}
	var packages []string
	for _, group := range []string{"typescript", "web"} {
		dirs, err := filepath.Glob(filepath.Join(repoRoot, group, "*", "node_modules"))
		if err != nil {
			t.Fatal(err)
		}
		packages = append(packages, dirs...)
	}
	for _, dir := range packages {
		err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.Type()&fs.ModeSymlink == 0 {
				return err
			}
			target, err := os.Readlink(p)
			if err != nil {
				return err
			}
			if !filepath.IsAbs(target) {
				target = filepath.Join(filepath.Dir(p), target)
			}
			links[p] = filepath.Clean(target)
			return nil
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	return links
}

// TestTypeScriptServersBuildInAnyOutputRoot (D65, amended): the project's
// TypeScript implementations, members of the default output root's Bun
// workspace, schemas/dist, install and type-check there as their
// scaffolds set them up, and the fixture's stack builds into two other
// output roots, one after the other and both at once. Each root's build
// mirrors the implementations into its impl/, and its workspace lists the
// mirrors and none of the project's packages, so each root installs
// frozen to its lockfile, the second from the first's, its servers, job
// and worker type-check, and each resolves its implementations to the
// root's own mirrors and runs them, a change to the package reaching a
// root with its next build and not before. The site ts-web, whose package
// the default root's build scaffolds, is mirrored the same way, and builds
// in each root's mirror (tsgen.SiteDir), where the stack's builds keep
// it, and never in its package. No install in either root touches the
// links in the project's own node_modules, which name the default root
// alone, as they did before; without the mirror, each root's install
// would take them over.
func TestTypeScriptServersBuildInAnyOutputRoot(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the TypeScript installs in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot := testpaths.TempDir(t)
	f := loadTypeScriptFixture(t)
	names := naming.Default()
	def := filepath.Join(repoRoot, "schemas", "dist")
	if err := buildTSInto(f, repoRoot, paths, def, tsRootServices...); err != nil {
		t.Fatal(err)
	}
	for service, code := range map[string]string{"ts-pricing": tsPricingImplementation, "ts-shop": tsShopImplementation} {
		if err := os.WriteFile(filepath.Join(names.TypeScriptImplementationDir(repoRoot, service), "index.ts"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	command := func(dir, name string, args ...string) error {
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			return fmt.Errorf("%s %s in %s: %w\n%s", filepath.Base(name), strings.Join(args, " "), dir, err, output)
		}
		return nil
	}
	if err := command(def, bun, "install"); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the default output root (likely offline): %v", err))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	implementations := []string{names.TypeScriptImplementationDir(repoRoot, "ts-pricing"), names.TypeScriptImplementationDir(repoRoot, "ts-shop")}
	projectChecks := func() {
		t.Helper()
		for _, dir := range implementations {
			if err := command(dir, tsc, "--noEmit", "-p", "tsconfig.json"); err != nil {
				t.Errorf("the project's package does not type-check against the default output root: %v", err)
			}
		}
	}
	projectChecks()
	links := projectLinks(t, repoRoot)
	if len(links) == 0 {
		t.Fatal("the default output root's install linked nothing into the project's packages")
	}
	// Each link names the default root's packages, or another link of
	// the package's own node_modules, as .bin's do.
	for link, target := range links {
		modules := link[:strings.Index(link, string(filepath.Separator)+"node_modules"+string(filepath.Separator))+len("/node_modules")]
		if !strings.HasPrefix(target, def+string(filepath.Separator)) && !strings.HasPrefix(target, modules+string(filepath.Separator)) {
			t.Fatalf("the default output root's install linked %s to %s, outside %s", link, target, def)
		}
	}

	site := names.SiteImplementationDir(repoRoot, tsSite)
	if dir, err := tsgen.SiteDir(def, repoRoot, tsSite, names.SiteImplementationPath(tsSite)); err != nil || dir != site {
		t.Fatalf("in the default output root, ts-web builds in %s (%v), want its package %s", dir, err, site)
	}
	// buildsSite builds ts-web where out's install builds it: in its
	// mirror, which holds the package's files, and not in the package.
	buildsSite := func(out string) error {
		dir, err := tsgen.SiteDir(out, repoRoot, tsSite, names.SiteImplementationPath(tsSite))
		if err != nil {
			return err
		}
		if mirror := servergen.MirrorDir(out, tsSite); dir != mirror {
			return fmt.Errorf("in %s, ts-web builds in %s, not its mirror %s", out, dir, mirror)
		}
		for _, file := range []string{"index.html", "config.generated.ts"} {
			mirrored, err := os.ReadFile(filepath.Join(dir, file))
			if source, serr := os.ReadFile(filepath.Join(site, file)); err != nil || serr != nil || string(mirrored) != string(source) {
				return fmt.Errorf("%s's mirror of ts-web does not hold its %s: %v, %v", out, file, err, serr)
			}
		}
		if err := command(dir, bun, "run", "build"); err != nil {
			return err
		}
		if _, err := os.Stat(filepath.Join(dir, "dist", "index.html")); err != nil {
			return fmt.Errorf("ts-web's build in %s wrote no page: %w", dir, err)
		}
		if _, err := os.Stat(filepath.Join(site, "dist")); !errors.Is(err, fs.ErrNotExist) {
			return fmt.Errorf("ts-web's build for %s wrote into its package: %v", out, err)
		}
		return nil
	}

	roots := []string{filepath.Join(testpaths.TempDir(t), "a"), filepath.Join(testpaths.TempDir(t), "b")}
	deployables := []string{"ts-pricing", "ts-shop", tsSweepCarts, tsRemindShoppers}
	// typecheck type-checks each package of the stack under out.
	typecheck := func(out string) error {
		for _, deployable := range deployables {
			dir := servergen.ServerDir(out, "ts-stack", deployable)
			if err := command(dir, tsc, "--noEmit", "-p", "tsconfig.json"); err != nil {
				return err
			}
		}
		return nil
	}
	// resolves has the packages of the stack under out resolve their
	// implementations, each to out's mirror.
	resolves := func(out string) error {
		for server, service := range map[string]string{"ts-pricing": "ts-pricing", "ts-shop": "ts-shop", tsRemindShoppers: "ts-shop"} {
			resolve := exec.Command(bun, "-e", `console.log(Bun.resolveSync("`+names.NpmImplementationPackage(service)+`", process.cwd()))`)
			resolve.Dir = servergen.ServerDir(out, "ts-stack", server)
			resolved, err := resolve.CombinedOutput()
			if err != nil {
				return fmt.Errorf("resolve %s's implementation from %s: %w\n%s", server, out, err, resolved)
			}
			if mirror := servergen.MirrorDir(out, service); !strings.HasPrefix(strings.TrimSpace(string(resolved)), mirror+string(filepath.Separator)) {
				return fmt.Errorf("%s in %s resolves its implementation to %s, outside the root's mirror %s", server, out, strings.TrimSpace(string(resolved)), mirror)
			}
		}
		return nil
	}
	lockfile := func(out string) string {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(out, "bun.lock"))
		if err != nil {
			t.Fatalf("no lockfile in %s: %v", out, err)
		}
		return string(data)
	}
	together := func(run func(out string) error) {
		t.Helper()
		errs := make(chan error, len(roots))
		for _, out := range roots {
			go func() { errs <- run(out) }()
		}
		for range roots {
			if err := <-errs; err != nil {
				t.Error(err)
			}
		}
		if t.Failed() {
			t.FailNow()
		}
	}
	untouched := func(when string) {
		t.Helper()
		got := projectLinks(t, repoRoot)
		for link, target := range got {
			for _, out := range roots {
				if strings.HasPrefix(target, out+string(filepath.Separator)) {
					t.Errorf("%s, the project's link %s names %s, under %s", when, link, target, out)
				}
			}
		}
		if !maps.Equal(got, links) {
			for link, target := range got {
				if links[link] != target {
					t.Errorf("%s, the project's link %s names %s, not %s", when, link, target, links[link])
				}
			}
			t.Errorf("%s, the project's packages hold %d links, not the %d the default root's install made", when, len(got), len(links))
		}
	}

	// One root after the other, the second from the first's lockfile.
	var lock string
	for i, out := range roots {
		if i > 0 {
			if err := os.MkdirAll(out, 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(out, "bun.lock"), []byte(lock), 0o644); err != nil {
				t.Fatal(err)
			}
		}
		if err := buildTSInto(f, repoRoot, paths, out, tsRootServices...); err != nil {
			t.Fatal(err)
		}
		root, err := os.ReadFile(filepath.Join(out, "package.json"))
		if err != nil || !strings.Contains(string(root), `"impl/*"`) || strings.Contains(string(root), `"../`) {
			t.Fatalf("%s's workspace does not list its mirrors in place of the project's packages: %v\n%s", out, err, root)
		}
		if i == 0 {
			if err := command(out, bun, "install"); err != nil {
				t.Fatal(err)
			}
			lock = lockfile(out)
			if !strings.Contains(lock, `"impl/ts-shop": {`) || strings.Contains(lock, `"../`) {
				t.Fatalf("%s's lockfile does not name the mirrors alone:\n%s", out, lock[:min(len(lock), 4000)])
			}
		}
		if err := command(out, bun, "install", "--frozen-lockfile"); err != nil {
			t.Fatal(err)
		}
		if err := resolves(out); err != nil {
			t.Fatal(err)
		}
		if err := typecheck(out); err != nil {
			t.Fatal(err)
		}
		if err := buildsSite(out); err != nil {
			t.Fatal(err)
		}
		untouched("after " + out + "'s install")
	}

	// Both at once, after a change to ts-shop's implementation, which each
	// root's server answers with once its build mirrored it. A second
	// change, which no build mirrors, reaches neither.
	shop := filepath.Join(names.TypeScriptImplementationDir(repoRoot, "ts-shop"), "index.ts")
	edit := func(header string) {
		t.Helper()
		code := strings.Replace(tsShopImplementation, `'id,region\n'`, `'`+header+`\n'`, 1)
		if code == tsShopImplementation {
			t.Fatal("ts-shop's implementation answers its export with no header to change")
		}
		if err := os.WriteFile(shop, []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	edit("id,region,built")
	together(func(out string) error { return buildTSInto(f, repoRoot, paths, out, "ts-stack") })
	together(func(out string) error { return command(out, bun, "install", "--frozen-lockfile") })
	together(resolves)
	together(buildsSite)
	edit("id,region,unbuilt")
	for _, out := range roots {
		if got := lockfile(out); got != lock {
			t.Errorf("another build changed %s's lockfile:\n%s", out, got)
		}
		edge := variables(t, "TS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"})
		edge = append(edge, variables(t, "TS_MEDIA_BUCKET", ir.BucketConnection{Name: "ts-media", Endpoint: "http://127.0.0.1:9"})...)
		server := bunServer(t, bun, servergen.ServerDir(out, "ts-stack", "ts-shop"), append([]string{"TS_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/ts_db?connect_timeout=1"}, edge...)...)
		server.expect(t, http.MethodGet, "/api/carts/tea/export", http.StatusOK, "id,region,built\ntea,eu\n")
		server.stop(t)
	}
	untouched("after both roots' installs")
	projectChecks()
	if err := command(def, bun, "install", "--frozen-lockfile"); err != nil {
		t.Errorf("the default output root no longer installs frozen: %v", err)
	}
	untouched("after the default root's install")
}

// listContext lists the files of the gzipped tarball at archive, a build
// context stackdeploy.WriteContext wrote.
func listContext(t *testing.T, archive string) []string {
	t.Helper()
	file, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	gz, err := gzip.NewReader(file)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	var files []string
	for {
		header, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return files
		}
		if err != nil {
			t.Fatal(err)
		}
		if header.Typeflag != tar.TypeDir {
			files = append(files, strings.TrimSuffix(header.Name, "/"))
		}
	}
}

// TestTheTypeScriptImageOfAnotherOutputRootTakesItsMirrors (D65,
// amended): a TypeScript server built into an output root other than the
// default one, inside the build context, gets a Dockerfile whose context
// takes the root's mirrors under impl/, each manifest by pattern and the
// server's own mirrors whole, and no implementation's own directory.
func TestTheTypeScriptImageOfAnotherOutputRootTakesItsMirrors(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	f := loadTypeScriptFixture(t)
	paths := tsFakePaths(repoRoot)
	// The checkouts the context takes in by name.
	for _, file := range []string{
		filepath.Join(paths.ScalarTypeScript, "package.json"),
		filepath.Join(paths.HTTPRuntimeTypeScript, "package.json"),
		filepath.Join(repoRoot, "third_party", "superscalar", "Cargo.toml"),
		filepath.Join(repoRoot, "third_party", "superscalar", "Cargo.lock"),
		filepath.Join(repoRoot, "third_party", "superscalar", "crates", "napi", "Cargo.toml"),
	} {
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte("{}\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	if err := buildTSInto(f, repoRoot, paths, filepath.Join(repoRoot, "schemas", "dist"), tsRootServices...); err != nil {
		t.Fatal(err)
	}
	out := filepath.Join(repoRoot, "schemas", "dist-b")
	if err := buildTSInto(f, repoRoot, paths, out, tsRootServices...); err != nil {
		t.Fatal(err)
	}
	dir := servergen.ServerDir(out, "ts-stack", "ts-shop")
	ignore, err := os.ReadFile(filepath.Join(dir, servergen.DockerIgnoreFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"!schemas/dist-b/impl/*/package.json\n", "!schemas/dist-b/impl/ts-shop\n"} {
		if !strings.Contains(string(ignore), want) {
			t.Errorf("the context does not take in %q:\n%s", strings.TrimSpace(want), ignore)
		}
	}
	if strings.Contains(string(ignore), "\n!typescript/") {
		t.Errorf("the context takes in an implementation's own directory:\n%s", ignore)
	}
	archive, err := os.Create(filepath.Join(t.TempDir(), "context.tar"))
	if err != nil {
		t.Fatal(err)
	}
	defer archive.Close()
	built, err := stackdeploy.WriteContext(archive, repoRoot, filepath.Join(dir, servergen.DockerFile))
	if err != nil {
		t.Fatalf("write the build context: %v", err)
	}
	files := listContext(t, archive.Name())
	for _, want := range []string{"schemas/dist-b/impl/ts-shop/index.ts", "schemas/dist-b/impl/ts-shop/package.json", "schemas/dist-b/impl/ts-pricing/package.json", "schemas/dist-b/package.json"} {
		if !slices.Contains(files, want) {
			t.Errorf("the context of %s holds no %s", built.Dockerfile, want)
		}
	}
	for _, file := range files {
		if strings.HasPrefix(file, "typescript/") || strings.HasPrefix(file, "schemas/dist/") || file == "schemas/dist-b/impl/ts-pricing/index.ts" {
			t.Errorf("the context of %s holds %s", built.Dockerfile, file)
		}
	}
}
