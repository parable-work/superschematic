package behaviortypes

import (
	"encoding/json"
	"maps"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// Each core narrowing types only fields its behavior declares, so a field a
// declaration renames fails here, whether or not a golden composes the
// behavior. Variants is left out: its config names the type's own fields,
// and it declares none.
func TestCoreNarrowingsTypeDeclaredFields(t *testing.T) {
	reg := registry.New(naming.Default())
	document := ir.NewSchema("items", ir.SchemaKindGeneral)
	target := Target{Schema: "items", Base: "Item", Document: document, Instance: &ir.TypeDef{Name: "Item"}}
	for _, name := range NarrowerNames() {
		if name == "Variants" {
			continue
		}
		declaration, ok := reg.Behavior(name)
		if !ok {
			t.Errorf("%s has a narrowing and no declaration", name)
			continue
		}
		if _, err := Narrow(declaration, nil, target); err != nil {
			t.Errorf("%s: %v", name, err)
		}
	}
}

// Comments' blocks config narrows what post, finish and edit give: every
// declared kind (ref, action, code and attachment) when absent or all named
// (the block alias), the kinds it names, and when it names none, no blocks
// for post and finish and an empty array for edit, which clears the blocks
// a message was posted with before. A record's blocks keep every kind, as
// posted blocks, and so do listEdits' items.
func TestCommentsNarrowsTheBlocksPostAndEditGive(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Comments")
	target := Target{Schema: "notes", Base: "Note", Document: ir.NewSchema("notes", ir.SchemaKindGeneral), Instance: &ir.TypeDef{Name: "Note"}}
	given := func(n Narrowing, op string) any {
		blocks, err := at(n.Params[op], "properties", "blocks")
		if err != nil {
			return nil
		}
		return blocks["items"]
	}
	for _, test := range []struct {
		config string
		want   []string
	}{
		{``, nil},
		{`{"blocks": ["ref", "action", "code", "attachment"]}`, nil},
		{`{"blocks": ["attachment", "code", "action", "ref"]}`, nil},
		{`{"blocks": ["ref", "action"]}`, []string{"ref", "action"}},
		{`{"blocks": ["attachment", "code"]}`, []string{"code", "attachment"}},
		{`{"blocks": ["action"]}`, []string{"action"}},
		{`{"blocks": []}`, []string{}},
	} {
		n, err := Narrow(declaration, json.RawMessage(test.config), target)
		if err != nil {
			t.Fatalf("%s: %v", test.config, err)
		}
		if len(n.Aliases) != 3 || n.Aliases[0].Name != "NoteCommentBlock" || n.Aliases[1].Name != "NoteCommentPostedBlock" || n.Aliases[2].Name != "NoteCommentMessage" {
			t.Fatalf("%s: aliases %v", test.config, n.Aliases)
		}
		var kinds []string
		for _, kind := range n.Aliases[0].Schema.(map[string]any)["oneOf"].([]any) {
			kinds = append(kinds, blockKind(kind))
		}
		if !slices.Equal(kinds, []string{"ref", "action", "code", "attachment"}) {
			t.Errorf("%s: NoteCommentBlock's kinds are %v", test.config, kinds)
		}
		if items, _ := at(n.Aliases[2].Schema, "properties", "blocks", "items"); items[RefKey] != "NoteCommentPostedBlock" {
			t.Errorf("%s: a record's blocks are %v", test.config, items)
		}
		if items, _ := at(n.Results["listEdits"], "properties", "items", "items", "properties", "blocks", "items"); items[RefKey] != "NoteCommentPostedBlock" {
			t.Errorf("%s: an edit's blocks are %v", test.config, items)
		}
		if items, _ := at(n.Results["listPinned"], "properties", "items", "items"); items[RefKey] != "NoteCommentMessage" {
			t.Errorf("%s: listPinned's items are %v", test.config, items)
		}
		for _, op := range commentsBlocksGiven {
			items := given(n, op)
			switch {
			case test.want == nil:
				if node, _ := items.(map[string]any); node[RefKey] != "NoteCommentBlock" {
					t.Errorf("%s: %s gives %v, want every kind", test.config, op, items)
				}
			case len(test.want) == 0 && op != "edit":
				if items != nil {
					t.Errorf("%s: %s gives %v, want no blocks", test.config, op, items)
				}
			case len(test.want) == 0:
				blocks, err := at(n.Params[op], "properties", "blocks")
				if err != nil || blocks["items"] != false || blocks["maxItems"] != json.Number("0") {
					t.Errorf("%s: %s gives blocks %v (%v), want only the empty array", test.config, op, blocks, err)
				}
				var r TSRenderer
				if got := r.Render(blocks, ""); got != "never[]" {
					t.Errorf("%s: %s's blocks render as %s, want never[]", test.config, op, got)
				}
			default:
				node, _ := items.(map[string]any)
				kinds, _ := node["oneOf"].([]any)
				var got []string
				for _, kind := range kinds {
					got = append(got, blockKind(kind))
				}
				if !slices.Equal(got, test.want) {
					t.Errorf("%s: %s gives the kinds %v, want %v", test.config, op, got, test.want)
				}
			}
		}
		for _, op := range []string{"getMessage", "begin", "finish", "interrupt", "moveAnchor"} {
			if n.Results[op].(map[string]any)[RefKey] != "NoteCommentMessage" {
				t.Errorf("%s: %s returns %v", test.config, op, n.Results[op])
			}
		}
	}
}

// Comments' composing field is always there, an array of subjects, and its
// signals' data are their declared paramsSchemas, which no config narrows.
func TestCommentsComposingAndSignals(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Comments")
	target := Target{Schema: "notes", Base: "Note", Document: ir.NewSchema("notes", ir.SchemaKindGeneral), Instance: &ir.TypeDef{Name: "Note"}}
	n, err := Narrow(declaration, nil, target)
	if err != nil {
		t.Fatal(err)
	}
	composing := n.Fields["composing"]
	var r TSRenderer
	if got := r.Render(WithoutDescription(composing.Schema), ""); !composing.Present || got != "string[]" {
		t.Errorf("composing is %s (present %v), want string[] on every read", got, composing.Present)
	}
	if got := slices.Sorted(maps.Keys(n.Signals)); !slices.Equal(got, []string{"draft", "typing", "viewing"}) {
		t.Fatalf("signals %v", got)
	}
	if required, _ := n.Signals["draft"].(map[string]any)["required"].([]any); len(required) != 3 {
		t.Errorf("draft's data requires %v", required)
	}
}

// Comments' unread field is the reader's count, always there (D77, step 3);
// a message's stand and listMessages' readUpTo are optional, as declared.
func TestCommentsReadState(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Comments")
	target := Target{Schema: "notes", Base: "Note", Document: ir.NewSchema("notes", ir.SchemaKindGeneral), Instance: &ir.TypeDef{Name: "Note"}}
	n, err := Narrow(declaration, nil, target)
	if err != nil {
		t.Fatal(err)
	}
	unread := n.Fields["unread"]
	var r TSRenderer
	if got := r.Render(WithoutDescription(unread.Schema), ""); !unread.Present || got != "number" {
		t.Errorf("unread is %s (present %v), want a number on every read", got, unread.Present)
	}
	var message map[string]any
	for _, alias := range n.Aliases {
		if alias.Name == "NoteCommentMessage" {
			message, _ = alias.Schema.(map[string]any)
		}
	}
	if properties, _ := message["properties"].(map[string]any); properties["stand"] == nil {
		t.Errorf("NoteCommentMessage has no stand: %v", message)
	}
	if required, _ := message["required"].([]any); slices.Contains(required, any("stand")) {
		t.Error("a message's stand is required; a composing message has none")
	}
	if _, err := at(n.Results["listMessages"], "properties", "readUpTo"); err != nil {
		t.Errorf("listMessages: %v", err)
	}
}

// A record's code block holds moveAnchor's members, each optional, which a
// code block given to post does not: <Base>CommentPostedBlock is the
// block alias's kinds, the code kind extended. A record names its for, and
// its resolve's, as declared.
func TestCommentsPostedBlocksAndFor(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Comments")
	target := Target{Schema: "notes", Base: "Note", Document: ir.NewSchema("notes", ir.SchemaKindGeneral), Instance: &ir.TypeDef{Name: "Note"}}
	n, err := Narrow(declaration, nil, target)
	if err != nil {
		t.Fatal(err)
	}
	code := func(alias Alias) map[string]any {
		for _, kind := range alias.Schema.(map[string]any)["oneOf"].([]any) {
			if blockKind(kind) == "code" {
				properties, _ := at(kind, "properties")
				return properties
			}
		}
		t.Fatalf("%s has no code kind", alias.Name)
		return nil
	}
	given, posted := code(n.Aliases[0]), code(n.Aliases[1])
	for _, member := range []string{"original", "moves", "movedAt", "movedBy", "movedFor"} {
		if given[member] != nil {
			t.Errorf("a code block given holds %s", member)
		}
		if posted[member] == nil {
			t.Errorf("a posted code block lacks %s", member)
		}
	}
	var r TSRenderer
	if got := r.Render(WithoutDescription(posted["original"]), ""); got != "{ lines: number[]; revision: string }" {
		t.Errorf("original renders as %s", got)
	}
	message, _ := n.Aliases[2].Schema.(map[string]any)
	properties, _ := message["properties"].(map[string]any)
	if properties["for"] == nil {
		t.Error("NoteCommentMessage has no for")
	}
	if required, _ := message["required"].([]any); slices.Contains(required, any("for")) {
		t.Error("a message's for is required; one written for nobody has none")
	}
	if _, err := at(message, "properties", "resolved", "properties", "for"); err != nil {
		t.Errorf("resolved: %v", err)
	}
}

// A table's Workflow types the state its transition's caller read (from)
// as the config's states, as it types to, and keeps unchanged, the
// result's boolean, as declared (D104).
func TestNarrowWorkflowTypesFromAsAState(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Workflow")
	table := &ir.TypeDef{Name: "Question", Behaviors: []ir.BehaviorRef{{Name: "Workflow", Field: "status"}}}
	target := Target{Schema: "questions", Base: "QuestionStatus", Document: ir.NewSchema("questions", ir.SchemaKindDB), Instance: table}
	n, err := Narrow(declaration, json.RawMessage(`{"states": ["open", "answered"], "transitions": [{"from": "open", "to": "answered", "by": ["askedOf"]}], "repeat": "unchanged"}`), target)
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"to", "from"} {
		property, err := at(n.Params["transition"], "properties", name)
		if err != nil || property[RefKey] != "QuestionStatusState" {
			t.Errorf("transition's %s = %v (%v), want QuestionStatusState", name, property, err)
		}
	}
	if required, _ := n.Params["transition"].(map[string]any)["required"].([]any); len(required) != 1 || required[0] != "to" {
		t.Errorf("transition's required = %v, want to alone", required)
	}
	from, _ := at(n.Params["transition"], "properties", "from")
	if description, _ := from["description"].(string); description == "" {
		t.Errorf("transition's from has lost its description: %v", from)
	}
	unchanged, err := at(n.Results["transition"], "properties", "unchanged")
	if err != nil || unchanged["type"] != "boolean" {
		t.Errorf("transition's result unchanged = %v (%v), want the declared boolean", unchanged, err)
	}
	if !slices.Contains(n.Vetoes, "not_principal") || !slices.Contains(n.Vetoes, "stale") {
		t.Errorf("a table's Workflow vetoes = %v, want not_principal and stale among them", n.Vetoes)
	}
}

