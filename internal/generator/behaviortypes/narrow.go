package behaviortypes

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Target is the type a config is narrowed on.
type Target struct {
	// Schema is the schema's name: the engine's name for it, which
	// Dependencies' blockers default to.
	Schema string
	// Base names the types a narrowing refers to (Note: NoteState,
	// NoteFields, NotePatch): the type's name.
	Base     string
	Document *ir.Schema
	Instance *ir.TypeDef
}

// Alias is a type a narrowing names, such as a Workflow's states: a node
// {"$ts": Name} of a narrowed schema refers to it.
type Alias struct {
	Name   string
	Doc    string
	Schema any
}

// FieldShape is a behavior field as a read returns it, under its
// behavior's name: its JSON Schema, and whether every read holds it (an
// engine leaves out a field whose reader returns nothing).
type FieldShape struct {
	Schema  any
	Present bool
}

// Narrowing is what a behavior adds to its type under its config. Every
// schema is a decoded JSON Schema; a node {"$ts": name} is a type the
// narrowing names: one of its Aliases, or the type's own (<Base>Fields,
// <Base>Patch), which only an operation's schemas refer to.
type Narrowing struct {
	Aliases []Alias
	// Fields are the behavior's fields by name.
	Fields map[string]FieldShape
	// Params and Results are each operation's, by name.
	Params  map[string]any
	Results map[string]any
	// Signals are each signal's data, its paramsSchema, by name (D77): a
	// config narrows none.
	Signals map[string]any
	// CreateParams is the create parameters' schema, as the config
	// narrows it; nil when the declaration takes none.
	CreateParams any
	// Preconditions is the declaration's preconditionSchema, or nil.
	Preconditions any
	// Variants is a Variants config, which types the instance's own fields.
	Variants *Variants
}

// Variants is Variants' config: Field holds the type Types names for the
// value By holds.
type Variants struct {
	Field string
	By    string
	Types map[string]string
}

// Behavior is one behavior a type composes: its declaration and what its
// config narrows.
type Behavior struct {
	Declaration registry.Behavior
	Narrowed    Narrowing
}

// narrower narrows one core behavior's shapes under its decoded config.
type narrower func(n *Narrowing, config map[string]any, target Target) error

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
	"Approvals":    narrowApprovals,
	"Numbering":    narrowNumbering,
	"Timers":       narrowTimers,
	"Derived":      narrowDerived,
	"Provenance":   narrowProvenance,
	"Assignment":   narrowAssignment,
	"Lease":        narrowLease,
	"Presence":     narrowPresence,
	"Blueprint":    narrowBlueprint,
	"Budget":       narrowBudget,
	"Retries":      narrowRetries,
	"Stock":        narrowStock,
	"Calendar":     narrowCalendar,
	"Holds":        narrowHolds,
	"Payments":     narrowPayments,
}

// idPattern is the pattern of an instance id, as the engine's declarations
// write it.
const idPattern = "^[A-Za-z0-9][A-Za-z0-9._:-]{0,255}$"

// NarrowerNames lists the behaviors the core narrows, sorted.
func NarrowerNames() []string {
	return SortedKeys(coreNarrowers)
}

