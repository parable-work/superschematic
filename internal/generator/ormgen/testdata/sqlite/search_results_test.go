// fixture-search-db's search across tables end to end on a SQLite file
// (D155, D186, #615): six @searchable tables share one search index, whose
// words on SQLite are an FTS5 table the index's triggers keep, searched
// through the schema runtime's crosstable over the SQLite database's
// CrossTables, the caller's access checked inside the query by the
// repositories' conditions on SQLite. TestTheGoReadsAcrossTablesOnSQLite
// writes this file into the generated ORM module, applies the SQLite DDL
// the build writes to a file, and runs these cases, which are the
// TypeScript repositories' on SQLite (tsgen's search_sqlite.test.ts) and
// the Go ones' on Postgres (search_go.go); with the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the same cases run on Postgres
// too, with the same keys, and each case's hits on the two, as sets, are
// held equal (bm25 and ts_rank_cd weigh the texts alike and score them
// differently, D186). drain runs what the queue's worker runs: each ready
// message of the SearchIndexing queue, handed to IndexSearch.
//
// Contract's terms are an Encrypted<T> field, which the Go repositories do
// not seal on SQLite, so on SQLite it has no repository: its rows are
// written by SQL on both databases, and its rows a list read would show
// are every row for a user and the grant's target for a party.
package orm_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"

	orm "example.com/schemas/orm/fixture-search-db"
	types "example.com/schemas/types/go/fixture-search-db"
	_ "modernc.org/sqlite"
)

// searchDB is the ORM on SQLite or on Postgres, as a case reaches it.
type searchDB interface {
	orm.DatabaseInterface
	CrossTables() crosstable.Host
	AuthenticateGrant(ctx context.Context, token string, now time.Time) (access.GrantAuthentication, error)
}

// searchFixture is what the cases run on: a database of either dialect, a
// raw statement written with $n parameters, and whether it is SQLite.
type searchFixture struct {
	db     searchDB
	exec   func(ctx context.Context, sql string, args ...any) (int64, error)
	query  func(ctx context.Context, sql string, args ...any) ([][]any, error)
	sqlite bool
}

func openSQLite(t *testing.T) searchFixture {
	t.Helper()
	ctx := context.Background()
	ddl, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "search.sqlite")))
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
	return searchFixture{db: db, sqlite: true,
		exec: func(ctx context.Context, query string, args ...any) (int64, error) {
			result, err := raw.ExecContext(ctx, strings.ReplaceAll(query, "$", "?"), args...)
			if err != nil {
				return 0, err
			}
			return result.RowsAffected()
		},
		query: func(ctx context.Context, query string, args ...any) ([][]any, error) {
			rows, err := raw.QueryContext(ctx, strings.ReplaceAll(query, "$", "?"), args...)
			if err != nil {
				return nil, err
			}
			defer func() { _ = rows.Close() }()
			columns, _ := rows.Columns()
			var out [][]any
			for rows.Next() {
				cells := make([]any, len(columns))
				targets := make([]any, len(cells))
				for i := range cells {
					targets[i] = &cells[i]
				}
				if err := rows.Scan(targets...); err != nil {
					return nil, err
				}
				out = append(out, cells)
			}
			return out, rows.Err()
		},
	}
}

func openPostgres(t *testing.T) searchFixture {
	t.Helper()
	target := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("search_sqlite_%d", time.Now().UnixNano())
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
	db, err := orm.ConnectWithPool(pool, orm.WithFieldKeys("BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc="))
	if err != nil {
		t.Fatal(err)
	}
	return searchFixture{db: db,
		exec: func(ctx context.Context, query string, args ...any) (int64, error) {
			tag, err := pool.Exec(ctx, query, args...)
			return tag.RowsAffected(), err
		},
		query: func(ctx context.Context, query string, args ...any) ([][]any, error) {
			rows, err := pool.Query(ctx, query, args...)
			if err != nil {
				return nil, err
			}
			defer rows.Close()
			var out [][]any
			for rows.Next() {
				values, err := rows.Values()
				if err != nil {
					return nil, err
				}
				out = append(out, values)
			}
			return out, rows.Err()
		},
	}
}

