package memorystoregen

import (
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustgen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	ir "github.com/parable-work/superschematic/ir"
)

// The Rust definitions (D75): a crate at types/rust/<store>, named as the
// store's types crate (Naming.RustTypesCrate, shop-cache-types), whose
// src/lib.rs holds a unit struct and a constant per declaration, named
// after its class: `pub const STOCK_CHANGED: StockChanged`. Its `for_`
// takes the parts in order, typed as their schema's Rust types write
// them, formats each part's wire string, and returns the HTTP runtime's
// memorystore::Channel<T> or memorystore::Key; a key whose declaration
// has maxTtlSeconds has `for_ttl(ttl, ...)` instead, which refuses a TTL
// above the maximum. A channel's payload is the type of the dependency's
// Rust types crate, which the crate depends on by path, beside the
// HTTP runtime and, for a scalar part, the scalar crate.
//
// A channel's decoder ignores keys its payload does not declare, at every
// depth. A generated type does so already, unless it, or a type it
// reaches, has @denyUnknownFields: the crate then decodes such a payload
// through a function of its own that removes the keys each type does not
// declare before it decodes.

// RustRuntimeVersion and RustScalarVersion are the versions the crate
// names the HTTP runtime and the scalar crate by when the naming file
// sets no path to their checkouts, as the types crates name theirs.
const (
	RustRuntimeVersion = "0.0.0"
	RustScalarVersion  = "0.1.0"
	rustSerdeJSON      = "1.0.149"
)

// rustWidth is the width rustfmt lays a line out in.
const rustWidth = 100

// GenerateRust writes the store's Rust definitions into in.Dir: a crate
// with a const per declaration whose `for_` takes the parts in order and
// returns the runtime's Channel<T> or Key (D75).
func GenerateRust(in Input) error {
	c, err := newRustCrate(in)
	if err != nil {
		return fmt.Errorf("memory store %s: %w", in.Store, err)
	}
	if err := os.MkdirAll(filepath.Join(in.Dir, "src"), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(in.Dir, "Cargo.toml"), []byte(c.manifest()), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(in.Dir, "src", "lib.rs"), []byte(c.lib()), 0o644)
}

// rustCrate is the crate one store's definitions are.
type rustCrate struct {
	in      Input
	names   naming.Naming
	crate   string
	runtime string

	// scalars are the Rust types of the store schema's scalars.
	scalars map[string]string

	// deps are the dependency types crates the definitions name, by
	// service; usesScalar whether a part is of the scalar crate's type.
	deps       map[string]bool
	usesScalar bool

	decls []rustDecl

	// keeps are the functions that remove a payload's undeclared keys, by
	// type, when a payload reaches a type with @denyUnknownFields.
	keeps []rustKeep
}

type rustDecl struct {
	decl   *ir.MemoryStoreDeclaration
	ident  string
	konst  string
	parts  []rustPart
	result string
	// payload is the payload's Rust path, "" for a signal or a key, and
	// decoder the crate's own decoder for it, "" for serde's.
	payload string
	decoder string
}

type rustPart struct {
	name  string // the part's name in the schema, productId
	ident string // its parameter, product_id
	typ   string // its parameter's type, &superscalar::Uuid
	// let is the statement that makes its wire string, "" for a &str
	// parameter, and arg the element of the name's parts.
	let string
	arg string
}

// rustKeep is a function that removes the keys a type does not declare
// from its JSON, then from each field of an object type it holds.
type rustKeep struct {
	fn     string
	typ    string
	keys   []string
	nested []rustNested
}

type rustNested struct {
	key   string
	depth int
	isMap bool
	keep  string
}

