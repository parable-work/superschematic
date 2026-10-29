package servergen_test

import (
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
	"github.com/parable-work/superschematic/internal/testpaths"
)

// The Go half of the caller fixture: caller-go-api is caller-ts-api as a
// public Go API, and caller-go-stack serves it.
const (
	callerGoAPI    = "caller-go-api"
	callerGoStack  = "caller-go-stack"
	callerGoWorker = "caller-go-api-write-notes"
)

var callerGoOrder = []string{"caller-db", callerGoAPI, callerGoStack}

// callerGoImplementation is callerTSImplementation in Go: the operations
// pass the route's context, which acts as the caller, to Deps.DB, stampNote
// passes orm.AsSystem's, and the worker, whose context acts as no one, is
// refused a write until it passes orm.AsSystem's.
const callerGoImplementation = `package callergoapi

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	api "example.com/schemas/api/caller-go-api"
	orm "example.com/schemas/orm/caller-db"
	callerdb "example.com/schemas/types/go/caller-db"
	types "example.com/schemas/types/go/caller-go-api"
)

func New(deps api.Deps) (api.Implementations, error) {
	return api.Implementations{Notes: &Notes{deps: deps}}, nil
}

var _ api.Constructor = New

func NewWorkers(deps api.Deps) (api.Workers, error) {
	return &workers{deps: deps}, nil
}

var _ api.WorkersConstructor = NewWorkers

type workers struct {
	deps api.Deps
}

func (w *workers) WriteNotes(ctx context.Context, msg callerdb.NoteRequested) error {
	notes := w.deps.DB.GetNoteRepository()
	if _, err := notes.CreateOne(ctx, &callerdb.Note{Text: "nobody wrote " + msg.Text}); !errors.Is(err, orm.ErrNoUserInContext) {
		return fmt.Errorf("a worker wrote as someone: %v", err)
	}
	_, err := notes.CreateOne(orm.AsSystem(ctx), &callerdb.Note{Text: msg.Text})
	return err
}

type Notes struct {
	deps api.Deps
}

func view(note *callerdb.Note) *types.NoteView {
	out := &types.NoteView{Id: note.Id.String(), Text: note.Text, CreatedBy: note.CreatedBy.String()}
	if note.UpdatedBy != nil {
		out.UpdatedBy = note.UpdatedBy.String()
	}
	return out
}

func noteID(id string) (callerdb.IdentityUUID, error) {
	var out callerdb.IdentityUUID
	quoted, _ := json.Marshal(id)
	return out, json.Unmarshal(quoted, &out)
}

func (impl *Notes) CreateNote(ctx context.Context, input *types.NoteInput) (*types.NoteView, error) {
	var created *callerdb.Note
	err := impl.deps.DB.Transaction(ctx, func(tx orm.TxInterface) error {
		note, err := tx.GetNoteRepository().CreateOne(ctx, &callerdb.Note{Text: input.Text})
		if err != nil {
			return err
		}
		created = note
		return orm.EnqueueNoteRequested(ctx, tx, &callerdb.NoteRequested{Text: "follow-up to " + input.Text})
	})
	if err != nil {
		return nil, err
	}
	return view(created), nil
}

func (impl *Notes) UpdateNote(ctx context.Context, id string, input *types.NoteInput) (*types.NoteView, error) {
	key, err := noteID(id)
	if err != nil {
		return nil, err
	}
	text := input.Text
	note, err := impl.deps.DB.GetNoteRepository().UpdateOne(ctx, key, &orm.NoteUpdate{Text: &text})
	if err != nil {
		return nil, err
	}
	return view(note), nil
}

func (impl *Notes) StampNote(ctx context.Context, id string) (*types.NoteView, error) {
	key, err := noteID(id)
	if err != nil {
		return nil, err
	}
	text := "stamped"
	note, err := impl.deps.DB.GetNoteRepository().UpdateOne(orm.AsSystem(ctx), key, &orm.NoteUpdate{Text: &text})
	if err != nil {
		return nil, err
	}
	return view(note), nil
}

func (impl *Notes) ListNotes(ctx context.Context) ([]types.NoteView, error) {
	notes, _, err := impl.deps.DB.GetNoteRepository().FindMany(ctx, &orm.NoteFilter{}, &orm.NoteFindOptions{Fields: orm.NoteFields{Id: true, Text: true, CreatedAt: true, UpdatedAt: true, CreatedBy: true, UpdatedBy: true}})
	if err != nil {
		return nil, err
	}
	out := make([]types.NoteView, 0, len(notes))
	for _, note := range notes {
		out = append(out, *view(note))
	}
	return out, nil
}
`

// TestGoOperationsActAsTheirCallerAndWorkersAsTheSystem is
// TestTypeScriptOperationsActAsTheirCallerAndWorkersAsTheSystem for a
// generated Go server and worker (D87): each route runs its operation with
// a context that acts as the caller, whose ID the identity runtime put on
// it, so Deps.DB stamps createdBy and updatedBy with the caller, the
// nullable updatedBy through its pointer; orm.AsSystem acts as the system
// actor caller-db names, in an operation and in the worker, whose context
// acts as no one.
func TestGoOperationsActAsTheirCallerAndWorkersAsTheSystem(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		postgres = os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	}
	if postgres == "" {
		t.Skipf("set %s to run a Go server and worker against Postgres", identityDatabaseEnv)
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := loadCallerFixture(t, callerGoOrder...)
	f.build(t, repoRoot, paths, callerGoOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	impl := filepath.Join(naming.Default().GoImplementationDir(repoRoot, callerGoAPI), "implementation.go")
	if err := os.WriteFile(impl, []byte(callerGoImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	dir := servergen.ServerDir(out, callerGoStack, callerGoAPI)
	workerDir := servergen.ServerDir(out, callerGoStack, callerGoWorker)
	binaries := map[string]string{}
	for _, d := range []string{dir, workerDir} {
		goCommand(t, d, "mod", "tidy")
		goCommand(t, d, "vet", ".")
		binary := filepath.Join(t.TempDir(), filepath.Base(d))
		goCommand(t, d, "build", "-o", binary, ".")
		binaries[d] = binary
	}

	databaseURL := identitySchema(t, dir, postgres, filepath.Join(generator.SQLDir(out, "caller-db"), "create.sql"))
	identityConfig := "CALLER_GO_API_IDENTITY=" + `{"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	server := start(t, binaries[dir], "CALLER_DB_DATABASE_URL="+databaseURL, identityConfig)
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
		t.Errorf("stampNote, which passes orm.AsSystem's context, recorded updatedBy %v, want the system actor %s", stamped.UpdatedBy, system)
	}

	worker := exec.Command(binaries[workerDir])
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
	server.stop(t)
}
