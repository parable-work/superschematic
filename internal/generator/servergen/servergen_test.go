package servergen_test

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/release"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var update = flag.Bool("update", false, "rewrite the golden entrypoints")

const (
	// servicesRoot holds the fixture: shop-stack, whose declared server
	// Storefront serves shop-orders and shop-reviews and calls shop-api,
	// which runs on a default server of its own, and shop-db, the one
	// database of the three APIs. Its one environment is local.
	servicesRoot = "testdata/services"

	// cloudStack is a second stack of the fixture, with shop-stack's
	// servers, whose Staging environment places shop-db on Cloud SQL.
	cloudStack = "shop-cloud-stack"

	// goldenRoot holds the entrypoints as the output root lays them out,
	// under server, and the scaffolds as the repository root does, under
	// go.
	goldenRoot = "testdata/golden"
)

// order is the order a build-all builds the fixture in, cloudStack left
// out: shop-orders calls shop-api, so its server builds after shop-api's
// SDK.
var order = []string{"shop-db", "shop-api", "shop-orders", "shop-reviews", "shop-stack"}

// apis are the services of order the stacks serve, in order.
var apis = order[:len(order)-1]

// expireOrders is the deployable of shop-orders' job ExpireOrders, whose
// entrypoint each stack's build writes beside the servers' (D52).
const expireOrders = "shop-orders-expire-orders"

// fixture is the loaded fixture services.
type fixture struct {
	reg     *registry.Registry
	schemas map[string]*ir.Schema
	configs map[string]*schemaconfig.SchemaConfig
}

// loadFixture loads every fixture service, cloudStack included, with the
// core registry and stacktest's fake target, whose sql connector derives a
// Cloud SQL connector configuration as the gcp target's does.
func loadFixture(t *testing.T, root string) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range append(slices.Clone(order), cloudStack) {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(root, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// options are one service's build in the repository at repoRoot, whose
// schemas root is repoRoot/schemas, as the CLI sets them.
func (f fixture) options(repoRoot string, paths naming.LocalPaths) generator.Options {
	return generator.Options{
		OutputRoot:     filepath.Join(repoRoot, "schemas", "dist"),
		Paths:          paths,
		Naming:         naming.Default(),
		Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
		RepositoryRoot: repoRoot,
		Registry:       f.reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			if schema, ok := f.schemas[name]; ok {
				return schema, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
		DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
			cfg, ok := f.configs[name]
			return cfg, ok
		},
		LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			if cfg, ok := f.configs[name]; ok {
				return cfg, nil
			}
			return nil, fmt.Errorf("no fixture %s", name)
		},
	}
}

// build builds services, or every fixture service in order, into the
// repository at repoRoot.
func (f fixture) build(t *testing.T, repoRoot string, paths naming.LocalPaths, services ...string) *generator.Result {
	t.Helper()
	if len(services) == 0 {
		services = order
	}
	var result *generator.Result
	for _, name := range services {
		var err error
		result, err = generator.Run(f.schemas[name], f.configs[name], f.options(repoRoot, paths))
		if err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	return result
}

// fakePaths places the runtime modules in the repository at repoRoot, as
// this repository's naming file does.
func fakePaths(repoRoot string) naming.LocalPaths {
	return naming.LocalPaths{
		ScalarGo:        filepath.Join(repoRoot, "third_party", "superscalar", "go"),
		SchemaIR:        filepath.Join(repoRoot, "ir"),
		SchemaRuntimeGo: filepath.Join(repoRoot, "runtime", "schema", "go"),
		HTTPRuntimeGo:   filepath.Join(repoRoot, "runtime", "http", "go"),
		VersionGraphGo:  filepath.Join(repoRoot, "runtime", "versiongraph", "go"),
	}
}

// generated lists the files the build of each of stacks writes, by their
// path under goldenRoot: an entrypoint's from the output root, a
// scaffold's from the repository root. It lists every server's
// cloudsql.go, which only a server on Cloud SQL has.
func generated(repoRoot string, stacks ...string) map[string]string {
	files := map[string]string{}
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, stack := range stacks {
		for _, server := range []string{"Storefront", "shop-api", expireOrders} {
			for _, file := range []string{servergen.MainFile, servergen.CloudSQLFile, servergen.ModFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
				files[filepath.Join("server", stack, server, file)] = filepath.Join(servergen.ServerDir(out, stack, server), file)
			}
		}
	}
	for _, service := range []string{"shop-api", "shop-orders", "shop-reviews"} {
		dir := naming.Default().GoImplementationDir(repoRoot, service)
		for _, file := range []string{apigen.ImplementationFile, servergen.ModFile} {
			files[filepath.Join("go", service, file)] = filepath.Join(dir, file)
		}
	}
	return files
}

// TestEntrypointGolden: each stack's build writes an entrypoint module per
// server, Storefront serving two APIs on one database and calling
// shop-api, and shop-api's default server, and one per job, shop-orders'
// ExpireOrders, which builds shop-orders' Deps as Storefront does and runs
// the job (D52); and scaffolds each API's implementation with a module of
// its own. shop-stack's entrypoints never run on Cloud SQL and link no
// Cloud SQL connector; cloudStack's, whose Staging places shop-db on Cloud
// SQL, do. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestEntrypointGolden -update
func TestEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, stack := range []string{"shop-stack", cloudStack} {
		result := f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(apis), stack)...)
		if got, want := result.Outputs["server"], servergen.StackDir(out, stack); got != want {
			t.Errorf("server output = %q, want %q", got, want)
		}
	}

	compareGoldens(t, goldenRoot, generated(repoRoot, "shop-stack", cloudStack))
	for _, stack := range []string{"shop-stack", cloudStack} {
		entries, err := os.ReadDir(servergen.StackDir(out, stack))
		if err != nil {
			t.Fatal(err)
		}
		var servers []string
		for _, e := range entries {
			servers = append(servers, e.Name())
		}
		if got := strings.Join(servers, " "); got != "Storefront shop-api "+expireOrders {
			t.Errorf("%s: entrypoints = %s, want Storefront, shop-api and %s", stack, got, expireOrders)
		}
	}
}

