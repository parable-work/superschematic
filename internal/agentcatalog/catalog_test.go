package agentcatalog

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/docs"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/registry/registrytest"
	"github.com/parable-work/superschematic/packages"
)

func sources() Sources {
	return Sources{Packages: packages.Sources, CapabilityMap: docs.CapabilityMap}
}

// withAcme is the core registry with the acme test extension linked, as a
// binary that carries it assembles one.
func withAcme(t *testing.T) *registry.Registry {
	t.Helper()
	reg := registry.New(naming.Naming{})
	if err := generator.RegisterCore(reg); err != nil {
		t.Fatal(err)
	}
	if err := reg.Use(registrytest.Acme{}); err != nil {
		t.Fatal(err)
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
	return reg
}

func render(t *testing.T, name string, reg *registry.Registry) string {
	t.Helper()
	out, err := Render(name, reg, sources())
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// TestEveryDecoratorIsListed holds the decorator catalog to the registry:
// each core decorator has an entry with its TypeScript declaration, and an
// extension's has one with its argument's schema.
func TestEveryDecoratorIsListed(t *testing.T) {
	reg := generator.CoreRegistry(naming.Naming{})
	catalog := render(t, Decorators, reg)
	for _, spec := range reg.Decorators() {
		entry := "\n### `@" + spec.Name + "`\n\n```ts\n"
		if !strings.Contains(catalog, entry) {
			t.Errorf("decorator @%s has no entry with a declaration", spec.Name)
		}
	}

	acme := withAcme(t)
	catalog = render(t, Decorators, acme)
	for _, want := range []string{
		"\n## `" + registrytest.Package + "`\n",
		"\n### `@shelf`\n\nRegistered by the `acme` extension.\n\n- On a field, only in Catalog schemas.\n- Argument on a field: `{",
	} {
		if !strings.Contains(catalog, want) {
			t.Errorf("acme's decorator catalog lacks %q", want)
		}
	}
}

func TestEveryBehaviorIsListed(t *testing.T) {
	acme := withAcme(t)
	catalog := render(t, Behaviors, acme)
	for _, beh := range acme.Behaviors() {
		if !strings.Contains(catalog, "\n## `"+beh.Name+"`\n") {
			t.Errorf("behavior %s has no entry", beh.Name)
		}
	}
	if !strings.Contains(catalog, "- Config: `WorkflowConfig`, required.\n") {
		t.Error("Workflow's entry does not name its config type")
	}
	if !strings.Contains(catalog, "\n```ts\ninterface WorkflowConfig {") {
		t.Error("the config types do not include WorkflowConfig")
	}
}

func TestScalarsAndCapabilities(t *testing.T) {
	reg := generator.CoreRegistry(naming.Naming{})
	scalars := render(t, Scalars, reg)
	for _, name := range reg.Scalars().Names() {
		if !strings.Contains(scalars, "| `"+name+"` |") {
			t.Errorf("scalar %s has no row", name)
		}
	}
	capabilities := render(t, Capabilities, reg)
	if !strings.HasPrefix(capabilities, "# Capability map\n") {
		t.Errorf("capability map starts %q", capabilities[:40])
	}
	if strings.Contains(capabilities, "](/superschematic/") {
		t.Error("capability map keeps a site-relative link")
	}
	if _, err := Render("nope", reg, sources()); err == nil || !strings.Contains(err.Error(), "the catalogs are scalars, decorators, behaviors, capabilities") {
		t.Errorf("unknown catalog: %v", err)
	}
}

func TestDeclarationEnd(t *testing.T) {
	src := strings.Join([]string{
		"/** A route. */",
		"export function rest(_method: HttpMethod, _path?: string): MethodDecorator {",
		"  return noop;",
		"}",
		"export function versioned<T extends Function>(target: T): void;",
		"export function versioned(_options?: { readonly days?: number }): ClassDecorator;",
		"// A key.",
		"export const key: PropertyDecorator = noop;",
		"export type Pair<T> = { readonly a: T; readonly b: (x: T) => string };",
		"export interface Options {",
		"  /** It's a name; the apostrophe is in a comment. */",
		"  readonly name?: string;",
		"}",
	}, "\n") + "\n"
	exports, err := parseFile("x.ts", src)
	if err != nil {
		t.Fatal(err)
	}
	want := []struct{ name, signature, comment string }{
		{"rest", "function rest(method: HttpMethod, path?: string): MethodDecorator", "A route."},
		{"versioned", "function versioned<T extends Function>(target: T): void", ""},
		{"versioned", "function versioned(options?: { readonly days?: number }): ClassDecorator", ""},
		{"key", "const key: PropertyDecorator", "A key."},
		{"Pair", "type Pair<T> = { readonly a: T; readonly b: (x: T) => string }", ""},
		{"Options", "interface Options {\n  /** It's a name; the apostrophe is in a comment. */\n  readonly name?: string;\n}", ""},
	}
	if len(exports) != len(want) {
		t.Fatalf("got %d exports, want %d: %+v", len(exports), len(want), exports)
	}
	for i, w := range want {
		d := exports[i]
		if d.name != w.name || d.signature() != w.signature || d.comment != w.comment {
			t.Errorf("export %d = %q %q %q, want %q %q %q", i, d.name, d.signature(), d.comment, w.name, w.signature, w.comment)
		}
	}
	if exports[0].overload || !exports[1].overload {
		t.Errorf("overload flags: rest %v, versioned %v", exports[0].overload, exports[1].overload)
	}
}
