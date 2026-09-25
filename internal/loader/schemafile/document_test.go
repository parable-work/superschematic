package schemafile

import (
	"reflect"
	"slices"
	"strings"
	"testing"
)

// TestDocumentCollectionKeysMatchTheDocument: the keys that mark a
// multi-definition document are exactly Document's collection fields, the
// json name of every field except the name, kind, description and comment
// that restate the service.
func TestDocumentCollectionKeysMatchTheDocument(t *testing.T) {
	var want []string
	documentType := reflect.TypeFor[Document]()
	for i := range documentType.NumField() {
		name, _, _ := strings.Cut(documentType.Field(i).Tag.Get("json"), ",")
		switch name {
		case "name", "kind", "description", "comment":
			continue
		}
		want = append(want, name)
	}
	got := slices.Clone(documentCollectionKeys)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("documentCollectionKeys = %v, want Document's collections %v", got, want)
	}
}

// TestDispatchRejectsAKeyTheDocumentDoesNotHave: a payload whose only key is
// not a Document collection has no shape, and the error says so. "concepts"
// was on the list without a Document field: such a file dispatched as a
// document and then failed validation on an unknown key.
func TestDispatchRejectsAKeyTheDocumentDoesNotHave(t *testing.T) {
	payload := map[string]any{"concepts": map[string]any{}}
	if IsDocumentForm(payload) {
		t.Fatal("a payload with no document collection key was taken for a document")
	}
	if _, err := dispatch(payload, core()); err == nil || !strings.Contains(err.Error(), "cannot determine schema file shape") {
		t.Fatalf("dispatch error = %v, want the shape error", err)
	}
	if !IsDocumentForm(map[string]any{"operationSets": []any{}}) {
		t.Fatal("an operationSets key is a document collection")
	}
}
