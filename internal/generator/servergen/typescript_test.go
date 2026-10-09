package servergen_test

import (
	"crypto/ed25519"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

const (
	// tsServicesRoot holds the TypeScript fixture (D51): ts-stack, whose
	// default server of ts-shop, on ts-db, calls ts-pricing's, whose one
	// operation set admits only calling services. Its one environment is
	// local.
	tsServicesRoot = "testdata/typescript/services"

	// tsCloudStack is a second stack of the fixture, with ts-stack's
	// servers, whose Staging environment places ts-db on Cloud SQL.
	tsCloudStack = "ts-cloud-stack"

	// tsSite is the fixture's site, which ts-stack deploys and which calls
	// ts-shop: ts-shop's server answers CORS for it (D55). It is loaded,
	// not built.
	tsSite = "ts-web"

	// tsGoldenRoot holds the TypeScript entrypoints as the output root
	// lays them out.
	tsGoldenRoot = "testdata/golden/typescript"
)

// tsOrder is the order a build-all builds the TypeScript fixture in,
// tsCloudStack left out: ts-shop calls ts-pricing, so it builds after
// ts-pricing's SDK.
var tsOrder = []string{"ts-db", "ts-pricing", "ts-shop", "ts-stack"}

// tsAPIs are the services of tsOrder the stacks serve.
var tsAPIs = tsOrder[1 : len(tsOrder)-1]

// loadTypeScriptFixture loads every TypeScript fixture service, as
// loadFixture loads the Go one.
func loadTypeScriptFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range append(slices.Clone(tsOrder), tsCloudStack, tsSite) {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsServicesRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// tsFakePaths places the checkouts a TypeScript server's image builds from
// in the repository at repoRoot, as this repository's naming file does.
func tsFakePaths(repoRoot string) naming.LocalPaths {
	return naming.LocalPaths{
		ScalarTypeScript:      filepath.Join(repoRoot, "third_party", "superscalar", "bindings", "typescript"),
		HTTPRuntimeTypeScript: filepath.Join(repoRoot, "runtime", "http", "typescript"),
	}
}

// tsGenerated lists the files of each TypeScript entrypoint the builds of
// stacks write, by their path under tsGoldenRoot.
func tsGenerated(repoRoot string, stacks ...string) map[string]string {
	files := map[string]string{}
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, stack := range stacks {
		for _, server := range []string{"ts-pricing", "ts-shop"} {
			for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile, servergen.TypeScriptConfigFile, servergen.DockerFile, servergen.DockerIgnoreFile} {
				files[filepath.Join("server", stack, server, file)] = filepath.Join(servergen.ServerDir(out, stack, server), file)
			}
		}
	}
	return files
}

// TestTypeScriptEntrypointGolden: each stack's build writes a package per
// TypeScript server into the output root's Bun workspace: ts-shop's opens
// a pool on ts-db, builds a client of ts-pricing that sends the edge's
// credential, and authenticates its end users; ts-pricing's verifies its
// callers against its callers field. ts-stack's ts-shop never runs on
// Cloud SQL and depends on no Cloud SQL connector; ts-cloud-stack's, whose
// Staging places ts-db on Cloud SQL, does. Each gets a Dockerfile that
// builds superscalar's Node addon and installs the workspace. Regenerate
// with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptEntrypointGolden -update
func TestTypeScriptEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	out := filepath.Join(repoRoot, "schemas", "dist")
	for _, stack := range []string{"ts-stack", tsCloudStack} {
		result := f.build(t, repoRoot, tsFakePaths(repoRoot), append(slices.Clone(tsAPIs), stack)...)
		if got, want := result.Outputs["server"], servergen.StackDir(out, stack); got != want {
			t.Errorf("server output = %q, want %q", got, want)
		}
	}
	compareGoldens(t, tsGoldenRoot, tsGenerated(repoRoot, "ts-stack", tsCloudStack))

	// The servers join the workspace, and the scaffolded implementations
	// are its members too.
	root, err := os.ReadFile(filepath.Join(out, "package.json"))
	if err != nil || !strings.Contains(string(root), `"server/*/*"`) || !strings.Contains(string(root), `"../../typescript/*"`) {
		t.Errorf("the output root's workspace does not hold the servers and the implementations: %v\n%s", err, root)
	}
	for _, service := range tsAPIs {
		if _, err := os.Stat(filepath.Join(naming.Default().TypeScriptImplementationDir(repoRoot, service), "package.json")); err != nil {
			t.Errorf("the build did not scaffold %s: %v", service, err)
		}
	}
}

