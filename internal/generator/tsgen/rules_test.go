package tsgen

import (
	"encoding/json"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/behaviorfields"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// workflowTable is a DB schema whose Order carries a Workflow on the field
// named field, with the fields it owns added as the loader adds them
// (D103).
func workflowTable(t *testing.T, reg *registry.Registry, field string) *ir.Schema {
	t.Helper()
	schema := ir.NewSchema("orders-db", ir.SchemaKindDB)
	schema.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString, TypeMappings: map[string]string{"typescript": "JSDate", "sql": "TIMESTAMPTZ"}}
	schema.Types["Order"] = &ir.TypeDef{
		Name: "Order",
		Role: ir.RoleDBTable,
		Behaviors: []ir.BehaviorRef{{Name: "Workflow", Field: field,
			Config: json.RawMessage(`{"states":["draft","paid"],"transitions":[{"from":"draft","to":"paid"}]}`)}},
		Fields: []*ir.FieldDef{
			{Name: "code", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Key: true},
			{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
		},
	}
	if _, err := behaviorfields.Expand(schema, reg); err != nil {
		t.Fatal(err)
	}
	return schema
}

// A field's operations sit under the field in its table's repository, so
// a field named as one of the repository's own members is refused, naming
// the table and the field; a table whose fields are behaviors needs the
// registry that declares them.
func TestORMRefusesAUseOnARepositoryMember(t *testing.T) {
	reg := registry.New(naming.Default())
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	_, err := Generate(workflowTable(t, reg, "findMany"), Options{SchemaName: "orders-db", Registry: reg, Clock: clock})
	if want := "tsgen: table Order: the behavior Workflow on field findMany would put its operations under the repository's member findMany, which the repository holds itself; rename the field"; err == nil || err.Error() != want {
		t.Fatalf("Generate: err = %v, want %q", err, want)
	}
	if _, err := Generate(workflowTable(t, reg, "status"), Options{SchemaName: "orders-db", Clock: clock}); err == nil || !strings.Contains(err.Error(), "no registry declares them") {
		t.Fatalf("Generate without a registry: err = %v", err)
	}
	output, err := Generate(workflowTable(t, reg, "status"), Options{SchemaName: "orders-db", Registry: reg, Clock: clock})
	if err != nil {
		t.Fatal(err)
	}
	order := output.ORM.Tables[0]
	if len(order.Uses) != 1 || order.Uses[0].ID != "status" || order.Uses[0].Operations[0].Params != "{ to: 'draft' | 'paid' }" {
		t.Fatalf("Order's uses = %+v", order.Uses)
	}
	for _, c := range order.CreateColumns() {
		if c.Owned {
			t.Errorf("a create sets the owned column %s", c.Member)
		}
	}
	if output.ORM.Tables[0].KeyType != "string" {
		t.Errorf("key type = %s", output.ORM.Tables[0].KeyType)
	}
}

// repositoryMember is a member of the HTTP runtime's TableRepository as
// orm.ts declares it: a method, accessor or field of the class body, two
// spaces in, or a parameter property of its constructor, four.
var repositoryMember = regexp.MustCompile(`^  (?:(?:private|readonly|async|get|set|static|protected|public)\s+)*([A-Za-z_$][\w$]*)\s*[(<:=?]|^    (?:private|readonly|public|protected)\s+(?:readonly\s+)?([A-Za-z_$][\w$]*)\s*[:?]`)

// TestRepositoryMembersAreTheRuntimes holds repositoryMembers to the HTTP
// runtime's TableRepository (D103): the runtime refuses a use whose id
// names any member of the repository, which it would shadow (bindUses), so
// the build refuses every one of them first, and a member added to the
// class must join the list.
func TestRepositoryMembersAreTheRuntimes(t *testing.T) {
	source, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), "runtime", "http", "typescript", "src", "orm.ts"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	start := strings.Index(text, "\nclass TableRepository {")
	if start < 0 {
		t.Fatal("orm.ts declares no class TableRepository")
	}
	end := strings.Index(text[start:], "\n}\n")
	if end < 0 {
		t.Fatal("orm.ts's class TableRepository has no end")
	}
	var found int
	for _, line := range strings.Split(text[start:start+end], "\n") {
		m := repositoryMember.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1] + m[2]
		found++
		if !slices.Contains(repositoryMembers, name) && name != "constructor" {
			t.Errorf("TableRepository has the member %s, which repositoryMembers does not list", name)
		}
	}
	if found < len(repositoryMembers)/2 {
		t.Fatalf("read %d members of TableRepository from orm.ts; the pattern no longer reads the class", found)
	}
}
