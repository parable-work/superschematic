package registry

import (
	"sync"
	"testing"
)

// TestCompileSchemaCompilesADocumentOnce holds the cache compileSchema
// keeps: one compiled schema for a document's url and bytes, another for
// another url or other bytes, and a failure answered as it was the first
// time.
func TestCompileSchemaCompilesADocumentOnce(t *testing.T) {
	const url = "superschematic://tests/compile-schema/once.json"
	document := []byte(`{"type": "object", "required": ["name"]}`)

	first, err := compileSchema(document, url)
	if err != nil {
		t.Fatal(err)
	}
	again, err := compileSchema(append([]byte(nil), document...), url)
	if err != nil {
		t.Fatal(err)
	}
	if again != first {
		t.Error("the same document under the same url was compiled twice")
	}
	if err := first.Validate(map[string]any{"name": "a"}); err != nil {
		t.Errorf("the compiled schema refuses a value it describes: %v", err)
	}
	if err := first.Validate(map[string]any{}); err == nil {
		t.Error("the compiled schema accepts a value without its required member")
	}

	elsewhere, err := compileSchema(document, "superschematic://tests/compile-schema/elsewhere.json")
	if err != nil {
		t.Fatal(err)
	}
	if elsewhere == first {
		t.Error("another url was given the first url's schema")
	}
	other, err := compileSchema([]byte(`{"type": "string"}`), url)
	if err != nil {
		t.Fatal(err)
	}
	if other == first {
		t.Error("other bytes under the same url were given the first document's schema")
	}
	if err := other.Validate("a"); err != nil {
		t.Errorf("the second document's schema is not its own: %v", err)
	}
}

func TestCompileSchemaRepeatsAFailure(t *testing.T) {
	const url = "superschematic://tests/compile-schema/failure.json"
	document := []byte(`{"type": 7}`)
	_, first := compileSchema(document, url)
	if first == nil {
		t.Fatal("a document that is not a JSON Schema compiled")
	}
	_, again := compileSchema(document, url)
	if again == nil || again.Error() != first.Error() {
		t.Errorf("the second compile answered %v, the first %v", again, first)
	}
}

// TestCompileSchemaFromManyGoroutines runs under -race: registries are
// built in parallel, and every one compiles the core's documents.
func TestCompileSchemaFromManyGoroutines(t *testing.T) {
	const url = "superschematic://tests/compile-schema/parallel.json"
	document := []byte(`{"type": "integer", "minimum": 0}`)
	var wait sync.WaitGroup
	for range 8 {
		wait.Go(func() {
			compiled, err := compileSchema(document, url)
			if err != nil {
				t.Error(err)
				return
			}
			if err := compiled.Validate(3); err != nil {
				t.Error(err)
			}
		})
	}
	wait.Wait()
}