// compareGoldens checks each generated file of files, by its path under
// root, against its golden, which -update rewrites, and removes when the
// build did not write it.
func compareGoldens(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for _, rel := range slices.Sorted(maps.Keys(files)) {
		golden := filepath.Join(root, rel)
		got, err := os.ReadFile(files[rel])
		written := err == nil
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if *update {
			if !written {
				if err := os.Remove(golden); err != nil && !errors.Is(err, os.ErrNotExist) {
					t.Fatal(err)
				}
				continue
			}
			if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(golden, got, 0o644); err != nil {
				t.Fatal(err)
			}
			continue
		}
		want, err := os.ReadFile(golden)
		switch {
		case errors.Is(err, os.ErrNotExist) && !written:
		case errors.Is(err, os.ErrNotExist):
			t.Errorf("the build wrote %s, which has no golden (run with -update to write it)", rel)
		case err != nil:
			t.Fatal(err)
		case !written:
			t.Errorf("the build did not write %s, which has a golden", rel)
		case string(got) != string(want):
			t.Errorf("%s differs from %s; run with -update and review the diff", rel, golden)
		}
	}
}

// testRelease is a release the tests generate as, with made-up digests of
// its static archives.
var testRelease = release.Release{
	Version:  "1.2.3",
	ScalarGo: "v0.0.0-20260928143325-10cf493f485e",
	Archives: map[string]string{
		"darwin-arm64": strings.Repeat("1", 64),
		"darwin-x64":   strings.Repeat("2", 64),
		"linux-arm64":  strings.Repeat("3", 64),
		"linux-x64":    strings.Repeat("4", 64),
	},
}

// releaseGoldenRoot holds the entrypoint modules a release writes in a
// project with no [paths], as the output root lays them out.
const releaseGoldenRoot = "testdata/golden/release"

// TestReleaseEntrypointGolden: in a project that takes the runtime
// modules from the module proxy, whose naming file has no [paths], a
// release's build pins each runtime module to the release in the server's
// go.mod, every version the generated modules require: superschematic's
// at the release's tag and superscalar's at the version the release links.
// The Dockerfile downloads the release's static archives for the image's
// platform, checked against their digests, in place of the Rust stages,
// and the context holds no checkout. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestReleaseEntrypointGolden -update
func TestReleaseEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	for _, name := range append(slices.Clone(apis), "shop-stack") {
		opts := f.options(repoRoot, naming.LocalPaths{})
		opts.Release = &testRelease
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, server := range []string{"Storefront", "shop-api"} {
		for _, file := range []string{servergen.ModFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
			files[filepath.Join("server", "shop-stack", server, file)] = filepath.Join(servergen.ServerDir(out, "shop-stack", server), file)
		}
	}
	compareGoldens(t, releaseGoldenRoot, files)
}

// TestNoArchivesNoDockerfile: without [paths] scalar_go, a binary built
// from a checkout, which is no release, and a release binary its release
// workflow did not build, which names no digests, have no archives for
// the image to link, so the build writes no Dockerfile and says why.
func TestNoArchivesNoDockerfile(t *testing.T) {
	f := loadFixture(t, servicesRoot)
	for _, tc := range []struct {
		name    string
		release release.Release
		why     string
	}{
		{"checkout build", release.Release{}, "this superschematic is built from a checkout, which is no release"},
		{"no digests", release.Release{Version: "1.2.3", ScalarGo: testRelease.ScalarGo}, "this superschematic 1.2.3, which its release workflow did not build, names no digest of them for linux-x64"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repoRoot := t.TempDir()
			var log strings.Builder
			for _, name := range append(slices.Clone(apis), "shop-stack") {
				opts := f.options(repoRoot, naming.LocalPaths{})
				opts.Release, opts.Log = &tc.release, &log
				if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
					t.Fatalf("build %s: %v", name, err)
				}
			}
			dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "shop-api")
			if _, err := os.Stat(filepath.Join(dir, servergen.DockerFile)); !errors.Is(err, os.ErrNotExist) {
				t.Errorf("the build wrote a Dockerfile: %v", err)
			}
			if want := "server shop-api: no Dockerfile, since the naming file's [paths] scalar_go is unset, so the image links the static archives superschematic's release ships, and " + tc.why; !strings.Contains(log.String(), want) {
				t.Errorf("the log does not say %q:\n%s", want, log.String())
			}
		})
	}
}

// TestOnlyAServerOnCloudSQLLinksTheConnector: a server whose database some
// environment places on Cloud SQL gets cloudsql.go, which main.go's connect
// calls for a Cloud SQL configuration, and requires the Cloud SQL
// connector. A server no environment places there gets neither, and its
// connect refuses a Cloud SQL configuration and says why.
func TestOnlyAServerOnCloudSQLLinksTheConnector(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot))
	f.build(t, repoRoot, fakePaths(repoRoot), cloudStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	read := func(stack, server, file string) (string, bool) {
		data, err := os.ReadFile(filepath.Join(servergen.ServerDir(out, stack, server), file))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		return string(data), err == nil
	}
	for _, server := range []string{"Storefront", "shop-api"} {
		if _, ok := read("shop-stack", server, servergen.CloudSQLFile); ok {
			t.Errorf("shop-stack's %s, never on Cloud SQL, has cloudsql.go", server)
		}
		if mod, _ := read("shop-stack", server, servergen.ModFile); strings.Contains(mod, "cloud.google.com") {
			t.Errorf("shop-stack's %s, never on Cloud SQL, requires a Google module:\n%s", server, mod)
		}
		main, _ := read("shop-stack", server, servergen.MainFile)
		if want := "which server " + server + " does not link: no environment of stack shop-stack placed its databases on Cloud SQL"; !strings.Contains(main, want) || strings.Contains(main, "connectCloudSQL") {
			t.Errorf("shop-stack's %s connects a Cloud SQL configuration or does not say why it refuses one:\n%s", server, main)
		}

		if _, ok := read(cloudStack, server, servergen.CloudSQLFile); !ok {
			t.Errorf("%s's %s, on Cloud SQL in Staging, has no cloudsql.go", cloudStack, server)
		}
		if mod, _ := read(cloudStack, server, servergen.ModFile); !strings.Contains(mod, "\tcloud.google.com/go/cloudsqlconn v1.25.3\n") {
			t.Errorf("%s's %s does not require the Cloud SQL connector:\n%s", cloudStack, server, mod)
		}
		if main, _ := read(cloudStack, server, servergen.MainFile); !strings.Contains(main, "return connectCloudSQL(ctx, field, *db.CloudSQL)") {
			t.Errorf("%s's %s does not connect a Cloud SQL configuration:\n%s", cloudStack, server, main)
		}
	}
}

