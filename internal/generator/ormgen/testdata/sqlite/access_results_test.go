package orm

// The Go repositories of fixture-access-db on a SQLite file, held to
// per-record access (D156, D180) as on Postgres (D182, #611): one script
// of reads and writes as a user, another user, an agent run and a party
// with grants, each answer the rows' names or the refusal, and the same
// answers on Postgres when SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL is
// set. An audience private, named and inherited from a folder keeps each
// read, a condition through a relation and each write; a grant reaches
// the rows its role's paths lead to and nothing else, and is refused at
// the next call once it has expired or been revoked; a reveal writes a
// row of the access log.

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
	"time"

	types "example.com/schemas/types/go/fixture-access-db"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	_ "modernc.org/sqlite"
)

type accessCall struct {
	name   string
	answer any
}

// accessAnswer is what a call answered: its value, or its refusal by kind.
func accessAnswer(value any, err error) any {
	switch {
	case err == nil:
		return value
	case errors.Is(err, ErrNotFound):
		return "not found"
	case errors.Is(err, ErrAccessRefused):
		return "access refused"
	case errors.Is(err, access.ErrGrantRefused):
		return "grant refused"
	}
	return "error: " + err.Error()
}

// accessFixture is what the script runs on: a database of either dialect,
// its grant authority, and a raw insert of a contract, whose repository
// SQLite refuses (its terms are an Encrypted<T> field).
type accessFixture struct {
	db        DatabaseInterface
	authority access.GrantAuthority
	contract  func(ctx context.Context, id, name string) error
}

// The script's principals, fixed so both dialects answer alike.
var (
	ada   = uuid.MustParse("00000000-0000-4000-8000-0000000000a1")
	bob   = uuid.MustParse("00000000-0000-4000-8000-0000000000b2")
	carol = uuid.MustParse("00000000-0000-4000-8000-0000000000c3")
	party = uuid.MustParse("00000000-0000-4000-8000-0000000000d4")
)

