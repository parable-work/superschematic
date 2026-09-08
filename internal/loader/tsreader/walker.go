package tsreader

import (
	"errors"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/loader/verify"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// walker converts the AST of a service's schema files into the v2 Schema IR.
// One walker walks one service; it shares the service program's checker.
type walker struct {
	checker     *typeChecker
	packages    *packageIndex
	schema      *ir.Schema
	cfg         *SchemaConfig
	sp          *serviceProgram
	servicePath string

	// reg answers which packages are authoring packages, which decorators
	// exist for which target and what each schema kind permits. The walker
	// keeps no copy of any of it.
	reg *registry.Registry

	// classFields memoizes flattened field lists per class declaration so
	// heritage chains and @source targets resolve once.
	classFields map[*astNode][]*ir.FieldDef

	// imports accumulates cross-service named imports: package -> symbols.
	imports map[string]map[string]bool

	// externals collects the qualified names cross-service references use,
	// fed to Validate as known externals.
	externals map[string]bool

	// importSites collects every package import occurrence with its source
	// location, for the format-agnostic verification pass.
	importSites []verify.ImportSite

	// externalTypes collects the flattened definitions of cross-service
	// @source targets (keyed by service-qualified name), resolved through
	// the compiler for the verification pass.
	externalTypes map[string]*ir.TypeDef

	// externalEnums collects compiler-resolved dependency enums used by local
	// fields so composite defaults can validate serialized enum values.
	externalEnums map[string]*ir.EnumDef

	// suppressRecording disables import recording while resolving a foreign
	// class's fields (an @source target): only this schema's own references
	// belong in Imports.
	suppressRecording bool

	errs SchemaErrorList
}

func newWalker(sp *serviceProgram, cfg *SchemaConfig, schema *ir.Schema, packages *packageIndex, reg *registry.Registry) *walker {
	return &walker{
		checker:       sp.checker(),
		packages:      packages,
		schema:        schema,
		cfg:           cfg,
		sp:            sp,
		servicePath:   sp.servicePath,
		reg:           reg,
		classFields:   make(map[*astNode][]*ir.FieldDef),
		imports:       make(map[string]map[string]bool),
		externals:     make(map[string]bool),
		externalTypes: make(map[string]*ir.TypeDef),
		externalEnums: make(map[string]*ir.EnumDef),
	}
}

// addErr records a schema error and keeps walking, so authors see every
// mistake in one pass.
func (w *walker) addErr(serr *SchemaError) {
	if serr != nil {
		w.errs = append(w.errs, serr)
	}
}

// walkFile walks the top-level statements of one schema file.
func (w *walker) walkFile(file *astSourceFile) {
	for _, stmt := range file.Statements.Nodes {
		switch stmt.Kind {
		case kindImportDeclaration:
			w.checkImportDeclaration(stmt)
		case kindClassDeclaration:
			if isExported(stmt) {
				w.walkClass(stmt)
			}
		case kindEnumDeclaration:
			if isExported(stmt) {
				w.walkEnum(stmt)
			}
		case kindTypeAliasDeclaration:
			if isExported(stmt) {
				w.walkTypeAlias(stmt)
			}
		case kindVariableStatement:
			// Service sentinel constants; evaluated on demand.
		case kindInterfaceDeclaration:
			w.addErr(errorAtNode(stmt, "interfaces are not schema declarations; use an exported class"))
		}
	}
}

func isExported(node *astNode) bool {
	return hasSyntacticModifier(node, modifierFlagsExport)
}

// decoratorRef is one parsed decorator: its resolved identity and the raw
// argument expressions when it was invoked as a call.
type decoratorRef struct {
	id   symbolIdentity
	args []*astNode
	node *astNode
}

// kindSpec returns the registry's rules for this service's kind. An
// unregistered kind yields the zero spec: no operation sets, no @source
// role, embedded structs.
func (w *walker) kindSpec() registry.KindSpec {
	spec, _ := w.reg.Kind(string(w.cfg.Kind))
	return spec
}

// isAuthoring reports whether the identity was declared in a schema
// authoring package (Naming.AuthoringPackages plus every registered
// decorator's packages).
func (w *walker) isAuthoring(id symbolIdentity) bool {
	return w.reg.IsAuthoringPackage(id.pkg)
}

// siteOf converts a node's location into the registry's Site.
func (w *walker) siteOf(node *astNode) registry.Site {
	file, line, col := locationOfNode(node)
	return registry.Site{File: file, Line: line, Col: col}
}

// applyDecorator dispatches one decorator through the registry: the (name,
// target) lookup plus the declaring-package check replace the walker's old
// switch and its default arm; the kind gate reproduces the per-decorator kind
// messages; arguments are evaluated statically and handed to Apply. The
// returned diagnostic is nil when the decorator applied cleanly. Decorators
// registered without Apply are markers the walker reads itself.
func (w *walker) applyDecorator(d decoratorRef, target registry.DecoratorTarget, node registry.Node) *SchemaError {
	spec, ok := w.reg.Decorator(d.id.name, target)
	if !ok || !spec.DeclaredIn(d.id.pkg) {
		return errorAtNode(d.node, "decorator @%s is not valid on %s", d.id.name, target)
	}
	kind := string(w.cfg.Kind)
	if !spec.AllowsKind(kind) {
		return errorAtNode(d.node, "%s", spec.KindError(kind))
	}
	if spec.Apply == nil {
		return nil
	}
	args := make([]any, 0, len(d.args))
	for _, arg := range d.args {
		v, serr := w.evaluateExpression(arg)
		if serr != nil {
			return serr
		}
		args = append(args, v)
	}
	if err := spec.ValidateArgs(args); err != nil {
		return errorAtNode(d.node, "%s", err)
	}
	if err := spec.Apply(node, args, w.siteOf(d.node)); err != nil {
		var argErr *registry.ArgError
		if errors.As(err, &argErr) && argErr.Index >= 0 && argErr.Index < len(d.args) {
			return errorAtNode(d.args[argErr.Index], "%s", argErr.Msg)
		}
		return errorAtNode(d.node, "%s", err)
	}
	return nil
}

// versionedConfigFromDecorator reads @versioned({ ... }?). A nil config means
// the bare marker form was used and generators should keep default behavior.
func (w *walker) versionedConfigFromDecorator(d decoratorRef) (*ir.VersionedConfig, *SchemaError) {
	if len(d.args) == 0 {
		return nil, nil
	}
	if len(d.args) != 1 {
		return nil, errorAtNode(d.node, "@versioned takes at most one config object")
	}
	v, serr := w.evaluateExpression(d.args[0])
	if serr != nil {
		return nil, serr
	}
	cfg, ok := v.(map[string]any)
	if !ok {
		return nil, errorAtNode(d.node, "@versioned config must be an object literal")
	}

	out := &ir.VersionedConfig{}
	for key, value := range cfg {
		switch key {
		case "retentionDays":
			f, ok := value.(float64)
			if !ok {
				return nil, errorAtNode(d.node, "@versioned retentionDays must be a number literal")
			}
			n := int(f)
			out.RetentionDays = &n
		case "partitionBy":
			s, ok := value.(string)
			if !ok {
				return nil, errorAtNode(d.node, "@versioned partitionBy must be a string literal")
			}
			out.PartitionBy = s
		case "pruneKeepReferencedBy":
			// One reference or a list of them: a versioned table can be pinned
			// by several independent readers of its historical rows.
			refs, ok := value.([]any)
			if !ok {
				refs = []any{value}
			}
			for _, entry := range refs {
				ref, ok := entry.(map[string]any)
				if !ok {
					return nil, errorAtNode(d.node, "@versioned pruneKeepReferencedBy must be an object literal or an array of them")
				}
				pin := &ir.PruneReference{}
				for refKey, refValue := range ref {
					s, ok := refValue.(string)
					if !ok {
						return nil, errorAtNode(d.node, "@versioned pruneKeepReferencedBy %s must be a string literal", refKey)
					}
					switch refKey {
					case "table":
						pin.Table = s
					case "keyColumn":
						pin.KeyColumn = s
					case "versionColumn":
						pin.VersionColumn = s
					default:
						return nil, errorAtNode(d.node, "@versioned pruneKeepReferencedBy has unknown key %q", refKey)
					}
				}
				out.PruneKeepReferencedBy = append(out.PruneKeepReferencedBy, pin)
			}
		default:
			return nil, errorAtNode(d.node, "@versioned config has unknown key %q", key)
		}
	}
	return out, nil
}

// decoratorsOf parses and identity-resolves a node's decorators. Decorators
// that do not originate from an authoring package are schema errors.
func (w *walker) decoratorsOf(node *astNode) []decoratorRef {
	var out []decoratorRef
	for _, d := range node.Decorators() {
		expr := d.AsDecorator().Expression
		target := expr
		var args []*astNode
		if expr.Kind == kindCallExpression {
			call := expr.AsCallExpression()
			target = call.Expression
			if call.Arguments != nil {
				args = call.Arguments.Nodes
			}
		}
		id, ok := w.identityOf(target)
		if !ok {
			w.addErr(errorAtNode(d, "cannot resolve decorator"))
			continue
		}
		if !w.isAuthoring(id) {
			origin := "a toolchain package"
			if scope := w.reg.Naming().AuthoringScope(); scope != "" {
				origin = "a " + scope + " toolchain package"
			}
			w.addErr(errorAtNode(d, "decorator @%s does not come from %s", id.name, origin))
			continue
		}
		out = append(out, decoratorRef{id: id, args: args, node: d})
	}
	return out
}

func findDecorator(decorators []decoratorRef, name string) *decoratorRef {
	for i := range decorators {
		if decorators[i].id.name == name {
			return &decorators[i]
		}
	}
	return nil
}

// walkClass dispatches an exported class declaration to its role-specific
// walk based on its decorators, members, and the service kind.
func (w *walker) walkClass(node *astNode) {
	name := node.Name().Text()
	decorators := w.decoratorsOf(node)

	if classHasMethods(node) {
		if !w.kindSpec().AllowsOperationSets {
			w.addErr(errorAtNode(node, "operation sets (classes with methods) are only allowed in API schemas (this service is kind %s)", w.cfg.Kind))
			return
		}
		w.walkOperationSet(node, name, decorators)
		return
	}

	w.walkStructClass(node, name, decorators)
}

func classHasMethods(node *astNode) bool {
	for _, m := range node.AsClassDeclaration().Members.Nodes {
		if m.Kind == kindMethodDeclaration {
			return true
		}
	}
	return false
}

// structRole determines the TypeDef role for a plain (non-operation) class:
// @trait and @source pick the role directly, the "Input" suffix marks API
// inputs, and the kind's StructRole covers the rest.
func (w *walker) structRole(name string, decorators []decoratorRef) ir.Role {
	if findDecorator(decorators, "trait") != nil {
		return ir.RoleTrait
	}
	spec := w.kindSpec()
	if findDecorator(decorators, "source") != nil {
		if spec.SourceProjectionRole != "" {
			return spec.SourceProjectionRole
		}
		// The kind does not allow @source; the decorator loop reports it.
		return ir.RoleAPIView
	}
	if strings.HasSuffix(name, "Input") {
		return ir.RoleAPIInput
	}
	if spec.StructRole != "" {
		return spec.StructRole
	}
	return ir.RoleEmbeddedStruct
}

// walkStructClass walks a data-shaped class into a TypeDef.
func (w *walker) walkStructClass(node *astNode, name string, decorators []decoratorRef) {
	file := getSourceFileOfNode(node)
	td := &ir.TypeDef{
		Name:    name,
		Owner:   w.sp.relPath(file.FileName()),
		Comment: leadingComment(node),
		Role:    w.structRole(name, decorators),
	}
	td.IsTrait = td.Role == ir.RoleTrait
	if td.IsTrait {
		w.applyTraitConfig(node, td)
	}

	// Kind gates are reported before the heritage and field walks so a
	// misplaced @source is the first thing an author sees for the class.
	kind := string(w.cfg.Kind)
	kindRejected := make(map[*astNode]bool)
	for _, d := range decorators {
		spec, ok := w.reg.Decorator(d.id.name, registry.TargetType)
		if ok && spec.DeclaredIn(d.id.pkg) && !spec.AllowsKind(kind) {
			kindRejected[d.node] = true
			w.addErr(errorAtNode(d.node, "%s", spec.KindError(kind)))
		}
	}
	// Markers the walker reads itself, ahead of field resolution so a bad
	// @versioned config is reported even when a field fails to resolve.
	if findDecorator(decorators, "envVars") != nil {
		td.EnvVars = true
	}
	if versioned := findDecorator(decorators, "versioned"); versioned != nil {
		td.Versioned = true
		if cfg, serr := w.versionedConfigFromDecorator(*versioned); serr != nil {
			w.addErr(serr)
		} else {
			td.VersionedConfig = cfg
		}
	}

	w.applyHeritage(node, td)

	fields, ok := w.resolveClassFieldDefs(node)
	if !ok {
		return
	}
	td.Fields = fields

	for _, d := range decorators {
		if kindRejected[d.node] {
			continue
		}
		w.addErr(w.applyDecorator(d, registry.TargetType, registry.Node{Schema: w.schema, Type: td}))
	}

	if src := findDecorator(decorators, "source"); src != nil {
		w.applySource(td, src)
	}

	if _, exists := w.schema.Types[name]; exists {
		w.addErr(errorAtNode(node, "duplicate type %q", name))
		return
	}
	w.schema.Types[name] = td
}

// applyHeritage records raw heritage and flattening metadata (Extends and
// Implements) on the TypeDef. Field flattening itself happens in
// resolveClassFieldDefs.
func (w *walker) applyHeritage(node *astNode, td *ir.TypeDef) {
	raw := &ir.RawHeritage{}
	for _, clause := range heritageClauses(node) {
		hc := clause.AsHeritageClause()
		for _, t := range hc.Types.Nodes {
			text := w.nodeText(t)
			ewta := t.AsExpressionWithTypeArguments()
			if hc.Token == kindExtendsKeyword {
				raw.Extends = text
				id, ok := w.identityOf(ewta.Expression)
				if !ok {
					w.addErr(errorAtNode(t, "cannot resolve base class %q", text))
					continue
				}
				td.Extends = id.name
			} else {
				raw.Implements = append(raw.Implements, text)
				id, typeArgs, ok := w.traitHeritageTarget(t, true)
				if !ok {
					continue
				}
				ref := ir.TraitRef{Name: id.name}
				if len(typeArgs) > 0 {
					if len(typeArgs) > 1 {
						w.addErr(errorAtNode(t, "trait %s takes at most one Config type argument", id.name))
					} else if cfgArgs, serr := w.traitConfigArgs(typeArgs[0]); serr != nil {
						w.addErr(serr)
					} else {
						ref.ConfigArgs = cfgArgs
					}
				}
				td.Implements = append(td.Implements, ref)
			}
		}
	}
	if raw.Extends != "" || len(raw.Implements) > 0 {
		td.RawHeritage = raw
	}
}

// traitHeritageTarget resolves one implements-clause entry to the trait it
// names, returning the trait identity and its type arguments. Errors are
// reported only when report is true, so the heritage-recording pass and the
// field-flattening pass do not double-report the same clause.
//
// Two authoring forms exist. Marker and config-only traits have no members,
// so TypeScript accepts implementing them directly: `implements Reviewed` or
// `implements Tagged<{...}>`. Field-bearing traits cannot be implemented
// directly -- TypeScript demands implementers re-declare every member
// (TS2720), which contradicts trait flattening -- so they are named through
// the Trait<T> heritage carrier from @superschematic/schema: `implements
// Trait<SoftDeletable>`. The carrier erases to an empty object type for the
// compiler; here it unwraps to its type argument.
func (w *walker) traitHeritageTarget(t *astNode, report bool) (symbolIdentity, []*astNode, bool) {
	fail := func(node *astNode, format string, args ...any) (symbolIdentity, []*astNode, bool) {
		if report {
			w.addErr(errorAtNode(node, format, args...))
		}
		return symbolIdentity{}, nil, false
	}

	ewta := t.AsExpressionWithTypeArguments()
	id, ok := w.identityOf(ewta.Expression)
	if !ok {
		return fail(t, "cannot resolve implemented trait %q", w.nodeText(t))
	}
	var typeArgs []*astNode
	if args := ewta.TypeArguments; args != nil {
		typeArgs = args.Nodes
	}
	if !id.is("@superschematic/schema", "Trait") {
		return id, typeArgs, true
	}

	if len(typeArgs) != 1 {
		return fail(t, "Trait<T> takes exactly one trait type argument")
	}
	inner := typeArgs[0]
	if inner.Kind != kindTypeReference {
		return fail(inner, "the Trait<T> argument must name a trait class")
	}
	ref := inner.AsTypeReferenceNode()
	id, ok = w.identityOf(ref.TypeName)
	if !ok {
		return fail(inner, "cannot resolve implemented trait %q", w.nodeText(inner))
	}
	if ref.TypeArguments != nil {
		return id, ref.TypeArguments.Nodes, true
	}
	return id, nil, true
}

// applyTraitConfig captures a configurable trait's Config schema from its
// generic type parameter: `@trait class T<Config extends Shape>` records the
// flattened fields of Shape as the trait's TraitConfig. A trait without a
// type parameter is a marker or field-bearing trait and carries none.
func (w *walker) applyTraitConfig(node *astNode, td *ir.TypeDef) {
	tps := node.AsClassDeclaration().TypeParameters
	if tps == nil || len(tps.Nodes) == 0 {
		return
	}
	if len(tps.Nodes) > 1 {
		w.addErr(errorAtNode(node, "traits declare at most one Config type parameter"))
		return
	}
	tp := tps.Nodes[0].AsTypeParameterDeclaration()
	if tp.Constraint == nil {
		w.addErr(errorAtNode(tps.Nodes[0], "the trait Config type parameter must declare an extends constraint"))
		return
	}
	fields, ok := w.traitConfigFields(tp.Constraint)
	if !ok {
		return
	}
	td.TraitConfig = &ir.TraitConfigSchema{Fields: fields}
}

// traitConfigFields resolves a trait Config constraint to its field schema.
// The constraint is either the open TraitConfig shape from @superschematic/schema (no
// declared fields: any configuration is accepted), a schema class, an
// object-shaped type alias, or an inline object type.
func (w *walker) traitConfigFields(constraint *astNode) ([]*ir.FieldDef, bool) {
	switch constraint.Kind {
	case kindTypeLiteral:
		return w.fieldsFromTypeLiteral(constraint)

	case kindTypeReference:
		id, ok := w.identityOf(constraint.AsTypeReferenceNode().TypeName)
		if !ok {
			w.addErr(errorAtNode(constraint, "cannot resolve trait Config constraint"))
			return nil, false
		}
		if id.is("@superschematic/schema", "TraitConfig") {
			return nil, true
		}
		if id.decl != nil && id.decl.Kind == kindClassDeclaration {
			return w.resolveClassFieldDefs(id.decl)
		}
		if id.decl != nil && id.decl.Kind == kindTypeAliasDeclaration {
			alias := id.decl.AsTypeAliasDeclaration()
			if alias.Type.Kind == kindTypeLiteral {
				return w.fieldsFromTypeLiteral(alias.Type)
			}
		}
	}
	w.addErr(errorAtNode(constraint, "the trait Config constraint must be an object shape (a schema class, object type alias, or inline object type)"))
	return nil, false
}

// fieldsFromTypeLiteral converts an inline object type's property signatures
// into FieldDefs.
func (w *walker) fieldsFromTypeLiteral(node *astNode) ([]*ir.FieldDef, bool) {
	var fields []*ir.FieldDef
	ok := true
	for _, m := range node.AsTypeLiteralNode().Members.Nodes {
		if m.Kind != kindPropertySignature {
			w.addErr(errorAtNode(m, "object types may only contain property signatures"))
			ok = false
			continue
		}
		sig := m.AsPropertySignatureDeclaration()
		if sig.Type == nil {
			w.addErr(errorAtNode(m, "schema fields must have an explicit type annotation"))
			ok = false
			continue
		}
		info, serr := w.resolveTypeNode(sig.Type)
		if serr != nil {
			w.addErr(serr)
			ok = false
			continue
		}
		w.recordReference(info)
		optional := sig.PostfixToken != nil && sig.PostfixToken.Kind == kindQuestionToken
		fields = append(fields, &ir.FieldDef{
			Name:     m.Name().Text(),
			Comment:  leadingComment(m),
			TypeRef:  info.ref,
			Required: !info.nullable && !optional,
		})
	}
	return fields, ok
}

// traitConfigArgs reads the Config type argument supplied on an implements
// clause (`implements Tagged<{ channel: "email" }>`): an object type whose
// properties are literal types, resolved through the checker.
func (w *walker) traitConfigArgs(node *astNode) (map[string]any, *SchemaError) {
	t := w.checker.GetTypeFromTypeNode(node)
	if t == nil {
		return nil, errorAtNode(node, "cannot resolve trait config argument")
	}
	args := make(map[string]any)
	for _, prop := range w.checker.GetPropertiesOfType(t) {
		pt := w.checker.GetTypeOfSymbol(prop)
		v, ok := literalValue(pt)
		if !ok {
			return nil, errorAtNode(node, "trait config values must be literals (key %q)", prop.Name)
		}
		args[prop.Name] = v
	}
	return args, nil
}

func heritageClauses(node *astNode) []*astNode {
	clauses := node.AsClassDeclaration().HeritageClauses
	if clauses == nil {
		return nil
	}
	return clauses.Nodes
}

// resolveClassFieldDefs returns the flattened fields of a class: base-class
// and trait fields first (with InheritedFrom set to the declaring type), then
// the class's own properties. Results are memoized; heritage must stay within
// classes the program can see.
func (w *walker) resolveClassFieldDefs(node *astNode) ([]*ir.FieldDef, bool) {
	if fields, ok := w.classFields[node]; ok {
		return fields, true
	}
	// Pre-seed to break cycles; a cyclic extends chain is caught by the
	// compiler as a semantic diagnostic before the walk.
	w.classFields[node] = nil

	var fields []*ir.FieldDef
	ok := true

	for _, clause := range heritageClauses(node) {
		hc := clause.AsHeritageClause()
		for _, t := range hc.Types.Nodes {
			var id symbolIdentity
			var idOK bool
			if hc.Token == kindExtendsKeyword {
				id, idOK = w.identityOf(t.AsExpressionWithTypeArguments().Expression)
			} else {
				// Implements entries may name the trait through the
				// Trait<T> heritage carrier; unwrap to the trait class.
				// Errors were already reported by applyHeritage.
				id, _, idOK = w.traitHeritageTarget(t, false)
			}
			if !idOK {
				ok = false
				continue
			}
			// API operation-set marker bases carry no fields.
			if id.is("@superschematic/api", "Authenticated") || id.is("@superschematic/api", "Encrypted") {
				w.addErr(errorAtNode(t, "%s is an operation-set base and cannot be extended by a data type", id.name))
				ok = false
				continue
			}
			if id.decl == nil || id.decl.Kind != kindClassDeclaration {
				w.addErr(errorAtNode(t, "heritage target %q is not a class declaration", id.name))
				ok = false
				continue
			}
			// Foreign bases are TypeScript extends sugar: fields are flattened
			// into IR here. Suppress import recording so nested type refs on
			// the parent do not become codegen package dependencies.
			baseDeclFile := getSourceFileOfNode(id.decl)
			foreignBase := baseDeclFile != nil && !strings.HasPrefix(baseDeclFile.FileName(), w.servicePath+"/")
			prevSuppress := w.suppressRecording
			if foreignBase {
				w.suppressRecording = true
			}
			baseFields, baseOK := w.resolveClassFieldDefs(id.decl)
			w.suppressRecording = prevSuppress
			if !baseOK {
				ok = false
				continue
			}
			for _, bf := range baseFields {
				inherited := *bf
				if inherited.InheritedFrom == "" {
					inherited.InheritedFrom = id.name
				}
				fields = append(fields, &inherited)
			}
		}
	}

	for _, m := range node.AsClassDeclaration().Members.Nodes {
		if m.Kind != kindPropertyDeclaration {
			w.addErr(errorAtNode(m, "only property declarations are allowed on data types"))
			ok = false
			continue
		}
		fd, serr := w.fieldFromProperty(m)
		if serr != nil {
			w.addErr(serr)
			ok = false
			continue
		}
		fields = append(fields, fd)
	}

	w.classFields[node] = fields
	return fields, ok
}

// fieldFromProperty converts a property declaration into a FieldDef.
func (w *walker) fieldFromProperty(member *astNode) (*ir.FieldDef, *SchemaError) {
	prop := member.AsPropertyDeclaration()
	if prop.Type == nil {
		return nil, errorAtNode(member, "schema fields must have an explicit type annotation")
	}

	info, serr := w.resolveTypeNode(prop.Type)
	if serr != nil {
		return nil, serr
	}
	w.recordReference(info)

	optional := prop.PostfixToken != nil && prop.PostfixToken.Kind == kindQuestionToken
	platformDefault := ""
	if info.platformDefault {
		platformDefault = info.ref.Name
	}
	fd := &ir.FieldDef{
		Name:            member.Name().Text(),
		Comment:         leadingComment(member),
		TypeRef:         info.ref,
		Required:        !info.nullable && !optional,
		Default:         info.defaultValue,
		PlatformDefault: platformDefault,
		AutoGenerated:   info.autoGenerated,
		JsonField:       info.jsonField,
		HasMany:         info.hasMany,
		ManyToMany:      info.manyToMany,
		Secret:          info.secret,
		Encrypted:       info.encrypted,
	}
	if info.relation {
		fd.Relation = &ir.RelationDef{Type: info.ref.Name, OnDelete: info.relationOnDelete}
	}
	applyValidateConfig(info.validate, fd)

	for _, d := range w.decoratorsOf(member) {
		if serr := w.applyDecorator(d, registry.TargetField, registry.Node{Schema: w.schema, Field: fd}); serr != nil {
			return nil, serr
		}
	}
	return fd, nil
}

// applyValidateConfig maps the Validate<T, C> config keys onto FieldDef
// validation fields.
func applyValidateConfig(cfg map[string]any, fd *ir.FieldDef) {
	if cfg == nil {
		return
	}
	if v, ok := cfg["min"].(float64); ok {
		fd.ValidateMin = &v
	}
	if v, ok := cfg["max"].(float64); ok {
		fd.ValidateMax = &v
	}
	if v, ok := cfg["minLength"].(float64); ok {
		n := int(v)
		fd.ValidateMinLength = &n
	}
	if v, ok := cfg["maxLength"].(float64); ok {
		n := int(v)
		fd.ValidateMaxLength = &n
	}
	if v, ok := cfg["listMin"].(float64); ok {
		n := int(v)
		fd.ValidateListMin = &n
	}
	if v, ok := cfg["listMax"].(float64); ok {
		n := int(v)
		fd.ValidateListMax = &n
	}
	if v, ok := cfg["pattern"].(string); ok {
		fd.ValidatePattern = v
	}
}

// applySource resolves @source(Target): the cross-layer projection linkage.
// The walk records the SourceRef and the resolved (flattened) source type;
// the structural verification -- assignability, the @virtual requirement,
// the @sourceMustProject warning -- is the format-agnostic validation
// pass's job.
func (w *walker) applySource(td *ir.TypeDef, src *decoratorRef) {
	if len(src.args) != 1 {
		w.addErr(errorAtNode(src.node, "@source takes exactly one type argument"))
		return
	}
	id, ok := w.identityOf(src.args[0])
	if !ok || id.decl == nil || id.decl.Kind != kindClassDeclaration {
		w.addErr(errorAtNode(src.node, "@source target must be a schema class"))
		return
	}

	targetService := w.cfg.Name
	declFile := getSourceFileOfNode(id.decl)
	external := declFile != nil && !strings.HasPrefix(declFile.FileName(), w.servicePath+"/")
	if external {
		if id.pkg == "" {
			w.addErr(errorAtNode(src.node, "@source target %q is declared outside any named package", id.name))
			return
		}
		targetService = serviceNameForPackage(id.pkg)
		// @source is compile-time lineage only: do not record the target into
		// schema.Imports / generated package dependencies. TypeScript already
		// resolves the authoring import; ExternalTypes + SourceRef carry
		// verification data.
	}

	prevSuppress := w.suppressRecording
	w.suppressRecording = true
	sourceFields, _ := w.resolveClassFieldDefs(id.decl)
	w.suppressRecording = prevSuppress

	target := targetService + "." + id.name
	if external {
		w.externalTypes[target] = &ir.TypeDef{Name: id.name, Fields: sourceFields}
	}

	sourceByName := make(map[string]*ir.FieldDef, len(sourceFields))
	for _, f := range sourceFields {
		sourceByName[f.Name] = f
	}

	ref := &ir.SourceRef{Target: target}
	viewNames := make(map[string]bool, len(td.Fields))
	for _, f := range td.Fields {
		viewNames[f.Name] = true
		if f.Virtual {
			ref.Virtual = append(ref.Virtual, f.Name)
		}
	}
	for _, f := range sourceFields {
		if !viewNames[f.Name] {
			ref.OmittedFromSource = append(ref.OmittedFromSource, f.Name)
		}
	}
	td.Source = ref
}

// walkEnum converts an exported enum declaration into an EnumDef. Only
// string-valued enums are schema enums.
func (w *walker) walkEnum(node *astNode) {
	enum := node.AsEnumDeclaration()
	name := node.Name().Text()
	def := &ir.EnumDef{
		Name:    name,
		Owner:   w.cfg.Name,
		Comment: leadingComment(node),
	}
	for _, m := range enum.Members.Nodes {
		member := m.AsEnumMember()
		valueName := m.Name().Text()
		value := ir.EnumValueDef{Name: valueName, Comment: leadingComment(m)}
		if member.Initializer != nil {
			if member.Initializer.Kind != kindStringLiteral {
				w.addErr(errorAtNode(m, "enum values must be string literals"))
				continue
			}
			if s := member.Initializer.Text(); s != valueName {
				value.SerializedAs = s
			}
		}
		def.Values = append(def.Values, value)
	}
	if _, exists := w.schema.Enums[name]; exists {
		w.addErr(errorAtNode(node, "duplicate enum %q", name))
		return
	}
	w.schema.Enums[name] = def
}

// walkTypeAlias converts an exported object-shaped type alias into an
// EmbeddedStruct TypeDef. Other alias forms are not schema declarations.
func (w *walker) walkTypeAlias(node *astNode) {
	alias := node.AsTypeAliasDeclaration()
	name := node.Name().Text()
	if alias.Type.Kind != kindTypeLiteral {
		w.addErr(errorAtNode(node, "type aliases must be object shapes; use an enum or class for other forms"))
		return
	}
	file := getSourceFileOfNode(node)
	td := &ir.TypeDef{
		Name:    name,
		Owner:   w.sp.relPath(file.FileName()),
		Comment: leadingComment(node),
		Role:    ir.RoleEmbeddedStruct,
	}
	for _, m := range alias.Type.AsTypeLiteralNode().Members.Nodes {
		if m.Kind != kindPropertySignature {
			w.addErr(errorAtNode(m, "object type aliases may only contain property signatures"))
			continue
		}
		sig := m.AsPropertySignatureDeclaration()
		if sig.Type == nil {
			w.addErr(errorAtNode(m, "schema fields must have an explicit type annotation"))
			continue
		}
		info, serr := w.resolveTypeNode(sig.Type)
		if serr != nil {
			w.addErr(serr)
			continue
		}
		w.recordReference(info)
		optional := sig.PostfixToken != nil && sig.PostfixToken.Kind == kindQuestionToken
		td.Fields = append(td.Fields, &ir.FieldDef{
			Name:     m.Name().Text(),
			Comment:  leadingComment(m),
			TypeRef:  info.ref,
			Required: !info.nullable && !optional,
		})
	}
	if _, exists := w.schema.Types[name]; exists {
		w.addErr(errorAtNode(node, "duplicate type %q", name))
		return
	}
	w.schema.Types[name] = td
}

// walkOperationSet converts a class with methods into an OperationSet.
func (w *walker) walkOperationSet(node *astNode, name string, decorators []decoratorRef) {
	set := &ir.OperationSet{
		Name:    name,
		Comment: leadingComment(node),
	}

	auth := false
	for _, clause := range heritageClauses(node) {
		hc := clause.AsHeritageClause()
		for _, t := range hc.Types.Nodes {
			id, ok := w.identityOf(t.AsExpressionWithTypeArguments().Expression)
			if !ok {
				w.addErr(errorAtNode(t, "cannot resolve operation-set base"))
				continue
			}
			switch {
			case id.is("@superschematic/api", "Authenticated"):
				auth = true
			case id.is("@superschematic/api", "Encrypted"):
				set.Encrypted = true
			default:
				w.addErr(errorAtNode(t, "operation sets may only extend Authenticated or Encrypted from @superschematic/api"))
			}
		}
	}

	for _, d := range decorators {
		w.addErr(w.applyDecorator(d, registry.TargetOperationSet, registry.Node{Schema: w.schema, OperationSet: set}))
	}

	for _, m := range node.AsClassDeclaration().Members.Nodes {
		if m.Kind != kindMethodDeclaration {
			w.addErr(errorAtNode(m, "operation sets may only contain methods"))
			continue
		}
		op, serr := w.operationFromMethod(m)
		if serr != nil {
			w.addErr(serr)
			continue
		}
		op.Auth = op.Auth || auth
		set.Operations = append(set.Operations, op)
	}

	w.schema.OperationSets = append(w.schema.OperationSets, set)
}

// operationFromMethod converts one method declaration into an operation
// FieldDef. Every operation carries its HTTP method explicitly via @rest;
// there is no Queries-versus-Mutations inference in v2.
func (w *walker) operationFromMethod(m *astNode) (*ir.FieldDef, *SchemaError) {
	method := m.AsMethodDeclaration()
	if method.Type == nil {
		return nil, errorAtNode(m, "operations must declare an explicit return type")
	}
	info, serr := w.resolveTypeNode(method.Type)
	if serr != nil {
		return nil, serr
	}
	w.recordReference(info)

	op := &ir.FieldDef{
		Name:      m.Name().Text(),
		Comment:   leadingComment(m),
		TypeRef:   info.ref,
		Required:  !info.nullable,
		Encrypted: info.encrypted,
	}

	for _, d := range w.decoratorsOf(m) {
		serr := w.applyDecorator(d, registry.TargetOperation, registry.Node{Schema: w.schema, Field: op})
		if serr == nil {
			continue
		}
		// The middleware trio records its error and keeps the operation, so
		// a missing @rest on the same method is still reported.
		if spec, ok := w.reg.Decorator(d.id.name, registry.TargetOperation); ok && spec.RecordsErrors() {
			w.addErr(serr)
			continue
		}
		return nil, serr
	}

	if op.HTTPMethod == "" && !op.ManualRouteRegistration {
		return nil, errorAtNode(m, "operation %s needs @rest(method, path?) or @manualRouteRegistration", op.Name)
	}

	if method.Parameters != nil {
		for _, p := range method.Parameters.Nodes {
			arg, serr := w.argumentFromParameter(p)
			if serr != nil {
				return nil, serr
			}
			op.Arguments = append(op.Arguments, arg)
		}
	}
	return op, nil
}

// argumentFromParameter converts one method parameter into an ArgumentDef.
// Older schema files may prefix intentionally-unused stub parameters with "_";
// keep stripping that prefix so they load the same as the preferred names.
func (w *walker) argumentFromParameter(p *astNode) (*ir.ArgumentDef, *SchemaError) {
	param := p.AsParameterDeclaration()
	if param.Type == nil {
		return nil, errorAtNode(p, "operation parameters must have an explicit type annotation")
	}
	info, serr := w.resolveTypeNode(param.Type)
	if serr != nil {
		return nil, serr
	}
	if info.platformDefault {
		return nil, errorAtNode(p, "PlatformDefault<T> is only valid on object fields")
	}
	w.recordReference(info)

	arg := &ir.ArgumentDef{
		Name:     strings.TrimPrefix(p.Name().Text(), "_"),
		TypeRef:  info.ref,
		Required: !info.nullable && param.QuestionToken == nil,
		Default:  info.defaultValue,
		IsQuery:  info.queryParam,
	}
	if info.validate != nil {
		fd := &ir.FieldDef{}
		applyValidateConfig(info.validate, fd)
		arg.ValidateMin = fd.ValidateMin
		arg.ValidateMax = fd.ValidateMax
		arg.ValidateMinLength = fd.ValidateMinLength
		arg.ValidateMaxLength = fd.ValidateMaxLength
		arg.ValidateListMin = fd.ValidateListMin
		arg.ValidateListMax = fd.ValidateListMax
		arg.ValidatePattern = fd.ValidatePattern
	}
	return arg, nil
}

// recordReference records cross-service imports and known-external names off
// a resolved type reference.
func (w *walker) recordReference(info *typeInfo) {
	if w.suppressRecording || info == nil || info.importPkg == "" {
		return
	}
	w.recordImportSymbol(info.importPkg, info.importedName)
	w.externals[info.ref.Name] = true
}

func (w *walker) recordImportSymbol(pkg, symbol string) {
	if w.imports[pkg] == nil {
		w.imports[pkg] = make(map[string]bool)
	}
	w.imports[pkg][symbol] = true
	w.externals[serviceNameForPackage(pkg)+"."+symbol] = true
}

// finishImports converts the accumulated cross-service references into the
// schema's named Imports, sorted for determinism.
func (w *walker) finishImports() {
	pkgs := make([]string, 0, len(w.imports))
	for pkg := range w.imports {
		pkgs = append(pkgs, pkg)
	}
	sort.Strings(pkgs)
	for _, pkg := range pkgs {
		symbols := make([]string, 0, len(w.imports[pkg]))
		for s := range w.imports[pkg] {
			symbols = append(symbols, s)
		}
		sort.Strings(symbols)
		w.schema.Imports = append(w.schema.Imports, ir.Import{Package: pkg, Types: symbols})
	}
}

// nodeText renders a node's source text as written.
func (w *walker) nodeText(node *astNode) string {
	file := getSourceFileOfNode(node)
	if file == nil {
		return ""
	}
	text := file.Text()
	start := skipTrivia(text, node.Pos())
	end := node.End()
	if start > end || end > len(text) {
		return ""
	}
	return strings.TrimSpace(text[start:end])
}
