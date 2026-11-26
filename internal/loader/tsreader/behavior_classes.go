package tsreader

import (
	"encoding/json"
	"fmt"
	"path"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
)

// The decorators, members and tags a behavior's class is written with
// (D103), all from @superschematic/db: packages/db/src/behaviorclass.ts
// says what each means.
const (
	declaresBehaviorDecorator = "declaresBehavior"
	valueFieldDecorator       = "valueField"
	fromCreateDecorator       = "fromCreate"
	computedDecorator         = "computed"
	volatileDecorator         = "volatile"
	perReaderDecorator        = "perReader"
	subTableDecorator         = "subTable"

	// The methods that declare no operation: what a create gives the
	// behavior, and the precondition a caller presents it.
	createMethod       = "create"
	preconditionMethod = "precondition"

	// BehaviorClassSuffix ends the name of a file of behavior classes.
	BehaviorClassSuffix = ".behavior.ts"
)

// The JSDoc tags of an abstract method of a behavior's class.
const (
	tagWrites     = "@writes"
	tagScope      = "@scope"
	tagBeside     = "@beside"
	tagLog        = "@log"
	tagAsks       = "@asks"
	tagInvocation = "@invocation"
	tagChecks     = "@checks"
	tagReturns    = "@returns"
	tagParam      = "@param"
	tagEvent      = "@event"
	tagSignal     = "@signal"
)

// BehaviorClass is a behavior an abstract class declares (D103): the file
// and class that declare it, the declaration the build derives from it,
// the one the behaviors' core reads, and the Rust crate whose hooks run it
// on the core, when the class names one.
type BehaviorClass struct {
	// File is the class's file, relative to the directory read.
	File string
	// Class is the class's name.
	Class string
	// Declaration is the declaration derived from the class.
	Declaration registry.BehaviorDeclaration
	// Rules is the crate the class's rules option names; the zero value
	// when it names none.
	Rules registry.BehaviorRust
}

// ReadBehaviorClasses reads the behaviors the classes of dir's
// *.behavior.ts files declare (D103), in file and then declaration order.
// The directory holds a tsconfig.json, as a service's does, which resolves
// @superschematic/db and superscalar; a type error in a file is a schema
// error, as in a schema file. Each exported class marked @declaresBehavior
// is read into its declaration; a file's other declarations are the types
// the classes name. reg answers which packages are authoring packages
// (nil: the core's registry). A class that does not read is an error at
// its node, and every one is reported.
func ReadBehaviorClasses(dir string, reg *registry.Registry) ([]BehaviorClass, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, fmt.Errorf("behavior classes: %w", err)
	}
	root := filepath.ToSlash(abs)
	if reg == nil {
		reg = coreRegistry()
	}
	program, configDiags, err := newCorsaProgram(root, nil)
	if err != nil {
		return nil, fmt.Errorf("behavior classes in %s: %w", dir, err)
	}
	if len(configDiags) > 0 {
		return nil, fromDiagnostics(configDiags)
	}
	defer program.close()
	var files []*astSourceFile
	for _, file := range program.sourceFiles() {
		name := file.FileName()
		if path.Dir(name) == root && strings.HasSuffix(name, BehaviorClassSuffix) {
			files = append(files, file)
		}
	}
	sort.Slice(files, func(i, j int) bool { return files[i].FileName() < files[j].FileName() })
	var errs SchemaErrorList
	for _, file := range files {
		errs = append(errs, fromDiagnostics(program.syntacticDiagnostics(file))...)
	}
	if len(errs) == 0 {
		for _, file := range files {
			errs = append(errs, fromDiagnostics(program.semanticDiagnostics(file))...)
		}
	}
	if len(errs) > 0 {
		return nil, errs
	}
	w := &walker{checker: program.checker, packages: newPackageIndex(), reg: reg}
	var out []BehaviorClass
	for _, file := range files {
		for _, stmt := range file.Statements.Nodes {
			if stmt.Kind != kindClassDeclaration {
				continue
			}
			marker := findDecorator(w.decoratorsOf(stmt), declaresBehaviorDecorator)
			if marker == nil || marker.id.pkg != behaviorFieldPackage {
				continue
			}
			r := &behaviorClassReader{w: w}
			class := r.read(stmt, *marker)
			class.File = strings.TrimPrefix(file.FileName(), root+"/")
			errs = append(errs, r.errs...)
			out = append(out, class)
		}
	}
	errs = append(errs, w.errs...)
	if len(errs) > 0 {
		return nil, errs
	}
	return out, nil
}

