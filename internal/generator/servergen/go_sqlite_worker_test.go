package servergen_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// The Go server and workers over a database whose DB service lists sqlite
// first (#717): every entrypoint opens the SQLite file, the runtime's
// wiring takes the ORM's SQLiteDatabase, and each worker claims its queue
// there.

// goSQLiteWiringGoldenRoot holds the entrypoints of goWiringInput over a
// sqlite-first database.
const goSQLiteWiringGoldenRoot = "testdata/golden/wiring/go-sqlite"

// goSQLiteBehaviorWorkerGoldenRoot holds TicketTimers' entrypoint over a
// sqlite-first fixture-timers-db.
const goSQLiteBehaviorWorkerGoldenRoot = "testdata/golden/behaviors/go-worker-sqlite"

// goSQLiteWiringInput is goWiringInput over a sqlite-first database, with
// a @worker whose queue the implementation handles beside the notifier's.
func goSQLiteWiringInput(repoRoot string) servergen.Input {
	in := goWiringInput(repoRoot)
	o := in.APIs[0].Output
	o.Deps.SQLite = true
	o.Deps.Wiring.SQLite = true
	o.Workers = append(o.Workers, apigen.WorkerInfo{Name: "Work", Method: "Work", Queue: "WorkItem", Concurrency: 2})
	return in
}

// TestGoServerAndWorkersOnSQLiteGolden: over a sqlite-first database the
// server, the worker of the notifier's queue and a @worker of the app's
// open the SQLite file, the server and the notifier's worker wire the
// runtime over it as over a pool, and the app's worker claims its queue
// with the ORM's New<Queue>SQLiteQueue, with no drain slot or next due
// time. Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoServerAndWorkersOnSQLiteGolden -update
func TestGoServerAndWorkersOnSQLiteGolden(t *testing.T) {
	repoRoot := testpaths.TempDir(t)
	in := goSQLiteWiringInput(repoRoot)
	s, err := servergen.Plan(in)
	if err != nil {
		t.Fatal(err)
	}
	if err := servergen.Write(s, in.Dir); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{"server/" + servergen.MainFile: filepath.Join(in.Dir, servergen.MainFile)}
	mains := map[string]string{}
	for _, class := range []string{"Notify", "Work"} {
		worker := goSQLiteWiringInput(repoRoot)
		worker.Server = "workflow-" + strings.ToLower(class)
		worker.Dir = filepath.Join(filepath.Dir(in.Dir), worker.Server)
		worker.Worker = &servergen.WorkerInput{Name: class}
		w, err := servergen.Plan(worker)
		if err != nil {
			t.Fatalf("worker %s: %v", class, err)
		}
		if err := servergen.Write(w, worker.Dir); err != nil {
			t.Fatal(err)
		}
		files[strings.ToLower(class)+"/"+servergen.MainFile] = filepath.Join(worker.Dir, servergen.MainFile)
		main, err := os.ReadFile(filepath.Join(worker.Dir, servergen.MainFile))
		if err != nil {
			t.Fatal(err)
		}
		mains[class] = string(main)
	}
	compareGoldens(t, goSQLiteWiringGoldenRoot, files)
	for class, want := range map[string][]string{
		"Notify": {"sqlitefile.Open(", "fixtureworkflowapi.Wire(", "fixtureworkflowapi.NotifyQueue("},
		"Work":   {"sqlitefile.Open(", "fixtureworkflowdborm.NewWorkItemSQLiteQueue(fixtureWorkflowDb.SQL())"},
	} {
		for _, w := range want {
			if !strings.Contains(mains[class], w) {
				t.Errorf("the worker %s names no %s:\n%s", class, w, mains[class])
			}
		}
		if strings.Contains(mains[class], "pgxpool") || strings.Contains(mains[class], "queue.Slot") {
			t.Errorf("the worker %s over SQLite connects a pool or takes a slot:\n%s", class, mains[class])
		}
	}
}

// TestGoBehaviorWorkerEntrypointOnSQLite: TicketTimers over a sqlite-first
// fixture-timers-db opens the SQLite file and runs the queues its API's
// TicketTimersQueues builds over the ORM's SQLiteDatabase, as on Postgres.
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestGoBehaviorWorkerEntrypointOnSQLite -update
func TestGoBehaviorWorkerEntrypointOnSQLite(t *testing.T) {
	o := goTimersAPI()
	o.Deps.SQLite = true
	main := planGoBehaviorWorker(t, o, goSQLiteBehaviorWorkerGoldenRoot, "timers-stack", "TicketTimers", "fixture-timers-db")
	for _, want := range []string{
		"fixtureTimersDbSQL, err := sqlitefile.Open(",
		"fixtureTimersDb, err := fixturetimersdborm.OpenSQLite(ctx, fixtureTimersDbSQL)",
		"queues := fixturetimersapi.TicketTimersQueues(fixtureTimersDb, logger)",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("main.go holds no %q:\n%s", want, main)
		}
	}
}
