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

// tableOperationCases are the cases tableOperationsTest runs, by name: the
// end-to-end scenario the generated Go server's table operations are held
// to (D106), which the TypeScript server's are held to as well.
var tableOperationCases = []string{
	"the interface holds only the operation the project writes",
	"a team is created, read and listed a page at a time",
	"a second team of one key is a conflict",
	"members are created and listed",
	"an issue is created under its team with the table's defaults and the caller's stamp",
	"an issue is read, and a guest reads only the guest view",
	"a parent that does not exist, or is another row's, is not found",
	"a foreign key that names no row is refused at its member",
	"a list pages by cursor in each sort",
	"a list searches its search fields",
	"a list filters by a field of each kind",
	"a cursor that names no row is refused",
	"an update sets the members the body holds",
	"an update tells a member left out from a null",
	"a null for a column that may not be null is refused at its member",
	"an update of no member writes nothing",
	"a delete keeps the row, stamped, and every operation leaves it out",
	"a comment is created under its issue and deleted for good",
	"a route requires its caller and its permission",
	"a write that records its caller and has none is unauthorized",
	"the operation the project writes reaches its implementation",
}

// TestTableOperationsGoServerServesItsTables builds fixture-tracker-db and
// fixture-tracker-api as build-all does, then writes tableOperationsTest
// into the generated API module and runs it, after go vet: the generated
// Go server's table operations (D106), mounted on handlers that read and
// write the tables of fixture-tracker-db through its ORM on the Postgres
// behaviorServerDatabaseEnv names, called over HTTP with a test
// authenticator in front of the routes, as a Go server the project writes
// mounts them: fixture-tracker-api is not public and has no user model. It
// covers every kind, the path's parents, the soft delete, the audit
// stamps, the defaults, an update's absent, null and empty body, the
// refusals (404, 409, the 400 at a member, 401, 403), a list's page, sort,
// search and filters of every kind, and the one operation the project
// writes. The test skips without the database and in -short mode.
func TestTableOperationsGoServerServesItsTables(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's table operations in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server's table operations against Postgres")
	}
	paths := testpaths.Local(t)
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	for _, service := range []string{"fixture-tracker-db", "fixture-tracker-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		if _, err := Run(schema, cfg, Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         naming.Default(),
			Clock:          clock,
			LoadDependency: loadDependency,
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}

	apiDir := APIDir(outputRoot, "fixture-tracker-api")
	// The module holds tables.go, and its interface only the operation the
	// project writes.
	for file, wants := range map[string][]string{
		"tables.go":     {"func tableIssuesUpdateIssue(", "func asIssueView(row *fixturetrackerdb.Issue) types.IssueView"},
		"interfaces.go": {"SummarizeTeam(ctx context.Context, id types.IdentityUUID) (*types.TeamSummary, error)"},
	} {
		data, err := os.ReadFile(filepath.Join(apiDir, file))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range wants {
			if !strings.Contains(string(data), want) {
				t.Errorf("%s lacks %s", file, want)
			}
		}
	}
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-tracker-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "table_operations_test.go"), []byte(tableOperationsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(apiDir, "table_refusals_internal_test.go"), []byte(tableRefusalsTest), 0o644); err != nil {
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
		// Environ sets PWD to Dir, which keeps the module's relative
		// replace paths valid under a symlinked temp dir.
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module: %v\n%s", strings.Join(args, " "), err, out)
		}
	}
	t.Logf("generated API tests:\n%s", out)
	for _, name := range tableOperationCases {
		if want := "--- PASS: TestTableOperations/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
	if !strings.Contains(string(out), "--- PASS: TestTableRefusals ") {
		t.Error("the generated module's TestTableRefusals did not pass")
	}
}

