package tswriter

import (
	"encoding/json"
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
	if len(def.Extensions) > 0 {
		e.failf("type %s: extension data (%s) has no TypeScript authoring form in this writer", def.Name, strings.Join(sortedKeys(def.Extensions), ", "))
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
	case ir.RoleProjection:
		if e.doc.Kind != "" && e.doc.Kind != ir.SchemaKindDB {
			e.failf("type %s: role Projection is only expressible in a DB schema (document kind %s)", def.Name, e.doc.Kind)
			return
		}
		e.emitProjection(def)
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
		def.User != nil || def.UserRole != nil ||
		def.IsTrait || def.TraitConfig != nil || def.Source != nil ||
		len(def.Indexes) > 0 || def.JsonField || def.EnvVars ||
		def.DenyUnknownFields || def.StrictJSON || len(def.Behaviors) > 0 || def.Display != nil {
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
		expr += arraySuffix(fd.TypeRef)
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
	if def.DenyUnknownFields {
		fmt.Fprintf(&e.body, "@%s\n", e.use("denyUnknownFields"))
	}
	if def.StrictJSON {
		fmt.Fprintf(&e.body, "@%s\n", e.use("strictJSON"))
	}
	if def.Versioned {
		fmt.Fprintf(&e.body, "@%s%s\n", e.use("versioned"), versionedConfigArgs(def.VersionedConfig))
	}
	if def.Optimistic {
		fmt.Fprintf(&e.body, "@%s\n", e.use("optimistic"))
	}
	if def.VersionGraph != nil {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("versionGraph"), versionGraphArgs(def.VersionGraph))
	}
	if def.GraphMember != nil {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("graphMember"), e.graphMemberArgs(def.GraphMember))
	}
	if def.EnvVars {
		fmt.Fprintf(&e.body, "@%s\n", e.use("envVars"))
	}
	for _, ref := range def.Behaviors {
		e.emitBehavior(def.Name, ref)
	}
	if def.Display != nil {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("display"), displayArgs(def.Display))
	}
	e.emitStackDeclarations(def)

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

// emitBehavior renders one @behavior(name, config?) line. Several render in
// list order, which is the source order the walker reads them back in.
func (e *emitter) emitBehavior(typeName string, ref ir.BehaviorRef) {
	if len(ref.Config) == 0 {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("behavior"), quote(ref.Name))
		return
	}
	var config any
	if err := json.Unmarshal(ref.Config, &config); err != nil {
		e.failf("type %s: behavior %s config: %v", typeName, ref.Name, err)
		return
	}
	fmt.Fprintf(&e.body, "@%s(%s, %s)\n", e.use("behavior"), quote(ref.Name), valueLiteral(config))
}

