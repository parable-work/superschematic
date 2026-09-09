package tswriter

import (
	"fmt"
	"reflect"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// emitEnum renders an exported string enum.
func (e *emitter) emitEnum(def *ir.EnumDef) {
	if def.Description != "" {
		e.failf("enum %s: descriptions have no TypeScript authoring form", def.Name)
	}
	e.body.WriteString("\n")
	e.comment("", def.Comment)
	fmt.Fprintf(&e.body, "export enum %s {\n", e.ident(def.Name, "enum"))
	for i, v := range def.Values {
		if v.Description != "" {
			e.failf("enum %s.%s: descriptions have no TypeScript authoring form", def.Name, v.Name)
		}
		e.comment("  ", v.Comment)
		serialized := v.SerializedAs
		if serialized == "" {
			serialized = v.Name
		}
		fmt.Fprintf(&e.body, "  %s = %s", e.ident(v.Name, "enum value"), quote(serialized))
		if i < len(def.Values)-1 {
			e.body.WriteString(",")
		}
		e.body.WriteString("\n")
	}
	e.body.WriteString("}\n")
}

// emitType renders one type definition: an abstract class for table, view,
// trait, and struct roles, or an object type alias for embedded structs in
// DB schemas (a plain class in a DB schema reads back as a table).
func (e *emitter) emitType(def *ir.TypeDef) {
	if def.Description != "" {
		e.failf("type %s: descriptions have no TypeScript authoring form", def.Name)
	}

	switch def.Role {
	case ir.RoleDBTable:
		if e.doc.Kind != "" && e.doc.Kind != ir.SchemaKindDB {
			e.failf("type %s: role DBTable is only expressible in a DB schema (document kind %s)", def.Name, e.doc.Kind)
			return
		}
		e.emitClass(def)
	case ir.RoleEmbeddedStruct:
		if e.doc.Kind == ir.SchemaKindDB {
			e.emitTypeAlias(def)
			return
		}
		e.emitClass(def)
	case ir.RoleAPIView:
		if def.Source == nil {
			e.failf("type %s: role APIView requires a source projection (@source) in TypeScript", def.Name)
			return
		}
		e.emitClass(def)
	case ir.RoleTrait:
		e.emitClass(def)
	default:
		e.failf("type %s: role %s has no TypeScript authoring form", def.Name, def.Role)
	}
}

// emitTypeAlias renders an embedded struct as an exported object type alias.
// Aliases carry only plain fields: a name, a type, optionality, and a
// comment.
func (e *emitter) emitTypeAlias(def *ir.TypeDef) {
	owner := "type " + def.Name
	if def.Extends != "" || len(def.Implements) > 0 || def.RawHeritage != nil ||
		def.IsTrait || def.TraitConfig != nil || def.Source != nil ||
		len(def.Indexes) > 0 || def.JsonField || def.EnvVars {
		e.failf("%s: embedded structs in a DB schema render as type aliases and cannot carry heritage or decorators", owner)
		return
	}

	e.body.WriteString("\n")
	e.comment("", def.Comment)
	fmt.Fprintf(&e.body, "export type %s = {\n", e.ident(def.Name, "type"))
	for _, fd := range def.Fields {
		fieldOwner := fmt.Sprintf("%s.%s", def.Name, fd.Name)
		e.checkPlainField(fd, fieldOwner)
		e.comment("  ", fd.Comment)
		optional := ""
		if !fd.Required {
			optional = "?"
		}
		expr := e.renderTypeName(fd.TypeRef.Name, fieldOwner)
		if fd.TypeRef.IsMap {
			e.failf("%s: map-typed fields have no TypeScript authoring form", fieldOwner)
		}
		if fd.TypeRef.IsArray {
			expr += "[]"
		}
		fmt.Fprintf(&e.body, "  readonly %s%s: %s;\n", e.ident(fd.Name, "field"), optional, expr)
	}
	e.body.WriteString("};\n")
}

// checkPlainField rejects field metadata a plain property signature cannot
// carry.
func (e *emitter) checkPlainField(fd *ir.FieldDef, owner string) {
	plain := *fd
	plain.Name = ""
	plain.Comment = ""
	plain.TypeRef = ir.TypeRef{}
	plain.Required = false
	if !isZeroField(&plain) {
		e.failf("%s: only a name, type, optionality, and comment are expressible on this field surface", owner)
	}
}

// emitClass renders an exported abstract class with its decorators,
// heritage, and fields.
func (e *emitter) emitClass(def *ir.TypeDef) {
	e.body.WriteString("\n")
	e.comment("", def.Comment)

	// Class decorators.
	if def.IsTrait || def.Role == ir.RoleTrait {
		if def.TraitConfig != nil {
			e.failf("type %s: configurable trait schemas are not statically readable from TypeScript yet", def.Name)
		}
		fmt.Fprintf(&e.body, "@%s({})\n", e.use("trait"))
	}
	for _, idx := range def.Indexes {
		keys := make([]string, len(idx.Keys))
		for i, k := range idx.Keys {
			keys[i] = quote(k)
		}
		opts := ""
		switch {
		case idx.Name != "" && idx.Unique:
			opts = fmt.Sprintf(", { unique: true, name: %s }", quote(idx.Name))
		case idx.Name != "":
			opts = fmt.Sprintf(", { name: %s }", quote(idx.Name))
		case idx.Unique:
			opts = ", true"
		}
		fmt.Fprintf(&e.body, "@%s<%s>([%s]%s)\n", e.use("index"), def.Name, strings.Join(keys, ", "), opts)
	}
	if def.Source != nil {
		e.emitSourceDecorator(def)
	}
	if def.JsonField {
		fmt.Fprintf(&e.body, "@%s\n", e.use("jsonField"))
	}
	if def.Versioned {
		fmt.Fprintf(&e.body, "@%s%s\n", e.use("versioned"), versionedConfigArgs(def.VersionedConfig))
	}
	if def.EnvVars {
		fmt.Fprintf(&e.body, "@%s\n", e.use("envVars"))
	}

	fmt.Fprintf(&e.body, "export abstract class %s%s {\n", e.ident(def.Name, "type"), e.heritageClause(def))

	first := true
	for _, fd := range def.Fields {
		if fd.InheritedFrom != "" {
			// Pre-flattened heritage fields re-flatten from the base class.
			continue
		}
		fieldOwner := fmt.Sprintf("%s.%s", def.Name, fd.Name)
		e.checkStructField(fd, fieldOwner)
		if !first {
			e.body.WriteString("\n")
		}
		first = false
		e.comment("  ", fd.Comment)
		for _, dec := range e.fieldDecorators(fd) {
			fmt.Fprintf(&e.body, "  @%s\n", dec)
		}
		fmt.Fprintf(&e.body, "  %s: %s;\n", e.ident(fd.Name, "field"), e.fieldTypeExpr(fd, fieldOwner, structField))
	}
	e.body.WriteString("}\n")
}

func versionedConfigArgs(cfg *ir.VersionedConfig) string {
	if cfg == nil || (cfg.RetentionDays == nil && cfg.PartitionBy == "" && len(cfg.PruneKeepReferencedBy) == 0) {
		return ""
	}
	parts := []string{}
	if cfg.RetentionDays != nil {
		parts = append(parts, fmt.Sprintf("retentionDays: %d", *cfg.RetentionDays))
	}
	if cfg.PartitionBy != "" {
		parts = append(parts, fmt.Sprintf("partitionBy: %s", quote(cfg.PartitionBy)))
	}
	if pins := cfg.PruneKeepReferencedBy; len(pins) > 0 {
		refs := make([]string, 0, len(pins))
		for _, pin := range pins {
			refs = append(refs, fmt.Sprintf("{ table: %s, keyColumn: %s, versionColumn: %s }",
				quote(pin.Table), quote(pin.KeyColumn), quote(pin.VersionColumn)))
		}
		// One reference keeps the single-object form, so declarations written
		// before the list form round-trip byte-identical.
		if len(refs) == 1 {
			parts = append(parts, "pruneKeepReferencedBy: "+refs[0])
		} else {
			parts = append(parts, "pruneKeepReferencedBy: ["+strings.Join(refs, ", ")+"]")
		}
	}
	return fmt.Sprintf("({ %s })", strings.Join(parts, ", "))
}

// heritageClause renders " extends Base implements A, B" from RawHeritage
// (which preserves type arguments as written), falling back to the resolved
// Extends and Implements names.
func (e *emitter) heritageClause(def *ir.TypeDef) string {
	extends := def.Extends
	implements := make([]string, 0, len(def.Implements))
	for _, tr := range def.Implements {
		if len(tr.ConfigArgs) > 0 {
			e.failf("type %s: trait config arguments are not statically readable from TypeScript yet", def.Name)
		}
		implements = append(implements, tr.Name)
	}
	if def.RawHeritage != nil {
		if def.RawHeritage.Extends != "" {
			extends = def.RawHeritage.Extends
		}
		if len(def.RawHeritage.Implements) > 0 {
			implements = def.RawHeritage.Implements
		}
	}
	// Resolved names drive the imports; the raw text is what renders.
	e.recordHeritageImports(def.Extends)
	for _, tr := range def.Implements {
		e.recordHeritageImports(tr.Name)
	}

	var b strings.Builder
	if extends != "" {
		b.WriteString(" extends " + extends)
	}
	if len(implements) > 0 {
		b.WriteString(" implements " + strings.Join(implements, ", "))
	}
	return b.String()
}

// recordHeritageImports records the relative import a heritage target
// declared in a sibling file needs.
func (e *emitter) recordHeritageImports(name string) {
	if name == "" {
		return
	}
	if _, local := e.doc.Types[name]; local {
		return
	}
	if base, ok := e.ctx.DefLocations[name]; ok && base != e.ctx.Base {
		e.importRelative(relativeModule(e.ctx.Base, base), name)
	}
}

// emitSourceDecorator renders @source(Target), importing the target when it
// lives in another service.
func (e *emitter) emitSourceDecorator(def *ir.TypeDef) {
	target := def.Source.Target
	if local, ok := e.localSourceTarget(target); ok {
		e.recordHeritageImports(local)
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("source"), local)
		return
	}
	i := strings.LastIndex(target, ".")
	service, name := target[:i], target[i+1:]
	for _, imp := range e.doc.Imports {
		if serviceNameForPackage(imp.Package) == service {
			e.importSymbol(imp.Package, name)
			fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("source"), name)
			return
		}
	}
	e.failf("type %s: @source target %q is not covered by the document's imports", def.Name, target)
}

