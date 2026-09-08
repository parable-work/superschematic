package sqlgen

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// fkOnDeleteRe captures each FK constraint name and its ON DELETE action from
// generated create.sql. (?s) lets .*? span the multi-line ALTER TABLE block up
// to the first ON DELETE of that constraint.
var fkOnDeleteRe = regexp.MustCompile(`(?s)ADD CONSTRAINT (fk_[a-z0-9_]+).*?ON DELETE ([A-Z ]+?);`)

// TestGenerateForeignKeyOnDelete exercises all three FK-build sites and every
// onDelete value in one generate, asserting an EXACT per-constraint action map.
// A bare strings.Contains would pass even if two FKs' actions were swapped; the
// map is swap-proof and is the coverage net for the HasMany back-ref (L229) and
// explicit-scalar (L494) sites the fixture-db golden does not exercise.
func TestGenerateForeignKeyOnDelete(t *testing.T) {
	schema := ir.NewSchema("synthetic-ondelete", ir.SchemaKindDB)

	schema.Types["Parent"] = &ir.TypeDef{
		Name: "Parent",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			// @hasMany injects a parent_id back-ref FK on the child table (L229),
			// which has no RelationDef and must stay CASCADE.
			{Name: "children", TypeRef: ir.TypeRef{Name: "Child", IsArray: true}, Required: true, HasMany: true},
		},
	}
	schema.Types["Child"] = &ir.TypeDef{
		Name: "Child",
		Role: ir.RoleDBTable,
		Fields: []*ir.FieldDef{
			{Name: "id", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			// Object-ref relations (buildForeignKey, L694) carry the onDelete.
			{Name: "parentRestrict", TypeRef: ir.TypeRef{Name: "Parent"}, Required: true, Relation: &ir.RelationDef{Type: "Parent", OnDelete: "RESTRICT"}},
			{Name: "parentNoAction", TypeRef: ir.TypeRef{Name: "Parent"}, Required: true, Relation: &ir.RelationDef{Type: "Parent", OnDelete: "NO ACTION"}},
			{Name: "parentDefault", TypeRef: ir.TypeRef{Name: "Parent"}, Required: true, Relation: &ir.RelationDef{Type: "Parent"}},
			// Explicit-scalar relation (L494): scalar column carrying a RelationDef
			// with no onDelete -> must stay CASCADE.
			{Name: "parentScalarId", TypeRef: ir.TypeRef{Name: "string"}, Relation: &ir.RelationDef{Type: "Parent"}},
		},
	}

	output, err := Generate(schema, Options{
		SchemaName: "synthetic-ondelete",
		Clock:      codegen.FixedClock(time.Unix(0, 0).UTC()),
	})
	if err != nil {
		t.Fatalf("generate: %v", err)
	}

	outDir := t.TempDir()
	if err := WriteDDL(output, outDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	createSQL, err := os.ReadFile(filepath.Join(outDir, "create.sql"))
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}

	got := map[string]string{}
	for _, m := range fkOnDeleteRe.FindAllStringSubmatch(string(createSQL), -1) {
		got[m[1]] = m[2]
	}

	want := map[string]string{
		"fk_child_parent_restrict_id":  "RESTRICT",
		"fk_child_parent_no_action_id": "NO ACTION",
		"fk_child_parent_default_id":   "CASCADE", // empty onDelete -> default
		"fk_child_parent_scalar_id":    "CASCADE", // explicit-scalar site (L494)
		"fk_child_parent_id":           "CASCADE", // @hasMany back-ref site (L229)
	}
	// Count guard: a spurious/extra FK constraint (or a renamed one) would
	// otherwise slip past the per-name checks below.
	if len(got) != len(want) {
		t.Errorf("emitted %d FK constraints, want %d: %v\ncreate.sql:\n%s", len(got), len(want), got, createSQL)
	}
	for name, action := range want {
		if got[name] != action {
			t.Errorf("constraint %s: ON DELETE = %q, want %q\ncreate.sql:\n%s", name, got[name], action, createSQL)
		}
	}
}
