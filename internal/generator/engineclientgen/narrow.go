package engineclientgen

import (
	"encoding/json"
	"fmt"
	"slices"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// narrowTarget is the type a config is narrowed on.
type narrowTarget struct {
	// schema is the schema's name.
	schema string
	// base names the instance type's exports (Note: NoteFields, NotePatch).
	base     string
	document *ir.Schema
	instance *ir.TypeDef
}

// alias is a type a narrowing exports, such as a Workflow's states.
type alias struct {
	name   string
	doc    string
	schema any
}

// fieldShape is a behavior field as a read returns it, under its behavior's
// name: its JSON Schema, and whether every read holds it (an engine leaves
// out a field whose reader returns nothing).
type fieldShape struct {
	schema  any
	present bool
}

// narrowing is what a behavior adds to its type under its config. Every
// schema is a decoded JSON Schema; a node {"$ts": name} is a type of the
// module.
type narrowing struct {
	aliases []alias
	// fields are the behavior's fields by name.
	fields map[string]fieldShape
	// params and results are each operation's, by name.
	params  map[string]any
	results map[string]any
	// createParams is the create parameters' schema, as the config
	// narrows it; nil when the declaration takes none.
	createParams any
	// preconditions is the declaration's preconditionSchema, or nil.
	preconditions any
	// variants is a Variants config, which types the instance's own fields.
	variants *variantsConfig
}

// variantsConfig is Variants' config: field holds the type types names for
// the value by holds.
type variantsConfig struct {
	field string
	by    string
	types map[string]string
}

// narrower narrows one core behavior's shapes under its decoded config.
type narrower func(n *narrowing, config map[string]any, target narrowTarget) error

// coreNarrowers are the narrowings of the core's behaviors. A behavior
// without one keeps its declaration's shapes, and its fields hold any
// JSON. An extension's behavior is never here: its name is qualified.
var coreNarrowers = map[string]narrower{
	"Workflow":     narrowWorkflow,
	"Comments":     narrowComments,
	"Revisions":    narrowRevisions,
	"Dependencies": narrowDependencies,
	"Links":        narrowLinks,
	"Rollups":      narrowRollups,
	"Variants":     narrowVariants,
	"Assignment":   narrowAssignment,
	"Lease":        narrowLease,
	"Presence":     narrowPresence,
	"Blueprint":    narrowBlueprint,
	"Budget":       narrowBudget,
	"Retries":      narrowRetries,
}

// idPattern is the pattern of an instance id, as the engine's declarations
// write it.
const idPattern = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"

// narrow starts from the declaration's shapes and applies the behavior's
// core narrowing, when it has one.
func narrow(declaration registry.Behavior, config json.RawMessage, target narrowTarget) (narrowing, error) {
	n := narrowing{fields: map[string]fieldShape{}, params: map[string]any{}, results: map[string]any{}}
	for _, field := range declaration.Fields {
		n.fields[field.Name] = fieldShape{schema: map[string]any{"description": field.Description}}
	}
	for _, op := range declaration.Operations {
		params, err := decodeSchema(op.ParamsSchema)
		if err != nil {
			return n, fmt.Errorf("operation %s paramsSchema: %w", op.Name, err)
		}
		result, err := decodeSchema(op.ResultSchema)
		if err != nil {
			return n, fmt.Errorf("operation %s resultSchema: %w", op.Name, err)
		}
		n.params[op.Name] = params
		n.results[op.Name] = result
	}
	var err error
	if n.createParams, err = decodeSchema(declaration.CreateParamsSchema); err != nil {
		return n, fmt.Errorf("createParamsSchema: %w", err)
	}
	if n.preconditions, err = decodeSchema(declaration.PreconditionSchema); err != nil {
		return n, fmt.Errorf("preconditionSchema: %w", err)
	}
	narrowFn, ok := coreNarrowers[declaration.Name]
	if !ok || declaration.Extension != "" {
		return n, nil
	}
	var decoded map[string]any
	if len(config) > 0 && string(config) != "null" {
		if err := json.Unmarshal(config, &decoded); err != nil {
			return n, fmt.Errorf("config: %w", err)
		}
	}
	if decoded == nil {
		decoded = map[string]any{}
	}
	return n, narrowFn(&n, decoded, target)
}

// ref is a node that renders as a type of the module, keeping a
// description for its doc comment.
func ref(name, description string) map[string]any {
	node := map[string]any{refKey: name}
	if description != "" {
		node["description"] = description
	}
	return node
}

// enumOf is a schema whose values are the strings given.
func enumOf(values []string) map[string]any {
	out := make([]any, 0, len(values))
	for _, value := range values {
		out = append(out, value)
	}
	return map[string]any{"type": "string", "enum": out}
}

// stringsOf reads a JSON array of strings.
func stringsOf(value any) []string {
	list, _ := value.([]any)
	out := make([]string, 0, len(list))
	for _, each := range list {
		if text, ok := each.(string); ok {
			out = append(out, text)
		}
	}
	return out
}

// at walks a schema through object keys and returns the node there.
func at(schema any, path ...string) (map[string]any, error) {
	node, ok := schema.(map[string]any)
	if !ok {
		return nil, fmt.Errorf("the declaration has no schema at %v", path)
	}
	for _, key := range path {
		next, ok := node[key].(map[string]any)
		if !ok {
			return nil, fmt.Errorf("the declaration's schema has no %v", path)
		}
		node = next
	}
	return node, nil
}

// setProperty replaces a property of the object schema node, keeping its
// description. The property must exist, so a narrowing cannot drift from
// its declaration unnoticed.
func setProperty(object map[string]any, name string, replacement map[string]any) error {
	properties, ok := object["properties"].(map[string]any)
	if !ok {
		return fmt.Errorf("the declaration's schema has no properties for %s", name)
	}
	current, ok := properties[name].(map[string]any)
	if !ok {
		return fmt.Errorf("the declaration's schema has no property %s", name)
	}
	if description, ok := current["description"].(string); ok {
		if _, has := replacement["description"]; !has {
			replacement["description"] = description
		}
	}
	properties[name] = replacement
	return nil
}

// setOperationProperty retypes a property of an operation's params or
// result, at a path of object keys below it.
func setOperationProperty(schemas map[string]any, operation string, path []string, name string, replacement map[string]any) error {
	object, err := at(schemas[operation], path...)
	if err != nil {
		return fmt.Errorf("operation %s: %w", operation, err)
	}
	if err := setProperty(object, name, replacement); err != nil {
		return fmt.Errorf("operation %s: %w", operation, err)
	}
	return nil
}

// setField types a field of the behavior, keeping its declaration's
// description. The field must be declared, so a narrowing cannot drift from
// its declaration unnoticed.
func setField(n *narrowing, name string, schema map[string]any, present bool) error {
	current, ok := n.fields[name]
	if !ok {
		return fmt.Errorf("the declaration has no field %s", name)
	}
	if description := descriptionOf(current.schema); description != "" {
		schema["description"] = description
	}
	n.fields[name] = fieldShape{schema: schema, present: present}
	return nil
}

// Workflow: the status and transition's states are the config's.
func narrowWorkflow(n *narrowing, config map[string]any, target narrowTarget) error {
	states := stringsOf(config["states"])
	state := target.base + "State"
	n.aliases = append(n.aliases, alias{name: state, doc: fmt.Sprintf("A state of %s's Workflow.", an(target.base)), schema: enumOf(states)})
	if err := setField(n, "status", ref(state, ""), true); err != nil {
		return err
	}
	if err := setOperationProperty(n.params, "transition", nil, "to", ref(state, "")); err != nil {
		return err
	}
	for _, name := range []string{"from", "to"} {
		if err := setOperationProperty(n.results, "transition", nil, name, ref(state, "")); err != nil {
			return err
		}
	}
	return nil
}

// Comments: the count is always there.
func narrowComments(n *narrowing, _ map[string]any, _ narrowTarget) error {
	return setField(n, "commentCount", map[string]any{"type": "integer"}, true)
}

// Revisions: a proposal's patch is the type's patch, a revision's data its
// own fields; the revision number is absent until the first revision.
func narrowRevisions(n *narrowing, _ map[string]any, target narrowTarget) error {
	if err := setField(n, "revision", map[string]any{"type": "integer"}, false); err != nil {
		return err
	}
	patch := target.base + "Patch"
	if err := setOperationProperty(n.params, "propose", nil, "patch", ref(patch, "")); err != nil {
		return err
	}
	for _, op := range []string{"propose", "approve", "reject"} {
		if err := setOperationProperty(n.results, op, nil, "patch", ref(patch, "")); err != nil {
			return err
		}
	}
	if err := setOperationProperty(n.results, "listProposals", []string{"properties", "items", "items"}, "patch", ref(patch, "")); err != nil {
		return err
	}
	return setOperationProperty(n.results, "listRevisions", []string{"properties", "items", "items"}, "data", ref(target.base+"Fields", ""))
}

// Dependencies: a blocker is an instance of a schema the config lists, the
// type's own when it lists none, and names its schema unless it is the
// type's own. The create parameters are the engine's narrowing
// (dependenciesCreateParams).
func narrowDependencies(n *narrowing, config map[string]any, target narrowTarget) error {
	schemas := stringsOf(config["schemas"])
	if len(schemas) == 0 {
		schemas = []string{target.schema}
	}
	own := slices.Contains(schemas, target.schema)
	required := []any{"id"}
	if !own {
		required = []any{"id", "schema"}
	}
	blockerSchema := func() map[string]any {
		schema := enumOf(schemas)
		if own {
			schema["description"] = fmt.Sprintf("The blocker's schema; %s when absent.", target.schema)
		} else {
			schema["description"] = "The blocker's schema."
		}
		return schema
	}
	n.createParams = map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties": map[string]any{
			"blockers": map[string]any{
				"description": "The instance's blockers from its create, each held to addBlocker's rules.",
				"type":        "array",
				"maxItems":    json.Number("500"),
				"items": map[string]any{
					"type":                 "object",
					"additionalProperties": false,
					"required":             required,
					"properties": map[string]any{
						"schema": blockerSchema(),
						"id":     map[string]any{"description": "The blocker's id.", "type": "string", "pattern": idPattern},
					},
				},
			},
		},
	}
	for _, op := range []string{"addBlocker", "removeBlocker"} {
		params, err := at(n.params[op])
		if err != nil {
			return err
		}
		if err := setProperty(params, "schema", blockerSchema()); err != nil {
			return fmt.Errorf("operation %s: %w", op, err)
		}
		params["required"] = required
		if err := setOperationProperty(n.results, op, nil, "schema", enumOf(schemas)); err != nil {
			return err
		}
	}
	if err := setOperationProperty(n.results, "listBlockers", []string{"properties", "items", "items"}, "schema", enumOf(schemas)); err != nil {
		return err
	}
	return setField(n, "blocked", map[string]any{"type": "boolean"}, true)
}