func accessID(t *testing.T, u uuid.UUID) types.IdentityUUID {
	t.Helper()
	id, err := types.ParseIdentityUUID(u.String())
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func accessScript(t *testing.T, f accessFixture) []accessCall {
	t.Helper()
	db := f.db
	var calls []accessCall
	record := func(name string, value any, err error) {
		calls = append(calls, accessCall{name: name, answer: accessAnswer(value, err)})
	}
	// No principal: the writes that set the rows up read and write every
	// row, as a job's do.
	ctx := WithUserID(context.Background(), accessID(t, ada))
	as := func(p access.Principal) context.Context { return WithPrincipal(ctx, p) }
	user := func(who uuid.UUID) context.Context {
		return as(access.Principal{Subject: who.String(), Kind: access.KindUser})
	}
	folder := func(name, audience string, named ...uuid.UUID) types.IdentityUUID {
		t.Helper()
		row := &types.Folder{Name: name, Owner: accessID(t, ada), Audience: audience, AgentAccess: "followRole"}
		for _, who := range named {
			row.AudienceNamed = append(row.AudienceNamed, accessID(t, who))
		}
		created, err := db.GetFolderRepository().CreateOne(ctx, row)
		if err != nil {
			t.Fatalf("create the folder %s: %v", name, err)
		}
		return *created.Id
	}
	document := func(title string, in types.IdentityUUID) types.IdentityUUID {
		t.Helper()
		created, err := db.GetDocumentRepository().CreateOne(ctx, &types.Document{Title: title, Folder: &types.Folder{Id: &in}, Audience: "inherit", AgentAccess: "readOnly"})
		if err != nil {
			t.Fatalf("create the document %s: %v", title, err)
		}
		return *created.Id
	}
	folderNames := func(ctx context.Context, filter *FolderFilter) (any, error) {
		rows, _, err := db.GetFolderRepository().FindMany(ctx, filter, &FolderFindOptions{Fields: FolderFields{}.All()})
		if err != nil {
			return nil, err
		}
		var names []string
		for _, row := range rows {
			names = append(names, row.Name)
		}
		sort.Strings(names)
		return names, nil
	}
	documentTitles := func(ctx context.Context, filter *DocumentFilter) (any, error) {
		rows, _, err := db.GetDocumentRepository().FindMany(ctx, filter, &DocumentFindOptions{Fields: DocumentFields{}.All()})
		if err != nil {
			return nil, err
		}
		var titles []string
		for _, row := range rows {
			titles = append(titles, row.Title)
		}
		sort.Strings(titles)
		return titles, nil
	}

	private, named, open := folder("private", "private"), folder("named", "named", bob), folder("open", "everyone")
	inPrivate, inNamed := document("in private", private), document("in named", named)

	// The audience: private, named and inherited.
	for _, who := range []struct {
		name string
		id   uuid.UUID
	}{{"ada", ada}, {"bob", bob}, {"carol", carol}} {
		names, err := folderNames(user(who.id), nil)
		record(who.name+" lists the folders", names, err)
		titles, err := documentTitles(user(who.id), nil)
		record(who.name+" lists the documents, which inherit their folder's audience", titles, err)
		count, err := db.GetFolderRepository().FindPage(user(who.id), nil, &FolderPageOptions{Fields: FolderFields{}.All()})
		if err == nil {
			record(who.name+" pages the folders", len(count.Rows), nil)
		} else {
			record(who.name+" pages the folders", nil, err)
		}
	}
	_, err := db.GetFolderRepository().GetOne(user(bob), private, nil)
	record("bob gets the private folder", nil, err)
	got, err := db.GetFolderRepository().GetOne(user(bob), named, nil)
	if err == nil {
		record("bob gets the folder that names him", got.Name, nil)
	} else {
		record("bob gets the folder that names him", nil, err)
	}
	many, err := db.GetFolderRepository().GetManyByIDs(user(carol), []types.IdentityUUID{private, named, open})
	record("carol reads the folders by their keys", len(many), err)
	private2 := "private"
	titles, err := documentTitles(user(bob), &DocumentFilter{Folder: &FolderFilter{Name: &StringFilter{Eq: &private2}}})
	record("bob's filter through the relation to the private folder", titles, err)
	renamed := "renamed by bob"
	_, err = db.GetFolderRepository().UpdateOne(user(bob), private, &FolderUpdate{Name: &renamed})
	record("bob updates the private folder", nil, err)
	n, err := db.GetFolderRepository().UpdateMany(user(bob), nil, &FolderUpdate{Name: &renamed})
	record("bob updates every folder he may", n, err)
	err = db.GetFolderRepository().DeleteOne(user(carol), private)
	record("carol deletes the private folder", nil, err)
	names, err := folderNames(ctx, nil)
	record("every folder, as a job reads them", names, err)

	// An agent run acting for ada reads her documents and writes none:
	// their agent access is readOnly.
	agent := as(access.Principal{Subject: uuid.New().String(), Kind: access.KindAgent, ActingFor: &access.ActingFor{Kind: access.KindUser, Subject: ada.String()}})
	titles, err = documentTitles(agent, nil)
	record("an agent run for ada lists the documents", titles, err)
	title := "rewritten by an agent"
	_, err = db.GetDocumentRepository().UpdateOne(agent, inPrivate, &DocumentUpdate{Title: &title})
	record("an agent run rewrites a readOnly document", nil, err)

	// A folder opened to everyone opens the documents that inherit from it.
	everyone := "everyone"
	_, err = db.GetFolderRepository().UpdateOne(user(ada), private, &FolderUpdate{Audience: &everyone})
	record("ada opens the private folder", nil, err)
	titles, err = documentTitles(user(carol), nil)
	record("carol lists the documents once the folder is open", titles, err)
	_ = inNamed

	// Grants: a party reaches the requests and units of the contract its
	// grant names, through its role's paths, and nothing else.
	c1, c2 := uuid.MustParse("00000000-0000-4000-8000-0000000000e1"), uuid.MustParse("00000000-0000-4000-8000-0000000000e2")
	for _, c := range []struct {
		id   uuid.UUID
		name string
	}{{c1, "first"}, {c2, "second"}} {
		if err := f.contract(ctx, c.id.String(), c.name); err != nil {
			t.Fatalf("insert the contract %s: %v", c.name, err)
		}
	}
	if _, err := db.GetRoleRepository().CreateOne(ctx, &types.Role{Name: "tenant", Permissions: []string{"Request.read"}, Reaches: []string{"Request:unit.contract", "Unit:contract"}}); err != nil {
		t.Fatalf("create the role: %v", err)
	}
	unit := func(name string, contract uuid.UUID) types.IdentityUUID {
		t.Helper()
		created, err := db.GetUnitRepository().CreateOne(ctx, &types.Unit{Name: name, Contract: types.Contract{Id: ptrOf(accessID(t, contract))}})
		if err != nil {
			t.Fatalf("create the unit %s: %v", name, err)
		}
		return *created.Id
	}
	u1, u2 := unit("first unit", c1), unit("second unit", c2)
	for _, r := range []struct {
		title string
		unit  types.IdentityUUID
	}{{"first request", u1}, {"second request", u2}} {
		if _, err := db.GetRequestRepository().CreateOne(ctx, &types.Request{Title: r.title, Unit: types.Unit{Id: ptrOf(r.unit)}}); err != nil {
			t.Fatalf("create the request %s: %v", r.title, err)
		}
	}
	now := time.Now()
	later := now.Add(time.Hour)
	issue := func(ends *time.Time) (*types.Grant, string) {
		t.Helper()
		grant, token, err := IssueGrant(ctx, db, access.IssueGrantInput{Holder: party.String(), HolderKind: "party", Role: "tenant", TargetType: "Contract", TargetID: c1.String(), StartsAt: now.Add(-time.Minute), EndsAt: ends})
		if err != nil {
			t.Fatalf("issue a grant: %v", err)
		}
		return grant, token
	}
	_, lasting := issue(nil)
	expiring, expires := issue(&later)
	authenticated, err := f.authority.AuthenticateGrant(ctx, lasting, now)
	record("the grant's token authenticates its holder", map[string]any{"kind": string(authenticated.Principal.Kind), "role": authenticated.Role, "permissions": authenticated.Permissions}, err)
	held := as(authenticated.Principal)
	requests, _, err := db.GetRequestRepository().FindMany(held, nil, &RequestFindOptions{Fields: RequestFields{Title: true}})
	var requestTitles []string
	for _, row := range requests {
		requestTitles = append(requestTitles, row.Title)
	}
	record("the party lists the requests its grant reaches", requestTitles, err)
	units, _, err := db.GetUnitRepository().FindMany(held, nil, &UnitFindOptions{Fields: UnitFields{Name: true}})
	var unitNames []string
	for _, row := range units {
		unitNames = append(unitNames, row.Name)
	}
	record("the party lists the units its grant reaches", unitNames, err)
	names, err = folderNames(held, nil)
	record("the party lists the folders, which no path of its role reaches", names, err)
	_, err = db.GetUnitRepository().GetOne(held, u2, nil)
	record("the party gets a unit of another contract", nil, err)
	_, err = db.GetRequestRepository().CreateOne(held, &types.Request{Title: "made by the party", Unit: types.Unit{Id: ptrOf(u2)}})
	record("the party creates a request under another contract's unit", nil, err)
	err = db.Transaction(held, func(tx TxInterface) error {
		_, err := tx.GetRequestRepository().CreateMany(held, []*types.Request{{Title: "reached", Unit: types.Unit{Id: ptrOf(u1)}}, {Title: "not reached", Unit: types.Unit{Id: ptrOf(u2)}}})
		return err
	})
	record("the party's CreateMany of a row its grant does not reach", nil, err)
	everyRequest, _, err := db.GetRequestRepository().FindMany(ctx, nil, &RequestFindOptions{Fields: RequestFields{Title: true}})
	record("the requests, as a job reads them", len(everyRequest), err)

	_, err = f.authority.AuthenticateGrant(ctx, expires, now)
	record("an expiring grant before its end", nil, err)
	_, err = f.authority.AuthenticateGrant(ctx, expires, later.Add(time.Minute))
	record("the expiring grant on the next call after its end", nil, err)
	active, err := ActiveGrants(ctx, db, access.ActiveGrantsFilter{Holder: party.String(), Now: later.Add(time.Minute)})
	record("the active grants after the expiring one ended", len(active), err)
	err = RevokeGrant(ctx, db, expiring.Id.String(), now)
	record("revoke the expiring grant", nil, err)
	// A grant's target as the active grants' route reads its owner (#735):
	// the row's members, as the system reads them, and none for a key no
	// row has.
	target, err := GrantTarget(user(carol), db, "Folder", private.String())
	owner, _ := json.Marshal(target["owner"])
	ada62, _ := json.Marshal(accessID(t, ada))
	if string(owner) != string(ada62) {
		t.Errorf("the grant's target folder names the owner %s, want %s", owner, ada62)
	}
	record("a grant's target names its owner, read as the system", string(owner) == string(ada62), err)
	missing, err := GrantTarget(ctx, db, "Folder", party.String())
	record("a grant's target that is no row", missing == nil, err)
	_, err = f.authority.AuthenticateGrant(ctx, expires, now)
	record("the revoked grant on the next call", nil, err)
	_, err = f.authority.AuthenticateGrant(ctx, "no such token", now)
	record("an unknown token", nil, err)

	// The access log: a reveal writes one row, as the system actor.
	reveal := access.RevealEntry(authenticated.Principal, "getContract", "Contract", c1.String(), "terms")
	err = RecordAccess(held, db, reveal)
	record("a reveal records its access", nil, err)
	err = AccessLogOf(db).Record(user(ada), access.LogEntry{Kind: access.LogAgentRead, Principal: ada.String(), PrincipalKind: "user", Operation: "listDocuments"})
	record("the access log records an agent run's read", nil, err)
	entries, _, err := db.GetAccessEntryRepository().FindMany(ctx, nil, &AccessEntryFindOptions{Fields: AccessEntryFields{}.All()})
	var logged []string
	for _, entry := range entries {
		data, _ := json.Marshal(entry)
		var members map[string]any
		_ = json.Unmarshal(data, &members)
		logged = append(logged, fmt.Sprintf("%v %v %v %v", members["kind"], members["operation"], members["targetType"], members["field"]))
	}
	sort.Strings(logged)
	record("the access log", logged, err)
	return calls
}

func ptrOf[T any](v T) *T { return &v }

func TestAccessOnSQLiteAndPostgres(t *testing.T) {
	ctx := context.Background()
	sqliteDDL, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", SQLiteDSN(filepath.Join(t.TempDir(), "access.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(sqliteDDL)); err != nil {
		t.Fatalf("the SQLite DDL: %v", err)
	}
	lite, err := OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	defer lite.Close()
	onSQLite := accessScript(t, accessFixture{db: lite, authority: lite, contract: func(ctx context.Context, id, name string) error {
		_, err := lite.ExecContext(ctx, `INSERT INTO contract (id, "name") VALUES (?1, ?2)`, id, name)
		return err
	}})
	for _, call := range onSQLite {
		data, _ := json.Marshal(call.answer)
		t.Logf("%s: %s", call.name, data)
		if s, ok := call.answer.(string); ok && strings.HasPrefix(s, "error: ") {
			t.Errorf("%s on SQLite: %s", call.name, s)
		}
	}
	want := map[string]any{
		"ada lists the folders":   []string{"named", "open", "private"},
		"bob lists the folders":   []string{"named", "open"},
		"carol lists the folders": []string{"open"},
		"bob lists the documents, which inherit their folder's audience":   []string{"in named"},
		"carol lists the documents, which inherit their folder's audience": []string(nil),
		"bob gets the private folder":                                      "not found",
		"bob's filter through the relation to the private folder":          []string(nil),
		"bob updates the private folder":                                   "not found",
		"carol deletes the private folder":                                 "not found",
		"an agent run rewrites a readOnly document":                        "not found",
		"carol lists the documents once the folder is open":                []string{"in private"},
		"the party lists the requests its grant reaches":                   []string{"first request"},
		"the party lists the units its grant reaches":                      []string{"first unit"},
		"the party lists the folders, which no path of its role reaches":   []string(nil),
		"the party gets a unit of another contract":                        "not found",
		"the party creates a request under another contract's unit":        "access refused",
		"the party's CreateMany of a row its grant does not reach":         "access refused",
		"the expiring grant on the next call after its end":                "grant refused",
		"the revoked grant on the next call":                               "grant refused",
		"an unknown token":                                                 "grant refused",
		"the active grants after the expiring one ended":                   1,
		"carol reads the folders by their keys":                            1,
		"the access log":                                                   []string{"agentRead listDocuments <nil> <nil>", "reveal getContract Contract terms"},
	}
	for _, call := range onSQLite {
		if w, ok := want[call.name]; ok && !reflect.DeepEqual(call.answer, w) {
			t.Errorf("%s on SQLite: %#v, want %#v", call.name, call.answer, w)
		}
	}

	dsn := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	if dsn == "" {
		t.Logf("SQLite answered %d calls; set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to hold them to Postgres", len(onSQLite))
		return
	}
	createSQL, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	base, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer base.Close()
	schema := fmt.Sprintf("ormgen_sqlite_access_%d", time.Now().UnixNano())
	if _, err := base.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	defer func() { _, _ = base.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") }()
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(createSQL)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	postgres, err := ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	defer postgres.Close()
	onPostgres := accessScript(t, accessFixture{db: postgres, authority: postgres, contract: func(ctx context.Context, id, name string) error {
		_, err := pool.Exec(ctx, `INSERT INTO contract (id, "name") VALUES ($1, $2)`, id, name)
		return err
	}})
	if len(onPostgres) != len(onSQLite) {
		t.Fatalf("Postgres answered %d calls and SQLite %d", len(onPostgres), len(onSQLite))
	}
	for i, pg := range onPostgres {
		if !reflect.DeepEqual(pg.answer, onSQLite[i].answer) {
			t.Errorf("%s: Postgres answered %#v and SQLite %#v", pg.name, pg.answer, onSQLite[i].answer)
		}
	}
	t.Logf("SQLite and Postgres answered %d calls alike", len(onSQLite))
}
