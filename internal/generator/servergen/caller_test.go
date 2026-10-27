package servergen_test

import (
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

const (
	// callerRoot holds the caller fixture (D87): caller-db has the user
	// model, a Note table with a required createdBy and a nullable
	// updatedBy, and a queue, NoteRequested, and names its system actor;
	// caller-ts-api writes notes through Deps.orm in operations that need
	// a caller and declares a worker, WriteNotes, that writes them as the
	// system actor; caller-ts-stack serves it.
	callerRoot     = "testdata/caller"
	callerTSAPI    = "caller-ts-api"
	callerTSStack  = "caller-ts-stack"
	callerTSWorker = "caller-ts-api-write-notes"

	// callerSystemActor is caller-db's outputs.sql.systemActor.
	callerSystemActor = "3f2c1a9e-7b4d-4e8a-9c61-0d5e2f7a8b10"
)

var callerTSOrder = []string{"caller-db", callerTSAPI, callerTSStack}

// loadCallerFixture loads the caller fixture's services with the core
// registry.
func loadCallerFixture(t *testing.T, names ...string) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range names {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(callerRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// callerTSImplementation writes notes through Deps.orm with no acting user
// of its own: an operation's writes record its caller, stampNote's the
// system actor it chooses, and the worker's, which serve no request, the
// system actor once a write as no one has refused.
const callerTSImplementation = `import { enqueue, NoUserError } from '@superschematic/http-runtime/postgres';
import { noteRequestedQueue } from '@schemas/caller-db-types/queues';
import type { NoteRow } from '@schemas/caller-db-types/orm';
import type { Constructor, WorkersConstructor } from '@schemas/caller-ts-api-api';

const view = (row: NoteRow) => ({ id: row.id, text: row.text, createdBy: row.createdBy, updatedBy: row.updatedBy });

export const create: Constructor = deps => ({
  notes: {
    async createNote({ input }) {
      return view(
        await deps.orm.transaction(async tx => {
          const row = await tx.note.createOne({ text: input.text });
          await enqueue(tx, noteRequestedQueue, { text: 'follow-up to ' + input.text });
          return row;
        })
      );
    },
    async updateNote({ id, input }) {
      return view(await deps.orm.note.updateOne(id as NoteRow['id'], { text: input.text }));
    },
    async stampNote({ id }) {
      return view(await deps.orm.asSystem().note.updateOne(id as NoteRow['id'], { text: 'stamped' }));
    },
    async listNotes() {
      return (await deps.orm.note.findMany({}, { orderBy: [{ field: 'createdAt' }] })).rows.map(view);
    },
  },
});

export const createWorkers: WorkersConstructor = deps => ({
  async writeNotes(message) {
    try {
      await deps.orm.note.createOne({ text: 'nobody wrote ' + message.text });
      throw new Error('a worker wrote as someone');
    } catch (error) {
      if (!(error instanceof NoUserError)) throw error;
    }
    await deps.orm.asSystem().note.createOne({ text: message.text });
    deps.logger.info('wrote a note', { text: message.text });
  },
});
`

// noteView is a note as caller-ts-api answers it.
type noteView struct {
	ID        string  `json:"id"`
	Text      string  `json:"text"`
	CreatedBy string  `json:"createdBy"`
	UpdatedBy *string `json:"updatedBy"`
}

// TestTypeScriptOperationsActAsTheirCallerAndWorkersAsTheSystem: a
// generated TypeScript server and worker over caller-db on Postgres (D87).
// A signed-in user's createNote writes a note, and enqueues a follow-up in
// the same transaction, through Deps.orm with no .as(ctx.principal): the
// note's createdBy and updatedBy are the caller. Another user's updateNote
// stamps updatedBy with them, and stampNote, which asks for asSystem(),
// with the system actor caller-db names. The worker, which serves no
// request, is refused a write as no one, then writes the follow-up as the
// system actor, whose id needs no user row.
func TestTypeScriptOperationsActAsTheirCallerAndWorkersAsTheSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		postgres = os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	}
	if postgres == "" {
		t.Skipf("set %s to run a TypeScript server and worker against Postgres", identityDatabaseEnv)
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
	f := loadCallerFixture(t, callerTSOrder...)
	f.build(t, repoRoot, paths, callerTSOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, callerTSAPI)
	if err := os.WriteFile(filepath.Join(impl, "index.ts"), []byte(callerTSImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	dir := servergen.ServerDir(out, callerTSStack, callerTSAPI)
	workerDir := servergen.ServerDir(out, callerTSStack, callerTSWorker)
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, pkg := range []string{dir, workerDir, impl} {
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
		}
	}

	databaseURL := identitySchemaTS(t, bun, dir, postgres, filepath.Join(generator.SQLDir(out, "caller-db"), "create.sql"))
	identityConfig := ir.IdentityConfigField(callerTSAPI) + `={"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	server := bunServer(t, bun, dir, "CALLER_DB_DATABASE_URL="+databaseURL, identityConfig)
	server.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	server.send(t, http.MethodPost, "/api/auth/register", `{"login": "bob@example.com", "name": "Bob", "password": "bob's password"}`, http.StatusOK, `"token"`)
	ada := server.login(t, "ada@example.com", "ada's password")
	bob := server.login(t, "bob@example.com", "bob's password")
	adaID, bobID := callerID(t, server, ada), callerID(t, server, bob)

	created := callNote(t, server, ada, http.MethodPost, "/api/notes", `{"text": "first"}`)
	if created.CreatedBy != adaID || created.UpdatedBy == nil || *created.UpdatedBy != adaID {
		t.Errorf("Ada's createNote recorded createdBy %q and updatedBy %v, want Ada, %s", created.CreatedBy, created.UpdatedBy, adaID)
	}
	updated := callNote(t, server, bob, http.MethodPut, "/api/notes/"+created.ID, `{"text": "edited"}`)
	if updated.CreatedBy != adaID || updated.UpdatedBy == nil || *updated.UpdatedBy != bobID {
		t.Errorf("Bob's updateNote recorded createdBy %q and updatedBy %v, want Ada's and Bob's, %s", updated.CreatedBy, updated.UpdatedBy, bobID)
	}
	system := base62UUID(t, callerSystemActor)
	stamped := callNote(t, server, bob, http.MethodPost, "/api/notes/"+created.ID+"/stamp", ``)
	if stamped.UpdatedBy == nil || *stamped.UpdatedBy != system {
		t.Errorf("stampNote, which asks for asSystem(), recorded updatedBy %v, want the system actor %s", stamped.UpdatedBy, system)
	}

	worker := exec.Command(bun, servergen.TypeScriptMainFile)
	worker.Dir = workerDir
	worker.Env = append(os.Environ(), "CALLER_DB_DATABASE_URL="+databaseURL, "WORKER_CONCURRENCY=1")
	var logs lockedBuffer
	worker.Stdout, worker.Stderr = &logs, &logs
	if err := worker.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = worker.Process.Kill(); _ = worker.Wait() })
	deadline := time.Now().Add(30 * time.Second)
	var followUp *noteView
	for followUp == nil {
		for _, note := range listNotes(t, server, ada) {
			if note.Text == "follow-up to first" {
				note := note
				followUp = &note
			}
		}
		if followUp != nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the worker wrote no follow-up:\n%s", logs.String())
		}
		time.Sleep(200 * time.Millisecond)
	}
	if followUp.CreatedBy != system || followUp.UpdatedBy == nil || *followUp.UpdatedBy != system {
		t.Errorf("the worker's note recorded createdBy %q and updatedBy %v, want the system actor %s", followUp.CreatedBy, followUp.UpdatedBy, system)
	}
	for _, note := range listNotes(t, server, ada) {
		if strings.HasPrefix(note.Text, "nobody wrote") {
			t.Errorf("the worker wrote a note as no one: %+v", note)
		}
	}
	if err := worker.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(logs.String(), `"msg":"wrote a note"`) {
		t.Errorf("the worker logged no note:\n%s", logs.String())
	}
	server.stop(t)
}

// callerID is the id of the user token signs in as, base62, as the
// identity runtime answers me.
func callerID(t *testing.T, s *started, token string) string {
	t.Helper()
	var me struct {
		Data struct {
			User struct {
				ID string `json:"id"`
			} `json:"user"`
		} `json:"data"`
	}
	callJSON(t, s, token, http.MethodGet, "/api/auth/me", "", &me)
	if me.Data.User.ID == "" {
		t.Fatal("me answered no id")
	}
	return me.Data.User.ID
}

// callNote sends body to path as the user token signs in as and returns
// the note the operation answers.
func callNote(t *testing.T, s *started, token, method, path, body string) noteView {
	t.Helper()
	var answer struct {
		Data noteView `json:"data"`
	}
	callJSON(t, s, token, method, path, body, &answer)
	return answer.Data
}

// listNotes is every note, as the user token signs in as reads them.
func listNotes(t *testing.T, s *started, token string) []noteView {
	t.Helper()
	var answer struct {
		Data []noteView `json:"data"`
	}
	callJSON(t, s, token, http.MethodGet, "/api/notes", "", &answer)
	return answer.Data
}

// callJSON sends body to path with token as a bearer and decodes the
// answer, which must be 200, into into.
func callJSON(t *testing.T, s *started, token, method, path, body string, into any) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("%s %s = %d: %s\n%s", method, path, resp.StatusCode, answer, s.out.String())
	}
	if err := json.Unmarshal(answer, into); err != nil {
		t.Fatalf("%s %s answered %s: %v", method, path, answer, err)
	}
}

// base62UUID is a hyphenated UUID in its base62 wire form, as a row of the
// ORM holds it.
func base62UUID(t *testing.T, hyphenated string) string {
	t.Helper()
	const alphabet = "0123456789ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz"
	n, ok := new(big.Int).SetString(strings.ReplaceAll(hyphenated, "-", ""), 16)
	if !ok {
		t.Fatalf("%s is no UUID", hyphenated)
	}
	base, digit := big.NewInt(62), new(big.Int)
	text := ""
	for n.Sign() > 0 {
		n.DivMod(n, base, digit)
		text = string(alphabet[digit.Int64()]) + text
	}
	return text
}
