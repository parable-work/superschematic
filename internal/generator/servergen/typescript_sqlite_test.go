package servergen_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The sqlite-first TypeScript fixture (D64; docs/stack-model.md, section
// 18.6): ts-notes, a TypeScript API with a job and a worker, on
// ts-notes-db, whose DB service lists sqlite first, deployed alone by
// ts-lite-stack, whose Local environment keeps the database in a SQLite
// file.
var tsLiteOrder = []string{"ts-notes-db", "ts-notes", "ts-lite-stack"}

// tsPruneNotes is the deployable of ts-notes' job PruneNotes, and
// tsIndexNotes of its worker IndexNotes, which claims ts-notes-db's queue
// NoteWritten on the SQLite file (D53, amended).
const (
	tsPruneNotes = "ts-notes-prune-notes"
	tsIndexNotes = "ts-notes-index-notes"
)

// loadTypeScriptSQLiteFixture loads the sqlite-first fixture's services.
func loadTypeScriptSQLiteFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range tsLiteOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsServicesRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// tsLiteGenerated lists the files of the sqlite-first fixture the
// goldens hold, by their path under tsGoldenRoot: ts-notes' deps.ts,
// config.ts and package.json, the server's and the job's main.ts and
// package.json, the server's fetch.ts, its Workers entrypoint, and the
// job's scheduled.ts, its entrypoint on Cron Triggers (D64).
func tsLiteGenerated(repoRoot string) map[string]string {
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, file := range []string{"deps.ts", "config.ts", "package.json"} {
		files[filepath.Join("api", "ts-notes", file)] = filepath.Join(out, "api", "ts-notes", file)
	}
	for _, server := range []string{"ts-notes", tsPruneNotes, tsIndexNotes} {
		for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile} {
			files[filepath.Join("server", "ts-lite-stack", server, file)] = filepath.Join(servergen.ServerDir(out, "ts-lite-stack", server), file)
		}
	}
	files[filepath.Join("server", "ts-lite-stack", "ts-notes", servergen.TypeScriptFetchFile)] = filepath.Join(servergen.ServerDir(out, "ts-lite-stack", "ts-notes"), servergen.TypeScriptFetchFile)
	files[filepath.Join("server", "ts-lite-stack", tsPruneNotes, servergen.TypeScriptScheduledFile)] = filepath.Join(servergen.ServerDir(out, "ts-lite-stack", tsPruneNotes), servergen.TypeScriptScheduledFile)
	return files
}

// TestTypeScriptSQLiteEntrypointGolden: ts-notes' DB service lists sqlite
// first, so its Deps.db is the runtime's SqlDatabase and not a pg Pool,
// its database field a DatabaseConnection, and its package peers on no
// pg. The server's main.ts opens the SQLite file its sqlite: URL names
// with openSqlite, /readyz pings it with ./d1's ping, the shutdown closes
// it, and its package depends on no pg; the job's main.ts opens it too.
// Edge places the server on the fake bundle platform, its job on the fake
// cron platform and the database on the fake SQLite one, so its fetch.ts
// takes Deps.db from the d1 binding its connection names and /readyz
// pings it, and the job's scheduled.ts takes it from the same binding
// (D64).
// Regenerate with:
//
//	go test ./internal/generator/servergen -run TestTypeScriptSQLiteEntrypointGolden -update
func TestTypeScriptSQLiteEntrypointGolden(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadTypeScriptSQLiteFixture(t)
	f.build(t, repoRoot, tsFakePaths(repoRoot), tsLiteOrder...)
	compareGoldens(t, tsGoldenRoot, tsLiteGenerated(repoRoot))
	for rel, path := range tsLiteGenerated(repoRoot) {
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(data), "'pg'") || strings.Contains(string(data), `"pg"`) || strings.Contains(string(data), "connectPostgres") {
			t.Errorf("%s reaches Postgres, though ts-notes-db lists sqlite first:\n%s", rel, data)
		}
	}
}

