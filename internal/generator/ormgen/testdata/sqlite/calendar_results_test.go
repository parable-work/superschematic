// fixture-access-db's calendar end to end on a SQLite file (D152, D156,
// D186, #615): documents on the calendar on the day they were made, held
// to their folder's audience and to a party's grants, read through the
// schema runtime's crosstable over the SQLite database's CrossTables, and
// the iCalendar feed its CalendarRoutes serve at a URL that holds a
// grant's token. TestTheGoReadsAcrossTablesOnSQLite writes this file into
// the generated ORM module, applies the SQLite DDL the build writes to a
// file, and runs these cases; with the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the same cases run on Postgres
// too, with the same keys and the same clock, and each case's answers on
// the two, the feeds' text among them, are held equal.
package orm_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"

	orm "example.com/schemas/orm/fixture-access-db"
	types "example.com/schemas/types/go/fixture-access-db"
	_ "modernc.org/sqlite"
)

// calendarDB is the ORM on SQLite or on Postgres, as a case reaches it.
type calendarDB interface {
	orm.DatabaseInterface
	CrossTables() crosstable.Host
	AuthenticateGrant(ctx context.Context, token string, now time.Time) (access.GrantAuthentication, error)
}

// calendarFixture is what the cases run on: a database of either dialect,
// and a raw write of a document's creation time, which a create stamps.
type calendarFixture struct {
	db    calendarDB
	stamp func(ctx context.Context, id string, at time.Time) error
}

func openSQLite(t *testing.T) calendarFixture {
	t.Helper()
	ctx := context.Background()
	ddl, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "calendar.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("the SQLite DDL: %v", err)
	}
	db, err := orm.OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return calendarFixture{db: db, stamp: func(ctx context.Context, id string, at time.Time) error {
		_, err := raw.ExecContext(ctx, `UPDATE document SET created_at = ?1 WHERE id = ?2`, at.UTC().Format("2006-01-02T15:04:05.000Z"), id)
		return err
	}}
}

func openPostgres(t *testing.T) calendarFixture {
	t.Helper()
	target := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("calendar_sqlite_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	return calendarFixture{db: db, stamp: func(ctx context.Context, id string, at time.Time) error {
		_, err := pool.Exec(ctx, `UPDATE document SET created_at = $1 WHERE id = $2`, at, id)
		return err
	}}
}

// TestCalendarOnSQLite runs the cases on SQLite and, with a Postgres, on
// Postgres, and holds each case's answers on the two equal.
func TestCalendarOnSQLite(t *testing.T) {
	var answers [2]map[string]any
	t.Run("sqlite", func(t *testing.T) { answers[0] = calendarCases(t, openSQLite(t)) })
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Log("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the same cases on Postgres")
		return
	}
	t.Run("postgres", func(t *testing.T) { answers[1] = calendarCases(t, openPostgres(t)) })
	names := make([]string, 0, len(answers[0]))
	for name := range answers[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sqlite, _ := json.MarshalIndent(answers[0][name], "", " ")
		postgres, _ := json.MarshalIndent(answers[1][name], "", " ")
		if string(sqlite) != string(postgres) {
			t.Errorf("%s: SQLite and Postgres differ\nsqlite:   %s\npostgres: %s", name, sqlite, postgres)
		}
	}
	if !t.Failed() {
		t.Logf("SQLite and Postgres answered %d cases alike", len(names))
	}
}

