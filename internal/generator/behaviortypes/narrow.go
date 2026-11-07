package behaviortypes

import (
	"encoding/json"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
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
	// Fields are the behavior's fields by name: every declared field but
	// one the type's form keeps none of (an engine type's Retries'
	// retryAt, failingSince and keyedBy, D104), which a consumer leaves
	// out.
	Fields map[string]FieldShape
	// Params and Results are each operation's, by name.
	Params  map[string]any
	Results map[string]any
	// Signals are each signal's data, its paramsSchema, by name (D77): a
	// config narrows none.
	Signals map[string]any
	// Events are each event's payload, its payloadSchema, by kind, as the
	// config narrows it (D104): what the API's Workers method of the event
	// takes.
	Events map[string]any
	// CreateParams is the create parameters' schema, as the config
	// narrows it; nil when the declaration takes none.
	CreateParams any
	// Preconditions is the declaration's preconditionSchema, or nil.
	Preconditions any
	// Variants is a Variants config, which types the instance's own fields.
	Variants *Variants
	// Vetoes are the codes of the declaration's vetoes a caller of the type
	// may meet, in declaration order: all of them, but for a code the
	// type's form never answers (an engine type's Workflow answers no
	// not_principal or stale, D104).
	Vetoes []string
	// Refused are the operations of the declaration a type's form refuses
	// every call of, in declaration order: an engine type's Budget refuses
	// observe (D104). A client of the type offers none of them.
	Refused []string
	// Permissions are the permissions the config names under members whose
	// names do not say so (Seats' take and assign), sorted, each once, which
	// behaviorops lists beside ConfigPermissions' (D104).
	Permissions []string
}

