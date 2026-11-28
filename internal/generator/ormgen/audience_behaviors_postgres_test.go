package ormgen

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// audienceBehaviorsFixture is the DB service whose @audience tables compose
// behaviors (D156, D103): a Task its owner keeps private or shows to the
// principals it names, a Workflow that holds a conversation, and a Note its
// author keeps private unless it says otherwise, which agent runs read
// alone, and whose summary a deriver writes (D146).
const audienceBehaviorsFixture = "fixture-audience-behaviors-db"

// audienceBehaviorsORMCases are the cases the generated module's test runs
// over fixture-audience-behaviors-db, by name.
var audienceBehaviorsORMCases = []string{
	"a create through the core that names no audience takes the table's defaults",
	"the core reads a held row's audience ids as a list",
	"a deriver fills a private note's summary and another user still cannot read it",
}

// TestHeldBehaviorTablesOnPostgres holds the Go behaviors' core to an
// @audience table that composes behaviors (D156, D180), on Postgres,
// through the generated Go ORM of fixture-audience-behaviors-db. A
// create through the core that names neither audience nor agentAccess
// takes the table's defaults, as a create through the API does, where it
// bound "" and the table's trigger refused it. The core reads such a row
// with its audience ids, a uuid[], as a list of UUIDs, where it decoded
// effectiveAudience as one UUID and refused every command on the row
// ("a []interface {} is no UUID"): a Workflow transition and a Comments
// post of a private task, and a read of a named one. And the HTTP
// runtime's Go deriver loop (runtime/http/go/deriver, D146), opened as a
// deriver's generated main.go opens it, as a behavior caller with no
// principal, which reads every row (D180), fills the summary of a private
// note its author made, which the author then reads and another user
// still cannot. It generates the ORM module, adds the HTTP runtime's
// module to it, and runs audienceBehaviorsORMTest in it against the
// Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL. The module links
// the core's static archive, which scripts/rules-archive.sh stages; the
// test skips without it, and in -short mode.
func TestHeldBehaviorTablesOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the held behavior tables' end-to-end check in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the held behavior tables against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}

	ormDir := generateBehaviorORMModule(t, paths, audienceBehaviorsFixture)
	// The test runs the HTTP runtime's deriver loop, whose module and the
	// modules it reaches by a replace of its own the ORM's go.mod names
	// too: Go takes no replace from a dependency's go.mod.
	edit := exec.Command("go", "mod", "edit",
		"-require=github.com/parable-work/superschematic/runtime/http/go@v0.0.0-00010101000000-000000000000",
		"-replace=github.com/parable-work/superschematic/runtime/http/go="+paths.HTTPRuntimeGo,
		"-replace=github.com/parable-work/superschematic/runtime/schema/go="+paths.SchemaRuntimeGo,
		"-replace=github.com/parable-work/superschematic/ir="+paths.SchemaIR,
	)
	edit.Dir = ormDir
	if out, err := edit.CombinedOutput(); err != nil {
		t.Fatalf("go mod edit: %v\n%s", err, out)
	}
	if err := os.WriteFile(filepath.Join(ormDir, "audience_behaviors_test.go"), []byte(audienceBehaviorsORMTest), 0o644); err != nil {
		t.Fatalf("write the held tables' test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range audienceBehaviorsORMCases {
		if want := "--- PASS: TestHeldBehaviorTablesOnPostgres/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// audienceBehaviorsORMTest is the generated module's test: tasks and notes
// through the generated repositories, in a schema of their own, as their
// owners and as another user, the core's own read of a row, and the HTTP
// runtime's deriver loop over the notes' derived operations, with a model
// that is not one.
const audienceBehaviorsORMTest = `package orm

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-audience-behaviors-db"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/deriver"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

// noteInputs are the inputs of a note's summary.
type noteInputs struct {
	Body *string ` + "`json:\"body,omitempty\"`" + `
}

func TestHeldBehaviorTablesOnPostgres(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the held tables against Postgres")
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
	schema := fmt.Sprintf("ormgen_held_behaviors_%d", time.Now().UnixNano())
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

	user := func(t *testing.T, text string) types.IdentityUUID {
		t.Helper()
		id, err := ParseUserID(text)
		if err != nil {
			t.Fatal(err)
		}
		return id
	}
	alice, bob := user(t, "7c9e6679-7425-40de-944b-e07fc1f90ae7"), user(t, "0f8fad5b-d9cb-469f-a165-70867728950e")
	as := func(id types.IdentityUUID) context.Context {
		return WithPrincipal(ctx, access.Principal{Subject: id.String(), Kind: access.KindUser})
	}
	ids := func(list []types.IdentityUUID) []string {
		out := make([]string, len(list))
		for i, id := range list {
			out[i] = id.String()
		}
		slices.Sort(out)
		return out
	}
	// coreRow is the row of key as the core reads it, in a transaction of
	// its own.
	coreRow := func(t *testing.T, typeName string, key types.IdentityUUID) *rules.Row {
		t.Helper()
		tx, err := pool.Begin(ctx)
		if err != nil {
			t.Fatal(err)
		}
		defer func() { _ = tx.Rollback(ctx) }()
		row, err := rules.ReadRow(ctx, rulesHost(tx), typeName, key.String())
		if err != nil {
			t.Fatalf("the core's read of %s %s: %v", typeName, key, err)
		}
		if row == nil {
			t.Fatalf("the core reads no %s %s", typeName, key)
		}
		return row
	}

	t.Run("a create through the core that names no audience takes the table's defaults", func(t *testing.T) {
		task, err := db.Task.CreateOne(as(alice), &TaskCreate{Title: "Water the plants", Owner: alice})
		if err != nil {
			t.Fatalf("create a task that names no audience: %v", err)
		}
		if task.Audience != "everyone" || task.AgentAccess != "followRole" || task.EffectiveAudience != nil || task.EffectiveAgents != "followRole" {
			t.Errorf("the task's audience is %q, %q, resolved %v and %q, want everyone and followRole", task.Audience, task.AgentAccess, task.EffectiveAudience, task.EffectiveAgents)
		}
		if got, err := db.Task.GetOne(as(bob), *task.Id, nil); err != nil || got.Title != "Water the plants" {
			t.Errorf("another user's read of the task everyone sees = %v, %v", got, err)
		}
		// A table whose defaults are its own: a note is its author's alone,
		// and agent runs only read it.
		note, err := db.Note.CreateOne(as(alice), &NoteCreate{Author: alice, Body: "Buy more soil."})
		if err != nil {
			t.Fatalf("create a note that names no audience: %v", err)
		}
		if note.Audience != "private" || note.AgentAccess != "readOnly" || note.EffectiveAgents != "readOnly" {
			t.Errorf("the note's audience is %q and %q, resolved %q, want private and readOnly", note.Audience, note.AgentAccess, note.EffectiveAgents)
		}
		if got := ids(note.EffectiveAudience); !slices.Equal(got, []string{alice.String()}) {
			t.Errorf("the note's effective audience = %v, want its author alone", got)
		}
		if _, err := db.Note.GetOne(as(bob), *note.Id, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("another user's read of a private note = %v, want ErrNotFound", err)
		}
		// A create that names them keeps what it names.
		named, err := db.Note.CreateOne(as(alice), &NoteCreate{Author: alice, Body: "Shared.", Audience: "everyone", AgentAccess: "none"})
		if err != nil {
			t.Fatalf("create a note that names its audience: %v", err)
		}
		if named.Audience != "everyone" || named.AgentAccess != "none" {
			t.Errorf("the named note's audience is %q and %q, want everyone and none", named.Audience, named.AgentAccess)
		}
	})

	t.Run("the core reads a held row's audience ids as a list", func(t *testing.T) {
		private, err := db.Task.CreateOne(as(alice), &TaskCreate{Title: "Renew passport", Owner: alice, Audience: "private"})
		if err != nil {
			t.Fatalf("create a private task: %v", err)
		}
		own := coreRow(t, "Task", *private.Id).Own
		var effective []string
		if err := json.Unmarshal(own["effectiveAudience"], &effective); err != nil || !slices.Equal(effective, []string{alice.String()}) {
			t.Errorf("the core's effectiveAudience of a private task = %s, %v; want [%s]", own["effectiveAudience"], err, alice)
		}
		// Its owner's operations read the row whole, its ids among it.
		moved, err := db.Task.Status().Transition(as(alice), *private.Id, TaskStatusTransitionParams{To: "done"})
		if err != nil || string(moved.From) != "open" || string(moved.To) != "done" {
			t.Fatalf("the owner's transition of a private task = %+v, %v", moved, err)
		}
		if _, err := db.Task.Comments().Post(as(alice), *private.Id, TaskCommentsPostParams{Body: "Done on Tuesday."}); err != nil {
			t.Fatalf("the owner's post on a private task: %v", err)
		}
		// A named row's ids, its owner's and those it names, read alike,
		// and an update through the core writes them back.
		named, err := db.Task.CreateOne(as(alice), &TaskCreate{Title: "Book flights", Owner: alice, Audience: "named", AudienceNamed: []types.IdentityUUID{bob}})
		if err != nil {
			t.Fatalf("create a named task: %v", err)
		}
		own = coreRow(t, "Task", *named.Id).Own
		var namedIDs []string
		if err := json.Unmarshal(own["audienceNamed"], &namedIDs); err != nil || !slices.Equal(namedIDs, []string{bob.String()}) {
			t.Errorf("the core's audienceNamed = %s, %v; want [%s]", own["audienceNamed"], err, bob)
		}
		effective = nil
		if err := json.Unmarshal(own["effectiveAudience"], &effective); err != nil {
			t.Fatalf("the core's effectiveAudience of a named task = %s: %v", own["effectiveAudience"], err)
		}
		slices.Sort(effective)
		if want := ids([]types.IdentityUUID{alice, bob}); !slices.Equal(effective, want) {
			t.Errorf("the core's effectiveAudience of a named task = %v, want %v", effective, want)
		}
		title := "Book the flights"
		updated, err := db.Task.UpdateOne(as(bob), *named.Id, &TaskUpdate{Title: &title})
		if err != nil || updated.Title != title || !slices.Equal(ids(updated.EffectiveAudience), ids([]types.IdentityUUID{alice, bob})) {
			t.Fatalf("a named user's update of the task = %+v, %v", updated, err)
		}
	})

	t.Run("a deriver fills a private note's summary and another user still cannot read it", func(t *testing.T) {
		note, err := db.Note.CreateOne(as(alice), &NoteCreate{Author: alice, Body: "Call the plumber about the leak under the sink."})
		if err != nil {
			t.Fatalf("create a private note: %v", err)
		}
		// The deriver acts as itself, holding the Derived config's
		// permission, as a deriver's generated main.go opens the
		// repositories: a behavior caller with no principal, which reads
		// every row (D180).
		deriving := WithBehaviorCaller(ctx, rules.Caller{Subject: "fixture-api-fill-notes", Permissions: []string{"notes.derive"}})
		tables := []deriver.Table{
			deriver.TableOf("Note", db.GetNoteRepository().Derived().StaleDerivations, db.GetNoteRepository().Derived().SettleDerivations),
		}
		var asked []string
		answer := deriver.Methods("FillNotes", map[string]func(context.Context, deriver.Item) (deriver.Answer, error){
			"Note.summary": deriver.Method(func(_ context.Context, item deriver.DerivedItem[noteInputs]) (deriver.DerivedAnswer[string], error) {
				if item.Inputs.Body == nil {
					return deriver.Failed[string]("inputs", "no body"), nil
				}
				asked = append(asked, item.ID)
				return deriver.Value("A leak to fix."), nil
			}),
		})
		summary := func() string {
			got, err := db.Note.GetOne(as(alice), *note.Id, nil)
			if err != nil {
				t.Fatalf("the author's read of the note: %v", err)
			}
			return got.Summary
		}
		runCtx, cancel := context.WithCancel(deriving)
		defer cancel()
		stopped := make(chan error, 1)
		go func() {
			stopped <- deriver.Run(runCtx, deriver.Options{Tables: tables, Answer: answer, Idle: 50 * time.Millisecond, Permanent: func(err error) bool {
				refusal, ok := rules.AsRefusal(err)
				return ok && refusal.Code != rules.CodeBusy
			}})
		}()
		deadline := time.Now().Add(30 * time.Second)
		for summary() != "A leak to fix." {
			select {
			case err := <-stopped:
				t.Fatalf("the deriver stopped before it filled the note: %v", err)
			case <-time.After(20 * time.Millisecond):
			}
			if time.Now().After(deadline) {
				t.Fatal("the deriver did not fill the note in thirty seconds")
			}
		}
		cancel()
		if err := <-stopped; err != nil && !errors.Is(err, context.Canceled) {
			t.Fatalf("Run = %v", err)
		}
		if !slices.Contains(asked, note.Id.String()) {
			t.Errorf("the summary's method was asked of %v, want the note %s among them", asked, note.Id)
		}
		page, err := db.GetNoteRepository().Derived().StaleDerivations(deriving, NoteDerivedStaleDerivationsParams{})
		if err != nil {
			t.Fatalf("pull: %v", err)
		}
		for _, item := range page.Items {
			if item.Id == note.Id.String() {
				t.Errorf("the note's %s is stale after it settled", item.Field)
			}
		}
		// The note stays its author's alone.
		got, err := db.Note.GetOne(as(alice), *note.Id, nil)
		if err != nil || got.Audience != "private" || !slices.Equal(ids(got.EffectiveAudience), []string{alice.String()}) {
			t.Errorf("the author's note after the deriver = %+v, %v; want private to her", got, err)
		}
		if _, err := db.Note.GetOne(as(bob), *note.Id, nil); !errors.Is(err, ErrNotFound) {
			t.Errorf("another user's read of the filled note = %v, want ErrNotFound", err)
		}
		found, _, err := db.Note.FindMany(as(bob), nil, &NoteFindOptions{Fields: NoteFields{Id: true}})
		if err != nil {
			t.Fatalf("another user's list of notes: %v", err)
		}
		for _, other := range found {
			if *other.Id == *note.Id {
				t.Errorf("another user's list of notes holds the private note %s", note.Id)
			}
		}
	})
}
`
