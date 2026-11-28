package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	migrate "github.com/parable-work/superschematic/runtime/migrate/go"
	"github.com/parable-work/superschematic/runtime/migrate/go/internal/testdb"
)

// writeRecord writes runtime mode's record of a plan's model to a new
// directory and returns its path.
func writeRecord(t *testing.T, dir, service, model string) string {
	t.Helper()
	doc, err := json.Marshal(map[string]any{
		"version": migrate.RecordVersion, "service": service, "modelHash": model,
		"documents": []any{map[string]any{"name": service}}, "layout": map[string]any{"types": map[string]any{}},
		"versions": map[string]any{"Order": 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "record.json")
	if err := os.WriteFile(path, doc, 0o644); err != nil {
		t.Fatal(err)
	}
	return path
}

// TestApplyWritesTheRecordWithThePlan: apply --record writes runtime
// mode's record (D117) in the transaction that finishes the plan, beside
// the model it ends at; a record of another model is refused before
// anything runs.
func TestApplyWritesTheRecordWithThePlan(t *testing.T) {
	create := readFixture(t, migrate.SQLite, "01-create")
	db := filepath.Join(t.TempDir(), "app.db")
	stale := writeRecord(t, t.TempDir(), create.Service, "not-the-model")
	invoke(t, nil, "apply", "--plan", fixturePath(migrate.SQLite, "01-create"), "--record", stale, "--database-url", db).
		expect(t, exitFailed, "the record is of model not-the-model")
	if got := testdb.Strings(t, db, "SELECT count(*) FROM sqlite_master WHERE name = 'superschematic_schema_state'"); got[0] != "0" {
		t.Fatalf("a refused record ran the plan: %v", got)
	}

	record := writeRecord(t, t.TempDir(), create.Service, create.To)
	invoke(t, nil, "apply", "--plan", fixturePath(migrate.SQLite, "01-create"), "--record", record, "--database-url", db).
		expect(t, exitOK, "plan "+create.Hash+" applied")
	got := testdb.Strings(t, db, "SELECT service || ' ' || model_hash || ' ' || versions FROM "+migrate.RecordTable)
	if len(got) != 1 || got[0] != create.Service+" "+create.To+` {"Order":1}` {
		t.Fatalf("the record holds %v", got)
	}
	invoke(t, nil, "apply", "--plan", "x.json", "--record", "-", "--database-url", db).
		expect(t, exitUsage, "--record takes a file")
}

// TestJobWritesARuntimeRecordAndItsReads runs a runtime publish's job on
// Postgres: the plan, its record and the servers' privileges, which the
// transaction that finishes the plan gives with the record, and the reads
// of the runner's state and the record alone, which a later job takes back
// from a role it no longer lists.
func TestJobWritesARuntimeRecordAndItsReads(t *testing.T) {
	r := roles(t, "tasks-api", "old-api")
	api, old := r[0], r[1]
	dbURL := testdb.NewPostgres(t)
	create := readFixture(t, migrate.Postgres, "01-create")
	createAbs, err := filepath.Abs(fixturePath(migrate.Postgres, "01-create"))
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	job := &migrate.Job{Version: migrate.JobVersion, Phase: migrate.All, Databases: []*migrate.JobDatabase{{
		Service: create.Service, DatabaseURL: dbURL, Plan: createAbs,
		Record:     writeRecord(t, dir, create.Service, create.To),
		Privileges: &migrate.Privileges{ReadWrite: []string{api, old}, ReadState: true},
	}}}
	invoke(t, nil, "job", "--job", writeJob(t, job)).expect(t, exitOK, "plan "+create.Hash+" applied")
	if got := testdb.Strings(t, dbURL, "SELECT model_hash FROM "+migrate.RecordTable); len(got) != 1 || got[0] != create.To {
		t.Fatalf("the record holds %v", got)
	}
	for _, role := range []string{api, old} {
		if !can(t, dbURL, role, `public."order"`, "INSERT") {
			t.Errorf("%s cannot write order", role)
		}
		for _, table := range []string{"public.superschematic_schema_state", "public." + migrate.RecordTable} {
			if !can(t, dbURL, role, table, "SELECT") || can(t, dbURL, role, table, "INSERT") || can(t, dbURL, role, table, "UPDATE") {
				t.Errorf("%s holds other than SELECT alone on %s", role, table)
			}
		}
		if can(t, dbURL, role, "public.superschematic_migrations", "SELECT") {
			t.Errorf("%s reads the runner's log", role)
		}
	}
	// The server reads what it follows a publish by.
	testdb.Exec(t, asRole(t, dbURL, api), "SELECT model_hash, plan_hash FROM superschematic_schema_state",
		"SELECT documents FROM "+migrate.RecordTable)

	// The next publish no longer lists old: its reads go too.
	evolve := readFixture(t, migrate.Postgres, "02-evolve")
	evolveAbs, err := filepath.Abs(fixturePath(migrate.Postgres, "02-evolve"))
	if err != nil {
		t.Fatal(err)
	}
	job.Databases[0].Plan = evolveAbs
	job.Databases[0].Record = writeRecord(t, t.TempDir(), evolve.Service, evolve.To)
	job.Databases[0].Privileges.ReadWrite = []string{api}
	invoke(t, nil, "job", "--job", writeJob(t, job)).expect(t, exitOK, "plan "+evolve.Hash+" applied")
	if can(t, dbURL, old, "public."+migrate.RecordTable, "SELECT") || can(t, dbURL, old, "public.superschematic_schema_state", "SELECT") {
		t.Errorf("%s still reads the state", old)
	}
	if !can(t, dbURL, api, "public."+migrate.RecordTable, "SELECT") {
		t.Errorf("%s no longer reads the record", api)
	}
	if got := testdb.Strings(t, dbURL, "SELECT model_hash FROM "+migrate.RecordTable); len(got) != 1 || got[0] != evolve.To {
		t.Fatalf("the record holds %v", got)
	}

	// A record with no plan is refused.
	job.Databases[0].Plan = ""
	invoke(t, nil, "job", "--job", writeJob(t, job)).expect(t, exitFailed, "has a record and no plan")
}

// TestRecordPrintsARuntimeDatabasesRecordAndItsModel: record prints the
// record apply --record wrote (D117), and --model the model it is of, the
// one status --model prints; it refuses a file that does not exist, which
// it does not create, a database with no record, and one whose model has
// moved past the record.
func TestRecordPrintsARuntimeDatabasesRecordAndItsModel(t *testing.T) {
	for _, backend := range []string{"sqlite", "postgres"} {
		t.Run(backend, func(t *testing.T) {
			dialect, db := migrate.SQLite, filepath.Join(t.TempDir(), "app.db")
			if backend == "postgres" {
				dialect, db = migrate.Postgres, testdb.NewPostgres(t)
			}
			create := readFixture(t, dialect, "01-create")
			if backend == "sqlite" {
				invoke(t, nil, "record", "--database-url", db).expect(t, exitFailed)
				if _, err := os.Stat(db); err == nil {
					t.Fatal("record created the file it was to read")
				}
			}
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "01-create"), "--database-url", db).expect(t, exitOK)
			invoke(t, nil, "record", "--database-url", db).expect(t, exitFailed, "holds no record of runtime mode")

			// The second plan writes the record of its model.
			evolve := readFixture(t, dialect, "02-evolve")
			record := writeRecord(t, t.TempDir(), evolve.Service, evolve.To)
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "02-evolve"), "--record", record, "--database-url", db).expect(t, exitOK)
			printed := invoke(t, nil, "record", "--database-url", db).expect(t, exitOK)
			got, err := migrate.ReadRecord([]byte(printed.stdout))
			if err != nil {
				t.Fatal(err)
			}
			if got.Service != create.Service || got.ModelHash != evolve.To || string(got.Versions) != `{"Order":1}` || string(got.Documents) != `[{"name":"`+create.Service+`"}]` {
				t.Fatalf("record printed %+v", got)
			}
			model := invoke(t, nil, "record", "--model", "--database-url", db).expect(t, exitOK)
			status := invoke(t, nil, "status", "--service", create.Service, "--model", "--database-url", db).expect(t, exitOK)
			if model.stdout != status.stdout {
				t.Fatalf("record --model printed\n%s\nstatus --model\n%s", model.stdout, status.stdout)
			}

			// A plan with no record leaves the record behind the model.
			invoke(t, nil, "apply", "--plan", fixturePath(dialect, "03-audit"), "--database-url", db).expect(t, exitOK)
			invoke(t, nil, "record", "--database-url", db).expect(t, exitFailed, "the record is of model "+evolve.To)
		})
	}
}
