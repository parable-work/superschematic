package ormgen

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// workflowORMCases are the cases the generated module's test runs over
// fixture-workflow-db, by name: tsgen's workflow.test.ts's, whose expected
// messages are the specification, on the generated repository and, where a
// case moves the row under a command, rules.Execute over its RulesLayout.
var workflowORMCases = []string{
	"a create runs the core: each status starts in its initial state, stamped when it entered it, at version 1",
	"a field's typed operation moves it along a transition and bumps the version; the other field stays",
	"a move the config has no transition for is vetoed in the engine's words, and the row stays",
	"a transition that names a permission is forbidden to a caller who lacks it, in the engine's words, and runs for one who holds it",
	"an update takes no owned field: it writes the own fields, and one that names an owned field is refused; a delete removes the row",
	"a raw SQL write of an owned column is never refused, and moves the row's version",
	"a write that moves the row under a command runs the command again from fresh state",
	"a row that moves under every run is refused as busy after 8 runs, and nothing is written",
	"an upsert by a unique key inserts a new row with what the core's create gives it, and updates a held row's own fields, its states kept",
	"an operation on a key no row holds is ErrNotFound and the core's not_found",
}

// TestWorkflowTablesOnPostgres holds the generated Go ORM of a database
// whose tables' fields are behaviors to the rules module's adapter on
// Postgres (D103), as tsgen's TestWorkflowTablesEndToEnd holds the
// TypeScript repository to the HTTP runtime's. It loads fixture-workflow-db
// with the core's registry, generates its Go types, its ORM and its DDL
// into a temporary tree, checks that OrderCreate, OrderUpdate and
// ShipmentUpdate hold no field a behavior owns (as the TypeScript test's
// @ts-expect-error cases do), and builds, vets and tests the ORM module
// with workflowORMTest, whose every case (workflowORMCases) must pass on
// the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links
// the core's static archive, which scripts/rules-archive.sh stages; the
// test skips without it, and in -short mode.
func TestWorkflowTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated workflow ORM's end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated workflow ORM against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateWorkflowORMModule(t, paths)
	for typeName, owned := range map[string][]string{
		"OrderCreate":    {"Status", "StatusEnteredAt", "Version"},
		"OrderUpdate":    {"Status", "StatusEnteredAt"},
		"ShipmentCreate": {"Status", "StatusEnteredAt", "PaymentStatus", "PaymentStatusEnteredAt", "Version"},
		"ShipmentUpdate": {"Status", "StatusEnteredAt", "PaymentStatus", "PaymentStatusEnteredAt"},
	} {
		members := structMembers(t, ormDir, typeName)
		if len(members) == 0 {
			t.Fatalf("the generated ORM declares no struct %s", typeName)
		}
		for _, member := range owned {
			if slices.Contains(members, member) {
				t.Errorf("%s has the member %s, which a behavior owns: only its operations change it (members %v)", typeName, member, members)
			}
		}
	}
	if err := os.WriteFile(filepath.Join(ormDir, "workflow_test.go"), []byte(workflowORMTest), 0o644); err != nil {
		t.Fatalf("write the workflow test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range workflowORMCases {
		if want := "--- PASS: TestWorkflowTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// generateWorkflowORMModule writes fixture-workflow-db's Go types module,
// its ORM module and its Postgres DDL (under the ORM's testdata) into a
// temporary tree, loaded and generated with the core's registry, and
// returns the ORM module's directory.
func generateWorkflowORMModule(t *testing.T, paths naming.LocalPaths) string {
	t.Helper()
	reg := registry.New(naming.Default())
	schema, err := loader.LoadService(filepath.Join(fixturesDir, workflowFixture), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load %s: %v", workflowFixture, err)
	}
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/" + workflowFixture
	root := t.TempDir()
	typesDir := filepath.Join(root, "types", "go", workflowFixture)
	ormDir := filepath.Join(root, "orm", workflowFixture)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: workflowFixture, ModulePath: typesModule, Registry: reg, Clock: clock})
	if err != nil {
		t.Fatalf("generate types: %v", err)
	}
	if err := typegen.SetReplacePaths(typesOutput, paths, typesDir); err != nil {
		t.Fatalf("set types replace paths: %v", err)
	}
	if err := typegen.WriteTypes(typesOutput, typesDir); err != nil {
		t.Fatalf("write types: %v", err)
	}

	ormOutput, err := Generate(schema, Options{
		SchemaName:  workflowFixture,
		ModulePath:  "example.com/schemas/orm/" + workflowFixture,
		TypesModule: typesModule,
		Registry:    reg,
		Clock:       clock,
	})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatalf("set orm replace paths: %v", err)
	}
	if ormOutput.RulesReplacePath == "" {
		t.Fatal("SetReplacePaths gave the rules module no replace path")
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}

	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: workflowFixture})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	if err := sqlgen.WriteDDL(ddl, filepath.Join(ormDir, "testdata")); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	createSQL := filepath.Join(ormDir, "testdata", "create.sql")
	pgtest.WriteCreateSQL(t, createSQL, createSQL)
	return ormDir
}