// TestTheScaffoldNeverOverwrites: the stack's build writes an
// implementation only while its package is missing, never rewrites the
// one it wrote or the module beside it, and never writes into a package
// that holds a Go file.
func TestTheScaffoldNeverOverwrites(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot))

	dir := naming.Default().GoImplementationDir(repoRoot, "shop-orders")
	file := filepath.Join(dir, apigen.ImplementationFile)
	edited := []byte("package shoporders\n\n// The engineer's code, with the jobs' constructor.\nfunc NewJobs() {}\n")
	if err := os.WriteFile(file, edited, 0o644); err != nil {
		t.Fatal(err)
	}
	mod := filepath.Join(dir, servergen.ModFile)
	editedMod := []byte("module example.com/mine/orders\n\ngo 1.26.4\n")
	if err := os.WriteFile(mod, editedMod, 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	for path, want := range map[string][]byte{file: edited, mod: editedMod} {
		if got, err := os.ReadFile(path); err != nil || string(got) != string(want) {
			t.Errorf("a second build rewrote %s: %q, %v", path, got, err)
		}
	}
	main, err := os.ReadFile(filepath.Join(servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "Storefront"), servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), `shopordersimpl "example.com/mine/orders"`) {
		t.Errorf("the entrypoint does not import the implementation from its renamed module:\n%s", main)
	}

	if err := os.Remove(file); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "orders.go"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, fakePaths(repoRoot), "shop-stack")
	if _, err := os.Stat(file); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("a build scaffolded into a package that holds orders.go: %v", err)
	}
}

// TestAnImplementationThatPredatesItsJobsFails: shop-orders declares a
// job, and its implementation, which exists and so is the engineer's,
// declares no NewJobs. The build writes nothing into it and fails, saying
// what to add (D52).
func TestAnImplementationThatPredatesItsJobsFails(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot))
	dir := naming.Default().GoImplementationDir(repoRoot, "shop-orders")
	file := filepath.Join(dir, apigen.ImplementationFile)
	before := []byte("package shoporders\n\n// The engineer's code, from before the job.\nfunc New() {}\n")
	if err := os.WriteFile(file, before, 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], f.options(repoRoot, fakePaths(repoRoot)))
	for _, want := range []string{
		"stack shop-stack: shop-orders declares jobs, and its implementation at " + dir + ", which the build no longer writes into, declares no NewJobs",
		"func NewJobs(deps api.Deps) (api.Jobs, error)",
		"ExpireOrders(ctx context.Context) error",
		"where api is example.com/schemas/api/shop-orders",
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build = %v, want it to say %q", err, want)
		}
	}
	if got, err := os.ReadFile(file); err != nil || string(got) != string(before) {
		t.Errorf("a refused build wrote %s: %q, %v", file, got, err)
	}
}

// TestAnImplementationInNoModuleFails: a package the build did not
// scaffold, which no go.mod holds, cannot be imported, and the build says
// where to add one.
func TestAnImplementationInNoModuleFails(t *testing.T) {
	repoRoot := t.TempDir()
	dir := naming.Default().GoImplementationDir(repoRoot, "shop-reviews")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "reviews.go"), []byte("package shopreviews\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot), order[:len(order)-1]...)
	_, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], f.options(repoRoot, fakePaths(repoRoot)))
	if err == nil || !strings.Contains(err.Error(), "server Storefront serves shop-reviews, whose implementation "+dir+" is in no Go module; add a go.mod at it or above it") {
		t.Fatalf("build = %v, want the missing module named", err)
	}
	if _, err := os.Stat(naming.Default().GoImplementationDir(repoRoot, "shop-orders")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a refused build scaffolded shop-orders: %v", err)
	}
}

// TestTwoAPIsOnOneRouteFail: two APIs one server serves cannot register
// one method and path, whatever their path parameters are named, and the
// build names both.
func TestTwoAPIsOnOneRouteFail(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	reviews := f.schemas["shop-reviews"].OperationSets[0].Operations[0]
	reviews.RestPath = "orders/{productId}"
	_, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], f.options(repoRoot, fakePaths(repoRoot)))
	want := "stack shop-stack: server Storefront serves APIs that register one route twice, and one router answers each method and path once: shop-orders registers GET /api/orders/{id} and shop-reviews registers GET /api/orders/{productId}"
	if err == nil || !strings.Contains(err.Error(), want) {
		t.Fatalf("build = %v, want %q", err, want)
	}
	// The stack generator wrote the Local environment before the server
	// generator refused; no entrypoint or implementation is written.
	for _, dir := range []string{
		servergen.StackDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack"),
		filepath.Dir(naming.Default().GoImplementationDir(repoRoot, "shop-orders")),
	} {
		if _, err := os.Stat(dir); !errors.Is(err, os.ErrNotExist) {
			t.Errorf("a refused build wrote %s: %v", dir, err)
		}
	}
}

