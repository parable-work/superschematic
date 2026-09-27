package tswriter

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// languagePrimitives are the host-language keywords usable directly.
var languagePrimitives = map[string]bool{"string": true, "number": true, "boolean": true}

// renderTypeName resolves a TypeRef base name into a TypeScript type
// expression, recording whatever import it needs: a superscalar namespace, a
// cross-service named import, a sibling-file relative import, or nothing for
// local and primitive names.
func (e *emitter) renderTypeName(name, owner string) string {
	if languagePrimitives[name] {
		return name
	}

	if _, ok := e.doc.Scalars[name]; ok {
		return e.renderScalar(name, owner)
	}

	if i := strings.LastIndex(name, "."); i >= 0 {
		service, local := name[:i], name[i+1:]
		for _, imp := range e.doc.Imports {
			if serviceNameForPackage(imp.Package) != service {
				continue
			}
			for _, t := range imp.Types {
				if t == local {
					e.importSymbol(imp.Package, local)
					return local
				}
			}
		}
		// Not a declared cross-service import: assume a superscalar brand the
		// document does not redeclare (single-file conversions reference
		// scalars declared in sibling files).
		return e.renderScalar(name, owner)
	}

	if _, local := e.doc.Types[name]; local {
		return name
	}
	if _, local := e.doc.Enums[name]; local {
		return name
	}
	if base, ok := e.ctx.DefLocations[name]; ok && base != e.ctx.Base {
		e.importRelative(relativeModule(e.ctx.Base, base), name)
	}
	return name
}

// renderScalar emits a namespaced scalar reference, importing its namespace
// from the scalar library package. The writer has no options path, so the
// package name is the process-wide value the CLI set (naming.Active).
func (e *emitter) renderScalar(name, owner string) string {
	scalarPkg := naming.Active().ScalarNpmPackage
	i := strings.Index(name, ".")
	if i <= 0 {
		e.failf("%s: scalar %q is not namespaced; TypeScript schemas can only reference %s scalars", owner, name, scalarPkg)
		return name
	}
	e.importSymbol(scalarPkg, name[:i])
	return name
}

// serviceNameForPackage derives the schema service name from a package name:
// the scope prefix is stripped ("@schemas/web-db" -> "web-db").
func serviceNameForPackage(pkg string) string {
	if i := strings.LastIndex(pkg, "/"); i >= 0 {
		return pkg[i+1:]
	}
	return pkg
}

// relativeModule computes the relative import specifier from one
// extension-free schema base to another (both slash paths).
func relativeModule(from, to string) string {
	fromParts := strings.Split(from, "/")
	toParts := strings.Split(to, "/")
	common := 0
	for common < len(fromParts)-1 && common < len(toParts)-1 && fromParts[common] == toParts[common] {
		common++
	}
	var b strings.Builder
	ups := len(fromParts) - 1 - common
	if ups == 0 {
		b.WriteString("./")
	}
	for range ups {
		b.WriteString("../")
	}
	b.WriteString(strings.Join(toParts[common:], "/"))
	return b.String()
}

// fieldKind classifies which FieldDef surface is being rendered, since the
// legal decorator metadata differs per surface.
type fieldKind int

const (
	structField fieldKind = iota
	aliasField
	operationField
)

// fieldTypeExpr renders a field's full type expression: the base reference
// wrapped in the @superschematic generic forms its IR metadata encodes. The wrapper
// nesting order keeps Default innermost (its value argument must extend the
// unwrapped base type) and Nullable outermost.
func (e *emitter) fieldTypeExpr(fd *ir.FieldDef, owner string, kind fieldKind) string {
	if fd.TypeRef.IsMap {
		e.failf("%s: map-typed fields have no TypeScript authoring form", owner)
		return "never"
	}

	expr := e.renderTypeName(fd.TypeRef.Name, owner)

	if fd.Default != nil {
		expr = fmt.Sprintf("%s<%s, %s>", e.use("Default"), expr, e.defaultLiteral(fd, owner))
	}
	if cfg := validateConfigExpr(fd); cfg != "" {
		expr = fmt.Sprintf("%s<%s, %s>", e.use("Validate"), expr, cfg)
	}
	if fd.Secret {
		expr = fmt.Sprintf("%s<%s>", e.use("Secret"), expr)
	}

	wrapsArray := false
	if fd.JsonField {
		expr = fmt.Sprintf("%s<%s>", e.use("JsonField"), expr)
	}
	switch {
	case fd.Relation != nil:
		if fd.Relation.Type != "" && fd.Relation.Type != fd.TypeRef.Name {
			e.failf("%s: relation target %q differs from the field type %q, which TypeScript cannot express", owner, fd.Relation.Type, fd.TypeRef.Name)
		}
		if fd.Relation.Field != "" {
			e.failf("%s: an explicit relation foreign-key field has no TypeScript authoring form", owner)
		}
		if fd.Relation.OnDelete != "" {
			expr = fmt.Sprintf("%s<%s, { onDelete: %q }>", e.use("Relation"), expr, fd.Relation.OnDelete)
		} else {
			expr = fmt.Sprintf("%s<%s>", e.use("Relation"), expr)
		}
	case fd.HasMany && fd.ManyToMany:
		e.failf("%s: a field cannot be both hasMany and manyToMany", owner)
	case fd.HasMany:
		expr = fmt.Sprintf("%s<%s>", e.use("HasMany"), expr)
		wrapsArray = true
	case fd.ManyToMany:
		expr = fmt.Sprintf("%s<%s>", e.use("ManyToMany"), expr)
		wrapsArray = true
	}
	switch {
	case !wrapsArray:
		expr += arraySuffix(fd.TypeRef)
	case fd.TypeRef.IsArray:
		// HasMany<T> / ManyToMany<T> already denote T[].
		expr += strings.Repeat("[]", fd.TypeRef.ArrayDepth()-1)
	default:
		e.failf("%s: hasMany/manyToMany fields must have an array type reference", owner)
	}

	if fd.AutoGenerated {
		expr = fmt.Sprintf("%s<%s>", e.use("AutoGenerate"), expr)
	}
	if fd.Encrypted {
		expr = fmt.Sprintf("%s<%s>", e.use("EncryptedField"), expr)
	}
	if !fd.Required && kind != aliasField {
		expr = fmt.Sprintf("%s<%s>", e.use("Nullable"), expr)
	}
	return expr
}