func newRustCrate(in Input) (*rustCrate, error) {
	names := in.Context.Options.Naming.OrDefault()
	c := &rustCrate{
		in:      in,
		names:   names,
		crate:   names.RustTypesCrate(in.Store),
		runtime: crateIdent(names.HTTPRuntimeRustCrate) + "::memorystore",
		scalars: rustgen.ScalarRustTypes(in.Schema, names),
		deps:    map[string]bool{},
	}
	taken := map[string]string{}
	for _, decl := range in.Declarations {
		d := rustDecl{
			decl:  decl,
			ident: rustutil.ToPascalCase(decl.Symbol),
			konst: strings.ToUpper(rustutil.Identifier(decl.Symbol, "declaration")),
		}
		if prev, clash := taken[d.ident]; clash {
			return nil, fmt.Errorf("declarations %s and %s are both %s in Rust", prev, decl.Name, d.ident)
		}
		taken[d.ident] = decl.Name
		params := map[string]bool{"ttl": decl.MaxTTLSeconds > 0, "name": true}
		for _, part := range decl.Parts {
			p, err := c.part(decl, part)
			if err != nil {
				return nil, err
			}
			if params[p.ident] {
				p.ident += "_part"
				p = c.rebind(p)
			}
			params[p.ident] = true
			d.parts = append(d.parts, p)
		}
		switch {
		case decl.Kind == ir.MemoryStoreChannel:
			payload, ok := in.PayloadOf(decl)
			if !ok {
				d.result = "Channel<Signal>"
				break
			}
			c.deps[payload.Service] = true
			d.payload = c.depCrate(payload.Service) + "::" + payload.Type
			d.result = "Channel<" + d.payload + ">"
			if keep := c.keep(payload.Service, payload.Def, map[string]bool{}); keep != "" {
				d.decoder = "decode_" + rustutil.Identifier(payload.Type, "payload")
			}
		default:
			d.result = "Key"
		}
		c.decls = append(c.decls, d)
	}
	return c, nil
}

func crateIdent(crate string) string { return strings.ReplaceAll(crate, "-", "_") }

func (c *rustCrate) depCrate(service string) string {
	return crateIdent(c.names.RustTypesCrate(service))
}

// part is a part's parameter and how its wire string is made.
func (c *rustCrate) part(decl *ir.MemoryStoreDeclaration, part ir.MemoryStorePart) (rustPart, error) {
	p := rustPart{name: part.Name, ident: rustutil.Identifier(part.Name, "part")}
	name := part.Type.Name
	switch part.Format {
	case ir.MemoryStorePartEnum:
		pkg := importOf(c.in.Schema, name)
		if pkg == "" {
			return p, fmt.Errorf("part %s of %s is enum %s, which the schema imports from no dependency", part.Name, decl.Name, name)
		}
		service := typegen.DependencyServiceName(pkg)
		c.deps[service] = true
		p.typ = c.depCrate(service) + "::" + name
	case ir.MemoryStorePartBoolean:
		p.typ = "bool"
	case ir.MemoryStorePartInteger:
		p.typ = "i64"
		if rust, ok := c.scalars[name]; ok && rust != "i64" {
			p.typ = "&" + c.scalarType(rust)
		}
	case ir.MemoryStorePartString, "":
		p.typ = "&str"
		if rust, ok := c.scalars[name]; ok && rust != "String" {
			p.typ = "&" + c.scalarType(rust)
		}
	default:
		return p, fmt.Errorf("part %s of %s has format %q, which no Rust definition writes", part.Name, decl.Name, part.Format)
	}
	return c.rebind(p), nil
}

// scalarType is a scalar's Rust type, noting when it is the scalar crate's.
func (c *rustCrate) scalarType(rust string) string {
	if strings.HasPrefix(rust, c.names.ScalarRustCrateIdent()+"::") {
		c.usesScalar = true
	}
	return rust
}

// rebind sets the statement and the element of p's wire string for its
// parameter's name and type.
func (c *rustCrate) rebind(p rustPart) rustPart {
	switch {
	case p.typ == "&str":
		p.let, p.arg = "", p.ident
	case p.typ == "bool":
		p.let, p.arg = "let "+p.ident+" = if "+p.ident+` { "true" } else { "false" };`, p.ident
	case p.typ == "i64":
		p.let, p.arg = "let "+p.ident+" = "+p.ident+".to_string();", "&"+p.ident
	case !strings.HasPrefix(p.typ, "&"):
		// An enum, by value: its wire value.
		p.let, p.arg = "let "+p.ident+" = "+p.ident+".as_str();", p.ident
	default:
		p.let, p.arg = "let "+p.ident+" = memorystore::part("+p.ident+")?;", "&"+p.ident
	}
	return p
}

// keep writes the function that removes the keys def, a type of service,
// and each object type it reaches, do not declare, when one of them has
// @denyUnknownFields, and returns its name; "" when none has.
func (c *rustCrate) keep(service string, def *ir.TypeDef, seen map[string]bool) string {
	if !c.strict(service, def, map[string]bool{}) {
		return ""
	}
	fn := "keep_" + rustutil.Identifier(def.Name, "type")
	if seen[def.Name] || slices.ContainsFunc(c.keeps, func(k rustKeep) bool { return k.fn == fn }) {
		return fn
	}
	seen[def.Name] = true
	c.deps[service] = true
	k := rustKeep{fn: fn, typ: def.Name}
	dep := c.in.Dependencies[service]
	for _, f := range def.Fields {
		if f == nil {
			continue
		}
		k.keys = append(k.keys, f.Name)
		nested := dep.Types[f.TypeRef.Name]
		if nested == nil {
			continue
		}
		if inner := c.keep(service, nested, seen); inner != "" {
			k.nested = append(k.nested, rustNested{key: f.Name, depth: f.TypeRef.ArrayDepth(), isMap: f.TypeRef.IsMap, keep: inner})
		}
	}
	sort.Strings(k.keys)
	c.keeps = append(c.keeps, k)
	return fn
}

