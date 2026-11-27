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
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// graphServerDatabaseEnv names the Postgres the generated servers' graph
// operations run on, the one the TypeScript server's are held to
// (tsrestgen's TestGeneratedGraphOperationsEndToEnd).
const graphServerDatabaseEnv = "SUPERSCHEMATIC_VERSIONGRAPH_TEST_DATABASE_URL"

// graphOperationCases are the cases graphOperationsTest runs, by name: the
// end-to-end scenario of D138 the TypeScript server's graph operations are
// held to (tsrestgen's graphCases), but its MCP case, which a Go server of
// fixture-graph-api has no endpoint for.
var graphOperationCases = []string{
	"branch creates the primary line of a root that has none, and a change set of it",
	"save writes rows with order keys, and the tree reads them back",
	"arrange puts a parent's children in the order given",
	"commit writes a commit, and a stale version is a version conflict",
	"a change set is its creator's: another caller is not found, and the override permission lifts the rule",
	"merge with a tag commits onto the primary line, and history, diff and materialize read it",
	"release needs its own permission, a second release at the same version conflicts, and released reads the tree",
	"entity history lists the commits that changed an entity",
	"a rebase that conflicts answers the conflicts and writes nothing, and a resolution settles it",
	"revert composes a change set to an earlier commit's tree",
	"a sealed ref refuses a save, and discard frees the name",
	"an input the core refuses is unprocessable",
	"a root, a ref or a commit that is not this root's is not found",
	"a caller without the route's permission is refused by the API's auth, and a write needs a caller who is a UUID",
}

// buildGraphFixture builds fixture-version-graph-db and fixture-graph-api
// as build-all does, the API as a server of language, under outputRoot.
// A Rust server's database gets its Rust types too.
func buildGraphFixture(t *testing.T, outputRoot, language string) *Result {
	t.Helper()
	paths := testpaths.Local(t)
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	rustTypes := func(cfg *schemaconfig.SchemaConfig) {
		if language != APILanguageRust {
			return
		}
		types, _ := cfg.Outputs["types"].(map[string]any)
		if types == nil {
			types = map[string]any{}
			cfg.Outputs["types"] = types
		}
		types["rust"] = map[string]any{"enabled": true}
	}
	var result *Result
	for _, service := range []string{"fixture-version-graph-db", "fixture-graph-api"} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, service))
		if err != nil {
			t.Fatalf("load %s: %v", service, err)
		}
		rustTypes(cfg)
		if service == "fixture-graph-api" {
			cfg.Outputs["api"] = map[string]any{"enabled": true, "language": language}
		}
		result, err = Run(schema, cfg, Options{
			OutputRoot: outputRoot,
			Paths:      paths,
			Naming:     naming.Default(),
			Clock:      clock,
			LoadDependency: func(name string) (*ir.Schema, error) {
				return loader.LoadService(filepath.Join(tsFixtures, name))
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				_, cfg, err := loader.LoadServiceWithConfig(filepath.Join(tsFixtures, name))
				if err != nil {
					return nil, false
				}
				rustTypes(cfg)
				return cfg, true
			},
		})
		if err != nil {
			t.Fatalf("run %s as %s: %v", service, language, err)
		}
	}
	return result
}

// writeGraphScenario writes graphOperationsTest and the database's
// create.sql into the Go module of fixture-graph-api built under
// outputRoot, and returns the module's directory.
func writeGraphScenario(t *testing.T, outputRoot string) string {
	t.Helper()
	apiDir := APIDir(outputRoot, "fixture-graph-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-version-graph-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "graph_operations_test.go"), []byte(graphOperationsTest), 0o644); err != nil {
		t.Fatal(err)
	}
	return apiDir
}

// runGraphScenario runs go mod tidy, then each of steps, in the module of
// apiDir with env, and checks that every case of graphOperationCases
// passed, against what serves the API: the generated Go server, or the
// server env names.
func runGraphScenario(t *testing.T, apiDir, against string, env []string, steps ...[]string) {
	t.Helper()
	var out []byte
	for _, args := range append([][]string{{"mod", "tidy"}}, steps...) {
		cmd := exec.Command("go", args...)
		cmd.Dir = apiDir
		// Environ sets PWD to Dir, which keeps the module's relative
		// replace paths valid under a symlinked temp dir.
		cmd.Env = append(cmd.Environ(), env...)
		var err error
		out, err = cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("go %s in the generated API module, against %s: %v\n%s", strings.Join(args, " "), against, err, out)
		}
	}
	t.Logf("the graph operations against %s:\n%s", against, out)
	for _, name := range graphOperationCases {
		if want := "--- PASS: TestGraphOperations/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("%s did not pass %q", against, name)
		}
	}
}