// TestTypeScriptPlanRefusesWhatSQLiteCannotRun: a stack whose environments
// resolve refuses these first; the plan of an entrypoint refuses them too,
// since its code could not reach the database: an identity store over a
// sqlite-first authDb, which no TypeScript store runs on (D50). A worker
// whose queue's database lists sqlite first plans, claiming on the SQLite
// file with sqliteQueue (D53, amended).
func TestTypeScriptPlanRefusesWhatSQLiteCannotRun(t *testing.T) {
	input := func(output *tsrestgen.APIOutput, worker *servergen.WorkerInput) servergen.TypeScriptInput {
		return servergen.TypeScriptInput{
			Stack:  "notes-stack",
			Server: "notes",
			Dir:    t.TempDir(),
			APIs: []servergen.TypeScriptAPIInput{{
				Output: output,
				Routes: &apigen.APIOutput{SchemaName: "notes"},
				Config: &envgen.ConfigOutput{SchemaName: "notes", Derived: []envgen.DerivedField{
					{Key: "NOTES_DB_DATABASE", Kind: ir.EdgeSQL, Service: "notes-db", From: "authDb", Dialect: "sqlite"},
				}},
				Implementation: servergen.TypeScriptImplementation{Package: "@schemas/notes-implementation"},
			}},
			Worker: worker,
		}
	}
	deps := tsrestgen.DepsInfo{Database: "notes-db", Dialect: "sqlite", DatabaseTypesPackage: "@schemas/notes-db-types"}
	identity := &tsrestgen.APIOutput{
		SchemaName: "notes", PackageName: "@schemas/notes-api", HasEnvConfig: true, Deps: deps,
		Identity: &tsrestgen.IdentityInfo{AuthDB: "notes-db", DescriptorModule: "@schemas/notes-db-types/identity"},
	}
	_, err := servergen.PlanTypeScript(input(identity, nil))
	if want := "server notes serves notes, which authenticates with the identity runtime over notes-db, its authDb, whose DB service lists sqlite first; a TypeScript server's identity store runs on Postgres, and one over SQLite is not built (D50, D64)"; err == nil || !strings.Contains(err.Error(), want) {
		t.Errorf("PlanTypeScript of an identity API on SQLite = %v, want %q", err, want)
	}
	worker := &tsrestgen.APIOutput{
		SchemaName: "notes", PackageName: "@schemas/notes-api", HasEnvConfig: true, Deps: deps,
		Workers: []tsrestgen.WorkerInfo{{Name: "IndexNotes", Method: "indexNotes", Queue: "NoteWritten", Concurrency: 1}},
	}
	plan, err := servergen.PlanTypeScript(input(worker, &servergen.WorkerInput{Name: "IndexNotes"}))
	if err != nil || plan.Worker == nil || plan.Worker.Database == nil || !plan.Worker.Database.SQLite {
		t.Errorf("PlanTypeScript of a worker on SQLite = %+v, %v, want a worker that claims on the SQLite file", plan, err)
	}
}

// tsNotesImplementation keeps notes in Deps.db, a SqlDatabase over the
// SQLite file, creating its table when it starts, as a migration would.
// Its job, PruneNotes, deletes the notes whose text is empty. putNote
// enqueues a NoteWritten in the batch that writes the note, and its worker,
// IndexNotes, logs each message, failing one of the note poison.
const tsNotesImplementation = `import { notFound } from '@superschematic/http-runtime';
import { enqueueStatement } from '@superschematic/http-runtime/d1';
import { noteWrittenQueue } from '@schemas/ts-notes-db-types/queues';
import type { Constructor, JobsConstructor, WorkersConstructor } from '@schemas/ts-notes-api';

export const create: Constructor = async deps => {
  await deps.db.exec('CREATE TABLE IF NOT EXISTS note (id TEXT PRIMARY KEY, text TEXT NOT NULL)');
  return {
    notes: {
      async getNote({ id }) {
        const note = await deps.db.prepare('SELECT id, text FROM note WHERE id = ?').bind(id).first<{ id: string; text: string }>();
        if (note === null) throw notFound('no note ' + id);
        return note;
      },
      async putNote({ id, text }) {
        // The note and its message commit together, in one batch.
        await deps.db.batch([
          deps.db.prepare('INSERT INTO note (id, text) VALUES (?1, ?2) ON CONFLICT (id) DO UPDATE SET text = ?2').bind(id, text),
          enqueueStatement(deps.db, noteWrittenQueue, { noteId: id, words: text.split(' ').filter(Boolean), empty: text === '' }),
        ]);
        return { id, text };
      },
    },
  };
};

export const createWorkers: WorkersConstructor = deps => ({
  async indexNotes(message) {
    if (message.noteId === 'poison') throw new Error('poison every time');
    deps.logger.info('indexed', { note: message.noteId, words: message.words.join('+'), empty: message.empty });
  },
});

export const createJobs: JobsConstructor = deps => ({
  async pruneNotes() {
    const pruned = await deps.db.prepare("DELETE FROM note WHERE text = ''").run();
    deps.logger.info('pruned', { notes: pruned.meta.changes });
  },
});
`

