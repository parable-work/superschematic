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
// declared kind when absent (the block alias), the kinds it names, and
// when it names none, no blocks for post and finish and an empty array for
// edit, which clears the blocks a message was posted with before. A
// record's blocks keep every kind.
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
		{`{"blocks": ["ref", "action"]}`, nil},
		{`{"blocks": ["action"]}`, []string{"action"}},
		{`{"blocks": []}`, []string{}},
	} {
		n, err := Narrow(declaration, json.RawMessage(test.config), target)
		if err != nil {
			t.Fatalf("%s: %v", test.config, err)
		}
		if len(n.Aliases) != 2 || n.Aliases[0].Name != "NoteCommentBlock" || n.Aliases[1].Name != "NoteCommentMessage" {
			t.Fatalf("%s: aliases %v", test.config, n.Aliases)
		}
		if items, _ := at(n.Aliases[1].Schema, "properties", "blocks", "items"); items[RefKey] != "NoteCommentBlock" {
			t.Errorf("%s: a record's blocks are %v", test.config, items)
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
		for _, op := range []string{"getMessage", "begin", "finish", "interrupt"} {
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
