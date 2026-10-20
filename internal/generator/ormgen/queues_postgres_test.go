package ormgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/typegen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

const queueFixture = "fixture-queue-db"

// queueGeoFixture holds DeliveryPlanned, a queue whose message holds
// locations, POINT columns, and durations, INTERVAL ones, which SQLite
// cannot store, so it is Postgres alone, apart from queueFixture, which
// writes its queues in both dialects.
const queueGeoFixture = "fixture-queue-geo-db"

// queueSchema is queueFixture with the queue of queueGeoFixture beside its
// own, so one ORM module, one TypeScript types package and one Rust types
// crate hold the queues of both.
func queueSchema(t *testing.T) *ir.Schema {
	t.Helper()
	schema, err := loader.LoadService(filepath.Join(fixturesDir, queueFixture))
	if err != nil {
		t.Fatalf("load %s: %v", queueFixture, err)
	}
	geo, err := loader.LoadService(filepath.Join(fixturesDir, queueGeoFixture))
	if err != nil {
		t.Fatalf("load %s: %v", queueGeoFixture, err)
	}
	for name, scalar := range geo.Scalars {
		if _, ok := schema.Scalars[name]; !ok {
			schema.Scalars[name] = scalar
		}
	}
	for name, td := range geo.Types {
		if _, clash := schema.Types[name]; clash {
			t.Fatalf("%s and %s both declare %s", queueFixture, queueGeoFixture, name)
		}
		schema.Types[name] = td
	}
	return schema
}

// TestGeneratedQueuesOnPostgres generates the Go types module and the ORM
// module for fixture-queue-db, whose queue OrderPlaced holds a field of
// every kind a column takes, then builds, vets and tests the ORM (D53). Its
// generated test, against the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, enqueues in a transaction that
// rolls back and in one that commits, claims and reads each field back,
// claims with two pools at once, extends, completes, fails until the
// message is dead, releases, takes back a claim that expired, and marks
// dead a message whose last attempt's claim expired; and it doubles a
// failure's backoff up to the maximum, prunes the done messages past the
// retention, and deletes a message of a queue that keeps none as it
// completes it. A second generated test enqueues and claims DeliveryPlanned
// of fixture-queue-geo-db, whose locations are POINT columns and whose
// durations are INTERVAL ones, and reads the columns as Postgres holds
// them. It skips without the database.
func TestGeneratedQueuesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	paths := testpaths.Local(t)
	schema := queueSchema(t)
	fixedClock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	typesModule := "example.com/schemas/types/go/" + queueFixture
	tempRoot := t.TempDir()
	typesDir := filepath.Join(tempRoot, "types", "go", queueFixture)
	ormDir := filepath.Join(tempRoot, "orm", queueFixture)

	typesOutput, err := typegen.Generate(schema, typegen.Options{SchemaName: queueFixture, ModulePath: typesModule, Clock: fixedClock})
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
		SchemaName:  queueFixture,
		ModulePath:  "example.com/schemas/orm/" + queueFixture,
		TypesModule: typesModule,
		Clock:       fixedClock,
	})
	if err != nil {
		t.Fatalf("generate orm: %v", err)
	}
	if err := SetReplacePaths(ormOutput, paths, ormDir); err != nil {
		t.Fatalf("set orm replace paths: %v", err)
	}
	if err := WriteORM(ormOutput, ormDir); err != nil {
		t.Fatalf("write orm: %v", err)
	}
	ddl, err := sqlgen.Generate(schema, sqlgen.Options{SchemaName: queueFixture})
	if err != nil {
		t.Fatalf("generate ddl: %v", err)
	}
	ddlDir := t.TempDir()
	if err := sqlgen.WriteDDL(ddl, ddlDir); err != nil {
		t.Fatalf("write ddl: %v", err)
	}
	out := runGeneratedORMModule(t, ormDir, filepath.Join(ddlDir, "create.sql"), "queues_test.go", queuesORMTest)
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") != "" {
		for _, test := range []string{"TestQueueOnPostgres", "TestQueueBackoffGrowsAndPruneRemovesDoneMessages", "TestLocationsAndDurationsOnPostgres"} {
			if !strings.Contains(out, "--- PASS: "+test+" ") {
				t.Errorf("the generated %s did not pass", test)
			}
		}
	}
}

const queuesORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	scalars "github.com/parable-work/superscalar/go"
	types "example.com/schemas/types/go/fixture-queue-db"
)

// openQueueDatabase applies create.sql in a schema of the test's own.
func openQueueDatabase(t *testing.T) (*Database, *pgxpool.Pool, func() *pgxpool.Pool) {
	t.Helper()
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated queues against Postgres")
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
	t.Cleanup(base.Close)
	schema := fmt.Sprintf("queues_orm_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	t.Cleanup(func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	open := func() *pgxpool.Pool {
		cfg, err := pgxpool.ParseConfig(dsn)
		if err != nil {
			t.Fatalf("parse database URL: %v", err)
		}
		cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
		pool, err := pgxpool.NewWithConfig(ctx, cfg)
		if err != nil {
			t.Fatalf("connect schema pool: %v", err)
		}
		t.Cleanup(pool.Close)
		return pool
	}
	pool := open()
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		t.Fatalf("connect generated orm: %v", err)
	}
	return db, pool, open
}

// state reads a message's state, attempts and last error.
func state(t *testing.T, pool *pgxpool.Pool, id string) (string, int, string) {
	t.Helper()
	var s, lastError string
	var attempts int
	if err := pool.QueryRow(context.Background(), "SELECT state, attempts, COALESCE(last_error, '') FROM order_placed_queue WHERE id = $1::uuid", id).Scan(&s, &attempts, &lastError); err != nil {
		t.Fatalf("read message %s: %v", id, err)
	}
	return s, attempts, lastError
}

// due makes every ready message due now.
func due(t *testing.T, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), "UPDATE order_placed_queue SET due_at = now() - interval '1 second' WHERE state = 'ready'"); err != nil {
		t.Fatal(err)
	}
}