// TestTypeScriptEntrypointNamesTheImplementation: the server's package
// depends on an implementation by the name its package.json gives it,
// which the engineer may change, and refuses an implementation whose
// package holds TypeScript and no package.json, which the workspace could
// not link.
func TestTypeScriptEntrypointNamesTheImplementation(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), append(slices.Clone(tsAPIs), "ts-stack")...)
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, "ts-pricing")
	manifest := filepath.Join(impl, "package.json")
	data, err := os.ReadFile(manifest)
	if err != nil {
		t.Fatal(err)
	}
	renamed := strings.Replace(string(data), `"@schemas/ts-pricing-implementation"`, `"@acme/pricing"`, 1)
	if err := os.WriteFile(manifest, []byte(renamed), 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, tsFakePaths(repoRoot), "ts-stack")
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "ts-stack", "ts-pricing")
	for _, file := range []string{servergen.TypeScriptPackageFile, servergen.TypeScriptMainFile} {
		got, err := os.ReadFile(filepath.Join(dir, file))
		if err != nil || !strings.Contains(string(got), "@acme/pricing") {
			t.Errorf("%s does not name the renamed implementation: %v\n%s", file, err, got)
		}
	}

	if err := os.Remove(manifest); err != nil {
		t.Fatal(err)
	}
	opts := f.options(repoRoot, tsFakePaths(repoRoot))
	_, err = generator.Run(f.schemas["ts-stack"], f.configs["ts-stack"], opts)
	if err == nil || !strings.Contains(err.Error(), "holds no package.json, by which the Bun workspace links it; add one named @schemas/ts-pricing-implementation") {
		t.Fatalf("a build over an implementation with no package.json = %v, want it refused", err)
	}
}