// TestSearchOnSQLite runs the cases on SQLite and, with a Postgres, on
// Postgres, and holds each case's answers on the two equal.
func TestSearchOnSQLite(t *testing.T) {
	var answers [2]map[string]any
	t.Run("sqlite", func(t *testing.T) { answers[0] = searchCases(t, openSQLite(t)) })
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Log("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the same cases on Postgres")
		return
	}
	t.Run("postgres", func(t *testing.T) { answers[1] = searchCases(t, openPostgres(t)) })
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
	alice = uuidOf(0xa1)
	bob   = uuidOf(0xb0)
	party = uuidOf(0xd4)
)

func user(subject string) crosstable.Caller {
	return crosstable.Caller{Principal: access.Principal{Subject: subject, Kind: access.KindUser}}
}

func as(principal access.Principal) context.Context {
	return orm.WithPrincipal(context.Background(), principal)
}

var system = orm.AsSystem(context.Background())

func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func first[R any](rows []R, _ int, err error) ([]R, error) { return rows, err }

func id(text string) types.IdentityUUID { return must(orm.ParseUserID(text)) }

func idp(n int) *types.IdentityUUID { v := id(uuidOf(n)); return &v }

func sorted(keys []string) []string {
	out := slices.Clone(keys)
	slices.Sort(out)
	return out
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: %q, want %q", what, got, want)
	}
}

func wordsOf(text string) []string {
	return strings.FieldsFunc(strings.ToLower(text), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) })
}