// An engine type's Workflow refuses from and never answers unchanged,
// not_principal or stale (D104), so its narrowing has none of them, and its
// generated clients offer none.
func TestNarrowWorkflowOfAnEngineTypeHasNoFromOrUnchanged(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Workflow")
	engine := &ir.TypeDef{Name: "Order", Behaviors: []ir.BehaviorRef{{Name: "Workflow"}}}
	for _, instance := range []*ir.TypeDef{engine, nil} {
		target := Target{Schema: "orders", Base: "Order", Document: ir.NewSchema("orders", ir.SchemaKindGeneral), Instance: instance}
		n, err := Narrow(declaration, json.RawMessage(`{"states": ["draft", "paid"], "transitions": [{"from": "draft", "to": "paid"}]}`), target)
		if err != nil {
			t.Fatal(err)
		}
		params, _ := at(n.Params["transition"], "properties")
		results, _ := at(n.Results["transition"], "properties")
		if got := slices.Sorted(maps.Keys(params)); !slices.Equal(got, []string{"to"}) {
			t.Errorf("instance %v: transition's parameters = %v, want to alone", instance, got)
		}
		if got := slices.Sorted(maps.Keys(results)); !slices.Equal(got, []string{"from", "to"}) {
			t.Errorf("instance %v: transition's result = %v, want from and to", instance, got)
		}
		if want := []string{"already_in_state", "terminal_state", "transition_not_allowed", "no_status"}; !slices.Equal(n.Vetoes, want) {
			t.Errorf("instance %v: vetoes = %v, want %v", instance, n.Vetoes, want)
		}
	}
	// Narrow leaves the declaration as it was.
	again := Target{Schema: "orders", Base: "Order", Document: ir.NewSchema("orders", ir.SchemaKindGeneral), Instance: &ir.TypeDef{Name: "Order", Behaviors: []ir.BehaviorRef{{Name: "Workflow", Field: "status"}}}}
	n, err := Narrow(declaration, json.RawMessage(`{"states": ["draft"], "transitions": []}`), again)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := at(n.Params["transition"], "properties", "from"); err != nil {
		t.Errorf("a table's transition after an engine type's has no from: %v", err)
	}
}