// fieldDecorators returns the rendered decorator names for a struct field.
// JsonField, Secret, AutoGenerate, and the relation forms render as type
// wrappers instead.
func (e *emitter) fieldDecorators(fd *ir.FieldDef) []string {
	var out []string
	if fd.Key {
		out = append(out, e.use("key"))
	}
	if fd.Unique {
		out = append(out, e.use("unique"))
	}
	if fd.SearchField {
		out = append(out, e.use("searchField"))
	}
	if fd.UIHidden {
		out = append(out, e.use("uiHidden"))
	}
	if fd.Virtual {
		out = append(out, e.use("virtual"))
	}
	if fd.SourceMustProject {
		out = append(out, e.use("sourceMustProject"))
	}
	if fd.TemporalFormat != "" {
		out = append(out, e.use("temporalFormat")+fmt.Sprintf("(%s)", quote(fd.TemporalFormat)))
	}
	return out
}

// checkStructField rejects FieldDef metadata that has no authoring form on a
// data-class property.
func (e *emitter) checkStructField(fd *ir.FieldDef, owner string) {
	rest := *fd
	rest.Name = ""
	rest.Comment = ""
	rest.TypeRef = ir.TypeRef{}
	rest.Required = false
	rest.Default = nil
	rest.ValidateMin, rest.ValidateMax = nil, nil
	rest.ValidateMinLength, rest.ValidateMaxLength = nil, nil
	rest.ValidateListMin, rest.ValidateListMax = nil, nil
	rest.ValidatePattern = ""
	rest.Key, rest.Unique, rest.AutoGenerated = false, false, false
	rest.SearchField, rest.JsonField, rest.Secret = false, false, false
	rest.UIHidden, rest.Virtual, rest.Encrypted = false, false, false
	rest.SourceMustProject = false
	rest.Exclude = false
	rest.TemporalFormat = ""
	rest.Relation = nil
	rest.HasMany, rest.ManyToMany = false, false
	if !isZeroField(&rest) {
		e.failf("%s: carries metadata with no TypeScript authoring form on a data field", owner)
	}
}

// isZeroField reports whether a FieldDef has been fully cleared.
func isZeroField(fd *ir.FieldDef) bool {
	return reflect.DeepEqual(fd, &ir.FieldDef{})
}