// behaviorClassReader reads one behavior's class.
type behaviorClassReader struct {
	w    *walker
	errs SchemaErrorList
	// params are the class's type parameters, the table and the config,
	// which a schema's types may not name.
	params map[string]bool
	// at is the config path of the member being read (registry's
	// ConfigPath, by segment), and refs, while the config is read, the
	// references its members' types mark (Names, Hears); nil otherwise.
	at   []string
	refs *[]registry.BehaviorReference
}

func (r *behaviorClassReader) fail(node *astNode, format string, args ...any) {
	r.errs = append(r.errs, errorAtNode(node, format, args...))
}

// read reads the class node, marked with @declaresBehavior (marker).
func (r *behaviorClassReader) read(node *astNode, marker decoratorRef) BehaviorClass {
	class := node.AsClassDeclaration()
	name := node.Name().Text()
	out := BehaviorClass{Class: name}
	decl := &out.Declaration
	decl.Name = name
	if !isExported(node) || !hasSyntacticModifier(node, modifierFlagsAbstract) {
		r.fail(node, "behavior class %s: a behavior is declared by an exported abstract class (D103)", name)
	}
	doc := readDoc(node)
	decl.Description = doc.text
	r.readOptions(name, marker, &out)
	// The contracts it implements come first, then those it asks.
	asks := decl.Contracts
	decl.Contracts = nil

	// The config is the last type parameter's constraint; a first of two
	// is the table the behavior sits on.
	r.params = map[string]bool{}
	if class.TypeParameters != nil {
		params := class.TypeParameters.Nodes
		for _, p := range params {
			r.params[p.Name().Text()] = true
		}
		if len(params) > 2 {
			r.fail(params[2], "behavior class %s takes at most two type parameters: the table it sits on and its config (D103)", name)
		}
		last := params[len(params)-1].AsTypeParameterDeclaration()
		if last.Constraint == nil {
			if len(params) == 2 {
				r.fail(params[1], "behavior class %s: its config, the type parameter %s, needs a constraint, the config's type: %s extends { ... }", name, params[1].Name().Text(), params[1].Name().Text())
			}
		} else {
			// The config's members that name a part of the table, marked
			// by their types (Names<T, R>, Hears<T>), are its references.
			var refs []registry.BehaviorReference
			r.refs, r.at = &refs, nil
			schema, ok := r.schema(last.Constraint)
			r.refs = nil
			if ok {
				decl.ConfigSchema = r.raw(last.Constraint, schema)
				decl.References = refs
			}
		}
	}

	for _, clause := range heritageClauses(node) {
		hc := clause.AsHeritageClause()
		for _, t := range hc.Types.Nodes {
			expr := t.AsExpressionWithTypeArguments().Expression
			id, ok := r.w.identityOf(expr)
			if hc.Token != kindImplementsKeyword || !ok || id.pkg != behaviorFieldPackage || !slices.Contains(registry.Contracts, id.name) {
				r.fail(t, "behavior class %s: a behavior's class implements contracts alone, from @superschematic/db (%s), and extends nothing (D103, D104)", name, strings.Join(registry.Contracts, ", "))
				continue
			}
			decl.Contracts = append(decl.Contracts, registry.BehaviorContract{Name: id.name, Role: registry.ContractRoleImplements, Description: readDoc(t).text})
		}
	}
	decl.Contracts = append(decl.Contracts, asks...)

	for _, member := range class.Members.Nodes {
		switch member.Kind {
		case kindPropertyDeclaration:
			prop := member.AsPropertyDeclaration()
			nested := prop.Initializer != nil && prop.Initializer.Kind == kindClassExpression
			switch {
			case nested && isStatic(member):
				r.readTable(name, member, decl)
			case nested || isStatic(member):
				r.fail(member, "behavior class %s: %s: a sub-table is a static nested class, static %s = class { ... }, and a field an instance property with no value (D103)", name, member.Name().Text(), member.Name().Text())
			default:
				r.readField(name, member, decl)
			}
		case kindMethodDeclaration:
			r.readMethod(name, member, decl)
		default:
			r.fail(member, "behavior class %s: a member is a field, a sub-table or an abstract method (D103)", name)
		}
	}
	if decl.Value == "" {
		for _, field := range decl.Fields {
			if field.Optional || field.FromCreate {
				r.fail(node, "behavior class %s: field %s is optional or given by a create, which only the value field may be: mark it @valueField", name, field.Name)
			}
		}
	}
	return out
}