// TestGraphOperationsGoServerServesTheGraph builds fixture-version-graph-db
// and fixture-graph-api as build-all does, with the API as a Go server,
// then writes graphOperationsTest into the generated API module and runs
// it, after go vet: the generated Go server's graph operations (D138),
// mounted on the methods of graphs.go, which read the root, its refs and
// its commits through the ORM of fixture-version-graph-db and run each
// operation through its Recipe graph's generated facade, on the Postgres
// graphServerDatabaseEnv names, called over HTTP with a test authenticator
// in front of the routes. The build warns of no graph operation, and the
// interface holds getRecipe alone. The test skips without the database and
// in -short mode.
func TestGraphOperationsGoServerServesTheGraph(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server's graph operations in -short mode")
	}
	if os.Getenv(graphServerDatabaseEnv) == "" {
		t.Skip("set " + graphServerDatabaseEnv + " to run the generated Go server's graph operations against Postgres")
	}
	outputRoot := t.TempDir()
	result := buildGraphFixture(t, outputRoot, APILanguageGo)
	for _, warning := range result.Warnings {
		if strings.Contains(warning, "@graphOperations") {
			t.Errorf("the Go build warned of a graph operation: %s", warning)
		}
	}
	apiDir := writeGraphScenario(t, outputRoot)
	for file, wants := range map[string][]string{
		"graphs.go":     {"func (g *recipeOperationsRoutes) EntityHistory(", "func GraphsOf(db *orm.Database) Graphs {"},
		"interfaces.go": {"GetRecipe(ctx context.Context, id types.IdentityUUID) (*types.Recipe, error)"},
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
		if file == "interfaces.go" && strings.Contains(string(data), "Branch(") {
			t.Errorf("interfaces.go holds a graph operation:\n%s", data)
		}
	}
	runGraphScenario(t, apiDir, "the Go server", []string{"CGO_LDFLAGS=" + archiveLDFlags(t)},
		[]string{"vet", "./..."},
		[]string{"test", "-count=1", "-v", "-run", "^TestGraphOperations$", "./..."},
	)
}