// displayArgs renders @display's argument with its members in the IR's
// order, and states and transitions by key.
func displayArgs(d *ir.TypeDisplay) string {
	var parts []string
	text := func(key, value string) {
		if value != "" {
			parts = append(parts, key+": "+quote(value))
		}
	}
	text("noun", d.Noun)
	text("plural", d.Plural)
	text("titleField", d.TitleField)
	text("createLabel", d.CreateLabel)
	if len(d.SummaryFields) > 0 {
		names := make([]string, len(d.SummaryFields))
		for i, name := range d.SummaryFields {
			names[i] = quote(name)
		}
		parts = append(parts, "summaryFields: ["+strings.Join(names, ", ")+"]")
	}
	if len(d.States) > 0 {
		states := make([]string, 0, len(d.States))
		for _, name := range sortedKeys(d.States) {
			state := d.States[name]
			var members []string
			if state.Label != "" {
				members = append(members, "label: "+quote(state.Label))
			}
			if state.ActiveForm != "" {
				members = append(members, "activeForm: "+quote(state.ActiveForm))
			}
			if state.Tone != "" {
				members = append(members, "tone: "+quote(string(state.Tone)))
			}
			states = append(states, propertyName(name)+": { "+strings.Join(members, ", ")+" }")
		}
		parts = append(parts, "states: { "+strings.Join(states, ", ")+" }")
	}
	if len(d.Transitions) > 0 {
		froms := make([]string, 0, len(d.Transitions))
		for _, from := range sortedKeys(d.Transitions) {
			tos := make([]string, 0, len(d.Transitions[from]))
			for _, to := range sortedKeys(d.Transitions[from]) {
				tos = append(tos, propertyName(to)+": "+quote(d.Transitions[from][to]))
			}
			froms = append(froms, propertyName(from)+": { "+strings.Join(tos, ", ")+" }")
		}
		parts = append(parts, "transitions: { "+strings.Join(froms, ", ")+" }")
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

func versionedConfigArgs(cfg *ir.VersionedConfig) string {
	if cfg == nil || (cfg.RetentionDays == nil && cfg.PartitionBy == "" && len(cfg.PruneKeepReferencedBy) == 0 && len(cfg.Exclude) == 0) {
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
	if len(cfg.Exclude) > 0 {
		names := make([]string, 0, len(cfg.Exclude))
		for _, name := range cfg.Exclude {
			names = append(names, quote(name))
		}
		parts = append(parts, "exclude: ["+strings.Join(names, ", ")+"]")
	}
	return fmt.Sprintf("({ %s })", strings.Join(parts, ", "))
}

// versionGraphArgs renders the @versionGraph config, or nothing for the
// zero config.
func versionGraphArgs(cfg *ir.VersionGraphConfig) string {
	var parts []string
	if cfg.Name != "" {
		parts = append(parts, "name: "+quote(cfg.Name))
	}
	if cfg.SchemaEpoch != 0 {
		parts = append(parts, fmt.Sprintf("schemaEpoch: %d", cfg.SchemaEpoch))
	}
	if cfg.SnapshotEvery != nil {
		parts = append(parts, fmt.Sprintf("snapshotEvery: %d", *cfg.SnapshotEvery))
	}
	if len(parts) == 0 {
		return ""
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// graphMemberArgs renders the @graphMember config. graph and parent.of name
// classes as values, imported when a sibling file declares them.
func (e *emitter) graphMemberArgs(cfg *ir.GraphMemberConfig) string {
	e.recordHeritageImports(cfg.Graph)
	parts := []string{"graph: " + cfg.Graph}
	if p := cfg.Parent; p != nil {
		e.recordHeritageImports(p.Of)
		parts = append(parts, fmt.Sprintf("parent: { key: %s, of: %s }", quote(p.Key), p.Of))
	}
	if cfg.Order != "" {
		parts = append(parts, "order: "+quote(cfg.Order))
	}
	if cfg.Singleton {
		parts = append(parts, "singleton: true")
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}

// heritageClause renders " extends Base implements A, B" from RawHeritage
// (which preserves type arguments as written), falling back to the resolved
// Extends and Implements names. The user model's traits, which RawHeritage
// leaves out, lead the implements list.
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
	implements = append(e.identityTraits(def), implements...)
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

// identityTraits renders the user model's traits a table carries (D50):
// User<{ login: "email"; name: "displayName" }> and UserRole.
func (e *emitter) identityTraits(def *ir.TypeDef) []string {
	var out []string
	if user := def.User; user != nil {
		members := []string{"login: " + quote(user.Login)}
		if user.Name != "" {
			members = append(members, "name: "+quote(user.Name))
		}
		out = append(out, fmt.Sprintf("%s<{ %s }>", e.useCoreTrait("User"), strings.Join(members, "; ")))
	}
	if def.UserRole != nil {
		out = append(out, e.useCoreTrait("UserRole"))
	}
	return out
}

// useCoreTrait records the import of a user model trait and returns the
// name the file refers to it by: the trait's own, or an alias when the
// service declares a definition of that name, as a table named User does.
// The reader knows the trait by its import, so an alias reads the same.
func (e *emitter) useCoreTrait(symbol string) string {
	if !e.declares(symbol) {
		return e.use(symbol)
	}
	alias := symbol + "Trait"
	for i := 2; e.declares(alias); i++ {
		alias = fmt.Sprintf("%sTrait%d", symbol, i)
	}
	return e.useAs(symbolPackages[symbol], symbol, alias)
}

// declares reports whether the document or a sibling file of the service
// declares a definition of the given name.
func (e *emitter) declares(name string) bool {
	_, inDoc := e.doc.Types[name]
	_, isEnum := e.doc.Enums[name]
	_, isUnion := e.doc.Unions[name]
	_, inService := e.ctx.DefLocations[name]
	return inDoc || isEnum || isUnion || inService
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
	if fd.Title != "" {
		out = append(out, e.useAs("@superschematic/schema", "docs", "schemaDocs")+fmt.Sprintf("({ title: %s })", quote(fd.Title)))
	}
	if fd.Purpose != "" {
		out = append(out, e.use("purpose")+fmt.Sprintf("(%s)", quote(fd.Purpose)))
	}
	if fd.Icon != "" {
		out = append(out, e.useAs("@superschematic/schema", "icon", "schemaIcon")+fmt.Sprintf("(%s)", quote(fd.Icon)))
	}
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
	if fd.ConflictUnit != "" {
		out = append(out, e.use("conflictUnit")+fmt.Sprintf("(%s)", quote(fd.ConflictUnit)))
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
	rest.Title, rest.Purpose, rest.Icon = "", "", ""
	rest.Default = nil
	rest.ValidateMin, rest.ValidateMax = nil, nil
	rest.ValidateUploadMaxBytes = nil
	rest.ValidateMinLength, rest.ValidateMaxLength = nil, nil
	rest.ValidateListMin, rest.ValidateListMax = nil, nil
	rest.ValidatePattern = ""
	rest.Key, rest.Unique, rest.AutoGenerated = false, false, false
	rest.SearchField, rest.JsonField, rest.Secret = false, false, false
	rest.UIHidden, rest.Virtual, rest.Encrypted = false, false, false
	rest.SourceMustProject = false
	rest.TemporalFormat = ""
	rest.ConflictUnit = ""
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
