package behaviortypes

import (
	"encoding/json"
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

// Comments' blocks config narrows what post and edit give: every declared
// kind when absent (the block alias), the kinds it names, and when it
// names none, no blocks for post and an empty array for edit, which
// clears the blocks a message was posted with before. A record's blocks
// keep every kind.
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
		for _, op := range []string{"post", "edit"} {
			items := given(n, op)
			switch {
			case test.want == nil:
				if node, _ := items.(map[string]any); node[RefKey] != "NoteCommentBlock" {
					t.Errorf("%s: %s gives %v, want every kind", test.config, op, items)
				}
			case len(test.want) == 0 && op == "post":
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
		if n.Results["getMessage"].(map[string]any)[RefKey] != "NoteCommentMessage" {
			t.Errorf("%s: getMessage returns %v", test.config, n.Results["getMessage"])
		}
	}
}
