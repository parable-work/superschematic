package generator

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
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

// poolCases are the cases poolTest runs, by name: the end-to-end scenario
// every language's server and behavior worker of Pool on a table is held
// to (D104), with the core's words. A person's accounts at two vendors and
// a paid key, among which pick, an operation of the table as a whole,
// answers the open account lowest by rank, a vendor's open accounts first
// when prefer names it, then its near ones; an account a provider refuses
// is out until the time it names, thirty minutes when it names none, and
// comes back only when a check with room made after that time says so,
// which the worker's recheck asks the project's handler for; and an account
// whose Budget meter has no room counts as out until the meter has room
// again. The TypeScript test runs one case more, of the MCP tool.
var poolCases = []string{
	"a pick answers the open account lowest by rank among an owner's accounts",
	"a null member of match or prefer is absent, so a pick keeps an owner's accounts whatever their rank",
	"prefer tries a vendor's open accounts, then its near ones, then the others by rank",
	"markOut puts an account out for thirty minutes and arms its recheck at outUntil",
	"markOut keeps the later until, and a repeat answers unchanged",
	"a check with room before outUntil leaves the account out",
	"with every account out, pick answers no member and the soonest outUntil",
	"a due recheck hands the handler its event once, whose check with room opens the account",
	"a recheck whose check finds no room puts the account out until resetsAt and arms the recheck there",
	"an account whose Budget meter has no room counts as out until the provider's figure lapses",
	"markOut and reportRoom without accounts.pool are forbidden",
	"reportRoom of a key no row holds is not found",
}