func TestQueueOnPostgres(t *testing.T) {
	db, pool, open := openQueueDatabase(t)
	ctx := context.Background()
	queue := db.OrderPlacedQueue()
	if queue == nil || OrderPlacedLease != 2*time.Minute {
		t.Fatalf("OrderPlacedLease = %s, want the fixture's 2m", OrderPlacedLease)
	}

	note := "leave it by the door"
	placed := scalars.TemporalDateTime(time.Date(2026, 10, 9, 12, 30, 15, 250000000, time.UTC))
	id, _ := scalars.ParseUUID("6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e")
	msg := &types.OrderPlaced{
		OrderId:  id,
		PlacedAt: placed,
		Priority: types.Priority_High,
		Quantity: 3,
		Note:     note,
		ShipTo:   types.Address{Line1: "1 Main St", City: "Springfield"},
		Tags:     []string{"gift", "fragile"},
	}

	// A message enqueued in a transaction that rolls back is never due.
	rolledBack := errors.New("roll back")
	if err := db.Transaction(ctx, func(tx TxInterface) error {
		if err := EnqueueOrderPlaced(ctx, tx, msg); err != nil {
			return err
		}
		return rolledBack
	}); !errors.Is(err, rolledBack) {
		t.Fatalf("Transaction = %v, want the rollback", err)
	}
	if claims, err := queue.Claim(ctx, 10, time.Minute); err != nil || len(claims) != 0 {
		t.Fatalf("Claim after a rollback = %+v, %v, want none", claims, err)
	}

	// One that commits is, and reads back field for field. A message
	// that leaves a field with a default out takes the default.
	if err := db.Transaction(ctx, func(tx TxInterface) error { return EnqueueOrderPlaced(ctx, tx, msg) }); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	if err := db.Transaction(ctx, func(tx TxInterface) error {
		return EnqueueOrderPlaced(ctx, tx, &types.OrderPlaced{OrderId: id, PlacedAt: placed, ShipTo: types.Address{Line1: "2 Elm St", City: "Shelbyville"}})
	}); err != nil {
		t.Fatalf("enqueue: %v", err)
	}
	claims, err := queue.Claim(ctx, 1, time.Minute)
	if err != nil || len(claims) != 1 {
		t.Fatalf("Claim = %+v, %v, want one", claims, err)
	}
	first := claims[0]
	if first.Attempt != 1 || first.ID == "" || first.Token == "" {
		t.Fatalf("claim = %+v, want attempt 1 with an ID and a token", first)
	}
	got := first.Message
	if got.OrderId != msg.OrderId || !time.Time(got.PlacedAt).Equal(time.Time(msg.PlacedAt)) || got.Priority != msg.Priority ||
		got.Quantity != msg.Quantity || got.Note != note || got.ShipTo != msg.ShipTo || !reflect.DeepEqual(got.Tags, msg.Tags) {
		t.Fatalf("claimed message = %+v, want %+v", got, msg)
	}

	// A second pool claims the other message and not the claimed one.
	other := db
	if second, err := ConnectWithPool(open()); err == nil {
		other = second
	}
	rest, err := other.OrderPlacedQueue().Claim(ctx, 10, time.Minute)
	if err != nil || len(rest) != 1 || rest[0].ID == first.ID {
		t.Fatalf("second Claim = %+v, %v, want the other message alone", rest, err)
	}
	if rest[0].Message.Priority != types.Priority_Low || rest[0].Message.Note != "" || len(rest[0].Message.Tags) != 0 {
		t.Fatalf("defaulted message = %+v, want priority low, no note and no tags", rest[0].Message)
	}
	if more, err := queue.Claim(ctx, 10, time.Minute); err != nil || len(more) != 0 {
		t.Fatalf("Claim of a claimed queue = %+v, %v, want none", more, err)
	}

	// Extend, then complete; a second complete has lost its claim.
	if err := queue.Extend(ctx, first.ID, first.Token, 5*time.Minute); err != nil {
		t.Fatalf("Extend: %v", err)
	}
	if err := queue.Complete(ctx, first.ID, first.Token); err != nil {
		t.Fatalf("Complete: %v", err)
	}
	if s, _, _ := state(t, pool, first.ID); s != "done" {
		t.Fatalf("completed message is %s, want done", s)
	}
	if err := queue.Complete(ctx, first.ID, first.Token); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("second Complete = %v, want ErrClaimLost", err)
	}

	// Release gives the other message back without counting the attempt.
	second := rest[0]
	if err := queue.Release(ctx, second.ID, second.Token); err != nil {
		t.Fatalf("Release: %v", err)
	}
	if s, attempts, _ := state(t, pool, second.ID); s != "ready" || attempts != 0 {
		t.Fatalf("released message is %s after %d attempts, want ready after 0", s, attempts)
	}

	// Failing it makes it wait its backoff, then due again, until its
	// retries, three, are spent: the fourth failure leaves it dead.
	for attempt := 1; attempt <= 4; attempt++ {
		due(t, pool)
		claims, err := queue.Claim(ctx, 10, time.Minute)
		if err != nil || len(claims) != 1 || claims[0].Attempt != attempt {
			t.Fatalf("Claim of attempt %d = %+v, %v", attempt, claims, err)
		}
		dead, err := queue.Fail(ctx, claims[0].ID, claims[0].Token, fmt.Sprintf("try %d failed", attempt))
		if err != nil {
			t.Fatalf("Fail: %v", err)
		}
		s, _, lastError := state(t, pool, second.ID)
		if dead != (attempt == 4) || (attempt < 4 && s != "ready") || (attempt == 4 && s != "dead") || lastError != fmt.Sprintf("try %d failed", attempt) {
			t.Fatalf("after failure %d: dead %t, state %s, last error %q", attempt, dead, s, lastError)
		}
		if attempt < 4 {
			if waiting, err := queue.Claim(ctx, 10, time.Minute); err != nil || len(waiting) != 0 {
				t.Fatalf("Claim during the backoff = %+v, %v, want none", waiting, err)
			}
		}
	}

	// A claim that expires returns its message: the next claim takes it,
	// as its next attempt, under a new token, and the old one is lost.
	if err := db.Transaction(ctx, func(tx TxInterface) error { return EnqueueOrderPlaced(ctx, tx, msg) }); err != nil {
		t.Fatal(err)
	}
	expiring, err := queue.Claim(ctx, 10, time.Second)
	if err != nil || len(expiring) != 1 {
		t.Fatalf("Claim = %+v, %v", expiring, err)
	}
	if _, err := pool.Exec(ctx, "UPDATE order_placed_queue SET claim_expires_at = now() - interval '1 second' WHERE id = $1::uuid", expiring[0].ID); err != nil {
		t.Fatal(err)
	}
	retaken, err := queue.Claim(ctx, 10, time.Minute)
	if err != nil || len(retaken) != 1 || retaken[0].ID != expiring[0].ID || retaken[0].Attempt != 2 || retaken[0].Token == expiring[0].Token {
		t.Fatalf("Claim after the claim expired = %+v, %v, want attempt 2 of %s", retaken, err, expiring[0].ID)
	}
	if err := queue.Complete(ctx, expiring[0].ID, expiring[0].Token); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("Complete under the expired token = %v, want ErrClaimLost", err)
	}

	// A message whose last attempt's claim expires is dead, not claimed
	// again.
	if _, err := pool.Exec(ctx, "UPDATE order_placed_queue SET attempts = 4, claim_expires_at = now() - interval '1 second' WHERE id = $1::uuid", retaken[0].ID); err != nil {
		t.Fatal(err)
	}
	if again, err := queue.Claim(ctx, 10, time.Minute); err != nil || len(again) != 0 {
		t.Fatalf("Claim of a message past its last attempt = %+v, %v, want none", again, err)
	}
	if s, _, lastError := state(t, pool, retaken[0].ID); s != "dead" || !strings.Contains(lastError, "claim of its last attempt expired") {
		t.Fatalf("message past its last attempt is %s (%q), want dead", s, lastError)
	}
}

