package apigen

import (
	"bytes"
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// OpenAPIHook edits the OpenAPI document the api generator builds for an API
// schema, after the core has built it and before it is written to
// openapi.json and embedded in openapi.go. It is how an extension changes
// that document (the vendor-extension keys it carries, an extra top-level
// section) without a core option. Registered with
// Registry.RegisterOpenAPIHook; hooks run in registration order.
type OpenAPIHook struct {
	// Name identifies the hook in errors.
	Name string
	// Extension is the registering extension's Name().
	Extension string
	// Edit receives the API schema and the document as decoded JSON: objects
	// are map[string]any, arrays []any, numbers json.Number. It edits doc in
	// place. An error fails the api generator and names the hook.
	Edit func(schema *ir.Schema, doc map[string]any) error
}

// applyOpenAPIHooks runs hooks over spec and returns the edited document.
// The document passes through JSON first so every hook sees the same plain
// JSON values, whatever Go types the core built it from; numbers stay
// json.Number, so no literal changes on the way back out. With no hooks the
// spec is returned as is and the output is unchanged.
func applyOpenAPIHooks(spec map[string]interface{}, schema *ir.Schema, hooks []OpenAPIHook) (map[string]interface{}, error) {
	if len(hooks) == 0 {
		return spec, nil
	}
	raw, err := json.Marshal(spec)
	if err != nil {
		return nil, fmt.Errorf("marshal OpenAPI spec for hooks: %w", err)
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var doc map[string]interface{}
	if err := decoder.Decode(&doc); err != nil {
		return nil, fmt.Errorf("decode OpenAPI spec for hooks: %w", err)
	}
	for _, hook := range hooks {
		if hook.Edit == nil {
			continue
		}
		if err := hook.Edit(schema, doc); err != nil {
			return nil, fmt.Errorf("OpenAPI hook %s: %w", hook.Name, err)
		}
	}
	return doc, nil
}
