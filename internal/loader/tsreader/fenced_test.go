package tsreader

import (
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestFencesAreRead: each @fenced of a class is recorded in the order
// written, with the fields it covers in theirs, a relation by its field's
// name; a fence with no covers holds its field alone; and a class with no
// @fenced holds none (D119).
func TestFencesAreRead(t *testing.T) {
	schema, _, err := LoadService("testdata/services/fixture-fenced-db")
	if err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string][]*ir.Fence{
		"Courier": nil,
		"Parcel":  {{Field: "version"}},
		"Delivery": {
			{Field: "planVersion", Covers: []string{"address", "courier"}},
			{Field: "runVersion", Covers: []string{"attempts", "proof"}},
		},
		"Depot": {{Field: "revision", Covers: []string{"name", "deletedAt"}}},
	} {
		td := schema.Types[name]
		if td == nil || td.Role != ir.RoleDBTable {
			t.Fatalf("%s = %+v, want a table", name, td)
		}
		if !reflect.DeepEqual(td.Fences, want) {
			t.Errorf("%s.Fences = %s", name, describeFences(td.Fences))
		}
	}
	if !schema.Types["Parcel"].Fences[0].CoversRow() {
		t.Error("Parcel's fence, which lists no covers, does not cover the row")
	}
}

func describeFences(fences []*ir.Fence) string {
	var b strings.Builder
	for _, f := range fences {
		b.WriteString(" " + f.Field + "[" + strings.Join(f.Covers, ",") + "]")
	}
	return b.String()
}

// TestFencedRefusesAFieldTwoFencesCover: the compiler takes a field in the
// covers of two fences, so the build refuses it, with the type, the fence
// and the field (D119).
func TestFencedRefusesAFieldTwoFencesCover(t *testing.T) {
	_, _, err := LoadService("testdata/services/broken-fenced-shared-cover")
	if err == nil {
		t.Fatal("LoadService took a field two fences cover")
	}
	if want := "broken-fenced-shared-cover: @fenced on Job.runVersion: covers lists priority, which the fence on Job.version covers; one fence covers a field, so a write of it advances one counter"; !strings.Contains(err.Error(), want) {
		t.Fatalf("error = %v, want it to contain %q", err, want)
	}
}