// TestNoRepositoryRootWritesNoEntrypoint: a build that has no repository
// root, such as a library caller's, cannot find the implementations, so it
// writes no entrypoint and says so.
func TestNoRepositoryRootWritesNoEntrypoint(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	opts := f.options(repoRoot, fakePaths(repoRoot))
	opts.RepositoryRoot = ""
	result, err := generator.Run(f.schemas["shop-stack"], f.configs["shop-stack"], opts)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := result.Outputs["server"]; ok || !strings.Contains(strings.Join(result.Skipped, " "), "server") {
		t.Errorf("outputs %v, skipped %v; want server skipped", result.Outputs, result.Skipped)
	}
}

// TestATypeScriptServerScaffoldsItsImplementation: with shop-api served in
// TypeScript, cloudStack's build scaffolds its implementation as a
// TypeScript package at the [implementation_paths] typescript template,
// writes no Go scaffold, writes its server's TypeScript entrypoint, and
// makes the output root the Bun workspace of the generated TypeScript
// packages, the server and that implementation (D51). Storefront's Go
// entrypoint is written as before.
func TestATypeScriptServerScaffoldsItsImplementation(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	cfg := *f.configs["shop-api"]
	cfg.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}, "typescript": map[string]any{"enabled": true}},
		"api":   map[string]any{"enabled": true, "language": generator.APILanguageTypeScript},
		"sdk":   map[string]any{"go": map[string]any{"enabled": true}},
	}
	f.configs["shop-api"] = &cfg
	f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(apis), cloudStack)...)

	names := naming.Default()
	impl := names.TypeScriptImplementationDir(repoRoot, "shop-api")
	for _, file := range []string{"index.ts", "package.json", "tsconfig.json"} {
		if _, err := os.Stat(filepath.Join(impl, file)); err != nil {
			t.Errorf("the TypeScript scaffold lacks %s: %v", file, err)
		}
	}
	index, err := os.ReadFile(filepath.Join(impl, "index.ts"))
	if err != nil || !strings.Contains(string(index), "export const create: Constructor") {
		t.Errorf("index.ts does not export create: %v\n%s", err, index)
	}
	if _, err := os.Stat(names.GoImplementationDir(repoRoot, "shop-api")); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a TypeScript API got a Go scaffold: %v", err)
	}
	out := filepath.Join(repoRoot, "schemas", "dist")
	if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, cloudStack, "shop-api"), servergen.TypeScriptMainFile)); err != nil {
		t.Errorf("the TypeScript server got no entrypoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, cloudStack, "shop-api"), servergen.MainFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the TypeScript server got a Go entrypoint: %v", err)
	}
	if _, err := os.Stat(filepath.Join(servergen.ServerDir(out, cloudStack, "Storefront"), servergen.MainFile)); err != nil {
		t.Errorf("Storefront's Go entrypoint is missing: %v", err)
	}
	root, err := os.ReadFile(filepath.Join(out, "package.json"))
	if err != nil || !strings.Contains(string(root), `"../../typescript/*"`) {
		t.Errorf("the output root's workspace does not hold the implementations: %v\n%s", err, root)
	}

	// Its package is the engineer's from then on.
	edited := []byte("// The engineer's code.\n")
	if err := os.WriteFile(filepath.Join(impl, "index.ts"), edited, 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, fakePaths(repoRoot), cloudStack)
	if got, err := os.ReadFile(filepath.Join(impl, "index.ts")); err != nil || string(got) != string(edited) {
		t.Errorf("a second build rewrote index.ts: %q, %v", got, err)
	}
}

// TestTheBuildContextHoldsTheRuntimes: a project whose runtime modules lie
// above the repository root, as an example inside a checkout does, gets no
// Dockerfile, until the naming file's [paths] build_context names the
// directory that holds both; the Dockerfile's paths are then relative to
// it.
func TestTheBuildContextHoldsTheRuntimes(t *testing.T) {
	checkout := t.TempDir()
	repoRoot := filepath.Join(checkout, "examples", "shop")
	f := loadFixture(t, servicesRoot)
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "shop-stack", "shop-api")
	dockerfile := filepath.Join(dir, servergen.DockerFile)
	f.build(t, repoRoot, fakePaths(checkout))
	if _, err := os.Stat(dockerfile); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("with the runtimes outside the repository root, %s: %v; want no Dockerfile", dockerfile, err)
	}

	n := naming.Default()
	n.Paths.BuildContext = "../.."
	for _, name := range order {
		opts := f.options(repoRoot, fakePaths(checkout))
		opts.Naming = n
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	data, err := os.ReadFile(filepath.Join(dir, servergen.DockerIgnoreFile))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"!examples/shop/schemas/dist/server/shop-stack/shop-api\n", "!third_party/superscalar/go\n", "!examples/shop/go/shop-api\n"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("%s lacks %q:\n%s", servergen.DockerIgnoreFile, want, data)
		}
	}
}