// A table's Approvals types the gated states as the alias <Base>Gate,
// which signOff's and withdrawSignOff's state and their results' refer to,
// and keeps signOff's version, optional, and the veto stale (D104).
func TestNarrowApprovalsOfATableTypesItsGatesAndVersion(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Approvals")
	table := &ir.TypeDef{Name: "Task", Behaviors: []ir.BehaviorRef{{Name: "Approvals", Implements: true}, {Name: "Workflow", Field: "status"}}}
	target := Target{Schema: "tasks", Base: "TaskApprovals", Document: ir.NewSchema("tasks", ir.SchemaKindDB), Instance: table}
	n, err := Narrow(declaration, json.RawMessage(`{"gates": {"proving": {"count": 1, "permission": "tasks.review", "resetOn": []}, "done": {"count": 1, "permission": "tasks.review", "pinned": true}}}`), target)
	if err != nil {
		t.Fatal(err)
	}
	if len(n.Aliases) != 1 || n.Aliases[0].Name != "TaskApprovalsGate" || n.Aliases[0].Doc != "A state a TaskApprovals's Approvals gates." {
		t.Fatalf("aliases = %+v, want TaskApprovalsGate", n.Aliases)
	}
	if values, _ := n.Aliases[0].Schema.(map[string]any)["enum"].([]any); len(values) != 2 || values[0] != "done" || values[1] != "proving" {
		t.Errorf("TaskApprovalsGate = %v, want done and proving", n.Aliases[0].Schema)
	}
	for _, op := range []string{"signOff", "withdrawSignOff"} {
		for kind, schema := range map[string]any{"parameters": n.Params[op], "result": n.Results[op]} {
			if state, err := at(schema, "properties", "state"); err != nil || state[RefKey] != "TaskApprovalsGate" {
				t.Errorf("%s's %s state = %v (%v), want TaskApprovalsGate", op, kind, state, err)
			}
		}
	}
	version, err := at(n.Params["signOff"], "properties", "version")
	if err != nil || version["type"] != "integer" || version["minimum"] != float64(1) {
		t.Errorf("signOff's version = %v (%v), want the declared integer", version, err)
	}
	if required, _ := n.Params["signOff"].(map[string]any)["required"].([]any); len(required) != 1 || required[0] != "state" {
		t.Errorf("signOff's required = %v, want state alone", required)
	}
	if !slices.Contains(n.Vetoes, "stale") {
		t.Errorf("a table's Approvals vetoes = %v, want stale among them", n.Vetoes)
	}
}