// readOptions reads @declaresBehavior's options into out.
func (r *behaviorClassReader) readOptions(class string, marker decoratorRef, out *BehaviorClass) {
	if len(marker.args) == 0 {
		return
	}
	if len(marker.args) > 1 || marker.args[0].Kind != kindObjectLiteralExpression {
		r.fail(marker.node, "behavior class %s: @declaresBehavior takes one options object", class)
		return
	}
	decl := &out.Declaration
	for _, p := range marker.args[0].AsObjectLiteralExpression().Properties.Nodes {
		if p.Kind != kindPropertyAssignment {
			r.fail(p, "behavior class %s: @declaresBehavior's options are plain property assignments", class)
			continue
		}
		key := p.Name().Text()
		init := p.AsPropertyAssignment().Initializer
		if key == "vetoes" {
			decl.Vetoes = r.vetoes(class, init)
			continue
		}
		value, serr := r.w.evaluateExpression(init)
		if serr != nil {
			r.errs = append(r.errs, serr)
			continue
		}
		bad := func(what string) { r.fail(init, "behavior class %s: @declaresBehavior's %s is %s", class, key, what) }
		switch key {
		case "name":
			if s, ok := value.(string); ok && s != "" {
				decl.Name = s
			} else {
				bad("the behavior's name, a string")
			}
		case "requires", "conflicts":
			names, ok := stringList(value)
			if !ok {
				bad("a list of behavior names")
			} else if key == "requires" {
				decl.Requires = names
			} else {
				decl.Conflicts = names
			}
		case "armsDeadlines", "hearsChanges":
			if value != true {
				bad("true, or left out")
			} else if key == "armsDeadlines" {
				decl.ArmsDeadlines = true
			} else {
				decl.HearsChanges = true
			}
		case "asks":
			asks, ok := value.(map[string]any)
			if !ok {
				bad("the contracts it asks, by name, each with what it means here")
				continue
			}
			for _, contract := range sortedAnyKeys(asks) {
				text, _ := asks[contract].(string)
				decl.Contracts = append(decl.Contracts, registry.BehaviorContract{Name: contract, Role: registry.ContractRoleAsks, Description: text})
			}
		case "rules":
			crate, ok := value.(map[string]any)
			if !ok {
				bad("{ crate, path, register }")
				continue
			}
			out.Rules.Crate, _ = crate["crate"].(string)
			out.Rules.Path, _ = crate["path"].(string)
			out.Rules.Register, _ = crate["register"].(string)
		default:
			r.fail(p, "behavior class %s: @declaresBehavior has no option %s (name, requires, conflicts, armsDeadlines, hearsChanges, vetoes, asks, rules)", class, key)
		}
	}
}

