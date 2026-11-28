// fixture-comments-inbox-db end to end in the Go repositories and the HTTP
// runtime's notify package (#713, D153, D154, D157), run by
// TestGoCommentsInboxEndToEnd: tickets, some private to their owner, whose
// Comments emits its mentions and replies, and the notifier's tables and
// dispatch queue. The generated create.sql is applied in a schema of the
// test's own on Postgres, and the cases are the TypeScript repositories'
// (tsgen's comments_inbox.test.ts): each event the core writes to the
// ticket's event queue is claimed and handed to the notifier in the
// transaction that spends its claim, as a behavior worker's Workers
// method does, and the notifier's routes reach the subjects each event
// names who may read the ticket.
package orm_test

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"slices"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/schema/go/access"

	orm "example.com/schemas/orm/fixture-comments-inbox-db"
	types "example.com/schemas/types/go/fixture-comments-inbox-db"
)

var system = orm.AsSystem(context.Background())

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func check(t *testing.T, err error) {
	t.Helper()
	if err != nil {
		t.Fatal(err)
	}
}

// world is a schema of its own holding fixture-comments-inbox-db's
// tables, the notifier over it, three people and two tickets of Ann's:
// open, which everyone reads, and closed, which she alone reads.
type world struct {
	t             *testing.T
	pool          *pgxpool.Pool
	db            *orm.Database
	notifier      *notify.Notifier
	ann, ben, cal types.IdentityUUID
	open, closed  types.IdentityUUID
}