// An engine type's Approvals refuses signOff's version and never answers
// stale (D104): its narrowing names the gated states inline, as before,
// and has neither, so its generated clients do not change.
func TestNarrowApprovalsOfAnEngineTypeHasNoVersion(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Approvals")
	engine := &ir.TypeDef{Name: "Expense", Behaviors: []ir.BehaviorRef{{Name: "Approvals"}}}
	for _, instance := range []*ir.TypeDef{engine, nil} {
		target := Target{Schema: "expenses", Base: "Expense", Document: ir.NewSchema("expenses", ir.SchemaKindGeneral), Instance: instance}
		n, err := Narrow(declaration, json.RawMessage(`{"gates": {"approved": {"count": 2, "permission": "expenses.approve"}}}`), target)
		if err != nil {
			t.Fatal(err)
		}
		if len(n.Aliases) != 0 {
			t.Errorf("instance %v: aliases = %+v, want none", instance, n.Aliases)
		}
		params, _ := at(n.Params["signOff"], "properties")
		if got := slices.Sorted(maps.Keys(params)); !slices.Equal(got, []string{"note", "state"}) {
			t.Errorf("instance %v: signOff's parameters = %v, want note and state", instance, got)
		}
		if state, _ := at(n.Params["signOff"], "properties", "state"); state["enum"] == nil {
			t.Errorf("instance %v: signOff's state = %v, want the inline enum", instance, state)
		}
		if want := []string{"approvals_missing", "already_signed", "creator_cannot_sign", "in_state", "no_gate", "not_signed"}; !slices.Equal(n.Vetoes, want) {
			t.Errorf("instance %v: vetoes = %v, want %v", instance, n.Vetoes, want)
		}
	}
}