func uuidOf(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var (
	ada   = uuidOf(0xa1)
	bob   = uuidOf(0xb2)
	carol = uuidOf(0xc3)
	party = uuidOf(0xd4)
)

// clock is the cases' now, noon of the day they run, the same on both
// databases: the documents are made in the two months before it, and a
// feed issued then has started.
var clock = time.Now().UTC().Truncate(24 * time.Hour).Add(12 * time.Hour)

func idOf(t *testing.T, text string) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(text)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func ptrOf[T any](v T) *T { return &v }

func user(who string) crosstable.Caller {
	return crosstable.Caller{Principal: access.Principal{Subject: who, Kind: access.KindUser}}
}

func calendarCases(t *testing.T, f calendarFixture) map[string]any {
	db := f.db
	answers := map[string]any{}
	// The writes that set the rows up are a job's, as ada.
	ctx := orm.WithUserID(context.Background(), idOf(t, ada))
	folder := func(n int, name, audience string, named ...string) types.IdentityUUID {
		t.Helper()
		row := &types.Folder{Id: ptrOf(idOf(t, uuidOf(0x100+n))), Name: name, Owner: idOf(t, ada), Audience: audience, AgentAccess: "followRole"}
		for _, who := range named {
			row.AudienceNamed = append(row.AudienceNamed, idOf(t, who))
		}
		created, err := db.GetFolderRepository().CreateOne(ctx, row)
		if err != nil {
			t.Fatalf("create the folder %s: %v", name, err)
		}
		return *created.Id
	}
	documents := 0
	document := func(title string, in *types.IdentityUUID, at time.Time) string {
		t.Helper()
		documents++
		row := &types.Document{Id: ptrOf(idOf(t, uuidOf(0x200+documents))), Title: title, Audience: "inherit", AgentAccess: "readOnly"}
		if in != nil {
			row.Folder = &types.Folder{Id: in}
		}
		created, err := db.GetDocumentRepository().CreateOne(ctx, row)
		if err != nil {
			t.Fatalf("create the document %s: %v", title, err)
		}
		if err := f.stamp(ctx, created.Id.ToUUID().String(), at); err != nil {
			t.Fatal(err)
		}
		return created.Id.String()
	}
	// listed are the documents a list read of reader shows, by key.
	listed := func(t *testing.T, reader access.Principal) []string {
		t.Helper()
		rows, _, err := db.GetDocumentRepository().FindMany(orm.WithPrincipal(context.Background(), reader), nil, &orm.DocumentFindOptions{Fields: orm.DocumentFields{}.All()})
		if err != nil {
			t.Fatal(err)
		}
		var keys []string
		for _, row := range rows {
			keys = append(keys, row.Id.String())
		}
		sort.Strings(keys)
		return keys
	}
	window := crosstable.CalendarRead{From: clock.Add(-60 * 24 * time.Hour).Format(time.RFC3339), To: clock.Format(time.RFC3339)}
	read := func(t *testing.T, caller crosstable.Caller, read crosstable.CalendarRead) crosstable.Calendar {
		t.Helper()
		out, err := crosstable.ReadCalendar(context.Background(), db.CrossTables(), caller, read, crosstable.CalendarOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	keysOf := func(c crosstable.Calendar) []string {
		var keys []string
		for _, entry := range c.Entries {
			keys = append(keys, entry.Key)
		}
		sort.Strings(keys)
		return keys
	}

	private, named, open := folder(1, "private", "private"), folder(2, "named", "named", bob), folder(3, "open", "everyone")
	day := func(n int) time.Time {
		return clock.Add(-time.Duration(n) * 24 * time.Hour).Add(time.Duration(n) * time.Minute)
	}
	document("plans", &private, day(3))
	document("minutes", &named, day(10))
	document("notice", &open, day(20))
	document("loose note", nil, day(25))
	document("before the window", &open, day(90))

	t.Run("each reader's calendar holds the documents their list reads show, by their audience", func(t *testing.T) {
		got := map[string]any{}
		for _, who := range []struct{ name, id string }{{"ada", ada}, {"bob", bob}, {"carol", carol}} {
			c := read(t, user(who.id), window)
			keys := keysOf(c)
			var inWindow []string
			for _, key := range listed(t, access.Principal{Subject: who.id, Kind: access.KindUser}) {
				if !isOld(t, db, key) {
					inWindow = append(inWindow, key)
				}
			}
			if !slices.Equal(keys, inWindow) {
				t.Errorf("%s's calendar holds %q, and their list reads show %q in the window", who.name, keys, inWindow)
			}
			for _, entry := range c.Entries {
				if entry.Source != "document" || entry.Kind != "document" || entry.Color != "documents" || entry.Type != "Document" || entry.AllDay {
					t.Errorf("an entry: %+v", entry)
				}
			}
			starts := make([]string, len(c.Entries))
			for i, entry := range c.Entries {
				starts[i] = entry.Start
			}
			if !sort.StringsAreSorted(starts) {
				t.Errorf("%s's calendar is not in start order: %q", who.name, starts)
			}
			got[who.name] = c
		}
		if n := len(got["ada"].(crosstable.Calendar).Entries); n != 4 {
			t.Errorf("ada, who owns every folder, reads %d entries in the window, want 4", n)
		}
		if n := len(got["carol"].(crosstable.Calendar).Entries); n != 1 {
			t.Errorf("carol, whom no folder names, reads %d entries, want the open folder's alone", n)
		}
		// An agent run for bob reads his documents, its agent access
		// readOnly.
		agent := crosstable.Caller{Principal: access.Principal{Subject: uuidOf(0xe5), Kind: access.KindAgent, ActingFor: &access.ActingFor{Kind: access.KindUser, Subject: bob}}}
		if keys := keysOf(read(t, agent, window)); !slices.Equal(keys, keysOf(got["bob"].(crosstable.Calendar))) {
			t.Errorf("an agent run for bob reads %q, and bob %q", keys, keysOf(got["bob"].(crosstable.Calendar)))
		}
		// A limit keeps a source to its first entries and says there are
		// more.
		limited := window
		limited.Limit = 1
		if c := read(t, user(ada), limited); len(c.Entries) != 1 || !c.More {
			t.Errorf("a limit of 1: %+v", c)
		}
		got["sources none"] = len(read(t, user(ada), crosstable.CalendarRead{From: window.From, To: window.To, Sources: []string{"no such source"}}).Entries)
		answers["audience"] = got
	})

	t.Run("a party's calendar holds the documents its grants reach and a scope of mine its own", func(t *testing.T) {
		// The party's grant reaches the open folder's documents through
		// its role's path; the audience of each holds as for anyone.
		if _, err := db.GetRoleRepository().CreateOne(ctx, &types.Role{Name: "reader", Permissions: []string{"Document.read"}, Reaches: []string{"Document:folder"}}); err != nil {
			t.Fatalf("create the role: %v", err)
		}
		_, token, err := orm.IssueGrant(ctx, db, access.IssueGrantInput{Holder: party, HolderKind: "party", Role: "reader", TargetType: "Folder", TargetID: open.String()})
		if err != nil {
			t.Fatal(err)
		}
		authenticated, err := db.AuthenticateGrant(context.Background(), token, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		caller := crosstable.Caller{Principal: authenticated.Principal, Permissions: authenticated.Permissions}
		c := read(t, caller, window)
		var want []string
		for _, key := range listed(t, authenticated.Principal) {
			if !isOld(t, db, key) {
				want = append(want, key)
			}
		}
		if keys := keysOf(c); !slices.Equal(keys, want) || len(keys) != 1 || c.Entries[0].Title != "notice" {
			t.Errorf("the party's calendar holds %+v, and its list reads show %q", c.Entries, want)
		}
		// A source with no owner field is left out of a read of mine.
		mine := window
		mine.Mine = true
		if c := read(t, user(ada), mine); len(c.Entries) != 0 {
			t.Errorf("a read of mine holds %+v", c.Entries)
		}
		answers["party"] = c
	})

	// The feeds' routes, their clock the cases', and a caller by header.
	var role = "viewer"
	if _, err := db.GetRoleRepository().CreateOne(ctx, &types.Role{Name: types.IdentitySlug(role), Permissions: []string{"calendar.read"}}); err != nil {
		t.Fatalf("create the role: %v", err)
	}
	now := clock
	routes := httptest.NewServer(crosstable.CalendarRoutes(crosstable.CalendarRoutesOptions{
		Host: db.CrossTables(),
		Authenticate: func(r *http.Request) (*crosstable.Caller, error) {
			subject := r.Header.Get("X-Subject")
			if subject == "" {
				return nil, nil
			}
			caller := user(subject)
			return &caller, nil
		},
		FeedDaysBefore: 60,
		FeedDaysAfter:  1,
		Now:            func() time.Time { return now },
	}))
	t.Cleanup(routes.Close)
	call := func(t *testing.T, method, path, subject string, body any) (int, []byte) {
		t.Helper()
		var reader io.Reader
		if body != nil {
			data, _ := json.Marshal(body)
			reader = strings.NewReader(string(data))
		}
		request, err := http.NewRequest(method, routes.URL+path, reader)
		if err != nil {
			t.Fatal(err)
		}
		if subject != "" {
			request.Header.Set("X-Subject", subject)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		data, _ := io.ReadAll(response.Body)
		return response.StatusCode, data
	}
	type issued struct {
		Data struct {
			ID, Token, Path, EndsAt string
		}
	}
	issue := func(t *testing.T, subject string, body map[string]any) issued {
		t.Helper()
		status, data := call(t, http.MethodPost, "/api/calendar/feeds", subject, body)
		if status != http.StatusCreated {
			t.Fatalf("issue a feed: %d %s", status, data)
		}
		var out issued
		if err := json.Unmarshal(data, &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	t.Run("a feed's token reads its holder's calendar as a busy or a full iCalendar document", func(t *testing.T) {
		busy := issue(t, bob, map[string]any{"role": role, "detail": "busy"})
		full := issue(t, bob, map[string]any{"role": role, "detail": "full", "sources": []string{"document"}})
		if !strings.HasPrefix(busy.Data.Token, access.GrantTokenPrefix) || busy.Data.Path != "/api/calendar/feed/"+busy.Data.Token+".ics" {
			t.Errorf("the issued feed: %+v", busy.Data)
		}
		got := map[string]any{}
		for name, feed := range map[string]issued{"busy": busy, "full": full} {
			status, text := call(t, http.MethodGet, feed.Data.Path, "", nil)
			if status != http.StatusOK || !strings.HasPrefix(string(text), "BEGIN:VCALENDAR\r\n") {
				t.Fatalf("the %s feed: %d %s", name, status, text)
			}
			got[name] = string(text)
		}
		// Bob's: the named folder's minutes and the open folder's notice;
		// never the private folder's plans, nor ada's loose note.
		want := read(t, user(bob), window)
		if n := strings.Count(got["full"].(string), "BEGIN:VEVENT"); n != len(want.Entries) || n != 2 {
			t.Errorf("the full feed holds %d events, and bob's calendar %d", n, len(want.Entries))
		}
		for _, title := range []string{"minutes", "notice"} {
			if !strings.Contains(got["full"].(string), "SUMMARY:"+title+"\r\n") {
				t.Errorf("the full feed lacks %q:\n%s", title, got["full"])
			}
			if strings.Contains(got["busy"].(string), title) {
				t.Errorf("the busy feed shows the title %q", title)
			}
		}
		if strings.Contains(got["full"].(string), "plans") || strings.Contains(got["full"].(string), "loose note") {
			t.Error("the feed shows a document bob may not read")
		}
		if want := crosstable.CalendarICS(want.Entries, crosstable.DetailBusy, now, ""); got["busy"] != want {
			t.Errorf("the busy feed is not its entries' iCalendar text:\n%s\n%s", got["busy"], want)
		}
		// The feed's token reads the feed and nothing else.
		if _, err := db.AuthenticateGrant(context.Background(), busy.Data.Token, time.Now()); err == nil || !errors.Is(err, access.ErrGrantRefused) {
			t.Errorf("a feed's token authenticated another route: %v", err)
		}
		answers["feeds"] = got
	})

	t.Run("a revoked, a rotated and an expired feed is refused, and its token reads nothing else", func(t *testing.T) {
		feed := issue(t, carol, map[string]any{"role": role, "detail": "full"})
		if status, _ := call(t, http.MethodGet, feed.Data.Path, "", nil); status != http.StatusOK {
			t.Fatalf("the feed: %d", status)
		}
		// Only its holder rotates it.
		if status, _ := call(t, http.MethodPost, "/api/calendar/feeds/"+feed.Data.ID+"/rotate", bob, nil); status != http.StatusNotFound {
			t.Errorf("bob rotated carol's feed: %d", status)
		}
		status, data := call(t, http.MethodPost, "/api/calendar/feeds/"+feed.Data.ID+"/rotate", carol, nil)
		var rotated issued
		_ = json.Unmarshal(data, &rotated)
		if status != http.StatusOK || rotated.Data.Token == "" || rotated.Data.Token == feed.Data.Token {
			t.Fatalf("rotate: %d %s", status, data)
		}
		got := map[string]any{}
		status, text := call(t, http.MethodGet, feed.Data.Path, "", nil)
		got["the rotated token"] = []any{status, string(text)}
		if status != http.StatusUnauthorized || !strings.Contains(string(text), "the feed has ended") {
			t.Errorf("the rotated token: %d %s", status, text)
		}
		status, text = call(t, http.MethodGet, rotated.Data.Path, "", nil)
		got["its successor"] = []any{status, strings.Count(string(text), "BEGIN:VEVENT")}
		if status != http.StatusOK {
			t.Errorf("the rotated feed: %d %s", status, text)
		}
		if status, _ := call(t, http.MethodDelete, "/api/calendar/feeds/"+rotated.Data.ID, carol, nil); status != http.StatusNoContent {
			t.Errorf("revoke: %d", status)
		}
		status, text = call(t, http.MethodGet, rotated.Data.Path, "", nil)
		got["the revoked token"] = []any{status, string(text)}
		if status != http.StatusUnauthorized {
			t.Errorf("the revoked feed: %d %s", status, text)
		}
		// A feed of one day, fetched two days on.
		short := issue(t, carol, map[string]any{"role": role, "detail": "busy", "days": 1})
		now = clock.Add(48 * time.Hour)
		status, text = call(t, http.MethodGet, short.Data.Path, "", nil)
		now = clock
		got["the expired token"] = []any{status, string(text)}
		if status != http.StatusUnauthorized || !strings.Contains(string(text), "the feed has ended") {
			t.Errorf("the expired feed: %d %s", status, text)
		}
		status, text = call(t, http.MethodGet, "/api/calendar/feed/grant_not-a-feed.ics", "", nil)
		got["an unknown token"] = status
		if status != http.StatusUnauthorized {
			t.Errorf("an unknown token: %d %s", status, text)
		}
		answers["refused feeds"] = got
	})
	return answers
}

// isOld reports whether the document key was made before the cases'
// window, which its calendar leaves out.
func isOld(t *testing.T, db calendarDB, key string) bool {
	t.Helper()
	id, err := orm.ParseUserID(key)
	if err != nil {
		t.Fatal(err)
	}
	row, err := db.GetDocumentRepository().GetOne(orm.AsSystem(context.Background()), id, nil)
	if err != nil {
		t.Fatal(err)
	}
	return row.Title == "before the window"
}
