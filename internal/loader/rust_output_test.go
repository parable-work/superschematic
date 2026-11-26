package loader

import (
	"testing"

	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	ir "github.com/parable-work/superschematic/ir"
)

// TestRustOutputIsReadFromTheConfig: a DB service builds Rust with its
// Rust types, an API with a Rust server, and nothing else does (D181).
func TestRustOutputIsReadFromTheConfig(t *testing.T) {
	for _, tc := range []struct {
		name string
		cfg  *schemaconfig.SchemaConfig
		want string
	}{
		{"no config", nil, ""},
		{"a DB with Rust types", &schemaconfig.SchemaConfig{Kind: ir.SchemaKindDB, Outputs: map[string]any{
			"types": map[string]any{"rust": map[string]any{"enabled": true}},
		}}, "outputs.types.rust"},
		{"a DB with Rust types off", &schemaconfig.SchemaConfig{Kind: ir.SchemaKindDB, Outputs: map[string]any{
			"types": map[string]any{"rust": map[string]any{"enabled": false}, "typescript": map[string]any{"enabled": true}},
		}}, ""},
		{"an API with a Rust server", &schemaconfig.SchemaConfig{Kind: ir.SchemaKindAPI, Outputs: map[string]any{
			"api": map[string]any{"enabled": true, "language": "rust"},
		}}, "outputs.api.language: rust"},
		{"an API with Rust types and a TypeScript server", &schemaconfig.SchemaConfig{Kind: ir.SchemaKindAPI, Outputs: map[string]any{
			"types": map[string]any{"rust": map[string]any{"enabled": true}},
			"api":   map[string]any{"enabled": true},
		}}, ""},
	} {
		if got := rustOutput(tc.cfg); got != tc.want {
			t.Errorf("%s: rustOutput = %q, want %q", tc.name, got, tc.want)
		}
	}
}
