package main

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// TestCommittedCatalogsAreCurrent holds every committed catalog to what the
// command writes, as the -check run in CI does, so a go test run catches a
// registry, package or superscalar change that did not regenerate them.
func TestCommittedCatalogsAreCurrent(t *testing.T) {
	root := testpaths.RepoRoot(t)
	outputs, err := render(root)
	if err != nil {
		t.Fatal(err)
	}
	for rel, want := range outputs {
		have, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			t.Fatalf("%s: %v; run: go run ./internal/tools/agentcatalog", rel, err)
		}
		if !bytes.Equal(have, want) {
			t.Errorf("%s is out of date; run: go run ./internal/tools/agentcatalog", rel)
		}
	}
}

// TestEveryCoreDecoratorIsListed holds the decorator catalog to the
// registry: each core decorator has an entry.
func TestEveryCoreDecoratorIsListed(t *testing.T) {
	outputs, err := render(testpaths.RepoRoot(t))
	if err != nil {
		t.Fatal(err)
	}
	catalog := string(outputs[skillDecoratorsPath])
	for _, spec := range generator.CoreRegistry(naming.Naming{}).Decorators() {
		if spec.Extension != "" {
			continue
		}
		if !strings.Contains(catalog, "\n### `@"+spec.Name+"`\n") {
			t.Errorf("decorator @%s has no entry in %s", spec.Name, skillDecoratorsPath)
		}
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
