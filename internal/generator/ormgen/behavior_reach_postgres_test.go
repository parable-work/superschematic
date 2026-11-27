package ormgen

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
)

// behaviorReachFixture is the DB service whose @audience table composes
// behaviors (D156, D103): a Task its owner keeps private or shows to the
// principals it names, whose status is a Workflow and which holds a
// conversation, as tsgen's TestAudienceBehaviorsEndToEnd runs it.
const behaviorReachFixture = "fixture-audience-behaviors-db"

// behaviorReachORMCases are the cases behaviorReachORMTest runs over
// fixture-audience-behaviors-db, by name: tsgen's audience_behaviors.test.ts
// holds the TypeScript repositories to the first three, and the Rust
// repositories' audience_rules.rs runs all five.
var behaviorReachORMCases = []string{
	"a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing",
	"a private row's behaviors answer its owner",
	"a named row's behaviors answer the principals it names",
	"a party whose grant reaches the row runs its behaviors, and one whose grant reaches another row finds it not there",
	"an agent run reads a read-only row's conversation, and its moves and posts are not found",
}

// TestBehaviorOperationsKeepToTheCallerOnPostgres holds the generated Go
// ORM's behavior operations on a held table to the caller's reach (D156,
// D180), as #554 holds the TypeScript repositories': under WithPrincipal,
// a Workflow transition and a Comments post, edit, delete and list of a
// row the caller does not see, or for one that writes may not write, is
// ErrNotFound before the core runs, wrapping the core's not_found of a row
// no live row holds, which a route answers 404, and writes nothing, as
// UpdateOne and DeleteOne are. A private row answers its owner, a named
// row the principals it names, a party the row its grant names, and an
// agent run with read-only access its conversation's reads alone. It loads
// fixture-audience-behaviors-db with the core's registry, generates its Go
// types, ORM and DDL into a temporary tree, and runs behaviorReachORMTest
// on the Postgres at SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL; every case
// must pass. The module links the core's static archive, which
// scripts/rules-archive.sh stages; the test skips without it, without the
// database and in -short mode.
func TestBehaviorOperationsKeepToTheCallerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated ORM's behavior operations under per-record access in -short mode")
	}
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the generated ORM's behavior operations under per-record access against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}
	ormDir := generateBehaviorORMModule(t, paths, behaviorReachFixture)
	if err := os.WriteFile(filepath.Join(ormDir, "behavior_reach_test.go"), []byte(behaviorReachORMTest), 0o644); err != nil {
		t.Fatalf("write the reach test: %v", err)
	}
	out := runORMModule(t, ormDir)
	for _, name := range behaviorReachORMCases {
		if want := "--- PASS: TestBehaviorOperationsKeepToTheCaller/" + strings.ReplaceAll(name, " ", "_"); !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q:\n%s", name, out)
		}
	}
}