// TestTypeScriptSQLiteEntrypointCompilesAndServes: the sqlite-first
// server's and job's packages install in the workspace and type-check with
// the implementation, fetch.ts and scheduled.ts among them, and both
// bundle for workerd, the job's bundle running under workerd over a D1
// stand-in. The server runs under Bun on a sqlite: URL: it
// opens the file, answers /readyz, keeps a note in it and reads it back,
// and stops cleanly. The job runs once on the same file and prunes the
// empty note. On a postgres:// URL, or a D1 binding, which only a Worker
// reaches, the server refuses to start, naming the variable.
func TestTypeScriptSQLiteEntrypointCompilesAndServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadTypeScriptSQLiteFixture(t)
	f.build(t, repoRoot, paths, tsLiteOrder...)
	names := naming.Default()
	out := filepath.Join(repoRoot, "schemas", "dist")
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	check := func(dir string) {
		t.Helper()
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = dir
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", dir, err, output)
		}
	}
	impl := names.TypeScriptImplementationDir(repoRoot, "ts-notes")
	// The scaffold type-checks against the API package before the
	// engineer's code replaces it.
	check(impl)
	if err := os.WriteFile(filepath.Join(impl, tsrestgen.ImplementationFile), []byte(tsNotesImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	serverDir := servergen.ServerDir(out, "ts-lite-stack", "ts-notes")
	jobDir := servergen.ServerDir(out, "ts-lite-stack", tsPruneNotes)
	workerDir := servergen.ServerDir(out, "ts-lite-stack", tsIndexNotes)
	// The server's tsconfig.json takes fetch.ts in too, and the job's
	// scheduled.ts: Edge places them on the fake bundle and cron
	// platforms, as Workers and Cron Triggers (D64).
	for _, dir := range []string{impl, serverDir, jobDir, workerDir} {
		check(dir)
	}
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist", "workerd", "superscalar_wasm_bg.wasm")); err == nil {
		for _, dir := range []string{serverDir, jobDir} {
			bundle := exec.Command(bun, "bundle.ts", "--check")
			bundle.Dir = dir
			if output, err := bundle.CombinedOutput(); err != nil || !strings.Contains(string(output), "bundle (checked): main.js") {
				t.Errorf("bun bundle.ts --check in %s, whose Deps.db is a D1 binding on Workers = %v:\n%s", filepath.Base(dir), err, output)
			}
		}
		runPruneNotesUnderWorkerd(t, bun, paths, jobDir)
	} else {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("superscalar's workerd build is not built (scripts/superscalar-dep.sh builds it): %v", err))
	}

	file := filepath.Join(t.TempDir(), "notes_db.sqlite")
	url := "TS_NOTES_DB_DATABASE_URL=sqlite:" + filepath.ToSlash(file)
	// The SQLite model's DDL of ts-notes-db, which a migration would apply:
	// the note table and the queue NoteWritten's.
	model, err := sqlmigrate.BuildModel(f.schemas["ts-notes-db"], sqlgen.Options{SchemaName: "ts-notes-db"}, sqlmigrate.SQLite)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	queueRows := liteQueue(t, bun, file, ddl)
	notes := bunServer(t, bun, serverDir, url)
	notes.expect(t, http.MethodGet, "/readyz", http.StatusOK, `"ready"`)
	notes.send(t, http.MethodPut, "/api/notes/tea", `{"text":"steep for four minutes"}`, http.StatusOK, `"text":"steep for four minutes"`)
	notes.send(t, http.MethodPut, "/api/notes/empty", `{"text":""}`, http.StatusOK)
	notes.expect(t, http.MethodGet, "/api/notes/tea", http.StatusOK, `"id":"tea"`, `"steep for four minutes"`)
	notes.expect(t, http.MethodGet, "/api/notes/none", http.StatusNotFound, "no note none")
	notes.stop(t)
	if !strings.Contains(notes.out.String(), `"msg":"stopped"`) {
		t.Errorf("ts-notes did not stop cleanly:\n%s", notes.out.String())
	}

	job := exec.Command(bun, servergen.TypeScriptMainFile)
	job.Dir = jobDir
	job.Env = append(os.Environ(), url)
	if output, err := job.CombinedOutput(); err != nil || !strings.Contains(string(output), `"msg":"pruned"`) || !strings.Contains(string(output), `"notes":1`) || !strings.Contains(string(output), `"msg":"job done"`) {
		t.Errorf("the job on the SQLite file = %v:\n%s", err, output)
	}
	notes = bunServer(t, bun, serverDir, url)
	notes.expect(t, http.MethodGet, "/api/notes/empty", http.StatusNotFound)
	notes.expect(t, http.MethodGet, "/api/notes/tea", http.StatusOK, `"steep for four minutes"`)
	notes.send(t, http.MethodPut, "/api/notes/poison", `{"text":"never indexed"}`, http.StatusOK)
	notes.stop(t)
	runIndexNotes(t, bun, workerDir, url, queueRows)

	for env, want := range map[string][]string{
		"TS_NOTES_DB_DATABASE_URL=postgres://notes:secret@127.0.0.1:9/notes": {"database ts-notes-db, from TS_NOTES_DB_DATABASE: the database's URL is a postgres: URL; a sqlite-first database's is sqlite:<path>"},
		"TS_NOTES_DB_DATABASE_D1_BINDING=TS_NOTES_DB_DATABASE":               {"TS_NOTES_DB_DATABASE is a D1 binding, which only a Worker reaches; database ts-notes-db lists sqlite first, so set TS_NOTES_DB_DATABASE_URL to a sqlite: URL"},
	} {
		cmd := exec.Command(bun, servergen.TypeScriptMainFile)
		cmd.Dir = serverDir
		cmd.Env = append(os.Environ(), env, "TS_NOTES_DB_DATABASE_D1_DATABASE=0b0c4d4e-8f9a-4b1c-9d2e-3f4a5b6c7d8e", "PORT="+freePort(t))
		if !strings.Contains(env, "D1_BINDING") {
			cmd.Env = append(os.Environ(), env, "PORT="+freePort(t))
		}
		refused, err := cmd.CombinedOutput()
		for _, w := range want {
			if err == nil || !strings.Contains(string(refused), w) {
				t.Errorf("ts-notes on %s = %v, want it to stop saying %q:\n%s", env, err, w, refused)
			}
		}
		if strings.Contains(string(refused), "secret") {
			t.Errorf("ts-notes printed the database URL's password:\n%s", refused)
		}
	}
}