// linkSpec is one link of a Links config.
type linkSpec struct {
	name     string
	schema   string
	required bool
	// pin is what the link pins of its target, "revision" or "release",
	// or "" for a link that pins nothing (the engine's linkPin).
	pin string
}

func linksOf(config map[string]any) []linkSpec {
	links, _ := config["links"].(map[string]any)
	out := make([]linkSpec, 0, len(links))
	for _, name := range sortedKeys(links) {
		spec, _ := links[name].(map[string]any)
		schema, _ := spec["schema"].(string)
		required, _ := spec["required"].(bool)
		out = append(out, linkSpec{name: name, schema: schema, required: required, pin: linkPin(spec["pinned"])})
	}
	return out
}

// linkPin reads a link's pinned as the engine does: true or "revision"
// pins a revision of Revisions, "release" a release of Branches, and any
// other value nothing.
func linkPin(pinned any) string {
	switch pinned {
	case true, "revision":
		return "revision"
	case "release":
		return "release"
	}
	return ""
}

// Links: the names are the config's; the create parameters are the
// engine's narrowing (linksCreateParams): a property per link, the
// required ones required, the revision or release it records only for a
// pinned link; the targets field holds each link the instance has, with
// its target's schema, and for a pinned link its pin, the target's latest
// of that kind and whether the target has moved past the pin.
func narrowLinks(n *narrowing, config map[string]any, target narrowTarget) error {
	links := linksOf(config)
	names := make([]string, 0, len(links))
	for _, link := range links {
		names = append(names, link.name)
	}
	linkName := target.base + "LinkName"
	n.aliases = append(n.aliases, alias{name: linkName, doc: fmt.Sprintf("A link of %s, by name.", an(target.base)), schema: enumOf(names)})

	createProperties := map[string]any{}
	fieldProperties := map[string]any{}
	var required []any
	for _, link := range links {
		targetProperties := map[string]any{
			"id": map[string]any{"description": "The target's id.", "type": "string", "pattern": idPattern},
		}
		recordProperties := map[string]any{
			"schema": map[string]any{"type": "string", "enum": []any{link.schema}},
			"id":     map[string]any{"type": "string"},
		}
		description := fmt.Sprintf("The %s instance %s points at: its id.", link.schema, link.name)
		if pin := link.pin; pin != "" {
			targetProperties[pin] = map[string]any{"description": fmt.Sprintf("The target's %s to record, one it has had; its latest when absent.", pin), "type": "integer", "minimum": json.Number("1")}
			recordProperties[pin] = map[string]any{"description": fmt.Sprintf("The target's %s the link records.", pin), "type": "integer"}
			recordProperties["latest"] = map[string]any{"description": fmt.Sprintf("The target's latest %s.", pin), "type": "integer"}
			recordProperties["stale"] = map[string]any{"description": fmt.Sprintf("Whether the target has moved past that %s.", pin), "type": "boolean"}
			description = fmt.Sprintf("The %s instance %s points at: its id, or an object with its id and the %s to record.", link.schema, link.name, pin)
		}
		createProperties[link.name] = map[string]any{
			"description":          description,
			"type":                 []any{"string", "object"},
			"pattern":              idPattern,
			"additionalProperties": false,
			"required":             []any{"id"},
			"properties":           targetProperties,
		}
		fieldProperties[link.name] = map[string]any{
			"description":          fmt.Sprintf("The %s instance %s points at.", link.schema, link.name),
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"schema", "id"},
			"properties":           recordProperties,
		}
		if link.required {
			required = append(required, link.name)
		}
	}
	created := map[string]any{
		"description":          "The links the instance holds from its create, by name, each held to link's rules.",
		"type":                 "object",
		"additionalProperties": false,
		"properties":           createProperties,
	}
	field := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           fieldProperties,
	}
	if len(required) > 0 {
		created["required"] = required
		field["required"] = required
	}
	n.createParams = created
	if err := setField(n, "targets", field, len(required) > 0); err != nil {
		return err
	}

	for _, op := range []string{"link", "unlink", "listLinked"} {
		if err := setOperationProperty(n.params, op, nil, "name", ref(linkName, "")); err != nil {
			return err
		}
	}
	for _, op := range []string{"link", "unlink"} {
		if err := setOperationProperty(n.results, op, nil, "name", ref(linkName, "")); err != nil {
			return err
		}
	}
	return nil
}