// graphOperationsTest runs in the generated module of fixture-graph-api.
// It serves the generated routes over the ORM of fixture-version-graph-db,
// on a schema of its own in the Postgres graphServerDatabaseEnv names,
// with a test authenticator in front of them, and calls them over HTTP;
// or, with GRAPH_OPERATIONS_SERVER set, starts that binary over the same
// schema and calls it instead.
const graphOperationsTest = `package fixturegraphapi_test

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"slices"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-graph-api"
	orm "example.com/schemas/orm/fixture-version-graph-db"
	types "example.com/schemas/types/go/fixture-graph-api"
	dbtypes "example.com/schemas/types/go/fixture-version-graph-db"
)

// authenticate is the test's authenticator: Authorization: Bearer
// <subject>[:<permission>[,...]] puts that subject on the request, holding
// those permissions; no header, no caller.
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

// recipes is the project's implementation of getRecipe, the one operation
// no generated handler serves.
type recipes struct{ db orm.DatabaseInterface }

func (r recipes) GetRecipe(ctx context.Context, id types.IdentityUUID) (*types.Recipe, error) {
	row, err := r.db.GetRecipeRepository().GetOne(ctx, id, nil)
	if err != nil {
		return nil, err
	}
	var out types.Recipe
	data, err := json.Marshal(row)
	if err != nil {
		return nil, err
	}
	return &out, json.Unmarshal(data, &out)
}

// openDatabase is the pool and the ORM of fixture-version-graph-db over a
// schema of its own, holding the build's create.sql, which it drops when
// the test ends.
func openDatabase(t *testing.T) (*orm.Database, string) {
	t.Helper()
	target := os.Getenv("` + graphServerDatabaseEnv + `")
	if target == "" {
		t.Skip("set ` + graphServerDatabaseEnv + ` to run the generated server's graph operations against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("graph_ops_go_%d", time.Now().UnixNano())
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
	return db, schema
}

// externalServerEnv names the binary of another server of the same API,
// which the scenario then calls in the Go server's place: the generated
// Rust server's. The binary reads its database from
// GRAPH_OPERATIONS_DATABASE_URL, a URL whose search path is the scenario's
// schema, listens on a port of its own on the loopback interface, and
// prints "listening on <url>".
const externalServerEnv = "GRAPH_OPERATIONS_SERVER"

// startExternalServer starts the server binary over the scenario's schema
// and returns its URL, once it prints it.
func startExternalServer(t *testing.T, binary, schema string) string {
	t.Helper()
	target, err := url.Parse(os.Getenv("` + graphServerDatabaseEnv + `"))
	if err != nil {
		t.Fatal(err)
	}
	query := target.Query()
	query.Set("options", "-c search_path="+schema+",public")
	// A Postgres URL's reader takes a space as %20, not as the + a form
	// writes.
	target.RawQuery = strings.ReplaceAll(query.Encode(), "+", "%20")
	server := exec.Command(binary)
	server.Env = append(os.Environ(), "GRAPH_OPERATIONS_DATABASE_URL="+target.String())
	server.Stderr = os.Stderr
	stdout, err := server.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := server.Start(); err != nil {
		t.Fatalf("start %s: %v", binary, err)
	}
	t.Cleanup(func() {
		_ = server.Process.Kill()
		_ = server.Wait()
	})
	lines := bufio.NewScanner(stdout)
	for lines.Scan() {
		if address, ok := strings.CutPrefix(lines.Text(), "listening on "); ok {
			return strings.TrimSpace(address)
		}
	}
	t.Fatalf("%s ended without saying where it listens", binary)
	return ""
}

// reply is a response: its status and its decoded body.
type reply struct {
	status int
	body   map[string]any
	raw    string
}

// data is a 200's data.
func (r reply) data(t *testing.T) any {
	t.Helper()
	data, ok := r.body["data"]
	if r.status != http.StatusOK || !ok {
		t.Fatalf("status %d, body %s, want a 200 with data", r.status, r.raw)
	}
	return data
}

// object is a 200's data, an object.
func (r reply) object(t *testing.T) map[string]any {
	t.Helper()
	data, ok := r.data(t).(map[string]any)
	if !ok {
		t.Fatalf("data is not an object: %s", r.raw)
	}
	return data
}

// list is a 200's data, a list.
func (r reply) list(t *testing.T) []any {
	t.Helper()
	data, ok := r.data(t).([]any)
	if !ok {
		t.Fatalf("data is not a list: %s", r.raw)
	}
	return data
}

// client calls the server as one caller.
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

func (c client) get(path string) reply            { return c.do(http.MethodGet, path, nil) }
func (c client) post(path string, body any) reply { return c.do(http.MethodPost, path, body) }
func (c client) put(path string, body any) reply  { return c.do(http.MethodPut, path, body) }

// id is an ID's hyphenated text, whichever form the wire gave it in.
func id(t *testing.T, value any) string {
	t.Helper()
	text, _ := value.(string)
	parsed, err := types.ParseIdentityUUID(text)
	if err != nil {
		t.Fatalf("%v is no ID: %v", value, err)
	}
	return parsed.ToUUID().String()
}

// refused checks a refusal's status and, when code is not empty, its
// code and, when detail is not empty, its detail.
func refused(t *testing.T, what string, got reply, status int, code, detail string) {
	t.Helper()
	t.Logf("%s: %d %s", what, got.status, got.raw)
	if got.status != status || (code != "" && got.body["code"] != code) || (detail != "" && got.body["detail"] != detail) {
		t.Errorf("%s: %d %s, want %d %s %q", what, got.status, got.raw, status, code, detail)
	}
}

// member is the value at a dotted path of an object.
func member(value any, path string) any {
	for _, name := range strings.Split(path, ".") {
		object, ok := value.(map[string]any)
		if !ok {
			return nil
		}
		value = object[name]
	}
	return value
}

// number is a JSON number as an int.
func number(t *testing.T, value any) int {
	t.Helper()
	n, ok := value.(float64)
	if !ok {
		t.Fatalf("%v is no number", value)
	}
	return int(n)
}

// headings are the headings of a tree's parts, in its order.
func headings(t *testing.T, tree map[string]any) []string {
	t.Helper()
	var out []string
	parts, _ := tree["part"].([]any)
	for _, part := range parts {
		out = append(out, member(part, "heading").(string))
	}
	return out
}

// partOf is the part of a tree whose entity key is key.
func partOf(t *testing.T, tree map[string]any, key string) map[string]any {
	t.Helper()
	parts, _ := tree["part"].([]any)
	for _, part := range parts {
		if id(t, member(part, "entityKey")) == key {
			return part.(map[string]any)
		}
	}
	t.Fatalf("the tree holds no part %s", key)
	return nil
}

func TestGraphOperations(t *testing.T) {
	ctx := context.Background()
	db, schema := openDatabase(t)
	var base string
	if external := os.Getenv(externalServerEnv); external != "" {
		base = startExternalServer(t, external, schema)
	} else {
		router := chi.NewRouter()
		router.Use(chimiddleware.RequestID)
		router.Use(authenticate)
		if err := api.RegisterRoutes(router, api.Config{
			DB:              db,
			Graphs:          api.GraphsOf(db),
			Logger:          zap.NewNop(),
			Implementations: api.Implementations{Recipe: recipes{db: db}},
		}); err != nil {
			t.Fatal(err)
		}
		server := httptest.NewServer(router)
		t.Cleanup(server.Close)
		base = server.URL
	}
	// newID is a random UUID, hyphenated.
	newID := func() string {
		var raw [16]byte
		if _, err := rand.Read(raw[:]); err != nil {
			t.Fatal(err)
		}
		raw[6] = raw[6]&0x0f | 0x40
		raw[8] = raw[8]&0x3f | 0x80
		return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:16])
	}
	ann, bob, ada, pat := newID(), newID(), newID(), newID()
	as := func(token string) client { return client{t: t, base: base, token: token} }
	annC := as(ann + ":recipes.write")
	bobC := as(bob + ":recipes.write")
	adaC := as(ada + ":recipes.write,recipes.admin")
	patC := as(pat + ":recipes.write,recipes.release")
	leeC := as(newID())
	samC := as("sam@kitchen:recipes.write")
	nobody := as("")

	annID, err := orm.ParseUserID(ann)
	if err != nil {
		t.Fatal(err)
	}
	// newRecipe is a recipe row, created as ann.
	newRecipe := func(title string) string {
		t.Helper()
		row, err := db.GetRecipeRepository().CreateOne(orm.WithUserID(ctx, annID), &dbtypes.Recipe{Title: title})
		if err != nil {
			t.Fatal(err)
		}
		return row.Id.ToUUID().String()
	}
	// versionOf is a ref's version as its row holds it.
	versionOf := func(ref string) int {
		t.Helper()
		refID, err := orm.ParseUserID(ref)
		if err != nil {
			t.Fatal(err)
		}
		row, err := db.GetRecipeRefRepository().GetOne(ctx, refID, nil)
		if err != nil {
			t.Fatal(err)
		}
		return int(row.Version)
	}
	// primaryOf is the primary line of a recipe: its id and its creator.
	primaryOf := func(recipe string) (string, string) {
		t.Helper()
		root, err := orm.ParseUserID(recipe)
		if err != nil {
			t.Fatal(err)
		}
		none := true
		row, err := db.GetRecipeRefRepository().FindOne(ctx, &orm.RecipeRefFilter{RootID: &orm.UUIDFilter{Eq: &root}, ParentRefID: &orm.UUIDFilter{IsNull: &none}}, nil)
		if err != nil {
			t.Fatalf("the recipe has no primary line: %v", err)
		}
		return row.Id.ToUUID().String(), row.CreatedBy.ToUUID().String() + "|" + row.Name
	}

	recipe := newRecipe("Soup")
	var main, firstCommit, tagged string
	var draft struct {
		id      string
		version int
	}
	var parts []string
	var rebased struct {
		id      string
		version int
	}

	t.Run("branch creates the primary line of a root that has none, and a change set of it", func(t *testing.T) {
		created := annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "draft"}).object(t)
		if created["name"] != "draft" || created["sealed"] != false || created["discarded"] != false || id(t, created["root"]) != recipe {
			t.Errorf("the change set: %v", created)
		}
		primary, made := primaryOf(recipe)
		if made != ann+"|main" {
			t.Errorf("the primary line is %s, want made by ann and named main", made)
		}
		if id(t, created["parent"]) != primary {
			t.Errorf("the change set's parent is %v, want the primary line %s", created["parent"], primary)
		}
		main = primary
		draft.id, draft.version = id(t, created["id"]), number(t, created["version"])

		other := bobC.post("/recipes/"+recipe+"/refs", map[string]any{"from": main, "name": "other"}).object(t)
		if id(t, other["parent"]) != main {
			t.Errorf("a branch of the ref from names: %v", other)
		}
		if again, _ := primaryOf(recipe); again != main {
			t.Errorf("a second primary line: %s", again)
		}
		refused(t, "a name taken", annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "draft"}), 409, "name_taken", "")
	})

	t.Run("save writes rows with order keys, and the tree reads them back", func(t *testing.T) {
		ranks, err := db.RecipeGraph().OrderKeys("", "", 3)
		if err != nil {
			t.Fatal(err)
		}
		saved := annC.put("/recipes/"+recipe+"/refs/"+draft.id+"/edits", map[string]any{
			"version": draft.version,
			"edits": map[string]any{
				"part": map[string]any{"upsert": []any{
					map[string]any{"rank": ranks[0], "heading": "Intro"},
					map[string]any{"rank": ranks[1], "heading": "Method"},
					map[string]any{"rank": ranks[2], "heading": "Notes"},
				}},
				"step": map[string]any{"upsert": []any{map[string]any{"position": 1, "instruction": "Boil the stock", "timings": map[string]any{"boil": 10}}}},
			},
		}).object(t)
		if v := number(t, member(saved, "ref.version")); v <= draft.version {
			t.Errorf("the ref's version %d did not move past %d", v, draft.version)
		} else {
			draft.version = v
		}
		savedParts, _ := member(saved, "saved.part").([]any)
		if got := headings(t, saved["saved"].(map[string]any)); !slices.Equal(got, []string{"Intro", "Method", "Notes"}) {
			t.Errorf("the saved parts: %v", got)
		}
		if steps, _ := member(saved, "saved.step").([]any); len(steps) != 1 {
			t.Errorf("the saved steps: %v", steps)
		}
		for _, part := range savedParts {
			parts = append(parts, id(t, member(part, "entityKey")))
		}

		tree := annC.get("/recipes/" + recipe + "/refs/" + draft.id + "/tree").object(t)
		if got := headings(t, tree); !slices.Equal(got, []string{"Intro", "Method", "Notes"}) {
			t.Errorf("the tree's parts: %v", got)
		}
		// A row holds its entity key and its content, and nothing else of it.
		var keys []string
		for key := range tree["part"].([]any)[0].(map[string]any) {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if !slices.Contains([]string{"entityKey", "heading", "parentKey", "rank"}, key) {
				t.Errorf("a part's row holds %s: %v", key, keys)
			}
		}
		step := tree["step"].([]any)[0]
		if number(t, member(step, "position")) != 1 || member(step, "instruction") != "Boil the stock" || number(t, member(step, "timings.boil")) != 10 {
			t.Errorf("the step: %v", step)
		}
		if covers, _ := tree["cover"].([]any); len(covers) != 0 || tree["contentHash"] == "" {
			t.Errorf("the tree: %v", tree)
		}
		if findings, ok := tree["findings"].([]any); !ok || len(findings) != 0 {
			t.Errorf("the tree's findings: %v", tree["findings"])
		}
	})

	t.Run("arrange puts a parent's children in the order given", func(t *testing.T) {
		arranged := annC.post("/recipes/"+recipe+"/refs/"+draft.id+"/arrange", map[string]any{"version": draft.version, "kind": "part", "keys": []string{parts[2], parts[0], parts[1]}}).object(t)
		draft.version = number(t, member(arranged, "ref.version"))
		if moved, _ := member(arranged, "saved.part").([]any); len(moved) == 0 {
			t.Errorf("arrange wrote no part: %v", arranged)
		}
		tree := annC.get("/recipes/" + recipe + "/refs/" + draft.id + "/tree").object(t)
		if got := headings(t, tree); !slices.Equal(got, []string{"Notes", "Intro", "Method"}) {
			t.Errorf("the arranged parts: %v", got)
		}
	})

	t.Run("commit writes a commit, and a stale version is a version conflict", func(t *testing.T) {
		refused(t, "a stale version", annC.post("/recipes/"+recipe+"/refs/"+draft.id+"/commit", map[string]any{"version": draft.version - 1, "message": "stale"}), 409, "version_conflict", "")
		committed := annC.post("/recipes/"+recipe+"/refs/"+draft.id+"/commit", map[string]any{"version": draft.version, "message": "three parts"}).object(t)
		commit, _ := committed["commit"].(map[string]any)
		if id(t, commit["root"]) != recipe || id(t, commit["ref"]) != draft.id || commit["message"] != "three parts" || commit["sequence"] != nil {
			t.Errorf("the commit: %v", commit)
		}
		draft.version = number(t, member(committed, "ref.version"))
		firstCommit = id(t, commit["id"])
		refused(t, "nothing to commit", annC.post("/recipes/"+recipe+"/refs/"+draft.id+"/commit", map[string]any{"version": draft.version}), 409, "nothing_to_commit", "")
	})

	t.Run("a change set is its creator's: another caller is not found, and the override permission lifts the rule", func(t *testing.T) {
		edits := map[string]any{"cover": map[string]any{"upsert": []any{map[string]any{"photoUrl": "https://example.com/soup.jpg"}}}}
		refused(t, "bob's save", bobC.put("/recipes/"+recipe+"/refs/"+draft.id+"/edits", map[string]any{"version": draft.version, "edits": edits}), 404, "not_found", "ref not found")
		refused(t, "bob's commit", bobC.post("/recipes/"+recipe+"/refs/"+draft.id+"/commit", map[string]any{"version": draft.version}), 404, "not_found", "")
		refused(t, "bob's discard", bobC.post("/recipes/"+recipe+"/refs/"+draft.id+"/discard", map[string]any{"version": draft.version}), 404, "not_found", "")
		// A merge writes its target: a merge into another caller's change
		// set is not found as a save to it is, whatever the source.
		refused(t, "bob's merge into ann's", bobC.post("/recipes/"+recipe+"/refs/"+main+"/merge", map[string]any{"into": draft.id, "version": draft.version}), 404, "not_found", "")
		// A primary line is never discarded, by the caller who made it either.
		refused(t, "a discard of the primary line", annC.post("/recipes/"+recipe+"/refs/"+main+"/discard", map[string]any{"version": versionOf(main)}), 409, "primary_merge_only", "")
		// A read is not a write of the change set.
		if tree := bobC.get("/recipes/" + recipe + "/refs/" + draft.id + "/tree").object(t); len(tree["part"].([]any)) != 3 {
			t.Errorf("bob's read: %v", tree)
		}
		if v := versionOf(draft.id); v != draft.version {
			t.Errorf("the change set moved to %d", v)
		}

		saved := adaC.put("/recipes/"+recipe+"/refs/"+draft.id+"/edits", map[string]any{"version": draft.version, "edits": edits}).object(t)
		if covers, _ := member(saved, "saved.cover").([]any); len(covers) != 1 {
			t.Errorf("ada's save: %v", saved)
		}
		committed := adaC.post("/recipes/"+recipe+"/refs/"+draft.id+"/commit", map[string]any{"version": number(t, member(saved, "ref.version")), "message": "a cover"}).object(t)
		draft.version = number(t, member(committed, "ref.version"))
		if by := id(t, member(committed, "commit.createdBy")); by != ada {
			t.Errorf("ada's commit was made by %s", by)
		}
	})

	t.Run("merge with a tag commits onto the primary line, and history, diff and materialize read it", func(t *testing.T) {
		merged := annC.post("/recipes/"+recipe+"/refs/"+draft.id+"/merge", map[string]any{"version": versionOf(main), "tag": true, "message": "first edition"}).object(t)
		if conflicts, ok := merged["conflicts"].([]any); !ok || len(conflicts) != 0 {
			t.Errorf("the merge's conflicts: %v", merged["conflicts"])
		}
		if id(t, member(merged, "ref.id")) != main || id(t, member(merged, "commit.ref")) != main || member(merged, "commit.message") != "first edition" || number(t, member(merged, "commit.sequence")) != 1 {
			t.Errorf("the merge: %v", merged)
		}
		tagged = id(t, member(merged, "commit.id"))

		history := annC.get("/recipes/" + recipe + "/refs/" + main + "/history").list(t)
		if len(history) != 1 || id(t, member(history[0], "id")) != tagged {
			t.Errorf("the primary line's history: %v", history)
		}
		if drafts := annC.get("/recipes/" + recipe + "/refs/" + draft.id + "/history").list(t); len(drafts) != 2 {
			t.Errorf("the change set's history: %v", drafts)
		}
		tree := annC.get("/recipes/" + recipe + "/commits/" + tagged).object(t)
		if got := headings(t, tree); !slices.Equal(got, []string{"Notes", "Intro", "Method"}) || len(tree["cover"].([]any)) != 1 {
			t.Errorf("the tagged tree: %v", tree)
		}
		changes := annC.get("/recipes/" + recipe + "/diff?from=" + firstCommit + "&to=" + tagged).list(t)
		if len(changes) != 1 || member(changes[0], "kind") != "cover" || member(changes[0], "operation") != "ADD" {
			t.Fatalf("the diff: %v", changes)
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(member(changes[0], "row").(string)), &row); err != nil || row["photo_url"] != "https://example.com/soup.jpg" {
			t.Errorf("the change's row: %v %v", member(changes[0], "row"), err)
		}
	})

	t.Run("release needs its own permission, a second release at the same version conflicts, and released reads the tree", func(t *testing.T) {
		refused(t, "released before a release", patC.get("/recipes/"+recipe+"/released"), 404, "not_found", "")
		// recipes.write does not release: the operation's permissions replace the class's.
		refused(t, "ann's release", annC.post("/recipes/"+recipe+"/release", map[string]any{"commit": tagged, "version": 0}), 403, "", "")
		refused(t, "an untagged release", patC.post("/recipes/"+recipe+"/release", map[string]any{"commit": firstCommit, "version": 0}), 409, "not_tagged", "")
		release := patC.post("/recipes/"+recipe+"/release", map[string]any{"commit": tagged, "version": 0}).object(t)
		if id(t, release["root"]) != recipe || id(t, release["commit"]) != tagged {
			t.Errorf("the release: %v", release)
		}
		refused(t, "a second release at 0", patC.post("/recipes/"+recipe+"/release", map[string]any{"commit": tagged, "version": 0}), 409, "version_conflict", "")

		released := annC.get("/recipes/" + recipe + "/released").object(t)
		if id(t, member(released, "release.commit")) != tagged || number(t, member(released, "release.version")) != number(t, release["version"]) {
			t.Errorf("released: %v", released)
		}
		if parts, _ := member(released, "tree.part").([]any); len(parts) != 3 {
			t.Errorf("the released tree: %v", released["tree"])
		}
		// The released table of parts holds the tree's rows (D139).
		root, _ := orm.ParseUserID(recipe)
		rows, _, err := db.GetPartReleasedRepository().FindMany(ctx, &orm.PartReleasedFilter{RecipeID: &orm.UUIDFilter{Eq: &root}}, &orm.PartReleasedFindOptions{Fields: orm.PartReleasedFields{Id: true}})
		if err != nil || len(rows) != 3 {
			t.Errorf("the released parts: %d %v", len(rows), err)
		}
	})

	t.Run("entity history lists the commits that changed an entity", func(t *testing.T) {
		history := annC.get("/recipes/" + recipe + "/entities/part/" + parts[0] + "/history").list(t)
		if len(history) != 1 || member(history[0], "operation") != "ADD" || id(t, member(history[0], "commit.id")) != tagged || number(t, member(history[0], "commit.sequence")) != 1 {
			t.Errorf("the part's history: %v", history)
		}
		refused(t, "a kind the graph has none of", annC.get("/recipes/"+recipe+"/entities/sauce/"+parts[0]+"/history"), 404, "not_found", "")
	})

	t.Run("a rebase that conflicts answers the conflicts and writes nothing, and a resolution settles it", func(t *testing.T) {
		first := annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "rename-intro"}).object(t)
		second := annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "rename-intro-too"}).object(t)
		firstID, secondID := id(t, first["id"]), id(t, second["id"])
		intro := partOf(t, annC.get("/recipes/"+recipe+"/refs/"+firstID+"/tree").object(t), parts[0])
		if intro["heading"] != "Intro" {
			t.Fatalf("the intro: %v", intro)
		}
		renamed := func(heading string) map[string]any {
			out := map[string]any{}
			for key, value := range intro {
				if value != nil {
					out[key] = value
				}
			}
			out["heading"] = heading
			return out
		}

		// The first change set renames the part and reaches the primary line.
		one := annC.put("/recipes/"+recipe+"/refs/"+firstID+"/edits", map[string]any{"version": number(t, first["version"]), "edits": map[string]any{"part": map[string]any{"upsert": []any{renamed("Introduction")}}}}).object(t)
		committed := annC.post("/recipes/"+recipe+"/refs/"+firstID+"/commit", map[string]any{"version": number(t, member(one, "ref.version"))}).object(t)
		if committed["commit"] == nil {
			t.Fatalf("the first change set's commit: %v", committed)
		}
		if merged := annC.post("/recipes/"+recipe+"/refs/"+firstID+"/merge", map[string]any{"version": versionOf(main)}).object(t); len(merged["conflicts"].([]any)) != 0 {
			t.Fatalf("the first merge: %v", merged)
		}

		// The second renames it another way, and its rebase finds the conflict.
		two := annC.put("/recipes/"+recipe+"/refs/"+secondID+"/edits", map[string]any{"version": number(t, second["version"]), "edits": map[string]any{"part": map[string]any{"upsert": []any{renamed("Preface")}}}}).object(t)
		twoVersion := number(t, member(two, "ref.version"))
		conflicted := annC.post("/recipes/"+recipe+"/refs/"+secondID+"/rebase", map[string]any{"version": twoVersion}).object(t)
		conflicts, _ := conflicted["conflicts"].([]any)
		if len(conflicts) != 1 {
			t.Fatalf("the rebase's conflicts: %v", conflicted)
		}
		conflict := conflicts[0].(map[string]any)
		text := func(name string) any {
			var value any
			if err := json.Unmarshal([]byte(conflict[name].(string)), &value); err != nil {
				t.Fatalf("the conflict's %s: %v", name, err)
			}
			return value
		}
		if conflict["kind"] != "part" || id(t, conflict["entityKey"]) != parts[0] || text("ours") != "Preface" || text("theirs") != "Introduction" || text("base") != "Intro" || conflicted["commit"] != nil {
			t.Errorf("the conflict: %v", conflicted)
		}
		// Nothing was written: the ref is at the version it was.
		if v := versionOf(secondID); v != twoVersion {
			t.Errorf("the conflicted rebase moved the ref to %d", v)
		}

		// A resolution that names no conflict of the merge is refused.
		refused(t, "an unmatched resolution", annC.post("/recipes/"+recipe+"/refs/"+secondID+"/rebase", map[string]any{
			"version":     twoVersion,
			"resolutions": []any{map[string]any{"kind": "part", "entityKey": parts[1], "path": conflict["path"], "take": "theirs"}},
		}), 422, "unmatched_resolution", "")

		settled := annC.post("/recipes/"+recipe+"/refs/"+secondID+"/rebase", map[string]any{
			"version":     twoVersion,
			"resolutions": []any{map[string]any{"kind": "part", "entityKey": parts[0], "path": conflict["path"], "take": "ours"}},
		}).object(t)
		if len(settled["conflicts"].([]any)) != 0 || settled["commit"] == nil {
			t.Fatalf("the settled rebase: %v", settled)
		}
		rebased.id, rebased.version = secondID, number(t, member(settled, "ref.version"))
		if heading := partOf(t, annC.get("/recipes/"+recipe+"/refs/"+secondID+"/tree").object(t), parts[0])["heading"]; heading != "Preface" {
			t.Errorf("the rebased heading: %v", heading)
		}
	})

	t.Run("revert composes a change set to an earlier commit's tree", func(t *testing.T) {
		reverted := annC.post("/recipes/"+recipe+"/refs/"+rebased.id+"/revert", map[string]any{"version": rebased.version, "toCommit": tagged}).object(t)
		if reverted["commit"] == nil {
			t.Fatalf("the revert: %v", reverted)
		}
		rebased.version = number(t, member(reverted, "ref.version"))
		if heading := partOf(t, annC.get("/recipes/"+recipe+"/refs/"+rebased.id+"/tree").object(t), parts[0])["heading"]; heading != "Intro" {
			t.Errorf("the reverted heading: %v", heading)
		}
	})

	t.Run("a sealed ref refuses a save, and discard frees the name", func(t *testing.T) {
		sealed := annC.post("/recipes/"+recipe+"/refs/"+rebased.id+"/seal", map[string]any{"version": rebased.version}).object(t)
		if member(sealed, "ref.sealed") != true {
			t.Fatalf("the seal: %v", sealed)
		}
		refused(t, "a save of a sealed ref", annC.put("/recipes/"+recipe+"/refs/"+rebased.id+"/edits", map[string]any{"version": number(t, member(sealed, "ref.version")), "edits": map[string]any{"note": map[string]any{"upsert": []any{map[string]any{"body": "late"}}}}}), 409, "ref_sealed", "")

		scratch := annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "scratch"}).object(t)
		if discarded := annC.post("/recipes/"+recipe+"/refs/"+id(t, scratch["id"])+"/discard", map[string]any{"version": number(t, scratch["version"])}).data(t); discarded != true {
			t.Errorf("the discard: %v", discarded)
		}
		refused(t, "a discarded ref's tree", annC.get("/recipes/"+recipe+"/refs/"+id(t, scratch["id"])+"/tree"), 404, "not_found", "")
		if again := annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "scratch"}).object(t); id(t, again["id"]) == id(t, scratch["id"]) {
			t.Errorf("the name was not freed: %v", again)
		}
	})

	t.Run("an input the core refuses is unprocessable", func(t *testing.T) {
		ref := annC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "reorder"}).object(t)
		refID, version := id(t, ref["id"]), number(t, ref["version"])
		refused(t, "keys that are not the children", annC.post("/recipes/"+recipe+"/refs/"+refID+"/arrange", map[string]any{"version": version, "kind": "part", "keys": []string{parts[0]}}), 422, "invalid_arrangement", "")
		refused(t, "a kind whose order is no key", annC.post("/recipes/"+recipe+"/refs/"+refID+"/arrange", map[string]any{"version": version, "kind": "step", "keys": []string{}}), 422, "order_not_a_key", "")
		refused(t, "a delete of an entity the ref does not hold", annC.put("/recipes/"+recipe+"/refs/"+refID+"/edits", map[string]any{"version": version, "edits": map[string]any{"part": map[string]any{"delete": []string{newID()}}}}), 422, "entity_not_found", "")
		if v := versionOf(refID); v != version {
			t.Errorf("a refused write moved the ref to %d", v)
		}
	})

	t.Run("a root, a ref or a commit that is not this root's is not found", func(t *testing.T) {
		nowhere := newID()
		for what, got := range map[string]reply{
			"branch":      annC.post("/recipes/"+nowhere+"/refs", map[string]any{"name": "draft"}),
			"compose":     annC.get("/recipes/" + nowhere + "/refs/" + draft.id + "/tree"),
			"released":    annC.get("/recipes/" + nowhere + "/released"),
			"materialize": annC.get("/recipes/" + nowhere + "/commits/" + tagged),
		} {
			refused(t, what+" of no root", got, 404, "not_found", "Recipe not found")
		}

		stew := newRecipe("Stew")
		theirs := annC.post("/recipes/"+stew+"/refs", map[string]any{"name": "draft"}).object(t)
		theirsID, theirsVersion := id(t, theirs["id"]), number(t, theirs["version"])
		for what, got := range map[string]reply{
			"compose":       annC.get("/recipes/" + recipe + "/refs/" + theirsID + "/tree"),
			"history":       annC.get("/recipes/" + recipe + "/refs/" + theirsID + "/history"),
			"save":          annC.put("/recipes/"+recipe+"/refs/"+theirsID+"/edits", map[string]any{"version": theirsVersion, "edits": map[string]any{}}),
			"branch":        annC.post("/recipes/"+recipe+"/refs", map[string]any{"from": theirsID, "name": "crossed"}),
			"merge":         annC.post("/recipes/"+recipe+"/refs/"+draft.id+"/merge", map[string]any{"into": theirsID, "version": theirsVersion}),
			"compose of no": annC.get("/recipes/" + recipe + "/refs/" + nowhere + "/tree"),
		} {
			refused(t, what+" of another root's ref", got, 404, "not_found", "ref not found")
		}
		// A commit of another root is not found where the path or the body
		// names it, so a caller learns nothing of another root's commits.
		for what, got := range map[string]reply{
			"materialize":  annC.get("/recipes/" + stew + "/commits/" + tagged),
			"diff":         annC.get("/recipes/" + stew + "/diff?from=" + tagged + "&to=" + tagged),
			"diff to none": annC.get("/recipes/" + recipe + "/diff?from=" + tagged + "&to=" + nowhere),
			"release":      patC.post("/recipes/"+stew+"/release", map[string]any{"commit": tagged, "version": 0}),
			"revert":       annC.post("/recipes/"+stew+"/refs/"+theirsID+"/revert", map[string]any{"version": theirsVersion, "toCommit": tagged}),
		} {
			refused(t, what+" of another root's commit", got, 404, "not_found", "commit not found")
		}
	})

	t.Run("a caller without the route's permission is refused by the API's auth, and a write needs a caller who is a UUID", func(t *testing.T) {
		refused(t, "lee's branch", leeC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "mine"}), 403, "", "")
		refused(t, "lee's read", leeC.get("/recipes/"+recipe+"/refs/"+draft.id+"/tree"), 403, "", "")
		refused(t, "no caller's read", nobody.get("/recipes/"+recipe+"/refs/"+draft.id+"/tree"), 401, "", "")
		refused(t, "sam's branch", samC.post("/recipes/"+recipe+"/refs", map[string]any{"name": "mine"}), 403, "no_actor", "")
		if tree := samC.get("/recipes/" + recipe + "/refs/" + draft.id + "/tree").object(t); len(tree["part"].([]any)) != 3 {
			t.Errorf("sam's read: %v", tree)
		}
	})
}
`