// argumentTypeExpr renders an operation argument's type expression.
func (e *emitter) argumentTypeExpr(arg *ir.ArgumentDef, owner string) string {
	if arg.TypeRef.IsMap {
		e.failf("%s: map-typed arguments have no TypeScript authoring form", owner)
		return "never"
	}
	expr := e.renderTypeName(arg.TypeRef.Name, owner)
	if arg.Default != nil {
		fd := &ir.FieldDef{TypeRef: arg.TypeRef, Default: arg.Default}
		expr = fmt.Sprintf("%s<%s, %s>", e.use("Default"), expr, e.defaultLiteral(fd, owner))
	}
	if cfg := validateConfigExpr(&ir.FieldDef{
		ValidateMin: arg.ValidateMin, ValidateMax: arg.ValidateMax,
		ValidateMinLength: arg.ValidateMinLength, ValidateMaxLength: arg.ValidateMaxLength,
		ValidateListMin: arg.ValidateListMin, ValidateListMax: arg.ValidateListMax,
		ValidatePattern: arg.ValidatePattern,
	}); cfg != "" {
		expr = fmt.Sprintf("%s<%s, %s>", e.use("Validate"), expr, cfg)
	}
	expr += arraySuffix(arg.TypeRef)
	if !arg.Required {
		expr = fmt.Sprintf("%s<%s>", e.use("Nullable"), expr)
	}
	if arg.IsQuery {
		expr = fmt.Sprintf("%s<%s>", e.use("QueryParam"), expr)
	}
	return expr
}

// arraySuffix renders a reference's list depth: "" for T, "[]" for T[] and
// "[][]" for T[][].
func arraySuffix(ref ir.TypeRef) string {
	return strings.Repeat("[]", ref.ArrayDepth())
}

// defaultLiteral renders the V in Default<T, V>. The literal form follows
// the base type: enum members render as member references, number and
// boolean primitives as bare literals, strings as string literals. Scalar
// and object bases cannot satisfy Default's V extends T constraint with a
// literal, so they are not expressible.
func (e *emitter) defaultLiteral(fd *ir.FieldDef, owner string) string {
	value := *fd.Default
	base := fd.TypeRef.Name

	if enum := e.lookupEnum(base); enum != nil {
		for _, v := range enum.Values {
			serialized := v.SerializedAs
			if serialized == "" {
				serialized = v.Name
			}
			if serialized == value {
				return base + "." + v.Name
			}
		}
		e.failf("%s: default %q is not a member of enum %s", owner, value, base)
		return quote(value)
	}

	switch base {
	case "number":
		if _, err := strconv.ParseFloat(value, 64); err != nil {
			e.failf("%s: default %q is not a numeric literal", owner, value)
		}
		return value
	case "boolean":
		if value != "true" && value != "false" {
			e.failf("%s: default %q is not a boolean literal", owner, value)
		}
		return value
	case "string":
		return quote(value)
	}

	e.failf("%s: a default on type %q has no TypeScript authoring form (Default's value must be a literal of the base type)", owner, base)
	return quote(value)
}

// lookupEnum resolves an enum by name from the document or the service-level
// context.
func (e *emitter) lookupEnum(name string) *ir.EnumDef {
	if def, ok := e.doc.Enums[name]; ok {
		return def
	}
	if def, ok := e.ctx.Enums[name]; ok {
		return def
	}
	return nil
}

// validateConfigExpr renders the C in Validate<T, C> from the FieldDef
// validation metadata, or "" when the field carries none.
func validateConfigExpr(fd *ir.FieldDef) string {
	var parts []string
	add := func(key, value string) {
		parts = append(parts, key+": "+value)
	}
	if fd.ValidateMin != nil {
		add("min", formatFloat(*fd.ValidateMin))
	}
	if fd.ValidateMax != nil {
		add("max", formatFloat(*fd.ValidateMax))
	}
	if fd.ValidateUploadMaxBytes != nil {
		add("uploadMaxBytes", strconv.FormatInt(*fd.ValidateUploadMaxBytes, 10))
	}
	if fd.ValidateMinLength != nil {
		add("minLength", strconv.Itoa(*fd.ValidateMinLength))
	}
	if fd.ValidateMaxLength != nil {
		add("maxLength", strconv.Itoa(*fd.ValidateMaxLength))
	}
	if fd.ValidateListMin != nil {
		add("listMin", strconv.Itoa(*fd.ValidateListMin))
	}
	if fd.ValidateListMax != nil {
		add("listMax", strconv.Itoa(*fd.ValidateListMax))
	}
	if fd.ValidatePattern != "" {
		add("pattern", quote(fd.ValidatePattern))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func formatFloat(v float64) string {
	return strconv.FormatFloat(v, 'f', -1, 64)
}