// tableRefusalsTest runs in the generated module of fixture-tracker-api, in
// its package: what tables.go answers a refusal of the database's with
// (D106, D42), for the refusals the tracker's own operations do not
// reach, a unique key of several columns or over an expression, a foreign
// key the detail does not name, a null a column does not take, and a
// delete a foreign key blocks.
const tableRefusalsTest = `package fixturetrackerapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"testing"

	"github.com/jackc/pgx/v5/pgconn"
)

func TestTableRefusals(t *testing.T) {
	// The repositories wrap the driver's error.
	failed := func(code, detail, constraint, column string) error {
		return fmt.Errorf("failed to scan result: %w", &pgconn.PgError{Code: code, Detail: detail, ConstraintName: constraint, ColumnName: column})
	}
	problem := func(err error) string {
		var app *AppError
		if errors.As(err, &app) {
			return fmt.Sprintf("%d %s %s", app.HTTPStatus(), app.ErrorCode, app.Message)
		}
		var refusal *tableRefusal
		if errors.As(err, &refusal) {
			data, _ := json.Marshal(refusal.errors)
			return "400 " + string(data)
		}
		var driver *pgconn.PgError
		if errors.As(err, &driver) {
			return "the error itself, " + driver.Code
		}
		return err.Error()
	}
	for _, tc := range []struct {
		name string
		got  error
		want string
	}{
		{"a unique key of one column", tableWriteError(failed("23505", "Key (key)=(core) already exists.", "team_key_key", ""), rowsOfTeam), "409 conflict Team with that key already exists"},
		{"a unique key of two columns", tableWriteError(failed("23505", "Key (team_id, title)=(1, x) already exists.", "", ""), rowsOfIssue), "409 conflict Issue with that teamId and title already exists"},
		{"a unique key of three columns", tableWriteError(failed("23505", "Key (team_id, \"title\", status)=(1, x, open) already exists.", "", ""), rowsOfIssue), "409 conflict Issue with that teamId, title and status already exists"},
		{"a unique key in another language", tableWriteError(failed("23505", "Schl\u00fcssel \u00bb(key)=(core)\u00ab existiert bereits.", "", ""), rowsOfTeam), "409 conflict Team with that key already exists"},
		{"a unique key over an expression", tableWriteError(failed("23505", "Key (lower(key::text))=(core) already exists.", "", ""), rowsOfTeam), "409 conflict Team conflicts with an existing row"},
		{"a unique key with no detail", tableWriteError(failed("23505", "", "", ""), rowsOfTeam), "409 conflict Team conflicts with an existing row"},
		{"a foreign key of the body's", tableWriteError(failed("23503", "Key (assignee_id)=(1) is not present in table \"member\".", "fk_issue_assignee_id", ""), rowsOfIssue, "team_id"), "400 {\"assigneeId\":[{\"validator\":\"reference\",\"message\":\"names no Member\"}]}"},
		{"a foreign key of the path's", tableWriteError(failed("23503", "Key (team_id)=(1) is not present in table \"team\".", "fk_issue_team_id", ""), rowsOfIssue, "team_id"), "404 not_found Team not found"},
		{"a foreign key the detail does not name", tableWriteError(failed("23503", "", "fk_issue_assignee_id", ""), rowsOfIssue, "team_id"), "400 {\"assigneeId\":[{\"validator\":\"reference\",\"message\":\"names no Member\"}]}"},
		{"a foreign key of no column of the table", tableWriteError(failed("23503", "Key (owner_id)=(1) is not present in table \"member\".", "fk_other_owner_id", ""), rowsOfIssue), "the error itself, 23503"},
		{"a null a column does not take", tableWriteError(failed("23502", "", "", "title"), rowsOfIssue), "400 {\"title\":[{\"validator\":\"required\",\"message\":\"required field\"}]}"},
		{"a null in a column no body sets", tableWriteError(failed("23502", "", "", "created_by"), rowsOfIssue), "the error itself, 23502"},
		{"a delete a foreign key blocks", tableDeleteError(failed("23503", "Key (id)=(1) is still referenced from table \"issue\".", "fk_issue_team_id", ""), rowsOfTeam), "409 conflict Team is still referenced"},
		{"any other refusal of a delete", tableDeleteError(failed("40001", "", "", ""), rowsOfTeam), "the error itself, 40001"},
		{"any other refusal of a write", tableWriteError(failed("23514", "", "ck_issue_title", ""), rowsOfIssue), "the error itself, 23514"},
	} {
		if got := problem(tc.got); got != tc.want {
			t.Errorf("%s: %s, want %s", tc.name, got, tc.want)
		}
	}
}
`