// strict reports whether def, or an object type of service it reaches,
// has @denyUnknownFields.
func (c *rustCrate) strict(service string, def *ir.TypeDef, seen map[string]bool) bool {
	if def == nil || seen[def.Name] {
		return false
	}
	seen[def.Name] = true
	if def.DenyUnknownFields {
		return true
	}
	dep := c.in.Dependencies[service]
	for _, f := range def.Fields {
		if f != nil && dep != nil && c.strict(service, dep.Types[f.TypeRef.Name], seen) {
			return true
		}
	}
	return false
}

// manifest is the crate's Cargo.toml.
func (c *rustCrate) manifest() string {
	paths := c.in.Context.Options.Paths
	type dep struct{ name, spec string }
	var deps []dep
	pathOr := func(target, version string) string {
		if rel, err := naming.RelPath(c.in.Dir, target); err == nil && rel != "" {
			return `{ path = "` + filepath.ToSlash(rel) + `" }`
		}
		return `"` + version + `"`
	}
	deps = append(deps, dep{c.names.HTTPRuntimeRustCrate, pathOr(paths.HTTPRuntimeRust, RustRuntimeVersion)})
	if c.usesScalar {
		deps = append(deps, dep{c.names.ScalarRustCrate, pathOr(paths.ScalarRust, RustScalarVersion)})
	}
	for service := range c.deps {
		deps = append(deps, dep{c.names.RustTypesCrate(service), `{ path = "../` + service + `" }`})
	}
	if len(c.keeps) > 0 {
		deps = append(deps, dep{"serde_json", `"` + rustSerdeJSON + `"`})
	}
	sort.Slice(deps, func(i, j int) bool { return deps[i].name < deps[j].name })
	var b strings.Builder
	fmt.Fprintf(&b, "[package]\nname = %q\nversion = \"0.1.0\"\nedition = \"2021\"\nrust-version = \"1.99.0\"\n", c.crate)
	fmt.Fprintf(&b, "description = %q\nlicense = \"UNLICENSED\"\n\n[dependencies]\n", "Generated Rust definitions of the channels and keys of the "+c.in.Store+" memory store")
	for _, d := range deps {
		fmt.Fprintf(&b, "%s = %s\n", d.name, d.spec)
	}
	return b.String()
}

// lib is the crate's src/lib.rs.
func (c *rustCrate) lib() string {
	var b strings.Builder
	b.WriteString("// Code generated by superschematic memorystoregen. DO NOT EDIT.\n\n")
	fmt.Fprintf(&b, "//! The channels and keys of the `%s` memory store (D75): a value per\n", c.in.Store)
	b.WriteString("//! declaration of its schema, whose `for_` takes the declaration's parts in\n")
	b.WriteString("//! order and returns the HTTP runtime's `Channel` or `Key`, so a caller never\n")
	b.WriteString("//! formats a name.\n\n")
	b.WriteString("#![allow(clippy::module_name_repetitions)]\n\n")
	hasKey := slices.ContainsFunc(c.decls, func(d rustDecl) bool { return d.decl.Kind.IsKey() })
	if hasKey {
		b.WriteString("use std::time::Duration;\n\n")
	}
	items := []string{"self", "MemoryStoreError"}
	if hasKey {
		items = append(items, "Key")
	}
	if slices.ContainsFunc(c.decls, func(d rustDecl) bool { return d.decl.Kind == ir.MemoryStoreChannel }) {
		items = append(items, "Channel")
	}
	if slices.ContainsFunc(c.decls, func(d rustDecl) bool { return d.result == "Channel<Signal>" }) {
		items = append(items, "Signal")
	}
	b.WriteString(rustutil.UseLine(c.runtime, items) + "\n")
	for _, d := range c.decls {
		b.WriteString("\n")
		c.writeDecl(&b, d)
	}
	for _, d := range c.decls {
		if d.decoder != "" && !strings.Contains(b.String(), "fn "+d.decoder+"(") {
			c.writeDecoder(&b, d)
		}
	}
	for _, k := range c.keeps {
		c.writeKeep(&b, k)
	}
	return b.String()
}