// TestTypeScriptEntrypointWithoutCheckoutsHasNoDockerfile: the image builds
// superscalar's Node addon and the HTTP runtime's package from checkouts,
// which no published package replaces yet, so without [paths]
// scalar_typescript the build writes no Dockerfile and says why.
func TestTypeScriptEntrypointWithoutCheckoutsHasNoDockerfile(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptFixture(t)
	var log strings.Builder
	for _, name := range append(slices.Clone(tsAPIs), "ts-stack") {
		opts := f.options(repoRoot, naming.LocalPaths{})
		opts.Log = &log
		if _, err := generator.Run(f.schemas[name], f.configs[name], opts); err != nil {
			t.Fatalf("build %s: %v", name, err)
		}
	}
	dir := servergen.ServerDir(filepath.Join(repoRoot, "schemas", "dist"), "ts-stack", "ts-shop")
	if _, err := os.Stat(filepath.Join(dir, servergen.TypeScriptMainFile)); err != nil {
		t.Errorf("no main.ts: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, servergen.DockerFile)); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("a Dockerfile without the checkouts: %v", err)
	}
	if want := "server ts-shop: no Dockerfile, since the naming file's [paths] scalar_typescript is unset"; !strings.Contains(log.String(), want) {
		t.Errorf("the build did not say %q:\n%s", want, log.String())
	}
}

// TestTypeScriptBuildKeepsTheLockfile: the lockfile the workspace's
// install writes beside the root, which the project commits (D51,
// amended), outlasts every build of the stack, and the root it pairs with
// is the same bytes on each. In a repository whose rules ignore the output
// root itself, the stack's build says in one line how to commit the
// lockfile, and changes no ignore file; once the rules take the lockfile
// back, it says nothing.
func TestTypeScriptBuildKeepsTheLockfile(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not installed")
	}
	global := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(global, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", global)
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	repoRoot := t.TempDir()
	if out, err := exec.Command("git", "-C", repoRoot, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("git init: %v\n%s", err, out)
	}
	gitignore := filepath.Join(repoRoot, ".gitignore")
	if err := os.WriteFile(gitignore, []byte("schemas/dist/\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsAPIs...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	lock := filepath.Join(out, "bun.lock")
	const pinned = "{\n  \"lockfileVersion\": 1,\n  \"workspaces\": {}\n}\n"
	if err := os.WriteFile(lock, []byte(pinned), 0o644); err != nil {
		t.Fatal(err)
	}
	build := func() string {
		t.Helper()
		var log strings.Builder
		opts := f.options(repoRoot, tsFakePaths(repoRoot))
		opts.Log = &log
		if _, err := generator.Run(f.schemas["ts-stack"], f.configs["ts-stack"], opts); err != nil {
			t.Fatalf("build ts-stack: %v\n%s", err, log.String())
		}
		return log.String()
	}
	log := build()
	root, err := os.ReadFile(filepath.Join(out, "package.json"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "  - " + lock + " is ignored by .gitignore:1:schemas/dist/; commit it, so images and CI install the TypeScript versions it pins: ignore the output root's contents, not the directory (dist/* and !dist/bun.lock in place of dist/; docs/stack-model.md, section 8.6)\n"; !strings.Contains(log, want) {
		t.Errorf("the build did not say how to commit the lockfile, %q:\n%s", want, log)
	}
	if got, err := os.ReadFile(gitignore); err != nil || string(got) != "schemas/dist/\n" {
		t.Errorf("the build changed .gitignore: %q, %v", got, err)
	}

	if err := os.WriteFile(gitignore, []byte("schemas/dist/*\n!schemas/dist/bun.lock\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsAPIs...)
	if log := build(); strings.Contains(log, "bun.lock") {
		t.Errorf("the build mentions the lockfile, which git does not ignore:\n%s", log)
	}
	if got, err := os.ReadFile(lock); err != nil || string(got) != pinned {
		t.Errorf("the builds did not keep the lockfile: %q, %v", got, err)
	}
	if again, err := os.ReadFile(filepath.Join(out, "package.json")); err != nil || string(again) != string(root) {
		t.Errorf("the workspace's root changed between builds: %v\n%s\nthen\n%s", err, root, again)
	}
}

// tsPricingImplementation quotes a price per SKU for a calling service.
const tsPricingImplementation = `import type { Constructor } from '@schemas/ts-pricing-api';

export const create: Constructor = deps => ({
  quotes: {
    async getQuote({ sku }, ctx) {
      deps.logger.info('quoted', { sku, caller: ctx.serviceCaller?.deployable });
      return { sku, cents: sku.length * 100 };
    },
  },
});
`

// tsShopImplementation answers a cart with ts-pricing's quote, which it
// asks for with the client in its Deps. It takes a while over the cart
// named slow, so a test can stop the server with a request in flight, and
// answers 501 for the one named unpriced.
const tsShopImplementation = `import { notImplemented } from '@superschematic/http-runtime';
import type { AuthenticatorFactory, Constructor } from '@schemas/ts-shop-api';

export const create: Constructor = deps => ({
  carts: {
    async getCart({ id }, ctx) {
      if (id === 'slow') await new Promise(resolve => setTimeout(resolve, 1500));
      if (id === 'unpriced') throw notImplemented('carts.getCart for an unpriced cart');
      const quote = await deps.tsPricing.quotes.getQuote(id, { forward: ctx });
      return { id, cents: quote.cents, region: deps.config.STOREFRONT_REGION };
    },
    async getMyCart() {
      throw notImplemented('carts.getMyCart');
    },
  },
});

export const authenticate: AuthenticatorFactory = () => async () => null;
`

// edgeKey is an Ed25519 key pair as JWKs: the private one a caller signs
// with and the public one the callee verifies with.
func edgeKey(t *testing.T) (private, public string) {
	t.Helper()
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	x := base64.RawURLEncoding.EncodeToString(pub)
	privateJWK, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "edge-1", "x": x, "d": base64.RawURLEncoding.EncodeToString(priv.Seed())})
	if err != nil {
		t.Fatal(err)
	}
	publicJWK, err := json.Marshal(map[string]string{"kty": "OKP", "crv": "Ed25519", "kid": "edge-1", "x": x})
	if err != nil {
		t.Fatal(err)
	}
	return string(privateJWK), string(publicJWK)
}

// variables are the variables of a derived field holding value, as
// ir.DerivedVariables encodes a resolved environment's value.
func variables(t *testing.T, field string, value any) []string {
	t.Helper()
	vars, err := ir.DerivedVariables(field, value)
	if err != nil {
		t.Fatal(err)
	}
	env := make([]string, len(vars))
	for i, v := range vars {
		env[i] = fmt.Sprintf("%s=%v", v.Name, v.Value)
	}
	return env
}

// bunServer runs main.ts in dir under Bun on a port of its own and waits
// for /healthz.
func bunServer(t *testing.T, bun, dir string, env ...string) *started {
	t.Helper()
	port := freePort(t)
	var out strings.Builder
	cmd := exec.Command(bun, servergen.TypeScriptMainFile)
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), append(env, "PORT="+port)...)
	cmd.Stdout, cmd.Stderr = &out, &out
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	s := &started{cmd: cmd, base: "http://127.0.0.1:" + port, out: &out}
	t.Cleanup(func() { _ = cmd.Process.Kill(); _ = cmd.Wait() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		resp, err := http.Get(s.base + "/healthz")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return s
			}
		}
		if time.Now().After(deadline) || cmd.ProcessState != nil {
			t.Fatalf("main.ts in %s did not answer /healthz: %v\n%s", dir, err, out.String())
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// installTypeScriptRuntime builds the HTTP runtime's dist/, which the
// workspace's root overrides the package with, and checks superscalar's
// binding is built.
func installTypeScriptRuntime(t *testing.T, bun string, paths naming.LocalPaths) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist")); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar TypeScript binding not built: %v", err))
	}
	install := exec.Command(bun, "install", "--frozen-lockfile")
	install.Dir = paths.HTTPRuntimeTypeScript
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed for the http runtime (likely offline): %v\n%s", err, out))
	}
	build := exec.Command(bun, "run", "build")
	build.Dir = paths.HTTPRuntimeTypeScript
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the http runtime: %v\n%s", err, out)
	}
}