// dueIn is the seconds until a message is due.
func dueIn(t *testing.T, pool *pgxpool.Pool, id string) float64 {
	t.Helper()
	var seconds float64
	if err := pool.QueryRow(context.Background(), "SELECT extract(epoch FROM due_at - now())::float8 FROM order_placed_queue WHERE id = $1::uuid", id).Scan(&seconds); err != nil {
		t.Fatal(err)
	}
	return seconds
}

// TestQueueBackoffGrowsAndPruneRemovesDoneMessages: each failure of
// OrderPlaced doubles its minute's backoff, up to its 200s maximum; a prune
// deletes the messages done past its hour's retention, a batch at a time,
// and none done within it and no dead one; and Ping, which keeps no done
// message, deletes one as it completes it (D68).
func TestQueueBackoffGrowsAndPruneRemovesDoneMessages(t *testing.T) {
	db, pool, _ := openQueueDatabase(t)
	ctx := context.Background()
	queue := db.OrderPlacedQueue()
	id, _ := scalars.ParseUUID("6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e")
	msg := &types.OrderPlaced{OrderId: id, ShipTo: types.Address{Line1: "1 Main St", City: "Springfield"}}
	enqueue := func() {
		t.Helper()
		if err := db.Transaction(ctx, func(tx TxInterface) error { return EnqueueOrderPlaced(ctx, tx, msg) }); err != nil {
			t.Fatal(err)
		}
	}

	enqueue()
	for _, step := range []struct {
		attempt int
		wait    float64
	}{{1, 60}, {2, 120}, {3, 200}} {
		due(t, pool)
		claims, err := queue.Claim(ctx, 1, time.Minute)
		if err != nil || len(claims) != 1 || claims[0].Attempt != step.attempt {
			t.Fatalf("Claim of attempt %d = %+v, %v", step.attempt, claims, err)
		}
		if dead, err := queue.Fail(ctx, claims[0].ID, claims[0].Token, "later"); err != nil || dead {
			t.Fatalf("Fail of attempt %d = %t, %v", step.attempt, dead, err)
		}
		if left := dueIn(t, pool, claims[0].ID); left > step.wait || left < step.wait-5 {
			t.Fatalf("attempt %d is due in %.1fs, want about %.0fs", step.attempt, left, step.wait)
		}
	}

	if _, err := pool.Exec(ctx, "DELETE FROM order_placed_queue"); err != nil {
		t.Fatal(err)
	}
	for range 4 {
		enqueue()
	}
	claims, err := queue.Claim(ctx, 4, time.Minute)
	if err != nil || len(claims) != 4 {
		t.Fatalf("Claim = %+v, %v, want four", claims, err)
	}
	for _, c := range claims[:2] {
		if _, err := pool.Exec(ctx, "UPDATE order_placed_queue SET state = 'done', updated_at = now() - interval '2 hours' WHERE id = $1::uuid", c.ID); err != nil {
			t.Fatal(err)
		}
	}
	if err := queue.Complete(ctx, claims[2].ID, claims[2].Token); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, "UPDATE order_placed_queue SET state = 'dead', updated_at = now() - interval '30 days' WHERE id = $1::uuid", claims[3].ID); err != nil {
		t.Fatal(err)
	}
	for i, want := range []int{1, 1, 0} {
		if n, err := queue.Prune(ctx, 1); err != nil || n != want {
			t.Fatalf("Prune %d = %d, %v, want %d", i, n, err, want)
		}
	}
	if s, _, _ := state(t, pool, claims[2].ID); s != "done" {
		t.Fatalf("the message done within the retention is %s", s)
	}
	if s, _, _ := state(t, pool, claims[3].ID); s != "dead" {
		t.Fatalf("the dead message is %s", s)
	}

	pings := db.PingQueue()
	if err := db.Transaction(ctx, func(tx TxInterface) error { return EnqueuePing(ctx, tx, &types.Ping{}) }); err != nil {
		t.Fatal(err)
	}
	ping, err := pings.Claim(ctx, 1, time.Minute)
	if err != nil || len(ping) != 1 {
		t.Fatalf("Ping claim = %+v, %v", ping, err)
	}
	if err := pings.Complete(ctx, ping[0].ID, ping[0].Token); err != nil {
		t.Fatal(err)
	}
	var left int
	if err := pool.QueryRow(ctx, "SELECT count(*) FROM ping_queue").Scan(&left); err != nil || left != 0 {
		t.Fatalf("ping_queue holds %d messages after the complete, %v; want none", left, err)
	}
	if err := pings.Complete(ctx, ping[0].ID, ping[0].Token); !errors.Is(err, ErrClaimLost) {
		t.Fatalf("a second Complete of a deleted ping = %v, want ErrClaimLost", err)
	}
}

