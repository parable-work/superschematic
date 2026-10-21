package main

import (
	"bytes"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/cli"
)

// initFixture is stackinit's tree with no stack: two APIs, one of them
// internal, their database and a site.
const initFixture = "../../internal/stackinit/testdata/tree"

// wishlistServices are acme-shop's wishlist-stack and the services only it
// deploys.
var wishlistServices = []string{"shop-wishlist", "shop-wishlist-db", "wishlist-stack"}

// copyTree copies the directory src to dst.
func copyTree(t *testing.T, src, dst string) {
	t.Helper()
	err := filepath.WalkDir(src, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(src, path)
		if err != nil {
			return err
		}
		target := filepath.Join(dst, rel)
		if entry.IsDir() {
			return os.MkdirAll(target, 0o755)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		return os.WriteFile(target, data, 0o644)
	})
	if err != nil {
		t.Fatal(err)
	}
}

// run runs the installed binary's command line with args.
func run(t *testing.T, args ...string) string {
	t.Helper()
	buf := new(bytes.Buffer)
	root := cli.New(cli.Config{Name: "superschematic"}, extensions()...)
	root.SetOut(buf)
	root.SetErr(buf)
	root.SetIn(strings.NewReader(""))
	root.SetArgs(args)
	if err := root.Execute(); err != nil {
		t.Fatalf("superschematic %s: %v\n%s", strings.Join(args, " "), err, buf.String())
	}
	return buf.String()
}

// TestStackInitWritesAGcpEnvironment: the installed binary links the gcp
// target, so stack init writes a gcp environment from a project and a
// region, with the types of gcp's values and settings for tsc, read from
// the schemas the target registers. The stack loads and resolves, and the
// stack generator writes both environments.
func TestStackInitWritesAGcpEnvironment(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	project := t.TempDir()
	schemasRoot := filepath.Join(project, "schemas")
	copyTree(t, filepath.Join(initFixture, "services"), filepath.Join(schemasRoot, "services"))
	base, err := os.ReadFile(filepath.Join(initFixture, "tsconfig.base.json"))
	if err != nil {
		t.Fatal(err)
	}
	base = []byte(strings.ReplaceAll(string(base), "../../../../", filepath.ToSlash(repoRoot)+"/"))
	if err := os.WriteFile(filepath.Join(schemasRoot, "tsconfig.base.json"), base, 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(project)

	out := run(t, "stack", "init", "--gcp-project", "notes-staging", "--region", "us-east1")
	for _, want := range []string{
		"  src/gcp-target.ts\n",
		"  Staging on gcp, project notes-staging in us-east1\n",
		"\nNext:\n  superschematic stack dev --environment Dev\n  superschematic stack bootstrap Staging\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("stack init printed no %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "does not resolve") {
		t.Errorf("the stack does not resolve:\n%s", out)
	}

	dir := filepath.Join(schemasRoot, "services", "notes-stack")
	schema, err := os.ReadFile(filepath.Join(dir, "src", "stack.schema.ts"))
	if err != nil {
		t.Fatal(err)
	}
	if want := `@environment({ target: "gcp", gcp: { project: "notes-staging", region: "us-east1" } })` + "\nexport abstract class Staging {}\n"; !strings.Contains(string(schema), want) {
		t.Errorf("the stack declares no %q:\n%s", want, schema)
	}
	// The stack loaded with tsc's checks, so the file types target "gcp";
	// its lines follow the schemas gcp registers, which change with it.
	types, err := os.ReadFile(filepath.Join(dir, "src", "gcp-target.ts"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"declare module \"@superschematic/stack\" {\n  interface Targets {\n    gcp: {\n",
		"      values: { project: string; projectNumber?: string; region: string; production?: boolean };\n",
		"      server: { minInstances?: number; ",
		"      database: { tier?: string; highAvailability?: boolean; version?: \"POSTGRES_15\" | ",
	} {
		if !strings.Contains(string(types), want) {
			t.Errorf("src/gcp-target.ts holds no %q:\n%s", want, types)
		}
	}

	outputRoot := t.TempDir()
	run(t, "build", dir, "--out", outputRoot)
	for _, env := range []string{"Dev", "Staging"} {
		if _, err := os.Stat(filepath.Join(outputRoot, "stack", "notes-stack", env, "environment.json")); err != nil {
			t.Errorf("the stack generator wrote no %s: %v", env, err)
		}
	}
}

// TestStackInitOverAcmeShop: over a copy of acme-shop's schemas, beside the
// shop-stack written by hand, init writes a second stack that resolves in
// Dev and on gcp, and deploys and exposes what shop-stack does: every API
// and the site, each API an end user calls. When acme's stack changes what
// it deploys or exposes on purpose, change the line this compares.
//
// The copy leaves out wishlistServices, which wishlist-stack deploys on
// Cloudflare and shop-stack does not: init deploys every API of the tree,
// and gcp cannot place shop-wishlist, a TypeScript server over a database
// that lists sqlite first (D64).
func TestStackInitOverAcmeShop(t *testing.T) {
	repoRoot, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	acme := filepath.Join(repoRoot, "examples", "acme-shop", "schemas")
	project := t.TempDir()
	schemasRoot := filepath.Join(project, "schemas")
	services, err := os.ReadDir(filepath.Join(acme, "services"))
	if err != nil {
		t.Fatal(err)
	}
	for _, svc := range services {
		if !svc.IsDir() || slices.Contains(wishlistServices, svc.Name()) {
			continue
		}
		copyTree(t, filepath.Join(acme, "services", svc.Name()), filepath.Join(schemasRoot, "services", svc.Name()))
	}
	for _, name := range []string{"superschematic.toml", "tsconfig.base.json"} {
		data, err := os.ReadFile(filepath.Join(acme, name))
		if err != nil {
			t.Fatal(err)
		}
		data = []byte(strings.ReplaceAll(string(data), `"../../../`, `"`+filepath.ToSlash(repoRoot)+"/"))
		if err := os.WriteFile(filepath.Join(schemasRoot, name), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	t.Chdir(project)

	out := run(t, "stack", "init", "--name", "shop-stack-init", "--force", "--gcp-project", "acme-staging", "--region", "us-east1")
	if strings.Contains(out, "does not resolve") {
		t.Errorf("the stack does not resolve:\n%s", out)
	}
	if want := "\nNext:\n  superschematic stack dev ./schemas/services/shop-stack-init --environment Dev\n  superschematic stack bootstrap Staging --stack ./schemas/services/shop-stack-init\n"; !strings.HasSuffix(out, want) {
		t.Errorf("stack init printed no %q:\n%s", want, out)
	}
	// stackDecl is a file's @stack declaration, from `@stack(` to the class
	// it decorates, its whitespace collapsed, so one a formatter wraps over
	// several lines compares equal to one on a single line.
	stackDecl := func(path string) string {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		text := string(data)
		start := strings.Index(text, "\n@stack(")
		if start < 0 {
			t.Fatalf("%s declares no @stack", path)
		}
		end := strings.Index(text[start:], "\nexport ")
		if end < 0 {
			t.Fatalf("%s decorates no class with its @stack", path)
		}
		return strings.Join(strings.Fields(text[start:start+end]), " ")
	}
	written := stackDecl(filepath.Join(schemasRoot, "services", "shop-stack-init", "src", "stack.schema.ts"))
	byHand := stackDecl(filepath.Join(acme, "services", "shop-stack", "src", "stack.schema.ts"))
	if written != byHand {
		t.Errorf("stack init wrote\n  %s\nwhere acme's shop-stack declares\n  %s", written, byHand)
	}
}