// vetoes reads the enum @declaresBehavior's vetoes names: each member a
// code, in order, its JSDoc its description.
func (r *behaviorClassReader) vetoes(class string, init *astNode) []registry.BehaviorVeto {
	id, ok := r.w.identityOf(init)
	if !ok || id.decl == nil || id.decl.Kind != kindEnumDeclaration {
		r.fail(init, "behavior class %s: @declaresBehavior's vetoes names an enum, each member a code its refusals carry", class)
		return nil
	}
	var out []registry.BehaviorVeto
	for _, m := range id.decl.AsEnumDeclaration().Members.Nodes {
		if m.AsEnumMember().Initializer != nil {
			r.fail(m, "behavior class %s: a veto's enum member takes no value: its name is the code", class)
		}
		out = append(out, registry.BehaviorVeto{Code: m.Name().Text(), Description: readDoc(m).text})
	}
	return out
}

// readField reads a property of the class into one of its fields.
func (r *behaviorClassReader) readField(class string, member *astNode, decl *registry.BehaviorDeclaration) {
	prop := member.AsPropertyDeclaration()
	name := member.Name().Text()
	if prop.Initializer != nil {
		r.fail(member, "behavior class %s: field %s takes no value: a class declares and never implements (D103)", class, name)
	}
	field := registry.BehaviorField{Name: name, Description: readDoc(member).text}
	flags := map[string]bool{}
	for _, d := range r.w.decoratorsOf(member) {
		switch d.id.name {
		case valueFieldDecorator, fromCreateDecorator, computedDecorator, volatileDecorator, perReaderDecorator:
			flags[d.id.name] = true
		default:
			r.fail(d.node, "behavior class %s: field %s: @%s is no field marker of a behavior (@valueField, @fromCreate, @computed, @volatile, @perReader)", class, name, d.id.name)
		}
	}
	optional := prop.PostfixToken != nil && prop.PostfixToken.Kind == kindQuestionToken
	if flags[valueFieldDecorator] {
		if decl.Value != "" {
			r.fail(member, "behavior class %s: fields %s and %s are both @valueField", class, decl.Value, name)
		}
		decl.Value = name
		field.Optional = optional
	} else if optional {
		r.fail(member, "behavior class %s: field %s is marked ?, which only the value field takes: every other field a table takes is optional already", class, name)
	}
	field.FromCreate = flags[fromCreateDecorator]
	field.Volatile = flags[volatileDecorator] || flags[perReaderDecorator]
	field.PerReader = flags[perReaderDecorator]
	if !flags[computedDecorator] && !field.Volatile {
		field.Type = r.columnType(class, name, prop.Type)
	}
	decl.Fields = append(decl.Fields, field)
}

// columnType is the IR type of the column a field's type takes: string,
// number or boolean, or a superscalar scalar's name (Temporal.DateTime).
func (r *behaviorClassReader) columnType(class, field string, node *astNode) string {
	if node == nil {
		r.fail(nil, "behavior class %s: field %s has no type", class, field)
		return ""
	}
	switch node.Kind {
	case kindStringKeyword:
		return "string"
	case kindNumberKeyword:
		return "number"
	case kindBooleanKeyword:
		return "boolean"
	case kindTypeReference:
		if id, ok := r.w.identityOf(node.AsTypeReferenceNode().TypeName); ok && id.pkg == behaviorFieldPackage && id.name == "RowKey" {
			return "string"
		}
		if brand, ok := r.w.scalarBrand(r.w.checker.GetTypeFromTypeNode(node)); ok {
			return brand
		}
	}
	r.fail(node, "behavior class %s: field %s: a field's type is a column's, string, number, boolean or a scalar (Temporal.DateTime); one the behavior computes at a read is @computed or @volatile", class, field)
	return ""
}