// liteQueue applies ddl to the SQLite file and returns a reader of its
// queue NoteWritten's rows, by note: each one's state, attempts and last
// error, read through bun:sqlite.
func liteQueue(t *testing.T, bun, file, ddl string) func() map[string]queueRow {
	t.Helper()
	dir := t.TempDir()
	script := filepath.Join(dir, "sqlite.ts")
	if err := os.WriteFile(script, []byte(`import { Database } from 'bun:sqlite';
const [file, sql] = process.argv.slice(2);
const db = new Database(file, { create: true });
db.run('PRAGMA busy_timeout = 5000');
if (sql.startsWith('SELECT')) console.log(JSON.stringify(db.query(sql).all()));
else db.exec(sql);
db.close();
`), 0o644); err != nil {
		t.Fatal(err)
	}
	run := func(sql string) string {
		t.Helper()
		output, err := exec.Command(bun, script, file, sql).CombinedOutput()
		if err != nil {
			t.Fatalf("bun sqlite.ts %q: %v\n%s", sql, err, output)
		}
		return string(output)
	}
	run(ddl)
	return func() map[string]queueRow {
		t.Helper()
		var list []queueRow
		if err := json.Unmarshal([]byte(run("SELECT note_id AS sku, state, attempts, claim_token, last_error FROM note_written_queue")), &list); err != nil {
			t.Fatal(err)
		}
		byNote := map[string]queueRow{}
		for _, r := range list {
			byNote[r.SKU] = r
		}
		return byNote
	}
}

