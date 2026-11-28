package orm

// The Go repositories of fixture-audience-behaviors-db on a SQLite file,
// whose @audience tables compose behaviors (D156, D103, D201): each
// behavior operation of a row is held to its caller's reach before the
// behaviors' core runs, as on Postgres (behavior_reach_test.go, ormgen's
// TestBehaviorOperationsKeepToTheCallerOnPostgres), and a table
// operation's answer names no row the caller does not see.

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	types "example.com/schemas/types/go/fixture-audience-behaviors-db"
	"github.com/google/uuid"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	_ "modernc.org/sqlite"
)

func TestBehaviorOperationsKeepToTheCallerOnSQLite(t *testing.T) {
	background := context.Background()
	sqliteDDL, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", SQLiteDSN(filepath.Join(t.TempDir(), "reach.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(background, string(sqliteDDL)); err != nil {
		t.Fatalf("the SQLite DDL: %v", err)
	}
	db, err := OpenSQLite(background, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	tasks := db.GetTaskRepository()

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
	task := func(t *testing.T, title, audience, agents string, named ...uuid.UUID) types.IdentityUUID {
		t.Helper()
		input := &TaskCreate{Title: title, Owner: id(t, alice), Audience: audience, AgentAccess: agents}
		for _, who := range named {
			input.AudienceNamed = append(input.AudienceNamed, id(t, who))
		}
		row, err := tasks.CreateOne(setup, input)
		if err != nil {
			t.Fatalf("create the task %s: %v", title, err)
		}
		return *row.Id
	}
	status := func(t *testing.T, key types.IdentityUUID) string {
		t.Helper()
		var state string
		if err := raw.QueryRowContext(background, "SELECT status FROM task WHERE id = ?1", key.ToUUID().String()).Scan(&state); err != nil {
			t.Fatalf("read the status: %v", err)
		}
		return state
	}
	// The number of a row's messages, read past the repositories.
	messages := func(t *testing.T, key types.IdentityUUID) int {
		t.Helper()
		var n int
		if err := raw.QueryRowContext(background, "SELECT count(*) FROM task_comments_messages WHERE task_id = ?1", key.ToUUID().String()).Scan(&n); err != nil {
			t.Fatalf("read the messages: %v", err)
		}
		return n
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
	refused := func(t *testing.T, ctx context.Context, key types.IdentityUUID, message int64) {
		t.Helper()
		_, err := tasks.Status().Transition(ctx, key, TaskStatusTransitionParams{To: TaskStatusStateDone})
		notFound(t, "transition", err)
		_, err = tasks.Comments().Post(ctx, key, TaskCommentsPostParams{Body: "Can I see this?"})
		notFound(t, "post", err)
		body := "Mine now"
		_, err = tasks.Comments().Edit(ctx, key, TaskCommentsEditParams{Message: message, Body: &body})
		notFound(t, "edit", err)
		_, err = tasks.Comments().DeleteMessage(ctx, key, TaskCommentsDeleteMessageParams{Message: message})
		notFound(t, "delete a message", err)
		_, err = tasks.Comments().ListMessages(ctx, key, TaskCommentsListMessagesParams{})
		notFound(t, "list the messages", err)
		title := "Mine now"
		if _, err := tasks.UpdateOne(ctx, key, &TaskUpdate{Title: &title}); !errors.Is(err, ErrNotFound) {
			t.Fatalf("update: %v, want ErrNotFound", err)
		}
		if err := tasks.DeleteOne(ctx, key); !errors.Is(err, ErrNotFound) {
			t.Fatalf("delete: %v, want ErrNotFound", err)
		}
	}
	answers := func(t *testing.T, ctx context.Context, key types.IdentityUUID, author string) {
		t.Helper()
		moved, err := tasks.Status().Transition(ctx, key, TaskStatusTransitionParams{To: TaskStatusStateDone})
		if err != nil {
			t.Fatalf("transition: %v", err)
		}
		if moved.From != TaskStatusStateOpen || moved.To != TaskStatusStateDone {
			t.Fatalf("transition moved %s to %s", moved.From, moved.To)
		}
		posted, err := tasks.Comments().Post(ctx, key, TaskCommentsPostParams{Body: "Done on Tuesday."})
		if err != nil {
			t.Fatalf("post: %v", err)
		}
		if posted.Author != author {
			t.Fatalf("the message's author is %q, want %q", posted.Author, author)
		}
		body := "Done on Wednesday."
		if _, err := tasks.Comments().Edit(ctx, key, TaskCommentsEditParams{Message: posted.Id, Body: &body}); err != nil {
			t.Fatalf("edit: %v", err)
		}
		page, err := tasks.Comments().ListMessages(ctx, key, TaskCommentsListMessagesParams{})
		if err != nil {
			t.Fatalf("list the messages: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Id != posted.Id || page.Items[0].Body != body {
			t.Fatalf("the conversation lists %+v", page.Items)
		}
		if _, err := tasks.Comments().DeleteMessage(ctx, key, TaskCommentsDeleteMessageParams{Message: posted.Id}); err != nil {
			t.Fatalf("delete the message: %v", err)
		}
	}
	post := func(t *testing.T, key types.IdentityUUID) int64 {
		t.Helper()
		posted, err := tasks.Comments().Post(setup, key, TaskCommentsPostParams{Body: "First."})
		if err != nil {
			t.Fatalf("post the first message: %v", err)
		}
		return posted.Id
	}

	t.Run("a private row's and a named row's behaviors are not found for a user outside their audience, and write nothing", func(t *testing.T) {
		mine := task(t, "Renew passport", "private", "followRole")
		bobs := task(t, "Book flights", "named", "followRole", bob)
		for _, key := range []types.IdentityUUID{mine, bobs} {
			refused(t, asUser(carol), key, 1)
			if status(t, key) != "open" {
				t.Fatal("a transition the caller may not make moved the row")
			}
			if messages(t, key) != 0 {
				t.Fatal("a post the caller may not make wrote")
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
		key := task(t, "Book flights", "named", "followRole", bob)
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
		if status(t, other) != "open" || messages(t, other) != 1 {
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
		page, err := tasks.Comments().ListMessages(asAgent, key, TaskCommentsListMessagesParams{})
		if err != nil {
			t.Fatalf("list the messages: %v", err)
		}
		if len(page.Items) != 1 || page.Items[0].Id != message {
			t.Fatalf("the conversation lists %+v", page.Items)
		}
		_, err = tasks.Status().Transition(asAgent, key, TaskStatusTransitionParams{To: TaskStatusStateDone})
		notFound(t, "transition", err)
		_, err = tasks.Comments().Post(asAgent, key, TaskCommentsPostParams{Body: "On it."})
		notFound(t, "post", err)
		if status(t, key) != "open" || messages(t, key) != 1 {
			t.Fatal("a write the agent run may not make wrote")
		}
	})

	t.Run("pick answers a user no runner it does not see", func(t *testing.T) {
		runner, err := db.GetRunnerRepository().CreateOne(setup, &RunnerCreate{Owner: id(t, alice), Rank: 1})
		if err != nil {
			t.Fatalf("create a private runner: %v", err)
		}
		pick := func(t *testing.T, ctx context.Context) *RunnerPoolPickMember {
			t.Helper()
			picked, err := db.GetRunnerRepository().Pool().Pick(ctx, RunnerPoolPickParams{})
			if err != nil {
				t.Fatalf("pick: %v", err)
			}
			return picked.Member
		}
		if member := pick(t, asUser(bob)); member != nil {
			t.Fatalf("pick for a user outside the runner's audience = %+v, want no member", member)
		}
		for _, ctx := range []context.Context{asUser(alice), setup} {
			if member := pick(t, ctx); member == nil || member.Key != runner.Id.String() {
				t.Fatalf("pick = %+v, want the runner %s", member, runner.Id)
			}
		}
	})
}
