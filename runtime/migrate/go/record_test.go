package migrate

import (
	"errors"
	"strings"
	"testing"
)

// TestReadRecord reads a record document, compacting its JSON members, and
// refuses one of another version, with a member it does not know, with no
// service or model, or whose members are not a list and two objects.
func TestReadRecord(t *testing.T) {
	r, err := ReadRecord([]byte(`{"version": 1, "service": "tasks-db", "modelHash": "abc",
		"documents": [ {"name": "tasks-db"} ], "layout": {"types": {}}, "versions": {"Task": 2}}`))
	if err != nil {
		t.Fatal(err)
	}
	if r.Service != "tasks-db" || r.ModelHash != "abc" || string(r.Documents) != `[{"name":"tasks-db"}]` || string(r.Versions) != `{"Task":2}` {
		t.Fatalf("read %+v", r)
	}
	for doc, want := range map[string]string{
		`{"version": 2, "service": "s", "modelHash": "m", "documents": [], "layout": {}, "versions": {}}`:         "version 2",
		`{"version": 1, "service": "s", "modelHash": "m", "documents": [], "layout": {}, "versions": {}, "x": 1}`: "unknown field",
		`{"version": 1, "service": "", "modelHash": "m", "documents": [], "layout": {}, "versions": {}}`:          "service",
		`{"version": 1, "service": "s", "modelHash": "m", "documents": {}, "layout": {}, "versions": {}}`:         "documents is not a list",
		`{"version": 1, "service": "s", "modelHash": "m", "documents": [], "layout": [], "versions": {}}`:         "layout is not an object",
		`{"version": 1, "service": "s", "modelHash": "m", "documents": [], "layout": {}}`:                         "versions is not an object",
	} {
		if _, err := ReadRecord([]byte(doc)); err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("ReadRecord(%s) = %v, want %q", doc, err, want)
		}
	}
}

// TestCheckRecord refuses a record of another service or another model
// than the one the plan ends at.
func TestCheckRecord(t *testing.T) {
	plan := &Plan{Service: "tasks-db", To: "to"}
	if err := CheckRecord(plan, &Record{Service: "tasks-db", ModelHash: "to"}); err != nil {
		t.Fatal(err)
	}
	for _, r := range []*Record{{Service: "other", ModelHash: "to"}, {Service: "tasks-db", ModelHash: "from"}} {
		if err := CheckRecord(plan, r); !errors.Is(err, ErrRefused) {
			t.Errorf("CheckRecord(%+v) = %v, want a refusal", r, err)
		}
	}
}