// tableOperationsTest runs in the generated module of fixture-tracker-api.
// It serves the generated routes over the ORM of fixture-tracker-db, on a
// schema of its own in the Postgres behaviorServerDatabaseEnv names, with
// a test authenticator in front of them, and calls them over HTTP.
const tableOperationsTest = `package fixturetrackerapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/paging"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-tracker-api"
	orm "example.com/schemas/orm/fixture-tracker-db"
	types "example.com/schemas/types/go/fixture-tracker-api"
)

// authenticate is the test's authenticator, in place of the auth an API
// with an authDb wires: Authorization: Bearer <subject>[:<permission>[,...]]
// puts that subject on the request, holding those permissions, as an
// identity middleware puts its principal and roles; no header, no caller.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			subject, held, _ := strings.Cut(token, ":")
			var permissions []string
			if held != "" {
				permissions = strings.Split(held, ",")
			}
			ctx := session.ContextWithPrincipalID(r.Context(), subject)
			ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test", Permissions: permissions}})
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

// teams is the project's implementation of summarizeTeam, the one
// operation no generated handler serves: the team's open issues, counted
// through the ORM.
type teams struct{ db orm.DatabaseInterface }

func (t teams) SummarizeTeam(ctx context.Context, id types.IdentityUUID) (*types.TeamSummary, error) {
	open := "open"
	_, total, err := t.db.GetIssueRepository().FindMany(ctx, &orm.IssueFilter{TeamID: &orm.UUIDFilter{Eq: &id}, Status: &orm.StringFilter{Eq: &open}}, &orm.IssueFindOptions{Fields: orm.IssueFields{Id: true}})
	if err != nil {
		return nil, err
	}
	return &types.TeamSummary{TeamId: id, OpenIssues: float64(total)}, nil
}

// openDatabase is the pool and the ORM of fixture-tracker-db over a schema
// of its own, holding the build's create.sql, which it drops when the test
// ends.
func openDatabase(t *testing.T) (*pgxpool.Pool, *orm.Database) {
	t.Helper()
	target := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if target == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server's table operations against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("table_ops_go_%d", time.Now().UnixNano())
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
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		pool.Close()
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return pool, db
}

// reply is a response: its status and its decoded body.
type reply struct {
	status int
	body   map[string]any
	raw    string
}

// data is a 200's data, an object.
func (r reply) data(t *testing.T) map[string]any {
	t.Helper()
	data, ok := r.body["data"].(map[string]any)
	if r.status != http.StatusOK || !ok {
		t.Fatalf("status %d, body %s, want a 200 with an object", r.status, r.raw)
	}
	return data
}

// client calls the server as one caller.
type client struct {
	t      *testing.T
	server *httptest.Server
	token  string
}

func (c client) do(method, path string, body any) reply {
	c.t.Helper()
	var payload io.Reader
	if body != nil {
		text, isText := body.(string)
		if !isText {
			encoded, err := json.Marshal(body)
			if err != nil {
				c.t.Fatal(err)
			}
			text = string(encoded)
		}
		payload = bytes.NewReader([]byte(text))
	}
	request, err := http.NewRequest(method, c.server.URL+"/api"+path, payload)
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
	raw, err := io.ReadAll(response.Body)
	if err != nil {
		c.t.Fatal(err)
	}
	out := reply{status: response.StatusCode, raw: strings.TrimSpace(string(raw))}
	if len(raw) > 0 {
		if err := json.Unmarshal(raw, &out.body); err != nil {
			c.t.Fatalf("%s %s: the body is not a JSON object: %s", method, path, raw)
		}
	}
	return out
}

func (c client) get(path string) reply                { return c.do(http.MethodGet, path, nil) }
func (c client) post(path string, body any) reply     { return c.do(http.MethodPost, path, body) }
func (c client) patch(path string, body any) reply    { return c.do(http.MethodPatch, path, body) }
func (c client) delete(path string) reply             { return c.do(http.MethodDelete, path, nil) }

// uuid is an ID's hyphenated text, whichever form the wire gave it in.
func uuid(t *testing.T, id any) string {
	t.Helper()
	text, _ := id.(string)
	parsed, err := types.ParseIdentityUUID(text)
	if err != nil {
		t.Fatalf("%v is no ID: %v", id, err)
	}
	return parsed.ToUUID().String()
}

// refused checks a refusal's status, code and detail, and logs its body.
func refused(t *testing.T, what string, got reply, status int, code, detail string) {
	t.Helper()
	t.Logf("%s: %d %s", what, got.status, got.raw)
	// A refusal of the route's own guard carries no code.
	held, _ := got.body["code"].(string)
	if got.status != status || held != code || got.body["detail"] != detail || got.body["status"] != float64(status) {
		t.Errorf("%s: %d %s, want %d %s %q", what, got.status, got.raw, status, code, detail)
	}
}

// refusedAt checks the 400 of a body refused at one member.
func refusedAt(t *testing.T, what string, got reply, member, validator, message string) {
	t.Helper()
	t.Logf("%s: %d %s", what, got.status, got.raw)
	errors, _ := got.body["errors"].(map[string]any)
	list, _ := errors[member].([]any)
	if got.status != http.StatusBadRequest || got.body["code"] != "bad_request" || len(errors) != 1 || len(list) != 1 {
		t.Fatalf("%s: %d %s, want a 400 bad_request at %s alone", what, got.status, got.raw, member)
	}
	if entry, _ := list[0].(map[string]any); entry["validator"] != validator || entry["message"] != message {
		t.Errorf("%s: %s is refused with %v, want %s %q", what, member, list[0], validator, message)
	}
}

const (
	alice   = "11111111-1111-4111-8111-111111111111"
	bob     = "22222222-2222-4222-8222-222222222222"
	missing = "99999999-9999-4999-8999-999999999999"
	every   = ":teams.write,members.write,issues.read,issues.write"
)

func TestTableOperations(t *testing.T) {
	ctx := context.Background()
	pool, db := openDatabase(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{
		DB:              db,
		Logger:          zap.NewNop(),
		Implementations: api.Implementations{Teams: teams{db: db}},
	}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	as := func(token string) client { return client{t: t, server: server, token: token} }
	ada, ben, reader, nobody := as(alice+every), as(bob+every), as(bob+":issues.read"), as("")

	// column reads one column of one row, as text; "" for null.
	column := func(t *testing.T, table, name string, id any) string {
		t.Helper()
		var value *string
		if err := pool.QueryRow(ctx, "SELECT "+name+"::text FROM "+table+" WHERE id = $1", uuid(t, id)).Scan(&value); err != nil {
			t.Fatalf("read %s.%s of %v: %v", table, name, id, err)
		}
		if value == nil {
			return ""
		}
		return *value
	}
	// walk follows next from the first page of path under query, and
	// returns the titles, or the names, of each page's items.
	walk := func(t *testing.T, c client, path string, query url.Values, member string) [][]string {
		t.Helper()
		var pages [][]string
		for {
			if len(pages) > 20 {
				t.Fatalf("more pages than rows: %v", pages)
			}
			target := path
			if encoded := query.Encode(); encoded != "" {
				target += "?" + encoded
			}
			got := c.get(target)
			data := got.data(t)
			items, ok := data["items"].([]any)
			next, hasNext := data["next"]
			if !ok || !hasNext || len(data) != 2 {
				t.Fatalf("GET %s: data is not a page, {items, next}: %s", target, got.raw)
			}
			page := []string{}
			for _, item := range items {
				page = append(page, item.(map[string]any)[member].(string))
			}
			pages = append(pages, page)
			if next == nil {
				return pages
			}
			query = maps(query)
			query.Set("cursor", next.(string))
		}
	}
	// all is walk's pages as one list.
	all := func(t *testing.T, c client, path, query, member string) []string {
		t.Helper()
		values, err := url.ParseQuery(query)
		if err != nil {
			t.Fatal(err)
		}
		if values.Get("limit") == "" {
			values.Set("limit", "5")
		}
		out := []string{}
		for _, page := range walk(t, c, path, values, member) {
			out = append(out, page...)
		}
		return out
	}

	t.Run("the interface holds only the operation the project writes", func(t *testing.T) {
		if got := reflect.TypeOf(api.Implementations{}); got.NumField() != 1 || got.Field(0).Name != "Teams" {
			t.Errorf("Implementations = %v, want Teams alone", got)
		}
		if got := reflect.TypeOf((*api.TeamsImplementation)(nil)).Elem(); got.NumMethod() != 1 || got.Method(0).Name != "SummarizeTeam" {
			t.Errorf("TeamsImplementation = %v, want SummarizeTeam alone", got)
		}
	})

	var core, ops map[string]any
	t.Run("a team is created, read and listed a page at a time", func(t *testing.T) {
		core = ada.post("/teams", map[string]any{"key": "core", "name": "Core"}).data(t)
		if len(core) != 3 || core["key"] != "core" || core["name"] != "Core" || core["id"] == nil {
			t.Fatalf("created %v, want id, key and name", core)
		}
		ops = ada.post("/teams", map[string]any{"key": "ops", "name": "Ops"}).data(t)
		if got := ada.get("/teams/" + core["id"].(string)).data(t); !reflect.DeepEqual(got, core) {
			t.Errorf("read %v, want %v", got, core)
		}
		// The key reads as its hyphenated text too.
		if got := ada.get("/teams/" + uuid(t, core["id"])).data(t); !reflect.DeepEqual(got, core) {
			t.Errorf("read by the hyphenated key %v, want %v", got, core)
		}
		got := walk(t, ada, "/teams", url.Values{"limit": {"1"}}, "key")
		if len(got) != 2 || len(got[0]) != 1 || len(got[1]) != 1 || got[0][0] == got[1][0] {
			t.Errorf("teams, one to a page: %v", got)
		}
		refused(t, "a team no row holds", ada.get("/teams/"+missing), 404, "not_found", "Team not found")
	})

	t.Run("a second team of one key is a conflict", func(t *testing.T) {
		refused(t, "a second team of the key core", ada.post("/teams", map[string]any{"key": "core", "name": "Core again"}), 409, "conflict", "Team with that key already exists")
		if got := all(t, ada, "/teams", "", "key"); len(got) != 2 {
			t.Errorf("teams after the conflict: %v", got)
		}
	})

	var lin, max map[string]any
	t.Run("members are created and listed", func(t *testing.T) {
		lin = ada.post("/members", map[string]any{"name": "Lin"}).data(t)
		max = ada.post("/members", map[string]any{"name": "Max"}).data(t)
		if len(lin) != 2 || lin["name"] != "Lin" {
			t.Fatalf("created %v, want id and name", lin)
		}
		got := all(t, ada, "/members", "", "name")
		sort.Strings(got)
		if !reflect.DeepEqual(got, []string{"Lin", "Max"}) {
			t.Errorf("members = %v", got)
		}
	})

	coreIssues := "/teams/" + fmt.Sprint(core["id"]) + "/issues"
	issues := map[string]map[string]any{}
	create := func(t *testing.T, body map[string]any) map[string]any {
		t.Helper()
		issue := ada.post(coreIssues, body).data(t)
		issues[issue["title"].(string)] = issue
		return issue
	}
	t.Run("an issue is created under its team with the table's defaults and the caller's stamp", func(t *testing.T) {
		crash := create(t, map[string]any{"title": "Crash on save", "body": "100% reproducible", "priority": 3, "estimate": 2.5, "dueOn": "2026-03-01", "assigneeId": lin["id"]})
		if crash["status"] != "open" || crash["priority"] != float64(3) || crash["estimate"] != 2.5 || crash["dueOn"] != "2026-03-01" || crash["body"] != "100% reproducible" {
			t.Errorf("created %v", crash)
		}
		if uuid(t, crash["teamId"]) != uuid(t, core["id"]) || uuid(t, crash["assigneeId"]) != uuid(t, lin["id"]) || uuid(t, crash["createdBy"]) != alice {
			t.Errorf("created %v, want it under core, assigned to Lin, by alice", crash)
		}
		if crash["createdAt"] == nil || crash["updatedAt"] != nil {
			t.Errorf("created %v, want createdAt and no updatedAt", crash)
		}
		// Status and priority unsaid take the table's defaults; null is
		// unsaid too.
		typo := create(t, map[string]any{"title": "Fix typo", "priority": nil, "body": nil})
		if typo["status"] != "open" || typo["priority"] != float64(0) || typo["assigneeId"] != nil || typo["dueOn"] != nil {
			t.Errorf("created %v, want the defaults", typo)
		}
		if got := column(t, "issue", "body", typo["id"]) + column(t, "issue", "estimate", typo["id"]) + column(t, "issue", "assignee_id", typo["id"]); got != "" {
			t.Errorf("the columns the body left unsaid hold %q, want null", got)
		}
		create(t, map[string]any{"title": "Slow_query in list", "priority": 1, "dueOn": "2026-04-15", "assigneeId": max["id"]})
		create(t, map[string]any{"title": "Slowxquery at 50%", "body": "ui polish", "priority": 2, "estimate": 8})
		// Another team's issue.
		other := ada.post("/teams/"+fmt.Sprint(ops["id"])+"/issues", map[string]any{"title": "Ops only"}).data(t)
		if uuid(t, other["teamId"]) != uuid(t, ops["id"]) {
			t.Errorf("created %v, want it under ops", other)
		}
	})
	crash := issues["Crash on save"]
	crashPath := "/issues/" + fmt.Sprint(crash["id"])

	t.Run("an issue is read, and a guest reads only the guest view", func(t *testing.T) {
		if got := reader.get(crashPath).data(t); !reflect.DeepEqual(got, crash) {
			t.Errorf("read %v, want %v", got, crash)
		}
		guest := nobody.get("/public" + crashPath).data(t)
		if want := map[string]any{"id": crash["id"], "title": "Crash on save", "status": "open"}; !reflect.DeepEqual(guest, want) {
			t.Errorf("a guest read %v, want %v", guest, want)
		}
		refused(t, "an issue no row holds", reader.get("/issues/"+missing), 404, "not_found", "Issue not found")
	})

	t.Run("a parent that does not exist, or is another row's, is not found", func(t *testing.T) {
		refused(t, "an issue under a team that does not exist", ada.post("/teams/"+missing+"/issues", map[string]any{"title": "Nowhere"}), 404, "not_found", "Team not found")
		refused(t, "a comment under an issue that does not exist", ada.post("/issues/"+missing+"/comments", map[string]any{"body": "hello"}), 404, "not_found", "Issue not found")
		// Each team lists its own issues.
		if got := all(t, reader, "/teams/"+fmt.Sprint(ops["id"])+"/issues", "", "title"); !reflect.DeepEqual(got, []string{"Ops only"}) {
			t.Errorf("ops's issues = %v", got)
		}
		if got := all(t, reader, "/teams/"+missing+"/issues", "", "title"); len(got) != 0 {
			t.Errorf("the issues of a team that does not exist = %v", got)
		}
	})

	t.Run("a foreign key that names no row is refused at its member", func(t *testing.T) {
		refusedAt(t, "a create assigned to no member", ada.post(coreIssues, map[string]any{"title": "Orphan", "assigneeId": missing}), "assigneeId", "reference", "names no Member")
		refusedAt(t, "an update assigned to no member", ada.patch(crashPath, map[string]any{"assigneeId": missing}), "assigneeId", "reference", "names no Member")
		if got := all(t, reader, coreIssues, "", "title"); slices.Contains(got, "Orphan") {
			t.Errorf("the refused create left a row: %v", got)
		}
		if got := reader.get(crashPath).data(t); !reflect.DeepEqual(got, crash) {
			t.Errorf("the refused update changed the row: %v", got)
		}
	})

	// The team's issues in the order they were created.
	created := []string{"Crash on save", "Fix typo", "Slow_query in list", "Slowxquery at 50%"}
	t.Run("a list pages by cursor in each sort", func(t *testing.T) {
		reversed := slices.Clone(created)
		slices.Reverse(reversed)
		byTitle := slices.Clone(created)
		sort.Strings(byTitle)
		titleDown := slices.Clone(byTitle)
		slices.Reverse(titleDown)
		pages := func(titles []string, limit int) [][]string {
			var out [][]string
			for start := 0; start < len(titles); start += limit {
				out = append(out, titles[start:min(start+limit, len(titles))])
			}
			return out
		}
		for _, tc := range []struct {
			sort string
			want []string
		}{
			{"", reversed},
			{"-createdAt", reversed},
			{"createdAt", created},
			{"priority", []string{"Fix typo", "Slow_query in list", "Slowxquery at 50%", "Crash on save"}},
			{"-priority", []string{"Crash on save", "Slowxquery at 50%", "Slow_query in list", "Fix typo"}},
			{"title", byTitle},
			{"-title", titleDown},
		} {
			query := url.Values{}
			if tc.sort != "" {
				query.Set("sort", tc.sort)
			}
			// The default limit is 2, and the most a page holds 5.
			if got, want := walk(t, reader, coreIssues, maps(query), "title"), pages(tc.want, 2); !reflect.DeepEqual(got, want) {
				t.Errorf("sort=%s: pages = %v, want %v", tc.sort, got, want)
			}
			query.Set("limit", "3")
			if got, want := walk(t, reader, coreIssues, maps(query), "title"), pages(tc.want, 3); !reflect.DeepEqual(got, want) {
				t.Errorf("sort=%s, limit=3: pages = %v, want %v", tc.sort, got, want)
			}
		}
	})

	t.Run("a list searches its search fields", func(t *testing.T) {
		for query, want := range map[string][]string{
			"q=crash":      {"Crash on save"},
			"q=POLISH":     {"Slowxquery at 50%"},
			"q=100%25":     {"Crash on save"},
			"q=slow+list":  {"Slow_query in list"},
			"q=w_q":        {"Slow_query in list"},
			"q=":           {"Slowxquery at 50%", "Slow_query in list", "Fix typo", "Crash on save"},
			"q=nothing":    {},
			"q=slow&sort=title": {"Slow_query in list", "Slowxquery at 50%"},
		} {
			if got := all(t, reader, coreIssues, query, "title"); !reflect.DeepEqual(got, want) {
				t.Errorf("%s: %v, want %v", query, got, want)
			}
		}
	})

	t.Run("a list filters by a field of each kind", func(t *testing.T) {
		at := func(title string) string { return url.QueryEscape(issues[title]["createdAt"].(string)) }
		linID, maxID := fmt.Sprint(lin["id"]), fmt.Sprint(max["id"])
		for _, tc := range []struct {
			name, query string
			want        []string
		}{
			// An enum.
			{"status", "filter[status]=open", created},
			{"status in", "filter[status][in]=started,done", nil},
			// An integer: equality, in, and a range.
			{"priority", "filter[priority]=2", []string{"Slowxquery at 50%"}},
			{"priority in", "filter[priority][in]=0,3", []string{"Crash on save", "Fix typo"}},
			{"priority range", "filter[priority][gte]=1&filter[priority][lt]=3", []string{"Slow_query in list", "Slowxquery at 50%"}},
			{"priority bounds", "filter[priority][gt]=1&filter[priority][lte]=3", []string{"Crash on save", "Slowxquery at 50%"}},
			// Text: equality, in, and contains, whose %, _ and \ match themselves.
			{"title", "filter[title]=Fix+typo", []string{"Fix typo"}},
			{"title in", "filter[title][in]=Fix+typo,Crash+on+save", []string{"Crash on save", "Fix typo"}},
			{"title contains", "filter[title][contains]=SLOW", []string{"Slow_query in list", "Slowxquery at 50%"}},
			{"title contains an underscore", "filter[title][contains]=w_q", []string{"Slow_query in list"}},
			{"title contains a percent sign", "filter[title][contains]=0%25", []string{"Slowxquery at 50%"}},
			{"title contains a percent sign alone", "filter[title][contains]=%25", []string{"Slowxquery at 50%"}},
			{"title contains a backslash", "filter[title][contains]=%5C", nil},
			// A number that may be null.
			{"estimate", "filter[estimate]=2.5", []string{"Crash on save"}},
			{"estimate range", "filter[estimate][gt]=2.5&filter[estimate][lte]=8", []string{"Slowxquery at 50%"}},
			{"estimate in", "filter[estimate][in]=2.5,8", []string{"Crash on save", "Slowxquery at 50%"}},
			{"estimate is null", "filter[estimate][isNull]=true", []string{"Fix typo", "Slow_query in list"}},
			{"estimate is not null", "filter[estimate][isNull]=false", []string{"Crash on save", "Slowxquery at 50%"}},
			// A date that may be null.
			{"dueOn", "filter[dueOn]=2026-03-01", []string{"Crash on save"}},
			{"dueOn range", "filter[dueOn][gte]=2026-03-02&filter[dueOn][lt]=2026-05-01", []string{"Slow_query in list"}},
			{"dueOn in", "filter[dueOn][in]=2026-03-01,2026-04-15", []string{"Crash on save", "Slow_query in list"}},
			{"dueOn is null", "filter[dueOn][isNull]=true", []string{"Fix typo", "Slowxquery at 50%"}},
			// A relation's key that may be null.
			{"assigneeId", "filter[assigneeId]=" + linID, []string{"Crash on save"}},
			{"assigneeId in", "filter[assigneeId][in]=" + linID + "," + maxID, []string{"Crash on save", "Slow_query in list"}},
			{"assigneeId is null", "filter[assigneeId][isNull]=true", []string{"Fix typo", "Slowxquery at 50%"}},
			{"assigneeId is not null", "filter[assigneeId][isNull]=false", []string{"Crash on save", "Slow_query in list"}},
			// A time.
			{"createdAt", "filter[createdAt]=" + at("Fix typo"), []string{"Fix typo"}},
			{"createdAt range", "filter[createdAt][gt]=" + at("Crash on save") + "&filter[createdAt][lte]=" + at("Slow_query in list"), []string{"Fix typo", "Slow_query in list"}},
			{"createdAt in", "filter[createdAt][in]=" + at("Crash on save") + "," + at("Slowxquery at 50%"), []string{"Crash on save", "Slowxquery at 50%"}},
			// Members hold at once, beside a search and a sort.
			{"combined", "filter[status]=open&filter[priority][gte]=1&filter[title][contains]=slow&filter[assigneeId][isNull]=true", []string{"Slowxquery at 50%"}},
			{"combined with a search", "filter[priority][lte]=2&q=slow", []string{"Slow_query in list", "Slowxquery at 50%"}},
			{"no row", "filter[priority]=2&filter[estimate][isNull]=true", nil},
		} {
			got := all(t, reader, coreIssues, tc.query+"&sort=createdAt", "title")
			if len(got) == 0 && len(tc.want) == 0 {
				continue
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("%s (%s): %v, want %v", tc.name, tc.query, got, tc.want)
			}
		}
		// A filter pages with its sort: one to a page.
		if got, want := walk(t, reader, coreIssues, url.Values{"filter[title][contains]": {"slow"}, "sort": {"-priority"}, "limit": {"1"}}, "title"), [][]string{{"Slowxquery at 50%"}, {"Slow_query in list"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("a filtered page of one: %v, want %v", got, want)
		}
	})

	t.Run("a cursor that names no row is refused", func(t *testing.T) {
		gone, err := types.ParseIdentityUUID(missing)
		if err != nil {
			t.Fatal(err)
		}
		cursor, err := paging.EncodeCursor(orm.IssuePageKey{Id: gone})
		if err != nil {
			t.Fatal(err)
		}
		bound, err := paging.BindCursor(cursor, paging.Sort{Field: "title"})
		if err != nil {
			t.Fatal(err)
		}
		refused(t, "a cursor of a row that does not exist", reader.get(coreIssues+"?sort=title&cursor="+bound), 400, "bad_request", paging.RefusedCursorDetail)
		// A key of another operation's holds no id of this one's.
		if cursor, err = paging.EncodeCursor(map[string]string{"name": "Lin"}); err != nil {
			t.Fatal(err)
		}
		if bound, err = paging.BindCursor(cursor, paging.Sort{Field: "title"}); err != nil {
			t.Fatal(err)
		}
		refused(t, "a cursor that holds no key", reader.get(coreIssues+"?sort=title&cursor="+bound), 400, "bad_request", paging.RefusedCursorDetail)
		// An unsorted list's cursor is the key alone.
		refused(t, "a cursor that holds no key, of an unsorted list", reader.get("/teams?cursor="+cursor), 400, "bad_request", paging.RefusedCursorDetail)
	})

	t.Run("an update sets the members the body holds", func(t *testing.T) {
		got := ben.patch(crashPath, map[string]any{"title": "Crash on every save", "status": "started", "priority": 4, "estimate": 3.5, "dueOn": "2026-03-02", "assigneeId": max["id"], "body": "always"}).data(t)
		for member, want := range map[string]any{"title": "Crash on every save", "status": "started", "priority": float64(4), "estimate": 3.5, "dueOn": "2026-03-02", "body": "always", "id": crash["id"], "teamId": crash["teamId"], "createdAt": crash["createdAt"], "createdBy": crash["createdBy"]} {
			if got[member] != want {
				t.Errorf("updated %s = %v, want %v", member, got[member], want)
			}
		}
		if uuid(t, got["assigneeId"]) != uuid(t, max["id"]) || got["updatedAt"] == nil {
			t.Errorf("updated %v, want it assigned to Max and updatedAt set", got)
		}
		if by := column(t, "issue", "updated_by", crash["id"]); by != bob {
			t.Errorf("updated_by = %s, want bob, %s", by, bob)
		}
		if by := column(t, "issue", "created_by", crash["id"]); by != alice {
			t.Errorf("created_by = %s, want alice still", by)
		}
		if read := reader.get(crashPath).data(t); !reflect.DeepEqual(read, got) {
			t.Errorf("read %v after the update, want %v", read, got)
		}
		refused(t, "an update of an issue no row holds", ben.patch("/issues/"+missing, map[string]any{"title": "Nothing"}), 404, "not_found", "Issue not found")
		if got := ben.patch(crashPath, "{\"status\": \"gone\"}"); got.status != 400 {
			t.Errorf("a status the enum lacks: %d %s, want 400", got.status, got.raw)
		}
	})

	t.Run("an update tells a member left out from a null", func(t *testing.T) {
		// One member: every other column stays.
		got := ben.patch(crashPath, map[string]any{"priority": 5}).data(t)
		if got["priority"] != float64(5) || got["title"] != "Crash on every save" || got["estimate"] != 3.5 || got["dueOn"] != "2026-03-02" || got["body"] != "always" || got["assigneeId"] == nil {
			t.Errorf("after one member: %v", got)
		}
		// A null clears a column that may be null, and leaves the others.
		got = ben.patch(crashPath, "{\"body\": null, \"estimate\": null, \"dueOn\": null, \"assigneeId\": null}").data(t)
		if got["body"] != nil || got["estimate"] != nil || got["dueOn"] != nil || got["assigneeId"] != nil || got["priority"] != float64(5) || got["title"] != "Crash on every save" || got["status"] != "started" {
			t.Errorf("after the nulls: %v", got)
		}
		if held := column(t, "issue", "body", crash["id"]) + column(t, "issue", "estimate", crash["id"]) + column(t, "issue", "due_on", crash["id"]) + column(t, "issue", "assignee_id", crash["id"]); held != "" {
			t.Errorf("the cleared columns hold %q, want null", held)
		}
		// A zero is a value, not a null.
		ben.patch(crashPath, "{\"estimate\": 0, \"priority\": 0}").data(t)
		if estimate, priority := column(t, "issue", "estimate", crash["id"]), column(t, "issue", "priority", crash["id"]); estimate != "0" || priority != "0" {
			t.Errorf("estimate = %q and priority = %q, want 0 and 0", estimate, priority)
		}
	})

	t.Run("a null for a column that may not be null is refused at its member", func(t *testing.T) {
		before := column(t, "issue", "updated_at", crash["id"])
		refusedAt(t, "a null title", ben.patch(crashPath, "{\"title\": null}"), "title", "required", "required field")
		refusedAt(t, "a null status", ben.patch(crashPath, "{\"status\": null, \"body\": \"kept out\"}"), "status", "required", "required field")
		refusedAt(t, "a null priority", ben.patch(crashPath, "{\"priority\": null}"), "priority", "required", "required field")
		// An empty title is a value the title's rules refuse, not a
		// member left out.
		refusedAt(t, "an empty title", ben.patch(crashPath, "{\"title\": \"\"}"), "title", "minLength", "must be at least 1 characters")
		refusedAt(t, "an empty title in a create", ada.post(coreIssues, "{\"title\": \"\"}"), "title", "minLength", "must be at least 1 characters")
		// The row is found first: one that does not exist is not found,
		// whatever the body holds.
		refused(t, "a null title for an issue no row holds", ben.patch("/issues/"+missing, "{\"title\": null}"), 404, "not_found", "Issue not found")
		if after := column(t, "issue", "updated_at", crash["id"]); after != before || column(t, "issue", "body", crash["id"]) != "" {
			t.Errorf("a refused update wrote the row: updated_at %s, then %s", before, after)
		}
	})

	t.Run("an update of no member writes nothing", func(t *testing.T) {
		before, by := column(t, "issue", "updated_at", crash["id"]), column(t, "issue", "updated_by", crash["id"])
		want := reader.get(crashPath).data(t)
		if got := ada.patch(crashPath, "{}").data(t); !reflect.DeepEqual(got, want) {
			t.Errorf("an empty update answered %v, want the row, %v", got, want)
		}
		if after := column(t, "issue", "updated_at", crash["id"]); after != before || column(t, "issue", "updated_by", crash["id"]) != by {
			t.Errorf("an empty update wrote the row: updated_at %s, then %s", before, after)
		}
	})

	t.Run("a delete keeps the row, stamped, and every operation leaves it out", func(t *testing.T) {
		typo := issues["Fix typo"]
		path := "/issues/" + fmt.Sprint(typo["id"])
		if got := ben.delete(path).data(t); !reflect.DeepEqual(got, typo) {
			t.Errorf("the delete answered %v, want the row as it was, %v", got, typo)
		}
		if at, by := column(t, "issue", "deleted_at", typo["id"]), column(t, "issue", "deleted_by", typo["id"]); at == "" || by != bob {
			t.Errorf("deleted_at = %q, deleted_by = %q, want the row kept and stamped by bob", at, by)
		}
		refused(t, "a read of the deleted issue", reader.get(path), 404, "not_found", "Issue not found")
		refused(t, "a guest's read of the deleted issue", nobody.get("/public"+path), 404, "not_found", "Issue not found")
		refused(t, "an update of the deleted issue", ben.patch(path, map[string]any{"title": "Back"}), 404, "not_found", "Issue not found")
		refused(t, "a second delete", ben.delete(path), 404, "not_found", "Issue not found")
		if got := all(t, reader, coreIssues, "sort=createdAt", "title"); slices.Contains(got, "Fix typo") || len(got) != 3 {
			t.Errorf("the list after the delete: %v", got)
		}
		refused(t, "a delete of an issue no row holds", ben.delete("/issues/"+missing), 404, "not_found", "Issue not found")
	})

	t.Run("a comment is created under its issue and deleted for good", func(t *testing.T) {
		slow := issues["Slow_query in list"]
		comments, others := crashPath+"/comments", "/issues/"+fmt.Sprint(slow["id"])+"/comments"
		first := ada.post(comments, map[string]any{"body": "seen it too"}).data(t)
		second := ada.post(comments, map[string]any{"body": "fixed in main"}).data(t)
		if len(first) != 4 || first["body"] != "seen it too" || uuid(t, first["issueId"]) != uuid(t, crash["id"]) || first["createdAt"] == nil {
			t.Fatalf("created %v, want id, issueId, body and createdAt", first)
		}
		if got := all(t, reader, comments, "", "body"); len(got) != 2 {
			t.Errorf("the issue's comments = %v", got)
		}
		if got := all(t, reader, others, "", "body"); len(got) != 0 {
			t.Errorf("another issue's comments = %v", got)
		}
		// A comment of another issue is not found under this one.
		refused(t, "a comment deleted under another issue", ben.delete(others+"/"+fmt.Sprint(first["id"])), 404, "not_found", "Comment not found")
		if got := ben.delete(comments + "/" + fmt.Sprint(first["id"])).data(t); !reflect.DeepEqual(got, first) {
			t.Errorf("the delete answered %v, want the comment as it was, %v", got, first)
		}
		var rows int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM comment WHERE id = $1", uuid(t, first["id"])).Scan(&rows); err != nil || rows != 0 {
			t.Errorf("the deleted comment's rows = %d, %v, want none", rows, err)
		}
		if got := all(t, reader, comments, "", "body"); !reflect.DeepEqual(got, []string{"fixed in main"}) || second["body"] != "fixed in main" {
			t.Errorf("the comments after the delete = %v", got)
		}
		refused(t, "a second delete of the comment", ben.delete(comments+"/"+fmt.Sprint(first["id"])), 404, "not_found", "Comment not found")
	})

	t.Run("a route requires its caller and its permission", func(t *testing.T) {
		// A route that requires a caller and no permission.
		refused(t, "the teams with no caller", nobody.get("/teams"), 401, "", "Authentication required")
		refused(t, "a team with no caller", nobody.get("/teams/"+fmt.Sprint(core["id"])), 401, "", "Authentication required")
		refused(t, "the members with no caller", nobody.get("/members"), 401, "", "Authentication required")
		// A route that requires a permission.
		refused(t, "an issue with no caller", nobody.get(crashPath), 401, "", "Authentication required")
		refused(t, "a team created with no caller", nobody.post("/teams", map[string]any{"key": "none", "name": "None"}), 401, "", "Authentication required")
		refused(t, "a team created without teams.write", reader.post("/teams", map[string]any{"key": "none", "name": "None"}), 403, "", "Insufficient permissions")
		refused(t, "an issue updated without issues.write", reader.patch(crashPath, map[string]any{"title": "Mine"}), 403, "", "Insufficient permissions")
		refused(t, "an issue deleted without issues.write", reader.delete(crashPath), 403, "", "Insufficient permissions")
		refused(t, "the issues without issues.read", as(bob+":teams.write").get(coreIssues), 403, "", "Insufficient permissions")
		// A caller with no permission reads what requires none.
		if got := all(t, as(bob), "/teams", "", "key"); len(got) != 2 {
			t.Errorf("the teams, as a caller with no permission: %v", got)
		}
		if got := all(t, ada, "/teams", "", "key"); slices.Contains(got, "none") {
			t.Errorf("a refused create left a team: %v", got)
		}
	})

	t.Run("a write that records its caller and has none is unauthorized", func(t *testing.T) {
		// A principal whose ID is no user's ID stamps no one.
		service := as("billing-worker" + every)
		refused(t, "a create by a caller that is no user", service.post(coreIssues, map[string]any{"title": "By no one"}), 401, "unauthorized", "Authentication required")
		refused(t, "a delete by a caller that is no user", service.delete(crashPath), 401, "unauthorized", "Authentication required")
		// A table that records no caller is written by anyone the route admits.
		if got := service.post("/members", map[string]any{"name": "Robot"}).data(t); got["name"] != "Robot" {
			t.Errorf("a member created by a caller that is no user: %v", got)
		}
	})

	t.Run("the operation the project writes reaches its implementation", func(t *testing.T) {
		got := ada.get("/teams/" + fmt.Sprint(core["id"]) + "/summary").data(t)
		// Crash on every save is started, Fix typo deleted: two are open.
		if uuid(t, got["teamId"]) != uuid(t, core["id"]) || got["openIssues"] != float64(2) || len(got) != 2 {
			t.Errorf("the summary = %v, want two open issues", got)
		}
	})
}

// maps is a copy of query.
func maps(query url.Values) url.Values {
	out := url.Values{}
	for name, values := range query {
		out[name] = slices.Clone(values)
	}
	return out
}
`
