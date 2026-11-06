package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// scheduleORMCases are the cases the generated module's test runs over
// fixture-schedules-db, by name: RunEvent's, which a behavior worker's
// handler of its event queue calls (D104), and the event a wake-up's fire
// writes, which it hands on.
var scheduleORMCases = []string{
	"a wake-up's fire writes its run event to the event queue in the transaction that fires it",
	"RunEvent spends a claimed event and commits the handler's writes with it",
	"RunEvent whose handler fails rolls the spend and the handler's writes back and returns its error",
	"RunEvent on a lost claim runs nothing",
	"RunEvent without a claim spends nothing and runs the handler",
	"a deleted problem's events stay in its event queue",
}

// TestScheduleTablesOnPostgres holds the generated Go ORM of a database
// whose table implements Schedules to the rules module's adapter on
// Postgres (D104), as TestTimersTablesOnPostgres holds one of Timers: a
// fire writes its event in its own transaction, and RunEvent spends a
// claimed message of the event queue and runs the handler in one
// transaction, a handler that fails rolling both back. It loads
// fixture-schedules-db with the core's registry, generates its Go types,
// its ORM and its DDL into a temporary tree, and builds, vets and tests the
// ORM module with scheduleORMTest, whose every case (scheduleORMCases) must
// pass on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The
// module links the core's static archive, which scripts/rules-archive.sh
// stages; the test skips without it, and in -short mode.
func TestScheduleTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Schedules ORM's end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated Schedules ORM against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, schedulesFixture)
	if _, err := os.Stat(filepath.Join(ormDir, "repository_problem_schedules_schedules.go")); err == nil {
		t.Error("the generated ORM holds a repository of Schedules' sub-table, which has none")
	}
	if err := os.WriteFile(filepath.Join(ormDir, "schedules_test.go"), []byte(scheduleORMTest), 0o644); err != nil {
		t.Fatalf("write the Schedules test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range scheduleORMCases {
		if want := "--- PASS: TestScheduleTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// scheduleORMTest is the generated module's test: Problem's events through
// the generated repository, in a schema of its own, read back past it with
// SQL.
const scheduleORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-schedules-db"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
)

func TestScheduleTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the Schedules table against Postgres")
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
	schema := fmt.Sprintf("ormgen_schedules_%d", time.Now().UnixNano())
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

	// The behavior worker's caller, who holds the permission Schedules'
	// operations need.
	worker := WithBehaviorCaller(ctx, rules.Caller{Subject: "fixture-schedules-api/ProblemWakeUps", Permissions: []string{"problems.schedule"}})

	create := func(t *testing.T) types.IdentityUUID {
		t.Helper()
		row, err := db.Problem.CreateOne(ctx, &ProblemCreate{Title: "Flaky test"})
		if err != nil {
			t.Fatalf("create a problem: %v", err)
		}
		return *row.Id
	}
	title := func(t *testing.T, id types.IdentityUUID) string {
		t.Helper()
		var title string
		if err := pool.QueryRow(ctx, "SELECT title FROM problem WHERE id = $1", id.ToUUID().String()).Scan(&title); err != nil {
			t.Fatal(err)
		}
		return title
	}
	queued := func(t *testing.T, id types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM problem_event_queue WHERE row_key = $1", id.ToUUID().String()).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// enqueue writes an event of the problem past the core, as a fire
	// writes one, and claims it as a worker would, giving back any other
	// message the claim took.
	enqueue := func(t *testing.T, id types.IdentityUUID) ProblemEventClaim {
		t.Helper()
		if _, err := pool.Exec(ctx, "INSERT INTO problem_event_queue (row_key, use_id, kind, at, payload) VALUES ($1, 'Schedules', 'run', 5000, '{\"name\": \"nudge\", \"run\": 1}')", id.ToUUID().String()); err != nil {
			t.Fatal(err)
		}
		claims, err := db.ProblemEventQueue().Claim(ctx, 10, time.Minute)
		if err != nil {
			t.Fatalf("claim: %v", err)
		}
		var found *ProblemEventClaim
		for i := range claims {
			if claims[i].Message.RowKey == id {
				found = &claims[i]
				continue
			}
			if err := db.ProblemEventQueue().Release(ctx, claims[i].ID, claims[i].Token); err != nil {
				t.Fatal(err)
			}
		}
		if found == nil {
			t.Fatal("no event of the problem was claimed")
		}
		return *found
	}
	// retitle is a handler that writes the problem's title through the
	// repository of the event's transaction.
	retitle := func(id types.IdentityUUID, to string) func(context.Context, TxInterface) error {
		return func(ctx context.Context, tx TxInterface) error {
			_, err := tx.GetProblemRepository().UpdateOne(ctx, id, &ProblemUpdate{Title: &to})
			return err
		}
	}

	t.Run("a wake-up's fire writes its run event to the event queue in the transaction that fires it", func(t *testing.T) {
		id := create(t)
		after := int64(600000)
		set, err := db.Problem.Schedules().SetSchedule(worker, id, ProblemSchedulesSetScheduleParams{Name: "nudge", After: &after})
		if err != nil {
			t.Fatalf("setSchedule: %v", err)
		}
		if queued(t, id) != 0 {
			t.Fatal("a setSchedule wrote an event")
		}
		if _, err := pool.Exec(ctx, "UPDATE problem_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = 'nudge'", id.ToUUID().String()); err != nil {
			t.Fatal(err)
		}
		claims, err := db.ProblemDeadlineQueue().Claim(ctx, 10, time.Minute)
		if err != nil || len(claims) != 1 {
			t.Fatalf("claimed %d deadlines, %v; want nudge's", len(claims), err)
		}
		run, err := db.Problem.RunDeadline(worker, claims[0].Message, &rules.Claim{ID: claims[0].ID, Token: claims[0].Token})
		if err != nil || !run.Ran {
			t.Fatalf("the fire: %+v, %v", run, err)
		}
		var useID, kind string
		var at int64
		var payload []byte
		if err := pool.QueryRow(ctx, "SELECT use_id, kind, at, payload FROM problem_event_queue WHERE row_key = $1", id.ToUUID().String()).Scan(&useID, &kind, &at, &payload); err != nil {
			t.Fatalf("read the event: %v", err)
		}
		var said map[string]any
		if err := json.Unmarshal(payload, &said); err != nil {
			t.Fatal(err)
		}
		nextAt := int64(*set.NextAt)
		if useID != "Schedules" || kind != "run" || at != nextAt || said["name"] != "nudge" || said["run"] != float64(1) || said["attempt"] != float64(1) || said["kind"] != "after" || said["dueAt"] != float64(nextAt) || said["firedAt"] != float64(nextAt) {
			t.Fatalf("the event is %s %s at %d saying %s, want Schedules' run at %d, nudge's run 1", useID, kind, at, payload, nextAt)
		}
	})

	t.Run("RunEvent spends a claimed event and commits the handler's writes with it", func(t *testing.T) {
		id := create(t)
		c := enqueue(t, id)
		ran := 0
		run, err := db.Problem.RunEvent(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token}, func(ctx context.Context, tx TxInterface) error {
			ran++
			return retitle(id, "told")(ctx, tx)
		})
		if err != nil || !run.Ran || ran != 1 {
			t.Fatalf("RunEvent = %+v, %v, the handler ran %d times; want it run once", run, err, ran)
		}
		if got := title(t, id); got != "told" || queued(t, id) != 0 {
			t.Fatalf("the title is %q and %d events are queued, want told and none", got, queued(t, id))
		}
	})

	t.Run("RunEvent whose handler fails rolls the spend and the handler's writes back and returns its error", func(t *testing.T) {
		id := create(t)
		c := enqueue(t, id)
		failed := errors.New("the agent was not told")
		_, err := db.Problem.RunEvent(worker, c.Message, &rules.Claim{ID: c.ID, Token: c.Token}, func(ctx context.Context, tx TxInterface) error {
			if err := retitle(id, "told")(ctx, tx); err != nil {
				return err
			}
			return failed
		})
		if !errors.Is(err, failed) {
			t.Fatalf("RunEvent = %v, want the handler's error", err)
		}
		var state string
		if err := pool.QueryRow(ctx, "SELECT state FROM problem_event_queue WHERE id = $1", c.ID).Scan(&state); err != nil || state != "claimed" {
			t.Fatalf("the message is %q (%v), want it still claimed, for the worker to fail", state, err)
		}
		if got := title(t, id); got != "Flaky test" {
			t.Fatalf("the title is %q, want the handler's write rolled back", got)
		}
	})

	t.Run("RunEvent on a lost claim runs nothing", func(t *testing.T) {
		id := create(t)
		c := enqueue(t, id)
		ran := false
		run, err := db.Problem.RunEvent(worker, c.Message, &rules.Claim{ID: c.ID, Token: "6c1d4a77-0a5e-4d43-8c6a-2a5c7e3d2003"}, func(context.Context, TxInterface) error {
			ran = true
			return nil
		})
		if err != nil || run.Ran || run.Why != rules.WhyClaimLost || ran || queued(t, id) != 1 {
			t.Fatalf("RunEvent on another token = %+v, %v, ran %t; want claim lost, nothing run, the message kept", run, err, ran)
		}
	})

	t.Run("RunEvent without a claim spends nothing and runs the handler", func(t *testing.T) {
		id := create(t)
		c := enqueue(t, id)
		run, err := db.Problem.RunEvent(worker, c.Message, nil, retitle(id, "heard"))
		if err != nil || !run.Ran || title(t, id) != "heard" || queued(t, id) != 1 {
			t.Fatalf("RunEvent without a claim = %+v, %v, title %q, %d queued; want it run, the message kept", run, err, title(t, id), queued(t, id))
		}
	})

	t.Run("a deleted problem's events stay in its event queue", func(t *testing.T) {
		id := create(t)
		enqueue(t, id)
		if err := db.Problem.DeleteOne(ctx, id); err != nil {
			t.Fatalf("delete: %v", err)
		}
		if got := queued(t, id); got != 1 {
			t.Fatalf("%d events of the deleted problem are queued, want its one", got)
		}
	})
}
`
