package jsonreader

import (
	"strings"
	"testing"
)

// TestReadRuntimeModePayload exercises the runtime-mode surface: a schema
// arriving as bytes (a persisted row, an over-the-wire body) validates
// through the same pipeline as a build-time file.
func TestReadRuntimeModePayload(t *testing.T) {
	payload := []byte(`{
		"types": {
			"SavedView": {
				"name": "SavedView",
				"role": "EmbeddedStruct",
				"comment": "A customer-saved view definition.",
				"fields": [
					{"name": "title", "typeRef": {"name": "string"}, "required": true}
				]
			}
		}
	}`)
	doc, err := Read(payload, "saved-view-row")
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	def := doc.Types["SavedView"]
	if def == nil {
		t.Fatal("decoded document has no SavedView type")
	}
	if def.Comment != "A customer-saved view definition." {
		t.Errorf("comment = %q", def.Comment)
	}
}

func TestReadRejectsInvalidRuntimePayload(t *testing.T) {
	payload := []byte(`{"types": {"X": {"name": "X", "role": "NotARole"}}}`)
	_, err := Read(payload, "saved-view-row")
	if err == nil {
		t.Fatal("Read accepted an invalid payload")
	}
	if !strings.Contains(err.Error(), "saved-view-row") {
		t.Errorf("error %q does not carry the runtime source label", err.Error())
	}
}

func TestReadFileMissing(t *testing.T) {
	if _, err := ReadFile("does/not/exist.schema.json", "exist.schema.json"); err == nil {
		t.Fatal("ReadFile succeeded on a missing file")
	}
}