// doc writes text as doc comment lines at indent, wrapped at 80 columns.
func doc(b *strings.Builder, indent, text string) {
	for _, para := range strings.Split(strings.TrimSpace(text), "\n\n") {
		line := indent + "///"
		for _, word := range strings.Fields(para) {
			if len(line)+1+len(word) > 80 && line != indent+"///" {
				b.WriteString(line + "\n")
				line = indent + "///"
			}
			line += " " + word
		}
		b.WriteString(line + "\n")
		if !strings.HasSuffix(text, para) {
			b.WriteString(indent + "///\n")
		}
	}
}

func (d rustDecl) kindText() string {
	switch {
	case d.decl.Kind == ir.MemoryStoreChannel && d.payload == "":
		return "A signal channel"
	case d.decl.Kind == ir.MemoryStoreChannel:
		return "A channel of `" + d.decl.Payload.Name + "` messages"
	case d.decl.Kind == ir.MemoryStoreClaim:
		return "A claim"
	}
	return "A counter"
}

func (c *rustCrate) writeDecl(b *strings.Builder, d rustDecl) {
	decl := d.decl
	if decl.Doc != "" {
		doc(b, "", decl.Doc)
		b.WriteString("///\n")
	}
	life := ""
	switch {
	case decl.TTLSeconds > 0:
		life = fmt.Sprintf(", whose key lives %d seconds after the write that creates it", decl.TTLSeconds)
	case decl.MaxTTLSeconds > 0:
		life = fmt.Sprintf(", whose key lives the TTL its caller gives, at most %d seconds", decl.MaxTTLSeconds)
	}
	doc(b, "", fmt.Sprintf("%s, `%s`%s.", d.kindText(), decl.NamePattern(), life))
	b.WriteString("#[derive(Clone, Copy, Debug, Default, PartialEq, Eq)]\n")
	fmt.Fprintf(b, "pub struct %s;\n\n", d.ident)
	fmt.Fprintf(b, "/// The `%s` declaration.\n", decl.Name)
	fmt.Fprintf(b, "pub const %s: %s = %s;\n\n", d.konst, d.ident, d.ident)
	fmt.Fprintf(b, "impl %s {\n", d.ident)
	b.WriteString("    /// The declaration's name, the segment after the store's.\n")
	fmt.Fprintf(b, "    pub const NAME: &'static str = %q;\n", decl.Name)
	switch {
	case decl.TTLSeconds > 0:
		b.WriteString("    /// How long a key lives after the write that creates it.\n")
		fmt.Fprintf(b, "    pub const TTL: Duration = Duration::from_secs(%d);\n", decl.TTLSeconds)
	case decl.MaxTTLSeconds > 0:
		b.WriteString("    /// The longest TTL `for_ttl` takes.\n")
		fmt.Fprintf(b, "    pub const MAX_TTL: Duration = Duration::from_secs(%d);\n", decl.MaxTTLSeconds)
	}
	b.WriteString("\n")
	var params, idents []string
	params = append(params, "&self")
	fn := "for_"
	what := "channel"
	if decl.Kind.IsKey() {
		what = "key"
	}
	if decl.MaxTTLSeconds > 0 {
		fn = "for_ttl"
		params = append(params, "ttl: Duration")
	}
	for _, p := range d.parts {
		params = append(params, p.ident+": "+p.typ)
		idents = append(idents, "`"+p.ident+"`")
	}
	if len(idents) > 0 {
		fmt.Fprintf(b, "    /// The %s of %s.\n", what, joinAnd(idents))
	} else {
		fmt.Fprintf(b, "    /// The %s.\n", what)
	}
	b.WriteString("    ///\n    /// # Errors\n    ///\n")
	if decl.MaxTTLSeconds > 0 {
		b.WriteString("    /// [`MemoryStoreError::Name`] for a part that is no segment, and\n")
		b.WriteString("    /// [`MemoryStoreError::Ttl`] for a TTL under 1ms or over [`Self::MAX_TTL`].\n")
	} else {
		b.WriteString("    /// [`MemoryStoreError::Name`] for a part that is no segment.\n")
	}
	b.WriteString(signature("    ", "pub fn "+fn, params, "Result<"+d.result+", MemoryStoreError>") + "\n")
	if decl.MaxTTLSeconds > 0 {
		b.WriteString("        let ttl = memorystore::key_ttl(Self::NAME, ttl, Self::MAX_TTL)?;\n")
	}
	var args []string
	for _, p := range d.parts {
		if p.let != "" {
			b.WriteString("        " + p.let + "\n")
		}
		args = append(args, p.arg)
	}
	b.WriteString(nameLet(args))
	switch {
	case decl.Kind.IsKey() && decl.MaxTTLSeconds > 0:
		b.WriteString("        Ok(Key::new(Self::NAME, name, ttl))\n")
	case decl.Kind.IsKey():
		b.WriteString("        Ok(Key::new(Self::NAME, name, Self::TTL))\n")
	case d.payload == "":
		b.WriteString("        Ok(Channel::signal(Self::NAME, name))\n")
	case d.decoder != "":
		fmt.Fprintf(b, "        Ok(Channel::with_decoder(Self::NAME, name, %s))\n", d.decoder)
	default:
		b.WriteString("        Ok(Channel::new(Self::NAME, name))\n")
	}
	b.WriteString("    }\n}\n")
}

