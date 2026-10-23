// Package agentcatalog writes Markdown references of what a superschematic
// binary knows, for a reader that authors schemas: a person, or a coding
// agent with no copy of this repository. `superschematic catalog` prints
// them, so each matches the binary that prints it, the extensions it links
// included. Nothing is restated by hand:
//
//   - scalars: every scalar of the registry's catalog, from superscalar's
//     metadata (and an extension's, when one replaces the catalog).
//   - decorators: every registered decorator, joined with its TypeScript
//     declaration in the core authoring packages, and the type wrappers.
//   - behaviors: every registered engine behavior, from its declaration,
//     with the core's config types.
//   - capabilities: the docs' capability map, with links to the docs site.
//
// A decorator's entry joins two sources. The registry says where it may
// sit, which kinds may use it and the JSON Schema its argument is checked
// against; the authoring package's TypeScript declaration says how it is
// written, with the declaration's comment and the exported types its
// signature names. A core decorator its packages do not declare is an
// error, so a reference cannot list a decorator a schema cannot import. An
// extension's decorators have no declaration here; their entries give the
// argument's JSON Schema instead.
package agentcatalog

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io/fs"
	"sort"
	"strings"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/internal/registry"
)

// The names `superschematic catalog` takes, in the order its help lists
// them.
const (
	Scalars      = "scalars"
	Decorators   = "decorators"
	Behaviors    = "behaviors"
	Capabilities = "capabilities"
)

// Names lists every catalog.
func Names() []string { return []string{Scalars, Decorators, Behaviors, Capabilities} }

// Sources are the files a catalog reads besides the registry.
type Sources struct {
	// Packages holds the core authoring packages' sources as packages/
	// does: api/src/*.ts, db/src/*.ts and the others.
	Packages fs.FS
	// CapabilityMap is the docs' capability map page, frontmatter and all.
	CapabilityMap []byte
}

// Render returns the named catalog as Markdown.
func Render(name string, reg *registry.Registry, src Sources) ([]byte, error) {
	switch name {
	case Scalars:
		return []byte("# Scalars\n\n" + ScalarBody(reg)), nil
	case Decorators, Behaviors:
		decls, err := readPackages(src.Packages)
		if err != nil {
			return nil, err
		}
		if name == Decorators {
			return renderDecorators(decls, reg)
		}
		return renderBehaviors(decls[schemaPackage], reg)
	case Capabilities:
		return renderCapabilities(src.CapabilityMap)
	}
	return nil, fmt.Errorf("no catalog named %q; the catalogs are %s", name, strings.Join(Names(), ", "))
}

// packageDirs maps each core authoring package to the directory under
// Sources.Packages holding its TypeScript sources.
var packageDirs = map[string]string{
	"@superschematic/schema":        "schema/src",
	"@superschematic/db":            "db/src",
	"@superschematic/api":           "api/src",
	"@superschematic/schema-config": "schema-config/src",
	"@superschematic/stack":         "stack/src",
}

// schemaPackage declares @behavior and the config type of each core
// behavior (BehaviorConfigs).
const schemaPackage = "@superschematic/schema"

// packageOrder is the order the decorator catalog lists the core packages
// in: the one every kind imports first, then by kind. An extension's
// packages follow, sorted.
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

func readPackages(fsys fs.FS) (map[string][]export, error) {
	if fsys == nil {
		return nil, fmt.Errorf("no authoring package sources to read declarations from")
	}
	decls := map[string][]export{}
	for pkg, dir := range packageDirs {
		exports, err := readPackage(fsys, dir)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", pkg, err)
		}
		decls[pkg] = exports
	}
	return decls, nil
}

// docsSite is where the docs are published; a docs page links another
// under its base, /superschematic/.
const docsSite = "https://parable-work.github.io"

