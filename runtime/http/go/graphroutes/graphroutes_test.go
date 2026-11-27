package graphroutes

import (
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
)

// set is a graph with a kind that holds a to-one relation.
var set = &Set{
	Graph:   "Doc",
	Root:    "Doc",
	RootKey: "id",
	Kinds: []Kind{
		{Kind: "line_item", Member: "lineItem", Fields: []string{"text", "authorId"}, Relations: []Relation{{Member: "authorId", Field: "author", Key: "id"}}},
		{Kind: "note", Member: "note", Fields: []string{"body"}},
	},
}

// TestRefusalIsTheProblemOfItsCode: a refusal of the engine or the core is
// the problem of its code's status, with its code and message, and an
// invalid tree's findings as details; a problem the handler made, and any
// other error, are returned as they are.
func TestRefusalIsTheProblemOfItsCode(t *testing.T) {
	cause := errors.New("engine")
	for code, status := range map[string]int{
		"not_found": http.StatusNotFound, "no_actor": http.StatusForbidden, "version_conflict": http.StatusConflict,
		"root_mismatch": http.StatusConflict, "invalid_tree": http.StatusUnprocessableEntity, "unmatched_resolution": http.StatusUnprocessableEntity,
	} {
		var problem *apperror.AppError
		if err := Refusal(cause, code, "the words", nil); !errors.As(err, &problem) || problem.HTTPStatus() != status || problem.ErrorCode != code || problem.Message != "the words" || !errors.Is(err, cause) {
			t.Errorf("%s: %v, want a %d", code, err, status)
		}
	}
	var problem *apperror.AppError
	findings := []map[string]string{{"code": "missing_parent"}}
	if err := Refusal(cause, "invalid_tree", "bad", findings); !errors.As(err, &problem) || !reflect.DeepEqual(problem.Details, map[string]any{"findings": findings}) {
		t.Errorf("an invalid tree's details: %+v", problem)
	}
	if err := Refusal(cause, "", "", nil); err != cause {
		t.Errorf("an error of no code is %v", err)
	}
	if err := Refusal(cause, "walk_ceiling", "", nil); err != cause {
		t.Errorf("an error of a code no refusal has is %v", err)
	}
	made := NotFound("ref not found")
	if err := Refusal(made, "version_conflict", "", nil); err != made {
		t.Errorf("a problem the handler made is %v", err)
	}
	if made.HTTPStatus() != http.StatusNotFound || made.ErrorCode != "not_found" {
		t.Errorf("NotFound = %+v", made)
	}
}

// TestFacadeEditsAndRows: a save's edits hold each relation's key as the
// object of the facade's field, and a tree's rows hold it as the row's
// member again, with the entity key and the content members alone.
func TestFacadeEditsAndRows(t *testing.T) {
	edits, err := FacadeEdits(set, json.RawMessage(`{"lineItem":{"upsert":[{"entityKey":"k1","text":"a","authorId":"u1"},{"text":"b","authorId":null}],"delete":["k2"]}}`))
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]any
	if err := json.Unmarshal(edits, &got); err != nil {
		t.Fatal(err)
	}
	want := map[string]any{"lineItem": map[string]any{
		"upsert": []any{
			map[string]any{"entityKey": "k1", "text": "a", "author": map[string]any{"id": "u1"}},
			map[string]any{"text": "b", "author": nil},
		},
		"delete": []any{"k2"},
		"unset":  []any{},
	}}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("FacadeEdits = %v,\nwant %v", got, want)
	}

	type row struct {
		EntityKey string         `json:"entityKey"`
		Text      string         `json:"text"`
		Author    map[string]any `json:"author"`
		Version   int            `json:"_version"`
	}
	tree := struct {
		LineItem    []row
		Note        []map[string]any
		ContentHash string
	}{LineItem: []row{{EntityKey: "k1", Text: "a", Author: map[string]any{"id": "u1"}, Version: 3}}}
	rows, err := Rows(set, tree)
	if err != nil {
		t.Fatal(err)
	}
	if want := (Values{"lineItem": []Values{{"entityKey": "k1", "text": "a", "authorId": "u1"}}, "note": []Values{}}); !reflect.DeepEqual(rows, want) {
		t.Errorf("Rows = %v,\nwant %v", rows, want)
	}
}

// TestViews: a ref, a commit and a release pointer, as rows of the graph's
// own tables hold them, are the API's views of them.
func TestViews(t *testing.T) {
	ref, err := RefView(set, map[string]any{"id": "r", "root": map[string]any{"id": "d"}, "parentRef": map[string]any{"id": "p"}, "headCommit": nil, "name": "draft", "sealedAt": "2026-01-01T00:00:00Z", "_version": 4})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Values{"id": "r", "root": "d", "parent": "p", "name": "draft", "sealed": true, "discarded": false, "version": float64(4)}); !reflect.DeepEqual(ref, want) {
		t.Errorf("RefView = %v,\nwant %v", ref, want)
	}
	commit, err := CommitView(set, map[string]any{"id": "c", "root": map[string]any{"id": "d"}, "ref": map[string]any{"id": "r"}, "schemaEpoch": 1, "contentHash": "h", "sequence": 2, "createdAt": "t", "createdBy": "u"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if commit["message"] != "" || commit["sequence"] != float64(2) || commit["snapshot"] != true || commit["ref"] != "r" || commit["parent"] != nil {
		t.Errorf("CommitView = %v", commit)
	}
	release, err := ReleaseView(set, map[string]any{"id": "x", "root": map[string]any{"id": "d"}, "commit": map[string]any{"id": "c"}, "_version": 1})
	if err != nil {
		t.Fatal(err)
	}
	if want := (Values{"id": "x", "root": "d", "commit": "c", "version": float64(1)}); !reflect.DeepEqual(release, want) {
		t.Errorf("ReleaseView = %v", release)
	}
	if _, err := RefView(set, nil); err == nil {
		t.Error("RefView of no row is no error")
	}
}