// Offers reports whether a client of the type offers the operation: one
// its form does not refuse every call of (Refused).
func (n Narrowing) Offers(operation string) bool {
	return !slices.Contains(n.Refused, operation)
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
	"Constants":    narrowConstants,
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
	"Seats":        narrowSeats,
	"Reconcile":    narrowReconcile,
	"Schedules":    narrowSchedules,
	"Pool":         narrowPool,
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
	n := Narrowing{Fields: map[string]FieldShape{}, Params: map[string]any{}, Results: map[string]any{}, Signals: map[string]any{}, Events: map[string]any{}}
	for _, field := range declaration.Fields {
		n.Fields[field.Name] = FieldShape{Schema: map[string]any{"description": field.Description}}
	}
	for _, veto := range declaration.Vetoes {
		n.Vetoes = append(n.Vetoes, veto.Code)
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
	for _, event := range declaration.Events {
		payload, err := decodeSchema(event.PayloadSchema)
		if err != nil {
			return n, fmt.Errorf("event %s payloadSchema: %w", event.Kind, err)
		}
		n.Events[event.Kind] = payload
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
// On a table, the state transition's caller read (from) is one of them too,
// and unchanged keeps its declared boolean (D104); an engine type's
// Workflow refuses from and never answers unchanged, not_principal or
// stale, so its types have none of them.
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
	if target.Instance == nil || target.Instance.IsEngineType() {
		// An engine type's Workflow refuses from and never answers
		// unchanged (D104): its generated clients offer neither.
		if err := deleteProperty(n.Params["transition"], "from"); err != nil {
			return fmt.Errorf("operation transition: %w", err)
		}
		if err := deleteProperty(n.Results["transition"], "unchanged"); err != nil {
			return fmt.Errorf("operation transition: %w", err)
		}
		n.Vetoes = slices.DeleteFunc(n.Vetoes, func(code string) bool { return code == "not_principal" || code == "stale" })
		return nil
	}
	return setOperationProperty(n.Params, "transition", nil, "from", ref(state, ""))
}

// commentsMessageResults are Comments' operations whose result is one
// message's record.
var commentsMessageResults = []string{"post", "begin", "finish", "interrupt", "edit", "deleteMessage", "react", "unreact", "pin", "unpin", "resolve", "reopen", "moveAnchor", "getMessage"}

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
// A block is one of the kinds post's parameters declare (<Base>CommentBlock),
// and a message's record is post's result with its blocks typed as the
// record holds them: <Base>CommentPostedBlock, the same kinds with a code
// block's move members (moveAnchor's original, moves, movedAt, movedBy and
// movedFor), which the parameters' closed code block does not declare.
// <Base>CommentMessage is what every operation that returns a record
// (begin, finish, interrupt and moveAnchor among them) returns and
// listMessages' and listPinned's pages hold; listEdits' items hold posted
// blocks too. A record keeps every kind of block, since narrowing the
// config leaves posted blocks as they are; what post, finish and edit give
// is of the kinds the config's blocks allow, and with none allowed post and
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
	postedBlock := target.Base + "CommentPostedBlock"
	message := target.Base + "CommentMessage"
	n.Aliases = append(n.Aliases, Alias{
		Name:   block,
		Doc:    fmt.Sprintf("A block of a message of %s's Comments: a structured part beside its body, of a kind Comments declares.", An(target.Base)),
		Schema: map[string]any{"oneOf": cloneValue(kinds)},
	})
	postedKinds, err := commentsPostedBlocks(kinds)
	if err != nil {
		return fmt.Errorf("operation post: %w", err)
	}
	n.Aliases = append(n.Aliases, Alias{
		Name:   postedBlock,
		Doc:    fmt.Sprintf("A block of a message of %s's Comments as the record holds it: a code block that moveAnchor moved also holds its first anchor and its last move.", An(target.Base)),
		Schema: map[string]any{"oneOf": postedKinds},
	})
	posted, ok := n.Results["post"].(map[string]any)
	if !ok {
		return fmt.Errorf("operation post has no result schema")
	}
	record := cloneSchema(posted)
	delete(record, "description")
	if err := setItems(record, ref(postedBlock, ""), "properties", "blocks"); err != nil {
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
	if err := setItems(n.Results["listPinned"], ref(message, ""), "properties", "items"); err != nil {
		return fmt.Errorf("operation listPinned: %w", err)
	}
	if err := setItems(n.Results["listEdits"], ref(postedBlock, ""), "properties", "items", "items", "properties", "blocks"); err != nil {
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

// commentsMoveMembers are what moveAnchor adds to a code block, in the
// order it writes them: the anchor the block was given with, how many
// times it moved, and when, by whom and for whom it last moved.
var commentsMoveMembers = map[string]any{
	"original": map[string]any{
		"description":          "The anchor the block was given with, before its first move.",
		"type":                 "object",
		"additionalProperties": false,
		"required":             []any{"revision", "lines"},
		"properties": map[string]any{
			"revision": map[string]any{"type": "string"},
			"lines":    map[string]any{"type": "array", "items": integer()},
		},
	},
	"moves":    map[string]any{"description": "How many times moveAnchor moved it.", "type": "integer"},
	"movedAt":  map[string]any{"description": "When it last moved, in epoch milliseconds.", "type": "integer"},
	"movedBy":  map[string]any{"description": "The subject of the caller who last moved it.", "type": "string"},
	"movedFor": map[string]any{"description": "The subject the caller who last moved it acted for, when it acted for someone.", "type": "string"},
}

// commentsPostedBlocks are the kinds of block a record holds: the kinds
// given, the code kind with moveAnchor's members, each optional. The code
// kind must be declared, so the members cannot drift from the declaration
// unnoticed.
func commentsPostedBlocks(kinds []any) ([]any, error) {
	out := cloneValue(kinds).([]any)
	for _, kind := range out {
		if blockKind(kind) != "code" {
			continue
		}
		properties, err := at(kind, "properties")
		if err != nil {
			return nil, err
		}
		for name, member := range commentsMoveMembers {
			if _, ok := properties[name]; ok {
				return nil, fmt.Errorf("a code block already declares %s", name)
			}
			properties[name] = cloneValue(member)
		}
		return out, nil
	}
	return nil, fmt.Errorf("a block is of no code kind")
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
// operations name a deadline of the config, the enum <Base>Deadline.
func narrowTimers(n *Narrowing, config map[string]any, target Target) error {
	deadlines, _ := config["deadlines"].(map[string]any)
	names := SortedKeys(deadlines)
	deadline := target.Base + "Deadline"
	n.Aliases = append(n.Aliases, Alias{Name: deadline, Doc: fmt.Sprintf("A deadline of %s's Timers.", An(target.Base)), Schema: EnumOf(names)})
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
		if err := setOperationProperty(n.Params, op, nil, "deadline", ref(deadline, "")); err != nil {
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
// a later version removed included. On a table the gated states are the
// alias <Base>Gate, and signOff takes the row's version its signer read,
// optional, which a pinned gate needs (D104); an engine type's Approvals
// refuses version and never answers stale, so its types name the states
// inline and have neither.
func narrowApprovals(n *Narrowing, config map[string]any, target Target) error {
	gates, _ := config["gates"].(map[string]any)
	states := SortedKeys(gates)
	properties := map[string]any{}
	for _, state := range states {
		properties[state] = map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"signedBy"},
			"properties": map[string]any{"signedBy": map[string]any{"type": "array", "items": map[string]any{"type": "string"}}},
		}
	}
	table := target.Instance != nil && !target.Instance.IsEngineType()
	state := func() map[string]any { return EnumOf(states) }
	if table {
		gate := target.Base + "Gate"
		n.Aliases = append(n.Aliases, Alias{Name: gate, Doc: fmt.Sprintf("A state %s's Approvals gates.", An(target.Base)), Schema: EnumOf(states)})
		state = func() map[string]any { return ref(gate, "") }
	}
	for _, op := range []string{"signOff", "withdrawSignOff"} {
		if err := setOperationProperty(n.Params, op, nil, "state", state()); err != nil {
			return err
		}
		if err := setOperationProperty(n.Results, op, nil, "state", state()); err != nil {
			return err
		}
	}
	if !table {
		if err := deleteProperty(n.Params["signOff"], "version"); err != nil {
			return fmt.Errorf("operation signOff: %w", err)
		}
		n.Vetoes = slices.DeleteFunc(n.Vetoes, func(code string) bool { return code == "stale" })
	}
	return setField(n, "gates", map[string]any{"type": "object", "additionalProperties": false, "properties": properties}, true)
}

// Constants: an engine type's Constants refuses oneWay and never answers
// one_way (D104), so its vetoes have none; a table's keeps the
// declaration's.
func narrowConstants(n *Narrowing, _ map[string]any, target Target) error {
	if target.Instance == nil || target.Instance.IsEngineType() {
		n.Vetoes = slices.DeleteFunc(n.Vetoes, func(code string) bool { return code == "one_way" })
	}
	return nil
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
// first; the token, active and the expiries are always there. On a table,
// expireHolder's count of each reason is the alias <Base>ExpireHolderReasons
// (JobLeaseExpireHolderReasons), which the API and the repositories name
// (D104); an engine type's keeps its declaration's shape.
func narrowLease(n *Narrowing, _ map[string]any, target Target) error {
	if target.Instance != nil && !target.Instance.IsEngineType() {
		reasons, err := at(n.Results["expireHolder"], "properties", "reasons")
		if err != nil {
			return fmt.Errorf("operation expireHolder: %w", err)
		}
		name := target.Base + "ExpireHolderReasons"
		alias := cloneSchema(reasons)
		// An object alias's doc is its schema's description, which the
		// types the API and the repositories add carry.
		alias["description"] = fmt.Sprintf("How many leases %s's expireHolder expired for each reason expire gives, by reason; a reason none expired for is absent.", An(target.Base))
		n.Aliases = append(n.Aliases, Alias{Name: name, Doc: alias["description"].(string), Schema: alias})
		if err := setOperationProperty(n.Results, "expireHolder", nil, "reasons", ref(name, "")); err != nil {
			return err
		}
	}
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

// retriesTableFields are the fields of Retries a table keeps and an engine
// type does not (D104): when the row may run again, since when it has
// failed, and the keyField value its counts were made under.
var retriesTableFields = []string{"retryAt", "failingSince", "keyedBy"}

// Retries: the attempts by class hold every class of the config; the best
// score is absent when no scored result is kept. On a table the config's
// failure classes are the alias <Base>Class, which recordAttempt's failure
// names, and the row keeps when it may run again, since when it has failed
// and the keyField value its counts were made under, each absent until set
// (D104); an engine type's Retries keeps none of the three, never answers
// retryAt, backing_off or throttled, and refuses the members that would, so
// its types have none of them.
func narrowRetries(n *Narrowing, config map[string]any, target Target) error {
	classes, _ := config["classes"].(map[string]any)
	names := SortedKeys(classes)
	properties := map[string]any{}
	required := make([]any, 0, len(classes))
	for _, name := range names {
		properties[name] = integer()
		required = append(required, name)
	}
	classAttempts := map[string]any{"type": "object", "additionalProperties": false, "properties": properties, "required": required}
	if err := setFields(n,
		typedField{"total", integer(), true},
		typedField{"classAttempts", classAttempts, true},
		typedField{"bestScore", map[string]any{"type": "number"}, false},
		typedField{"exhausted", boolean(), true},
		typedField{"stuck", boolean(), true},
	); err != nil {
		return err
	}
	if target.Instance == nil || target.Instance.IsEngineType() {
		for _, name := range retriesTableFields {
			if _, ok := n.Fields[name]; !ok {
				return fmt.Errorf("the declaration has no field %s", name)
			}
			delete(n.Fields, name)
		}
		if err := deleteProperty(n.Results["recordAttempt"], "retryAt"); err != nil {
			return fmt.Errorf("operation recordAttempt: %w", err)
		}
		n.Vetoes = slices.DeleteFunc(n.Vetoes, func(code string) bool { return code == "backing_off" || code == "throttled" })
		return nil
	}
	class := target.Base + "Class"
	n.Aliases = append(n.Aliases, Alias{Name: class, Doc: fmt.Sprintf("A failure class of %s's Retries.", An(target.Base)), Schema: EnumOf(names)})
	if err := setOperationProperty(n.Params, "recordAttempt", nil, "failure", ref(class, "")); err != nil {
		return err
	}
	return setFields(n,
		typedField{"retryAt", integer(), false},
		typedField{"failingSince", integer(), false},
		typedField{"keyedBy", map[string]any{"type": "string"}, false},
	)
}

// Seats (D104): the config's seats are the alias <Base>Seat, which every
// operation's seat names, takeSeat's, leaveSeat's and listSeats' items'
// results among them; the permissions its seats name, take and assign,
// are its Permissions. Seats keeps no field: its occupants are a
// sub-table, which listSeats reads.
func narrowSeats(n *Narrowing, config map[string]any, target Target) error {
	seats, _ := config["seats"].(map[string]any)
	names := SortedKeys(seats)
	seat := target.Base + "Seat"
	n.Aliases = append(n.Aliases, Alias{Name: seat, Doc: fmt.Sprintf("A seat of %s's Seats.", An(target.Base)), Schema: EnumOf(names)})
	for _, op := range []string{"takeSeat", "leaveSeat", "listSeats"} {
		if err := setOperationProperty(n.Params, op, nil, "seat", ref(seat, "")); err != nil {
			return err
		}
	}
	for _, op := range []string{"takeSeat", "leaveSeat"} {
		if err := setOperationProperty(n.Results, op, nil, "seat", ref(seat, "")); err != nil {
			return err
		}
	}
	if err := setOperationProperty(n.Results, "listSeats", []string{"properties", "items", "items"}, "seat", ref(seat, "")); err != nil {
		return err
	}
	for _, name := range names {
		each, _ := seats[name].(map[string]any)
		for _, member := range []string{"take", "assign"} {
			if permission, _ := each[member].(string); permission != "" && !slices.Contains(n.Permissions, permission) {
				n.Permissions = append(n.Permissions, permission)
			}
		}
	}
	slices.Sort(n.Permissions)
	return nil
}

// Reconcile (D104): the states of the Workflow its status names, among the
// table's uses, are the alias <Base>State, which the observed field names,
// absent before the first observation, as do observe's state, its result's
// observed, each move's from and to, and its drift's status and observed;
// the result's status names it or null, for a row with no status. When an
// observation was made and when the status last agreed with it are epoch
// milliseconds, each absent until set, and drift is absent while the
// status is the observed state.
func narrowReconcile(n *Narrowing, config map[string]any, target Target) error {
	status, _ := config["status"].(string)
	states, err := reconcileStates(target, status)
	if err != nil {
		return err
	}
	state := target.Base + "State"
	// The doc names the Reconcile by its base, which behaviorops reads as
	// the field (Sandbox's observed), and the Workflow by its field.
	workflow := target.Instance.Name + "." + status
	if status == "Workflow" {
		workflow = target.Instance.Name + "'s Workflow"
	}
	n.Aliases = append(n.Aliases, Alias{Name: state, Doc: fmt.Sprintf("A state %s's Reconcile reports, one of the states of %s.", An(target.Base), workflow), Schema: EnumOf(states)})
	drift := func() map[string]any {
		return map[string]any{
			"type": "object", "additionalProperties": false, "required": []any{"observed", "code"},
			"properties": map[string]any{"status": ref(state, ""), "observed": ref(state, ""), "code": map[string]any{"type": "string"}},
		}
	}
	if err := setFields(n,
		typedField{"observed", ref(state, ""), false},
		typedField{"observedAt", integer(), false},
		typedField{"drift", drift(), false},
		typedField{"reconciledAt", integer(), false},
	); err != nil {
		return err
	}
	if err := setOperationProperty(n.Params, "observe", nil, "state", ref(state, "")); err != nil {
		return err
	}
	nullable := ref(state, "")
	nullable["nullable"] = true
	for name, replacement := range map[string]map[string]any{"observed": ref(state, ""), "status": nullable} {
		if err := setOperationProperty(n.Results, "observe", nil, name, replacement); err != nil {
			return err
		}
	}
	for _, name := range []string{"from", "to"} {
		if err := setOperationProperty(n.Results, "observe", []string{"properties", "moves", "items"}, name, ref(state, "")); err != nil {
			return err
		}
	}
	for _, name := range []string{"status", "observed"} {
		if err := setOperationProperty(n.Results, "observe", []string{"properties", "drift"}, name, ref(state, "")); err != nil {
			return err
		}
	}
	return nil
}

// The states a schedule is in and what finishes one (D104), in the order
// the declaration's enums list them.
var (
	scheduleStates  = []string{"active", "paused", "finished"}
	scheduleEndings = []string{"run", "cancel", "endOn"}
)

// The kinds a schedule is of (D104), in the order the declaration's enum
// lists them.
var scheduleKinds = []string{"after", "every", "cron"}

// Schedules (D104): the config's outcomes are the alias <Base>Outcome,
// which reportRun's input names, and the kinds of schedule, a schedule's
// states and what finished it are <Base>Kind, <Base>State and
// <Base>EndedBy, which every view result names (set, pause, resume and
// cancel's, and listSchedules' items'), as the event run's kind does.
// Only inputs narrow to the config: setSchedule takes no after, every or
// cron for a kind the config leaves out, and no timeZone when cron is not
// among its kinds, and reportRun takes an outcome of outcomes. An output
// keeps the declaration's values, every kind and any outcome (lastOutcome
// and reportRun's result's outcome are strings), since a schedule of a
// kind a new config dropped keeps running and an outcome it dropped stays
// recorded (D104's configChange), and a reader's closed enum would refuse
// them. Schedules keeps no field: its schedules are a sub-table, which
// listSchedules reads.
func narrowSchedules(n *Narrowing, config map[string]any, target Target) error {
	kinds, outcomes := stringsOf(config["kinds"]), stringsOf(config["outcomes"])
	kind, outcome, state, endedBy := target.Base+"Kind", target.Base+"Outcome", target.Base+"State", target.Base+"EndedBy"
	owner := An(target.Base) + "'s Schedules"
	n.Aliases = append(n.Aliases,
		Alias{Name: kind, Doc: fmt.Sprintf("The kind of a schedule of %s: after, every or cron, whichever its config took when the schedule was set.", owner), Schema: EnumOf(scheduleKinds)},
		Alias{Name: outcome, Doc: fmt.Sprintf("An outcome %s records of a run.", owner), Schema: EnumOf(outcomes)},
		Alias{Name: state, Doc: fmt.Sprintf("The state of a schedule of %s.", owner), Schema: EnumOf(scheduleStates)},
		Alias{Name: endedBy, Doc: fmt.Sprintf("What finished a schedule of %s.", owner), Schema: EnumOf(scheduleEndings)},
	)
	params, err := at(n.Params["setSchedule"], "properties")
	if err != nil {
		return fmt.Errorf("operation setSchedule: %w", err)
	}
	for _, each := range []string{"after", "every", "cron"} {
		if _, ok := params[each]; !ok {
			return fmt.Errorf("operation setSchedule: the declaration's schema has no property %s", each)
		}
		if !slices.Contains(kinds, each) {
			delete(params, each)
		}
	}
	if !slices.Contains(kinds, "cron") {
		if err := deleteProperty(n.Params["setSchedule"], "timeZone"); err != nil {
			return fmt.Errorf("operation setSchedule: %w", err)
		}
	}
	if err := setOperationProperty(n.Params, "reportRun", nil, "outcome", ref(outcome, "")); err != nil {
		return err
	}
	view := map[string]string{"kind": kind, "state": state, "endedBy": endedBy}
	for _, op := range []string{"setSchedule", "pauseSchedule", "resumeSchedule", "cancelSchedule", "listSchedules"} {
		var path []string
		if op == "listSchedules" {
			path = []string{"properties", "items", "items"}
		}
		for _, member := range SortedKeys(view) {
			if err := setOperationProperty(n.Results, op, path, member, ref(view[member], "")); err != nil {
				return err
			}
		}
	}
	payload, ok := n.Events["run"].(map[string]any)
	if !ok {
		return fmt.Errorf("the declaration has no event run")
	}
	if err := setProperty(payload, "kind", ref(kind, "")); err != nil {
		return fmt.Errorf("event run: %w", err)
	}
	return nil
}

// poolAvailabilities are a Pool member's availabilities (D104): open, near
// its limit, or out.
var poolAvailabilities = []string{"open", "near", "out"}

// poolMatchTypes are the IR types of an own field pick's match and prefer
// compare (D104), by the JSON type a value of each holds: a string, a
// boolean, a number, an integer and a UUID or a subject, as its wire form
// gives it. An enum of the schema is a string.
var poolMatchTypes = map[string]string{
	"string": "string", "boolean": "boolean", "number": "number", "Generic.Int64": "integer", "Generic.Float64": "number",
	"Identity.UUID": "string", "Identity.UserID": "string",
}

// Pool (D104): a member's availability is the alias <Base>Availability
// (AccountPoolAvailability), which the field, markOut's and reportRoom's
// results and pick's member name; outUntil and checkedAt are epoch
// milliseconds, and every field is absent on a row from before the use.
// On a table, pick's match and prefer are the alias <Base>Match
// (AccountPoolMatch), an optional member per own field of the table the
// core compares (behaviorlayout.SpecFields): a scalar of poolMatchTypes or
// an enum of the schema, typed by the JSON value it holds, and not a list,
// a map, JSON, a date-time or a field encrypted at rest. A member takes no
// null: the core reads a null one as absent, as D12 has every optional
// value and as every server and SDK but TypeScript's would send it; pick's
// member is the alias <Base>PickMember
// (AccountPoolPickMember), or null, whose order is an integer when the
// config's order field is a Generic.Int64 and a number otherwise.
func narrowPool(n *Narrowing, config map[string]any, target Target) error {
	availability := target.Base + "Availability"
	on := target.Base
	if target.Instance != nil {
		on = target.Instance.Name
	}
	n.Aliases = append(n.Aliases, Alias{Name: availability, Doc: fmt.Sprintf("A Pool member's availability on %s.", on), Schema: EnumOf(poolAvailabilities)})
	if err := setFields(n,
		typedField{"availability", ref(availability, ""), false},
		typedField{"outUntil", integer(), false},
		typedField{"outReason", map[string]any{"type": "string"}, false},
		typedField{"checkedAt", integer(), false},
	); err != nil {
		return err
	}
	for _, op := range []string{"markOut", "reportRoom"} {
		if err := setOperationProperty(n.Results, op, nil, "availability", ref(availability, "")); err != nil {
			return err
		}
	}
	if target.Instance == nil || target.Instance.IsEngineType() {
		// No engine type composes Pool: the build refuses it there.
		return nil
	}
	match := target.Base + "Match"
	properties := map[string]any{}
	for _, column := range behaviorlayout.SpecFields(target.Document, target.Instance) {
		fd := OwnField(target.Instance, column.Field)
		if fd != nil && (fd.TypeRef.IsArray || fd.TypeRef.IsMap || fd.EncryptedAtRest) {
			continue
		}
		kind, ok := poolMatchTypes[column.Type]
		if !ok && target.Document != nil && target.Document.Enums[column.Type] != nil {
			kind, ok = "string", true
		}
		if !ok {
			continue
		}
		description := fmt.Sprintf("The value %s's %s holds.", on, column.Field)
		properties[column.Field] = map[string]any{"description": description, "type": kind}
	}
	// An object alias's doc is its schema's description, which the types
	// the API and the repositories add carry.
	matchDoc := fmt.Sprintf("The members of %s's Pool a pick keeps, or tries first, by the values own fields of %s hold; every member when absent.", on, on)
	n.Aliases = append(n.Aliases, Alias{Name: match, Doc: matchDoc, Schema: map[string]any{
		"description": matchDoc, "type": "object", "additionalProperties": false, "properties": properties,
	}})
	for _, member := range []string{"match", "prefer"} {
		if err := setOperationProperty(n.Params, "pick", nil, member, ref(match, "")); err != nil {
			return err
		}
	}
	declared, err := at(n.Results["pick"], "properties", "member")
	if err != nil {
		return fmt.Errorf("operation pick: %w", err)
	}
	picked := target.Base + "PickMember"
	member := cloneSchema(declared)
	member["description"] = fmt.Sprintf("The member of %s's Pool a pick picked: its key in its wire form, its availability, its value of the order field and whether prefer keeps it.", on)
	member["type"] = "object"
	order := "number"
	if name, _ := config["order"].(string); name != "" {
		if fd := OwnField(target.Instance, name); fd != nil && fd.TypeRef.Name == "Generic.Int64" {
			order = "integer"
		}
	}
	if err := setProperty(member, "availability", ref(availability, "")); err != nil {
		return fmt.Errorf("operation pick: member: %w", err)
	}
	if err := setProperty(member, "order", map[string]any{"type": []any{order, "null"}}); err != nil {
		return fmt.Errorf("operation pick: member: %w", err)
	}
	n.Aliases = append(n.Aliases, Alias{Name: picked, Doc: member["description"].(string), Schema: member})
	node := ref(picked, "")
	node["nullable"] = true
	return setOperationProperty(n.Results, "pick", nil, "member", node)
}

// reconcileStates are the states of the Workflow use of the target's table
// whose id is status, as its config lists them.
func reconcileStates(target Target, status string) ([]string, error) {
	if target.Instance == nil {
		return nil, fmt.Errorf("status %q names the Workflow of a table, and the target is none", status)
	}
	for _, use := range target.Instance.TableBehaviors() {
		if use.Name != "Workflow" || use.UseID() != status {
			continue
		}
		var config struct {
			States []string `json:"states"`
		}
		if err := json.Unmarshal(use.Config, &config); err != nil {
			return nil, fmt.Errorf("table %s: the Workflow on field %s, which status names: %w", target.Instance.Name, status, err)
		}
		return config.States, nil
	}
	return nil, fmt.Errorf("table %s: status %q is not a field of %s that is a Workflow", target.Instance.Name, status, target.Instance.Name)
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

// budgetMeterOperations are Budget's operations whose parameters name a
// meter, and budgetMeterResults those whose results do.
var (
	budgetMeterOperations = []string{"reserve", "checkReserve", "recordUsage", "settle", "setLimit", "observe", "reserveFor", "settleFor", "recordUsageFor"}
	budgetMeterResults    = []string{"recordUsage", "setLimit", "observe"}
)

// Budget: the meters field holds every meter of the config, by name; a
// meter with no limit holds null for its limit and what remains of it. On
// a table the config's meters are the alias <Base>Meter, which every
// operation's meter names, and recordUsage's, setLimit's and observe's
// results; an engine type's Budget refuses every observe (D104), which its
// clients therefore leave out (Refused).
func narrowBudget(n *Narrowing, config map[string]any, target Target) error {
	meters, _ := config["meters"].(map[string]any)
	if target.Instance == nil || target.Instance.IsEngineType() {
		n.Refused = append(n.Refused, "observe")
	} else {
		meter := target.Base + "Meter"
		n.Aliases = append(n.Aliases, Alias{Name: meter, Doc: fmt.Sprintf("A meter of %s's Budget.", An(target.Base)), Schema: EnumOf(SortedKeys(meters))})
		for _, op := range budgetMeterOperations {
			if err := setOperationProperty(n.Params, op, nil, "meter", ref(meter, "")); err != nil {
				return err
			}
		}
		for _, op := range budgetMeterResults {
			if err := setOperationProperty(n.Results, op, nil, "meter", ref(meter, "")); err != nil {
				return err
			}
		}
	}
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
			"for":        map[string]any{"type": "string"},
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
