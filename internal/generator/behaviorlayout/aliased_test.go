package behaviorlayout_test

import (
	"encoding/json"
	"testing"
)

// aliasedFixture is the DB service whose Company and Tag carry Aliased on
// a field (D147).
const aliasedFixture = "../../loader/tsreader/testdata/services/fixture-aliased-db"

// The layout document of tables whose fields carry Aliased (D147):
// Company's one use is its name field's, the name on the field's own
// column, its value; its operations rename, addAlias and removeAlias of a
// row and resolve of the table, which answers a row of it under row
// (answersRow), which an adapter keeps to its caller (D156); its aliases a
// sub-table of the row keyed by
// the normalized form; and its names a shared table of the use, kept for
// the table as a whole, keyed by the normalized form, whose holder column
// holds the row's key as the key's type, Identity.UUID, so an adapter
// writes it as it writes a key. Aliased keeps no queue.
// testdata/fixture-aliased-db.layout.json is the document the adapters of
// every language are written against.
func TestLayoutOfAliased(t *testing.T) {
	layout := layoutOfFixture(t, aliasedFixture, "fixture-aliased-db")
	td := layout.Types["Company"]
	if td == nil || len(td.Uses) != 1 || td.Uses[0].ID != "name" || len(td.Uses[0].Tables) != 1 || len(td.Uses[0].Shared) != 1 {
		t.Fatalf("Company's uses = %+v, want name's", td)
	}
	fields, err := json.Marshal(td.Uses[0].Fields)
	if err != nil {
		t.Fatal(err)
	}
	if want := `[{"name":"name","field":"name","column":"name","type":"string","value":true}]`; string(fields) != want {
		t.Errorf("Company's name fields = %s, want %s", fields, want)
	}
	shared, err := json.Marshal(td.Uses[0].Shared)
	if err != nil {
		t.Fatal(err)
	}
	want := `[{"name":"names","table":"company_name_names",` +
		`"key":{"name":"normalized","field":"normalized","column":"normalized","type":"string"},` +
		`"fields":[{"name":"holder","field":"holder","column":"holder","type":"Identity.UUID"}],` +
		`"version":"_version"}]`
	if string(shared) != want {
		t.Errorf("Company's names =\n%s\nwant\n%s", shared, want)
	}
	var operations []string
	for _, op := range td.Uses[0].Operations {
		operations = append(operations, op.Name)
		// resolve, of the table, answers a row of it, which each adapter
		// keeps to its caller (D156); no other operation answers one.
		if want := map[string]string{"resolve": "row"}[op.Name]; op.AnswersRow != want {
			t.Errorf("Company's name %s answers the row at %q, want %q", op.Name, op.AnswersRow, want)
		}
	}
	if got, _ := json.Marshal(operations); string(got) != `["rename","addAlias","removeAlias","resolve"]` {
		t.Errorf("Company's name operations = %s", got)
	}
	if td.Deadlines != nil || td.Changes != nil || td.Events != nil {
		t.Errorf("Company's queues = %+v %+v %+v, want none", td.Deadlines, td.Changes, td.Events)
	}
	if tag := layout.Types["Tag"]; tag == nil || len(tag.Uses) != 1 || tag.Uses[0].Shared[0].Table != "tag_label_names" {
		t.Errorf("Tag = %+v", tag)
	}
}
