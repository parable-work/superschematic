package orm

// The Go repositories of fixture-workflow-db, whose tables compose a
// Workflow on their status fields, on a SQLite file and on Postgres,
// result for result (D103, D116): one script of creates, transitions,
// updates and deletes, each a command of the behaviors' core run through
// the rules module's store of its dialect, and what each call answers on
// SQLite is what it answers on Postgres. A database's clock, the time a
// status entered its state included, is compared as set or unset, and a
// refusal by its kind and the core's code.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-workflow-db"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	_ "modernc.org/sqlite"
)

type workflowCall struct {
	name   string
	answer any
}

// stampedMembers are the members a database's clock sets.
var stampedMembers = map[string]bool{"statusEnteredAt": true, "paymentStatusEnteredAt": true, "placedAt": false}

func workflowCompared(value any) any {
	switch v := value.(type) {
	case map[string]any:
		out := map[string]any{}
		for member, inner := range v {
			if stampedMembers[member] {
				if inner == nil {
					out[member] = "unset"
				} else {
					out[member] = "set"
				}
				continue
			}
			out[member] = workflowCompared(inner)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, inner := range v {
			out[i] = workflowCompared(inner)
		}
		return out
	case string:
		if strings.Contains(v, "T") && len(v) >= 20 {
			if at, err := time.Parse(time.RFC3339Nano, v); err == nil {
				return at.UTC().Format(time.RFC3339Nano)
			}
		}
	}
	return value
}

func workflowAnswer(value any, err error) any {
	if err != nil {
		if refusal, ok := rules.AsRefusal(err); ok {
			return map[string]any{"refused": string(refusal.Code), "notFound": errors.Is(err, ErrNotFound)}
		}
		if errors.Is(err, ErrNotFound) {
			return map[string]any{"refused": "not found"}
		}
		if errors.Is(err, ErrSQLiteUnsupported) {
			return map[string]any{"refused": "Postgres's"}
		}
		return map[string]any{"refused": "error: " + err.Error()}
	}
	data, marshalErr := json.Marshal(value)
	if marshalErr != nil {
		return map[string]any{"unmarshalable": marshalErr.Error()}
	}
	var decoded any
	if err := json.Unmarshal(data, &decoded); err != nil {
		return map[string]any{"undecodable": err.Error()}
	}
	return map[string]any{"ok": workflowCompared(decoded)}
}

func workflowKey(n int) types.IdentityUUID {
	id, err := types.ParseIdentityUUID(fmt.Sprintf("00000000-0000-4000-8000-0000000000%02x", n))
	if err != nil {
		panic(err)
	}
	return id
}

func workflowKeyPtr(n int) *types.IdentityUUID {
	id := workflowKey(n)
	return &id
}

func workflowScript(t *testing.T, db DatabaseInterface) []workflowCall {
	t.Helper()
	alice, err := types.ParseIdentityUUID("5f0c3a52-8a5e-4c1b-9d1e-2f6f1b7c8d90")
	if err != nil {
		t.Fatal(err)
	}
	ctx := WithUserID(context.Background(), alice)
	shipper := WithBehaviorCaller(ctx, rules.Caller{Subject: fmt.Sprint(alice), Permissions: []string{"orders.ship"}})
	var calls []workflowCall
	call := func(name string, value any, err error) {
		calls = append(calls, workflowCall{name: name, answer: workflowAnswer(value, err)})
	}
	orders, shipments := db.GetOrderRepository(), db.GetShipmentRepository()
	all := OrderFields{}.All()

	order, err := orders.CreateOne(ctx, &OrderCreate{Id: workflowKeyPtr(1), Title: "Desk"})
	call("createOrder", order, err)
	order, err = orders.CreateOne(ctx, &OrderCreate{Id: workflowKeyPtr(2), Title: "Lamp"})
	call("createSecondOrder", order, err)
	order, err = orders.CreateOne(ctx, &OrderCreate{Id: workflowKeyPtr(1), Title: "Again"})
	call("createDuplicateOrder", order, err)
	shipment, err := shipments.CreateOne(ctx, &ShipmentCreate{Id: workflowKeyPtr(11), Reference: "S-1"})
	call("createShipment", shipment, err)
	order, err = orders.GetOne(ctx, workflowKey(1), nil)
	call("getOrder", order, err)
	moved, err := orders.Status().Transition(ctx, workflowKey(1), OrderStatusTransitionParams{To: OrderStatusStatePaid})
	call("payOrder", moved, err)
	moved, err = orders.Status().Transition(ctx, workflowKey(1), OrderStatusTransitionParams{To: OrderStatusStateDraft})
	call("unpayOrder", moved, err)
	moved, err = orders.Status().Transition(ctx, workflowKey(1), OrderStatusTransitionParams{To: OrderStatusStateShipped})
	call("shipWithoutPermission", moved, err)
	moved, err = orders.Status().Transition(shipper, workflowKey(1), OrderStatusTransitionParams{To: OrderStatusStateShipped})
	call("shipWithPermission", moved, err)
	moved, err = orders.Status().Transition(ctx, workflowKey(99), OrderStatusTransitionParams{To: OrderStatusStatePaid})
	call("transitionNoOrder", moved, err)
	paid := OrderStatusStatePaid
	moved, err = orders.Status().Transition(ctx, workflowKey(2), OrderStatusTransitionParams{From: &paid, To: OrderStatusStateShipped})
	call("transitionStale", moved, err)
	payment, err := shipments.PaymentStatus().Transition(ctx, workflowKey(11), ShipmentPaymentStatusTransitionParams{To: ShipmentPaymentStatusStatePaid})
	call("payShipment", payment, err)
	order, err = orders.GetOne(ctx, workflowKey(1), &OrderGetOptions{Fields: all})
	call("getShippedOrder", order, err)
	renamed := "Standing desk"
	order, err = orders.UpdateOne(ctx, workflowKey(1), &OrderUpdate{Title: &renamed})
	call("updateOrder", order, err)
	order, err = orders.UpdateOne(ctx, workflowKey(1), &OrderUpdate{})
	call("updateNothing", order, err)
	order, err = orders.UpdateOne(ctx, workflowKey(99), &OrderUpdate{Title: &renamed})
	call("updateNoOrder", order, err)
	found, total, err := orders.FindMany(ctx, &OrderFilter{Status: &StringFilter{In: []string{"shipped", "draft"}}}, &OrderFindOptions{Fields: all, OrderBy: []OrderOrderBy{{Field: OrderOrderById}}})
	call("findOrders", map[string]any{"rows": found, "total": total}, err)
	n, err := orders.UpdateMany(ctx, &OrderFilter{Status: &StringFilter{Eq: func() *string { s := "draft"; return &s }()}}, &OrderUpdate{Title: &renamed})
	call("updateOrders", n, err)
	created, err := orders.CreateMany(ctx, []*OrderCreate{{Id: workflowKeyPtr(3), Title: "Chair"}, {Id: workflowKeyPtr(4), Title: "Rug"}})
	call("createOrders", created, err)
	call("deleteOrder", nil, orders.DeleteOne(ctx, workflowKey(2)))
	call("deleteOrderAgain", nil, orders.DeleteOne(ctx, workflowKey(2)))
	order, err = orders.GetOne(ctx, workflowKey(2), nil)
	call("getDeletedOrder", order, err)
	err = db.Transaction(ctx, func(tx TxInterface) error {
		if _, err := tx.GetOrderRepository().CreateOne(ctx, &OrderCreate{Id: workflowKeyPtr(5), Title: "Shelf"}); err != nil {
			return err
		}
		_, err := tx.GetOrderRepository().Status().Transition(ctx, workflowKey(5), OrderStatusTransitionParams{To: OrderStatusStatePaid})
		return err
	})
	call("transaction", nil, err)
	err = db.Transaction(ctx, func(tx TxInterface) error {
		if _, err := tx.GetOrderRepository().CreateOne(ctx, &OrderCreate{Id: workflowKeyPtr(6), Title: "Stool"}); err != nil {
			return err
		}
		return errors.New("rolled back")
	})
	call("transactionRolledBack", nil, err)
	found, total, err = orders.FindMany(ctx, nil, &OrderFindOptions{Fields: all, OrderBy: []OrderOrderBy{{Field: OrderOrderById}}})
	call("ordersAtTheEnd", map[string]any{"rows": found, "total": total}, err)
	return calls
}

// TestWorkflowTablesOnSQLiteAndPostgres runs the script on both dialects
// and requires the same answer of each call.
func TestWorkflowTablesOnSQLiteAndPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to hold the workflow tables on SQLite to Postgres")
	}
	ctx := context.Background()
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("sqlite_workflow_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatal(err)
	}
	postgres, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	defer postgres.Close()

	sqliteDDL, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", SQLiteDSN(filepath.Join(t.TempDir(), "workflow.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(sqliteDDL)); err != nil {
		t.Fatalf("the SQLite DDL: %v", err)
	}
	lite, err := OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer lite.Close()

	onPostgres, onSQLite := workflowScript(t, postgres), workflowScript(t, lite)
	if len(onPostgres) != len(onSQLite) {
		t.Fatalf("Postgres answered %d calls and SQLite %d", len(onPostgres), len(onSQLite))
	}
	refusals := 0
	for i, pg := range onPostgres {
		lt := onSQLite[i]
		if !reflect.DeepEqual(pg.answer, lt.answer) {
			pgJSON, _ := json.MarshalIndent(pg.answer, "", "  ")
			ltJSON, _ := json.MarshalIndent(lt.answer, "", "  ")
			t.Errorf("%s: the answers differ\nPostgres:\n%s\nSQLite:\n%s", pg.name, pgJSON, ltJSON)
		}
		if m, ok := pg.answer.(map[string]any); ok {
			if r, refused := m["refused"].(string); refused {
				refusals++
				if strings.HasPrefix(r, "error: ") && !strings.Contains(r, "rolled back") && !strings.Contains(r, "no fields to update") {
					t.Errorf("%s: Postgres refused with an error of no kind: %s", pg.name, r)
				}
			}
		}
	}
	t.Logf("SQLite and Postgres answered %d calls alike, %d of them refusals", len(onPostgres), refusals)
}