// Narrow starts from the declaration's shapes and applies the behavior's
// core narrowing, when it has one.
func Narrow(declaration registry.Behavior, config json.RawMessage, target Target) (Narrowing, error) {
	n := Narrowing{Fields: map[string]FieldShape{}, Params: map[string]any{}, Results: map[string]any{}, Signals: map[string]any{}}
	for _, field := range declaration.Fields {
		n.Fields[field.Name] = FieldShape{Schema: map[string]any{"description": field.Description}}
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
		n.Params[op.Name] = params
		n.Results[op.Name] = result
	}
	for _, signal := range declaration.Signals {
		data, err := decodeSchema(signal.ParamsSchema)
		if err != nil {
			return n, fmt.Errorf("signal %s paramsSchema: %w", signal.Name, err)
		}
		n.Signals[signal.Name] = data
	}
	var err error
	if n.CreateParams, err = decodeSchema(declaration.CreateParamsSchema); err != nil {
		return n, fmt.Errorf("createParamsSchema: %w", err)
	}
	if n.Preconditions, err = decodeSchema(declaration.PreconditionSchema); err != nil {
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

// ref is a node that renders as a type the narrowing names, keeping a
// description for its doc comment.
func ref(name, description string) map[string]any {
	node := map[string]any{RefKey: name}
	if description != "" {
		node["description"] = description
	}
	return node
}

// EnumOf is a schema whose values are the strings given.
func EnumOf(values []string) map[string]any {
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
func setField(n *Narrowing, name string, schema map[string]any, present bool) error {
	current, ok := n.Fields[name]
	if !ok {
		return fmt.Errorf("the declaration has no field %s", name)
	}
	if description := DescriptionOf(current.Schema); description != "" {
		schema["description"] = description
	}
	n.Fields[name] = FieldShape{Schema: schema, Present: present}
	return nil
}

// Workflow: the status and transition's states are the config's; when the
// status was entered is absent on an instance from before Workflow kept it.
func narrowWorkflow(n *Narrowing, config map[string]any, target Target) error {
	states := stringsOf(config["states"])
	state := target.Base + "State"
	n.Aliases = append(n.Aliases, Alias{Name: state, Doc: fmt.Sprintf("A state of %s's Workflow.", An(target.Base)), Schema: EnumOf(states)})
	if err := setField(n, "status", ref(state, ""), true); err != nil {
		return err
	}
	if err := setField(n, "enteredAt", integer(), false); err != nil {
		return err
	}
	if err := setOperationProperty(n.Params, "transition", nil, "to", ref(state, "")); err != nil {
		return err
	}
	for _, name := range []string{"from", "to"} {
		if err := setOperationProperty(n.Results, "transition", nil, name, ref(state, "")); err != nil {
			return err
		}
	}
	return nil
}

// commentsMessageResults are Comments' operations whose result is one
// message's record.
var commentsMessageResults = []string{"post", "begin", "finish", "interrupt", "edit", "deleteMessage", "react", "unreact", "pin", "unpin", "resolve", "reopen", "getMessage"}

// commentsBlocksGiven are Comments' operations whose parameters give a
// message's blocks: post and finish, which take none when the config
// allows none, and edit.
var commentsBlocksGiven = []string{"post", "finish", "edit"}

// Comments (D77): the count of messages, the threads' counts, who is
// composing and the reader's unread count are always there, every instance
// starting with an empty conversation; when the latest message stood is
// absent while none has. A message's stand and listMessages' readUpTo are
// as declared, optional. Its namespace operations (inbox, claimDeliveries,
// settleDeliveries) belong to no type, and no config narrows them.
// A block is one of the kinds post's parameters declare, and a message's
// record is post's result with its blocks typed so: <Base>CommentBlock
// and <Base>CommentMessage, which every operation that returns a record
// (begin, finish and interrupt among them) returns and listMessages' page
// holds. A record keeps every kind of block, since narrowing the config
// leaves posted blocks as they are; what post, finish and edit give is of
// the kinds the config's blocks allow, and with none allowed post and
// finish take no blocks.
func narrowComments(n *Narrowing, config map[string]any, target Target) error {
	given, err := at(n.Params["post"], "properties", "blocks", "items")
	if err != nil {
		return fmt.Errorf("operation post: %w", err)
	}
	kinds, _ := given["oneOf"].([]any)
	if len(kinds) == 0 {
		return fmt.Errorf("operation post: a block is not one of its kinds")
	}
	block := target.Base + "CommentBlock"
	message := target.Base + "CommentMessage"
	n.Aliases = append(n.Aliases, Alias{
		Name:   block,
		Doc:    fmt.Sprintf("A block of a message of %s's Comments: a structured part beside its body, of a kind Comments declares.", An(target.Base)),
		Schema: map[string]any{"oneOf": cloneValue(kinds)},
	})
	posted, ok := n.Results["post"].(map[string]any)
	if !ok {
		return fmt.Errorf("operation post has no result schema")
	}
	record := cloneSchema(posted)
	delete(record, "description")
	if err := setItems(record, ref(block, ""), "properties", "blocks"); err != nil {
		return fmt.Errorf("operation post: %w", err)
	}
	n.Aliases = append(n.Aliases, Alias{
		Name:   message,
		Doc:    fmt.Sprintf("A message of %s's Comments, as every operation that returns one gives it.", An(target.Base)),
		Schema: record,
	})
	for _, op := range commentsMessageResults {
		result, ok := n.Results[op].(map[string]any)
		if !ok {
			return fmt.Errorf("operation %s has no result schema", op)
		}
		n.Results[op] = ref(message, DescriptionOf(result))
	}
	if err := setItems(n.Results["listMessages"], ref(message, ""), "properties", "items"); err != nil {
		return fmt.Errorf("operation listMessages: %w", err)
	}
	root, err := at(n.Results["listMessages"], "properties", "root")
	if err != nil {
		return fmt.Errorf("operation listMessages: %w", err)
	}
	if err := setOperationProperty(n.Results, "listMessages", nil, "root", ref(message, DescriptionOf(root))); err != nil {
		return err
	}
	if err := setItems(n.Results["listEdits"], ref(block, ""), "properties", "items", "items", "properties", "blocks"); err != nil {
		return fmt.Errorf("operation listEdits: %w", err)
	}
	allowed := kinds
	if list, ok := config["blocks"].([]any); ok {
		allowed = nil
		for _, kind := range kinds {
			if slices.Contains(stringsOf(list), blockKind(kind)) {
				allowed = append(allowed, kind)
			}
		}
	}
	for _, op := range commentsBlocksGiven {
		if len(allowed) == 0 && op != "edit" {
			if err := deleteProperty(n.Params[op], "blocks"); err != nil {
				return fmt.Errorf("operation %s: %w", op, err)
			}
			continue
		}
		if len(allowed) == 0 {
			// An edit giving no blocks is the only way to clear the
			// blocks a message was posted with before the config named
			// none, so edit keeps blocks, as an empty array.
			if err := emptyArray(n.Params[op], "properties", "blocks"); err != nil {
				return fmt.Errorf("operation %s: %w", op, err)
			}
			continue
		}
		var items map[string]any
		if len(allowed) == len(kinds) {
			items = ref(block, "")
		} else {
			items = map[string]any{"oneOf": cloneValue(allowed)}
		}
		if err := setItems(n.Params[op], items, "properties", "blocks"); err != nil {
			return fmt.Errorf("operation %s: %w", op, err)
		}
	}
	return setFields(n,
		typedField{"messages", integer(), true},
		typedField{"lastMessageAt", integer(), false},
		typedField{"threads", map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"open", "resolved"},
			"properties": map[string]any{"open": integer(), "resolved": integer()},
		}, true},
		typedField{"composing", map[string]any{"type": "array", "items": map[string]any{"type": "string"}}, true},
		typedField{"unread", integer(), true},
	)
}

// blockKind is the kind a block's schema holds: its kind's const.
func blockKind(schema any) string {
	kind, _ := at(schema, "properties", "kind")
	text, _ := kind["const"].(string)
	return text
}

// emptyArray narrows the array schema at a path of object keys to the
// empty array: no items (never[] in TypeScript), and at most none.
func emptyArray(schema any, path ...string) error {
	array, err := at(schema, path...)
	if err != nil {
		return err
	}
	if _, ok := array["items"]; !ok {
		return fmt.Errorf("the declaration's schema at %v holds no items", path)
	}
	array["items"] = false
	array["maxItems"] = json.Number("0")
	return nil
}

// setItems replaces the items of the array schema at a path of object
// keys, keeping their description.
func setItems(schema any, items map[string]any, path ...string) error {
	array, err := at(schema, path...)
	if err != nil {
		return err
	}
	current, ok := array["items"].(map[string]any)
	if !ok {
		return fmt.Errorf("the declaration's schema at %v holds no items", path)
	}
	if description, ok := current["description"].(string); ok {
		if _, has := items["description"]; !has {
			items["description"] = description
		}
	}
	array["items"] = items
	return nil
}

// deleteProperty removes a property of an object schema; it must exist.
func deleteProperty(schema any, name string) error {
	properties, err := at(schema, "properties")
	if err != nil {
		return err
	}
	if _, ok := properties[name]; !ok {
		return fmt.Errorf("the declaration's schema has no property %s", name)
	}
	delete(properties, name)
	return nil
}

// Revisions: a proposal's patch is the type's patch, a revision's data its
// own fields; the revision number is absent until the first revision.
func narrowRevisions(n *Narrowing, _ map[string]any, target Target) error {
	if err := setField(n, "revision", map[string]any{"type": "integer"}, false); err != nil {
		return err
	}
	patch := target.Base + "Patch"
	if err := setOperationProperty(n.Params, "propose", nil, "patch", ref(patch, "")); err != nil {
		return err
	}
	for _, op := range []string{"propose", "approve", "reject"} {
		if err := setOperationProperty(n.Results, op, nil, "patch", ref(patch, "")); err != nil {
			return err
		}
	}
	if err := setOperationProperty(n.Results, "listProposals", []string{"properties", "items", "items"}, "patch", ref(patch, "")); err != nil {
		return err
	}
	return setOperationProperty(n.Results, "listRevisions", []string{"properties", "items", "items"}, "data", ref(target.Base+"Fields", ""))
}

// Dependencies: a blocker is an instance of a schema the config lists, the
// type's own when it lists none, and names its schema unless it is the
// type's own. The create parameters are the engine's narrowing
// (dependenciesCreateParams).
func narrowDependencies(n *Narrowing, config map[string]any, target Target) error {
	schemas := stringsOf(config["schemas"])
	if len(schemas) == 0 {
		schemas = []string{target.Schema}
	}
	own := slices.Contains(schemas, target.Schema)
	required := []any{"id"}
	if !own {
		required = []any{"id", "schema"}
	}
	blockerSchema := func() map[string]any {
		schema := EnumOf(schemas)
		if own {
			schema["description"] = fmt.Sprintf("The blocker's schema; %s when absent.", target.Schema)
		} else {
			schema["description"] = "The blocker's schema."
		}
		return schema
	}
	n.CreateParams = map[string]any{
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
		params, err := at(n.Params[op])
		if err != nil {
			return err
		}
		if err := setProperty(params, "schema", blockerSchema()); err != nil {
			return fmt.Errorf("operation %s: %w", op, err)
		}
		params["required"] = required
		if err := setOperationProperty(n.Results, op, nil, "schema", EnumOf(schemas)); err != nil {
			return err
		}
	}
	if err := setOperationProperty(n.Results, "listBlockers", []string{"properties", "items", "items"}, "schema", EnumOf(schemas)); err != nil {
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
	// many is whether the link holds a list of targets, of at most max
	// (the engine's linkMany).
	many bool
	max  int
}

func linksOf(config map[string]any) []linkSpec {
	links, _ := config["links"].(map[string]any)
	out := make([]linkSpec, 0, len(links))
	for _, name := range SortedKeys(links) {
		spec, _ := links[name].(map[string]any)
		schema, _ := spec["schema"].(string)
		required, _ := spec["required"].(bool)
		many, _ := spec["many"].(bool)
		most, _ := spec["max"].(float64)
		out = append(out, linkSpec{name: name, schema: schema, required: required, pin: linkPin(spec["pinned"]), many: many, max: int(most)})
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
// pinned link, and a list of at most max targets for a many-valued link,
// at least one when it is required; the targets field holds each link the
// instance has, with its target's schema, and for a pinned link its pin,
// the target's latest of that kind and whether the target has moved past
// the pin, a list of them for a many-valued link.
func narrowLinks(n *Narrowing, config map[string]any, target Target) error {
	links := linksOf(config)
	names := make([]string, 0, len(links))
	for _, link := range links {
		names = append(names, link.name)
	}
	linkName := target.Base + "LinkName"
	n.Aliases = append(n.Aliases, Alias{Name: linkName, Doc: fmt.Sprintf("A link of %s, by name.", An(target.Base)), Schema: EnumOf(names)})

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
		entry := map[string]any{
			"description":          description,
			"type":                 []any{"string", "object"},
			"pattern":              idPattern,
			"additionalProperties": false,
			"required":             []any{"id"},
			"properties":           targetProperties,
		}
		record := map[string]any{
			"description":          fmt.Sprintf("The %s instance %s points at.", link.schema, link.name),
			"type":                 "object",
			"additionalProperties": false,
			"required":             []any{"schema", "id"},
			"properties":           recordProperties,
		}
		if link.many {
			entry = listOf(entry, fmt.Sprintf("The %s instances %s points at, in order, each once.", link.schema, link.name), link)
			record = listOf(record, fmt.Sprintf("The %s instances %s points at, in the order they were linked.", link.schema, link.name), link)
		}
		createProperties[link.name] = entry
		fieldProperties[link.name] = record
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
	n.CreateParams = created
	if err := setField(n, "targets", field, len(required) > 0); err != nil {
		return err
	}

	for _, op := range []string{"link", "unlink", "listLinked"} {
		if err := setOperationProperty(n.Params, op, nil, "name", ref(linkName, "")); err != nil {
			return err
		}
	}
	for _, op := range []string{"link", "unlink"} {
		if err := setOperationProperty(n.Results, op, nil, "name", ref(linkName, "")); err != nil {
			return err
		}
	}
	return nil
}

// listOf is a many-valued link's list of what items describes one target
// with: at most its max, and at least one when it is required.
func listOf(items map[string]any, description string, link linkSpec) map[string]any {
	list := map[string]any{"description": description, "type": "array", "maxItems": json.Number(strconv.Itoa(link.max)), "items": items}
	if link.required {
		list["minItems"] = json.Number("1")
	}
	return list
}

// Rollups: the values field holds each rollup's value by its function, or
// { over: true } when its link holds more instances than it reads. min and
// max have no value over no instance.
func narrowRollups(n *Narrowing, config map[string]any, _ Target) error {
	rollups, _ := config["rollups"].(map[string]any)
	over := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"over"},
		"properties": map[string]any{"over": map[string]any{"enum": []any{true}}},
	}
	properties := map[string]any{}
	var required []any
	for _, name := range SortedKeys(rollups) {
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
func narrowVariants(n *Narrowing, config map[string]any, target Target) error {
	field, _ := config["field"].(string)
	by, _ := config["by"].(string)
	types, _ := config["types"].(map[string]any)
	if OwnField(target.Instance, field) == nil {
		return fmt.Errorf("field %s is not a field of %s", field, target.Instance.Name)
	}
	byField := OwnField(target.Instance, by)
	if byField == nil {
		return fmt.Errorf("by %s is not a field of %s", by, target.Instance.Name)
	}
	var members []string
	if enum := target.Document.Enums[byField.TypeRef.Name]; enum != nil {
		members = EnumValues(enum)
	} else if byField.TypeRef.Name != codegen.PrimitiveString && !stringScalar(target.Document, byField.TypeRef.Name) {
		return fmt.Errorf("by %s is not a string or enum field of %s", by, target.Instance.Name)
	}
	out := &Variants{Field: field, By: by, Types: map[string]string{}}
	for _, value := range SortedKeys(types) {
		typeName, _ := types[value].(string)
		if target.Document.Types[typeName] == nil || typeName == target.Instance.Name {
			return fmt.Errorf("types: %s is not a type of the schema besides %s", typeName, target.Instance.Name)
		}
		if members != nil && !slices.Contains(members, value) {
			return fmt.Errorf("types: %s is not a value of %s's enum %s", value, by, byField.TypeRef.Name)
		}
		out.Types[value] = typeName
	}
	n.Variants = out
	return nil
}

// Numbering: the number and its label are absent on an instance made
// before its schema composed Numbering, until assignNumber numbers it. The
// config changes neither's type, so a nil config narrows as any does.
func narrowNumbering(n *Narrowing, _ map[string]any, _ Target) error {
	return setFields(n,
		typedField{"seq", integer(), false},
		typedField{"label", map[string]any{"type": "string"}, false},
	)
}

// timersOutcomes are what a fire's move meets, as a deadline's outcome
// holds it.
var timersOutcomes = []string{"moved", "skipped", "vetoed", "forbidden"}

// Timers: the deadlines field holds, by deadline of the config, the ones
// that have started, so none is required; each has its due time and
// whether it fired, and once fired when, and with a then its outcome. The
// operations name a deadline of the config.
func narrowTimers(n *Narrowing, config map[string]any, _ Target) error {
	deadlines, _ := config["deadlines"].(map[string]any)
	names := SortedKeys(deadlines)
	properties := map[string]any{}
	for _, name := range names {
		properties[name] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"dueAt", "fired"},
			"properties": map[string]any{
				"dueAt":   integer(),
				"fired":   boolean(),
				"firedAt": integer(),
				"outcome": EnumOf(timersOutcomes),
			},
		}
	}
	for _, op := range []string{"fireTimer", "resetTimer", "snoozeTimer"} {
		if err := setOperationProperty(n.Params, op, nil, "deadline", EnumOf(names)); err != nil {
			return err
		}
	}
	return setField(n, "deadlines", map[string]any{"type": "object", "additionalProperties": false, "properties": properties}, true)
}

// stringScalar reports whether a scalar's values are strings.
func stringScalar(document *ir.Schema, name string) bool {
	scalar := document.Scalars[name]
	if scalar == nil {
		return false
	}
	return ScalarJSONType(scalar) == "string"
}

// EnumValues lists an enum's values as an instance holds them.
func EnumValues(enum *ir.EnumDef) []string {
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

// Approvals: the gates field holds, by gated state, the subjects whose
// sign-offs count, and no gate with none, so none is required; signOff and
// withdrawSignOff name a gated state. listSignOffs takes any state, a gate
// a later version removed included.
func narrowApprovals(n *Narrowing, config map[string]any, _ Target) error {
	gates, _ := config["gates"].(map[string]any)
	states := SortedKeys(gates)
	properties := map[string]any{}
	for _, state := range states {
		properties[state] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"signedBy"},
			"properties": map[string]any{"signedBy": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		}
	}
	for _, op := range []string{"signOff", "withdrawSignOff"} {
		if err := setOperationProperty(n.Params, op, nil, "state", EnumOf(states)); err != nil {
			return err
		}
		if err := setOperationProperty(n.Results, op, nil, "state", EnumOf(states)); err != nil {
			return err
		}
	}
	return setField(n, "gates", map[string]any{"type": "object", "additionalProperties": false, "properties": properties}, true)
}

// Assignment: the assignee's subject, absent when unassigned.
func narrowAssignment(n *Narrowing, _ map[string]any, _ Target) error {
	return setField(n, "assignee", map[string]any{"type": "string"}, false)
}

// typedField is one field of a behavior as its narrowing types it.
type typedField struct {
	name    string
	schema  map[string]any
	present bool
}

// setFields types the fields given, in order.
func setFields(n *Narrowing, fields ...typedField) error {
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
func narrowLease(n *Narrowing, _ map[string]any, _ Target) error {
	ended := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"reason", "at"},
		"properties": map[string]any{
			"reason": EnumOf(leaseEnds),
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
func narrowPresence(n *Narrowing, _ map[string]any, _ Target) error {
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
func narrowRetries(n *Narrowing, config map[string]any, _ Target) error {
	classes, _ := config["classes"].(map[string]any)
	properties := map[string]any{}
	required := make([]any, 0, len(classes))
	for _, name := range SortedKeys(classes) {
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
func narrowBlueprint(n *Narrowing, _ map[string]any, _ Target) error {
	child := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"key", "id"},
		"properties": map[string]any{"key": map[string]any{"type": "string"}, "id": map[string]any{"type": "string"}},
	}
	return setField(n, "children", map[string]any{"type": "array", "items": child}, false)
}

// Budget: the meters field holds every meter of the config, by name; a
// meter with no limit holds null for its limit and what remains of it.
func narrowBudget(n *Narrowing, config map[string]any, _ Target) error {
	meters, _ := config["meters"].(map[string]any)
	nullable := func() map[string]any { return map[string]any{"type": []any{"integer", "null"}} }
	properties := map[string]any{}
	required := make([]any, 0, len(meters))
	for _, name := range SortedKeys(meters) {
		properties[name] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"used", "reserved", "limit", "remaining"},
			"properties": map[string]any{"used": integer(), "reserved": integer(), "limit": nullable(), "remaining": nullable()},
		}
		required = append(required, name)
	}
	return setField(n, "meters", map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}, true)
}

// Stock (D70): the counts and low are always there, every instance
// starting at zero.
func narrowStock(n *Narrowing, _ map[string]any, _ Target) error {
	return setFields(n,
		typedField{"onHand", integer(), true},
		typedField{"held", integer(), true},
		typedField{"available", integer(), true},
		typedField{"low", boolean(), true},
	)
}

// Calendar (D70): the time zone, the weekly hours, the config's until an
// instance sets its own, and the closures that have not ended are always
// there; hours have setOpeningHours' shape and a closure addClosure's
// result's.
func narrowCalendar(n *Narrowing, _ map[string]any, _ Target) error {
	hours, err := at(n.Params["setOpeningHours"], "properties", "hours")
	if err != nil {
		return fmt.Errorf("operation setOpeningHours: %w", err)
	}
	week, ok := firstOf(hours)
	if !ok {
		return fmt.Errorf("operation setOpeningHours: hours is not one of an object and null")
	}
	closure, ok := n.Results["addClosure"].(map[string]any)
	if !ok {
		return fmt.Errorf("operation addClosure has no result schema")
	}
	return setFields(n,
		typedField{"timeZone", map[string]any{"type": "string"}, true},
		typedField{"hours", cloneSchema(week), true},
		typedField{"closures", map[string]any{"type": "array", "items": cloneSchema(closure)}, true},
	)
}

// Holds (D70): an item names a hold of the config; items, state and total
// are always there, with the shapes retakeHolds' result gives them, which
// listHolds' and retakeHolds' results share.
func narrowHolds(n *Narrowing, config map[string]any, target Target) error {
	holds, _ := config["holds"].(map[string]any)
	name := target.Base + "HoldName"
	n.Aliases = append(n.Aliases, Alias{Name: name, Doc: fmt.Sprintf("A hold of %s's Holds, by name.", An(target.Base)), Schema: EnumOf(SortedKeys(holds))})
	for _, op := range []string{"retakeHolds", "listHolds"} {
		if err := setOperationProperty(n.Results, op, []string{"properties", "items", "items"}, "hold", ref(name, "")); err != nil {
			return err
		}
	}
	result, err := at(n.Results["retakeHolds"], "properties")
	if err != nil {
		return fmt.Errorf("operation retakeHolds: %w", err)
	}
	var fields []typedField
	for _, field := range []string{"items", "state", "total"} {
		schema, ok := result[field].(map[string]any)
		if !ok {
			return fmt.Errorf("operation retakeHolds: the result has no %s", field)
		}
		fields = append(fields, typedField{field, cloneSchema(schema), true})
	}
	return setFields(n, fields...)
}

// paymentsStates are what Payments.state holds.
var paymentsStates = []string{"unpaid", "pending", "authorized", "paid", "partiallyRefunded", "refunded"}

// Payments (D70): the amounts and state are always there; the currency is
// absent while the amount comes from Holds.total and the claim holds
// nothing priced. An attempt has openPayment's result's shape, with what
// the provider reported and when it opened and last moved; the results
// that give Payments.state after a call give it as the field does.
func narrowPayments(n *Narrowing, _ map[string]any, _ Target) error {
	opened, err := at(n.Results["openPayment"], "properties")
	if err != nil {
		return fmt.Errorf("operation openPayment: %w", err)
	}
	status, ok := opened["status"].(map[string]any)
	if !ok {
		return fmt.Errorf("operation openPayment: the result has no status")
	}
	for _, op := range []string{"recordPaymentEvent", "settleRefundFor"} {
		if err := setOperationProperty(n.Results, op, nil, "state", EnumOf(paymentsStates)); err != nil {
			return err
		}
	}
	str := func() map[string]any { return map[string]any{"type": "string"} }
	attempt := map[string]any{
		"type": "object", "additionalProperties": false,
		"required": []any{"attempt", "provider", "amount", "currency", "status", "openedAt", "updatedAt"},
		"properties": map[string]any{
			"attempt":     str(),
			"provider":    str(),
			"providerRef": str(),
			"amount":      integer(),
			"currency":    str(),
			"status":      cloneSchema(status),
			"authorized":  integer(),
			"captured":    integer(),
			"brand":       str(),
			"last4":       str(),
			"openedAt":    integer(),
			"updatedAt":   integer(),
		},
	}
	return setFields(n,
		typedField{"due", integer(), true},
		typedField{"authorized", integer(), true},
		typedField{"captured", integer(), true},
		typedField{"refunded", integer(), true},
		typedField{"outstanding", integer(), true},
		typedField{"overpaid", integer(), true},
		typedField{"currency", str(), false},
		typedField{"state", EnumOf(paymentsStates), true},
		typedField{"attempts", map[string]any{"type": "array", "items": attempt}, true},
	)
}

// firstOf is the first schema of a node's oneOf or anyOf: the non-null
// member of a nullable value.
func firstOf(node map[string]any) (map[string]any, bool) {
	for _, key := range []string{"oneOf", "anyOf"} {
		if members, ok := node[key].([]any); ok && len(members) > 0 {
			first, ok := members[0].(map[string]any)
			return first, ok
		}
	}
	return nil, false
}

// cloneSchema copies a decoded schema deeply, so a field's shape and the
// operation it was read from can be narrowed apart.
func cloneSchema(schema map[string]any) map[string]any {
	return cloneValue(schema).(map[string]any)
}

func cloneValue(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))
		for key, member := range v {
			out[key] = cloneValue(member)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, member := range v {
			out[i] = cloneValue(member)
		}
		return out
	}
	return value
}