// readTable reads a static property whose value is a class expression,
// marked @subTable, into one of the behavior's sub-tables: static, so a
// sub-table may share its name with a field (Timers' deadlines, the field
// its read computes and the table it keeps).
func (r *behaviorClassReader) readTable(class string, member *astNode, decl *registry.BehaviorDeclaration) {
	name := member.Name().Text()
	table := registry.BehaviorTable{Name: name, Description: readDoc(member).text}
	var marker *decoratorRef
	for _, d := range r.w.decoratorsOf(member) {
		if d.id.name == subTableDecorator {
			marker = &d
			continue
		}
		r.fail(d.node, "behavior class %s: sub-table %s takes @subTable alone", class, name)
	}
	if marker == nil || len(marker.args) != 1 {
		r.fail(member, "behavior class %s: sub-table %s is marked @subTable({ key, shared? })", class, name)
	} else if value, serr := r.w.evaluateExpression(marker.args[0]); serr != nil {
		r.errs = append(r.errs, serr)
	} else if options, ok := value.(map[string]any); ok {
		table.Key, _ = options["key"].(string)
		if options["shared"] == true {
			table.Scope = registry.TableScopeSchema
		}
		for key := range options {
			if key != "key" && key != "shared" {
				r.fail(marker.node, "behavior class %s: sub-table %s: @subTable has no option %s (key, shared)", class, name, key)
			}
		}
	}
	nested := member.AsPropertyDeclaration().Initializer.AsClassExpression()
	for _, m := range nested.Members.Nodes {
		if m.Kind != kindPropertyDeclaration {
			r.fail(m, "behavior class %s: sub-table %s: a sub-table's members are its fields", class, name)
			continue
		}
		prop := m.AsPropertyDeclaration()
		fieldName := m.Name().Text()
		field := registry.BehaviorField{Name: fieldName, Description: readDoc(m).text}
		if prop.Type != nil && prop.Type.Kind == kindTypeReference {
			if id, ok := r.w.identityOf(prop.Type.AsTypeReferenceNode().TypeName); ok && id.pkg == behaviorFieldPackage && id.name == "RowKey" {
				field.RowKey = true
			}
		}
		field.Type = r.columnType(class, name+"."+fieldName, prop.Type)
		table.Fields = append(table.Fields, field)
	}
	decl.Tables = append(decl.Tables, table)
}

// readMethod reads an abstract method of the class: an operation, an
// event, a signal, the create's parameters or a caller's precondition.
func (r *behaviorClassReader) readMethod(class string, member *astNode, decl *registry.BehaviorDeclaration) {
	method := member.AsMethodDeclaration()
	name := member.Name().Text()
	if method.Body != nil || !hasSyntacticModifier(member, modifierFlagsAbstract) {
		r.fail(member, "behavior class %s: %s is an abstract method signature: a class declares and never implements, since a body would run in one language (D103)", class, name)
	}
	doc := readDoc(member)
	var param *astNode
	if method.Parameters != nil {
		switch len(method.Parameters.Nodes) {
		case 0:
		case 1:
			param = method.Parameters.Nodes[0].AsParameterDeclaration().Type
		default:
			r.fail(member, "behavior class %s: %s takes one parameter, its parameters' object", class, name)
		}
	}
	paramsSchema := func() json.RawMessage {
		schema := map[string]any{"type": "object", "additionalProperties": false}
		if param != nil {
			var ok bool
			if schema, ok = r.schema(param); !ok {
				return nil
			}
		}
		if text := doc.tag(tagParam); text != "" {
			// @param <name> <description>
			if _, rest, ok := strings.Cut(text, " "); ok {
				schema["description"] = rest
			}
		}
		return r.raw(member, schema)
	}
	switch {
	case name == createMethod:
		decl.CreateParamsSchema = paramsSchema()
		return
	case name == preconditionMethod:
		decl.PreconditionSchema = paramsSchema()
		return
	case doc.has(tagEvent):
		decl.Events = append(decl.Events, registry.BehaviorEvent{Kind: name, Description: doc.text, PayloadSchema: paramsSchema()})
		return
	case doc.has(tagSignal):
		decl.Signals = append(decl.Signals, registry.BehaviorSignal{Name: name, Description: doc.text, ParamsSchema: paramsSchema(), Access: doc.tag(tagSignal)})
		return
	}
	op := registry.BehaviorOperation{Name: name, Description: doc.text, ParamsSchema: paramsSchema()}
	if method.Type == nil {
		r.fail(member, "behavior class %s: operation %s names its result type", class, name)
	} else if result, ok := r.schema(method.Type); ok {
		if text := doc.tag(tagReturns); text != "" {
			result["description"] = text
		}
		op.ResultSchema = r.raw(method.Type, result)
	}
	op.Writes = doc.has(tagWrites)
	op.Beside = doc.has(tagBeside)
	op.Scope = doc.tag(tagScope)
	op.Asks = doc.tag(tagAsks)
	op.InvocationPolicy = doc.tag(tagInvocation)
	if doc.has(tagLog) {
		if doc.tag(tagLog) != "false" {
			r.fail(member, "behavior class %s: operation %s: @log takes false, a beside operation that appends no event", class, name)
		}
		logged := false
		op.Log = &logged
	}
	if checks := doc.tag(tagChecks); checks != "" {
		op.Permissions = strings.Fields(checks)
	}
	decl.Operations = append(decl.Operations, op)
}

