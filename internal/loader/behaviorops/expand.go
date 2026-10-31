// Package behaviorops adds a table's behavior operations to an API (D103 in
// docs/DECISIONS.md). An API marks a class with no members
// @behaviorOperations(Type), from @superschematic/api, naming a table of its
// database one of whose fields is a behavior:
//
//	@behaviorOperations(Order)
//	@requirePermission(["orders.write"])
//	export abstract class OrderActions {}
//
// Once the set passes its checks (verify.CheckBehaviorOperations), and
// before the rest of the verification pass, the loader fills it here with
// one operation for each operation of each behavior on a field of the
// table, in field order and then declaration order: POST
// order/{id}/status/transition, named statusTransition, taking the row's
// key and an input of the operation's parameters and returning its result,
// with generated @docs and an @mcp handle (order_status_transition), or a
// hidden @mcp when the set's @requireService admits only calling services
// (D37), the set's clause as its own, and the marker
// ir.FieldDef.BehaviorOperation.
// The input and result types, and the enums they name, are the
// declaration's schemas as the config narrows them (behaviortypes.Narrow),
// as the repositories type them. So every check the pass makes of an
// authored operation holds for these, and the OpenAPI document, the SDKs,
// the permission catalog and the tool documents read them as any other;
// only the servers route them, to a generated handler that runs them
// through the behaviors' core.
package behaviorops