// fieldNames is a schema of a list of the names given, as an operation's
// paramsSchema lists them: at least one, each once.
func fieldNames(names []string) map[string]any {
	return map[string]any{"type": "array", "minItems": 1, "uniqueItems": true, "items": EnumOf(names)}
}

// ownField is a node that renders as the TypeScript type of one of the
// type's own fields, without the absence an optional field allows.
func ownField(target Target, name string) map[string]any {
	return ref(fmt.Sprintf("NonNullable<%sFields[%s]>", target.Base, strconv.Quote(name)), "")
}

// Derived: the fields field holds, by derived field of the config, its last
// settled value's record and its override, so none is required; stale
// lists derived fields. The operations name derived fields, and
// applyDerived's values are of each field's own type (D74).
func narrowDerived(n *Narrowing, config map[string]any, target Target) error {
	fields, _ := config["fields"].(map[string]any)
	names := SortedKeys(fields)
	record := map[string]any{
		"type": "object", "additionalProperties": false,
		"properties": map[string]any{
			"sourceHash": map[string]any{"type": "string"},
			"model":      map[string]any{"type": "string"},
			"settledAt":  integer(),
			"settledBy":  map[string]any{"type": "string"},
			"confidence": map[string]any{"type": "number"},
			"overridden": map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"by", "at"},
				"properties": map[string]any{"by": map[string]any{"type": "string"}, "at": integer()},
			},
		},
	}
	properties := map[string]any{}
	values := map[string]any{}
	for _, name := range names {
		properties[name] = record
		values[name] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"sourceHash", "value"},
			"properties": map[string]any{
				"sourceHash": map[string]any{"type": "string", "minLength": 1},
				"value":      ownField(target, name),
				"confidence": map[string]any{"type": "number", "minimum": 0, "maximum": 1},
				"usage":      map[string]any{"type": "object"},
			},
		}
	}
	settledNames := map[string]any{"type": "array", "items": EnumOf(names)}
	for _, step := range []error{
		setOperationProperty(n.Params, "staleDerivations", nil, "field", EnumOf(names)),
		setOperationProperty(n.Params, "applyDerived", nil, "fields", map[string]any{"type": "object", "minProperties": 1, "additionalProperties": false, "properties": values}),
		setOperationProperty(n.Results, "applyDerived", nil, "settled", settledNames),
		setOperationProperty(n.Params, "rederive", nil, "fields", fieldNames(names)),
		setOperationProperty(n.Results, "rederive", nil, "stale", map[string]any{"type": "array", "items": EnumOf(names)}),
		setField(n, "fields", map[string]any{"type": "object", "additionalProperties": false, "properties": properties}, true),
	} {
		if step != nil {
			return step
		}
	}
	return setField(n, "stale", map[string]any{"type": "array", "items": EnumOf(names)}, true)
}

