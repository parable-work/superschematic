package behaviortypes

import (
	"encoding/json"
	"testing"
)

// TestTSRendererReadonly: a readonly renderer marks each property, index
// signature and array readonly, inline and on its own lines, and a plain
// one writes the same schema mutable as before.
func TestTSRendererReadonly(t *testing.T) {
	var schema any
	if err := json.Unmarshal([]byte(`{
		"type": "object",
		"required": ["states"],
		"properties": {
			"states": {"type": "array", "items": {"type": "string"}},
			"outcomes": {"type": "object", "additionalProperties": {"enum": ["success", "failure"]}},
			"moves": {"type": "array", "items": {"type": "object", "required": ["to"], "properties": {"to": {"type": "string"}}}},
			"grid": {"type": "array", "items": {"type": "array", "items": {"type": "string"}}},
			"rules": {"type": "array", "items": {"type": "object", "required": ["when"], "properties": {"when": {"description": "When it fires.", "type": "string"}}}}
		}
	}`), &schema); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		readonly bool
		want     string
	}{
		{true, `{
  readonly grid?: ReadonlyArray<readonly string[]>;
  readonly moves?: ReadonlyArray<{ readonly to: string }>;
  readonly outcomes?: { readonly [key: string]: 'success' | 'failure' };
  readonly rules?: ReadonlyArray<{
    /** When it fires. */
    readonly when: string;
  }>;
  readonly states: readonly string[];
}`},
		{false, `{
  grid?: string[][];
  moves?: Array<{ to: string }>;
  outcomes?: { [key: string]: 'success' | 'failure' };
  rules?: Array<{
    /** When it fires. */
    when: string;
  }>;
  states: string[];
}`},
	}
	for _, c := range cases {
		r := TSRenderer{Readonly: c.readonly}
		if got := r.Render(schema, ""); got != c.want {
			t.Errorf("Readonly %v:\n%s\nwant:\n%s", c.readonly, got, c.want)
		}
	}
}
