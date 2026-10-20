package servergen_test

import (
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// The sqlite-first TypeScript fixture (D64; docs/stack-model.md, section
// 18.6): ts-notes, a TypeScript API with a job, on ts-notes-db, whose DB
// service lists sqlite first, deployed alone by ts-lite-stack, whose Local
// environment keeps the database in a SQLite file.
var tsLiteOrder = []string{"ts-notes-db", "ts-notes", "ts-lite-stack"}

// tsPruneNotes is the deployable of ts-notes' job PruneNotes.
const tsPruneNotes = "ts-notes-prune-notes"

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
// config.ts and package.json, and the server's and the job's main.ts and
// package.json.
func tsLiteGenerated(repoRoot string) map[string]string {
	out := filepath.Join(repoRoot, "schemas", "dist")
	files := map[string]string{}
	for _, file := range []string{"deps.ts", "config.ts", "package.json"} {
		files[filepath.Join("api", "ts-notes", file)] = filepath.Join(out, "api", "ts-notes", file)
	}
	for _, server := range []string{"ts-notes", tsPruneNotes} {
		for _, file := range []string{servergen.TypeScriptMainFile, servergen.TypeScriptPackageFile} {
			files[filepath.Join("server", "ts-lite-stack", server, file)] = filepath.Join(servergen.ServerDir(out, "ts-lite-stack", server), file)
		}
	}
	return files
}

// TestTypeScriptSQLiteEntrypointGolden: ts-notes' DB service lists sqlite
// first, so its Deps.db is the runtime's SqlDatabase and not a pg Pool,
// its database field a DatabaseConnection, and its package peers on no
// pg. The server's main.ts opens the SQLite file its sqlite: URL names
// with openSqlite, /readyz pings it with ./d1's ping, the shutdown closes
// it, and its package depends on no pg; the job's main.ts opens it too.
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

// tsNotesImplementation keeps notes in Deps.db, a SqlDatabase over the
// SQLite file, creating its table when it starts, as a migration would.
// Its job, PruneNotes, deletes the notes whose text is empty.
const tsNotesImplementation = `import { notFound } from '@superschematic/http-runtime';
import type { Constructor, JobsConstructor } from '@schemas/ts-notes-api';

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
        await deps.db.prepare('INSERT INTO note (id, text) VALUES (?1, ?2) ON CONFLICT (id) DO UPDATE SET text = ?2').bind(id, text).run();
        return { id, text };
      },
    },
  };
};

export const createJobs: JobsConstructor = deps => ({
  async pruneNotes() {
    const pruned = await deps.db.prepare("DELETE FROM note WHERE text = ''").run();
    deps.logger.info('pruned', { notes: pruned.meta.changes });
  },
});
`

// TestTypeScriptSQLiteEntrypointCompilesAndServes: the sqlite-first
// server's and job's packages install in the workspace and type-check with
// the implementation, and the server runs under Bun on a sqlite: URL: it
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
	for _, dir := range []string{impl, serverDir, jobDir} {
		check(dir)
	}

	file := filepath.Join(t.TempDir(), "notes_db.sqlite")
	url := "TS_NOTES_DB_DATABASE_URL=sqlite:" + filepath.ToSlash(file)
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
	notes.stop(t)

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