// raw is schema as JSON, or nil when it does not marshal.
func (r *behaviorClassReader) raw(node *astNode, schema map[string]any) json.RawMessage {
	data, err := json.Marshal(schema)
	if err != nil {
		r.fail(node, "the JSON Schema does not marshal: %v", err)
		return nil
	}
	return data
}

// classTags are the JSDoc tags a behavior's class reads.
var classTags = map[string]bool{
	tagWrites: true, tagScope: true, tagBeside: true, tagLog: true, tagAsks: true, tagInvocation: true,
	tagChecks: true, tagReturns: true, tagParam: true, tagEvent: true, tagSignal: true,
}

// classDoc is a declaration's JSDoc as a behavior's class reads it: its
// text, the lines before its first tag joined by spaces, and its tags,
// each a line that opens with one of classTags, its value the rest of the
// line and of the lines up to the next tag. A line that opens with another
// @word (a cron macro, @daily) is text.
type classDoc struct {
	text string
	tags map[string]string
}

func (d classDoc) has(tag string) bool {
	_, ok := d.tags[tag]
	return ok
}

func (d classDoc) tag(tag string) string {
	return d.tags[tag]
}

// readDoc reads the JSDoc comment attached to node, the last of its
// leading trivia's attached comments that is one.
func readDoc(node *astNode) classDoc {
	doc := classDoc{tags: map[string]string{}}
	trivia := leadingTrivia(node)
	spans := commentSpans(trivia)
	var jsdoc *commentSpan
	for i := attachedFrom(trivia, spans); i < len(spans); i++ {
		if spans[i].kind == jsdocComment {
			jsdoc = &spans[i]
		}
	}
	if jsdoc == nil {
		return doc
	}
	var text []string
	tag := ""
	for _, line := range jsdoc.lines {
		if !line.kept {
			continue
		}
		s := line.text
		if name, rest, _ := strings.Cut(s, " "); classTags[name] {
			tag = name
			doc.tags[tag] = strings.TrimSpace(rest)
			continue
		}
		if tag != "" {
			doc.tags[tag] = strings.TrimSpace(doc.tags[tag] + " " + s)
			continue
		}
		text = append(text, s)
	}
	doc.text = strings.Join(text, " ")
	return doc
}

// stringList is value as a list of strings.
func stringList(value any) ([]string, bool) {
	list, ok := value.([]any)
	if !ok {
		return nil, false
	}
	out := make([]string, 0, len(list))
	for _, v := range list {
		s, ok := v.(string)
		if !ok {
			return nil, false
		}
		out = append(out, s)
	}
	return out, true
}

func sortedAnyKeys(m map[string]any) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}