// TestLocationsAndDurationsOnPostgres: a location is a POINT whose x is the
// longitude and y the latitude, and a duration an INTERVAL of its
// microseconds, the sign of the whole duration kept and a nanosecond
// remainder truncated toward zero. Each reads back as it was written, an
// unset optional one as NULL and back as nil, and an interval written by
// hand with days and months reads as their hours, a day 24 and a month
// 30 days.
func TestLocationsAndDurationsOnPostgres(t *testing.T) {
	db, pool, _ := openQueueDatabase(t)
	ctx := context.Background()
	queue := db.DeliveryPlannedQueue()
	id, _ := scalars.ParseUUID("6f1c2f2e-7d1a-4b4a-9d43-3c7a1b2c3d4e")
	meet := types.GeoLocation{Lat: -33.8688, Lon: 151.2093}
	grace := types.TemporalDuration(-(time.Minute + 30*time.Second + 250*time.Millisecond))
	for _, msg := range []*types.DeliveryPlanned{
		{OrderId: id, DropAt: types.GeoLocation{Lat: 37.7749, Lon: -122.4194}, Wait: types.TemporalDuration(26*time.Hour + 1500), MeetAt: &meet, Grace: &grace},
		{OrderId: id, Wait: types.TemporalDuration(-1500)},
	} {
		if err := db.Transaction(ctx, func(tx TxInterface) error { return EnqueueDeliveryPlanned(ctx, tx, msg) }); err != nil {
			t.Fatalf("enqueue: %v", err)
		}
	}
	type row struct {
		x, y        float64
		wait        string
		meet, grace *string
	}
	var rows []row
	read, err := pool.Query(ctx, "SELECT (drop_at)[0], (drop_at)[1], wait::text, meet_at::text, grace::text FROM delivery_planned_queue ORDER BY wait DESC")
	if err != nil {
		t.Fatal(err)
	}
	for read.Next() {
		var r row
		if err := read.Scan(&r.x, &r.y, &r.wait, &r.meet, &r.grace); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, r)
	}
	if err := read.Err(); err != nil {
		t.Fatal(err)
	}
	text := func(s *string) string {
		if s == nil {
			return "NULL"
		}
		return *s
	}
	if len(rows) != 2 || rows[0].x != -122.4194 || rows[0].y != 37.7749 || rows[0].wait != "26:00:00.000001" || text(rows[0].meet) != "(151.2093,-33.8688)" || text(rows[0].grace) != "-00:01:30.25" ||
		rows[1].x != 0 || rows[1].y != 0 || rows[1].wait != "-00:00:00.000001" || rows[1].meet != nil || rows[1].grace != nil {
		t.Fatalf("the columns hold %+v", rows)
	}

	claims, err := queue.Claim(ctx, 10, time.Minute)
	if err != nil || len(claims) != 2 {
		t.Fatalf("Claim = %+v, %v, want both", claims, err)
	}
	for _, c := range claims {
		m := c.Message
		switch {
		case m.MeetAt != nil:
			if m.DropAt != (types.GeoLocation{Lat: 37.7749, Lon: -122.4194}) || m.Wait != types.TemporalDuration(26*time.Hour+time.Microsecond) || *m.MeetAt != meet || m.Grace == nil || *m.Grace != grace {
				t.Errorf("the full message reads as %+v", m)
			}
		default:
			if m.DropAt != (types.GeoLocation{}) || m.Wait != types.TemporalDuration(-time.Microsecond) || m.Grace != nil {
				t.Errorf("the message that leaves its options unset reads as %+v", m)
			}
		}
		if err := queue.Release(ctx, c.ID, c.Token); err != nil {
			t.Fatal(err)
		}
	}

	if _, err := pool.Exec(ctx, "UPDATE delivery_planned_queue SET wait = '1 mon 1 day -01:00:00', grace = '-2 days'"); err != nil {
		t.Fatal(err)
	}
	claims, err = queue.Claim(ctx, 10, time.Minute)
	if err != nil || len(claims) != 2 {
		t.Fatalf("Claim = %+v, %v, want both", claims, err)
	}
	for _, c := range claims {
		if c.Message.Wait != types.TemporalDuration(743*time.Hour) || c.Message.Grace == nil || *c.Message.Grace != types.TemporalDuration(-48*time.Hour) {
			t.Errorf("a message with days and months reads as %+v", c.Message)
		}
	}
}
`