// Rollups: the values field holds each rollup's value by its function, or
// { over: true } when its link holds more instances than it reads. min and
// max have no value over no instance.
func narrowRollups(n *narrowing, config map[string]any, _ narrowTarget) error {
	rollups, _ := config["rollups"].(map[string]any)
	over := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"over"},
		"properties": map[string]any{"over": map[string]any{"enum": []any{true}}},
	}
	properties := map[string]any{}
	var required []any
	for _, name := range sortedKeys(rollups) {
		spec, _ := rollups[name].(map[string]any)
		function, _ := spec["function"].(string)
		var value map[string]any
		switch function {
		case "count":
			value = map[string]any{"type": "integer"}
		case "countBy":
			value = map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "integer"}}
		case "sum", "min", "max":
			value = map[string]any{"type": "number"}
		case "all", "any":
			value = map[string]any{"type": "boolean"}
		default:
			return fmt.Errorf("rollup %s: function %q", name, function)
		}
		schema, _ := spec["schema"].(string)
		link, _ := spec["link"].(string)
		properties[name] = map[string]any{
			"description": fmt.Sprintf("%s over the %s instances whose %s points here.", function, schema, link),
			"anyOf":       []any{value, over},
		}
		if function != "min" && function != "max" {
			required = append(required, name)
		}
	}
	field := map[string]any{
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		field["required"] = required
	}
	return setField(n, "values", field, true)
}

