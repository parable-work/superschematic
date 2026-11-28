package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// goAccessCases are the cases goAccessTest runs, by name: D156's "done
// when" in the generated Go server (D180), as TestGeneratedAccessEndToEnd
// holds the TypeScript server to it, and D152's calendar feed, which the
// schema runtime's crosstable serves over the ORM (D187).
var goAccessCases = []string{
	"a private row never appears to another user in a list, a read or a search",
	"the reads, counts and writes of the repositories keep to the caller",
	"a named row is visible to its owner and to the principals it names",
	"a member of a nested team sees a row named for the outer team, and stops seeing it once removed",
	"a document inherits its folder's audience, and follows it when the folder's changes",
	"an agent run sees what whom it acts for sees, and writes no read-only row",
	"a row rule follows a path through the table's relations",
	"a party with a grant reaches only the rows its role's paths allow",
	"an expired or revoked grant is refused on the very next call",
	"a reveal opens the value for a holder of its permission and writes an access-log row",
	"a calendar feed shows only the rows its token's principal reads, busy or in full, and a rotated or revoked one is refused",
}

// TestGoAccessEndToEnd builds fixture-access-go-db and
// fixture-access-go-api as build-all does, then writes goAccessTest into
// the generated API module and runs it, after go vet: the generated Go
// server's per-record access (D156, D180), mounted behind
// session.GrantAuthenticator over the ORM's grants and a test
// authenticator for users and agent runs, on the Postgres
// behaviorServerDatabaseEnv names, in a schema of its own. Every case of
// goAccessCases must pass. It skips without the database and in -short
// mode.
func TestGoAccessEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's access in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's access against Postgres")
	}
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range []string{"fixture-access-go-db", "fixture-access-go-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		result, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         naming.Default(),
			Clock:          clock,
			LoadDependency: loadDependency,
		})
		if err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
		// Every table operation is served, the path rule and the reveal
		// among them.
		for _, warning := range result.Warnings {
			if strings.Contains(warning, "writes no handler") {
				t.Errorf("%s: %s", service, warning)
			}
		}
	}

	apiDir := APIDir(outputRoot, "fixture-access-go-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-access-go-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "access_test.go"), []byte(goAccessTest), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "tidy"},
		{"vet", "./..."},
		{"test", "-count=1", "-v", "./..."},
	} {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, name := range goAccessCases {
		if want := "--- PASS: TestAccess/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}

// goAccessTest runs in the generated module of fixture-access-go-api. It
// serves the generated routes over the ORM of fixture-access-go-db, behind
// the grants' authenticator and the test's own for users and agent runs,
// and calls them over HTTP. A request names its caller in its
// Authorization header: a grant's token runs as the grant's holder;
// <subject>:<permissions> is a user holding those permissions; and
// agent=<subject>/<for>:<permissions> an agent run acting for the user
// <for>.
const goAccessTest = `package fixtureaccessgoapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-access-go-api"
	orm "example.com/schemas/orm/fixture-access-go-db"
	dbtypes "example.com/schemas/types/go/fixture-access-go-db"
)

// uuidOf is the n-th test id, hyphenated.
func uuidOf(n int) string { return fmt.Sprintf("00000000-0000-4000-8000-%012x", n) }

var (
	ownerID, otherID, namedID, partyID, adminID = uuidOf(0xa1), uuidOf(0xa2), uuidOf(0xa3), uuidOf(0xa4), uuidOf(0xa5)
	runID                                       = uuidOf(0xb1)
)

const reads = ":contracts.read,requests.read,requests.write"

var (
	owner          = ownerID + reads
	other          = otherID + reads
	named          = namedID + reads
	party          = partyID + ":requests.read"
	admin          = adminID + ":requests.read,requests.admin"
	revealer       = adminID + ":contracts.read,contracts.reveal"
	agentForOwner  = "agent=" + runID + "/" + ownerID + reads
	agentForOther  = "agent=" + runID + "/" + otherID + reads
)

// callers is the test's authenticator of users and agent runs, behind the
// grants' one: the caller the Authorization header names, holding its
// permissions; none without one.
func callers(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		token, ok := session.BearerToken(r.Header)
		if !ok {
			next.ServeHTTP(w, r)
			return
		}
		who, held, _ := strings.Cut(token, ":")
		var permissions []string
		if held != "" {
			permissions = strings.Split(held, ",")
		}
		caller := session.Caller{Subject: who, Kind: access.KindUser}
		if rest, isAgent := strings.CutPrefix(who, "agent="); isAgent {
			subject, forWhom, _ := strings.Cut(rest, "/")
			caller = session.Caller{Subject: subject, Kind: access.KindAgent, ActingFor: &access.ActingFor{Kind: access.KindUser, Subject: forWhom}}
		}
		ctx := session.ContextWithCaller(r.Context(), caller)
		ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test", Permissions: permissions}})
		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// openDatabase is the ORM of fixture-access-go-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends.
func openDatabase(t *testing.T) *orm.Database {
	t.Helper()
	target := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if target == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL to run the generated Go server's access against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("access_go_%d", time.Now().UnixNano())
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
	ddl, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		pool.Close()
		t.Fatalf("apply create.sql: %v", err)
	}
	key := "BwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwcHBwc="
	db, err := orm.ConnectWithPool(pool, orm.WithFieldKeys(key))
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db
}

type reply struct {
	status int
	body   map[string]any
	raw    string
}

type client struct {
	t     *testing.T
	base  string
	token string
}

func (c client) do(method, path string, body any) reply {
	c.t.Helper()
	var payload io.Reader
	if body != nil {
		encoded, err := json.Marshal(body)
		if err != nil {
			c.t.Fatal(err)
		}
		payload = bytes.NewReader(encoded)
	}
	request, err := http.NewRequest(method, c.base+"/api"+path, payload)
	if err != nil {
		c.t.Fatal(err)
	}
	if body != nil {
		request.Header.Set("Content-Type", "application/json")
	}
	if c.token != "" {
		request.Header.Set("Authorization", "Bearer "+c.token)
	}
	response, err := http.DefaultClient.Do(request)
	if err != nil {
		c.t.Fatal(err)
	}
	defer response.Body.Close()
	raw, _ := io.ReadAll(response.Body)
	out := reply{status: response.StatusCode, raw: strings.TrimSpace(string(raw))}
	if len(raw) > 0 {
		_ = json.Unmarshal(raw, &out.body)
	}
	return out
}

// ok is the data of a call that must succeed.
func (c client) ok(method, path string, body any) map[string]any {
	c.t.Helper()
	r := c.do(method, path, body)
	data, isObject := r.body["data"].(map[string]any)
	if r.status != http.StatusOK || !isObject {
		c.t.Fatalf("%s %s as %q: %d %s, want a 200", method, path, c.token, r.status, r.raw)
	}
	return data
}

// status is the status a call answers.
func (c client) status(method, path string, body any) int {
	c.t.Helper()
	return c.do(method, path, body).status
}

// items is every item of a list the caller sees.
func (c client) items(path string) []map[string]any {
	c.t.Helper()
	var out []map[string]any
	next := ""
	for {
		at := path
		if next != "" {
			sep := "?"
			if strings.Contains(path, "?") {
				sep = "&"
			}
			at += sep + "cursor=" + next
		}
		page := c.ok(http.MethodGet, at, nil)
		items, _ := page["items"].([]any)
		for _, item := range items {
			out = append(out, item.(map[string]any))
		}
		cursor, more := page["next"].(string)
		if !more || cursor == "" {
			return out
		}
		next = cursor
	}
}

// texts are the member of each item, sorted.
func texts(items []map[string]any, member string) []string {
	out := []string{}
	for _, item := range items {
		out = append(out, fmt.Sprint(item[member]))
	}
	sort.Strings(out)
	return out
}

func equal(t *testing.T, what string, got, want []string) {
	t.Helper()
	if !slices.Equal(got, want) {
		t.Errorf("%s: %q, want %q", what, got, want)
	}
}

func is(t *testing.T, what string, got, want int) {
	t.Helper()
	if got != want {
		t.Errorf("%s: %d, want %d", what, got, want)
	}
}

func id(t *testing.T, text string) dbtypes.IdentityUUID {
	t.Helper()
	parsed, err := orm.ParseUserID(text)
	if err != nil {
		t.Fatal(err)
	}
	return parsed
}

// logRows are the access log's rows, oldest first.
func logRows(t *testing.T, db *orm.Database) []*dbtypes.AccessEntry {
	t.Helper()
	rows, _, err := db.GetAccessEntryRepository().FindMany(context.Background(), nil, &orm.AccessEntryFindOptions{Fields: orm.AccessEntryFields{}.All(), Limit: 1000})
	if err != nil {
		t.Fatal(err)
	}
	sort.SliceStable(rows, func(i, j int) bool { return time.Time(rows[i].At).Before(time.Time(rows[j].At)) })
	return rows
}

func ofKind(rows []*dbtypes.AccessEntry, kind string) []*dbtypes.AccessEntry {
	var out []*dbtypes.AccessEntry
	for _, row := range rows {
		if row.Kind == kind {
			out = append(out, row)
		}
	}
	return out
}

func TestAccess(t *testing.T) {
	ctx := context.Background()
	db := openDatabase(t)
	router := chi.NewRouter()
	router.Use(session.TeamAuthenticator(db, session.GrantAuthenticator(db, callers, nil)))
	if err := api.RegisterRoutes(router, api.Config{DB: db, Logger: zap.NewNop()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	as := func(token string) client { return client{t: t, base: server.URL, token: token} }

	// The contracts, written by the system: contract A lists the party.
	system := orm.AsSystem(ctx)
	contractA, err := db.GetContractRepository().CreateOne(system, &dbtypes.Contract{Name: "A", Status: "open", Terms: "Pay on the first"})
	if err != nil {
		t.Fatal(err)
	}
	contractB, err := db.GetContractRepository().CreateOne(system, &dbtypes.Contract{Name: "B", Status: "open", Terms: "Pay on the last"})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := db.GetContractPartyRepository().CreateOne(system, &dbtypes.ContractParty{Contract: dbtypes.Contract{Id: contractA.Id}, Party: id(t, partyID)}); err != nil {
		t.Fatal(err)
	}
	unitA, err := db.GetUnitRepository().CreateOne(system, &dbtypes.Unit{Name: "A1", Contract: dbtypes.Contract{Id: contractA.Id}})
	if err != nil {
		t.Fatal(err)
	}
	unitB, err := db.GetUnitRepository().CreateOne(system, &dbtypes.Unit{Name: "B1", Contract: dbtypes.Contract{Id: contractB.Id}})
	if err != nil {
		t.Fatal(err)
	}
	requestA, err := db.GetRequestRepository().CreateOne(system, &dbtypes.Request{Unit: dbtypes.Unit{Id: unitA.Id}, Title: "Fix the door"})
	if err != nil {
		t.Fatal(err)
	}
	requestB, err := db.GetRequestRepository().CreateOne(system, &dbtypes.Request{Unit: dbtypes.Unit{Id: unitB.Id}, Title: "Paint the hall"})
	if err != nil {
		t.Fatal(err)
	}
	// A role a grant names: what a tenant may do, and the rows it reaches
	// from the contract a grant targets.
	if _, err := db.GetRoleRepository().CreateOne(system, &dbtypes.Role{
		Name:        dbtypes.IdentitySlug("tenant"),
		Permissions: []string{"requests.read", "requests.write", "contracts.read"},
		Reaches:     []string{"Unit:contract", "Request:unit.contract"},
	}); err != nil {
		t.Fatal(err)
	}
	asOwner := orm.WithPrincipal(ctx, access.Principal{Subject: ownerID, Kind: access.KindUser})
	asOther := orm.WithPrincipal(ctx, access.Principal{Subject: otherID, Kind: access.KindUser})

	var mine, secret map[string]any
	t.Run("a private row never appears to another user in a list, a read or a search", func(t *testing.T) {
		mine = as(owner).ok(http.MethodPost, "/folders", map[string]any{"name": "Mine", "owner": ownerID, "audience": "private"})
		if mine["audience"] != "private" {
			t.Errorf("the folder's audience is %v", mine["audience"])
		}
		secret = as(owner).ok(http.MethodPost, "/documents", map[string]any{"folderId": mine["id"], "title": "Secret plan"})
		// A document in a folder inherits its audience.
		if secret["audience"] != "inherit" {
			t.Errorf("the document's audience is %v", secret["audience"])
		}
		as(owner).ok(http.MethodPost, "/documents", map[string]any{"title": "Public plan", "audience": "everyone"})
		equal(t, "the owner's documents", texts(as(owner).items("/documents"), "title"), []string{"Public plan", "Secret plan"})
		equal(t, "another's documents", texts(as(other).items("/documents"), "title"), []string{"Public plan"})
		equal(t, "another's search for plan", texts(as(other).items("/documents?q=plan"), "title"), []string{"Public plan"})
		equal(t, "another's search for secret", texts(as(other).items("/documents?q=secret"), "title"), []string{})
		is(t, "another's read", as(other).status(http.MethodGet, "/documents/"+secret["id"].(string), nil), http.StatusNotFound)
		is(t, "the owner's read", as(owner).status(http.MethodGet, "/documents/"+secret["id"].(string), nil), http.StatusOK)
		if slices.Contains(texts(as(other).items("/folders"), "name"), "Mine") {
			t.Error("another user lists the private folder")
		}
		// A write is held as a read is: the row is not there for another.
		is(t, "another's update", as(other).status(http.MethodPatch, "/documents/"+secret["id"].(string), map[string]any{"title": "Mine now"}), http.StatusNotFound)
		// The repository, outside any operation, reads every row, as a job does.
		row, err := db.GetDocumentRepository().GetOne(ctx, id(t, secret["id"].(string)), nil)
		if err != nil || row.Title != "Secret plan" {
			t.Errorf("outside an operation: %v, %v", row, err)
		}
	})

	t.Run("the reads, counts and writes of the repositories keep to the caller", func(t *testing.T) {
		documents := db.GetDocumentRepository()
		secretID := id(t, secret["id"].(string))
		for _, tc := range []struct {
			name string
			ctx  context.Context
			want int
		}{
			{"the owner", asOwner, 2},
			{"another user", asOther, 1},
			{"the anonymous caller", orm.WithPrincipal(ctx, access.Principal{}), 1},
			{"no operation", ctx, 2},
			{"the system actor", orm.AsSystem(asOther), 2},
		} {
			_, total, err := documents.FindMany(tc.ctx, nil, &orm.DocumentFindOptions{Fields: orm.DocumentFields{}.All()})
			if err != nil {
				t.Fatal(err)
			}
			is(t, tc.name+"'s count", total, tc.want)
		}
		if _, err := documents.GetOne(asOther, secretID, nil); err != orm.ErrNotFound {
			t.Errorf("another's GetOne: %v, want ErrNotFound", err)
		}
		if found, err := documents.GetManyByIDs(asOther, []dbtypes.IdentityUUID{secretID}); err != nil || len(found) != 0 {
			t.Errorf("another's GetManyByIDs: %v, %v", found, err)
		}
		title := "Not yours"
		secretTitle := "Secret plan"
		changed, err := documents.UpdateMany(asOther, &orm.DocumentFilter{Title: &orm.StringFilter{Eq: &secretTitle}}, &orm.DocumentUpdate{Title: &title})
		if err != nil || changed != 0 {
			t.Errorf("another's UpdateMany: %d, %v", changed, err)
		}
		deleted, err := documents.DeleteMany(asOther, &orm.DocumentFilter{Title: &orm.StringFilter{Eq: &secretTitle}})
		if err != nil || deleted != 0 {
			t.Errorf("another's DeleteMany: %d, %v", deleted, err)
		}
		if err := documents.DeleteOne(asOther, secretID); err != orm.ErrNotFound {
			t.Errorf("another's DeleteOne: %v, want ErrNotFound", err)
		}
		// A projection of the table reads as its source's rows the caller sees.
		for _, tc := range []struct {
			name string
			ctx  context.Context
			want int
		}{{"the owner", asOwner, 2}, {"another user", asOther, 1}, {"no operation", ctx, 2}} {
			_, total, err := db.DocumentTitle().FindMany(tc.ctx, nil, nil)
			if err != nil {
				t.Fatal(err)
			}
			is(t, tc.name+"'s count of the projection", total, tc.want)
		}
		if _, err := db.DocumentTitle().GetOne(asOther, secretID); err != orm.ErrNotFound {
			t.Errorf("another's read of the projection's row: %v, want ErrNotFound", err)
		}
		if _, err := db.DocumentTitle().GetOne(asOwner, secretID); err != nil {
			t.Errorf("the owner's read of the projection's row: %v", err)
		}
		// A filter through a relation keeps to the target's audience.
		folderName := "Mine"
		through, _, err := documents.FindMany(asOwner, &orm.DocumentFilter{Folder: &orm.FolderFilter{Name: &orm.StringFilter{Eq: &folderName}}}, &orm.DocumentFindOptions{Fields: orm.DocumentFields{}.All()})
		if err != nil || len(through) != 1 {
			t.Errorf("the owner's documents of Mine: %d, %v", len(through), err)
		}
		// An upsert of a table whose rows keep to the caller is refused.
		if _, err := documents.UpsertOne(asOwner, &dbtypes.Document{Title: "Upserted", Audience: "everyone", AgentAccess: "readOnly"}, orm.DocumentUniqueById, &orm.DocumentUpdate{Title: &title}); err == nil || !strings.Contains(err.Error(), "keep to the operation's caller") {
			t.Errorf("a held upsert: %v", err)
		}
	})

	t.Run("a named row is visible to its owner and to the principals it names", func(t *testing.T) {
		shared, err := db.GetDocumentRepository().CreateOne(asOwner, &dbtypes.Document{Title: "Shared notes", Audience: "named", AudienceNamed: []dbtypes.IdentityUUID{id(t, namedID)}, AgentAccess: "readOnly"})
		if err != nil {
			t.Fatal(err)
		}
		path := "/documents/" + shared.Id.String()
		is(t, "the owner's read", as(owner).status(http.MethodGet, path, nil), http.StatusOK)
		is(t, "the named one's read", as(named).status(http.MethodGet, path, nil), http.StatusOK)
		is(t, "another's read", as(other).status(http.MethodGet, path, nil), http.StatusNotFound)
		if !slices.Contains(texts(as(named).items("/documents"), "title"), "Shared notes") {
			t.Error("the named one does not list it")
		}
		if slices.Contains(texts(as(other).items("/documents"), "title"), "Shared notes") {
			t.Error("another lists it")
		}
	})

	t.Run("a member of a nested team sees a row named for the outer team, and stops seeing it once removed", func(t *testing.T) {
		// The named one is in the inner team, which is in the outer team; the
		// outer team is in the inner one too, a cycle the read of a caller's
		// teams ends.
		outer, err := db.GetTeamRepository().CreateOne(system, &dbtypes.Team{Name: "Outer"})
		if err != nil {
			t.Fatal(err)
		}
		inner, err := db.GetTeamRepository().CreateOne(system, &dbtypes.Team{Name: "Inner"})
		if err != nil {
			t.Fatal(err)
		}
		membership, err := db.GetTeamMemberRepository().CreateOne(system, &dbtypes.TeamMember{Team: *inner.Id, Member: id(t, namedID)})
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range []dbtypes.TeamMember{{Team: *outer.Id, Member: *inner.Id}, {Team: *inner.Id, Member: *outer.Id}} {
			if _, err := db.GetTeamMemberRepository().CreateOne(system, &m); err != nil {
				t.Fatal(err)
			}
		}
		doc, err := db.GetDocumentRepository().CreateOne(asOwner, &dbtypes.Document{Title: "For the outer team", Audience: "named", AudienceNamed: []dbtypes.IdentityUUID{*outer.Id}, AgentAccess: "readOnly"})
		if err != nil {
			t.Fatal(err)
		}
		path := "/documents/" + doc.Id.String()
		is(t, "the member's read", as(named).status(http.MethodGet, path, nil), http.StatusOK)
		if !slices.Contains(texts(as(named).items("/documents"), "title"), "For the outer team") {
			t.Error("the member does not list it")
		}
		is(t, "an agent run's for the member", as("agent="+runID+"/"+namedID+reads).status(http.MethodGet, path, nil), http.StatusOK)
		is(t, "a read of one in no team", as(other).status(http.MethodGet, path, nil), http.StatusNotFound)
		if slices.Contains(texts(as(other).items("/documents"), "title"), "For the outer team") {
			t.Error("one in no team lists it")
		}
		teams, err := db.Teams(ctx, []string{namedID})
		if err != nil {
			t.Fatal(err)
		}
		if len(teams) != 2 {
			t.Errorf("the member's teams are %v, want the inner and the outer once each", teams)
		}
		if err := db.GetTeamMemberRepository().DeleteOne(system, *membership.Id); err != nil {
			t.Fatal(err)
		}
		is(t, "the read once removed", as(named).status(http.MethodGet, path, nil), http.StatusNotFound)
		if slices.Contains(texts(as(named).items("/documents"), "title"), "For the outer team") {
			t.Error("the member removed still lists it")
		}
	})

	t.Run("a document inherits its folder's audience, and follows it when the folder's changes", func(t *testing.T) {
		team := as(owner).ok(http.MethodPost, "/folders", map[string]any{"name": "Team", "owner": ownerID})
		doc := as(owner).ok(http.MethodPost, "/documents", map[string]any{"folderId": team["id"], "title": "Team doc"})
		path := "/documents/" + doc["id"].(string)
		is(t, "another's read", as(other).status(http.MethodGet, path, nil), http.StatusOK)
		// Only the folder's owner changes it: a row rule.
		is(t, "another's change of the folder", as(other).status(http.MethodPatch, "/folders/"+team["id"].(string), map[string]any{"audience": "private"}), http.StatusNotFound)
		as(owner).ok(http.MethodPatch, "/folders/"+team["id"].(string), map[string]any{"audience": "private"})
		is(t, "another's read of a private folder's document", as(other).status(http.MethodGet, path, nil), http.StatusNotFound)
		is(t, "the owner's read", as(owner).status(http.MethodGet, path, nil), http.StatusOK)
		audience, namedOnes := "named", []dbtypes.IdentityUUID{id(t, otherID)}
		if _, err := db.GetFolderRepository().UpdateOne(asOwner, id(t, team["id"].(string)), &orm.FolderUpdate{Audience: &audience, AudienceNamed: &namedOnes}); err != nil {
			t.Fatal(err)
		}
		is(t, "the named one's read", as(other).status(http.MethodGet, path, nil), http.StatusOK)
		is(t, "one the folder does not name", as(named).status(http.MethodGet, path, nil), http.StatusNotFound)
		// A document that leaves its folder's audience keeps its own.
		as(owner).ok(http.MethodPatch, path, map[string]any{"audience": "everyone"})
		as(owner).ok(http.MethodPatch, "/folders/"+team["id"].(string), map[string]any{"audience": "private"})
		is(t, "a read of a document with its own audience", as(named).status(http.MethodGet, path, nil), http.StatusOK)
	})

	t.Run("an agent run sees what whom it acts for sees, and writes no read-only row", func(t *testing.T) {
		before := len(ofKind(logRows(t, db), access.LogAgentRead))
		path := "/documents/" + secret["id"].(string)
		is(t, "a run for the owner", as(agentForOwner).status(http.MethodGet, path, nil), http.StatusOK)
		is(t, "a run for another", as(agentForOther).status(http.MethodGet, path, nil), http.StatusNotFound)
		// Documents are read-only to agent runs unless a row says otherwise.
		is(t, "a run's write of a read-only row", as(agentForOwner).status(http.MethodPatch, path, map[string]any{"title": "Edited"}), http.StatusNotFound)
		open := as(owner).ok(http.MethodPost, "/documents", map[string]any{"title": "Agent may edit", "audience": "everyone", "agentAccess": "followRole"})
		if edited := as(agentForOwner).ok(http.MethodPatch, "/documents/"+open["id"].(string), map[string]any{"title": "Edited by a run"}); edited["title"] != "Edited by a run" {
			t.Errorf("the run's edit: %v", edited)
		}
		closed := as(owner).ok(http.MethodPost, "/documents", map[string]any{"title": "No agents", "audience": "everyone", "agentAccess": "none"})
		is(t, "a run's read of a row closed to runs", as(agentForOwner).status(http.MethodGet, "/documents/"+closed["id"].(string), nil), http.StatusNotFound)
		is(t, "the owner's read of it", as(owner).status(http.MethodGet, "/documents/"+closed["id"].(string), nil), http.StatusOK)
		// Each read the run made is in the access log, with whom it acted for.
		reads := ofKind(logRows(t, db), access.LogAgentRead)
		is(t, "the runs' reads", len(reads), before+3)
		last := reads[len(reads)-1]
		if last.Principal != runID || last.PrincipalKind != "agent" || last.ActingFor != ownerID || last.Operation != "getDocument" {
			t.Errorf("the last read: %+v", last)
		}
	})

	t.Run("a row rule follows a path through the table's relations", func(t *testing.T) {
		equal(t, "the party's requests", texts(as(party).items("/my/requests"), "title"), []string{"Fix the door"})
		equal(t, "another's requests", texts(as(other).items("/my/requests"), "title"), []string{})
		equal(t, "the administrator's requests", texts(as(admin).items("/my/requests"), "title"), []string{"Fix the door", "Paint the hall"})
	})

	var token string
	holder := uuidOf(0xc1)
	t.Run("a party with a grant reaches only the rows its role's paths allow", func(t *testing.T) {
		var err error
		_, token, err = orm.IssueGrant(system, db, access.IssueGrantInput{Holder: holder, HolderKind: "party", Role: "tenant", TargetType: "Contract", TargetID: contractA.Id.String()})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(token, "grant_") {
			t.Fatalf("the token %q", token)
		}
		tenant := as(token)
		equal(t, "the party's requests", texts(tenant.items("/requests"), "title"), []string{"Fix the door"})
		is(t, "a read of its request", tenant.status(http.MethodGet, "/requests/"+requestA.Id.String(), nil), http.StatusOK)
		is(t, "a read of another contract's request", tenant.status(http.MethodGet, "/requests/"+requestB.Id.String(), nil), http.StatusNotFound)
		equal(t, "the party's units", texts(tenant.items("/units"), "name"), []string{"A1"})
		is(t, "a read of its contract", tenant.status(http.MethodGet, "/contracts/"+contractA.Id.String(), nil), http.StatusOK)
		is(t, "a read of another contract", tenant.status(http.MethodGet, "/contracts/"+contractB.Id.String(), nil), http.StatusNotFound)
		// A table the role's paths do not reach has no rows for it.
		equal(t, "the party's documents", texts(tenant.items("/documents"), "title"), []string{})
		// A row it makes is one its grant reaches, or no row at all.
		made := tenant.ok(http.MethodPost, "/units/"+unitA.Id.String()+"/requests", map[string]any{"title": "Leaking tap"})
		if made["unitId"] != unitA.Id.String() {
			t.Errorf("the request it made: %v", made)
		}
		is(t, "a request about another contract's unit", tenant.status(http.MethodPost, "/units/"+unitB.Id.String()+"/requests", map[string]any{"title": "Not mine"}), http.StatusForbidden)
		notMine := "Not mine"
		if rows, _, err := db.GetRequestRepository().FindMany(ctx, &orm.RequestFilter{Title: &orm.StringFilter{Eq: &notMine}}, &orm.RequestFindOptions{Fields: orm.RequestFields{}.All()}); err != nil || len(rows) != 0 {
			t.Errorf("the refused create kept %d rows: %v", len(rows), err)
		}
		// Each call is in the access log, by the grant's id.
		var calls []*dbtypes.AccessEntry
		for _, row := range ofKind(logRows(t, db), access.LogGrantCall) {
			if row.Principal == id(t, holder).String() {
				calls = append(calls, row)
			}
		}
		is(t, "the grant's calls", len(calls), 9)
		if len(calls) > 0 && (calls[0].PrincipalKind != "party" || calls[0].Operation != "listRequests" || calls[0].Credential == "") {
			t.Errorf("the first call: %+v", calls[0])
		}
	})

	t.Run("an expired or revoked grant is refused on the very next call", func(t *testing.T) {
		// Revoked.
		revoked, revokedToken, err := orm.IssueGrant(system, db, access.IssueGrantInput{Holder: uuidOf(0xc2), HolderKind: "party", Role: "tenant", TargetType: "Contract", TargetID: contractA.Id.String()})
		if err != nil {
			t.Fatal(err)
		}
		is(t, "a call with the grant", as(revokedToken).status(http.MethodGet, "/requests", nil), http.StatusOK)
		if err := orm.RevokeGrant(ctx, db, revoked.Id.String(), time.Now()); err != nil {
			t.Fatal(err)
		}
		is(t, "a call after its revocation", as(revokedToken).status(http.MethodGet, "/requests", nil), http.StatusUnauthorized)
		// Ended by its date.
		ends := time.Now().Add(400 * time.Millisecond)
		_, datedToken, err := orm.IssueGrant(system, db, access.IssueGrantInput{Holder: uuidOf(0xc3), HolderKind: "party", Role: "tenant", TargetType: "Contract", TargetID: contractA.Id.String(), EndsAt: &ends})
		if err != nil {
			t.Fatal(err)
		}
		is(t, "a call before its end", as(datedToken).status(http.MethodGet, "/requests", nil), http.StatusOK)
		time.Sleep(500 * time.Millisecond)
		is(t, "a call after its end", as(datedToken).status(http.MethodGet, "/requests", nil), http.StatusUnauthorized)
		// Ended by its target reaching a state.
		_, closingToken, err := orm.IssueGrant(system, db, access.IssueGrantInput{Holder: uuidOf(0xc4), HolderKind: "party", Role: "tenant", TargetType: "Contract", TargetID: contractB.Id.String(), EndsWhenField: "status", EndsWhenState: "closed"})
		if err != nil {
			t.Fatal(err)
		}
		equal(t, "the closing grant's requests", texts(as(closingToken).items("/requests"), "title"), []string{"Paint the hall"})
		closed := "closed"
		if _, err := db.GetContractRepository().UpdateOne(system, *contractB.Id, &orm.ContractUpdate{Status: &closed}); err != nil {
			t.Fatal(err)
		}
		is(t, "a call after its target closed", as(closingToken).status(http.MethodGet, "/requests", nil), http.StatusUnauthorized)
		// An unknown token is refused as an ended one is.
		is(t, "an unknown token", as("grant_unknown").status(http.MethodGet, "/requests", nil), http.StatusUnauthorized)
		// The read of the active grants holds only the first grant still.
		active, err := orm.ActiveGrants(ctx, db, access.ActiveGrantsFilter{})
		if err != nil {
			t.Fatal(err)
		}
		var holders []string
		for _, grant := range active {
			holders = append(holders, grant.Holder)
		}
		equal(t, "the active grants' holders", holders, []string{id(t, holder).String()})
	})

	t.Run("a reveal opens the value for a holder of its permission and writes an access-log row", func(t *testing.T) {
		path := "/contracts/" + contractA.Id.String()
		if masked := as(owner).ok(http.MethodGet, path, nil); masked["terms"] != "[secret]" {
			t.Errorf("a read without the permission: %v", masked["terms"])
		}
		is(t, "the reveals before", len(ofKind(logRows(t, db), access.LogReveal)), 0)
		if opened := as(revealer).ok(http.MethodGet, path, nil); opened["terms"] != "Pay on the first" {
			t.Errorf("a read with the permission: %v", opened["terms"])
		}
		reveals := ofKind(logRows(t, db), access.LogReveal)
		is(t, "the reveals after", len(reveals), 1)
		if len(reveals) == 1 {
			r := reveals[0]
			if r.Principal != adminID || r.PrincipalKind != "user" || r.Operation != "getContract" || r.TargetType != "Contract" || r.TargetId != contractA.Id.String() || r.Field != "terms" {
				t.Errorf("the reveal: %+v", r)
			}
		}
		// A grant's holder whose role holds no reveal sees it masked.
		if masked := as(token).ok(http.MethodGet, path, nil); masked["terms"] != "[secret]" {
			t.Errorf("a grant's read: %v", masked["terms"])
		}
	})

	t.Run("a calendar feed shows only the rows its token's principal reads, busy or in full, and a rotated or revoked one is refused", func(t *testing.T) {
		// The calendar's routes, behind the same authenticators: a feed's
		// fetch carries no header, and reads as its token's holder.
		calendar := httptest.NewServer(session.GrantAuthenticator(db, callers, nil)(crosstable.CalendarRoutes(crosstable.CalendarRoutesOptions{
			Host: db.CrossTables(),
			Authenticate: func(r *http.Request) (*crosstable.Caller, error) {
				caller := session.GetCaller(r.Context())
				if caller.Anonymous() {
					return nil, nil
				}
				var held []string
				for _, role := range session.GetRoles(r.Context()) {
					held = append(held, role.Permissions...)
				}
				return &crosstable.Caller{Principal: caller, Permissions: held}, nil
			},
		})))
		t.Cleanup(calendar.Close)
		if _, err := db.GetRoleRepository().CreateOne(system, &dbtypes.Role{Name: dbtypes.IdentitySlug("calendar-feed"), Permissions: []string{"documents.calendar"}}); err != nil {
			t.Fatal(err)
		}
		send := func(method, path, who string) (int, string, http.Header) {
			t.Helper()
			var body io.Reader
			if method == http.MethodPost && strings.HasSuffix(path, "/feeds") {
				body = strings.NewReader(fmt.Sprintf(` + "`" + `{"role":"calendar-feed","detail":%q}` + "`" + `, who[strings.Index(who, "|")+1:]))
				who = who[:strings.Index(who, "|")]
			}
			request, err := http.NewRequest(method, calendar.URL+path, body)
			if err != nil {
				t.Fatal(err)
			}
			request.Header.Set("Content-Type", "application/json")
			if who != "" {
				request.Header.Set("Authorization", "Bearer "+who)
			}
			response, err := http.DefaultClient.Do(request)
			if err != nil {
				t.Fatal(err)
			}
			defer response.Body.Close()
			raw, _ := io.ReadAll(response.Body)
			return response.StatusCode, string(raw), response.Header
		}
		window := "from=" + time.Now().Add(-24*time.Hour).UTC().Format(time.RFC3339) + "&to=" + time.Now().Add(24*time.Hour).UTC().Format(time.RFC3339)
		// The read: each caller's documents of the day, as each reads them.
		read := func(who string) []string {
			t.Helper()
			status, raw, _ := send(http.MethodGet, "/api/calendar?"+window, who)
			var body struct {
				Data crosstable.Calendar ` + "`" + `json:"data"` + "`" + `
			}
			if err := json.Unmarshal([]byte(raw), &body); err != nil || status != http.StatusOK {
				t.Fatalf("the read as %s: %d %s", who, status, raw)
			}
			var titles []string
			for _, entry := range body.Data.Entries {
				titles = append(titles, entry.Title)
			}
			return titles
		}
		if titles := read(owner); !slices.Contains(titles, "Secret plan") || !slices.Contains(titles, "Public plan") {
			t.Errorf("the owner's calendar: %q", titles)
		}
		if titles := read(other); !slices.Contains(titles, "Public plan") || slices.Contains(titles, "Secret plan") {
			t.Errorf("another's calendar: %q", titles)
		}
		type feed struct {
			ID, Token, Path string
		}
		issue := func(who, detail string) feed {
			t.Helper()
			status, raw, _ := send(http.MethodPost, "/api/calendar/feeds", who+"|"+detail)
			var body struct{ Data feed }
			if err := json.Unmarshal([]byte(raw), &body); err != nil || status != http.StatusCreated {
				t.Fatalf("issue a feed as %s: %d %s", who, status, raw)
			}
			return body.Data
		}
		// The other user's feed, in full, read by a client with no header.
		others := issue(other, "full")
		if !strings.HasPrefix(others.Token, "grant_") {
			t.Fatalf("the feed's token %q", others.Token)
		}
		status, ics, header := send(http.MethodGet, others.Path, "")
		if status != http.StatusOK || !strings.Contains(header.Get("Content-Type"), "text/calendar") {
			t.Fatalf("the feed: %d %s %s", status, header.Get("Content-Type"), ics)
		}
		if !strings.Contains(ics, "SUMMARY:Public plan") || strings.Contains(ics, "Secret plan") {
			t.Errorf("another's feed:\n%s", ics)
		}
		// The owner's busy feed holds the owner's private document, as Busy
		// and a reference, and no title.
		owners := issue(owner, "busy")
		_, busy, _ := send(http.MethodGet, owners.Path, "")
		if strings.Contains(busy, "plan") || !strings.Contains(busy, "SUMMARY:Busy (") {
			t.Errorf("the busy feed:\n%s", busy)
		}
		ownersDocuments := len(read(owner))
		if events := strings.Count(busy, "BEGIN:VEVENT"); events != ownersDocuments {
			t.Errorf("the busy feed holds %d events, and the owner reads %d", events, ownersDocuments)
		}
		// A feed's token is no credential of any other route.
		is(t, "a feed's token on another route", as(others.Token).status(http.MethodGet, "/documents", nil), http.StatusUnauthorized)
		// Rotated: the old URL is refused, the new one reads.
		status, raw, _ := send(http.MethodPost, "/api/calendar/feeds/"+others.ID+"/rotate", other)
		var rotated struct{ Data feed }
		if err := json.Unmarshal([]byte(raw), &rotated); err != nil || status != http.StatusOK {
			t.Fatalf("rotate: %d %s", status, raw)
		}
		if status, _, _ := send(http.MethodGet, others.Path, ""); status != http.StatusUnauthorized {
			t.Errorf("the rotated feed's old URL: %d", status)
		}
		if status, _, _ := send(http.MethodGet, rotated.Data.Path, ""); status != http.StatusOK {
			t.Errorf("the rotated feed's new URL: %d", status)
		}
		// Another user cannot revoke it; its holder can, and it is refused.
		if status, _, _ := send(http.MethodDelete, "/api/calendar/feeds/"+rotated.Data.ID, owner); status != http.StatusNotFound {
			t.Errorf("another's revocation: %d", status)
		}
		if status, _, _ := send(http.MethodDelete, "/api/calendar/feeds/"+rotated.Data.ID, other); status != http.StatusNoContent {
			t.Errorf("the holder's revocation: %d", status)
		}
		if status, _, _ := send(http.MethodGet, rotated.Data.Path, ""); status != http.StatusUnauthorized {
			t.Errorf("the revoked feed: %d", status)
		}
		if status, _, _ := send(http.MethodGet, "/api/calendar/feed/grant_unknown.ics", ""); status != http.StatusUnauthorized {
			t.Errorf("an unknown feed: %d", status)
		}
	})
}
`
