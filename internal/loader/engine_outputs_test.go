package loader

import (
	"errors"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestEngineOutputsRefused (D103): outputs.engine configured the engine a
// DB service's engine types ran on, and a DB type is a table, so a config
// that sets it is refused as it is read, naming the table form, before the
// schema's types load; the same config without it loads.
func TestEngineOutputsRefused(t *testing.T) {
	schema := `{"name": "tickets", "kind": "DB", "types": {"Ticket": {"name": "Ticket", "role": "DBTable",
	  "fields": [{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true}]}}}`
	for _, tc := range []struct {
		name, outputs string
		refused       bool
	}{
		{"outputs.engine", `{"engine": {"policy": "allowAll", "tools": {"invocation": {"Payments.capturePayment": "ask"}}}}`, true},
		{"an empty outputs.engine", `{"engine": {}}`, true},
		{"no outputs.engine", `{}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := writeService(t, map[string]string{
				"schema.config.json":      `{"name": "tickets", "kind": "DB", "outputs": ` + tc.outputs + `}`,
				"src/tickets.schema.json": schema,
			})
			_, err := LoadService(dir)
			if !tc.refused {
				if err != nil {
					t.Fatalf("LoadService = %v", err)
				}
				return
			}
			if !errors.Is(err, registry.ErrEngineOutputs) || !strings.HasPrefix(err.Error(), "schema config for tickets: outputs.engine configured the engine") {
				t.Fatalf("LoadService = %v, want %v", err, registry.ErrEngineOutputs)
			}
		})
	}
	if _, err := registry.ParseOutputs(map[string]any{"engine": map[string]any{}}, registry.New(naming.Default())); !errors.Is(err, registry.ErrEngineOutputs) {
		t.Fatalf("ParseOutputs = %v, want %v", err, registry.ErrEngineOutputs)
	}
}