// Variants: the type's own field takes a type per value of another; the
// instance's own fields are typed as a union of them.
func narrowVariants(n *narrowing, config map[string]any, target narrowTarget) error {
	field, _ := config["field"].(string)
	by, _ := config["by"].(string)
	types, _ := config["types"].(map[string]any)
	if ownField(target.instance, field) == nil {
		return fmt.Errorf("field %s is not a field of %s", field, target.instance.Name)
	}
	byField := ownField(target.instance, by)
	if byField == nil {
		return fmt.Errorf("by %s is not a field of %s", by, target.instance.Name)
	}
	var members []string
	if enum := target.document.Enums[byField.TypeRef.Name]; enum != nil {
		members = enumValues(enum)
	} else if byField.TypeRef.Name != codegen.PrimitiveString && !stringScalar(target.document, byField.TypeRef.Name) {
		return fmt.Errorf("by %s is not a string or enum field of %s", by, target.instance.Name)
	}
	out := &variantsConfig{field: field, by: by, types: map[string]string{}}
	for _, value := range sortedKeys(types) {
		typeName, _ := types[value].(string)
		if target.document.Types[typeName] == nil || typeName == target.instance.Name {
			return fmt.Errorf("types: %s is not a type of the schema besides %s", typeName, target.instance.Name)
		}
		if members != nil && !slices.Contains(members, value) {
			return fmt.Errorf("types: %s is not a value of %s's enum %s", value, by, byField.TypeRef.Name)
		}
		out.types[value] = typeName
	}
	n.variants = out
	return nil
}

