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

// fieldShape is a behavior field as a read returns it: its JSON Schema, and
// whether every read holds it (an engine leaves out a field whose reader
// returns nothing).
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
	"Lease":        objectFields,
	"Presence":     objectFields,
	"Blueprint":    objectFields,
	"Budget":       objectFields,
	"Retries":      objectFields,
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

// Workflow: the status and transition's states are the config's.
func narrowWorkflow(n *narrowing, config map[string]any, target narrowTarget) error {
	states := stringsOf(config["states"])
	state := target.base + "State"
	n.aliases = append(n.aliases, alias{name: state, doc: fmt.Sprintf("A state of %s's Workflow.", an(target.base)), schema: enumOf(states)})
	n.fields["status"] = fieldShape{schema: ref(state, describedField(n, "status")), present: true}
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

// describedField is the declaration's description of a field.
func describedField(n *narrowing, name string) string {
	return descriptionOf(n.fields[name].schema)
}

// Comments: the count is always there.
func narrowComments(n *narrowing, _ map[string]any, _ narrowTarget) error {
	n.fields["commentCount"] = fieldShape{schema: map[string]any{"type": "integer", "description": describedField(n, "commentCount")}, present: true}
	return nil
}

// Revisions: a proposal's patch is the type's patch, a revision's data its
// own fields; the revision number is absent until the first revision.
func narrowRevisions(n *narrowing, _ map[string]any, target narrowTarget) error {
	n.fields["revision"] = fieldShape{schema: map[string]any{"type": "integer", "description": describedField(n, "revision")}}
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
	n.fields["blocked"] = fieldShape{schema: map[string]any{"type": "boolean", "description": describedField(n, "blocked")}, present: true}
	return nil
}

// linkSpec is one link of a Links config.
type linkSpec struct {
	name     string
	schema   string
	required bool
	pinned   bool
}

func linksOf(config map[string]any) []linkSpec {
	links, _ := config["links"].(map[string]any)
	out := make([]linkSpec, 0, len(links))
	for _, name := range sortedKeys(links) {
		spec, _ := links[name].(map[string]any)
		schema, _ := spec["schema"].(string)
		required, _ := spec["required"].(bool)
		pinned, _ := spec["pinned"].(bool)
		out = append(out, linkSpec{name: name, schema: schema, required: required, pinned: pinned})
	}
	return out
}

// Links: the names are the config's; the create parameters are the
// engine's narrowing (linksCreateParams): a property per link, the
// required ones required, a revision only for a pinned link; the field
// holds each link the instance has, with its target's schema.
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
		if link.pinned {
			targetProperties["revision"] = map[string]any{"description": "The target's revision to record, one it has had; its latest when absent.", "type": "integer", "minimum": json.Number("1")}
			recordProperties["revision"] = map[string]any{"description": "The target's revision the link records.", "type": "integer"}
			recordProperties["stale"] = map[string]any{"description": "Whether the target has moved past that revision.", "type": "boolean"}
			description = fmt.Sprintf("The %s instance %s points at: its id, or an object with its id and the revision to record.", link.schema, link.name)
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
		"description":          describedField(n, "links"),
		"type":                 "object",
		"additionalProperties": false,
		"properties":           fieldProperties,
	}
	if len(required) > 0 {
		created["required"] = required
		field["required"] = required
	}
	n.createParams = created
	n.fields["links"] = fieldShape{schema: field, present: len(required) > 0}

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

// Rollups: each rollup's value by its function, or { over: true } when its
// link holds more instances than it reads. min and max have no value over
// no instance.
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
		"description":          describedField(n, "rollups"),
		"type":                 "object",
		"additionalProperties": false,
		"properties":           properties,
	}
	if len(required) > 0 {
		field["required"] = required
	}
	n.fields["rollups"] = fieldShape{schema: field, present: true}
	return nil
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
	n.fields["assignee"] = fieldShape{schema: map[string]any{"type": "string", "description": describedField(n, "assignee")}}
	return nil
}

// objectFields types each field of a work-queue behavior as a JSON object,
// which is all their declarations say of them.
func objectFields(n *narrowing, _ map[string]any, _ narrowTarget) error {
	for _, name := range sortedKeys(n.fields) {
		n.fields[name] = fieldShape{schema: map[string]any{"type": "object", "description": describedField(n, name)}}
	}
	return nil
}
