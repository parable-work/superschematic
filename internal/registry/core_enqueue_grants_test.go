package registry

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestEnqueueOnChangeOfGrants reads of: "grants", a UserRole table's
// declaration of its grants' trigger (#625), and refuses any other of.
func TestEnqueueOnChangeOfGrants(t *testing.T) {
	queue := map[string]any{"class": "RoleGrantChanged"}
	def, err := enqueueOnChangeOf([]any{queue, map[string]any{
		"of":      "grants",
		"on":      []any{"delete", "insert"},
		"message": map[string]any{"user": "user", "role": "role"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if def.Of != ir.EnqueueOfGrants || strings.Join(def.On, ",") != "insert,delete" {
		t.Errorf("enqueueOnChangeOf = %+v", def)
	}
	_, err = enqueueOnChangeOf([]any{queue, map[string]any{"of": "roles", "on": []any{"insert"}, "message": map[string]any{"user": "user"}}})
	if err == nil || !strings.Contains(err.Error(), `@enqueueOnChange of is "grants"`) {
		t.Errorf("of roles: %v", err)
	}
}