// provenanceReviews are the states a recorded field's review takes.
var provenanceReviews = []string{"proposed", "accepted", "rejected", "edited"}

// Provenance: the fields field holds, by recorded field (the config's, or
// every own field), its last write's record, so none is required; accept
// and reject name recorded fields, and reject's patch holds their own
// types (D74).
func narrowProvenance(n *Narrowing, config map[string]any, target Target) error {
	names := stringsOf(config["fields"])
	if _, given := config["fields"]; !given && target.Instance != nil {
		for _, field := range target.Instance.Fields {
			if field == nil {
				continue
			}
			if field.JSONTag != "" {
				names = append(names, field.JSONTag)
			} else {
				names = append(names, field.Name)
			}
		}
	}
	slices.Sort(names)
	record := map[string]any{
		"type": "object", "additionalProperties": false, "required": []any{"source", "by", "at"},
		"properties": map[string]any{
			"source":     map[string]any{"type": "string"},
			"by":         map[string]any{"type": "string"},
			"at":         integer(),
			"confidence": map[string]any{"type": "number"},
			"review": map[string]any{
				"type": "object", "additionalProperties": false, "required": []any{"state"},
				"properties": map[string]any{"state": EnumOf(provenanceReviews), "by": map[string]any{"type": "string"}, "at": integer()},
			},
		},
	}
	properties := map[string]any{}
	quoted := make([]string, 0, len(names))
	for _, name := range names {
		properties[name] = record
		quoted = append(quoted, strconv.Quote(name))
	}
	picked := "never"
	if len(quoted) > 0 {
		picked = strings.Join(quoted, " | ")
	}
	for _, step := range []error{
		setOperationProperty(n.Params, "accept", nil, "fields", fieldNames(names)),
		setOperationProperty(n.Results, "accept", nil, "accepted", map[string]any{"type": "array", "items": EnumOf(names)}),
		setOperationProperty(n.Params, "reject", nil, "fields", fieldNames(names)),
		setOperationProperty(n.Params, "reject", nil, "patch", ref(fmt.Sprintf("Partial<Pick<%sFields, %s>>", target.Base, picked), "")),
		setOperationProperty(n.Results, "reject", nil, "rejected", map[string]any{"type": "array", "items": EnumOf(names)}),
		setField(n, "fields", map[string]any{"type": "object", "additionalProperties": false, "properties": properties}, true),
	} {
		if step != nil {
			return step
		}
	}
	return setField(n, "inReview", boolean(), true)
}