// stringScalar reports whether a scalar's values are strings.
func stringScalar(document *ir.Schema, name string) bool {
	scalar := document.Scalars[name]
	if scalar == nil {
		return false
	}
	return scalarJSONType(scalar) == "string"
}

// enumValues lists an enum's values as an instance holds them.
func enumValues(enum *ir.EnumDef) []string {
	out := make([]string, 0, len(enum.Values))
	for _, value := range enum.Values {
		if value.SerializedAs != "" {
			out = append(out, value.SerializedAs)
		} else {
			out = append(out, value.Name)
		}
	}
	return out
}

// Assignment: the assignee's subject, absent when unassigned.
func narrowAssignment(n *narrowing, _ map[string]any, _ narrowTarget) error {
	return setField(n, "assignee", map[string]any{"type": "string"}, false)
}

// typedField is one field of a behavior as its narrowing types it.
type typedField struct {
	name    string
	schema  map[string]any
	present bool
}

// setFields types the fields given, in order.
func setFields(n *narrowing, fields ...typedField) error {
	for _, field := range fields {
		if err := setField(n, field.name, field.schema, field.present); err != nil {
			return err
		}
	}
	return nil
}

// integer and boolean return the schema of a value of their type.
func integer() map[string]any { return map[string]any{"type": "integer"} }
func boolean() map[string]any { return map[string]any{"type": "boolean"} }

// leaseEnds are the ways a lease ends, as ended's reason holds them: a
// release, an abandon, or the reason expire gives.
var leaseEnds = []string{"release", "abandon", "ttl", "maxHold", "holder"}

// Lease: the holder and the lease's times are absent while the instance is
// free, and how the last lease ended while one is held and before the
// first; the token, active and the expiries are always there.
func narrowLease(n *narrowing, _ map[string]any, _ narrowTarget) error {
	ended := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"reason", "at"},
		"properties": map[string]any{
			"reason": enumOf(leaseEnds),
			"at":     integer(),
		},
	}
	return setFields(n,
		typedField{"holder", map[string]any{"type": "string"}, false},
		typedField{"token", integer(), true},
		typedField{"acquiredAt", integer(), false},
		typedField{"renewedAt", integer(), false},
		typedField{"expiresAt", integer(), false},
		typedField{"active", boolean(), true},
		typedField{"expiries", integer(), true},
		typedField{"ended", ended, false},
	)
}

// Presence: the deadline and the last beat are absent before the first
// beat, and the released leases before a miss; missed is always there.
func narrowPresence(n *narrowing, _ map[string]any, _ narrowTarget) error {
	released := map[string]any{"type": "object", "additionalProperties": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}}
	return setFields(n,
		typedField{"deadline", integer(), false},
		typedField{"lastBeatAt", integer(), false},
		typedField{"missed", boolean(), true},
		typedField{"released", released, false},
	)
}

// Retries: the attempts by class hold every class of the config; the best
// score is absent when no scored result is kept.
func narrowRetries(n *narrowing, config map[string]any, _ narrowTarget) error {
	classes, _ := config["classes"].(map[string]any)
	properties := map[string]any{}
	required := make([]any, 0, len(classes))
	for _, name := range sortedKeys(classes) {
		properties[name] = integer()
		required = append(required, name)
	}
	classAttempts := map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	return setFields(n,
		typedField{"total", integer(), true},
		typedField{"classAttempts", classAttempts, true},
		typedField{"bestScore", map[string]any{"type": "number"}, false},
		typedField{"exhausted", boolean(), true},
		typedField{"stuck", boolean(), true},
	)
}

// Blueprint: the children, each step's key and its child's id, are absent
// until the instance is stamped.
func narrowBlueprint(n *narrowing, _ map[string]any, _ narrowTarget) error {
	child := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"key", "id"},
		"properties": map[string]any{"key": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}},
	}
	return setField(n, "children", map[string]any{"type": "array", "items": child}, false)
}

// Budget: the meters field holds every meter of the config, by name; a
// meter with no limit holds null for its limit and what remains of it.
func narrowBudget(n *narrowing, config map[string]any, _ narrowTarget) error {
	meters, _ := config["meters"].(map[string]any)
	nullable := func() map[string]any { return map[string]any{"type": []any{"integer", "null"}} }
	properties := map[string]any{}
	required := make([]any, 0, len(meters))
	for _, name := range sortedKeys(meters) {
		properties[name] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"used", "reserved", "limit", "remaining"},
			"properties": map[string]any{"used": integer(), "reserved": integer(), "limit": nullable(), "remaining": nullable()},
		}
		required = append(required, name)
	}
	return setField(n, "meters", map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}, true)
}
