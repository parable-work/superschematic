package servergen_test

import (
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/envgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/sqlgen"
	"github.com/parable-work/superschematic/internal/generator/tsrestgen"
	"github.com/parable-work/superschematic/internal/loader/idempotency"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// idempotentFixture is the shop fixture with the reserved class of the
// idempotency keys in shop-db, as the loader expands it, and an
// @idempotent POST in shop-reviews, addReview, whose keys live an hour
// (D140).
func idempotentFixture(t *testing.T) fixture {
	t.Helper()
	f := loadFixture(t, servicesRoot)
	db := f.schemas["shop-db"]
	db.Types["IdempotencyKey"] = &ir.TypeDef{Name: "IdempotencyKey", Role: ir.RoleDBTable, IdempotencyKeys: &ir.IdempotencyKeysTrait{}}
	if _, err := idempotency.Expand(db); err != nil {
		t.Fatal(err)
	}
	reviews := f.schemas["shop-reviews"]
	reviews.OperationSets = append(reviews.OperationSets, &ir.OperationSet{
		Name: "ReviewMutations",
		Operations: []*ir.FieldDef{{
			Name:       "addReview",
			TypeRef:    ir.TypeRef{Name: "ReviewView"},
			Required:   true,
			HTTPMethod: "POST",
			RestPath:   "products/{productId}/reviews",
			Arguments: []*ir.ArgumentDef{
				{Name: "productId", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
				{Name: "body", TypeRef: ir.TypeRef{Name: "string"}, Required: true},
			},
			Idempotent: &ir.IdempotentConfig{TTLSeconds: 3600},
		}},
	})
	return f
}

// TestGoIdempotencyStoreWiring: Storefront serves shop-reviews, whose
// addReview is @idempotent, so its main.go opens a database/sql handle
// over shop-db's pool and hands shop-reviews' Config a store on the table
// the API package names; shop-orders, with no @idempotent operation, gets
// none, and neither does the fixture as it stands (D140).
func TestGoIdempotencyStoreWiring(t *testing.T) {
	read := func(repoRoot string, parts ...string) string {
		data, err := os.ReadFile(filepath.Join(append([]string{repoRoot, "schemas", "dist"}, parts...)...))
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}

	plain := t.TempDir()
	f := loadFixture(t, servicesRoot)
	f.build(t, plain, fakePaths(plain))
	if main := read(plain, "server", "shop-stack", "Storefront", servergen.MainFile); strings.Contains(main, "idempotency") || strings.Contains(main, "stdlib") {
		t.Errorf("a server with no @idempotent operation wires a store:\n%s", main)
	}

	keyed := t.TempDir()
	f = idempotentFixture(t)
	f.build(t, keyed, fakePaths(keyed))
	main := read(keyed, "server", "shop-stack", "Storefront", servergen.MainFile)
	for _, want := range []string{
		"\t\"github.com/jackc/pgx/v5/stdlib\"\n",
		"/idempotency\"\n",
		"\tshopDbSQL := stdlib.OpenDBFromPool(shopDbPool)\n\tdefer shopDbSQL.Close()\n",
		"IdempotencyStore: idempotency.NewSQLStore(shopDbSQL, idempotency.Postgres, shopreviews.IdempotencyTable),\n",
	} {
		if !strings.Contains(main, want) {
			t.Errorf("Storefront's main.go lacks %q:\n%s", want, main)
		}
	}
	if got := strings.Count(main, "IdempotencyStore:"); got != 1 {
		t.Errorf("Storefront hands %d APIs an idempotency store, want shop-reviews alone:\n%s", got, main)
	}
	if routes := read(keyed, "api", "shop-reviews", "routes.go"); !strings.Contains(routes, "Operation: \"reviewMutations.addReview\"") && !strings.Contains(routes, "Operation: \"review.addReview\"") {
		t.Errorf("shop-reviews' routes mount no idempotency step for addReview:\n%s", routes)
	}
	// The job and the worker of shop-orders serve no request and keep no key.
	for _, background := range []string{expireOrders, fulfilOrders} {
		if entry := read(keyed, "server", "shop-stack", background, servergen.MainFile); strings.Contains(entry, "idempotency") {
			t.Errorf("%s wires an idempotency store:\n%s", background, entry)
		}
	}
}

// idempotentReviews is shop-reviews' implementation for
// TestGoIdempotentServerRunsAnOperationOnce: addReview numbers the reviews
// it adds, so a second run of one request would answer another number.
const idempotentReviewsAdd = `	impl.added++
	if body == "refuse" {
		return nil, api.ConflictError("No more reviews", nil)
	}
	return &types.ReviewView{Id: fmt.Sprintf("r-%d", impl.added), Body: body}, nil
`

// TestGoIdempotentServerRunsAnOperationOnce builds Storefront from the
// fixture with an @idempotent addReview and runs it against Postgres: a
// repeat of a request under its Idempotency-Key is answered the first
// answer, marked a replay, and the operation runs once; the key with
// another body is refused 422; a request with no key runs each time; and
// the rows are in the table shop-db declares (D140).
func TestGoIdempotentServerRunsAnOperationOnce(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		t.Skipf("set %s to run a Go server with @idempotent routes against Postgres", identityDatabaseEnv)
	}
	paths := testpaths.Local(t)
	repoRoot := t.TempDir()
	f := idempotentFixture(t)
	f.build(t, repoRoot, paths)
	out := filepath.Join(repoRoot, "schemas", "dist")

	// The implementation: the scaffold's, with addReview filled in.
	impl := filepath.Join(naming.Default().GoImplementationDir(repoRoot, "shop-reviews"), "implementation.go")
	raw, err := os.ReadFile(impl)
	if err != nil {
		t.Fatal(err)
	}
	source := string(raw)
	at := strings.Index(source, ") AddReview(")
	if at < 0 {
		t.Fatalf("the scaffold has no AddReview:\n%s", source)
	}
	open := at + strings.Index(source[at:], "{\n") + 2
	end := open + strings.Index(source[open:], "\n}\n") + 1
	receiver := source[strings.LastIndex(source[:at], "func (")+len("func (") : at]
	name := strings.TrimPrefix(strings.Fields(receiver)[1], "*")
	source = source[:open] + idempotentReviewsAdd + source[end:]
	// The struct counts its runs.
	decl := "type " + name + " struct {\n"
	if !strings.Contains(source, decl) || strings.Fields(receiver)[0] != "impl" {
		t.Fatalf("the scaffold declares no %s with receiver impl:\n%s", decl, source)
	}
	source = strings.Replace(source, decl, decl+"\tadded int\n", 1)
	if !strings.Contains(source, "\t\"fmt\"\n") {
		source = strings.Replace(source, "import (\n", "import (\n\t\"fmt\"\n", 1)
	}
	if err := os.WriteFile(impl, []byte(source), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := servergen.ServerDir(out, "shop-stack", "Storefront")
	goCommand(t, dir, "mod", "tidy")
	goCommand(t, dir, "vet", ".")
	binary := filepath.Join(t.TempDir(), "Storefront")
	goCommand(t, dir, "build", "-o", binary, ".")

	// The table, as the SQL generator writes shop-db's on Postgres.
	model, err := sqlmigrate.BuildModel(f.schemas["shop-db"], sqlgen.Options{SchemaName: "shop-db"}, sqlmigrate.Postgres)
	if err != nil {
		t.Fatal(err)
	}
	ddl, err := sqlmigrate.CreateSQL(model)
	if err != nil {
		t.Fatal(err)
	}
	createSQL := filepath.Join(t.TempDir(), "create.sql")
	if err := os.WriteFile(createSQL, []byte(ddl), 0o644); err != nil {
		t.Fatal(err)
	}
	databaseURL := identitySchema(t, dir, postgres, createSQL)

	env := append(append([]string{"SHOP_DB_DATABASE_URL=" + databaseURL}, edgeVariables(t, "SHOP_API_SERVICE")...), bucketVariables(t, "SHOP_MEDIA_BUCKET", "http://127.0.0.1:9")...)
	server := start(t, binary, append(env, memoryStoreVariables(t, "SHOP_CACHE_MEMORY_STORE", "redis://127.0.0.1:9/0")...)...)

	add := func(body, key string) (int, string, string) {
		t.Helper()
		request, err := http.NewRequest(http.MethodPost, server.base+"/api/products/p-1/reviews", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		if key != "" {
			request.Header.Set("Idempotency-Key", key)
		}
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		answer, _ := io.ReadAll(response.Body)
		return response.StatusCode, response.Header.Get("Idempotent-Replay"), string(answer)
	}
	good := `{"body":"good tea"}`

	status, replay, first := add(good, "review-1")
	if status != http.StatusOK || replay != "" || !strings.Contains(first, `"id":"r-1"`) {
		t.Fatalf("the first review: %d, replay %q, %s", status, replay, first)
	}
	status, replay, again := add(good, "review-1")
	if status != http.StatusOK || replay != "true" || again != first {
		t.Errorf("the repeat: %d, replay %q, %s, want the first answer %s", status, replay, again, first)
	}
	if status, _, problem := add(`{"body":"bad tea"}`, "review-1"); status != http.StatusUnprocessableEntity || !strings.Contains(problem, "idempotency_key_reused") {
		t.Errorf("the key with another body: %d %s", status, problem)
	}
	// Another key, and no key, run the operation again: r-2, then r-3.
	if status, replay, other := add(good, "review-2"); status != http.StatusOK || replay != "" || !strings.Contains(other, `"id":"r-2"`) {
		t.Errorf("another key: %d, replay %q, %s", status, replay, other)
	}
	if status, _, none := add(good, ""); status != http.StatusOK || !strings.Contains(none, `"id":"r-3"`) {
		t.Errorf("no key: %d %s", status, none)
	}
	// A refusal is an answer too, kept and replayed: the operation ran
	// four times, so the next review is r-5.
	for i, want := range []string{"", "true"} {
		if status, replay, problem := add(`{"body":"refuse"}`, "review-3"); status != http.StatusConflict || replay != want || !strings.Contains(problem, "No more reviews") {
			t.Errorf("a refused review, request %d: %d, replay %q, %s", i, status, replay, problem)
		}
	}
	if status, _, next := add(good, "review-4"); status != http.StatusOK || !strings.Contains(next, `"id":"r-5"`) {
		t.Errorf("the review after the refusal: %d %s", status, next)
	}
	// A key over 255 characters is refused before anything runs.
	if status, _, problem := add(good, strings.Repeat("k", 256)); status != http.StatusBadRequest || !strings.Contains(problem, "idempotency_key_invalid") {
		t.Errorf("a key too long: %d %s", status, problem)
	}
	server.stop(t)
}

// TestTypeScriptIdempotencyStoreWiring: a TypeScript server's entrypoint,
// on Node.js and on Workers, over Postgres and over SQLite, hands the
// router of an API with @idempotent operations the store of their keys
// over its database, on the table the API package names, and imports the
// store's constructor from the HTTP runtime for it alone (D140).
func TestTypeScriptIdempotencyStoreWiring(t *testing.T) {
	table := &ir.IdempotencyTable{Table: "idempotency_key"}
	for _, workers := range []bool{false, true} {
		for _, sqlite := range []bool{false, true} {
			for _, idempotent := range []bool{false, true} {
				output := &tsrestgen.APIOutput{
					SchemaName:   "shop-api",
					PackageName:  "@schemas/shop-api-api",
					HasEnvConfig: true,
					Endpoints:    []tsrestgen.EndpointInfo{{Name: "placeOrder"}},
					Deps:         tsrestgen.DepsInfo{Database: "shop-db"},
				}
				if sqlite {
					output.Deps.Dialect = tsrestgen.SQLiteDialect
				}
				if idempotent {
					output.IdempotencyTable = table
					output.Endpoints[0].Idempotent = &ir.IdempotentConfig{}
				}
				in := servergen.TypeScriptInput{
					Stack:   "edge-stack",
					Server:  "shop-api",
					Dir:     t.TempDir(),
					Workers: workers,
					APIs: []servergen.TypeScriptAPIInput{{
						Output: output,
						Routes: &apigen.APIOutput{SchemaName: "shop-api"},
						Config: &envgen.ConfigOutput{
							SchemaName: "shop-api",
							Derived:    []envgen.DerivedField{{Key: "SHOP_DB_DATABASE_URL", Kind: ir.EdgeSQL, Service: "shop-db"}},
						},
						Implementation: servergen.TypeScriptImplementation{Package: "@schemas/shop-api-implementation"},
					}},
				}
				s, err := servergen.PlanTypeScript(in)
				if err != nil {
					t.Fatal(err)
				}
				if err := servergen.WriteTypeScript(s, in.Dir); err != nil {
					t.Fatal(err)
				}
				file := servergen.TypeScriptMainFile
				if workers {
					file = servergen.TypeScriptFetchFile
				}
				raw, err := os.ReadFile(filepath.Join(in.Dir, file))
				if err != nil {
					t.Fatal(err)
				}
				entry := string(raw)
				constructor, other, database := "postgresIdempotencyStore", "sqliteIdempotencyStore", "shopDbPool"
				if sqlite {
					constructor, other, database = other, constructor, "shopDbDatabase"
				}
				wired := strings.Contains(entry, "idempotencyStore: "+constructor+"("+database+", shopApiApi.idempotencyTable)")
				imported := strings.Contains(entry, constructor+", ") || strings.Contains(entry, ", "+constructor+" }")
				if wired != idempotent || imported != idempotent || strings.Contains(entry, other) {
					t.Errorf("workers %t, sqlite %t, idempotent %t: wired %t, imported %t:\n%s", workers, sqlite, idempotent, wired, imported, entry)
				}
			}
		}
	}
}
