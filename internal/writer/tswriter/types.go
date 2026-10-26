package tswriter

import (
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	ir "github.com/parable-work/superschematic/ir"
)

// emitEnum renders an exported string enum, a member's title as a JSDoc
// @title tag after its comment (D48, amended).
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
		if holdsTitleTag(v.Comment) {
			e.failf("enum %s.%s: a comment that holds @title has no TypeScript authoring form: the reader reads the tag only as the member's title", def.Name, v.Name)
		}
		e.comment("  ", v.Comment)
		if v.Title != "" {
			// Validation holds a title to one line without "*/", so the
			// tag's comment holds it. It goes after the comment, so a
			// reread keeps the comment's lines in order.
			fmt.Fprintf(&e.body, "  /** @title %s */\n", v.Title)
		}
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

// holdsTitleTag reports whether a member's comment holds the @title tag,
// which the TypeScript reader refuses anywhere in a member's comments but
// as the tag the writer writes (tsreader.titleAt): "@title" with no
// identifier character or "@" before it, and no identifier character or
// "-" after it. "@titles" and "a@title" are comment text.
func holdsTitleTag(comment string) bool {
	const tag = "@title"
	for from := 0; ; {
		i := strings.Index(comment[from:], tag)
		if i < 0 {
			return false
		}
		i += from
		before, _ := utf8.DecodeLastRuneInString(comment[:i])
		after, _ := utf8.DecodeRuneInString(comment[i+len(tag):])
		if (i == 0 || !tagNameRune(before) && before != '@') &&
			(i+len(tag) == len(comment) || !tagNameRune(after) && after != '-') {
			return true
		}
		from = i + 1
	}
}

// tagNameRune reports whether r may continue a JavaScript identifier, as
// far as a JSDoc tag's name is concerned.
func tagNameRune(r rune) bool {
	return r == '_' || r == '$' || unicode.IsLetter(r) || unicode.IsDigit(r)
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
		if e.doc.Kind == ir.SchemaKindDB && def.Queue == nil {
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
	case ir.RoleAPIInput:
		// The TypeScript reader gives a class whose name ends in Input this
		// role, and no other.
		if !strings.HasSuffix(def.Name, "Input") {
			e.failf("type %s: role APIInput is only expressible in TypeScript on a class whose name ends in Input", def.Name)
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
		len(def.Indexes) > 0 || len(def.RawSQL) > 0 || def.JsonField || def.EnvVars ||
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
		var options []string
		if idx.Unique {
			options = append(options, "unique: true")
		}
		for _, opt := range []struct{ key, value string }{
			{"name", idx.Name}, {"method", idx.Method}, {"ops", idx.Ops}, {"where", idx.Where}, {"expression", idx.Expression},
		} {
			if opt.value != "" {
				options = append(options, opt.key+": "+quote(opt.value))
			}
		}
		if idx.Concurrently != nil {
			options = append(options, fmt.Sprintf("concurrently: %t", *idx.Concurrently))
		}
		opts := ""
		switch {
		case len(options) == 1 && idx.Unique:
			opts = ", true"
		case len(options) > 0:
			opts = ", { " + strings.Join(options, ", ") + " }"
		}
		fmt.Fprintf(&e.body, "@%s<%s>([%s]%s)\n", e.use("index"), def.Name, strings.Join(keys, ", "), opts)
	}
	for _, raw := range def.RawSQL {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("rawSql"), rawSQLArgs(raw))
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
	if def.Queue != nil {
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("queue"), queueArgs(def.Queue))
	}
	for _, change := range def.EnqueueOnChange {
		// The queue may be declared in another file of the schema.
		e.recordHeritageImports(change.Queue)
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("enqueueOnChange"), enqueueOnChangeArgs(change))
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

	// A class with no fields of its own, such as each class of a Stack
	// schema, closes on its line, as the @job and @worker classes do.
	if !slices.ContainsFunc(def.Fields, func(fd *ir.FieldDef) bool { return fd.InheritedFrom == "" }) {
		fmt.Fprintf(&e.body, "export abstract class %s%s {}\n", e.ident(def.Name, "type"), e.heritageClause(def))
		return
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
	text("icon", d.Icon)
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
	if members := fieldDisplayMembers(fd.Display); members != "" {
		out = append(out, e.use("display")+"({ "+members+" })")
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
	if fd.UIControl != "" {
		out = append(out, e.use("uiControl")+fmt.Sprintf("(%s)", quote(fd.UIControl)))
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

// fieldDisplayMembers is a field's @display argument's members (D48,
// amended), in the IR's order: multiline, then currency, a code or
// { field }; empty for a display that declares neither.
func fieldDisplayMembers(display *ir.FieldDisplay) string {
	if display == nil {
		return ""
	}
	var members []string
	if display.Multiline != nil {
		members = append(members, fmt.Sprintf("multiline: %t", *display.Multiline))
	}
	if c := display.Currency; c != nil {
		if c.Code != "" {
			members = append(members, "currency: "+quote(c.Code))
		} else {
			members = append(members, "currency: { field: "+quote(c.Field)+" }")
		}
	}
	return strings.Join(members, ", ")
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
	if fieldDisplayMembers(rest.Display) != "" {
		rest.Display = nil
	}
	rest.Default = nil
	rest.ValidateMin, rest.ValidateMax = nil, nil
	rest.ValidateUploadMaxBytes = nil
	rest.ValidateMinLength, rest.ValidateMaxLength = nil, nil
	rest.ValidateListMin, rest.ValidateListMax = nil, nil
	rest.ValidatePattern = ""
	rest.Key, rest.Unique, rest.AutoGenerated = false, false, false
	rest.SearchField, rest.JsonField, rest.Secret = false, false, false
	rest.UIHidden, rest.Virtual, rest.Encrypted = false, false, false
	rest.UIControl = ""
	rest.SourceMustProject = false
	rest.TemporalFormat = ""
	rest.ConflictUnit = ""
	rest.Relation = nil
	rest.References = nil
	rest.HasMany, rest.ManyToMany = false, false
	if !isZeroField(&rest) {
		e.failf("%s: carries metadata with no TypeScript authoring form on a data field", owner)
	}
}

// isZeroField reports whether a FieldDef has been fully cleared.
func isZeroField(fd *ir.FieldDef) bool {
	return reflect.DeepEqual(fd, &ir.FieldDef{})
}

// rawSQLArgs renders @rawSql's options object, its SQL as template literals
// when they span lines (D83).
func rawSQLArgs(raw ir.RawSQLDef) string {
	sql := func(s string) string {
		if strings.Contains(s, "\n") && !strings.ContainsAny(s, "`\\") && !strings.Contains(s, "${") {
			return "`" + s + "`"
		}
		return quote(s)
	}
	parts := []string{"id: " + quote(raw.ID), "up: " + sql(raw.Up)}
	if raw.Down != "" {
		parts = append(parts, "down: "+sql(raw.Down))
	}
	if raw.After != "" {
		parts = append(parts, "after: "+quote(raw.After))
	}
	if len(raw.Dialects) > 0 {
		dialects := make([]string, len(raw.Dialects))
		for i, d := range raw.Dialects {
			dialects[i] = quote(d)
		}
		parts = append(parts, "dialects: ["+strings.Join(dialects, ", ")+"]")
	}
	return "{ " + strings.Join(parts, ", ") + " }"
}