// TestPoolGoServerOnPostgres builds fixture-pool-db and fixture-pool-api as
// build-all does, then writes poolTest into the generated API module and
// runs it, after go vet: the generated Go server's Pool and Budget
// operations of Account (D104), pick among them a route of the table as a
// whole, which names no key and runs as a command without a row, called
// through the generated Go SDK with a test authenticator in front of the
// routes; and the two queues of the generated behavior worker
// AccountRechecks, its deadlines and events, run in process with
// worker.RunAll, as its main runs them, with the test's own Workers, whose
// AccountRechecksPoolRecheck is the project's handler of the event recheck:
// it asks a fake provider and reports what it says with reportRoom. A
// recheck is made due by moving its message's due_at to now, as the
// Schedules test makes one, and its account's outUntil and the message's
// time to a moment past, so the handler's check is made after it, on the
// Postgres behaviorServerDatabaseEnv names. Each caller's subject, and each
// case's owner of its accounts, is a fresh UUID in its base62 wire form, as
// the identity runtime gives a principal's. It then plans the worker's
// entrypoint over the generated modules and an implementation the API's
// scaffold writes, and vets it. The module links the core's static archive,
// which scripts/rules-archive.sh stages; the test skips without it, without
// the database, and in -short mode.
func TestPoolGoServerOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the generated Go server and worker of Pool in -short mode")
	}
	if os.Getenv(behaviorServerDatabaseEnv) == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the generated Go server and worker of Pool against Postgres")
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.RulesGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_rules.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the behaviors' core's archive is not staged at %s: run scripts/rules-archive.sh (make rules-archive)", archive)
	}
	outputRoot := t.TempDir()
	clock := codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC))
	loadDependency := func(name string) (*ir.Schema, error) {
		return loader.LoadService(filepath.Join(tsFixtures, name))
	}
	var apiSchema, dbSchema *ir.Schema
	for _, service := range []string{"fixture-pool-db", "fixture-pool-api"} {
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
		if service == "fixture-pool-api" {
			apiSchema = schema
		} else {
			dbSchema = schema
		}
	}

	apiDir := APIDir(outputRoot, "fixture-pool-api")
	if err := os.MkdirAll(filepath.Join(apiDir, "testdata"), 0o755); err != nil {
		t.Fatal(err)
	}
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, "fixture-pool-db"), "create.sql"), filepath.Join(apiDir, "testdata", "create.sql"))
	if err := os.WriteFile(filepath.Join(apiDir, "behavior_pool_server_test.go"), []byte(poolTest), 0o644); err != nil {
		t.Fatal(err)
	}
	sdkModule := naming.Default().GoSDKModule("fixture-pool-api")
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	var out []byte
	for _, args := range [][]string{
		{"mod", "edit", "-require=" + sdkModule + "@v0.0.0-00010101000000-000000000000", "-replace=" + sdkModule + "=../../sdk/go/fixture-pool-api"},
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
	for _, name := range poolCases {
		if want := "--- PASS: TestPool/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(string(out), want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}

	vetEventWorkerEntrypoint(t, outputRoot, paths, apiSchema, dbSchema, env, "pool-stack", "AccountRechecks")
}

// poolTest runs in the generated module of fixture-pool-api. It serves the
// generated routes over the ORM of fixture-pool-db, on a schema of its own
// in the Postgres behaviorServerDatabaseEnv names, with a test
// authenticator in front of them, calls them through the generated Go SDK,
// creates accounts through the ORM, runs the generated worker's queues in
// process with the test's own Workers, and reads the table and the queues
// by SQL. The SDK's inputs and results, and the ORM's parameters, are read
// through their JSON, whose shape the declarations give.
const poolTest = `package fixturepoolapi_test

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-pool-api"
	orm "example.com/schemas/orm/fixture-pool-db"
	sdk "example.com/schemas/sdk/go/fixture-pool-api"
	types "example.com/schemas/types/go/fixture-pool-api"
	dbtypes "example.com/schemas/types/go/fixture-pool-db"
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

// openDatabase is the ORM of fixture-pool-db over a schema of its own,
// holding the build's create.sql, which it drops when the test ends, and
// its pool, which the test reads the tables through.
func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("` + behaviorServerDatabaseEnv + `")
	if url == "" {
		t.Skip("set ` + behaviorServerDatabaseEnv + ` to run the generated Go server and worker of Pool against Postgres")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("pool_go_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
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
	return db, pool
}

// subject is a fresh UUID in its base62 wire form, as the identity runtime
// gives a principal's subject.
func subject(t *testing.T) string {
	t.Helper()
	var b [16]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	id, err := dbtypes.ParseIdentityUUID(fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16]))
	if err != nil {
		t.Fatal(err)
	}
	return id.String()
}

// uuidOf is a subject as the ORM's UUID.
func uuidOf(t *testing.T, subject string) dbtypes.IdentityUUID {
	t.Helper()
	id, err := dbtypes.ParseIdentityUUID(subject)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// convert is a value through its JSON as another type: a test's JSON-shaped
// input as the generated type a call takes, or a result as the shape a case
// reads, whatever the generated type names it holds.
func convert[T any](value any) (T, error) {
	var out T
	data, err := json.Marshal(value)
	if err != nil {
		return out, err
	}
	err = json.Unmarshal(data, &out)
	return out, err
}

// as is convert, failing the case on an error.
func as[T any](t *testing.T, value any) T {
	t.Helper()
	out, err := convert[T](value)
	if err != nil {
		t.Fatalf("%+v: %v", value, err)
	}
	return out
}

// sortedJSON is a value's JSON with its keys sorted, as a result is shown.
func sortedJSON(t *testing.T, value any) string {
	t.Helper()
	out, err := json.Marshal(as[any](t, value))
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

// wantAPIRefused holds a call the server refused whole: its status, its
// code and its message.
func wantAPIRefused(t *testing.T, what string, err error, status int, code, message string) {
	t.Helper()
	var apiErr *sdk.APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("%s: %v, want the API's refusal", what, err)
	}
	if apiErr.StatusCode != status || apiErr.Code != code || apiErr.Message != message {
		t.Fatalf("%s refused with %d %s %q\nwant %d %s %q", what, apiErr.StatusCode, apiErr.Code, apiErr.Message, status, code, message)
	}
}

// member is the member pick answers.
type member struct {
	Key          string   ` + "`json:\"key\"`" + `
	Availability string   ` + "`json:\"availability\"`" + `
	Order        *float64 ` + "`json:\"order\"`" + `
	Preferred    bool     ` + "`json:\"preferred\"`" + `
}

// picked is what pick answers: the member, or none and when one may be
// back.
type picked struct {
	Member *member ` + "`json:\"member\"`" + `
	Until  *int64  ` + "`json:\"until\"`" + `
}

// state is what markOut and reportRoom answer of a member, its absent
// fields nil.
type state struct {
	Availability string  ` + "`json:\"availability\"`" + `
	OutUntil     *int64  ` + "`json:\"outUntil\"`" + `
	OutReason    *string ` + "`json:\"outReason\"`" + `
	CheckedAt    *int64  ` + "`json:\"checkedAt\"`" + `
	Opened       *bool   ` + "`json:\"opened\"`" + `
	Unchanged    *bool   ` + "`json:\"unchanged\"`" + `
}

// recheck is a recheck event's payload.
type recheck struct {
	OutUntil  int64   ` + "`json:\"outUntil\"`" + `
	OutReason *string ` + "`json:\"outReason\"`" + `
	CheckedAt *int64  ` + "`json:\"checkedAt\"`" + `
	DueAt     int64   ` + "`json:\"dueAt\"`" + `
	FiredAt   int64   ` + "`json:\"firedAt\"`" + `
}

// answer is what the fake provider says of an account: whether it has
// room, whether it is near its limit, and when its room is back.
type answer struct {
	Room     bool   ` + "`json:\"room\"`" + `
	Near     bool   ` + "`json:\"near,omitempty\"`" + `
	ResetsAt *int64 ` + "`json:\"resetsAt,omitempty\"`" + `
}

// received is an event the project's handler got: the account's key, the
// payload, and what the check it reported answered.
type received struct {
	key      string
	payload  recheck
	reported state
}

// rechecks is the project's Workers in the test (D104): its handler of
// Account's Pool's event recheck records each event it gets, asks the fake
// provider for the account, room when the test set no answer, and reports
// what it says with reportRoom through the event's transaction as the
// worker. It is idempotent by the account's key and the recheck's dueAt: an
// event of a pair it handled reports nothing again.
type rechecks struct {
	mu       sync.Mutex
	got      []received
	handled  map[string]bool
	provider map[string]answer
}

var _ api.Workers = (*rechecks)(nil)

func (w *rechecks) AccountRechecksPoolRecheck(ctx context.Context, tx orm.TxInterface, event types.AccountPoolRecheckEvent) error {
	key := event.Key.String()
	payload, err := convert[recheck](event.Payload)
	if err != nil {
		return err
	}
	w.mu.Lock()
	pair := fmt.Sprintf("%s/%d", key, payload.DueAt)
	if w.handled[pair] {
		w.mu.Unlock()
		return nil
	}
	said, ok := w.provider[key]
	if !ok {
		said = answer{Room: true}
	}
	w.mu.Unlock()
	id, err := dbtypes.ParseIdentityUUID(key)
	if err != nil {
		return err
	}
	params, err := convert[orm.AccountPoolReportRoomParams](said)
	if err != nil {
		return err
	}
	result, err := tx.GetAccountRepository().Pool().ReportRoom(ctx, id, params)
	if err != nil {
		return err
	}
	reported, err := convert[state](result)
	if err != nil {
		return err
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	w.handled[pair] = true
	w.got = append(w.got, received{key: key, payload: payload, reported: reported})
	return nil
}

// of are the events the handler reported on of an account, in the order
// it got them.
func (w *rechecks) of(key string) []received {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []received
	for _, r := range w.got {
		if r.key == key {
			out = append(out, r)
		}
	}
	return out
}

// say sets what the fake provider says of an account.
func (w *rechecks) say(key string, said answer) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.provider[key] = said
}

func TestPool(t *testing.T) {
	ctx := context.Background()
	db, pool := openDatabase(t)
	router := chi.NewRouter()
	router.Use(chimiddleware.RequestID)
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{DB: db, Logger: zap.NewNop()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	client := func(token string) *sdk.FixturePoolApiSDK {
		t.Helper()
		c, err := sdk.New(sdk.SDKConfig{BaseURL: server.URL})
		if err != nil {
			t.Fatal(err)
		}
		c.SetToken(token)
		return c
	}
	// ada holds accounts.pool and accounts.observe; guest holds neither.
	ada, guest := subject(t), subject(t)
	asAda, asGuest := client(ada+":accounts.pool,accounts.observe"), client(guest)
	workers := &rechecks{handled: map[string]bool{}, provider: map[string]answer{}}
	now := func() int64 { return time.Now().UnixMilli() }

	// account creates an account of an owner through the repository, with
	// the key, in its wire form, that key names when given, and returns its
	// key in its wire form and in its column's.
	account := func(t *testing.T, owner, name, vendor, kind string, rank int64, key ...string) (string, string) {
		t.Helper()
		create := &orm.AccountCreate{
			Owner: uuidOf(t, owner), Name: name, Vendor: vendor, Kind: dbtypes.AccountKind(kind), Rank: dbtypes.GenericInt64(rank),
		}
		if len(key) > 0 {
			id := uuidOf(t, key[0])
			create.Id = &id
		}
		row, err := db.GetAccountRepository().CreateOne(ctx, create)
		if err != nil {
			t.Fatalf("create the account %s: %v", name, err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	version := func(t *testing.T, column string) int64 {
		t.Helper()
		var v int64
		if err := pool.QueryRow(ctx, "SELECT _version FROM account WHERE id = $1", column).Scan(&v); err != nil {
			t.Fatal(err)
		}
		return v
	}
	unmoved := func(t *testing.T, what, column string, want int64) {
		t.Helper()
		if got := version(t, column); got != want {
			t.Fatalf("the account is at version %d after %s, want %d: it writes nothing", got, what, want)
		}
	}
	// row is an account's Pool fields as its columns hold them, each
	// instant in epoch milliseconds, 0 for none.
	type row struct {
		availability        string
		outUntil, checkedAt int64
	}
	read := func(t *testing.T, column string) row {
		t.Helper()
		var availability *string
		var outUntil, checkedAt *time.Time
		if err := pool.QueryRow(ctx, "SELECT pool_availability, pool_out_until, pool_checked_at FROM account WHERE id = $1", column).Scan(&availability, &outUntil, &checkedAt); err != nil {
			t.Fatal(err)
		}
		out := row{}
		if availability != nil {
			out.availability = *availability
		}
		if outUntil != nil {
			out.outUntil = outUntil.UnixMilli()
		}
		if checkedAt != nil {
			out.checkedAt = checkedAt.UnixMilli()
		}
		return out
	}
	pick := func(t *testing.T, c *sdk.FixturePoolApiSDK, input map[string]any) picked {
		t.Helper()
		result, err := c.AccountNamespace.Pool.Pick(ctx, as[types.AccountPoolPickInput](t, input))
		if err != nil {
			t.Fatalf("pick %s: %v", sortedJSON(t, input), err)
		}
		return as[picked](t, result)
	}
	// wantMember holds pick's answer to one member of a key, availability,
	// order and preferred.
	wantMember := func(t *testing.T, got picked, key, availability string, order float64, preferred bool) {
		t.Helper()
		m := got.Member
		if m == nil || m.Key != key || m.Availability != availability || m.Order == nil || *m.Order != order || m.Preferred != preferred || got.Until != nil {
			t.Fatalf("pick answered %s, want %s, %s, order %v, preferred %t", sortedJSON(t, got), key, availability, order, preferred)
		}
	}
	markOut := func(c *sdk.FixturePoolApiSDK, key string, input map[string]any) (state, error) {
		result, err := c.AccountNamespace.Pool.MarkOut(ctx, key, as[types.AccountPoolMarkOutInput](t, input))
		if err != nil {
			return state{}, err
		}
		return as[state](t, result), nil
	}
	mustMarkOut := func(t *testing.T, key string, input map[string]any) state {
		t.Helper()
		s, err := markOut(asAda, key, input)
		if err != nil {
			t.Fatalf("markOut %s: %v", sortedJSON(t, input), err)
		}
		return s
	}
	reportRoom := func(c *sdk.FixturePoolApiSDK, key string, input map[string]any) (state, error) {
		result, err := c.AccountNamespace.Pool.ReportRoom(ctx, key, as[types.AccountPoolReportRoomInput](t, input))
		if err != nil {
			return state{}, err
		}
		return as[state](t, result), nil
	}
	// armed is the live recheck message of an account: its at and due_at in
	// epoch milliseconds; false when there is none.
	armed := func(t *testing.T, column string) (int64, int64, bool) {
		t.Helper()
		rows, err := pool.Query(ctx, "SELECT at, due_at FROM account_deadline_queue WHERE row_key = $1 AND use_id = 'Pool' AND name = 'recheck' AND state IN ('ready', 'claimed')", column)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var at int64
		var due time.Time
		found := 0
		for rows.Next() {
			if err := rows.Scan(&at, &due); err != nil {
				t.Fatal(err)
			}
			found++
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		if found > 1 {
			t.Fatalf("account_deadline_queue holds %d live recheck messages of the account, want at most one", found)
		}
		return at, due.UnixMilli(), found == 1
	}
	// overdue makes an account's recheck due now, and the account's outUntil
	// and the message's time and payload's dueAt a moment past, so the
	// handler's check is made after its outUntil: a raw write of the owned
	// column and of the queue, which D103 keeps open. It returns the new
	// outUntil.
	overdue := func(t *testing.T, column string) int64 {
		t.Helper()
		past := now() - 1000
		if _, err := pool.Exec(ctx, "UPDATE account SET pool_out_until = $2 WHERE id = $1", column, time.UnixMilli(past)); err != nil {
			t.Fatal(err)
		}
		tag, err := pool.Exec(ctx, "UPDATE account_deadline_queue SET at = $2, payload = jsonb_build_object('dueAt', $2::bigint), due_at = now() WHERE row_key = $1 AND use_id = 'Pool' AND name = 'recheck' AND state = 'ready'", column, past)
		if err != nil {
			t.Fatal(err)
		}
		if tag.RowsAffected() != 1 {
			t.Fatalf("account_deadline_queue holds %d ready recheck messages of the account, want one", tag.RowsAffected())
		}
		return past
	}
	// events counts an account's messages of the event queue.
	events := func(t *testing.T, column string) int {
		t.Helper()
		var n int
		if err := pool.QueryRow(ctx, "SELECT count(*) FROM account_event_queue WHERE row_key = $1", column).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	// drive runs the worker's queues in process, as its main does, with the
	// test's Workers, until done holds or ten seconds pass, polling every
	// 100 ms.
	drive := func(t *testing.T, done func() bool) {
		t.Helper()
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, api.AccountRechecksQueues(db, workers, zap.NewNop()), worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond})
		}()
		deadline := time.Now().Add(10 * time.Second)
		for !done() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		if err := <-stopped; err != nil {
			t.Fatalf("the worker: %v", err)
		}
	}
	// fired drives the worker until the handler reported on one event of an
	// account and the account's event queue is empty.
	fired := func(t *testing.T, key, column string) received {
		t.Helper()
		drive(t, func() bool { return len(workers.of(key)) >= 1 && events(t, column) == 0 })
		got := workers.of(key)
		if len(got) != 1 {
			t.Fatalf("the handler reported on %d events of the account, want one: %+v", len(got), got)
		}
		if left := events(t, column); left != 0 {
			t.Fatalf("the account's event queue holds %d messages after its event ran, want none", left)
		}
		return got[0]
	}

	t.Run("a pick answers the open account lowest by rank among an owner's accounts", func(t *testing.T) {
		owner := subject(t)
		// a1 takes the greatest of three keys and k1 the least, so a pick
		// that went by key rather than by rank would answer k1.
		keys := []string{subject(t), subject(t), subject(t)}
		slices.SortFunc(keys, func(a, b string) int {
			return strings.Compare(uuidOf(t, a).ToUUID().String(), uuidOf(t, b).ToUUID().String())
		})
		a1, _ := account(t, owner, "a1", "anthropic", "account", 1, keys[2])
		account(t, owner, "a2", "anthropic", "account", 2, keys[1])
		account(t, owner, "k1", "anthropic", "key", 9, keys[0])
		if a1 != keys[2] {
			t.Fatalf("a1's key is %s, want %s", a1, keys[2])
		}
		wantMember(t, pick(t, asAda, map[string]any{"match": map[string]any{"owner": owner}}), a1, "open", 1, true)
	})

	t.Run("a null member of match or prefer is absent, so a pick keeps an owner's accounts whatever their rank", func(t *testing.T) {
		owner := subject(t)
		a1, _ := account(t, owner, "a1", "anthropic", "account", 1)
		account(t, owner, "o1", "openai", "account", 2)
		// The body as any HTTP client may send it: the SDK's types take no
		// null.
		body := fmt.Sprintf("{\"match\": {\"owner\": %q, \"rank\": null}, \"prefer\": {\"vendor\": null}}", owner)
		req, err := http.NewRequestWithContext(ctx, http.MethodPost, server.URL+"/api/account/pool/pick", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+ada+":accounts.pool,accounts.observe")
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer res.Body.Close()
		var envelope map[string]json.RawMessage
		if err := json.NewDecoder(res.Body).Decode(&envelope); err != nil || res.StatusCode != http.StatusOK {
			t.Fatalf("a pick of a null rank answered %d (%v): %s", res.StatusCode, err, sortedJSON(t, envelope))
		}
		wantMember(t, as[picked](t, envelope["data"]), a1, "open", 1, true)
	})

	t.Run("prefer tries a vendor's open accounts, then its near ones, then the others by rank", func(t *testing.T) {
		owner := subject(t)
		a1, _ := account(t, owner, "a1", "anthropic", "account", 1)
		a2, _ := account(t, owner, "a2", "anthropic", "account", 2)
		o1, _ := account(t, owner, "o1", "openai", "account", 3)
		near, err := reportRoom(asAda, a2, map[string]any{"room": true, "near": true})
		if err != nil {
			t.Fatalf("reportRoom: %v", err)
		}
		if near.Availability != "near" || near.CheckedAt == nil || near.Opened != nil {
			t.Fatalf("a check with room near the limit answered %s, want near", sortedJSON(t, near))
		}
		prefer := map[string]any{"vendor": "anthropic", "kind": "account"}
		match := map[string]any{"owner": owner}
		wantMember(t, pick(t, asAda, map[string]any{"match": match, "prefer": prefer, "exclude": []string{a1}}), a2, "near", 2, true)
		wantMember(t, pick(t, asAda, map[string]any{"match": match, "prefer": prefer, "exclude": []string{a1, a2}}), o1, "open", 3, false)
	})

	t.Run("markOut puts an account out for thirty minutes and arms its recheck at outUntil", func(t *testing.T) {
		owner := subject(t)
		a1, column := account(t, owner, "a1", "anthropic", "account", 1)
		before := now()
		out := mustMarkOut(t, a1, map[string]any{"reason": "rate limited"})
		after := now()
		if out.Availability != "out" || out.OutUntil == nil || *out.OutUntil < before+1800000 || *out.OutUntil > after+1800000 || out.OutReason == nil || *out.OutReason != "rate limited" || out.Unchanged != nil {
			t.Fatalf("markOut answered %s, want out for 1800000 ms from between %d and %d, rate limited", sortedJSON(t, out), before, after)
		}
		at, dueAt, ok := armed(t, column)
		if !ok || at != *out.OutUntil || dueAt != *out.OutUntil {
			t.Fatalf("the recheck message is at %d, due at %d (%t), want both at outUntil %d", at, dueAt, ok, *out.OutUntil)
		}
		if r := read(t, column); r.availability != "out" || r.outUntil != *out.OutUntil {
			t.Fatalf("the account is %+v, want out until %d", r, *out.OutUntil)
		}
	})

	t.Run("markOut keeps the later until, and a repeat answers unchanged", func(t *testing.T) {
		owner := subject(t)
		a1, column := account(t, owner, "a1", "anthropic", "account", 1)
		start := now()
		first := mustMarkOut(t, a1, map[string]any{"until": start + 3600000, "reason": "quota"})
		if first.OutUntil == nil || *first.OutUntil != start+3600000 {
			t.Fatalf("markOut answered %s, want out until %d", sortedJSON(t, first), start+3600000)
		}
		v := version(t, column)
		again := mustMarkOut(t, a1, map[string]any{"until": start + 600000, "reason": "quota"})
		if again.Availability != "out" || again.OutUntil == nil || *again.OutUntil != start+3600000 || again.OutReason == nil || *again.OutReason != "quota" || again.Unchanged == nil || !*again.Unchanged {
			t.Fatalf("the earlier markOut answered %s, want out until %d for quota, unchanged", sortedJSON(t, again), start+3600000)
		}
		unmoved(t, "the earlier markOut", column, v)
	})

	t.Run("a check with room before outUntil leaves the account out", func(t *testing.T) {
		owner := subject(t)
		a1, column := account(t, owner, "a1", "anthropic", "account", 1)
		start := now()
		mustMarkOut(t, a1, map[string]any{"until": start + 3600000, "reason": "quota"})
		before := now()
		checked, err := reportRoom(asAda, a1, map[string]any{"room": true})
		after := now()
		if err != nil {
			t.Fatalf("reportRoom: %v", err)
		}
		if checked.Availability != "out" || checked.CheckedAt == nil || *checked.CheckedAt < before || *checked.CheckedAt > after || checked.Opened != nil || checked.OutUntil == nil || *checked.OutUntil != start+3600000 {
			t.Fatalf("an early check with room answered %s, want out until %d, checked between %d and %d, not opened", sortedJSON(t, checked), start+3600000, before, after)
		}
		if r := read(t, column); r.availability != "out" || r.checkedAt != *checked.CheckedAt {
			t.Fatalf("the account is %+v, want out, checked at %d", r, *checked.CheckedAt)
		}
	})

	t.Run("with every account out, pick answers no member and the soonest outUntil", func(t *testing.T) {
		owner := subject(t)
		a1, _ := account(t, owner, "a1", "anthropic", "account", 1)
		a2, _ := account(t, owner, "a2", "anthropic", "account", 2)
		start := now()
		mustMarkOut(t, a1, map[string]any{"until": start + 600000, "reason": "quota"})
		mustMarkOut(t, a2, map[string]any{"until": start + 1200000, "reason": "quota"})
		got := pick(t, asAda, map[string]any{"match": map[string]any{"owner": owner}})
		if got.Member != nil || got.Until == nil || *got.Until != start+600000 {
			t.Fatalf("pick answered %s, want no member until %d", sortedJSON(t, got), start+600000)
		}
	})

	t.Run("a due recheck hands the handler its event once, whose check with room opens the account", func(t *testing.T) {
		owner := subject(t)
		a1, column := account(t, owner, "a1", "anthropic", "account", 1)
		mustMarkOut(t, a1, map[string]any{"until": now() + 600000, "reason": "rate limited"})
		outUntil := overdue(t, column)
		got := fired(t, a1, column)
		p := got.payload
		if p.DueAt != outUntil || p.OutUntil != outUntil || p.FiredAt < outUntil || p.OutReason == nil || *p.OutReason != "rate limited" || p.CheckedAt != nil {
			t.Fatalf("the recheck's payload is %s, want due at outUntil %d, rate limited, never checked", sortedJSON(t, p), outUntil)
		}
		if got.reported.Availability != "open" || got.reported.Opened == nil || !*got.reported.Opened {
			t.Fatalf("the handler's check answered %s, want the account opened", sortedJSON(t, got.reported))
		}
		if r := read(t, column); r.availability != "open" || r.outUntil != 0 {
			t.Fatalf("the account is %+v after the check, want open with no outUntil", r)
		}
		if at, _, ok := armed(t, column); ok {
			t.Fatalf("the account keeps a recheck message at %d after it opened", at)
		}
		start := time.Now()
		drive(t, func() bool { return time.Since(start) > 1500*time.Millisecond })
		if again := workers.of(a1); len(again) != 1 {
			t.Fatalf("the handler reported on %d events of the account after the worker ran on, want one", len(again))
		}
	})

	t.Run("a recheck whose check finds no room puts the account out until resetsAt and arms the recheck there", func(t *testing.T) {
		owner := subject(t)
		a1, column := account(t, owner, "a1", "anthropic", "account", 1)
		resetsAt := now() + 3600000
		workers.say(a1, answer{Room: false, ResetsAt: &resetsAt})
		mustMarkOut(t, a1, map[string]any{"until": now() + 600000, "reason": "rate limited"})
		overdue(t, column)
		got := fired(t, a1, column)
		if got.reported.Availability != "out" || got.reported.OutUntil == nil || *got.reported.OutUntil != resetsAt || got.reported.Opened != nil {
			t.Fatalf("the handler's check answered %s, want out until %d", sortedJSON(t, got.reported), resetsAt)
		}
		if r := read(t, column); r.availability != "out" || r.outUntil != resetsAt {
			t.Fatalf("the account is %+v after the check, want out until %d", r, resetsAt)
		}
		at, dueAt, ok := armed(t, column)
		if !ok || at != resetsAt || dueAt != resetsAt {
			t.Fatalf("the recheck message is at %d, due at %d (%t), want both at resetsAt %d", at, dueAt, ok, resetsAt)
		}
	})

	t.Run("an account whose Budget meter has no room counts as out until the provider's figure lapses", func(t *testing.T) {
		// The hourly meter's next period must not start while the
		// provider's figure holds, which would give the meter room first.
		if left := time.Until(time.Now().Truncate(time.Hour).Add(time.Hour)); left < 10*time.Second {
			time.Sleep(left + time.Second)
		}
		owner := subject(t)
		a1, _ := account(t, owner, "a1", "anthropic", "account", 1)
		a2, _ := account(t, owner, "a2", "anthropic", "account", 2)
		resetsAt := now() + 2000
		input := as[types.AccountBudgetObserveInput](t, map[string]any{"meter": "tokens", "used": 1000, "resetsAt": resetsAt})
		if _, err := asAda.AccountNamespace.Budget.Observe(ctx, a1, input); err != nil {
			t.Fatalf("observe: %v", err)
		}
		match := map[string]any{"match": map[string]any{"owner": owner}}
		wantMember(t, pick(t, asAda, match), a2, "open", 2, true)
		mustMarkOut(t, a2, map[string]any{"until": now() + 600000, "reason": "quota"})
		got := pick(t, asAda, match)
		if got.Member != nil || got.Until == nil || *got.Until != resetsAt {
			t.Fatalf("pick answered %s, want no member until the figure lapses at %d", sortedJSON(t, got), resetsAt)
		}
		time.Sleep(time.Until(time.UnixMilli(resetsAt)) + 200*time.Millisecond)
		wantMember(t, pick(t, asAda, match), a1, "open", 1, true)
	})

	t.Run("markOut and reportRoom without accounts.pool are forbidden", func(t *testing.T) {
		owner := subject(t)
		a1, column := account(t, owner, "a1", "anthropic", "account", 1)
		v := version(t, column)
		_, err := markOut(asGuest, a1, map[string]any{"reason": "x"})
		wantAPIRefused(t, "the guest's markOut", err, 403, "forbidden", guest+" may not mark Account "+a1+" out: it needs permission accounts.pool")
		_, err = reportRoom(asGuest, a1, map[string]any{"room": true})
		wantAPIRefused(t, "the guest's reportRoom", err, 403, "forbidden", guest+" may not report a check of Account "+a1+": it needs permission accounts.pool")
		unmoved(t, "the guest's markOut and reportRoom", column, v)
	})

	t.Run("reportRoom of a key no row holds is not found", func(t *testing.T) {
		gone := subject(t)
		_, err := reportRoom(asAda, gone, map[string]any{"room": true})
		wantAPIRefused(t, "a reportRoom of a key no row holds", err, 404, "not_found", "Account "+gone+" does not exist")
	})

	// Every case's accounts are its own owner's: a pick of no owner's
	// reads every live account, and answers one of them.
	if got := pick(t, asGuest, map[string]any{}); got.Member == nil || !slices.Contains([]string{"open", "near"}, got.Member.Availability) {
		t.Fatalf("a pick of every account by a caller holding no permission answered %s, want a member", sortedJSON(t, got))
	}
}
`