// behaviorReachORMTest is the generated module's test: tasks written with
// no principal, which reads and writes every row, a named row's audience
// set with SQL, then each behavior called under a principal, with the rows
// read back past the repositories with SQL.
const behaviorReachORMTest = `package orm

import (
	"context"
	"errors"
	"fmt"
	"os"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-audience-behaviors-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
)

func TestBehaviorOperationsKeepToTheCaller(t *testing.T) {
	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the behavior operations against Postgres")
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatalf("read create.sql: %v", err)
	}
	background := context.Background()
	base, err := pgxpool.New(background, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_behavior_reach_%d", time.Now().UnixNano())
	if _, err := base.Exec(background, "CREATE SCHEMA "+schema); err != nil {
		t.Fatalf("create schema: %v", err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatalf("parse database URL: %v", err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(background, cfg)
	if err != nil {
		t.Fatalf("connect schema pool: %v", err)
	}
	if _, err := pool.Exec(background, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatalf("connect generated orm: %v", err)
	}
	defer db.Close()

	id := func(t *testing.T, u uuid.UUID) types.IdentityUUID {
		t.Helper()
		parsed, err := types.ParseIdentityUUID(u.String())
		if err != nil {
			t.Fatalf("an id: %v", err)
		}
		return parsed
	}
	alice, bob, carol := uuid.New(), uuid.New(), uuid.New()
	// No principal: the writes that set the rows up read and write every
	// row, as a job's do.
	setup := WithUserID(background, id(t, alice))
	as := func(principal access.Principal) context.Context {
		return WithPrincipal(background, principal)
	}
	asUser := func(who uuid.UUID) context.Context {
		return as(access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	task := func(t *testing.T, title, audience, agents string) types.IdentityUUID {
		t.Helper()
		row, err := db.Task.CreateOne(setup, &TaskCreate{Title: title, Owner: id(t, alice), Audience: audience, AgentAccess: agents})
		if err != nil {
			t.Fatalf("create the task %s: %v", title, err)
		}
		return *row.Id
	}
	// A named row's audience is set past the repositories, so the rows a
	// case reaches need no list written through the core.
	named := func(t *testing.T, title string, who uuid.UUID) types.IdentityUUID {
		t.Helper()
		key := task(t, title, "private", "followRole")
		if _, err := pool.Exec(background, "UPDATE task SET audience = 'named', audience_named = ARRAY[$1::uuid] WHERE id = $2", who, key.ToUUID()); err != nil {
			t.Fatalf("name %s on %s: %v", who, title, err)
		}
		return key
	}
	status := func(t *testing.T, key types.IdentityUUID) string {
		t.Helper()
		var state string
		if err := pool.QueryRow(background, "SELECT status FROM task WHERE id = $1", key.ToUUID()).Scan(&state); err != nil {
			t.Fatalf("read the status: %v", err)
		}
		return state
	}
	// The bodies of a row's messages, read past the repositories.
	bodies := func(t *testing.T, key types.IdentityUUID) []string {
		t.Helper()
		rows, err := pool.Query(background, "SELECT coalesce(body, '') FROM task_comments_messages WHERE task_id = $1 ORDER BY created_at, message", key.ToUUID())
		if err != nil {
			t.Fatalf("read the messages: %v", err)
		}
		defer rows.Close()
		var out []string
		for rows.Next() {
			var body string
			if err := rows.Scan(&body); err != nil {
				t.Fatal(err)
			}
			out = append(out, body)
		}
		return out
	}
	// The core's not_found of a row no live row holds, wrapped in
	// ErrNotFound, which a route answers 404.
	notFound := func(t *testing.T, what string, err error) {
		t.Helper()
		refusal, ok := rules.AsRefusal(err)
		if !errors.Is(err, ErrNotFound) || !ok || refusal.Code != rules.CodeNotFound || refusal.Status != 404 {
			t.Fatalf("%s: %v, want ErrNotFound and the core's not_found", what, err)
		}
	}
	// Every behavior call of key as ctx's caller, a list of the
	// conversation among them, and an update and a delete, is not found,
	// message the id of a message on it.
	refused := func(t *testing.T, ctx context.Context, key types.IdentityUUID, message int64) {
		t.Helper()
		_, err := db.Task.Status().Transition(ctx, key, TaskStatusTransitionParams{To: TaskStatusStateDone})
		notFound(t, "transition", err)
		_, err = db.Task.Comments().Post(ctx, key, TaskCommentsPostParams{Body: "Can I see this?"})
		notFound(t, "post", err)
		body := "Mine now"
		_, err = db.Task.Comments().Edit(ctx, key, TaskCommentsEditParams{Message: message, Body: &body})
		notFound(t, "edit", err)
		_, err = db.Task.Comments().DeleteMessage(ctx, key, TaskCommentsDeleteMessageParams{Message: message})
		notFound(t, "delete a message", err)
		_, err = db.Task.Comments().ListMessages(ctx, key, TaskCommentsListMessagesParams{})
		notFound(t, "list the messages", err)
		title := "Mine now"
		if _, err := db.Task.UpdateOne(ctx, key, &TaskUpdate{Title: &title}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update: %v, want ErrNotFound", err)
		}
		if err := db.Task.DeleteOne(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete: %v, want ErrNotFound", err)
		}
	}
	// A transition, a post, an edit and a delete of the message posted,
	// and a list, of key as ctx's caller, author, which the row answers.
	answers := func(t *testing.T, ctx context.Context, key types.IdentityUUID, author string) {
		t.Helper()
		moved, err := db.Task.Status().Transition(ctx, key, TaskStatusTransitionParams{To: TaskStatusStateDone})
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
		if moved.From != TaskStatusStateOpen || moved.To != TaskStatusStateDone {
			t.Fatalf("transition moved %s to %s", moved.From, moved.To)
		}
		posted, err := db.Task.Comments().Post(ctx, key, TaskCommentsPostParams{Body: "Done on Tuesday."})
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if posted.Author != author {
			t.Fatalf("the message's author is %q, want %q", posted.Author, author)
		}
		body := "Done on Wednesday."
		if _, err := db.Task.Comments().Edit(ctx, key, TaskCommentsEditParams{Message: posted.Id, Body: &body}); err != nil {
			t.Fatalf("edit: %v", err)
		}
		page, err := db.Task.Comments().ListMessages(ctx, key, TaskCommentsListMessagesParams{})
		if err != nil {
			t.Fatalf("list the messages: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Id != posted.Id || page.Items[0].Body != body {
			t.Fatalf("the conversation lists %+v", page.Items)
		}
		if _, err := db.Task.Comments().DeleteMessage(ctx, key, TaskCommentsDeleteMessageParams{Message: posted.Id}); err != nil {
			t.Fatalf("delete the message: %v", err)
		}
	}
	// A message the setup posts, as the row's owner, which no reach holds.
	post := func(t *testing.T, key types.IdentityUUID) int64 {
		t.Helper()
		posted, err := db.Task.Comments().Post(setup, key, TaskCommentsPostParams{Body: "First."})
		if err != nil {
			t.Fatalf("post the first message: %v", err)
		}
		return posted.Id
	}

	t.Run("a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing", func(t *testing.T) {
		mine := task(t, "Renew passport", "private", "followRole")
		bobs := named(t, "Book flights", bob)
		for _, key := range []types.IdentityUUID{mine, bobs} {
			// The first message a post would hold: the reach is checked
			// before the core reads the conversation.
			refused(t, asUser(carol), key, 1)
			if status(t, key) != "open" {
				t.Fatal("a transition the caller may not make moved the row")
			}
			if got := bodies(t, key); len(got) != 0 {
				t.Fatalf("a post the caller may not make wrote: %v", got)
			}
		}
	})

	t.Run("a private row's behaviors answer its owner", func(t *testing.T) {
		key := task(t, "Renew passport", "private", "followRole")
		answers(t, asUser(alice), key, fmt.Sprint(id(t, alice)))
		if status(t, key) != "done" {
			t.Fatal("the owner's transition did not move the row")
		}
	})

	t.Run("a named row's behaviors answer the principals it names", func(t *testing.T) {
		key := named(t, "Book flights", bob)
		answers(t, asUser(bob), key, fmt.Sprint(id(t, bob)))
		if status(t, key) != "done" {
			t.Fatal("the named principal's transition did not move the row")
		}
	})

	t.Run("a party whose grant reaches the row runs its behaviors, and one whose grant reaches another row finds it not there", func(t *testing.T) {
		reached, other := task(t, "Fix the gate", "everyone", "followRole"), task(t, "Paint the fence", "everyone", "followRole")
		message := post(t, other)
		party := uuid.New()
		asParty := as(access.Principal{
			Subject: party.String(),
			Kind:    access.KindParty,
			Grants:  []access.GrantReach{{ID: "g1", TargetType: "Task", TargetID: reached.String()}},
		})
		answers(t, asParty, reached, fmt.Sprint(id(t, party)))
		refused(t, asParty, other, message)
		if status(t, other) != "open" || len(bodies(t, other)) != 1 {
			t.Fatal("a call the party's grant does not reach wrote")
		}
	})

	t.Run("an agent run reads a read-only row's conversation, and its moves and posts are not found", func(t *testing.T) {
		key := task(t, "Water the plants", "everyone", "readOnly")
		message := post(t, key)
		asAgent := as(access.Principal{
			Subject:   uuid.New().String(),
			Kind:      access.KindAgent,
			ActingFor: &access.ActingFor{Kind: access.KindUser, Subject: alice.String()},
		})
		page, err := db.Task.Comments().ListMessages(asAgent, key, TaskCommentsListMessagesParams{})
		if err != nil {
			t.Fatalf("list the messages: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Id != message {
			t.Fatalf("the conversation lists %+v", page.Items)
		}
		_, err = db.Task.Status().Transition(asAgent, key, TaskStatusTransitionParams{To: TaskStatusStateDone})
		notFound(t, "transition", err)
		_, err = db.Task.Comments().Post(asAgent, key, TaskCommentsPostParams{Body: "On it."})
		notFound(t, "post", err)
		if status(t, key) != "open" || len(bodies(t, key)) != 1 {
			t.Fatal("a write the agent run may not make wrote")
		}
	})
}
`
