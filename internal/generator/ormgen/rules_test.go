package ormgen

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/behaviorfields"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/release"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// repositoryOf is the repository of typeName in output.
func repositoryOf(t *testing.T, output *ORMOutput, typeName string) Repository {
	t.Helper()
	for _, repo := range output.Repositories {
		if repo.TypeName == typeName {
			return repo
		}
	}
	t.Fatalf("no repository of %s", typeName)
	return Repository{}
}

// A table whose fields are behaviors (D103) keeps the owned fields in its
// row and leaves them out of its create input, <Type>Create, and its
// update, <Type>Update; each use's operations hang under a method named
// for its field, typed from the declaration as the config narrows it; and
// the version, the fence of every write through the core, has no fenced
// methods of its own. A table without behaviors is as it was.
func TestATableWhoseFieldsAreBehaviorsLeavesTheOwnedFieldsOutOfItsInputs(t *testing.T) {
	output := generateFixture(t, workflowFixture)
	if output.Rules == nil || !json.Valid([]byte(output.Rules.LayoutJSON)) {
		t.Fatalf("Rules = %+v, want the layout document", output.Rules)
	}
	order := repositoryOf(t, output, "Order")
	if !order.HasBehaviors || order.CreateInput() != "OrderCreate" || order.HasIfVersion() {
		t.Fatalf("Order: behaviors %t, create input %s, IfVersion %t; want true, OrderCreate, false", order.HasBehaviors, order.CreateInput(), order.HasIfVersion())
	}
	var owned []string
	for _, f := range order.OwnedFields {
		owned = append(owned, f.Use+"."+f.Name+"="+f.Field)
	}
	if want := []string{"status.status=status", "status.enteredAt=statusEnteredAt"}; !slices.Equal(owned, want) {
		t.Fatalf("Order's owned fields = %v, want %v", owned, want)
	}
	var own []string
	for _, c := range order.CoreOwn {
		own = append(own, c.Field)
	}
	if want := []string{"title", "placedAt"}; !slices.Equal(own, want) {
		t.Fatalf("Order's own fields as the core reads them = %v, want %v", own, want)
	}
	if len(order.Uses) != 1 {
		t.Fatalf("Order's uses = %+v, want status", order.Uses)
	}
	status := order.Uses[0]
	if status.GoName != "Status" || status.Interface != "OrderStatusOperations" || len(status.Operations) != 1 {
		t.Fatalf("Order's status = %+v", status)
	}
	transition := status.Operations[0]
	if transition.GoName != "Transition" || transition.Params != "OrderStatusTransitionParams" || transition.ResultReturn() != "*OrderStatusTransitionResult" {
		t.Fatalf("Order's status transition = %+v", transition)
	}
	if len(status.Enums) != 1 || status.Enums[0].Name != "OrderStatusState" || len(status.Enums[0].Members) != 3 || status.Enums[0].Members[1].Const != "OrderStatusStatePaid" {
		t.Fatalf("Order's status enums = %+v, want OrderStatusState of draft, paid and shipped", status.Enums)
	}
	shipment := repositoryOf(t, output, "Shipment")
	var methods []string
	for _, use := range shipment.Uses {
		methods = append(methods, use.GoName+" "+use.Interface)
	}
	if want := []string{"Status ShipmentStatusOperations", "PaymentStatus ShipmentPaymentStatusOperations"}; !slices.Equal(methods, want) {
		t.Fatalf("Shipment's uses = %v, want %v", methods, want)
	}

	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatal(err)
	}
	for typeName, members := range map[string][]string{
		"OrderCreate":    {"Id", "Title", "PlacedAt"},
		"OrderUpdate":    {"Title", "PlacedAt", "PlacedAtSetNull", "UpdatedBy"},
		"ShipmentCreate": {"Id", "Reference"},
		"ShipmentUpdate": {"Reference", "UpdatedBy"},
	} {
		if got := structMembers(t, outDir, typeName); !slices.Equal(got, members) {
			t.Errorf("%s's members = %v, want %v", typeName, got, members)
		}
	}
	source, err := os.ReadFile(filepath.Join(outDir, "repository_shipment.go"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"func (r *ShipmentRepository) Status() ShipmentStatusOperations",
		"func (r *ShipmentRepository) PaymentStatus() ShipmentPaymentStatusOperations",
		"ShipmentPaymentStatusStateRefunded ShipmentPaymentStatusState = \"refunded\"",
		"func (r *ShipmentRepository) CreateOne(ctx context.Context, input *ShipmentCreate) (*types.Shipment, error)",
	} {
		if !strings.Contains(string(source), want) {
			t.Errorf("repository_shipment.go has no %q", want)
		}
	}
	for _, unwanted := range []string{"UpdateOneIfVersion", "DeleteOneIfVersion", "versionMiss"} {
		if strings.Contains(string(source), unwanted) {
			t.Errorf("repository_shipment.go has %s, which a table whose fields are behaviors does not", unwanted)
		}
	}

	plain := repositoryOf(t, generateFixture(t, "fixture-optimistic-db"), "Shelf")
	if plain.HasBehaviors || plain.CreateInput() != "types.Shelf" || !plain.HasIfVersion() || len(plain.Uses) != 0 {
		t.Fatalf("Shelf: behaviors %t, create input %s, IfVersion %t, uses %v; want an optimistic table as it was", plain.HasBehaviors, plain.CreateInput(), plain.HasIfVersion(), plain.Uses)
	}
}