// runIndexNotes runs the worker IndexNotes on the SQLite file the server
// wrote, whose queue holds the messages of the notes tea, empty and
// poison: it handles tea and empty, logging each, fails poison until it is
// dead, its retries spent, and on SIGTERM stops and exits 0 (D53,
// amended).
func runIndexNotes(t *testing.T, bun, workerDir, url string, rows func() map[string]queueRow) {
	t.Helper()
	if r := rows(); len(r) != 3 || r["tea"].State != ir.QueueStateReady {
		t.Fatalf("the server enqueued %v, want tea, empty and poison ready", r)
	}
	worker := exec.Command(bun, servergen.TypeScriptMainFile)
	worker.Dir = workerDir
	worker.Env = append(os.Environ(), url)
	var logs lockedBuffer
	worker.Stdout, worker.Stderr = &logs, &logs
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	exited := make(chan error, 1)
	go func() { exited <- worker.Wait() }()
	t.Cleanup(func() { _ = worker.Process.Kill() })
	deadline := time.Now().Add(30 * time.Second)
	for {
		r := rows()
		if r["tea"].State == ir.QueueStateDone && r["empty"].State == ir.QueueStateDone && r["poison"].State == ir.QueueStateDead {
			break
		}
		select {
		case err := <-exited:
			t.Fatalf("the worker exited: %v\n%s", err, logs.String())
		default:
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker did not settle the messages: %v\n%s", r, logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if poison := rows()["poison"]; poison.Attempts != 2 || poison.LastError == nil || *poison.LastError != "poison every time" {
		t.Errorf("poison took %d attempts with %v; want two, its error kept", poison.Attempts, poison.LastError)
	}
	if err := worker.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-exited:
		if err != nil {
			t.Errorf("the worker exited %v on SIGTERM, want 0:\n%s", err, logs.String())
		}
	case <-time.After(15 * time.Second):
		t.Fatalf("the worker did not stop on SIGTERM:\n%s", logs.String())
	}
	for _, want := range []string{
		`"msg":"worker started"`,
		`"msg":"indexed","stack":"ts-lite-stack","worker":"ts-notes-index-notes","api":"ts-notes","note":"tea","words":"steep+for+four+minutes","empty":false`,
		`"note":"empty","words":"","empty":true`,
	} {
		if !strings.Contains(logs.String(), want) {
			t.Errorf("the worker's log lacks %s:\n%s", want, logs.String())
		}
	}
}

// runPruneNotesUnderWorkerd bundles PruneNotes' scheduled.ts and runs it
// under the runtime's pinned workerd (testdata/workerd/cron-d1), with the
// bindings cloudflare.cron gives a job whose database is on D1 and a
// stand-in for the d1 binding: the tester fires the scheduled handler
// twice, the first run prunes two empty notes through the binding and the
// second none, each logged done (D64).
func runPruneNotesUnderWorkerd(t *testing.T, bun string, paths naming.LocalPaths, jobDir string) {
	t.Helper()
	workerd := filepath.Join(paths.HTTPRuntimeTypeScript, "test", "workerd", "node_modules", ".bin", "workerd")
	if _, err := os.Stat(workerd); err != nil {
		testpaths.RequireOrSkipTS(t, "workerd is not installed: run bun install --frozen-lockfile in runtime/http/typescript/test/workerd")
		return
	}
	dir := t.TempDir()
	bundle := exec.Command(bun, "bundle.ts", dir)
	bundle.Dir = jobDir
	if output, err := bundle.CombinedOutput(); err != nil {
		t.Fatalf("bun bundle.ts in %s: %v\n%s", filepath.Base(jobDir), err, output)
	}
	for _, file := range []string{"cron-d1.capnp", "d1.js", "tester.js"} {
		data, err := os.ReadFile(filepath.Join("testdata", "workerd", "cron-d1", file))
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, file), data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	cmd := exec.Command(workerd, "test", "cron-d1.capnp")
	cmd.Dir = dir
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("workerd test cron-d1.capnp: %v\n%s", err, output)
	}
	if testing.Verbose() {
		t.Logf("workerd test cron-d1.capnp:\n%s", output)
	}
	for _, want := range []string{
		"the D1 job under workerd: two runs done",
		`"msg":"job started","stack":"ts-lite-stack","job":"ts-notes-prune-notes","api":"ts-notes","class":"PruneNotes","cron":"0 3 * * *","try":1,"tries":1}`,
		`"msg":"pruned","stack":"ts-lite-stack","job":"ts-notes-prune-notes","api":"ts-notes","notes":2}`,
		`"msg":"pruned","stack":"ts-lite-stack","job":"ts-notes-prune-notes","api":"ts-notes","notes":0}`,
		`"msg":"job done"`,
	} {
		if !strings.Contains(string(output), want) {
			t.Errorf("workerd's output does not hold %s:\n%s", want, output)
		}
	}
	if strings.Contains(string(output), `"msg":"job failed"`) {
		t.Errorf("a run of PruneNotes failed under workerd:\n%s", output)
	}
}