func searchCases(t *testing.T, f searchFixture) map[string]any {
	db := f.db
	ctx := context.Background()
	host := db.CrossTables()
	answers := map[string]any{}
	asAlice := as(access.Principal{Subject: alice, Kind: access.KindUser})
	now := types.TemporalDateTime(time.Now())

	// drain is what the queue's worker runs: every ready message, indexed.
	drain := func(t *testing.T, options crosstable.SearchOptions) int {
		t.Helper()
		n := 0
		for {
			rows, err := f.query(ctx, `DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key`)
			if err != nil {
				t.Fatal(err)
			}
			if len(rows) == 0 {
				return n
			}
			for _, row := range rows {
				if _, err := crosstable.IndexSearch(ctx, host, crosstable.SearchIndexing{SourceTable: fmt.Sprint(row[0]), SourceKey: fmt.Sprint(row[1])}, options); err != nil {
					t.Fatal(err)
				}
			}
			n += len(rows)
		}
	}
	none := crosstable.SearchOptions{}
	search := func(t *testing.T, caller crosstable.Caller, request crosstable.SearchRequest, options crosstable.SearchOptions) (crosstable.SearchResult, []string) {
		t.Helper()
		if request.Limit == 0 {
			request.Limit = 100
		}
		result := must(crosstable.SearchTables(ctx, host, caller, request, options))
		var keys []string
		for _, hit := range result.Items {
			keys = append(keys, hit.Table+":"+hit.Key)
		}
		return result, keys
	}
	keys := func(t *testing.T, caller crosstable.Caller, q string) []string {
		t.Helper()
		_, out := search(t, caller, crosstable.SearchRequest{Q: q}, none)
		return out
	}
	// The contracts, by SQL: on SQLite the table has no Go repository.
	contracts := map[string][2]string{}
	contract := func(t *testing.T, n int, name, summary string) types.IdentityUUID {
		t.Helper()
		key := uuidOf(n)
		if _, err := f.exec(ctx, `INSERT INTO contract (id, name, summary) VALUES ($1, $2, $3)`, key, name, summary); err != nil {
			t.Fatal(err)
		}
		contracts[key] = [2]string{name, summary}
		return id(key)
	}
	// listed is the rows of every @searchable table principal's list reads
	// show whose title or body holds word, as table:key, sorted; a
	// contract's are every row for a user and a grant's target for a party.
	listed := func(t *testing.T, principal access.Principal, word string, target string) []string {
		t.Helper()
		c := as(principal)
		var out []string
		add := func(table string, key *types.IdentityUUID, texts ...string) {
			for _, text := range texts {
				if slices.Contains(wordsOf(text), word) {
					out = append(out, table+":"+key.String())
					return
				}
			}
		}
		for _, row := range must(first(db.GetFolderRepository().FindMany(c, nil, &orm.FolderFindOptions{Fields: orm.FolderFields{}.All(), Limit: 1000}))) {
			add("Folder", row.Id, row.Name)
		}
		for _, row := range must(first(db.GetDocumentRepository().FindMany(c, nil, &orm.DocumentFindOptions{Fields: orm.DocumentFields{}.All(), Limit: 1000}))) {
			add("Document", row.Id, row.Title, row.Text)
		}
		for key, texts := range contracts {
			if principal.Grants == nil || id(key).String() == target {
				add("Contract", idp0(key), texts[0], texts[1])
			}
		}
		for _, row := range must(first(db.GetUnitRepository().FindMany(c, nil, &orm.UnitFindOptions{Fields: orm.UnitFields{Id: true, Name: true, Notes: true}, Limit: 1000}))) {
			add("Unit", row.Id, row.Name, row.Notes)
		}
		for _, row := range must(first(db.GetRequestRepository().FindMany(c, nil, &orm.RequestFindOptions{Fields: orm.RequestFields{Id: true, Title: true, Details: true, Resolution: true}, Limit: 1000}))) {
			add("Request", row.Id, row.Title, row.Details, row.Resolution)
		}
		for _, row := range must(first(db.GetNoteRepository().FindMany(c, nil, &orm.NoteFindOptions{Fields: orm.NoteFields{}.All(), Limit: 1000}))) {
			add("Note", row.Id, row.Title, row.Text)
		}
		return sorted(out)
	}

	// Two folders: alice's own, and one everyone sees; a document in each.
	privateFolder := must(db.GetFolderRepository().CreateOne(asAlice, &types.Folder{Id: idp(0x101), Name: "Walnut private", Owner: id(alice), Audience: "private", AgentAccess: "followRole", CreatedAt: now}))
	sharedFolder := must(db.GetFolderRepository().CreateOne(asAlice, &types.Folder{Id: idp(0x102), Name: "Walnut shared", Owner: id(alice), Audience: "everyone", AgentAccess: "followRole", CreatedAt: now}))
	privateDoc := must(db.GetDocumentRepository().CreateOne(asAlice, &types.Document{Id: idp(0x201), Folder: &types.Folder{Id: privateFolder.Id}, Title: "Walnut plan", Text: "The plan in brief.", Audience: "inherit", AgentAccess: "followRole", CreatedAt: now}))
	sharedDoc := must(db.GetDocumentRepository().CreateOne(asAlice, &types.Document{Id: idp(0x202), Folder: &types.Folder{Id: sharedFolder.Id}, Title: "Memo", Text: "A walnut desk for the hall.", Audience: "inherit", AgentAccess: "followRole", CreatedAt: now}))
	// Two contracts, the first with a unit and a request about it.
	contractA := contract(t, 0x301, "Walnut supply", "Timber for the year.")
	contractB := contract(t, 0x302, "Bolts", "Bolts and a walnut trim.")
	unitA := must(db.GetUnitRepository().CreateOne(system, &orm.UnitCreate{Id: idp(0x401), Name: "Walnut unit", Contract: types.Contract{Id: &contractA}}))
	unitB := must(db.GetUnitRepository().CreateOne(system, &orm.UnitCreate{Id: idp(0x402), Name: "Annex", Notes: "Walnut floors.", Contract: types.Contract{Id: &contractB}}))
	requestA := must(db.GetRequestRepository().CreateOne(system, &types.Request{Id: idp(0x501), Unit: types.Unit{Id: unitA.Id}, Title: "Walnut leak", Details: "Water under the sink."}))
	requestB := must(db.GetRequestRepository().CreateOne(system, &types.Request{Id: idp(0x502), Unit: types.Unit{Id: unitB.Id}, Title: "Hinge", Details: "A walnut door sticks."}))
	// Notes in two projects, one deleted.
	noteP1 := must(db.GetNoteRepository().CreateOne(system, &types.Note{Id: idp(0x601), Project: "p1", Title: "Walnut idea", Text: "Oil it twice."}))
	noteP2 := must(db.GetNoteRepository().CreateOne(system, &types.Note{Id: idp(0x602), Project: "p2", Title: "Pine idea", Text: "A walnut stain on pine."}))
	noteGone := must(db.GetNoteRepository().CreateOne(system, &types.Note{Id: idp(0x603), Project: "p1", Title: "Walnut draft", Text: "Gone soon."}))
	if err := db.GetNoteRepository().DeleteOne(system, *noteGone.Id); err != nil {
		t.Fatal(err)
	}
	// A role a party's grant names: the units and requests of its contract.
	must(db.GetRoleRepository().CreateOne(system, &types.Role{Name: types.IdentitySlug("contract-party"), Permissions: []string{}, Reaches: []string{"Unit:contract", "Request:unit.contract"}}))
	answers["the first drain"] = drain(t, none)
	key := func(table string, row *types.IdentityUUID) string { return table + ":" + row.String() }

	t.Run("two principals with different access each find exactly the rows their list reads show, ranked across five tables", func(t *testing.T) {
		got := map[string]any{}
		for _, subject := range []string{alice, bob} {
			caller := user(subject)
			result, hits := search(t, caller, crosstable.SearchRequest{Q: "walnut"}, none)
			equal(t, subject+"'s hits", sorted(hits), listed(t, caller.Principal, "walnut", ""))
			if result.Total != len(hits) {
				t.Errorf("total %d, %d hits", result.Total, len(hits))
			}
			tables := map[string]bool{}
			for _, hit := range result.Items {
				if hit.Table != "Contract" {
					tables[hit.Table] = true
				}
			}
			if len(tables) < 5 {
				t.Errorf("the hits are of %d tables with repositories: %v", len(tables), tables)
			}
			// A page of three at a time reads the same rows, in the same
			// order, with the same total on every page: no gap where a
			// hidden row is.
			var paged []string
			for offset := 0; ; offset += 3 {
				page, pageKeys := search(t, caller, crosstable.SearchRequest{Q: "Walnut", Limit: 3, Offset: offset}, none)
				if page.Total != result.Total {
					t.Errorf("page at %d: total %d, want %d", offset, page.Total, result.Total)
				}
				for i, hit := range page.Items {
					if hit.Place != offset+i+1 {
						t.Errorf("page at %d: place %d of hit %d", offset, hit.Place, i)
					}
				}
				paged = append(paged, pageKeys...)
				if !page.More {
					break
				}
			}
			equal(t, "the pages", paged, hits)
			got[subject] = map[string]any{"hits": sorted(hits), "total": result.Total}
		}
		alices, bobs := keys(t, user(alice), "walnut"), keys(t, user(bob), "walnut")
		for _, want := range []string{key("Folder", privateFolder.Id), key("Document", privateDoc.Id)} {
			if !slices.Contains(alices, want) || slices.Contains(bobs, want) {
				t.Errorf("%s: alice %t, bob %t; want alice's alone", want, slices.Contains(alices, want), slices.Contains(bobs, want))
			}
		}
		if !slices.Contains(bobs, key("Document", sharedDoc.Id)) || len(alices)-len(bobs) != 2 {
			t.Errorf("bob's hits %q, alice's %q", bobs, alices)
		}
		// A soft-deleted note is in no one's search.
		if slices.Contains(alices, key("Note", noteGone.Id)) {
			t.Error("the deleted note is found")
		}
		// Two words: a row holds both.
		equal(t, "walnut sink", keys(t, user(bob), "walnut sink"), []string{key("Request", requestA.Id)})
		// An anonymous caller sees what everyone may.
		equal(t, "the anonymous caller's", sorted(keys(t, crosstable.Caller{}, "walnut")), sorted(bobs))
		answers["two principals"] = got
	})

	t.Run("a party finds the rows its grant reaches, and no table a grant reaches no row of", func(t *testing.T) {
		grant, token := must2(orm.IssueGrant(system, db, access.IssueGrantInput{Holder: party, HolderKind: "party", Role: "contract-party", TargetType: "Contract", TargetID: contractA.String()}))
		authenticate := func(token string) (*crosstable.Caller, error) {
			got, err := db.AuthenticateGrant(ctx, token, time.Now())
			if err != nil {
				return nil, err
			}
			return &crosstable.Caller{Principal: got.Principal, Permissions: got.Permissions}, nil
		}
		principal := must(authenticate(token))
		if principal.Principal.Kind != access.KindParty {
			t.Fatalf("the grant's principal is a %s", principal.Principal.Kind)
		}
		result, hits := search(t, *principal, crosstable.SearchRequest{Q: "walnut"}, none)
		equal(t, "the party's hits", sorted(hits), listed(t, principal.Principal, "walnut", contractA.String()))
		equal(t, "the party's hits", sorted(hits), sorted([]string{key("Contract", &contractA), key("Request", requestA.Id), key("Unit", unitA.Id)}))
		if result.Total != 3 {
			t.Errorf("total %d", result.Total)
		}
		// The route, as the grant's holder.
		route := httptest.NewServer(crosstable.SearchRoutes(crosstable.SearchRoutesOptions{Host: host, Authenticate: func(r *http.Request) (*crosstable.Caller, error) {
			bearer, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
			if !ok {
				return nil, nil
			}
			return authenticate(bearer)
		}}))
		defer route.Close()
		get := func(path, token string) (int, map[string]any) {
			request := must(http.NewRequest(http.MethodGet, route.URL+path, nil))
			if token != "" {
				request.Header.Set("Authorization", "Bearer "+token)
			}
			response := must(http.DefaultClient.Do(request))
			defer response.Body.Close()
			var body map[string]any
			_ = json.NewDecoder(response.Body).Decode(&body)
			return response.StatusCode, body
		}
		status, body := get("/api/search?q=walnut&limit=2", token)
		data, _ := body["data"].(map[string]any)
		if status != http.StatusOK || data["total"] != float64(3) || data["more"] != true {
			t.Fatalf("the route: %d %v", status, body)
		}
		var routed []string
		for _, item := range data["items"].([]any) {
			hit := item.(map[string]any)
			routed = append(routed, fmt.Sprint(hit["table"], ":", hit["key"]))
		}
		equal(t, "the route's hits", routed, hits[:2])
		// The MCP tool, as the same principal: Folder is a table its grant
		// reaches no row of, which the query leaves out.
		called, problem, err := crosstable.CallSearchTool(ctx, host, *principal, map[string]any{"q": "walnut", "tables": []any{"Unit", "Request", "Folder"}}, none)
		if err != nil || problem != nil {
			t.Fatalf("the tool: %v %+v", err, problem)
		}
		var tooled []string
		for _, hit := range called.Items {
			tooled = append(tooled, hit.Table+":"+hit.Key)
		}
		equal(t, "the tool's hits", sorted(tooled), sorted([]string{key("Request", requestA.Id), key("Unit", unitA.Id)}))
		if result, _ := search(t, *principal, crosstable.SearchRequest{Q: "walnut", Tables: []string{"Folder", "Note"}}, none); result.Total != 0 || len(result.Items) != 0 {
			t.Errorf("tables no grant reaches: %+v", result)
		}
		// Revoked, the very next call is refused, and no principal is 401.
		if err := orm.RevokeGrant(ctx, db, grant.Id.String(), time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := authenticate(token); !errors.Is(err, access.ErrGrantRefused) {
			t.Errorf("a revoked grant: %v", err)
		}
		if status, _ := get("/api/search?q=walnut", token); status != http.StatusUnauthorized {
			t.Errorf("the route with a revoked grant: %d", status)
		}
		if status, _ := get("/api/search?q=walnut", ""); status != http.StatusUnauthorized {
			t.Errorf("the route with no caller: %d", status)
		}
		answers["party"] = map[string]any{"hits": sorted(hits), "tool": sorted(tooled), "route": len(routed)}
	})

	t.Run("a hit's match, its snippet and its place, the words every one required, a page and its total", func(t *testing.T) {
		result, hits := search(t, user(bob), crosstable.SearchRequest{Q: "walnut"}, none)
		// A match in a title ranks above one in a body alone.
		if slices.Index(hits, key("Contract", &contractA)) > slices.Index(hits, key("Contract", &contractB)) {
			t.Errorf("the title's match ranks below the body's: %q", hits)
		}
		matched := map[string]string{}
		for _, hit := range result.Items {
			matched[hit.Table+":"+hit.Key] = hit.Matched
			var marked []string
			for _, part := range hit.Snippet {
				if part.Match {
					marked = append(marked, strings.ToLower(part.Text))
				}
			}
			if !slices.Equal(marked, []string{"walnut"}) && !slices.Equal(marked, []string{"walnut", "walnut"}) {
				t.Errorf("%s's snippet marks %q: %+v", hit.Key, marked, hit.Snippet)
			}
		}
		if matched[key("Contract", &contractB)] != "body" || matched[key("Contract", &contractA)] != "title" || matched[key("Document", sharedDoc.Id)] != "body" {
			t.Errorf("where the words matched: %v", matched)
		}
		// Every word is required, a word is a phrase of its own words, and a
		// query of no word with a letter or a digit matches nothing.
		equal(t, "walnut sink", keys(t, user(bob), "walnut sink"), []string{key("Request", requestA.Id)})
		equal(t, "walnut nowhere", keys(t, user(bob), "walnut nowhere"), nil)
		equal(t, "a quoted word", keys(t, user(bob), `"sink"`), []string{key("Request", requestA.Id)})
		if result, _ := search(t, user(bob), crosstable.SearchRequest{Q: "-- ** !!"}, none); result.Total != 0 {
			t.Errorf("a query of no word: %+v", result)
		}
		// A page past the end holds no hit, and its total holds.
		page, _ := search(t, user(bob), crosstable.SearchRequest{Q: "walnut", Limit: 5, Offset: 50}, none)
		if len(page.Items) != 0 || page.Total != result.Total || page.More {
			t.Errorf("a page past the end: %+v", page)
		}
		var bad crosstable.Invalid
		if _, err := crosstable.SearchTables(ctx, host, user(bob), crosstable.SearchRequest{Q: "walnut", Limit: 101}, none); !errors.As(err, &bad) {
			t.Errorf("a limit of 101: %v", err)
		}
		answers["matches"] = map[string]any{"matched": matched, "total": result.Total}
	})

	t.Run("a row's write is indexed by the queue's worker, and its delete and soft delete leave the index", func(t *testing.T) {
		if !slices.Contains(keys(t, user(bob), "walnut"), key("Folder", sharedFolder.Id)) {
			t.Fatal("bob does not find the shared folder")
		}
		// Alice keeps the shared folder to herself: its documents inherit
		// it. Neither shows to bob, with no drain, since the trigger copied
		// it.
		private, everyone := "private", "everyone"
		must(db.GetFolderRepository().UpdateOne(asAlice, *sharedFolder.Id, &orm.FolderUpdate{Audience: &private}))
		bobs := keys(t, user(bob), "walnut")
		if slices.Contains(bobs, key("Folder", sharedFolder.Id)) || slices.Contains(bobs, key("Document", sharedDoc.Id)) {
			t.Errorf("bob finds the folder made private: %q", bobs)
		}
		equal(t, "bob's hits", sorted(bobs), listed(t, user(bob).Principal, "walnut", ""))
		must(db.GetFolderRepository().UpdateOne(asAlice, *sharedFolder.Id, &orm.FolderUpdate{Audience: &everyone}))
		if !slices.Contains(keys(t, user(bob), "walnut"), key("Document", sharedDoc.Id)) {
			t.Error("bob does not find the document once its folder is everyone's again")
		}
		// A delete takes the index row with it, at once.
		if err := db.GetRequestRepository().DeleteOne(system, *requestB.Id); err != nil {
			t.Fatal(err)
		}
		if slices.Contains(keys(t, user(bob), "walnut"), key("Request", requestB.Id)) {
			t.Error("the deleted request is found")
		}
		// A change of text waits for the queue: found by its old words
		// until the worker indexes it again.
		chestnut := "Chestnut idea"
		must(db.GetNoteRepository().UpdateOne(system, *noteP1.Id, &orm.NoteUpdate{Title: &chestnut}))
		if got := keys(t, user(bob), "chestnut"); len(got) != 0 {
			t.Errorf("found before the drain: %q", got)
		}
		drained := drain(t, none)
		if drained != 1 {
			t.Errorf("drained %d, want 1", drained)
		}
		equal(t, "chestnut", keys(t, user(bob), "chestnut"), []string{key("Note", noteP1.Id)})
		// An update that changes no indexed field enqueues nothing.
		must(db.GetUnitRepository().UpdateOne(system, *unitA.Id, &orm.UnitUpdate{ContractID: &contractA}))
		if n := drain(t, none); n != 0 {
			t.Errorf("an update of no indexed field enqueued %d", n)
		}
		// A soft delete takes its index row at once.
		if err := db.GetNoteRepository().DeleteOne(system, *noteP2.Id); err != nil {
			t.Fatal(err)
		}
		if got := keys(t, user(bob), "pine"); len(got) != 0 {
			t.Errorf("the soft-deleted note is found: %q", got)
		}
		answers["writes"] = map[string]any{"drained": drained, "bob": sorted(keys(t, user(bob), "walnut"))}
	})

	t.Run("a conversation's messages and a file's extracted text are indexed with their row", func(t *testing.T) {
		if _, err := db.GetUnitRepository().Comments().Post(asAlice, *unitB.Id, orm.UnitCommentsPostParams{Body: "The sycamore by the gate needs a trim."}); err != nil {
			t.Fatal(err)
		}
		if _, err := f.exec(ctx, `INSERT INTO unit_attachments (unit_id, name, media_type, size, content_hash, object_key, state, extraction, extracted_text)
			VALUES ($1, 'survey.pdf', 'application/pdf', 10, $2, 'attachments/survey', 'ready', 'done', 'Juniper hedges line the drive.')`, unitB.Id.ToUUID().String(), strings.Repeat("0", 64)); err != nil {
			t.Fatal(err)
		}
		if n := drain(t, none); n != 1 {
			t.Errorf("drained %d, want the unit's one message", n)
		}
		got := map[string]string{}
		for word, where := range map[string]string{"sycamore": "comments", "juniper": "files"} {
			result, hits := search(t, user(bob), crosstable.SearchRequest{Q: word}, none)
			equal(t, word, hits, []string{key("Unit", unitB.Id)})
			if len(result.Items) == 1 {
				got[word] = result.Items[0].Matched
				if result.Items[0].Matched != where {
					t.Errorf("%s matched in %s, want %s", word, result.Items[0].Matched, where)
				}
			}
		}
		if f.sqlite {
			// The words are FTS5's, kept by the index's triggers: one row
			// each.
			rows := must(f.query(ctx, `SELECT (SELECT count(*) FROM search_index_fts), (SELECT count(*) FROM search_index)`))
			if fmt.Sprint(rows[0][0]) != fmt.Sprint(rows[0][1]) {
				t.Errorf("%v FTS5 rows of %v index rows", rows[0][0], rows[0][1])
			}
		}
		answers["texts"] = got
	})

	t.Run("a scope keeps to its rows, and an embedding is refused on SQLite", func(t *testing.T) {
		chestnuts := keys(t, user(bob), "chestnut")
		_, p1 := search(t, user(bob), crosstable.SearchRequest{Q: "chestnut", Scope: []string{"p1"}}, none)
		equal(t, "p1", p1, chestnuts)
		_, p2 := search(t, user(bob), crosstable.SearchRequest{Q: "chestnut", Scope: []string{"p2"}}, none)
		equal(t, "p2", p2, nil)
		// A relation's key is a scope too, in its wire form.
		_, unit := search(t, user(bob), crosstable.SearchRequest{Q: "walnut", Scope: []string{unitA.Id.String()}}, none)
		equal(t, "the unit's", unit, []string{key("Request", requestA.Id)})
		// Tables keeps to tables, and one that is not @searchable is refused.
		_, contracts := search(t, user(bob), crosstable.SearchRequest{Q: "walnut", Tables: []string{"Contract"}}, none)
		equal(t, "the contracts", sorted(contracts), sorted([]string{key("Contract", &contractA), key("Contract", &contractB)}))
		var bad crosstable.Invalid
		if _, err := crosstable.SearchTables(ctx, host, user(bob), crosstable.SearchRequest{Q: "walnut", Tables: []string{"ContractParty"}}, none); !errors.As(err, &bad) {
			t.Errorf("a table that is not @searchable: %v", err)
		}
		if f.sqlite {
			embeddings := crosstable.SearchOptions{Embeddings: &crosstable.Embeddings{Model: "stand-in-1", Embedder: crosstable.EmbedderFunc(func(_ context.Context, texts []string) ([][]float64, error) {
				return make([][]float64, len(texts)), nil
			})}}
			if _, err := crosstable.SearchTables(ctx, host, user(bob), crosstable.SearchRequest{Q: "walnut", Vector: []float64{1, 0}}, embeddings); !errors.As(err, &bad) || !errors.Is(err, crosstable.ErrSQLiteEmbeddings) && !strings.Contains(err.Error(), "Postgres alone") {
				t.Errorf("a vector on SQLite: %v", err)
			}
			if _, err := crosstable.SearchTables(ctx, host, user(bob), crosstable.SearchRequest{Q: "walnut"}, embeddings); !errors.Is(err, crosstable.ErrSQLiteEmbeddings) {
				t.Errorf("embeddings on SQLite: %v", err)
			}
			if _, err := crosstable.IndexSearch(ctx, host, crosstable.SearchIndexing{SourceTable: "Note", SourceKey: uuidOf(0x602)}, embeddings); !errors.Is(err, crosstable.ErrSQLiteEmbeddings) {
				t.Errorf("an indexing with embeddings on SQLite: %v", err)
			}
		}
		answers["scope"] = map[string]any{"p1": p1, "unit": unit, "contracts": sorted(contracts)}
	})

	t.Run("reindexSearch indexes every row of a table that became searchable", func(t *testing.T) {
		before := sorted(keys(t, user(bob), "walnut"))
		if _, err := f.exec(ctx, `DELETE FROM search_index WHERE source_table = 'Note'`); err != nil {
			t.Fatal(err)
		}
		if got := keys(t, user(bob), "chestnut"); len(got) != 0 {
			t.Errorf("found with no index rows: %q", got)
		}
		n := must(crosstable.ReindexSearch(ctx, host, crosstable.ReindexOptions{Tables: []string{"Note"}, Batch: 2}))
		if n != 3 {
			t.Errorf("reindexed %d rows, want 3", n)
		}
		equal(t, "chestnut", keys(t, user(bob), "chestnut"), []string{key("Note", noteP1.Id)})
		equal(t, "walnut", sorted(keys(t, user(bob), "walnut")), before)
		// The soft-deleted notes stay out of the index.
		rows := must(f.query(ctx, `SELECT count(*) FROM search_index WHERE source_table = 'Note'`))
		answers["reindex"] = []any{n, fmt.Sprint(rows[0][0])}
		if fmt.Sprint(rows[0][0]) != "1" {
			t.Errorf("%v index rows of notes, want the one that is not deleted", rows[0][0])
		}
	})
	return answers
}

func idp0(text string) *types.IdentityUUID { v := id(text); return &v }