// Generate refuses a table whose fields are behaviors when no registry
// declares them.
func TestATableWhoseFieldsAreBehaviorsNeedsARegistry(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, workflowFixture), loader.WithRegistry(registry.New(naming.Default())))
	if err != nil {
		t.Fatal(err)
	}
	_, err = Generate(schema, Options{SchemaName: workflowFixture, ModulePath: "example.com/orm", TypesModule: "example.com/types"})
	if want := "ormgen: table Order composes behaviors, and no registry declares them"; err == nil || err.Error() != want {
		t.Fatalf("Generate = %v, want %q", err, want)
	}
}

// workflowService writes a DB service in the data form whose tables each
// carry a Workflow on the field each names, by type, and returns it loaded
// with the core's registry.
func workflowService(t *testing.T, fields map[string]string) (*registry.Registry, string) {
	t.Helper()
	types := map[string]any{}
	for typeName, field := range fields {
		types[typeName] = map[string]any{
			"name": typeName,
			"role": "DBTable",
			"behaviors": []any{map[string]any{
				"name":   "Workflow",
				"field":  field,
				"config": map[string]any{"states": []string{"open", "closed"}, "transitions": []any{map[string]string{"from": "open", "to": "closed"}}},
			}},
			"fields": []any{
				map[string]any{"name": "id", "typeRef": map[string]string{"name": "Identity.UUID"}, "required": true, "key": true, "autoGenerated": true},
				map[string]any{"name": "title", "typeRef": map[string]string{"name": "string"}, "required": true},
			},
		}
	}
	doc := map[string]any{
		"name": "fixture-workflow-names-db",
		"kind": "DB",
		"scalars": map[string]any{
			"Identity.UUID":     map[string]string{"name": "Identity.UUID", "languagePrimitive": "string"},
			"Temporal.DateTime": map[string]string{"name": "Temporal.DateTime", "languagePrimitive": "string"},
		},
		"types": types,
	}
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "src", "tables.schema.json"), encoded, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "schema.config.json"), []byte(`{"name": "fixture-workflow-names-db", "kind": "DB", "outputs": {"types": {"go": {"enabled": true}}}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	return registry.New(naming.Default()), dir
}

// A use whose method would be one the repository holds itself, or whose
// types another use already names, fails Generate, naming the table, the
// behavior and the field to rename.
func TestAUseThatWouldTakeANameThePackageHoldsIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{
			"a repository method",
			map[string]string{"Order": "findOne"},
			"ormgen: table Order: the behavior Workflow on field findOne would put its operations under the repository's method FindOne, which the repository holds itself; rename the field",
		},
		{
			"a name another use takes",
			map[string]string{"Order": "lineStatus", "OrderLine": "status"},
			"ormgen: table OrderLine: the behavior Workflow on field status would name OrderLineStatusOperations, which the behavior Workflow on Order.lineStatus already names; rename the field",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			reg, dir := workflowService(t, tc.fields)
			schema, err := loader.LoadService(dir, loader.WithRegistry(reg))
			if err != nil {
				t.Fatalf("load: %v", err)
			}
			_, err = Generate(schema, Options{SchemaName: "fixture-workflow-names-db", ModulePath: "example.com/orm", TypesModule: "example.com/types", Registry: reg})
			if err == nil || err.Error() != tc.want {
				t.Fatalf("Generate = %v, want %q", err, tc.want)
			}
		})
	}
}

// The ORM of a schema whose tables' fields are behaviors requires the
// rules Go module (D103): SetReplacePaths replaces it with [paths]
// rules_go, and SetReleasePins pins it at the release's version. A schema
// with none reaches neither.
func TestTheRulesModuleIsReplacedAndPinned(t *testing.T) {
	paths := naming.LocalPaths{ScalarGo: "/repo/scalars/go", SchemaIR: "/repo/ir", RulesGo: "/repo/runtime/rules/go"}
	rel := release.Release{Version: "1.2.3"}
	d := naming.Default()

	output := generateFixture(t, workflowFixture)
	if err := SetReplacePaths(output, paths, "/repo/out/orm/fixture-workflow-db"); err != nil {
		t.Fatal(err)
	}
	if want := "../../../runtime/rules/go"; output.RulesReplacePath != want {
		t.Fatalf("RulesReplacePath = %q, want %q", output.RulesReplacePath, want)
	}
	SetReleasePins(output, d.ReleasePins(naming.LocalPaths{}, rel))
	if got := output.Pins[d.RulesGoModule]; got != "v1.2.3" {
		t.Fatalf("the rules module's pin = %q, want v1.2.3 (pins %v)", got, output.Pins)
	}
	outDir := t.TempDir()
	if err := WriteORM(output, outDir); err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile(filepath.Join(outDir, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"\t" + d.RulesGoModule + " v1.2.3\n", d.RulesGoModule + " => " + d.RulesGoModule + " v1.2.3"} {
		if !strings.Contains(string(mod), want) {
			t.Errorf("go.mod has no %q:\n%s", want, mod)
		}
	}

	plain := generateFixture(t, "fixture-optimistic-db")
	if err := SetReplacePaths(plain, paths, "/repo/out/orm/fixture-optimistic-db"); err != nil {
		t.Fatal(err)
	}
	SetReleasePins(plain, d.ReleasePins(naming.LocalPaths{}, rel))
	if _, pinned := plain.Pins[d.RulesGoModule]; plain.RulesReplacePath != "" || pinned {
		t.Fatalf("a schema with no behaviors: RulesReplacePath %q, pins %v; want neither", plain.RulesReplacePath, plain.Pins)
	}
}

// TestATableWhoseFieldsAreBehaviorsCompiles builds and vets the ORM of
// tables whose fields are behaviors (D103) in each shape a repository
// writes through the core differently: fixture-db's tables, extended as
// TestGeneratedORMCompiles extends them (versioned, soft-deletable with
// deletedBy and a history actor, a relation, audit stamps, JSON, lists,
// maps, enums with defaults, a UUID key other than id, a string key and a
// number key), and fixture-vault-db's, whose Encrypted<T> fields a create
// seals and an update seals for its row. Each table takes a Workflow on a
// field of its own, whose owned fields the loader's pass adds. The module
// builds without the core's archive, which only a binary links.
func TestATableWhoseFieldsAreBehaviorsCompiles(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	workflow := json.RawMessage(`{"states":["open","closed"],"transitions":[{"from":"open","to":"closed"}]}`)
	for _, tc := range []struct {
		fixture string
		extend  func(*ir.Schema)
		tables  []string
	}{
		{"fixture-db", func(schema *ir.Schema) {
			extendFixtureForCompileCoverage(schema)
			// updatedBy is the history actor of Tenant's deletes.
			tenant := schema.Types["Tenant"]
			tenant.Fields = append(tenant.Fields, &ir.FieldDef{Name: "updatedBy", TypeRef: ir.TypeRef{Name: "Identity.UUID"}})
			// A number key, which a create names: the core's key is the
			// row's own, and createThroughCore reads the row back by it.
			schema.Types["Ticket"] = &ir.TypeDef{Name: "Ticket", Role: ir.RoleDBTable, Fields: []*ir.FieldDef{
				{Name: "number", TypeRef: ir.TypeRef{Name: "number"}, Required: true, Key: true},
				{Name: "title", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			}}
		}, []string{"Tenant", "TenantUser", "Shipment", "Coupon", "UnionRecord", "LabelSet", "Ticket"}},
		{"fixture-vault-db", nil, []string{"PlaidItem", "ApiCredential"}},
	} {
		t.Run(tc.fixture, func(t *testing.T) {
			paths := testpaths.Local(t)
			reg := registry.New(naming.Default())
			schema, err := loader.LoadService(filepath.Join(fixturesDir, tc.fixture), loader.WithRegistry(reg))
			if err != nil {
				t.Fatalf("load %s: %v", tc.fixture, err)
			}
			if tc.extend != nil {
				tc.extend(schema)
			}
			for _, name := range tc.tables {
				td := schema.Types[name]
				td.Behaviors = append(td.Behaviors, ir.BehaviorRef{Name: "Workflow", Field: "stage", Config: workflow})
			}
			if _, err := behaviorfields.Expand(schema, reg); err != nil {
				t.Fatalf("add the owned fields: %v", err)
			}

			clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
			typesModule := "example.com/schemas/types/go/" + tc.fixture
			root := t.TempDir()
			typesDir := filepath.Join(root, "types", "go", tc.fixture)
			ormDir := filepath.Join(root, "orm", tc.fixture)
			typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: tc.fixture, ModulePath: typesModule, Registry: reg, Clock: clock})
			if err != nil {
				t.Fatalf("generate types: %v", err)
			}
			if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
				t.Fatal(err)
			}
			if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
				t.Fatal(err)
			}
			output, err := Generate(schema, Options{SchemaName: tc.fixture, ModulePath: "example.com/schemas/orm/" + tc.fixture, TypesModule: typesModule, Registry: reg, Clock: clock})
			if err != nil {
				t.Fatalf("generate orm: %v", err)
			}
			for _, name := range tc.tables {
				if repo := repositoryOf(t, output, name); !repo.HasBehaviors || len(repo.Uses) != 1 {
					t.Fatalf("%s: behaviors %t, uses %v", name, repo.HasBehaviors, repo.Uses)
				}
			}
			if err := SetReplacePaths(output, paths, ormDir); err != nil {
				t.Fatal(err)
			}
			if err := WriteORM(output, ormDir); err != nil {
				t.Fatal(err)
			}
			tidyGeneratedModule(t, ormDir)
			for _, args := range [][]string{{"build", "./..."}, {"vet", "./..."}} {
				cmd := exec.Command("go", args...)
				cmd.Dir = ormDir
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("go %s in the generated ORM module: %v\n%s", strings.Join(args, " "), err, out)
				}
			}
		})
	}
}

// tidyGeneratedModule runs go mod tidy in a generated module. It skips the
// test only when tidy could not reach the network (offline); any other
// failure fails it, since the rules module is replaced by a local path and
// a tidy that fails over it (a package the module does not hold, say) is a
// defect of the generated module.
func tidyGeneratedModule(t *testing.T, dir string) {
	t.Helper()
	tidy := exec.Command("go", "mod", "tidy")
	tidy.Dir = dir
	out, err := tidy.CombinedOutput()
	if err == nil {
		return
	}
	for _, offline := range []string{"dial tcp", "no such host", "i/o timeout", "connection refused", "network is unreachable", "TLS handshake timeout"} {
		if strings.Contains(string(out), offline) {
			t.Skipf("go mod tidy could not reach the network (offline): %v\n%s", err, out)
		}
	}
	t.Fatalf("go mod tidy in the generated module: %v\n%s", err, out)
}