// structMembers are the field names of the struct typeName that a Go file
// of dir declares, or none.
func structMembers(t *testing.T, dir, typeName string) []string {
	t.Helper()
	files, err := filepath.Glob(filepath.Join(dir, "*.go"))
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	for _, file := range files {
		parsed, err := parser.ParseFile(fset, file, nil, parser.SkipObjectResolution)
		if err != nil {
			t.Fatalf("parse %s: %v", file, err)
		}
		var members []string
		ast.Inspect(parsed, func(n ast.Node) bool {
			spec, ok := n.(*ast.TypeSpec)
			if !ok || spec.Name.Name != typeName {
				return true
			}
			if st, ok := spec.Type.(*ast.StructType); ok {
				for _, field := range st.Fields.List {
					for _, name := range field.Names {
						members = append(members, name.Name)
					}
				}
			}
			return false
		})
		if members != nil {
			return members
		}
	}
	return nil
}

// workflowORMTest is the generated module's test: the cases of tsgen's
// workflow.test.ts on the generated repository, in a schema of its own.
const workflowORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"reflect"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-workflow-db"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	rulespostgres "github.com/parable-work/superschematic/runtime/rules/go/postgres"
)

// movingStore is a store over tx that runs a write of its own, through the
// same transaction, before each of its first moves applies: the write
// bumps the row's version, so the core's change set finds the row moved.
type movingStore struct {
	rules.Store
	tx     pgx.Tx
	moves  int
	text   string
	values []any
}

func (s *movingStore) Apply(ctx context.Context, statements []rules.Statement) (bool, error) {
	if s.moves > 0 {
		s.moves--
		if _, err := s.tx.Exec(ctx, s.text, s.values...); err != nil {
			return false, err
		}
	}
	return s.Store.Apply(ctx, statements)
}

// sameJSON reports whether two JSON documents hold the same values, members
// in any order.
func sameJSON(t *testing.T, got, want string) bool {
	t.Helper()
	var g, w any
	if err := json.Unmarshal([]byte(got), &g); err != nil {
		t.Fatalf("decode %s: %v", got, err)
	}
	if err := json.Unmarshal([]byte(want), &w); err != nil {
		t.Fatalf("decode %s: %v", want, err)
	}
	return reflect.DeepEqual(g, w)
}

// refusalOf is the *rules.Refusal err holds, failing the test without one.
func refusalOf(t *testing.T, err error) *rules.Refusal {
	t.Helper()
	refusal, ok := rules.AsRefusal(err)
	if !ok {
		t.Fatalf("err = %v, want a *rules.Refusal", err)
	}
	return refusal
}

func millis(at *types.TemporalDateTime) int64 {
	if at == nil {
		return 0
	}
	return time.Time(*at).UnixMilli()
}

func TestWorkflowTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the workflow tables against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	ctx := context.Background()
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_workflow_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	alice, err := types.ParseIdentityUUID("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	if err != nil {
		t.Fatal(err)
	}
	ctx = WithUserID(ctx, alice)
	subject := fmt.Sprint(alice)
	title := func(text string) *string { return &text }

	t.Run("a create runs the core: each status starts in its initial state, stamped when it entered it, at version 1", func(t *testing.T) {
		before := time.Now().Add(-time.Second).UnixMilli()
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Desk"})
		if err != nil {
			t.Fatalf("create order: %v", err)
		}
		if order.Title != "Desk" || order.Status != string(OrderStatusStateDraft) || order.Version != 1 || millis(order.StatusEnteredAt) < before {
			t.Fatalf("order = %+v, want Desk, draft since the create, at version 1", order)
		}
		shipment, err := db.Shipment.CreateOne(ctx, &ShipmentCreate{Reference: "S-1"})
		if err != nil {
			t.Fatalf("create shipment: %v", err)
		}
		if shipment.Reference != "S-1" || shipment.Status != string(ShipmentStatusStatePending) || shipment.PaymentStatus != string(ShipmentPaymentStatusStateUnpaid) || shipment.Version != 1 || millis(shipment.PaymentStatusEnteredAt) < before {
			t.Fatalf("shipment = %+v, want S-1, pending and unpaid since the create, at version 1", shipment)
		}
		// The columns are where D103's layout rule puts them: the value
		// field in the field's own column, every other field after it.
		var status, paymentStatus string
		var version int64
		var enteredAt, paymentEnteredAt time.Time
		if err := pool.QueryRow(ctx, "SELECT status, payment_status, _version, status_entered_at, payment_status_entered_at FROM shipment WHERE reference = $1", "S-1").Scan(&status, &paymentStatus, &version, &enteredAt, &paymentEnteredAt); err != nil {
			t.Fatalf("read the shipment's columns: %v", err)
		}
		if status != "pending" || paymentStatus != "unpaid" || version != 1 || enteredAt.UnixMilli() != millis(shipment.StatusEnteredAt) || paymentEnteredAt.UnixMilli() != millis(shipment.PaymentStatusEnteredAt) {
			t.Fatalf("columns = %s, %s, %d, %v, %v", status, paymentStatus, version, enteredAt, paymentEnteredAt)
		}
	})

	t.Run("a field's typed operation moves it along a transition and bumps the version; the other field stays", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Chair"})
		if err != nil {
			t.Fatal(err)
		}
		moved, err := db.Order.Status().Transition(ctx, *order.Id, OrderStatusTransitionParams{To: OrderStatusStatePaid})
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
		if *moved != (OrderStatusTransitionResult{From: OrderStatusStateDraft, To: OrderStatusStatePaid}) {
			t.Fatalf("transition = %+v, want draft to paid", *moved)
		}
		paid, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if paid.Title != "Chair" || paid.Status != "paid" || paid.Version != 2 || millis(paid.StatusEnteredAt) < millis(order.StatusEnteredAt) {
			t.Fatalf("paid = %+v, want Chair, paid, at version 2", paid)
		}

		shipment, err := db.Shipment.CreateOne(ctx, &ShipmentCreate{Reference: "S-2"})
		if err != nil {
			t.Fatal(err)
		}
		payment, err := db.Shipment.PaymentStatus().Transition(ctx, *shipment.Id, ShipmentPaymentStatusTransitionParams{To: ShipmentPaymentStatusStatePaid})
		if err != nil {
			t.Fatalf("payment transition: %v", err)
		}
		if *payment != (ShipmentPaymentStatusTransitionResult{From: ShipmentPaymentStatusStateUnpaid, To: ShipmentPaymentStatusStatePaid}) {
			t.Fatalf("payment transition = %+v, want unpaid to paid", *payment)
		}
		after, err := db.Shipment.GetOne(ctx, *shipment.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if after.Status != "pending" || millis(after.StatusEnteredAt) != millis(shipment.StatusEnteredAt) || after.PaymentStatus != "paid" || after.Version != 2 {
			t.Fatalf("shipment = %+v, want pending as it was, paid, at version 2", after)
		}
	})

	t.Run("a move the config has no transition for is vetoed in the engine's words, and the row stays", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Lamp"})
		if err != nil {
			t.Fatal(err)
		}
		key := order.Id.String()
		if _, err := db.Order.Status().Transition(ctx, *order.Id, OrderStatusTransitionParams{To: OrderStatusStatePaid}); err != nil {
			t.Fatal(err)
		}
		_, err = db.Order.Status().Transition(ctx, *order.Id, OrderStatusTransitionParams{To: OrderStatusStateDraft})
		vetoed := refusalOf(t, err)
		if vetoed.Code != rules.CodeVetoed || vetoed.Status != 409 || vetoed.Class != "BehaviorVetoError" {
			t.Fatalf("refusal = %+v, want vetoed, 409, BehaviorVetoError", vetoed)
		}
		if want := "behavior Workflow vetoes transition of Order " + key + ": no transition leads from paid to draft; from paid it can move to shipped"; vetoed.Message != want {
			t.Fatalf("message = %q, want %q", vetoed.Message, want)
		}
		if want := ` + "`" + `{"behavior":"Workflow","use":"status","action":"transition","reason":"no transition leads from paid to draft; from paid it can move to shipped","vetoCode":"transition_not_allowed","vetoDetails":{"from":"paid","to":"draft","allowed":["shipped"]}}` + "`" + `; !sameJSON(t, string(vetoed.Details), want) {
			t.Fatalf("details = %s, want %s", vetoed.Details, want)
		}
		row, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != "paid" || row.Version != 2 {
			t.Fatalf("order = %+v, want paid at version 2", row)
		}
	})

	t.Run("a transition that names a permission is forbidden to a caller who lacks it, in the engine's words, and runs for one who holds it", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Shelf"})
		if err != nil {
			t.Fatal(err)
		}
		key := order.Id.String()
		if _, err := db.Order.Status().Transition(ctx, *order.Id, OrderStatusTransitionParams{To: OrderStatusStatePaid}); err != nil {
			t.Fatal(err)
		}
		_, err = db.Order.Status().Transition(ctx, *order.Id, OrderStatusTransitionParams{To: OrderStatusStateShipped})
		forbidden := refusalOf(t, err)
		want := rules.Refusal{Code: rules.CodeForbidden, Status: 403, Class: "EngineError", Message: subject + " may not move Order " + key + "'s status from paid to shipped: the transition needs permission orders.ship"}
		if forbidden.Code != want.Code || forbidden.Status != want.Status || forbidden.Class != want.Class || forbidden.Message != want.Message || len(forbidden.Details) != 0 {
			t.Fatalf("refusal = %+v, want %+v", forbidden, want)
		}
		row, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != "paid" || row.Version != 2 {
			t.Fatalf("order = %+v, want paid at version 2", row)
		}
		sam := WithBehaviorCaller(ctx, rules.Caller{Subject: "sam", Permissions: []string{"orders.ship"}})
		moved, err := db.Order.Status().Transition(sam, *order.Id, OrderStatusTransitionParams{To: OrderStatusStateShipped})
		if err != nil {
			t.Fatalf("transition as sam: %v", err)
		}
		if *moved != (OrderStatusTransitionResult{From: OrderStatusStatePaid, To: OrderStatusStateShipped}) {
			t.Fatalf("transition = %+v, want paid to shipped", *moved)
		}
		if row, err = db.Order.GetOne(ctx, *order.Id, nil); err != nil || row.Status != "shipped" || row.Version != 3 {
			t.Fatalf("order = %+v, %v, want shipped at version 3", row, err)
		}
	})

	t.Run("an update takes no owned field: it writes the own fields, and one that names an owned field is refused; a delete removes the row", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Stool"})
		if err != nil {
			t.Fatal(err)
		}
		updated, err := db.Order.UpdateOne(ctx, *order.Id, &OrderUpdate{Title: title("Bar stool")})
		if err != nil {
			t.Fatalf("update: %v", err)
		}
		if updated.Title != "Bar stool" || updated.Status != "draft" || millis(updated.StatusEnteredAt) != millis(order.StatusEnteredAt) || updated.Version != 2 {
			t.Fatalf("updated = %+v, want Bar stool, draft as it was, at version 2", updated)
		}
		// OrderUpdate has no Status member (the outer test reads the
		// generated source for it); one a caller of the adapter names
		// anyway is the core's to refuse.
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		host := rules.Host{Layout: RulesLayout, Store: rulespostgres.NewStore(tx)}
		for _, owned := range []string{"status", "statusEnteredAt"} {
			err := rules.RunUpdate(ctx, host, "Order", order.Id.String(), map[string]any{owned: "shipped"}, rules.Caller{Subject: subject}, nil)
			refusal := refusalOf(t, err)
			if refusal.Code != rules.CodeInvalidInstance || refusal.Status != 422 || refusal.Class != "InstanceValidationError" {
				t.Fatalf("an update naming %s = %+v, want invalid_instance, 422, InstanceValidationError", owned, refusal)
			}
			var details struct {
				Issues []struct{ Path, Rule string } ` + "`" + `json:"issues"` + "`" + `
			}
			if err := json.Unmarshal(refusal.Details, &details); err != nil || len(details.Issues) != 1 || details.Issues[0].Path != owned || details.Issues[0].Rule != "owned" {
				t.Fatalf("an update naming %s: details = %s, want one issue at %s, rule owned", owned, refusal.Details, owned)
			}
		}
		if err := tx.Rollback(ctx); err != nil {
			t.Fatal(err)
		}
		row, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.Title != "Bar stool" || row.Status != "draft" || row.Version != 2 {
			t.Fatalf("order = %+v, want Bar stool, draft, at version 2", row)
		}
		if err := db.Order.DeleteOne(ctx, *order.Id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if _, err := db.Order.GetOne(ctx, *order.Id, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a read after the delete = %v, want ErrNotFound", err)
		}
	})

	t.Run("a raw SQL write of an owned column is never refused, and moves the row's version", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Rug"})
		if err != nil {
			t.Fatal(err)
		}
		if err := db.Transaction(ctx, func(tx TxInterface) error {
			tag, err := tx.Exec(ctx, "UPDATE \"order\" SET status = $1 WHERE id = $2", "shipped", order.Id.ToUUID())
			if err == nil && tag.RowsAffected() != 1 {
				err = fmt.Errorf("the raw write changed %d rows, want 1", tag.RowsAffected())
			}
			return err
		}); err != nil {
			t.Fatalf("raw write: %v", err)
		}
		row, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != "shipped" || row.Version != 2 {
			t.Fatalf("order = %+v, want shipped at version 2", row)
		}
		// The core goes on from what the row holds: shipped is terminal.
		_, err = db.Order.Status().Transition(ctx, *order.Id, OrderStatusTransitionParams{To: OrderStatusStatePaid})
		var details struct {
			VetoCode string ` + "`" + `json:"vetoCode"` + "`" + `
		}
		if refusal := refusalOf(t, err); json.Unmarshal(refusal.Details, &details) != nil || details.VetoCode != "terminal_state" {
			t.Fatalf("a transition from the raw state = %+v, want the veto terminal_state", refusal)
		}
	})

	transition := func(key string) rules.Command {
		return rules.Command{
			Caller: rules.Caller{Subject: subject},
			Now:    time.Now().UnixMilli(),
			Action: rules.Action{Kind: rules.ActionOperation, Key: key, UseID: "status", Operation: "transition", Params: json.RawMessage(` + "`" + `{"to":"paid"}` + "`" + `)},
		}
	}

	t.Run("a write that moves the row under a command runs the command again from fresh state", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Mirror"})
		if err != nil {
			t.Fatal(err)
		}
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		store := &movingStore{Store: rulespostgres.NewStore(tx), tx: tx, moves: 1, text: "UPDATE \"order\" SET title = $1 WHERE id = $2", values: []any{"Wall mirror", order.Id.ToUUID()}}
		result, err := rules.Execute(ctx, rules.Host{Layout: RulesLayout, Store: store}, "Order", transition(order.Id.String()), nil)
		if err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("execute: %v", err)
		}
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		// Run twice: the first run's change set found the row moved and
		// wrote nothing, and the second ran on the row as the write left it.
		if result.Attempts != 2 || !sameJSON(t, string(result.Result), ` + "`" + `{"from":"draft","to":"paid"}` + "`" + `) {
			t.Fatalf("result = %d attempts, %s; want 2 attempts, draft to paid", result.Attempts, result.Result)
		}
		row, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.Title != "Wall mirror" || row.Status != "paid" || row.Version != 3 {
			t.Fatalf("order = %+v, want Wall mirror, paid, at version 3", row)
		}
	})

	t.Run("a row that moves under every run is refused as busy after 8 runs, and nothing is written", func(t *testing.T) {
		order, err := db.Order.CreateOne(ctx, &OrderCreate{Title: "Vase"})
		if err != nil {
			t.Fatal(err)
		}
		key := order.Id.String()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		store := &movingStore{Store: rulespostgres.NewStore(tx), tx: tx, moves: rules.MaxAttempts, text: "UPDATE \"order\" SET placed_at = NULL WHERE id = $1", values: []any{order.Id.ToUUID()}}
		_, err = rules.Execute(ctx, rules.Host{Layout: RulesLayout, Store: store}, "Order", transition(key), nil)
		busy := refusalOf(t, err)
		// The transaction goes on: each moved change set rolled back to its
		// savepoint, and the moves are the test's own writes.
		if err := tx.Commit(ctx); err != nil {
			t.Fatal(err)
		}
		want := fmt.Sprintf("transition of Order %s: the row kept moving while the command ran; it ran %d times and nothing was written, so it may be tried again", key, rules.MaxAttempts)
		if busy.Code != rules.CodeBusy || busy.Status != 503 || busy.Class != "EngineError" || busy.Message != want {
			t.Fatalf("refusal = %+v, want busy, 503, EngineError, %q", busy, want)
		}
		if store.moves != 0 {
			t.Fatalf("%d moves were left: the command ran fewer than %d times", store.moves, rules.MaxAttempts)
		}
		row, err := db.Order.GetOne(ctx, *order.Id, nil)
		if err != nil {
			t.Fatal(err)
		}
		if row.Status != "draft" || row.Version != int64(1+rules.MaxAttempts) {
			t.Fatalf("order = %+v, want draft at version %d", row, 1+rules.MaxAttempts)
		}
	})

	t.Run("an upsert by a unique key inserts a new row with what the core's create gives it, and updates a held row's own fields, its states kept", func(t *testing.T) {
		before := time.Now().Add(-time.Second).UnixMilli()
		inserted, err := db.Shipment.UpsertOneResult(ctx, &ShipmentCreate{Reference: "U-1"}, ShipmentUniqueByReference, &ShipmentUpdate{Reference: title("U-1")}, nil)
		if err != nil {
			t.Fatalf("upsert: %v", err)
		}
		row := inserted.Row
		if inserted.Status != UpsertInserted || row.Reference != "U-1" || row.Status != "pending" || row.PaymentStatus != "unpaid" || row.Version != 1 || millis(row.PaymentStatusEnteredAt) < before {
			t.Fatalf("upsert = %s, %+v; want inserted, U-1, pending and unpaid since the create, at version 1", inserted.Status, row)
		}
		moved, err := db.Shipment.PaymentStatus().Transition(ctx, *row.Id, ShipmentPaymentStatusTransitionParams{To: ShipmentPaymentStatusStatePaid})
		if err != nil || *moved != (ShipmentPaymentStatusTransitionResult{From: ShipmentPaymentStatusStateUnpaid, To: ShipmentPaymentStatusStatePaid}) {
			t.Fatalf("payment transition = %+v, %v; want unpaid to paid", moved, err)
		}

		many, err := db.Shipment.UpsertMany(ctx, []*ShipmentCreate{{Reference: "U-1"}, {Reference: "U-2"}}, ShipmentUniqueByReference, ShipmentFields{Reference: true}, nil)
		if err != nil {
			t.Fatalf("upsert many: %v", err)
		}
		type got struct {
			Status                         UpsertStatus
			Reference, State, PaymentState string
			Version                        int64
		}
		var results []got
		for _, result := range many {
			results = append(results, got{result.Status, result.Row.Reference, result.Row.Status, result.Row.PaymentStatus, result.Row.Version})
		}
		if want := []got{{UpsertUpdated, "U-1", "pending", "paid", 3}, {UpsertInserted, "U-2", "pending", "unpaid", 1}}; !reflect.DeepEqual(results, want) {
			t.Fatalf("upsert many = %+v, want %+v", results, want)
		}

		// An update that names a field a behavior owns is refused before
		// anything is written.
		_, err = db.Shipment.UpsertMany(ctx, []*ShipmentCreate{{Reference: "U-3"}}, ShipmentUniqueByReference, ShipmentFields{PaymentStatus: true}, nil)
		if want := "orm: UpsertMany of Shipment sets the fields an update sets, not \"paymentStatus\", which behavior Workflow on paymentStatus owns (D103): only its operations change it"; err == nil || err.Error() != want {
			t.Fatalf("an upsert naming an owned field = %v, want %q", err, want)
		}
		if _, err := db.Shipment.FindOne(ctx, &ShipmentFilter{Reference: &StringFilter{Eq: title("U-3")}}, nil); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a read of the refused upsert's row = %v, want ErrNotFound", err)
		}
	})

	t.Run("an operation on a key no row holds is ErrNotFound and the core's not_found", func(t *testing.T) {
		missing, err := types.ParseIdentityUUID("00000000-0000-4000-8000-0000000000aa")
		if err != nil {
			t.Fatal(err)
		}
		_, err = db.Order.Status().Transition(ctx, missing, OrderStatusTransitionParams{To: OrderStatusStatePaid})
		if !errors.Is(err, ErrNotFound) {
			t.Fatalf("err = %v, want ErrNotFound", err)
		}
		if refusal := refusalOf(t, err); refusal.Code != rules.CodeNotFound {
			t.Fatalf("refusal = %+v, want not_found", refusal)
		}
		if err := db.Order.DeleteOne(ctx, missing); !errors.Is(err, ErrNotFound) {
			t.Fatalf("a delete of the key = %v, want ErrNotFound", err)
		}
	})
}
`