func newWorld(t *testing.T) *world {
	t.Helper()
	target := os.Getenv("COMMENTS_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set COMMENTS_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin := must(pgxpool.New(ctx, target))
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("comments_inbox_go_%d", time.Now().UnixNano())
	must(admin.Exec(ctx, "CREATE SCHEMA "+schema))
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg := must(pgxpool.ParseConfig(target))
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool := must(pgxpool.NewWithConfig(ctx, cfg))
	t.Cleanup(pool.Close)
	if _, err := pool.Exec(ctx, string(must(os.ReadFile("testdata/create.sql")))); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db := must(orm.ConnectWithPool(pool))
	w := &world{t: t, pool: pool, db: db, notifier: must(notify.New(db.Notifications(), notify.Options{}))}
	account := func(email string) types.IdentityUUID {
		return *must(db.GetAccountRepository().CreateOne(system, &types.Account{Email: types.ContactEmail(email)})).Id
	}
	w.ann, w.ben, w.cal = account("ann@example.test"), account("ben@example.test"), account("cal@example.test")
	ticket := func(title, audience string) types.IdentityUUID {
		return *must(db.GetTicketRepository().CreateOne(system, &orm.TicketCreate{Title: title, Owner: w.ann, Audience: audience, AgentAccess: "followRole"})).Id
	}
	w.open, w.closed = ticket("Boiler pressure low", "everyone"), ticket("Annual review", "private")
	// The app's routes: a mention and a reply each reach the subjects the
	// event names (#713).
	w.exec(`INSERT INTO notice_route (name, event_kind, recipients) VALUES ('Mentions', 'comments.mentioned', '[{"payload":"subjects"}]'::jsonb)`)
	w.exec(`INSERT INTO notice_route (name, event_kind, urgency, recipients) VALUES ('Replies', 'comments.replied', 'digest', '[{"payload":"subjects"}]'::jsonb)`)
	return w
}

func (w *world) exec(sql string, args ...any) {
	w.t.Helper()
	if _, err := w.pool.Exec(context.Background(), sql, args...); err != nil {
		w.t.Fatalf("%s: %v", sql, err)
	}
}

// as is a context acting as user: the caller its behaviors' commands run
// as, whose reads keep to what it sees (D156).
func as(user types.IdentityUUID) context.Context {
	return orm.WithPrincipal(orm.WithUserID(context.Background(), user), access.Principal{Subject: user.String()})
}

func (w *world) comments() orm.TicketCommentsOperations {
	return w.db.GetTicketRepository().Comments()
}

func (w *world) post(user, ticket types.IdentityUUID, params orm.TicketCommentsPostParams) *orm.TicketCommentsCommentMessage {
	w.t.Helper()
	posted, err := w.comments().Post(as(user), ticket, params)
	check(w.t, err)
	return posted
}

// deliver hands every event the tickets' Comments emitted to the
// notifier, each in the transaction that spends its claim, then runs the
// dispatch queue until it is empty; it returns the kinds handed.
func (w *world) deliver() []string {
	w.t.Helper()
	ctx := context.Background()
	behaviors := orm.WithBehaviorCaller(system, rules.Caller{Subject: "behavior-worker"})
	var kinds []string
	for round := 0; round < 20; round++ {
		claims := must(w.db.TicketEventQueue().Claim(ctx, 20, time.Minute))
		if len(claims) == 0 {
			break
		}
		for _, claim := range claims {
			event := claim.Message
			kinds = append(kinds, event.Kind)
			must(w.db.GetTicketRepository().RunEvent(behaviors, event, &rules.Claim{ID: claim.ID, Token: claim.Token}, func(ctx context.Context, tx orm.TxInterface) error {
				var payload any
				if event.Payload != nil {
					check(w.t, json.Unmarshal(*event.Payload, &payload))
				}
				_, err := w.notifier.FromBehaviorEvent(ctx, tx, "Ticket", "comments", notify.BehaviorEvent{Kind: event.Kind, Key: event.RowKey.String(), Payload: payload})
				return err
			}))
		}
	}
	queue := w.notifier.WorkerQueue()
	for round := 0; round < 50; round++ {
		claims := must(queue.Claim(ctx, 20, 30*time.Second))
		if len(claims) == 0 {
			return kinds
		}
		for _, claim := range claims {
			if err := claim.Handle(ctx); err != nil {
				w.t.Fatalf("the dispatch queue's message %s: %v", claim.ID, err)
			}
			check(w.t, queue.Complete(ctx, claim.ID, claim.Token))
		}
	}
	w.t.Fatal("the dispatch queue did not drain")
	return kinds
}

// noticesOf is the user's notices, oldest first.
func (w *world) noticesOf(user types.IdentityUUID) []notify.Notice {
	w.t.Helper()
	notices := must(w.notifier.Inbox(context.Background(), notify.User(user.String()), nil, 50))
	slices.SortFunc(notices, func(a, b notify.Notice) int { return int(a.Reference - b.Reference) })
	return notices
}

// summary is each notice's kind, ticket and message.
func summary(notices []notify.Notice) []string {
	out := []string{}
	for _, n := range notices {
		out = append(out, fmt.Sprintf("%s %s %v", n.EventKind, n.SubjectKey, n.Payload["message"]))
	}
	return out
}

func notice(kind string, ticket types.IdentityUUID, message int64) string {
	return fmt.Sprintf("%s %s %d", kind, ticket, message)
}

func ptr[T any](v T) *T { return &v }

func TestCommentsInbox(t *testing.T) {
	t.Run("a mention reaches the mentioned principal's inbox, and a principal who cannot read the row is not notified", func(t *testing.T) {
		w := newWorld(t)
		posted := w.post(w.ann, w.open, orm.TicketCommentsPostParams{Body: "Can you look at the gauge?", Mentions: []string{w.ben.String(), w.cal.String(), w.ann.String()}})
		// Ben may not read the closed ticket: mentioned there, he hears nothing.
		w.post(w.ann, w.closed, orm.TicketCommentsPostParams{Body: "Notes for the review", Mentions: []string{w.ben.String()}})
		if kinds := w.deliver(); !slices.Equal(kinds, []string{"mentioned", "mentioned"}) {
			t.Fatalf("the events: %v", kinds)
		}
		want := []string{notice("comments.mentioned", w.open, posted.Id)}
		for _, who := range []types.IdentityUUID{w.ben, w.cal} {
			if got := summary(w.noticesOf(who)); !slices.Equal(got, want) {
				t.Fatalf("%s's notices = %v, want %v", who, got, want)
			}
		}
		if got := w.noticesOf(w.ann); len(got) != 0 {
			t.Fatalf("the author's notices = %v", summary(got))
		}
		payload := w.noticesOf(w.ben)[0].Payload
		if payload["author"] != w.ann.String() || fmt.Sprint(payload["subjects"]) != fmt.Sprint([]any{w.ben.String(), w.cal.String()}) || payload["excerpt"] != "Can you look at the gauge?" {
			t.Fatalf("the notice's payload: %v", payload)
		}
		var held int
		check(t, w.pool.QueryRow(context.Background(), `SELECT count(*) FROM notice WHERE subject_key = $1`, w.closed.String()).Scan(&held))
		if held != 0 {
			t.Fatalf("the closed ticket has %d notices", held)
		}
	})

	t.Run("a reply reaches the thread's followers, a follower it mentions once, and never its author", func(t *testing.T) {
		w := newWorld(t)
		top := w.post(w.ben, w.open, orm.TicketCommentsPostParams{Body: "The pump hums."})
		second := w.post(w.ann, w.open, orm.TicketCommentsPostParams{Body: "Since when?", Thread: ptr(top.Id)})
		third := w.post(w.cal, w.open, orm.TicketCommentsPostParams{Body: "Since Monday.", Thread: ptr(top.Id), Mentions: []string{w.ann.String()}})
		if kinds := w.deliver(); !slices.Equal(kinds, []string{"replied", "mentioned", "replied"}) {
			t.Fatalf("the events: %v", kinds)
		}
		if got, want := summary(w.noticesOf(w.ben)), []string{notice("comments.replied", w.open, second.Id), notice("comments.replied", w.open, third.Id)}; !slices.Equal(got, want) {
			t.Fatalf("Ben's notices = %v, want %v", got, want)
		}
		if got, want := summary(w.noticesOf(w.ann)), []string{notice("comments.mentioned", w.open, third.Id)}; !slices.Equal(got, want) {
			t.Fatalf("Ann's notices = %v, want %v", got, want)
		}
		if got := w.noticesOf(w.cal); len(got) != 0 {
			t.Fatalf("Cal's notices = %v", summary(got))
		}
		if replied := w.noticesOf(w.ben)[0]; replied.Urgency != "digest" {
			t.Fatalf("a reply's urgency = %q", replied.Urgency)
		}
	})

	t.Run("markRead clears a reader's unread, and follow turns a thread's replies on and off", func(t *testing.T) {
		w := newWorld(t)
		unread := func(user types.IdentityUUID) string {
			t.Helper()
			view := must(w.comments().View(as(user), w.open))
			return string(view["unread"])
		}
		first := w.post(w.ann, w.open, orm.TicketCommentsPostParams{Body: "Gauge reads 0.8 bar."})
		if got := unread(w.cal); got != "0" {
			t.Fatalf("unread before any read = %s", got)
		}
		read := must(w.comments().MarkRead(as(w.cal), w.open, orm.TicketCommentsMarkReadParams{UpTo: first.Id}))
		if read.ReadUpTo != 1 || read.Unread != 0 {
			t.Fatalf("markRead = %+v", read)
		}
		second := w.post(w.ann, w.open, orm.TicketCommentsPostParams{Body: "Refilled to 1.4."})
		third := w.post(w.ann, w.open, orm.TicketCommentsPostParams{Body: "Holding."})
		if got := unread(w.cal); got != "2" {
			t.Fatalf("Cal's unread = %s", got)
		}
		if got := unread(w.ann); got != "0" {
			t.Fatalf("the author's unread = %s", got)
		}
		if read := must(w.comments().MarkRead(as(w.cal), w.open, orm.TicketCommentsMarkReadParams{UpTo: second.Id})); read.ReadUpTo != 2 || read.Unread != 1 {
			t.Fatalf("markRead = %+v", read)
		}
		if read := must(w.comments().MarkRead(as(w.cal), w.open, orm.TicketCommentsMarkReadParams{UpTo: third.Id})); read.ReadUpTo != 3 || read.Unread != 0 {
			t.Fatalf("markRead = %+v", read)
		}
		if got := unread(w.cal); got != "0" {
			t.Fatalf("Cal's unread after reading = %s", got)
		}
		// The view reads as its caller: Ben may not read the closed ticket.
		if _, err := w.comments().View(as(w.ben), w.closed); err == nil {
			t.Fatal("Ben read the closed ticket's view")
		}
		if followed := must(w.comments().Follow(as(w.cal), w.open, orm.TicketCommentsFollowParams{Thread: first.Id, On: true})); !followed.Following {
			t.Fatalf("follow = %+v", followed)
		}
		reply := w.post(w.ben, w.open, orm.TicketCommentsPostParams{Body: "Valve was shut.", Thread: ptr(first.Id)})
		if followed := must(w.comments().Follow(as(w.cal), w.open, orm.TicketCommentsFollowParams{Thread: first.Id, On: false})); followed.Following {
			t.Fatalf("unfollow = %+v", followed)
		}
		later := w.post(w.ben, w.open, orm.TicketCommentsPostParams{Body: "Opened it.", Thread: ptr(first.Id)})
		w.deliver()
		if got, want := summary(w.noticesOf(w.cal)), []string{notice("comments.replied", w.open, reply.Id)}; !slices.Equal(got, want) {
			t.Fatalf("Cal's notices = %v, want %v", got, want)
		}
		if got, want := summary(w.noticesOf(w.ann)), []string{notice("comments.replied", w.open, reply.Id), notice("comments.replied", w.open, later.Id)}; !slices.Equal(got, want) {
			t.Fatalf("Ann's notices = %v, want %v", got, want)
		}
	})

	t.Run("a message posted for a sender over a channel records its via and its for", func(t *testing.T) {
		w := newWorld(t)
		// An agent run writes in Ben's name a text Ben sent over SMS.
		agent := orm.WithBehaviorCaller(as(w.ann), rules.Caller{Subject: "run-1", Kind: "agent", ActingFor: w.ben.String(), Channel: "sms"})
		posted := must(w.comments().Post(agent, w.open, orm.TicketCommentsPostParams{Body: "Running late, there by 4.", Mentions: []string{w.cal.String(), w.ben.String()}}))
		if posted.Author != "run-1" || posted.For == nil || *posted.For != w.ben.String() || posted.Via == nil || *posted.Via != "sms" {
			t.Fatalf("the posted message: %+v", posted)
		}
		read := must(w.comments().GetMessage(as(w.cal), w.open, orm.TicketCommentsGetMessageParams{Message: posted.Id}))
		if read.Via == nil || *read.Via != "sms" || read.For == nil || *read.For != w.ben.String() {
			t.Fatalf("the message read back: %+v", read)
		}
		var author, forSubject, via string
		check(t, w.pool.QueryRow(context.Background(), `SELECT author, for_subject, via FROM ticket_comments_messages`).Scan(&author, &forSubject, &via))
		if author != "run-1" || forSubject != w.ben.String() || via != "sms" {
			t.Fatalf("the stored row: %s %s %s", author, forSubject, via)
		}
		if plain := w.post(w.ann, w.open, orm.TicketCommentsPostParams{Body: "Thanks."}); plain.Via != nil || plain.For != nil {
			t.Fatalf("a call over the API: %+v", plain)
		}
		w.deliver()
		if got := w.noticesOf(w.ben); len(got) != 0 {
			t.Fatalf("Ben's notices = %v", summary(got))
		}
		payload := w.noticesOf(w.cal)[0].Payload
		if payload["author"] != "run-1" || payload["forSubject"] != w.ben.String() || payload["via"] != "sms" || fmt.Sprint(payload["subjects"]) != fmt.Sprint([]any{w.cal.String()}) {
			t.Fatalf("Cal's notice's payload: %v", payload)
		}
	})
}
