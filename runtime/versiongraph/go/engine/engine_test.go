package engine_test

import (
	"encoding/json"
	"errors"
	"testing"

	versiongraph "github.com/parable-work/superschematic/runtime/versiongraph/go"
	"github.com/parable-work/superschematic/runtime/versiongraph/go/engine"
)

// TestNewRefusesAVersion2Descriptor: the engine takes the fixture's
// descriptor, and the core refuses it as version 2 wrote it, without each
// kind's history, before the engine reaches its storage.
func TestNewRefusesAVersion2Descriptor(t *testing.T) {
	descriptor := mustReadFixture(t)
	if _, err := engine.New(descriptor, nil, engine.Options{}); err != nil {
		t.Fatalf("New refused the fixture's descriptor: %v", err)
	}
	var d map[string]any
	if err := json.Unmarshal(descriptor, &d); err != nil {
		t.Fatal(err)
	}
	d["version"] = 2
	for _, kind := range d["kinds"].([]any) {
		delete(kind.(map[string]any), "history")
	}
	_, err := engine.New(mustJSON(t, d), nil, engine.Options{})
	var coreErr *versiongraph.Error
	if !errors.As(err, &coreErr) || coreErr.Code != "invalid_descriptor" {
		t.Fatalf("New of a version 2 descriptor = %v, want invalid_descriptor", err)
	}
}