// renderCapabilities is the docs' capability map outside the site: its
// title as a heading in place of the frontmatter, and each link into the
// docs made absolute.
func renderCapabilities(page []byte) ([]byte, error) {
	rest, ok := strings.CutPrefix(string(page), "---\n")
	if !ok {
		return nil, fmt.Errorf("capability map: no frontmatter")
	}
	front, body, ok := strings.Cut(rest, "\n---\n")
	if !ok {
		return nil, fmt.Errorf("capability map: unterminated frontmatter")
	}
	title := ""
	for _, line := range strings.Split(front, "\n") {
		if t, ok := strings.CutPrefix(line, "title: "); ok {
			title = t
		}
	}
	if title == "" {
		return nil, fmt.Errorf("capability map: no title in the frontmatter")
	}
	body = strings.ReplaceAll(body, "](/superschematic/", "]("+docsSite+"/superschematic/")
	return []byte("# " + title + "\n" + body), nil
}

// ScalarBody is the scalar catalog below its title: one table per
// namespace, one row per scalar of the registry's catalog. The docs'
// scalar page is this body under its frontmatter.
func ScalarBody(reg *registry.Registry) string {
	catalog := reg.Scalars()
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
	for _, name := range catalog.Names() {
		meta, ok := catalog.Scalar(name)
		if !ok {
			continue
		}
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
	minLength, maxLength, minimum, maximum := meta.MinLength, meta.MaxLength, meta.Minimum, meta.Maximum
	var checks []string
	switch {
	case minLength > 0 && maxLength > 0:
		checks = append(checks, fmt.Sprintf("length %d to %d", minLength, maxLength))
	case maxLength > 0:
		checks = append(checks, fmt.Sprintf("at most %d characters", maxLength))
	case minLength > 0:
		checks = append(checks, fmt.Sprintf("at least %d characters", minLength))
	}
	switch {
	case minimum != nil && maximum != nil:
		checks = append(checks, fmt.Sprintf("%d to %d", *minimum, *maximum))
	case minimum != nil:
		checks = append(checks, fmt.Sprintf("at least %d", *minimum))
	case maximum != nil:
		checks = append(checks, fmt.Sprintf("at most %d", *maximum))
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

// renderDecorators is the decorator catalog: per authoring package, its
// decorators as the registry knows them beside their declarations, then
// its type wrappers, then the exported types those name. The behaviors'
// config types are left to the behavior catalog.
func renderDecorators(decls map[string][]export, reg *registry.Registry) ([]byte, error) {
	type entry struct {
		name  string
		specs []registry.DecoratorSpec
	}
	byPackage := map[string][]*entry{}
	index := map[string]*entry{}
	var extensionPackages []string
	for _, spec := range reg.Decorators() {
		if len(spec.Packages) == 0 {
			return nil, fmt.Errorf("decorator @%s names no authoring package", spec.Name)
		}
		home := spec.Packages[0]
		key := home + " " + spec.Name
		e := index[key]
		if e == nil {
			e = &entry{name: spec.Name}
			index[key] = e
			if _, core := packageDirs[home]; !core && len(byPackage[home]) == 0 {
				extensionPackages = append(extensionPackages, home)
			}
			byPackage[home] = append(byPackage[home], e)
		}
		e.specs = append(e.specs, spec)
	}
	sort.Strings(extensionPackages)
	packages := append(append([]string{}, packageOrder...), extensionPackages...)

	var b bytes.Buffer
	b.WriteString("# Decorators and type wrappers\n\n")
	b.WriteString("Every decorator this binary registers, grouped by the authoring package\n")
	b.WriteString("a schema file imports it from, and the core's type wrappers beside them.\n\n")
	b.WriteString("Each entry of a core package gives the TypeScript declaration `tsc`\n")
	b.WriteString("checks a schema file against, its comment, the nodes it may sit on (a\n")
	b.WriteString("type is a class with fields, an operation set a class with methods, an\n")
	b.WriteString("operation a method) and the kinds it is limited to when it is. An\n")
	b.WriteString("extension's entry gives the JSON Schema its argument is checked against,\n")
	b.WriteString("as the JSON and YAML forms write it; `superschematic json-schema` prints\n")
	b.WriteString("that schema for every decorator.\n\n")
	b.WriteString("## Which kinds may import a package\n\n")
	b.WriteString("Operation sets and operations exist only in API schemas. Every kind may\n")
	b.WriteString("import `superscalar` and the packages not listed here.\n\n")
	b.WriteString("| Package | Kinds that may import it |\n| --- | --- |\n")
	for _, pkg := range packages {
		var kinds []string
		for _, kind := range reg.Kinds() {
			spec, _ := reg.Kind(kind)
			if !spec.ForbiddenPackages[pkg] && reg.PackageAllowsKind(pkg, kind) {
				kinds = append(kinds, kind)
			}
		}
		fmt.Fprintf(&b, "| `%s` | %s |\n", pkg, strings.Join(kinds, ", "))
	}

	for _, pkg := range packages {
		entries := byPackage[pkg]
		sort.Slice(entries, func(i, j int) bool { return entries[i].name < entries[j].name })
		fmt.Fprintf(&b, "\n## `%s`\n", pkg)
		refs, printed := map[string]bool{}, map[string]bool{}
		if pkg == schemaPackage {
			printed = closure(decls[pkg], []string{behaviorConfigs})
		}
		for _, e := range entries {
			spec := e.specs[0]
			found := declarationsOf(decls, spec.Packages, e.name)
			if len(found) == 0 && spec.Extension == "" {
				return nil, fmt.Errorf("decorator @%s: no export named %s in %s", e.name, e.name, strings.Join(spec.Packages, ", "))
			}
			fmt.Fprintf(&b, "\n### `@%s`\n\n", e.name)
			if len(found) > 0 {
				b.WriteString("```ts\n")
				for _, d := range found {
					b.WriteString(d.signature() + "\n")
					for _, name := range d.names() {
						refs[name] = true
					}
				}
				b.WriteString("```\n\n")
			}
			seen := map[string]bool{}
			for _, d := range found {
				if d.comment != "" && !seen[d.comment] {
					seen[d.comment] = true
					b.WriteString(d.comment + "\n\n")
				}
			}
			if spec.Extension != "" {
				fmt.Fprintf(&b, "Registered by the `%s` extension.\n\n", spec.Extension)
			}
			// One line per kind restriction, naming every target it holds on.
			var limits []string
			targets := map[string][]string{}
			for _, s := range e.specs {
				limit := strings.Join(s.Kinds, " or ")
				if _, ok := targets[limit]; !ok {
					limits = append(limits, limit)
				}
				targets[limit] = append(targets[limit], s.Target.String())
			}
			for _, limit := range limits {
				fmt.Fprintf(&b, "- On %s", strings.Join(targets[limit], " or "))
				if limit != "" {
					fmt.Fprintf(&b, ", only in %s schemas", limit)
				}
				b.WriteString(".\n")
			}
			if len(found) == 0 {
				for _, s := range e.specs {
					if len(s.Args) == 0 {
						continue
					}
					arg, err := compactJSON(s.Args)
					if err != nil {
						return nil, fmt.Errorf("decorator @%s: %w", e.name, err)
					}
					fmt.Fprintf(&b, "- Argument on %s: %s\n", s.Target, arg)
				}
			}
			if pkg == schemaPackage && e.name == "behavior" {
				b.WriteString("- Each behavior, its config and what it adds to a type: `superschematic catalog behaviors`.\n")
			}
			if len(spec.Packages) > 1 {
				fmt.Fprintf(&b, "- Also exported by %s.\n", codeList(spec.Packages[1:]))
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

// compactJSON is raw on one line as a code span, its fence widened when
// the JSON holds a backtick.
func compactJSON(raw json.RawMessage) (string, error) {
	var b bytes.Buffer
	if err := json.Compact(&b, raw); err != nil {
		return "", err
	}
	if strings.Contains(b.String(), "`") {
		return "`` " + b.String() + " ``", nil
	}
	return "`" + b.String() + "`", nil
}