// nameLet is the statement that joins the parts into the name, as rustfmt
// lays it out.
func nameLet(args []string) string {
	array := "&[" + strings.Join(args, ", ") + "]"
	call := "Self::NAME, " + array
	if line := "        let name = memorystore::name(" + call + ")?;"; len(line) <= rustWidth && len(call) <= 60 {
		return line + "\n"
	}
	if len(array) <= 60 && len("            "+array+",") <= rustWidth {
		return "        let name = memorystore::name(\n            Self::NAME,\n            " + array + ",\n        )?;\n"
	}
	var b strings.Builder
	b.WriteString("        let name = memorystore::name(\n            Self::NAME,\n            &[\n")
	for _, arg := range args {
		b.WriteString("                " + arg + ",\n")
	}
	b.WriteString("            ],\n        )?;\n")
	return b.String()
}

// signature is a function's signature through its opening brace, on one
// line when it fits, else a parameter to a line, as rustfmt lays it out.
func signature(indent, name string, params []string, result string) string {
	line := indent + name + "(" + strings.Join(params, ", ") + ") -> " + result + " {"
	if len(line) <= rustWidth {
		return line
	}
	var b strings.Builder
	b.WriteString(indent + name + "(\n")
	for _, p := range params {
		b.WriteString(indent + "    " + p + ",\n")
	}
	b.WriteString(indent + ") -> " + result + " {")
	return b.String()
}

func joinAnd(items []string) string {
	switch len(items) {
	case 0:
		return ""
	case 1:
		return items[0]
	}
	return strings.Join(items[:len(items)-1], ", ") + " and " + items[len(items)-1]
}

func (c *rustCrate) writeDecoder(b *strings.Builder, d rustDecl) {
	keep := "keep_" + rustutil.Identifier(d.decl.Payload.Name, "type")
	fmt.Fprintf(b, "\n/// A `%s` message's JSON decoded, without the keys a type it holds does\n", d.decl.Payload.Name)
	b.WriteString("/// not declare, so a message from a newer release decodes.\n")
	b.WriteString(signature("", "fn "+d.decoder, []string{"data: &[u8]"}, "Result<"+d.payload+", String>") + "\n")
	b.WriteString("    let mut value: serde_json::Value =\n        serde_json::from_slice(data).map_err(|err| err.to_string())?;\n")
	fmt.Fprintf(b, "    %s(&mut value);\n", keep)
	b.WriteString("    serde_json::from_value(value).map_err(|err| err.to_string())\n}\n")
}

func (c *rustCrate) writeKeep(b *strings.Builder, k rustKeep) {
	fmt.Fprintf(b, "\n/// Removes the keys `%s` does not declare.\n", k.typ)
	fmt.Fprintf(b, "fn %s(value: &mut serde_json::Value) {\n", k.fn)
	b.WriteString("    let Some(object) = value.as_object_mut() else {\n        return;\n    };\n")
	quoted := make([]string, len(k.keys))
	for i, key := range k.keys {
		quoted[i] = fmt.Sprintf("%q", key)
	}
	array := "&[" + strings.Join(quoted, ", ") + "]"
	if line := "    const KEYS: &[&str] = " + array + ";"; len(line) <= rustWidth && len(array) <= 60 {
		b.WriteString(line + "\n")
	} else {
		b.WriteString("    const KEYS: &[&str] = &[\n")
		for _, q := range quoted {
			b.WriteString("        " + q + ",\n")
		}
		b.WriteString("    ];\n")
	}
	b.WriteString("    object.retain(|key, _| KEYS.contains(&key.as_str()));\n")
	for _, n := range k.nested {
		fmt.Fprintf(b, "    if let Some(value) = object.get_mut(%q) {\n", n.key)
		fmt.Fprintf(b, "        memorystore::each_value(value, %d, %t, %s);\n    }\n", n.depth, n.isMap, n.keep)
	}
	b.WriteString("}\n")
}