// An engine type's Constants refuses oneWay and never answers one_way
// (D104), so its vetoes have none; a table's keeps it. Constants has no
// operation and no field to narrow.
func TestNarrowConstantsDropsOneWayForAnEngineType(t *testing.T) {
	reg := registry.New(naming.Default())
	declaration, _ := reg.Behavior("Constants")
	config := json.RawMessage(`{"fields": ["owner"]}`)
	for _, tc := range []struct {
		name     string
		instance *ir.TypeDef
		want     []string
	}{
		{"a table", &ir.TypeDef{Name: "Task", Behaviors: []ir.BehaviorRef{{Name: "Constants", Implements: true}}}, []string{"one_way"}},
		{"an engine type", &ir.TypeDef{Name: "Step", Behaviors: []ir.BehaviorRef{{Name: "Constants"}}}, nil},
		{"no instance", nil, nil},
	} {
		n, err := Narrow(declaration, config, Target{Schema: "tasks", Base: "Task", Document: ir.NewSchema("tasks", ir.SchemaKindDB), Instance: tc.instance})
		if err != nil {
			t.Fatal(err)
		}
		if !slices.Equal(n.Vetoes, tc.want) {
			t.Errorf("%s: vetoes = %v, want %v", tc.name, n.Vetoes, tc.want)
		}
	}
}