// goCommand runs go in dir.
func goCommand(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// freePort returns a port nothing listens on.
func freePort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

// started is a server binary running on a port of its own.
type started struct {
	cmd  *exec.Cmd
	base string
	out  *strings.Builder
}

// start runs binary with env and waits for /healthz.
func start(t *testing.T, binary string, env ...string) *started {
	t.Helper()
	port := freePort(t)
	var out strings.Builder
	cmd := exec.Command(binary)
	cmd.Env = append(os.Environ(), append(env, "PORT="+port)...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &started{cmd: cmd, base: "http://127.0.0.1:" + port, out: &out}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(20 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s did not answer /healthz: %v\n%s", binary, err, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// expect sends method to path and checks the status, and that the body
// holds each of contains.
func (s *started) expect(t *testing.T, method, path string, status int, contains ...string) {
	t.Helper()
	s.send(t, method, path, "{}", status, contains...)
}

// send is expect with the request body body.
func (s *started) send(t *testing.T, method, path, body string, status int, contains ...string) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != status {
		t.Errorf("%s %s = %d, want %d: %s", method, path, resp.StatusCode, status, answer)
	}
	for _, want := range contains {
		if !strings.Contains(string(answer), want) {
			t.Errorf("%s %s answered %s, which lacks %q", method, path, answer, want)
		}
	}
}

// stop sends SIGTERM and checks the server drains and exits cleanly.
func (s *started) stop(t *testing.T) {
	t.Helper()
	if err := s.cmd.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server exited with %v after SIGTERM:\n%s", err, s.out.String())
		}
		if !strings.Contains(s.out.String(), `"msg":"stopped"`) {
			t.Errorf("the server did not log its graceful stop:\n%s", s.out.String())
		}
	case <-time.After(15 * time.Second):
		t.Errorf("the server did not stop within 15s of SIGTERM:\n%s", s.out.String())
	}
}

// TestEntrypointCompilesAndServes: each server's entrypoint builds with
// its scaffolded implementations and vets. Storefront starts with no
// database up, answers /healthz and reports the database in /readyz, and
// routes each API's requests to it, the scaffold answering 501 and its
// payload decryptor refusing an encrypted body; shop-api's scaffolded auth
// middleware refuses its protected route. Each stops cleanly on SIGTERM.
// Storefront, which no environment places on Cloud SQL, refuses to start
// on a Cloud SQL configuration.
func TestEntrypointCompilesAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, paths)

	out := filepath.Join(repoRoot, "schemas", "dist")
	binaries := map[string]string{}
	for _, server := range []string{"Storefront", "shop-api"} {
		dir := servergen.ServerDir(out, "shop-stack", server)
		goCommand(t, dir, "mod", "tidy")
		goCommand(t, dir, "vet", ".")
		binaries[server] = filepath.Join(t.TempDir(), server)
		goCommand(t, dir, "build", "-o", binaries[server], ".")
	}
	for _, service := range []string{"shop-api", "shop-orders", "shop-reviews"} {
		dir := naming.Default().GoImplementationDir(repoRoot, service)
		goCommand(t, dir, "mod", "tidy")
		goCommand(t, dir, "build", "./...")
	}

	// Port 9 discards; nothing answers a database there. The client of
	// shop-api signs its service credential with an edge key, as the local
	// target's connector derives it, which the server reads at startup.
	unreachable := "postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1&sslmode=disable"
	storefront := start(t, binaries["Storefront"], append([]string{"SHOP_DB_DATABASE_URL=" + unreachable}, edgeVariables(t, "SHOP_API_SERVICE")...)...)
	storefront.expect(t, http.MethodGet, "/healthz", http.StatusOK, `"ok"`)
	storefront.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["shop-db"]`)
	storefront.expect(t, http.MethodGet, "/api/orders/o-1", http.StatusNotImplemented, "Order.GetOrder")
	storefront.expect(t, http.MethodGet, "/api/products/p-1/reviews", http.StatusNotImplemented, "Review.ListReviews")
	storefront.send(t, http.MethodPost, "/api/orders", `{"algorithm":"RSA_OAEP_256","payload":"c2t1"}`, http.StatusBadRequest, "Failed to decrypt request payload")
	storefront.expect(t, http.MethodGet, "/api/openapi.json", http.StatusOK, "/api/orders/{id}")
	storefront.expect(t, http.MethodGet, "/api/nowhere", http.StatusNotFound)
	storefront.stop(t)

	shopAPI := start(t, binaries["shop-api"], "SHOP_DB_DATABASE_URL="+unreachable)
	shopAPI.expect(t, http.MethodGet, "/api/products/p-1", http.StatusNotImplemented, "Product.GetProduct")
	shopAPI.expect(t, http.MethodPut, "/api/products/p-1/name?name=x", http.StatusUnauthorized)
	shopAPI.stop(t)

	cmd := exec.Command(binaries["Storefront"])
	cmd.Env = append(os.Environ(), append(append(cloudSQLVariables(t, "SHOP_DB_DATABASE"), edgeVariables(t, "SHOP_API_SERVICE")...), "PORT="+freePort(t))...)
	refused, err := cmd.CombinedOutput()
	if want := "SHOP_DB_DATABASE is a Cloud SQL connector configuration, which server Storefront does not link"; err == nil || !strings.Contains(string(refused), want) {
		t.Errorf("Storefront on a Cloud SQL configuration = %v, want it to stop saying %q:\n%s", err, want, refused)
	}

	// shop-orders' job builds the API's Deps from the same variables and
	// runs ExpireOrders once: the scaffold's fails, and the job exits 1
	// saying so; an implemented one succeeds, and the job exits 0. Its pool
	// connects when first used, so the job runs with the database down.
	jobDir := servergen.ServerDir(out, "shop-stack", expireOrders)
	goCommand(t, jobDir, "mod", "tidy")
	goCommand(t, jobDir, "vet", ".")
	jobBinary := filepath.Join(t.TempDir(), expireOrders)
	goCommand(t, jobDir, "build", "-o", jobBinary, ".")
	runJob := func() (string, error) {
		cmd := exec.Command(jobBinary)
		cmd.Env = append(os.Environ(), append([]string{"SHOP_DB_DATABASE_URL=" + unreachable}, edgeVariables(t, "SHOP_API_SERVICE")...)...)
		output, err := cmd.CombinedOutput()
		return string(output), err
	}
	output, err := runJob()
	var exit *exec.ExitError
	if !errors.As(err, &exit) || exit.ExitCode() != 1 || !strings.Contains(output, `"msg":"job failed"`) || !strings.Contains(output, "job ExpireOrders") {
		t.Errorf("the scaffold's job = %v, want exit 1 with the not-implemented error:\n%s", err, output)
	}
	impl := filepath.Join(naming.Default().GoImplementationDir(repoRoot, "shop-orders"), apigen.ImplementationFile)
	source, err := os.ReadFile(impl)
	if err != nil {
		t.Fatal(err)
	}
	implemented := strings.Replace(string(source), `return api.NotImplementedError("job ExpireOrders")`, `j.deps.Logger.Info("expired no order")
	return nil`, 1)
	if implemented == string(source) {
		t.Fatalf("%s has no not-implemented ExpireOrders:\n%s", impl, source)
	}
	if err := os.WriteFile(impl, []byte(implemented), 0o644); err != nil {
		t.Fatal(err)
	}
	goCommand(t, jobDir, "build", "-o", jobBinary, ".")
	if output, err := runJob(); err != nil || !strings.Contains(output, `"msg":"expired no order"`) || !strings.Contains(output, `"msg":"job done"`) {
		t.Errorf("the implemented job = %v, want exit 0 after the method's log:\n%s", err, output)
	}
}

// cloudSQLVariables are the variables of the database field named field
// holding a Cloud SQL connection, as ir.DerivedVariables encodes a
// resolved environment's value for a platform.
func cloudSQLVariables(t *testing.T, field string) []string {
	t.Helper()
	vars, err := ir.DerivedVariables(field, ir.DatabaseConnection{CloudSQL: &ir.CloudSQLConnection{
		Instance: "acme-staging:us-east1:shop-db", Database: "shop_db", User: "storefront@acme-staging.iam",
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, len(vars))
	for i, v := range vars {
		env[i] = fmt.Sprintf("%s=%v", v.Name, v.Value)
	}
	return env
}

// offlineCredentials are the variables of application default credentials
// that reach nothing: a service account key, freshly generated, whose
// token endpoint is a loopback port nothing answers, and a proxy on that
// port for every other request, so a token fetch fails and no request
// leaves the machine.
func offlineCredentials(t *testing.T) []string {
	t.Helper()
	key, err := rsa.GenerateKey(rand.Reader, 2048)
	if err != nil {
		t.Fatal(err)
	}
	der, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	nowhere := "127.0.0.1:" + freePort(t)
	account, err := json.Marshal(map[string]string{
		"type":           "service_account",
		"project_id":     "acme-staging",
		"private_key_id": "test",
		"private_key":    string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: der})),
		"client_email":   "storefront@acme-staging.iam.gserviceaccount.com",
		"client_id":      "1",
		"token_uri":      "http://" + nowhere + "/token",
	})
	if err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(t.TempDir(), "credentials.json")
	if err := os.WriteFile(file, account, 0o600); err != nil {
		t.Fatal(err)
	}
	return []string{"GOOGLE_APPLICATION_CREDENTIALS=" + file, "HTTPS_PROXY=http://" + nowhere, "HTTP_PROXY=http://" + nowhere}
}

// TestCloudSQLEntrypointConnectsBothWays: a server some environment places
// on Cloud SQL builds and vets with the Cloud SQL connector, and its one
// binary connects either way. cloudsql.go's cloudSQLConfig turns the
// derived variables of a Cloud SQL connection into a pool that logs in as
// the IAM user over the dialer's connection, which a fake Postgres answers
// (testdata/cloudsql_test.go, run in the module). The binary serves on a
// connection string, as the local target derives it; on a Cloud SQL
// configuration it starts without dialing, and /readyz reports the
// database while the connector cannot fetch a token. The modules come
// from the network, so the test skips when go mod tidy cannot fetch them.
func TestCloudSQLEntrypointConnectsBothWays(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, paths, append(slices.Clone(apis), cloudStack)...)
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), cloudStack, "Storefront")
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	if out, err := tidy.CombinedOutput(); err != nil {
		t.Skipf("go mod tidy failed (likely offline): %v\n%s", err, out)
	}
	goCommand(t, dir, "vet", ".")
	binary := filepath.Join(t.TempDir(), "Storefront")
	goCommand(t, dir, "build", "-o", binary, ".")

	unit, err := os.ReadFile(filepath.Join("testdata", "cloudsql_test.go"))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "cloudsql_test.go"), unit, 0o644); err != nil {
		t.Fatal(err)
	}
	cloudSQL := cloudSQLVariables(t, "SHOP_DB_DATABASE")
	test := exec.Command("go", "test", "-v", "-count=1", "-run", "^TestCloudSQLConfig$", ".")
	test.Dir = dir
	// PWD keeps go in dir as the build saw it, through a symlinked
	// temporary directory, which the replaces' relative paths count from;
	// exec sets it only when Env is nil.
	test.Env = append(os.Environ(), append(cloudSQL, "PWD="+dir)...)
	if out, err := test.CombinedOutput(); err != nil || !strings.Contains(string(out), "--- PASS: TestCloudSQLConfig") {
		t.Fatalf("go test in %s: %v\n%s", dir, err, out)
	}

	shopAPI := edgeVariables(t, "SHOP_API_SERVICE")
	local := start(t, binary, append([]string{"SHOP_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1&sslmode=disable"}, shopAPI...)...)
	local.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["shop-db"]`)
	local.expect(t, http.MethodGet, "/api/orders/o-1", http.StatusNotImplemented, "Order.GetOrder")
	local.stop(t)

	cloud := start(t, binary, append(append(cloudSQL, offlineCredentials(t)...), shopAPI...)...)
	cloud.expect(t, http.MethodGet, "/api/orders/o-1", http.StatusNotImplemented, "Order.GetOrder")
	cloud.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["shop-db"]`)
	cloud.stop(t)
	// The ping dialed the instance through the connector, as the IAM user.
	for _, want := range []string{"user=storefront@acme-staging.iam database=shop_db", "(acme-staging:us-east1:shop-db): dial error"} {
		if !strings.Contains(cloud.out.String(), want) {
			t.Errorf("the server's readiness check did not log %q:\n%s", want, cloud.out.String())
		}
	}
}

// edgeVariables are the variables of the service field named field, an
// endpoint whose credential is a token signed with a fresh Ed25519 key,
// encoded as ir.DerivedVariables encodes a resolved environment's value.
func edgeVariables(t *testing.T, field string) []string {
	t.Helper()
	public, private, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	key, err := json.Marshal(map[string]string{
		"kty": "OKP", "crv": "Ed25519", "kid": "edge-1",
		"d": base64.RawURLEncoding.EncodeToString(private.Seed()),
		"x": base64.RawURLEncoding.EncodeToString(public),
	})
	if err != nil {
		t.Fatal(err)
	}
	vars, err := ir.DerivedVariables(field, ir.ServiceEndpoint{URL: "http://127.0.0.1:9", Credential: &ir.ServiceCredential{
		Source: ir.CredentialSignedToken, Audience: "shop-api", Issuer: "Storefront", Key: string(key),
	}})
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, len(vars))
	for i, v := range vars {
		env[i] = fmt.Sprintf("%s=%v", v.Name, v.Value)
	}
	return env
}

// withServiceClause gives shop-api's renameProduct an @allowService clause
// listing shop-orders, beside its user clause.
func withServiceClause(f fixture) {
	for _, set := range f.schemas["shop-api"].OperationSets {
		for _, op := range set.Operations {
			if op.Name == "renameProduct" {
				op.ServiceCallers = &ir.ServiceCallers{Mode: ir.ServiceCallersAllow, From: []string{"shop-orders"}}
			}
		}
	}
}

// TestToolchainPinsMatchToolsEnv: the go directive the modules state and
// the images the Dockerfiles build and run in, Go's, Rust's and Bun's, are
// tools.env's pins.
func TestToolchainPinsMatchToolsEnv(t *testing.T) {
	data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), "tools.env"))
	if err != nil {
		t.Fatal(err)
	}
	pins := map[string]string{}
	for _, line := range strings.Split(string(data), "\n") {
		if key, value, ok := strings.Cut(line, "="); ok && !strings.HasPrefix(key, "#") {
			pins[key] = value
		}
	}
	if pins["GO_VERSION"] != servergen.GoVersion {
		t.Errorf("tools.env pins Go %s; servergen.GoVersion is %s", pins["GO_VERSION"], servergen.GoVersion)
	}
	if pins["RUST_VERSION"] != servergen.RustVersion {
		t.Errorf("tools.env pins Rust %s; servergen.RustVersion is %s", pins["RUST_VERSION"], servergen.RustVersion)
	}
	if pins["BUN_VERSION"] != servergen.BunVersion {
		t.Errorf("tools.env pins Bun %s; servergen.BunVersion is %s", pins["BUN_VERSION"], servergen.BunVersion)
	}
}

// copyTree copies the directory src to dst, leaving out build products:
// target and node_modules directories, and skip, a directory under src.
func copyTree(t *testing.T, src, dst string, skip ...string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		if d.IsDir() {
			if d.Name() == "target" || d.Name() == "node_modules" || slices.Contains(skip, filepath.ToSlash(rel)) {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if !d.Type().IsRegular() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(dst, rel), data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// docker runs docker with args and returns its trimmed output.
func docker(t *testing.T, args ...string) string {
	t.Helper()
	out, err := exec.Command("docker", args...).CombinedOutput()
	if err != nil {
		t.Fatalf("docker %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return strings.TrimSpace(string(out))
}

// TestTheDockerfileBuildsAnImageThatServes: Storefront's Dockerfile, in
// cloudStack, where it links the Cloud SQL connector, builds from a
// repository root holding the generated modules, the scaffolds, the
// runtime modules and a superscalar checkout, and the image serves:
// /healthz, an API's route, and a clean stop on docker stop. It needs
// Docker, and builds superscalar's archive in a Rust stage, so it runs
// only outside -short.
func TestTheDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	repoRoot := dockerRepo(t)
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(apis), cloudStack)...)

	image := fmt.Sprintf("superschematic-servergen-test:%d", time.Now().UnixNano())
	dockerBuild(t, repoRoot, image, "-f", "schemas/dist/server/"+cloudStack+"/Storefront/Dockerfile")
	expectServes(t, image)
}

// TestTheReleaseDockerfileBuildsAnImageThatServes: in a project whose
// naming file names no superscalar checkout, Storefront's Dockerfile
// downloads the static archives of the release that wrote it, checks them
// against the digest the release names, and builds with no checkout in
// its context, and the image serves. A file server in a container stands
// in for the release page, holding archives the checkout's Dockerfile
// builds, and the build reaches it on the Docker host's network through
// the SUPERSCHEMATIC_RELEASE argument. superscalar's Go binding comes from
// the module proxy at the version this binary links. It runs only outside
// -short, where Docker runs.
func TestTheReleaseDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	repoRoot := dockerRepo(t)
	f := loadFixture(t, servicesRoot)
	f.build(t, repoRoot, fakePaths(repoRoot), append(slices.Clone(apis), cloudStack)...)

	// The archive the checkout's Dockerfile builds, for the Docker host's
	// platform, packed as a release packs it.
	stamp := time.Now().UnixNano()
	builder := fmt.Sprintf("superschematic-servergen-test:%d-superscalar", stamp)
	dockerBuild(t, repoRoot, builder, "--target", "superscalar", "-f", "schemas/dist/server/"+cloudStack+"/Storefront/Dockerfile")
	created := docker(t, "create", builder)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", created).Run() })
	archive := filepath.Join(t.TempDir(), "libsuperscalar_ffi.a")
	docker(t, "cp", created+":/libsuperscalar_ffi.a", archive)
	arch := docker(t, "version", "--format", "{{.Server.Arch}}")
	platform := release.Platform("linux", arch)
	if platform == "" {
		t.Skipf("a release ships no archives for linux/%s", arch)
	}
	r := release.Release{Version: "1.2.3", ScalarGo: scalarGoVersion(t), Archives: map[string]string{
		"linux-x64": strings.Repeat("4", 64), "linux-arm64": strings.Repeat("3", 64),
	}}
	tarball := filepath.Join(t.TempDir(), release.ArchiveName(r.Version, platform))
	r.Archives[platform] = packArchives(t, tarball, archive)

	// The project: the runtime modules from its checkout, superscalar
	// from the module proxy, and no superscalar checkout at all.
	if err := os.RemoveAll(filepath.Join(repoRoot, "third_party")); err != nil {
		t.Fatal(err)
	}
	paths := fakePaths(repoRoot)
	paths.ScalarGo = ""
	for _, name := range append(slices.Clone(apis), cloudStack) {
		opts := f.options(repoRoot, paths)
		opts.Release = &r
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	dockerfile := filepath.Join(servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), cloudStack, "Storefront"), servergen.DockerFile)
	if data, err := os.ReadFile(dockerfile); err != nil || !strings.Contains(string(data), "sum="+r.Archives[platform]) {
		t.Fatalf("%s does not pin the archives' digest %s: %v\n%s", dockerfile, r.Archives[platform], err, data)
	}

	server := docker(t, "run", "-d", "-p", "127.0.0.1::80", "busybox:1.37", "sh", "-c", "mkdir -p /www && exec httpd -f -p 80 -h /www")
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", server).Run() })
	docker(t, "cp", tarball, server+":/www/")
	address := firstLine(docker(t, "port", server, "80/tcp"))

	image := fmt.Sprintf("superschematic-servergen-test:%d", stamp)
	dockerBuild(t, repoRoot, image, "--network", "host", "--build-arg", "SUPERSCHEMATIC_RELEASE=http://"+address,
		"-f", "schemas/dist/server/"+cloudStack+"/Storefront/Dockerfile")
	expectServes(t, image)
}

// scalarGoVersion is the version of superscalar's Go binding the root
// module requires, the one a release built from this checkout links.
func scalarGoVersion(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, line := range strings.Split(string(data), "\n") {
		if version, ok := strings.CutPrefix(strings.TrimSpace(line), release.ScalarGoModule+" "); ok {
			return strings.Fields(version)[0]
		}
	}
	t.Fatalf("go.mod requires no %s", release.ScalarGoModule)
	return ""
}

// packArchives writes a release's archives tarball holding archive under
// lib/, in the directory the tarball is named for, and returns its hex
// SHA-256.
func packArchives(t *testing.T, tarball, archive string) string {
	t.Helper()
	data, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	dir := strings.TrimSuffix(filepath.Base(tarball), ".tar.gz")
	for _, entry := range []struct {
		name string
		data []byte
	}{{dir + "/lib/", nil}, {dir + "/lib/" + filepath.Base(archive), data}} {
		hdr := &tar.Header{Name: entry.name, Mode: 0o644, Size: int64(len(entry.data)), Typeflag: tar.TypeReg}
		if entry.data == nil {
			hdr.Mode, hdr.Typeflag = 0o755, tar.TypeDir
		}
		if err := tw.WriteHeader(hdr); err != nil {
			t.Fatal(err)
		}
		if _, err := tw.Write(entry.data); err != nil {
			t.Fatal(err)
		}
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := gz.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(tarball, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	sum := sha256.Sum256(buf.Bytes())
	return hex.EncodeToString(sum[:])
}

// needDocker skips a test outside -short, or where Docker does not run.
func needDocker(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("skipping docker build in -short mode")
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Skip("docker is not installed")
	}
	if out, err := exec.Command("docker", "info").CombinedOutput(); err != nil {
		t.Skipf("docker is not running: %v\n%s", err, out)
	}
}

// dockerRepo is a repository root holding the runtime modules and a
// superscalar checkout, without its archives, as the fixture's [paths]
// name them.
func dockerRepo(t *testing.T) string {
	t.Helper()
	local := testpaths.Local(t)
	repo := testpaths.RepoRoot(t)
	repoRoot := t.TempDir()
	for _, dir := range []string{"ir", "runtime/http/go", "runtime/schema/go", "third_party/superscalar/crates"} {
		copyTree(t, filepath.Join(repo, dir), filepath.Join(repoRoot, dir))
	}
	copyTree(t, local.ScalarGo, filepath.Join(repoRoot, "third_party", "superscalar", "go"), "lib")
	for _, file := range []string{"Cargo.toml", "Cargo.lock"} {
		data, err := os.ReadFile(filepath.Join(repo, "third_party", "superscalar", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "third_party", "superscalar", file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return repoRoot
}

// dockerBuild builds image from the context at root with args, and removes
// it when the test ends.
func dockerBuild(t *testing.T, root, image string, args ...string) {
	t.Helper()
	cmd := exec.Command("docker", append(append([]string{"build", "-q", "-t", image}, args...), ".")...)
	cmd.Dir = root
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, out)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })
}

// firstLine is s up to its first newline.
func firstLine(s string) string {
	if i := strings.Index(s, "\n"); i >= 0 {
		return s[:i]
	}
	return s
}

// expectServes runs Storefront's image and checks it serves: /healthz, an
// API's route, /readyz reporting its database down, and a clean stop on
// docker stop.
func expectServes(t *testing.T, image string) {
	t.Helper()
	container := docker(t, "run", "-d", "-p", "127.0.0.1::8080",
		"-e", "SHOP_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/shop_db?connect_timeout=1",
		"-e", "SHOP_API_SERVICE_URL=http://127.0.0.1:9", image)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	address := docker(t, "port", container, "8080/tcp")
	if i := strings.LastIndex(address, "\n"); i >= 0 {
		address = address[:i]
	}
	s := &started{base: "http://" + address, out: &strings.Builder{}}
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the container did not answer /healthz: %v\n%s", err, docker(t, "logs", container))
		}
		time.Sleep(200 * time.Millisecond)
	}
	s.expect(t, http.MethodGet, "/api/orders/o-1", http.StatusNotImplemented, "Order.GetOrder")
	s.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["shop-db"]`)
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"stopped"`) {
		t.Errorf("the container did not log its graceful stop:\n%s", logs)
	}
}