// TestTypeScriptEntrypointCompilesAndServes: each TypeScript server's
// package installs in the output root's workspace with the
// implementations, and main.ts type-checks and runs under Bun on the
// variables the stack derives. ts-pricing verifies a calling service's
// credential against its callers field and refuses a request without one.
// ts-shop starts with no database up, answers /healthz and reports the
// database in /readyz, answers a cart with ts-pricing's quote, which it
// asks for with a token signed with its edge's key, and refuses its
// protected route without an end user. On SIGTERM it lets a request in
// flight finish, then stops cleanly. Built for no environment on Cloud
// SQL, it refuses to start on a Cloud SQL configuration.
func TestTypeScriptEntrypointCompilesAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, paths, append(slices.Clone(tsAPIs), "ts-stack")...)
	names := naming.Default()
	for service, code := range map[string]string{"ts-pricing": tsPricingImplementation, "ts-shop": tsShopImplementation} {
		if err := os.WriteFile(filepath.Join(names.TypeScriptImplementationDir(repoRoot, service), "index.ts"), []byte(code), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	out := filepath.Join(repoRoot, "schemas", "dist")
	run := func(dir, name string, args ...string) {
		t.Helper()
		cmd := exec.Command(name, args...)
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("%s %s in %s: %v\n%s", name, strings.Join(args, " "), dir, err, output)
		}
	}
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	pricingDir := servergen.ServerDir(out, "ts-stack", "ts-pricing")
	shopDir := servergen.ServerDir(out, "ts-stack", "ts-shop")
	for _, dir := range []string{pricingDir, shopDir, names.TypeScriptImplementationDir(repoRoot, "ts-pricing"), names.TypeScriptImplementationDir(repoRoot, "ts-shop")} {
		run(dir, tsc, "--noEmit", "-p", "tsconfig.json")
	}

	// The edge from ts-shop to ts-pricing, as the local target's connector
	// derives it: ts-shop signs with the private key as its own issuer,
	// and ts-pricing's callers field holds the public key.
	private, public := edgeKey(t)
	pricing := bunServer(t, bun, pricingDir, variables(t, ir.CallersField("ts-pricing"), ir.ServiceAuth{Issuers: []*ir.ServiceAuthIssuer{{
		Issuer:             "ts-shop",
		Audience:           "ts-pricing",
		Algorithms:         []string{ir.AlgorithmEdDSA},
		Keys:               []ir.ServiceAuthKey{{JWK: public}},
		MaxLifetimeSeconds: 300,
		Callers:            []ir.ServiceAuthCaller{{Subject: "ts-shop", Deployable: "ts-shop", Serves: []string{"ts-shop"}}},
	}}})...)
	pricing.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)
	pricing.expect(t, http.MethodGet, "/api/quotes/tea", http.StatusUnauthorized, "service_unauthorized")

	// Port 9 discards; nothing answers a database there.
	unreachable := "postgres://shop@127.0.0.1:9/ts_db?connect_timeout=1&sslmode=disable"
	edge := variables(t, "TS_PRICING_SERVICE", ir.ServiceEndpoint{URL: pricing.base, Credential: &ir.ServiceCredential{
		Source: ir.CredentialSignedToken, Audience: "ts-pricing", Issuer: "ts-shop", Key: private,
	}})
	// The site ts-web calls ts-shop from its origin, which ts-shop's CORS
	// field lists (D55).
	const siteOrigin = "https://ts-web.acme.dev"
	shop := bunServer(t, bun, shopDir, append([]string{"TS_DB_DATABASE_URL=" + unreachable, "TS_SHOP_CORS_ORIGINS=" + siteOrigin}, edge...)...)
	checkCORS(t, shop.base, http.MethodOptions, "/api/carts/tea", siteOrigin, http.MethodGet, http.StatusNoContent, siteOrigin)
	checkCORS(t, shop.base, http.MethodOptions, "/api/carts/tea", "https://evil.example", http.MethodGet, http.StatusNoContent, "")
	checkCORS(t, shop.base, http.MethodGet, "/api/carts/tea", siteOrigin, "", http.StatusOK, siteOrigin)
	shop.expect(t, http.MethodGet, "/healthz", http.StatusOK, `"ok"`)
	shop.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["ts-db"]`)
	shop.expect(t, http.MethodGet, "/api/carts/tea", http.StatusOK, `"cents":300`, `"region":"eu"`)
	// The implementation's problem is the router's, though the two import
	// the runtime from packages with different peers.
	shop.expect(t, http.MethodGet, "/api/carts/unpriced", http.StatusNotImplemented, "carts.getCart for an unpriced cart")
	shop.expect(t, http.MethodGet, "/api/me/cart", http.StatusUnauthorized)
	shop.expect(t, http.MethodGet, "/api/nowhere", http.StatusNotFound, "not_found")
	if !strings.Contains(pricing.out.String(), `"msg":"quoted"`) || !strings.Contains(pricing.out.String(), `"caller":"ts-shop"`) {
		t.Errorf("ts-pricing did not log the quote for ts-shop:\n%s", pricing.out.String())
	}

	// SIGTERM with a request in flight: the request finishes, then the
	// server stops.
	slow := make(chan error, 1)
	go func() {
		resp, err := http.Get(shop.base + "/api/carts/slow")
		if err == nil {
			resp.Body.Close()
			if resp.StatusCode != http.StatusOK {
				err = fmt.Errorf("status %d", resp.StatusCode)
			}
		}
		slow <- err
	}()
	time.Sleep(300 * time.Millisecond)
	shop.stop(t)
	if err := <-slow; err != nil {
		t.Errorf("the request in flight at SIGTERM: %v\n%s", err, shop.out.String())
	}
	if !strings.Contains(shop.out.String(), `"msg":"shutting down"`) {
		t.Errorf("ts-shop did not log its shutdown:\n%s", shop.out.String())
	}
	pricing.stop(t)

	cmd := exec.Command(bun, servergen.TypeScriptMainFile)
	cmd.Dir = shopDir
	cmd.Env = append(os.Environ(), append(append(cloudSQLVariables(t, "TS_DB_DATABASE"), edge...), "PORT="+freePort(t))...)
	refused, err := cmd.CombinedOutput()
	if want := "TS_DB_DATABASE is a Cloud SQL connector configuration, which server ts-shop does not depend on"; err == nil || !strings.Contains(string(refused), want) {
		t.Errorf("ts-shop on a Cloud SQL configuration = %v, want it to stop saying %q:\n%s", err, want, refused)
	}
	cmd = exec.Command(bun, servergen.TypeScriptMainFile)
	cmd.Dir = shopDir
	cmd.Env = append(os.Environ(), "PORT="+freePort(t))
	refused, err = cmd.CombinedOutput()
	if want := "configuration of ts-shop: "; err == nil || !strings.Contains(string(refused), want) || !strings.Contains(string(refused), "TS_PRICING_SERVICE_URL") {
		t.Errorf("ts-shop without its configuration = %v, want it to stop naming the variables:\n%s", err, refused)
	}
}

// TestTheTypeScriptDockerfileBuildsAnImageThatServes: ts-shop's
// Dockerfile, in ts-cloud-stack, where it depends on the Cloud SQL
// connector, builds from the context a deploy writes
// (stackdeploy.WriteContext) out of a repository root holding the
// generated packages, the scaffolds, the installed workspace's lockfile
// and checkouts of superscalar and the HTTP runtime, and the image serves:
// /healthz, an API's route, /readyz reporting its database down, and a
// clean stop on docker stop. It needs Docker and the network, builds
// superscalar's Node addon in a Rust stage, and runs only outside -short.
func TestTheTypeScriptDockerfileBuildsAnImageThatServes(t *testing.T) {
	needDocker(t)
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skipf("bun not available: %v", err)
	}
	local := testpaths.Local(t)
	repo := testpaths.RepoRoot(t)
	repoRoot := t.TempDir()
	// The checkouts, with what a checkout builds, which the host's install
	// copies and the ignore file leaves out of the image's context.
	copyTree(t, local.HTTPRuntimeTypeScript, filepath.Join(repoRoot, "runtime", "http", "typescript"))
	copyTree(t, filepath.Join(repo, "third_party", "superscalar", "crates"), filepath.Join(repoRoot, "third_party", "superscalar", "crates"))
	copyTree(t, local.ScalarTypeScript, filepath.Join(repoRoot, "third_party", "superscalar", "bindings", "typescript"))
	for _, file := range []string{"Cargo.toml", "Cargo.lock"} {
		data, err := os.ReadFile(filepath.Join(repo, "third_party", "superscalar", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(repoRoot, "third_party", "superscalar", file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	f := loadTypeScriptFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), append(slices.Clone(tsAPIs), tsCloudStack)...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		t.Skipf("bun install failed at the output root (likely offline): %v\n%s", err, output)
	}

	dockerfile := filepath.Join(servergen.ServerDir(out, tsCloudStack, "ts-shop"), servergen.DockerFile)
	archive := filepath.Join(t.TempDir(), "context.tar.gz")
	file, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	built, err := stackdeploy.WriteContext(file, repoRoot, dockerfile)
	if cerr := file.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		t.Fatalf("write the build context: %v", err)
	}
	context, err := os.Open(archive)
	if err != nil {
		t.Fatal(err)
	}
	defer context.Close()
	image := fmt.Sprintf("superschematic-servergen-ts-test:%d", time.Now().UnixNano())
	build := exec.Command("docker", "build", "-q", "-t", image, "-f", built.Dockerfile, "-")
	build.Stdin = context
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("docker build: %v\n%s", err, output)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rmi", "-f", image).Run() })

	args := []string{"run", "-d", "-p", "127.0.0.1::8080", "-e", "TS_DB_DATABASE_URL=postgres://shop@127.0.0.1:9/ts_db?connect_timeout=1"}
	for _, v := range variables(t, "TS_PRICING_SERVICE", ir.ServiceEndpoint{URL: "http://127.0.0.1:9"}) {
		args = append(args, "-e", v)
	}
	container := docker(t, append(args, image)...)
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "-f", container).Run() })
	address := docker(t, "port", container, "8080/tcp")
	if i := strings.Index(address, "\n"); i >= 0 {
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
	s.expect(t, http.MethodGet, "/api/me/cart", http.StatusUnauthorized)
	s.expect(t, http.MethodGet, "/readyz", http.StatusServiceUnavailable, `"unavailable":["ts-db"]`)
	if user := docker(t, "exec", container, "id", "-un"); user != "bun" {
		t.Errorf("the server runs as %s, want the non-root user bun", user)
	}
	docker(t, "stop", container)
	if code := docker(t, "inspect", "-f", "{{.State.ExitCode}}", container); code != "0" {
		t.Errorf("the container exited %s on docker stop:\n%s", code, docker(t, "logs", container))
	}
	if logs := docker(t, "logs", container); !strings.Contains(logs, `"msg":"stopped"`) {
		t.Errorf("the container did not log its graceful stop:\n%s", logs)
	}
}

// TestTypeScriptRoutesAcrossAPIs: two APIs a TypeScript server serves
// cannot register one method and path, path parameters matched whatever
// their names, a manually registered operation included, which a
// TypeScript router mounts too.
func TestTypeScriptRoutesAcrossAPIs(t *testing.T) {
	api := func(name, path string) servergen.TypeScriptAPIInput {
		return servergen.TypeScriptAPIInput{
			Output: &tsrestgen.APIOutput{SchemaName: name, PackageName: "@schemas/" + name + "-api"},
			Routes: &apigen.APIOutput{SchemaName: name, Endpoints: []apigen.EndpointInfo{{Method: http.MethodPost, Path: path, ManualRouteRegistration: true}}},
		}
	}
	_, err := servergen.PlanTypeScript(servergen.TypeScriptInput{
		Stack:  "hooks-stack",
		Server: "Hooks",
		Dir:    t.TempDir(),
		APIs:   []servergen.TypeScriptAPIInput{api("billing", "/api/hooks/{provider}"), api("shipping", "/api/hooks/{carrier}")},
	})
	if err == nil || !strings.Contains(err.Error(), "billing registers POST /api/hooks/{provider} and shipping registers POST /api/hooks/{carrier}") {
		t.Fatalf("PlanTypeScript = %v, want the manual routes refused", err)
	}
}
