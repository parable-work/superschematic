package registry

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestIdempotentConfig (D140): @idempotent reads no argument or one config
// of ttl, a duration of at most 90 days, and required, and refuses any
// other with a message that names the option.
func TestIdempotentConfig(t *testing.T) {
	for name, c := range map[string]struct {
		args []any
		want ir.IdempotentConfig
	}{
		"no argument":  {nil, ir.IdempotentConfig{}},
		"an empty one": {[]any{map[string]any{}}, ir.IdempotentConfig{}},
		"a ttl":        {[]any{map[string]any{"ttl": "36h"}}, ir.IdempotentConfig{TTLSeconds: 36 * 3600}},
		"both":         {[]any{map[string]any{"ttl": "90m", "required": true}}, ir.IdempotentConfig{TTLSeconds: 5400, Required: true}},
		"the longest":  {[]any{map[string]any{"ttl": "2160h"}}, ir.IdempotentConfig{TTLSeconds: ir.MaxIdempotencyTTLSeconds}},
	} {
		got, err := idempotentConfig(c.args)
		if err != nil || got == nil || *got != c.want {
			t.Errorf("%s: %+v, %v, want %+v", name, got, err, c.want)
		}
	}
	for name, c := range map[string]struct {
		args []any
		want string
	}{
		"two arguments":      {[]any{map[string]any{}, "x"}, "@idempotent takes at most one config object"},
		"no object":          {[]any{"24h"}, "@idempotent config must be an object literal"},
		"a ttl in a number":  {[]any{map[string]any{"ttl": float64(3600)}}, `@idempotent ttl must be a duration in a string, such as "24h"`},
		"a ttl of no length": {[]any{map[string]any{"ttl": "soon"}}, `@idempotent ttl "soon" is no duration`},
		"a ttl too long":     {[]any{map[string]any{"ttl": "2161h"}}, `@idempotent ttl "2161h" is longer than 90 days`},
		"required in words":  {[]any{map[string]any{"required": "yes"}}, "@idempotent required must be true or false"},
		"another option":     {[]any{map[string]any{"window": "1h"}}, `@idempotent has no option "window"; it takes ttl and required`},
	} {
		if _, err := idempotentConfig(c.args); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error holding %q", name, err, c.want)
		}
	}
}
