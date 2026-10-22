// Command agentcatalog writes the catalogs the superschematic agent skill
// (skills/superschematic) reads, and the docs' scalar reference, from the
// sources the compiler itself reads, so neither restates a fact by hand:
//
//	skills/superschematic/references/scalars.md     every builtin scalar, from the pinned superscalar Go package
//	skills/superschematic/references/decorators.md  every core decorator and type wrapper, from the registry and packages/
//	skills/superschematic/references/behaviors.md   every core engine behavior, from the registry and its config types
//	skills/superschematic/references/capabilities.md the docs' capability map, with links to the docs site
//	docs/src/content/docs/reference/scalars.md      the scalar catalog as a docs page
//
// A decorator's entry joins two sources. The registry says where it may sit,
// which kinds may use it and the JSON Schema its argument is checked
// against; the authoring package's TypeScript declaration says how it is
// written, with the declaration's comment and the exported types its
// signature names. A registered decorator its packages do not declare fails
// the run, so the catalog cannot list a decorator a schema cannot import.
//
// Run from the repository root: go run ./internal/tools/agentcatalog
// With -check the command exits 1 when a committed file differs from what it
// would write.
package main

import (
	"bytes"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

const (
	skillScalarsPath      = "skills/superschematic/references/scalars.md"
	skillDecoratorsPath   = "skills/superschematic/references/decorators.md"
	skillBehaviorsPath    = "skills/superschematic/references/behaviors.md"
	skillCapabilitiesPath = "skills/superschematic/references/capabilities.md"
	docsCapabilitiesPath  = "docs/src/content/docs/start/capabilities.md"
	docsScalarsPath       = "docs/src/content/docs/reference/scalars.md"
)

// packageDirs maps each core authoring package to the directory holding its
// TypeScript sources.
var packageDirs = map[string]string{
	"@superschematic/schema":        "packages/schema/src",
	"@superschematic/db":            "packages/db/src",
	"@superschematic/api":           "packages/api/src",
	"@superschematic/schema-config": "packages/schema-config/src",
	"@superschematic/stack":         "packages/stack/src",
}

// schemaPackage declares @behavior and the config type of each core
// behavior (BehaviorConfigs).
const schemaPackage = "@superschematic/schema"

// packageOrder is the order the decorator catalog lists the packages in:
// the one every kind imports first, then by kind.
var packageOrder = []string{
	"@superschematic/schema",
	"@superschematic/db",
	"@superschematic/api",
	"@superschematic/schema-config",
	"@superschematic/stack",
}

// wrapperFiles are the files whose exports are type wrappers and base
// classes, which the registry does not know: the loader reads them from a
// field's type or a class's heritage.
var wrapperFiles = map[string][]string{
	"@superschematic/schema": {"wrappers.ts"},
	"@superschematic/db":     {"wrappers.ts"},
	"@superschematic/api":    {"wrappers.ts", "bases.ts"},
}

func main() {
	check := flag.Bool("check", false, "exit 1 when a committed catalog differs from the generated one")
	flag.Parse()

	root, err := os.Getwd()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	outputs, err := render(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	paths := make([]string, 0, len(outputs))
	for rel := range outputs {
		paths = append(paths, rel)
	}
	sort.Strings(paths)
	failed := false
	for _, rel := range paths {
		want := outputs[rel]
		path := filepath.Join(root, rel)
		if *check {
			have, err := os.ReadFile(path)
			if err != nil || !bytes.Equal(have, want) {
				fmt.Fprintf(os.Stderr, "%s is out of date; run: go run ./internal/tools/agentcatalog\n", rel)
				failed = true
			}
			continue
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
		if err := os.WriteFile(path, want, 0o644); err != nil {
			fmt.Fprintln(os.Stderr, err)
			os.Exit(1)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// render returns every catalog file, keyed by its path from the repository
// root.
func render(root string) (map[string][]byte, error) {
	decls := map[string][]export{}
	for pkg, dir := range packageDirs {
		exports, err := readPackage(filepath.Join(root, dir))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pkg, err)
		}
		decls[pkg] = exports
	}
	reg := generator.CoreRegistry(naming.Naming{})
	decorators, err := renderDecorators(decls, reg)
	if err != nil {
		return nil, err
	}
	behaviors, err := renderBehaviors(decls[schemaPackage], reg)
	if err != nil {
		return nil, err
	}
	capabilities, err := renderCapabilities(root)
	if err != nil {
		return nil, err
	}
	scalarBody := renderScalarBody()
	return map[string][]byte{
		skillCapabilitiesPath: capabilities,
		skillScalarsPath:      []byte(generatedHeader("<!--", " -->") + "# Scalars\n\n" + scalarBody),
		skillDecoratorsPath:   decorators,
		skillBehaviorsPath:    behaviors,
		docsScalarsPath:       []byte(docsScalarsFrontmatter + generatedHeader("<!--", " -->") + scalarBody),
	}, nil
}

// docsSite is where the docs are published; a docs page links another
// under its base, /superschematic/.
const docsSite = "https://parable-work.github.io"

// renderCapabilities is the docs' capability map as the skill reads it:
// its title as a heading in place of the frontmatter, and each link into
// the docs made absolute, so it resolves outside the site.
func renderCapabilities(root string) ([]byte, error) {
	src, err := os.ReadFile(filepath.Join(root, docsCapabilitiesPath))
	if err != nil {
		return nil, err
	}
	rest, ok := strings.CutPrefix(string(src), "---\n")
	if !ok {
		return nil, fmt.Errorf("%s: no frontmatter", docsCapabilitiesPath)
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return nil, fmt.Errorf("%s: unterminated frontmatter", docsCapabilitiesPath)
	}
	title := ""
	for _, line := range strings.Split(front, "\n") {
		if t, ok := strings.CutPrefix(line, "title: "); ok {
			title = t
		}
	}
	if title == "" {
		return nil, fmt.Errorf("%s: no title in the frontmatter", docsCapabilitiesPath)
	}
	body = strings.ReplaceAll(body, "](/superschematic/", "]("+docsSite+"/superschematic/")
	header := "<!-- @generated by go run ./internal/tools/agentcatalog from " + docsCapabilitiesPath + "; edit that page -->\n\n"
	return []byte(header + "# " + title + "\n" + body), nil
}

func generatedHeader(open, close string) string {
	return open + " @generated by go run ./internal/tools/agentcatalog; do not edit" + close + "\n\n"
}

const docsScalarsFrontmatter = `---
title: Scalars
description: Every builtin scalar a field can have, with what it checks and its type in SQL and each language.
sidebar:
  order: 0
---

`

// renderScalarBody is the scalar catalog below its title: one table per
// namespace, one row per scalar.
func renderScalarBody() string {
	names := make([]string, 0, len(scalars.ScalarMetadataByCanonical))
	for name := range scalars.ScalarMetadataByCanonical {
		names = append(names, name)
	}
	sort.Strings(names)

	var b strings.Builder
	b.WriteString("A field's type is a scalar from superscalar, imported in a schema file as\n")
	b.WriteString("`import { Contact, Generic, Identity } from \"superscalar\"` and written\n")
	b.WriteString("`Contact.Email`. Every generated decoder, in Go, TypeScript, Python and\n")
	b.WriteString("Rust, parses and checks the value the same way, and a DB column gets the\n")
	b.WriteString("SQL type below. Pick the narrowest scalar that fits before reaching for\n")
	b.WriteString("`Validate<T, { ... }>`: the scalar's checks come with it everywhere.\n\n")
	b.WriteString("Generated code names the scalar without its dot: `ContactEmail` in Go\n")
	b.WriteString("and TypeScript, over the base type in the table. The base types are\n")
	b.WriteString("Go / TypeScript / Python / Rust. *Normalizes* means the value is\n")
	b.WriteString("rewritten on parse (trimmed, lowercased, canonicalized) before it is\n")
	b.WriteString("checked and stored.\n")

	namespace := ""
	for _, name := range names {
		meta := scalars.ScalarMetadataByCanonical[name]
		ns, _, _ := strings.Cut(name, ".")
		if ns != namespace {
			namespace = ns
			fmt.Fprintf(&b, "\n## %s\n\n", ns)
			b.WriteString("| Scalar | What it holds | SQL | Base types | Checks | Example |\n")
			b.WriteString("| --- | --- | --- | --- | --- | --- |\n")
		}
		example := ""
		if len(meta.Examples) > 0 {
			example = code(meta.Examples[0])
		}
		fmt.Fprintf(&b, "| `%s` | %s | %s | %s | %s | %s |\n",
			name,
			cell(meta.Description),
			code(meta.SQLType),
			cell(strings.Join([]string{meta.GoType, meta.TypeScriptType, meta.PythonType, meta.RustType}, " / ")),
			scalarChecks(meta),
			example,
		)
	}
	return b.String()
}

// scalarChecks lists what a scalar's decoders check beyond its base type.
func scalarChecks(meta *scalars.ScalarMetadata) string {
	var checks []string
	switch {
	case meta.MinLength > 0 && meta.MaxLength > 0:
		checks = append(checks, fmt.Sprintf("length %d to %d", meta.MinLength, meta.MaxLength))
	case meta.MaxLength > 0:
		checks = append(checks, fmt.Sprintf("at most %d characters", meta.MaxLength))
	case meta.MinLength > 0:
		checks = append(checks, fmt.Sprintf("at least %d characters", meta.MinLength))
	}
	switch {
	case meta.Minimum != nil && meta.Maximum != nil:
		checks = append(checks, fmt.Sprintf("%d to %d", *meta.Minimum, *meta.Maximum))
	case meta.Minimum != nil:
		checks = append(checks, fmt.Sprintf("at least %d", *meta.Minimum))
	case meta.Maximum != nil:
		checks = append(checks, fmt.Sprintf("at most %d", *meta.Maximum))
	}
	if meta.Format != "" {
		checks = append(checks, "format "+code(meta.Format))
	}
	if meta.Pattern != "" {
		checks = append(checks, "pattern "+code(meta.Pattern))
	}
	if meta.HasCustomValidate {
		checks = append(checks, "its own validator")
	}
	if meta.CaseInsensitive {
		checks = append(checks, "case-insensitive")
	}
	if meta.HasCustomNormalize {
		checks = append(checks, "normalizes")
	}
	if len(meta.ReservedWords) > 0 {
		words := make([]string, len(meta.ReservedWords))
		for i, w := range meta.ReservedWords {
			words[i] = code(w)
		}
		checks = append(checks, "refuses "+strings.Join(words, ", "))
	}
	return strings.Join(checks, "; ")
}

// code is s as a Markdown code span inside a table cell: a pipe is
// escaped, which GitHub's tables and Starlight both read inside a span,
// and a backtick widens the fence.
func code(s string) string {
	if s == "" {
		return ""
	}
	s = strings.ReplaceAll(s, "|", `\|`)
	if strings.Contains(s, "`") {
		return "`` " + s + " ``"
	}
	return "`" + s + "`"
}

// cell is s as plain text inside a table cell.
func cell(s string) string {
	s = strings.ReplaceAll(s, "|", `\|`)
	return strings.Join(strings.Fields(s), " ")
}

// renderDecorators is the decorator catalog: per core authoring package,
// its decorators as the registry knows them beside their declarations,
// then its type wrappers, then the exported types those name. The
// behaviors' config types are left to the behavior catalog.
func renderDecorators(decls map[string][]export, reg *registry.Registry) ([]byte, error) {

	type entry struct {
		name  string
		specs []registry.DecoratorSpec
	}
	byPackage := map[string][]*entry{}
	index := map[string]*entry{}
	for _, spec := range reg.Decorators() {
		if spec.Extension != "" {
			continue
		}
		if len(spec.Packages) == 0 {
			return nil, fmt.Errorf("decorator @%s names no authoring package", spec.Name)
		}
		home := spec.Packages[0]
		key := home + " " + spec.Name
		e := index[key]
		if e == nil {
			e = &entry{name: spec.Name}
			index[key] = e
			byPackage[home] = append(byPackage[home], e)
		}
		e.specs = append(e.specs, spec)
	}

	var b bytes.Buffer
	b.WriteString(generatedHeader("<!--", " -->"))
	b.WriteString("# Decorators and type wrappers\n\n")
	b.WriteString("Every decorator the core registers, grouped by the authoring package a\n")
	b.WriteString("schema file imports it from, and the type wrappers beside them. An\n")
	b.WriteString("extension's decorators come from its own package.\n\n")
	b.WriteString("Each entry gives the TypeScript declaration `tsc` checks a schema file\n")
	b.WriteString("against, its comment, the nodes it may sit on (a type is a class with\n")
	b.WriteString("fields, an operation set a class with methods, an operation a method)\n")
	b.WriteString("and the kinds it is limited to when it is. The JSON and YAML forms write\n")
	b.WriteString("a decorator's argument as a JSON value; `superschematic json-schema`\n")
	b.WriteString("prints the schema the loader checks it against, for every decorator the\n")
	b.WriteString("binary you run knows, an extension's too.\n\n")
	b.WriteString("## Which kinds may import a package\n\n")
	b.WriteString("Operation sets and operations exist only in API schemas. Every kind may\n")
	b.WriteString("import `superscalar` and the packages not listed here.\n\n")
	b.WriteString("| Package | Kinds that may import it |\n| --- | --- |\n")
	for _, pkg := range packageOrder {
		var kinds []string
		for _, kind := range reg.Kinds() {
			spec, _ := reg.Kind(kind)
			if !spec.ForbiddenPackages[pkg] && reg.PackageAllowsKind(pkg, kind) {
				kinds = append(kinds, kind)
			}
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", pkg, strings.Join(kinds, ", "))
	}

	for _, pkg := range packageOrder {
		entries := byPackage[pkg]
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		fmt.Fprintf(&b, "\n## `%s`\n", pkg)
		refs, printed := map[string]bool{}, map[string]bool{}
		if pkg == schemaPackage {
			printed = closure(decls[pkg], []string{behaviorConfigs})
		}
		for _, e := range entries {
			found := declarationsOf(decls, e.specs[0].Packages, e.name)
			if len(found) == 0 {
				return nil, fmt.Errorf("decorator @%s: no export named %s in %s", e.name, e.name, strings.Join(e.specs[0].Packages, ", "))
			}
			fmt.Fprintf(&b, "\n### `@%s`\n\n", e.name)
			b.WriteString("```ts\n")
			for _, d := range found {
				b.WriteString(d.signature() + "\n")
				for _, name := range d.names() {
					refs[name] = true
				}
			}
			b.WriteString("```\n\n")
			seen := map[string]bool{}
			for _, d := range found {
				if d.comment != "" && !seen[d.comment] {
					seen[d.comment] = true
					b.WriteString(d.comment + "\n\n")
				}
			}
			// One line per kind restriction, naming every target it holds on.
			var limits []string
			targets := map[string][]string{}
			for _, spec := range e.specs {
				limit := strings.Join(spec.Kinds, " or ")
				if _, ok := targets[limit]; !ok {
					limits = append(limits, limit)
				}
				targets[limit] = append(targets[limit], spec.Target.String())
			}
			for _, limit := range limits {
				fmt.Fprintf(&b, "- On %s", strings.Join(targets[limit], " or "))
				if limit != "" {
					fmt.Fprintf(&b, ", only in %s schemas", limit)
				}
				b.WriteString(".\n")
			}
			if pkg == schemaPackage && e.name == "behavior" {
				b.WriteString("- Each behavior, its config and what it adds to a type: [behaviors.md](behaviors.md).\n")
			}
			if len(e.specs[0].Packages) > 1 {
				fmt.Fprintf(&b, "- Also exported by %s.\n", codeList(e.specs[0].Packages[1:]))
			}
		}

		var wrappers []export
		for _, file := range wrapperFiles[pkg] {
			for _, d := range decls[pkg] {
				if d.file == file && (d.kind == "type" || strings.HasSuffix(d.kind, "class")) {
					wrappers = append(wrappers, d)
				}
			}
		}
		if len(wrappers) > 0 {
			b.WriteString("\n### Type wrappers and bases\n")
			for _, d := range wrappers {
				fmt.Fprintf(&b, "\n```ts\n%s\n```\n", d.signature())
				if d.comment != "" {
					b.WriteString("\n" + d.comment + "\n")
				}
				printed[d.name] = true
				for _, name := range d.names() {
					refs[name] = true
				}
			}
		}

		types := namedTypes(decls[pkg], refs, printed)
		if len(types) > 0 {
			b.WriteString("\n### Types the declarations name\n")
			for _, d := range types {
				fmt.Fprintf(&b, "\n```ts\n%s\n```\n", d.signature())
				if d.comment != "" {
					b.WriteString("\n" + d.comment + "\n")
				}
			}
		}
	}
	return b.Bytes(), nil
}

// declarationsOf is the declarations of name in the first of packages that
// exports it: its overloads when it has any, else its one declaration.
func declarationsOf(decls map[string][]export, packages []string, name string) []export {
	for _, pkg := range packages {
		var all, overloads []export
		for _, d := range decls[pkg] {
			if d.name != name || (d.kind != "function" && d.kind != "const") {
				continue
			}
			all = append(all, d)
			if d.overload {
				overloads = append(overloads, d)
			}
		}
		if len(overloads) > 0 {
			return overloads
		}
		if len(all) > 0 {
			return all[:1]
		}
	}
	return nil
}

// namedTypes is the package's exported types, interfaces and enums that
// refs names, and those their declarations name in turn, in file order,
// leaving out those printed holds.
func namedTypes(exports []export, refs, printed map[string]bool) []export {
	roots := make([]string, 0, len(refs))
	for name := range refs {
		roots = append(roots, name)
	}
	include := closure(exports, roots)
	var out []export
	for _, d := range typeDecls(exports) {
		if include[d.name] && !printed[d.name] {
			out = append(out, d)
		}
	}
	return out
}

// typeDecls is the package's exported types, interfaces and enums, in file
// order, the last declaration of a name standing for it.
func typeDecls(exports []export) []export {
	last := map[string]int{}
	for i, d := range exports {
		if d.kind == "type" || d.kind == "interface" || d.kind == "enum" {
			last[d.name] = i
		}
	}
	var out []export
	for i, d := range exports {
		if j, ok := last[d.name]; ok && j == i {
			out = append(out, d)
		}
	}
	return out
}

// closure is the names of the package's types, interfaces and enums that
// roots names, and those their declarations name in turn.
func closure(exports []export, roots []string) map[string]bool {
	byName := map[string]export{}
	for _, d := range typeDecls(exports) {
		byName[d.name] = d
	}
	include := map[string]bool{}
	var visit func(name string)
	visit = func(name string) {
		d, ok := byName[name]
		if !ok || include[name] {
			return
		}
		include[name] = true
		for _, n := range d.names() {
			visit(n)
		}
	}
	for _, name := range roots {
		visit(name)
	}
	return include
}

func codeList(items []string) string {
	quoted := make([]string, len(items))
	for i, s := range items {
		quoted[i] = "`" + s + "`"
	}
	return strings.Join(quoted, ", ")
}