import (
	"fmt"
	"slices"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviortypes"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// The scalars an added type names where a schema node has no type of the
// IR's own: an integer, and any value no type describes.
const (
	int64Scalar = "Generic.Int64"
	jsonScalar  = "Generic.JSON"
)

// OperationName is the name of the operation the expansion adds for op of
// the use on the field use: the field's name and the operation's
// (statusTransition).
func OperationName(use, op string) string {
	return use + codegen.ToPascalCase(op)
}

// Base is the name the narrowing of the use on typeName's field use names
// its types by (OrderStatus), as the repositories name theirs.
func Base(typeName, use string) string {
	return typeName + codegen.ToPascalCase(use)
}

// InputType is the name of the input type of the use's op
// (OrderStatusTransitionInput).
func InputType(typeName, use, op string) string {
	return Base(typeName, use) + codegen.ToPascalCase(op) + "Input"
}

// ResultType is the name of the result type of the use's op
// (OrderStatusTransitionResult).
func ResultType(typeName, use, op string) string {
	return Base(typeName, use) + codegen.ToPascalCase(op) + "Result"
}

// RestPath is the route of the use's op, below the API's prefix, which
// addresses the row by key (order/{id}/status/transition,
// shipment/{id}/payment-status/transition).
func RestPath(typeName, key, use, op string) string {
	return codegen.ToKebabCase(typeName) + "/{" + key + "}/" + codegen.ToKebabCase(use) + "/" + codegen.ToKebabCase(op)
}

// Handle is the MCP handle of the use's op (order_status_transition).
func Handle(typeName, use, op string) string {
	return codegen.ToSnakeCase(typeName) + "_" + codegen.ToSnakeCase(use) + "_" + codegen.ToSnakeCase(op)
}

// RequireServiceToolReason is the @mcp hidden reason of an operation the
// expansion adds to set, a set whose @requireService admits only calling
// services: no end user's agent can call its tool (D37).
func RequireServiceToolReason(set string) string {
	return "Only the services that @requireService on " + set + " admits call it (D37)."
}

// Capability is the @docs capability of the use's op
// (order.status.transition, shipment.payment-status.transition).
func Capability(typeName, use, op string) string {
	return codegen.ToKebabCase(typeName) + "." + codegen.ToKebabCase(use) + "." + codegen.ToKebabCase(op)
}

// Title is the @docs title of the use's op (Order status: transition,
// Shipment payment status: transition).
func Title(typeName, use, op string) string {
	return typeName + " " + words(use) + ": " + words(op)
}

// words writes a camelCase name as lowercase words.
func words(name string) string {
	return strings.ReplaceAll(codegen.ToKebabCase(name), "-", " ")
}

// ConfigPermissions are the permissions a use's config names: every string
// under a key named permission, at any depth, sorted and each once (a
// Workflow transition's). Until a behavior's class declaration says which
// operation checks which, each operation of the use lists them all.
func ConfigPermissions(config []byte) []string {
	if len(config) == 0 {
		return nil
	}
	decoded, err := behaviortypes.DecodeSchema(config)
	if err != nil {
		return nil
	}
	var out []string
	var walk func(any)
	walk = func(node any) {
		switch v := node.(type) {
		case map[string]any:
			for key, value := range v {
				if text, ok := value.(string); ok && key == "permission" && text != "" {
					if !slices.Contains(out, text) {
						out = append(out, text)
					}
					continue
				}
				walk(value)
			}
		case []any:
			for _, value := range v {
				walk(value)
			}
		}
	}
	walk(decoded)
	sort.Strings(out)
	return out
}

// Added is a type or an enum the expansion adds for one set: its kind
// ("type" or "enum"), its name, and the use and operation it is added
// for, the first that names it.
type Added struct {
	Kind      string
	Name      string
	Use       string
	Operation string
}

// Plan is what the expansion adds for one @behaviorOperations set: its
// operations, and the types and enums they take and return, in the order
// they are first named.
type Plan struct {
	Operations []*ir.FieldDef
	Types      []*ir.TypeDef
	Enums      []*ir.EnumDef
	// Added lists every type and enum, in the order they are first named.
	Added []Added
	// Scalars are the scalar names the operations and types name.
	Scalars []string
}

// PlanSet works out what the expansion adds for set, a @behaviorOperations
// set whose table db, the API's database, declares, reading the behaviors' declarations
// from reg. It assumes the table is one (verify.CheckBehaviorOperations):
// declared by db, keyed by one field, one of whose fields is a behavior
// registered in reg, each of whose operations runs on a row. It fails only
// when a declaration does not narrow.
func PlanSet(set *ir.OperationSet, db *ir.Schema, reg *registry.Registry) (*Plan, error) {
	td := db.Types[set.BehaviorOperations.Type]
	if td == nil {
		return nil, fmt.Errorf("behaviorops: %s: %s declares no type %s", set.Name, db.Name, set.BehaviorOperations.Type)
	}
	key := keyField(td)
	if key == nil {
		return nil, fmt.Errorf("behaviorops: %s: %s has no @key field", set.Name, td.Name)
	}
	policy := reg.ToolInvocationPolicy()
	p := &Plan{}
	b := &builder{plan: p, named: map[string]bool{}}
	b.useScalar(key.TypeRef.Name)
	for _, ref := range td.FieldBehaviors() {
		declaration, ok := reg.Behavior(ref.Name)
		if !ok {
			return nil, fmt.Errorf("behaviorops: %s: %s's %s is the behavior %s, which is not registered", set.Name, td.Name, ref.UseID(), ref.Name)
		}
		use := ref.UseID()
		base := Base(td.Name, use)
		narrowed, err := behaviortypes.Narrow(declaration, ref.Config, behaviortypes.Target{
			Schema: db.Name, Base: base, Document: db, Instance: td,
		})
		if err != nil {
			return nil, fmt.Errorf("behaviorops: %s: %s's %s, %s: %w", set.Name, td.Name, use, behaviortypes.An(ref.Name), err)
		}
		// A narrowing's doc names the type it narrows by its base, which
		// for a use on a field is the type's name and the field's (an
		// OrderStatus's): its docs name the field instead (Order's status),
		// as the repositories' do.
		b.aliases = map[string]behaviortypes.Alias{}
		for _, alias := range narrowed.Aliases {
			alias.Doc = strings.ReplaceAll(alias.Doc, behaviortypes.An(base)+"'s", td.Name+"'s "+use)
			b.aliases[alias.Name] = alias
		}
		permissions := ConfigPermissions(ref.Config)
		for _, op := range declaration.Operations {
			b.use, b.operation = use, op.Name
			p.Operations = append(p.Operations, b.operationOf(set, db, td, key, ref, op, narrowed, permissions, policy))
		}
	}
	return p, nil
}

// operationOf is the operation the expansion adds for op of the use ref.
func (b *builder) operationOf(set *ir.OperationSet, db *ir.Schema, td *ir.TypeDef, key *ir.FieldDef, ref ir.BehaviorRef, op registry.BehaviorOperation, narrowed behaviortypes.Narrowing, permissions []string, policy registry.ToolInvocationPolicy) *ir.FieldDef {
	use := ref.UseID()
	out := &ir.FieldDef{
		Name:       OperationName(use, op.Name),
		Comment:    op.Description,
		Required:   true,
		HTTPMethod: "POST",
		RestPath:   RestPath(td.Name, key.Name, use, op.Name),
		Arguments: []*ir.ArgumentDef{{
			Name:     key.Name,
			Comment:  "The key of the " + td.Name + " row the operation runs on.",
			TypeRef:  ir.TypeRef{Name: key.TypeRef.Name},
			Required: true,
		}},
		Auth:        set.Auth,
		Permissions: slices.Clone(set.Permissions),
		Public:      set.Public,
		Docs: &ir.OperationDocs{
			Title:         Title(td.Name, use, op.Name),
			Description:   op.Description,
			Capability:    Capability(td.Name, use, op.Name),
			Lifecycle:     ir.DocsLifecycleActive,
			Visibility:    ir.DocsVisibilityPublic,
			MappingStatus: ir.DocsMappingStatusMapped,
		},
		MCP: &ir.OperationMCP{
			Handle:     Handle(td.Name, use, op.Name),
			Invocation: ir.MCPInvocation{Key: policy.Key, Value: op.InvocationPolicy},
		},
		Origin: ir.OriginBehavior,
		BehaviorOperation: &ir.BehaviorOperation{
			Database:    db.Name,
			Type:        td.Name,
			Key:         key.Name,
			Use:         use,
			Behavior:    ref.Name,
			Operation:   op.Name,
			Writes:      op.Writes,
			Permissions: slices.Clone(permissions),
		},
	}
	if out.MCP.Invocation.Value == "" {
		out.MCP.Invocation.Value = policy.Default
	}
	if callers := ir.EffectiveServiceCallers(set, out); callers != nil && callers.Mode == ir.ServiceCallersRequire {
		// Only a calling service reaches the route, so no end user's agent
		// can call the tool: the tool documents leave it out, and the
		// record says why, as @mcp({ hidden: true, reason }) would (D37).
		out.MCP = &ir.OperationMCP{Hidden: true, HiddenReason: RequireServiceToolReason(set.Name)}
	}
	if !op.Writes {
		out.Docs.ReplayMode = ir.DocsReplayModeReadOnly
	}
	if params, ok := narrowed.Params[op.Name].(map[string]any); ok {
		if properties, _ := params["properties"].(map[string]any); len(properties) > 0 {
			name := InputType(td.Name, use, op.Name)
			b.object(params, properties, name, fmt.Sprintf("The parameters of %s's %s %s.", td.Name, use, op.Name), ir.RoleAPIInput)
			out.Arguments = append(out.Arguments, &ir.ArgumentDef{Name: ir.BehaviorInputArgument, TypeRef: ir.TypeRef{Name: name}, Required: true})
		}
	}
	result := narrowed.Results[op.Name]
	name := ResultType(td.Name, use, op.Name)
	if s, ok := result.(map[string]any); ok && isObject(s) {
		if properties, _ := s["properties"].(map[string]any); len(properties) > 0 {
			b.object(s, properties, name, fmt.Sprintf("What %s's %s %s returns.", td.Name, use, op.Name), ir.RoleEmbeddedStruct)
			out.TypeRef = ir.TypeRef{Name: name}
			return out
		}
	}
	resultRef, nullable := b.typeOf(result, name, ir.RoleEmbeddedStruct)
	out.TypeRef, out.Required = resultRef, !nullable
	return out
}

// Expand fills each @behaviorOperations set of api with the operations of
// its table's behaviors, read from db, the API's database, and reg, which
// declares the behaviors, and adds the types and enums they take and return
// (D103). It assumes the sets passed verify.CheckBehaviorOperations, and
// does nothing for an API without such a set. It returns the scalar names
// the added types use, so the caller can hydrate any api did not hold; one
// only db declares is copied from it. It may run again on its own output.
func Expand(api, db *ir.Schema, reg *registry.Registry) ([]string, error) {
	var sets []*ir.OperationSet
	for _, set := range api.OperationSets {
		if set.IsBehaviorOperations() {
			sets = append(sets, set)
		}
	}
	if len(sets) == 0 || db == nil {
		return nil, nil
	}
	var scalars []string
	for _, set := range sets {
		plan, err := PlanSet(set, db, reg)
		if err != nil {
			return nil, err
		}
		set.Operations = plan.Operations
		if api.Types == nil {
			api.Types = map[string]*ir.TypeDef{}
		}
		if api.Enums == nil {
			api.Enums = map[string]*ir.EnumDef{}
		}
		for _, td := range plan.Types {
			td.Owner = api.Name
			api.Types[td.Name] = td
		}
		for _, ed := range plan.Enums {
			ed.Owner = api.Name
			api.Enums[ed.Name] = ed
		}
		for _, name := range plan.Scalars {
			if !slices.Contains(scalars, name) {
				scalars = append(scalars, name)
			}
		}
	}
	if api.Scalars == nil {
		api.Scalars = map[string]*ir.ScalarDef{}
	}
	for _, name := range scalars {
		if api.Scalars[name] != nil {
			continue
		}
		def := &ir.ScalarDef{Name: name}
		if declared := db.Scalars[name]; declared != nil {
			def = cloneScalar(declared)
		}
		api.Scalars[name] = def
	}
	return scalars, nil
}

// builder adds the types and enums of one set's operations to its plan,
// each once.
type builder struct {
	plan  *Plan
	named map[string]bool
	// aliases are the current use's narrowing's, by name.
	aliases map[string]behaviortypes.Alias
	// use and operation are what the types being added are for.
	use, operation string
}

// claim records name as added, and reports whether it was not before.
func (b *builder) claim(kind, name string) bool {
	if b.named[name] {
		return false
	}
	b.named[name] = true
	b.plan.Added = append(b.plan.Added, Added{Kind: kind, Name: name, Use: b.use, Operation: b.operation})
	return true
}

// useScalar records a scalar the added types name; a primitive is none.
func (b *builder) useScalar(name string) {
	switch name {
	case "string", "number", "boolean":
		return
	}
	if !slices.Contains(b.plan.Scalars, name) {
		b.plan.Scalars = append(b.plan.Scalars, name)
	}
}

// json is the type of a value no other type describes.
func (b *builder) json() ir.TypeRef {
	b.useScalar(jsonScalar)
	return ir.TypeRef{Name: jsonScalar}
}

// typeOf is the type a schema node is, and whether it admits null: an
// alias of a string enum the enum, added once; any other alias its schema
// under its name; a string, an integer, a number and a boolean the IR's; a
// string enum in place an enum named name; an object of properties a type
// named name, of role; an array its items' type as a list, two deep at
// most; an object of no properties whose additionalProperties is a schema
// that schema's type as a map; and anything else, Generic.JSON.
func (b *builder) typeOf(node any, name string, role ir.Role) (ir.TypeRef, bool) {
	s, ok := node.(map[string]any)
	if !ok {
		return b.json(), false
	}
	nullable, kinds := typesOf(s)
	if refName, ok := s[behaviortypes.RefKey].(string); ok {
		alias, ok := b.aliases[refName]
		if !ok {
			return b.json(), nullable
		}
		if values, ok := stringEnum(alias.Schema); ok {
			b.enum(refName, alias.Doc, values)
			return ir.TypeRef{Name: refName}, nullable
		}
		named, aliasNullable := b.typeOf(alias.Schema, refName, role)
		return named, nullable || aliasNullable
	}
	for _, key := range []string{"anyOf", "oneOf", "allOf", "not", "const"} {
		if _, ok := s[key]; ok {
			return b.json(), nullable
		}
	}
	if enum, ok := s["enum"].([]any); ok {
		values, isStrings := stringValues(enum)
		if !isStrings || len(kinds) != 1 || kinds[0] != "string" {
			return b.json(), nullable
		}
		b.enum(name, behaviortypes.DescriptionOf(s), values)
		return ir.TypeRef{Name: name}, nullable
	}
	if len(kinds) == 0 {
		if _, ok := s["properties"]; !ok {
			return b.json(), nullable
		}
		kinds = []string{"object"}
	}
	if len(kinds) > 1 {
		return b.json(), nullable
	}
	switch kinds[0] {
	case "string":
		return ir.TypeRef{Name: "string"}, nullable
	case "integer":
		b.useScalar(int64Scalar)
		return ir.TypeRef{Name: int64Scalar}, nullable
	case "number":
		return ir.TypeRef{Name: "number"}, nullable
	case "boolean":
		return ir.TypeRef{Name: "boolean"}, nullable
	case "array":
		items := s["items"]
		elem, _ := b.typeOf(items, name, role)
		switch {
		case elem.IsMap || elem.IsArrayOfArrays:
			return b.json(), nullable
		case elem.IsArray:
			elem.IsArrayOfArrays = true
		}
		elem.IsArray = true
		return elem, nullable
	case "object":
		properties, _ := s["properties"].(map[string]any)
		if len(properties) > 0 {
			b.object(s, properties, name, behaviortypes.DescriptionOf(s), role)
			return ir.TypeRef{Name: name}, nullable
		}
		if additional, ok := s["additionalProperties"].(map[string]any); ok {
			value, _ := b.typeOf(additional, name, role)
			if value.IsMap || value.IsArrayOfArrays {
				return b.json(), nullable
			}
			value.IsMap = true
			return value, nullable
		}
	}
	return b.json(), nullable
}

// object adds the type name an object schema is, of role, its fields in
// the order of their keys; a nested object of properties is the type
// named for the path to it.
func (b *builder) object(s, properties map[string]any, name, comment string, role ir.Role) {
	if !b.claim("type", name) {
		return
	}
	td := &ir.TypeDef{Name: name, Comment: comment, Role: role, Origin: ir.OriginBehavior}
	b.plan.Types = append(b.plan.Types, td)
	required := map[string]bool{}
	if list, ok := s["required"].([]any); ok {
		for _, each := range list {
			if text, ok := each.(string); ok {
				required[text] = true
			}
		}
	}
	for _, property := range behaviortypes.SortedKeys(properties) {
		node := properties[property]
		ref, nullable := b.typeOf(node, name+codegen.ToPascalCase(property), role)
		fd := &ir.FieldDef{
			Name:     property,
			Comment:  behaviortypes.DescriptionOf(node),
			TypeRef:  ref,
			Required: required[property] && !nullable,
			Origin:   ir.OriginBehavior,
		}
		validations(fd, node)
		td.Fields = append(td.Fields, fd)
	}
}

// enum adds the enum name of the string values, once.
func (b *builder) enum(name, comment string, values []string) {
	if !b.claim("enum", name) {
		return
	}
	ed := &ir.EnumDef{Name: name, Comment: comment, Origin: ir.OriginBehavior}
	for _, value := range values {
		ed.Values = append(ed.Values, ir.EnumValueDef{Name: behaviortypes.MemberName(value), SerializedAs: value})
	}
	b.plan.Enums = append(b.plan.Enums, ed)
}

// validations copies a scalar property's bounds onto its field: a
// string's minLength, maxLength and pattern, a number's minimum and
// maximum.
func validations(fd *ir.FieldDef, node any) {
	s, ok := node.(map[string]any)
	if !ok || fd.TypeRef.IsArray || fd.TypeRef.IsMap {
		return
	}
	switch fd.TypeRef.Name {
	case "string":
		if v, ok := s["minLength"].(float64); ok {
			n := int(v)
			fd.ValidateMinLength = &n
		}
		if v, ok := s["maxLength"].(float64); ok {
			n := int(v)
			fd.ValidateMaxLength = &n
		}
		if v, ok := s["pattern"].(string); ok {
			fd.ValidatePattern = v
		}
	case int64Scalar, "number":
		if v, ok := s["minimum"].(float64); ok {
			fd.ValidateMin = &v
		}
		if v, ok := s["maximum"].(float64); ok {
			fd.ValidateMax = &v
		}
	}
}

// isObject reports whether a schema node is an object: its type says so,
// or it has properties and no type.
func isObject(s map[string]any) bool {
	if _, ok := s[behaviortypes.RefKey]; ok {
		return false
	}
	_, kinds := typesOf(s)
	if len(kinds) == 0 {
		_, ok := s["properties"]
		return ok
	}
	return len(kinds) == 1 && kinds[0] == "object"
}

// typesOf reads a schema node's type, a name or a list of names, and
// whether it admits null.
func typesOf(s map[string]any) (nullable bool, kinds []string) {
	if flag, ok := s["nullable"].(bool); ok && flag {
		nullable = true
	}
	switch t := s["type"].(type) {
	case string:
		if t == "null" {
			return true, nil
		}
		kinds = append(kinds, t)
	case []any:
		for _, each := range t {
			name, _ := each.(string)
			if name == "null" {
				nullable = true
				continue
			}
			if name != "" {
				kinds = append(kinds, name)
			}
		}
	}
	return nullable, kinds
}

// stringEnum reads an alias's schema as a string enum.
func stringEnum(schema any) ([]string, bool) {
	s, ok := schema.(map[string]any)
	if !ok {
		return nil, false
	}
	enum, ok := s["enum"].([]any)
	if !ok {
		return nil, false
	}
	_, kinds := typesOf(s)
	if len(kinds) > 1 || (len(kinds) == 1 && kinds[0] != "string") {
		return nil, false
	}
	return stringValues(enum)
}

// stringValues reads a JSON array of strings.
func stringValues(values []any) ([]string, bool) {
	out := make([]string, 0, len(values))
	for _, each := range values {
		text, ok := each.(string)
		if !ok {
			return nil, false
		}
		out = append(out, text)
	}
	return out, true
}

// keyField returns td's one @key field, or nil when it has none or
// several.
func keyField(td *ir.TypeDef) *ir.FieldDef {
	var key *ir.FieldDef
	for _, fd := range td.Fields {
		if fd.Key {
			if key != nil {
				return nil
			}
			key = fd
		}
	}
	return key
}

// cloneScalar copies def, whose maps and lists the API's hydration may
// write, so the database's schema, which a build caches and shares, keeps
// its own.
func cloneScalar(def *ir.ScalarDef) *ir.ScalarDef {
	copied := *def
	if def.TypeMappings != nil {
		copied.TypeMappings = make(map[string]string, len(def.TypeMappings))
		for k, v := range def.TypeMappings {
			copied.TypeMappings[k] = v
		}
	}
	if def.ReservedWords != nil {
		copied.ReservedWords = append([]string(nil), def.ReservedWords...)
	}
	return &copied
}
