// fixture-search-db end to end in the Go repositories and the schema
// runtime's crosstable package (D155, D187), run by TestGoSearchEndToEnd:
// six @searchable tables share one search index. The generated create.sql
// is applied in a schema of the test's own on Postgres, and the cases are
// the TypeScript repositories' (tsgen's search.test.ts) and the Rust
// ones', through the generated repositories and crosstable over the ORM's
// CrossTables. drain runs what the queue's worker runs: each ready message
// of the SearchIndexing queue, handed to IndexSearch.
package orm_test

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"

	orm "example.com/schemas/orm/fixture-search-db"
	types "example.com/schemas/types/go/fixture-search-db"
)

func uuidOf(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var (
	alice = uuidOf(0xa1)
	bob   = uuidOf(0xb0)
	party = "3cKSm7Pv2EM8Wl4z4XvMh1"
)

func user(subject string) crosstable.Caller {
	return crosstable.Caller{Principal: access.Principal{Subject: subject, Kind: access.KindUser}}
}

// as is ctx reading and writing as principal, as a route binds it.
func as(principal access.Principal) context.Context {
	return orm.WithPrincipal(context.Background(), principal)
}

var system = orm.AsSystem(context.Background())

func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	target := os.Getenv("SEARCH_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set SEARCH_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("search_go_%d", time.Now().UnixNano())
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
	// The contract's terms are sealed under this key.
	db, err := orm.ConnectWithPool(pool, orm.WithFieldKeys("BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc="))
	if err != nil {
		t.Fatal(err)
	}
	return db, pool
}

// must is a call's value, which must succeed: a failure panics, which
// fails the test with its stack.
func must[T any](value T, err error) T {
	if err != nil {
		panic(err)
	}
	return value
}

// must2 is a two-valued call that must succeed.
func must2[A, B any](a A, b B, err error) (A, B) {
	if err != nil {
		panic(err)
	}
	return a, b
}

func id(text string) types.IdentityUUID {
	return must(orm.ParseUserID(text))
}

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

func TestSearch(t *testing.T) {
	db, pool := openDatabase(t)
	ctx := context.Background()
	host := db.CrossTables()
	asAlice := as(access.Principal{Subject: alice, Kind: access.KindUser})
	now := types.TemporalDateTime(time.Now())

	// drain is what the queue's worker runs: every ready message, indexed.
	drain := func(t *testing.T, options crosstable.SearchOptions) int {
		t.Helper()
		n := 0
		for {
			rows, err := pool.Query(ctx, `DELETE FROM search_indexing_queue WHERE id IN (SELECT id FROM search_indexing_queue WHERE state = 'ready' ORDER BY created_at LIMIT 100) RETURNING source_table, source_key`)
			if err != nil {
				t.Fatal(err)
			}
			var messages []crosstable.SearchIndexing
			for rows.Next() {
				var m crosstable.SearchIndexing
				if err := rows.Scan(&m.SourceTable, &m.SourceKey); err != nil {
					t.Fatal(err)
				}
				messages = append(messages, m)
			}
			rows.Close()
			if len(messages) == 0 {
				return n
			}
			for _, m := range messages {
				if _, err := crosstable.IndexSearch(ctx, host, m, options); err != nil {
					t.Fatal(err)
				}
			}
			n += len(messages)
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
	// listed is the rows of every @searchable table principal's list reads
	// show whose title or body holds word, as table:key, sorted.
	listed := func(t *testing.T, principal access.Principal, word string) []string {
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
		for _, row := range must(first(db.GetContractRepository().FindMany(c, nil, &orm.ContractFindOptions{Fields: orm.ContractFields{}.All(), Limit: 1000}))) {
			add("Contract", row.Id, row.Name, row.Summary)
		}
		for _, row := range must(first(db.GetUnitRepository().FindMany(c, nil, &orm.UnitFindOptions{Fields: orm.UnitFields{}.All(), Limit: 1000}))) {
			add("Unit", row.Id, row.Name, row.Notes)
		}
		for _, row := range must(first(db.GetRequestRepository().FindMany(c, nil, &orm.RequestFindOptions{Fields: orm.RequestFields{}.All(), Limit: 1000}))) {
			add("Request", row.Id, row.Title, row.Details, row.Resolution)
		}
		for _, row := range must(first(db.GetNoteRepository().FindMany(c, nil, &orm.NoteFindOptions{Fields: orm.NoteFields{}.All(), Limit: 1000}))) {
			add("Note", row.Id, row.Title, row.Text)
		}
		return sorted(out)
	}

	// Two folders: alice's own, and one everyone sees; a document in each.
	privateFolder := must(db.GetFolderRepository().CreateOne(asAlice, &types.Folder{Name: "Walnut private", Owner: id(alice), Audience: "private", AgentAccess: "followRole", CreatedAt: now}))
	sharedFolder := must(db.GetFolderRepository().CreateOne(asAlice, &types.Folder{Name: "Walnut shared", Owner: id(alice), Audience: "everyone", AgentAccess: "followRole", CreatedAt: now}))
	privateDoc := must(db.GetDocumentRepository().CreateOne(asAlice, &types.Document{Folder: &types.Folder{Id: privateFolder.Id}, Title: "Walnut plan", Text: "The plan in brief.", Audience: "inherit", AgentAccess: "followRole", CreatedAt: now}))
	sharedDoc := must(db.GetDocumentRepository().CreateOne(asAlice, &types.Document{Folder: &types.Folder{Id: sharedFolder.Id}, Title: "Memo", Text: "A walnut desk for the hall.", Audience: "inherit", AgentAccess: "followRole", CreatedAt: now}))
	// Two contracts, the first with a unit and a request about it.
	contractA := must(db.GetContractRepository().CreateOne(system, &types.Contract{Name: "Walnut supply", Summary: "Timber for the year.", Terms: "Pay in obsidian."}))
	contractB := must(db.GetContractRepository().CreateOne(system, &types.Contract{Name: "Bolts", Summary: "Bolts and a walnut trim."}))
	unitA := must(db.GetUnitRepository().CreateOne(system, &orm.UnitCreate{Name: "Walnut unit", Contract: types.Contract{Id: contractA.Id}}))
	unitB := must(db.GetUnitRepository().CreateOne(system, &orm.UnitCreate{Name: "Annex", Notes: "Walnut floors.", Contract: types.Contract{Id: contractB.Id}}))
	requestA := must(db.GetRequestRepository().CreateOne(system, &types.Request{Unit: types.Unit{Id: unitA.Id}, Title: "Walnut leak", Details: "Water under the sink."}))
	requestB := must(db.GetRequestRepository().CreateOne(system, &types.Request{Unit: types.Unit{Id: unitB.Id}, Title: "Hinge", Details: "A walnut door sticks."}))
	// Notes in two projects, one deleted.
	noteP1 := must(db.GetNoteRepository().CreateOne(system, &types.Note{Project: "p1", Title: "Walnut idea", Text: "Oil it twice."}))
	noteP2 := must(db.GetNoteRepository().CreateOne(system, &types.Note{Project: "p2", Title: "Pine idea", Text: "A walnut stain on pine."}))
	noteGone := must(db.GetNoteRepository().CreateOne(system, &types.Note{Project: "p1", Title: "Walnut draft", Text: "Gone soon."}))
	if err := db.GetNoteRepository().DeleteOne(system, *noteGone.Id); err != nil {
		t.Fatal(err)
	}
	// A role a party's grant names: the units and requests of its contract.
	must(db.GetRoleRepository().CreateOne(system, &types.Role{Name: types.IdentitySlug("contract-party"), Permissions: []string{}, Reaches: []string{"Unit:contract", "Request:unit.contract"}}))
	if drain(t, none) == 0 {
		t.Fatal("the creates enqueued nothing")
	}
	key := func(table string, row *types.IdentityUUID) string { return table + ":" + row.String() }

	t.Run("two principals with different access search the same text and each gets exactly the rows their list reads would show, ranked across at least five tables", func(t *testing.T) {
		for _, subject := range []string{alice, bob} {
			caller := user(subject)
			result, got := search(t, caller, crosstable.SearchRequest{Q: "walnut"}, none)
			equal(t, subject+"'s hits", sorted(got), listed(t, caller.Principal, "walnut"))
			if result.Total != len(got) {
				t.Errorf("total %d, %d hits", result.Total, len(got))
			}
			tables := map[string]bool{}
			for _, hit := range result.Items {
				tables[hit.Table] = true
			}
			if len(tables) < 5 {
				t.Errorf("the hits are of %d tables: %v", len(tables), tables)
			}
			// A page of three at a time reads the same rows, in the same
			// order, with the same total on every page.
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
			equal(t, "the pages", paged, got)
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
		// A match in a title ranks above one in a body alone.
		if slices.Index(bobs, key("Contract", contractA.Id)) > slices.Index(bobs, key("Contract", contractB.Id)) {
			t.Errorf("the title's match ranks below the body's: %q", bobs)
		}
		result, _ := search(t, user(bob), crosstable.SearchRequest{Q: "walnut"}, none)
		for _, hit := range result.Items {
			if hit.Key != contractB.Id.String() {
				continue
			}
			var matched []string
			for _, part := range hit.Snippet {
				if part.Match {
					matched = append(matched, strings.ToLower(part.Text))
				}
			}
			if hit.Matched != "body" || !slices.Equal(matched, []string{"walnut"}) {
				t.Errorf("the body's hit: %+v", hit)
			}
		}
		// Two words: a row holds both.
		equal(t, "walnut sink", keys(t, user(bob), "walnut sink"), []string{key("Request", requestA.Id)})
		// An anonymous caller sees what everyone may.
		equal(t, "the anonymous caller's", sorted(keys(t, crosstable.Caller{}, "walnut")), sorted(bobs))
	})

	t.Run("a party with a grant finds only the rows its grant reaches, through the route and the MCP tool too, and a revoked grant finds nothing", func(t *testing.T) {
		grant, token := must2(orm.IssueGrant(system, db, access.IssueGrantInput{Holder: party, HolderKind: "party", Role: "contract-party", TargetType: "Contract", TargetID: contractA.Id.String()}))
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
		result, got := search(t, *principal, crosstable.SearchRequest{Q: "walnut"}, none)
		equal(t, "the party's hits", sorted(got), listed(t, principal.Principal, "walnut"))
		equal(t, "the party's hits", sorted(got), sorted([]string{key("Contract", contractA.Id), key("Request", requestA.Id), key("Unit", unitA.Id)}))
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
		equal(t, "the route's hits", routed, got[:2])
		// The MCP tool, as the same principal.
		called, problem, err := crosstable.CallSearchTool(ctx, host, *principal, map[string]any{"q": "walnut", "tables": []any{"Unit", "Request", "Folder"}}, none)
		if err != nil || problem != nil {
			t.Fatalf("the tool: %v %+v", err, problem)
		}
		var tooled []string
		for _, hit := range called.Items {
			tooled = append(tooled, hit.Table+":"+hit.Key)
		}
		equal(t, "the tool's hits", sorted(tooled), sorted([]string{key("Request", requestA.Id), key("Unit", unitA.Id)}))
		// An agent run's token is a grant too (kind agent): its search
		// keeps to what the grant reaches, as the MCP endpoint answers it.
		_, runToken := must2(orm.IssueGrant(system, db, access.IssueGrantInput{Holder: uuidOf(0xa9), HolderKind: "agent", Role: "contract-party", TargetType: "Contract", TargetID: contractA.Id.String()}))
		agent := must(authenticate(runToken))
		if agent.Principal.Kind != access.KindAgent {
			t.Fatalf("the run's principal is a %s", agent.Principal.Kind)
		}
		called, _, err = crosstable.CallSearchTool(ctx, host, *agent, map[string]any{"q": "walnut"}, none)
		if err != nil {
			t.Fatal(err)
		}
		tooled = nil
		for _, hit := range called.Items {
			tooled = append(tooled, hit.Table+":"+hit.Key)
		}
		equal(t, "the run's hits", sorted(tooled), sorted(got))
		equal(t, "the run's hits", sorted(tooled), listed(t, agent.Principal, "walnut"))
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
	})

	t.Run("a row's conversation and files are indexed with it, and an encrypted field never is", func(t *testing.T) {
		if _, err := db.GetUnitRepository().Comments().Post(asAlice, *unitB.Id, orm.UnitCommentsPostParams{Body: "The sycamore by the gate needs a trim."}); err != nil {
			t.Fatal(err)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO unit_attachments (unit_id, name, media_type, size, content_hash, object_key, state, extraction, extracted_text)
			VALUES ($1, 'survey.pdf', 'application/pdf', 10, $2, 'attachments/survey', 'ready', 'done', 'Juniper hedges line the drive.')`, unitB.Id.ToUUID().String(), strings.Repeat("0", 64)); err != nil {
			t.Fatal(err)
		}
		if n := drain(t, none); n != 1 {
			t.Errorf("drained %d, want the unit's one message", n)
		}
		for word, where := range map[string]string{"sycamore": "comments", "juniper": "files"} {
			result, got := search(t, user(bob), crosstable.SearchRequest{Q: word}, none)
			equal(t, word, got, []string{key("Unit", unitB.Id)})
			if len(result.Items) == 1 && result.Items[0].Matched != where {
				t.Errorf("%s matched in %s, want %s", word, result.Items[0].Matched, where)
			}
		}
		// The contract's terms are sealed at rest and never reach the index.
		if result, _ := search(t, user(alice), crosstable.SearchRequest{Q: "obsidian"}, none); result.Total != 0 {
			t.Errorf("the sealed terms are found: %+v", result)
		}
		var n int
		if err := pool.QueryRow(ctx, `SELECT count(*)::int FROM search_index WHERE concat_ws(' ', title, body, comments, files) ILIKE '%obsidian%'`).Scan(&n); err != nil || n != 0 {
			t.Errorf("index rows holding the terms: %d, %v", n, err)
		}
	})

	t.Run("an index row follows its row: an audience change and a delete at once, a change of text once the queue drains", func(t *testing.T) {
		if !slices.Contains(keys(t, user(bob), "walnut"), key("Folder", sharedFolder.Id)) {
			t.Fatal("bob does not find the shared folder")
		}
		// Alice keeps the shared folder to herself: its documents inherit
		// it. Neither shows to bob, with no drain, since the trigger
		// copied it.
		private, everyone := "private", "everyone"
		must(db.GetFolderRepository().UpdateOne(asAlice, *sharedFolder.Id, &orm.FolderUpdate{Audience: &private}))
		bobs := keys(t, user(bob), "walnut")
		if slices.Contains(bobs, key("Folder", sharedFolder.Id)) || slices.Contains(bobs, key("Document", sharedDoc.Id)) {
			t.Errorf("bob finds the folder made private: %q", bobs)
		}
		equal(t, "bob's hits", sorted(bobs), listed(t, user(bob).Principal, "walnut"))
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
		if n := drain(t, none); n != 1 {
			t.Errorf("drained %d, want 1", n)
		}
		equal(t, "chestnut", keys(t, user(bob), "chestnut"), []string{key("Note", noteP1.Id)})
		// An update that changes no indexed field enqueues nothing.
		must(db.GetUnitRepository().UpdateOne(system, *unitA.Id, &orm.UnitUpdate{ContractID: contractA.Id}))
		if n := drain(t, none); n != 0 {
			t.Errorf("an update of no indexed field enqueued %d", n)
		}
		// A table's index rows lost are built again by ReindexSearch, every
		// row of it, the soft-deleted one removed again.
		if _, err := pool.Exec(ctx, `DELETE FROM search_index WHERE source_table = 'Note'`); err != nil {
			t.Fatal(err)
		}
		if got := keys(t, user(bob), "idea"); len(got) != 0 {
			t.Errorf("found with no index rows: %q", got)
		}
		if n := must(crosstable.ReindexSearch(ctx, host, crosstable.ReindexOptions{Tables: []string{"Note"}, Batch: 2})); n != 3 {
			t.Errorf("reindexed %d rows, want 3", n)
		}
		equal(t, "idea", sorted(keys(t, user(bob), "idea")), sorted([]string{key("Note", noteP1.Id), key("Note", noteP2.Id)}))
	})

	t.Run("scope keeps a search to the app's filter", func(t *testing.T) {
		_, p1 := search(t, user(bob), crosstable.SearchRequest{Q: "idea", Scope: []string{"p1"}}, none)
		equal(t, "p1", p1, []string{key("Note", noteP1.Id)})
		_, both := search(t, user(bob), crosstable.SearchRequest{Q: "idea", Scope: []string{"p1", "p2"}}, none)
		equal(t, "p1 and p2", sorted(both), sorted([]string{key("Note", noteP1.Id), key("Note", noteP2.Id)}))
		// A relation's key is a scope too, in its wire form.
		_, unit := search(t, user(bob), crosstable.SearchRequest{Q: "walnut", Scope: []string{unitA.Id.String()}}, none)
		equal(t, "the unit's", unit, []string{key("Request", requestA.Id)})
		// Tables keeps to tables, and one that is not @searchable is refused.
		_, contracts := search(t, user(bob), crosstable.SearchRequest{Q: "walnut", Tables: []string{"Contract"}}, none)
		equal(t, "the contracts", sorted(contracts), sorted([]string{key("Contract", contractA.Id), key("Contract", contractB.Id)}))
		var bad crosstable.Invalid
		if _, err := crosstable.SearchTables(ctx, host, user(bob), crosstable.SearchRequest{Q: "walnut", Tables: []string{"ContractParty"}}, none); !errors.As(err, &bad) {
			t.Errorf("a table that is not @searchable: %v", err)
		}
	})

	t.Run("embeddings re-rank the first hits, are kept while the text holds, and can be switched off", func(t *testing.T) {
		// Embeddings through a model provider that embeds; one that embeds
		// nothing is refused.
		var calls atomic.Int64
		embeddings, err := providers.SearchEmbeddingsOf(doors{&calls}, "stand-in-1", 0)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := providers.SearchEmbeddingsOf(talker{}, "stand-in-1", 0); !errors.Is(err, providers.ErrDoesNotEmbed) {
			t.Errorf("a provider that embeds nothing: %v", err)
		}
		withDoors := crosstable.SearchOptions{Embeddings: embeddings}
		everyRow := `INSERT INTO search_indexing_queue (source_table, source_key) SELECT source_table, source_key FROM search_index`
		if _, err := pool.Exec(ctx, everyRow); err != nil {
			t.Fatal(err)
		}
		drain(t, withDoors)
		var rows, embedded int
		if err := pool.QueryRow(ctx, `SELECT count(*)::int, (count(*) FILTER (WHERE embedding_model = 'stand-in-1'))::int FROM search_index`).Scan(&rows, &embedded); err != nil || rows != embedded {
			t.Fatalf("%d of %d rows embedded: %v", embedded, rows, err)
		}
		// A row whose text did not change keeps its embedding: no model
		// call.
		before := calls.Load()
		if _, err := pool.Exec(ctx, everyRow); err != nil {
			t.Fatal(err)
		}
		drain(t, withDoors)
		if calls.Load() != before {
			t.Errorf("%d model calls for rows whose text held", calls.Load()-before)
		}
		// A change of text clears its embedding, until it is embedded again.
		summary := "Bolts, a walnut trim and a door."
		must(db.GetContractRepository().UpdateOne(system, *contractB.Id, &orm.ContractUpdate{Summary: &summary}))
		drain(t, none)
		stored := func() []float64 {
			var v []float64
			if err := pool.QueryRow(ctx, `SELECT embedding FROM search_index WHERE source_table = 'Contract' AND source_key = $1`, contractB.Id.ToUUID().String()).Scan(&v); err != nil {
				t.Fatal(err)
			}
			return v
		}
		if v := stored(); v != nil {
			t.Errorf("the changed text kept its embedding %v", v)
		}
		if _, err := pool.Exec(ctx, `INSERT INTO search_indexing_queue (source_table, source_key) VALUES ('Contract', $1)`, contractB.Id.ToUUID().String()); err != nil {
			t.Fatal(err)
		}
		drain(t, withDoors)
		if v := stored(); !slices.Equal(v, []float64{1, 0}) {
			t.Errorf("the embedding is %v, want [1 0]", v)
		}

		// The text alone ranks the body-only match low; a query about
		// doors lifts it, over the same rows and the same total.
		plain, plainKeys := search(t, user(bob), crosstable.SearchRequest{Q: "walnut"}, none)
		lifted, liftedKeys := search(t, user(bob), crosstable.SearchRequest{Q: "walnut", Vector: []float64{1, 0}}, withDoors)
		if plain.Reranked || !lifted.Reranked || lifted.Total != plain.Total {
			t.Errorf("reranked %t and %t, totals %d and %d", plain.Reranked, lifted.Reranked, plain.Total, lifted.Total)
		}
		equal(t, "the re-ranked rows", sorted(liftedKeys), sorted(plainKeys))
		place := func(keys []string) int { return slices.Index(keys, key("Contract", contractB.Id)) }
		if place(liftedKeys) >= place(plainKeys) {
			t.Errorf("the door's contract is at %d re-ranked and %d by text", place(liftedKeys), place(plainKeys))
		}
		// With embeddings on and no vector, the query is embedded: the same.
		allDoors := crosstable.SearchOptions{Embeddings: &crosstable.Embeddings{Model: "stand-in-1", Embedder: crosstable.EmbedderFunc(func(_ context.Context, texts []string) ([][]float64, error) {
			out := make([][]float64, len(texts))
			for i := range out {
				out[i] = []float64{1, 0}
			}
			return out, nil
		})}}
		_, embeddedKeys := search(t, user(bob), crosstable.SearchRequest{Q: "walnut"}, allDoors)
		equal(t, "the query embedded", embeddedKeys, liftedKeys)
		// A model that fails leaves the text order; switched off, the text
		// order.
		down := crosstable.SearchOptions{Embeddings: &crosstable.Embeddings{Model: "stand-in-1", Embedder: crosstable.EmbedderFunc(func(context.Context, []string) ([][]float64, error) {
			return nil, errors.New("down")
		})}}
		failing, failingKeys := search(t, user(bob), crosstable.SearchRequest{Q: "walnut"}, down)
		if failing.Reranked {
			t.Error("a model that is down re-ranked")
		}
		equal(t, "a model that is down", failingKeys, plainKeys)
		equal(t, "switched off", keys(t, user(bob), "walnut"), plainKeys)
		// A vector with no embeddings to say its model is refused.
		var bad crosstable.Invalid
		if _, err := crosstable.SearchTables(ctx, host, user(bob), crosstable.SearchRequest{Q: "walnut", Vector: []float64{1, 0}}, none); !errors.As(err, &bad) {
			t.Errorf("a vector with no embeddings: %v", err)
		}
	})
}

// first is a FindMany's rows, its count dropped.
func first[R any](rows []R, _ int, err error) ([]R, error) { return rows, err }


// doors is a stand-in model provider (D159): a text's vector says whether
// it is about a door, counting the texts it embeds.
type doors struct{ calls *atomic.Int64 }

func (doors) Complete(context.Context, providers.ModelTurn) (providers.ModelCompletion, error) {
	return providers.ModelCompletion{}, errors.New("the stand-in completes nothing")
}

func (d doors) Embed(_ context.Context, request providers.ModelEmbedRequest) (providers.ModelEmbedding, error) {
	if request.Model != "stand-in-1" {
		return providers.ModelEmbedding{}, fmt.Errorf("embedded with %q", request.Model)
	}
	d.calls.Add(int64(len(request.Texts)))
	out := providers.ModelEmbedding{}
	for _, text := range request.Texts {
		if strings.Contains(strings.ToLower(text), "door") {
			out.Vectors = append(out.Vectors, []float64{1, 0})
		} else {
			out.Vectors = append(out.Vectors, []float64{0, 1})
		}
	}
	return out, nil
}

// talker is a model provider that embeds nothing.
type talker struct{}

func (talker) Complete(context.Context, providers.ModelTurn) (providers.ModelCompletion, error) {
	return providers.ModelCompletion{}, errors.New("the stand-in completes nothing")
}
