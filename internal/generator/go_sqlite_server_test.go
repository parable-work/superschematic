package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The Go server and job of deps-go-stock on a SQLite file (D116).
const (
	goStockStack  = "deps-go-stock-stack"
	goStockServer = "deps-go-stock"
	goStockJob    = "deps-go-stock-close-restocks"
)

// goStockOnSQLite loads deps-stock-db, with the sqlite dialect listed
// first and its workers' queues left to Postgres, and deps-go-stock and
// its stack.
func goStockOnSQLite(t *testing.T) (map[string]*ir.Schema, map[string]*schemaconfig.SchemaConfig) {
	t.Helper()
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for _, name := range []string{"deps-stock-db", goStockServer, goStockStack} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(depsFixtures, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	enabled := map[string]any{"enabled": true}
	db := *configs["deps-stock-db"]
	db.Outputs = map[string]any{
		"types": map[string]any{"go": enabled},
		"sql":   map[string]any{"dialects": []any{"sqlite", "postgres"}},
	}
	configs["deps-stock-db"] = &db
	return schemas, configs
}

// TestGoServerAndJobOnSQLite builds deps-go-stock, a Go API whose
// database lists sqlite first, and its stack, as build-all does (D116): the
// server's and the job's entrypoints open the database's SQLite file with
// the HTTP runtime's sqlitefile, and the API's Deps.DB is the ORM's
// SQLiteDatabase. With the implementation written against the ORM's
// interfaces, the server starts on a file the build's SQLite DDL creates,
// reports the file in /readyz, and serves a write and a read of the table
// through the repositories; the job runs over the same file and its write
// is what the server then reads; and SIGTERM stops the server.
//
// It runs outside -short, and needs no database.
func TestGoServerAndJobOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go server on SQLite in -short mode")
	}
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	schemas, configs := goStockOnSQLite(t)
	for _, service := range []string{"deps-stock-db", goStockServer, goStockStack} {
		// The stack's build scaffolds the implementation, with its go.mod.
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot:     outputRoot,
			Paths:          paths,
			Naming:         naming.Default(),
			Clock:          codegen.FixedClock(time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)),
			RepositoryRoot: repoRoot,
			LoadDependency: func(name string) (*ir.Schema, error) {
				if schema, ok := schemas[name]; ok {
					return schema, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
			DependencyConfig: func(name string) (*schemaconfig.SchemaConfig, bool) {
				cfg, ok := configs[name]
				return cfg, ok
			},
			LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
				if cfg, ok := configs[name]; ok {
					return cfg, nil
				}
				return nil, fmt.Errorf("no fixture %s", name)
			},
		}); err != nil {
			t.Fatalf("run %s: %v", service, err)
		}
	}
	serverDir := servergen.ServerDir(outputRoot, goStockStack, goStockServer)
	jobDir := servergen.ServerDir(outputRoot, goStockStack, goStockJob)
	for _, dir := range []string{serverDir, jobDir} {
		main, err := os.ReadFile(filepath.Join(dir, servergen.MainFile))
		if err != nil {
			t.Fatal(err)
		}
		for _, want := range []string{"sqlitefile.Open(", "depsstockdborm.OpenSQLite(ctx, depsStockDbSQL)"} {
			if !strings.Contains(string(main), want) {
				t.Errorf("%s opens no SQLite file: it lacks %s:\n%s", dir, want, main)
			}
		}
		if strings.Contains(string(main), "pgxpool") {
			t.Errorf("%s connects a Postgres pool:\n%s", dir, main)
		}
	}

	// The implementation, written against the ORM's interfaces: a restock
	// request created and read through Deps.DB, and the job's close of
	// every open one.
	implDir := naming.Default().GoImplementationDir(repoRoot, goStockServer)
	writeGoStockImplementation(t, implDir)

	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	binaries := map[string]string{}
	for name, dir := range map[string]string{goStockServer: serverDir, goStockJob: jobDir} {
		for _, args := range [][]string{{"mod", "tidy"}, {"vet", "."}} {
			goIn(t, dir, env, args...)
		}
		binaries[name] = filepath.Join(t.TempDir(), name)
		goIn(t, dir, env, "build", "-o", binaries[name], ".")
	}

	// The file a migration makes, from the build's SQLite DDL.
	sqlite3, err := exec.LookPath("sqlite3")
	if err != nil {
		t.Skip("sqlite3 not found, skipping the Go server on SQLite")
	}
	file := filepath.Join(t.TempDir(), "deps_stock_db.sqlite")
	ddl := filepath.Join(SQLDir(outputRoot, "deps-stock-db"), SQLiteSubdir, "create.sql")
	if out, err := exec.Command(sqlite3, file, ".read "+ddl).CombinedOutput(); err != nil {
		t.Fatalf("sqlite3 .read %s: %v\n%s", ddl, err, out)
	}
	databaseURL := "DEPS_STOCK_DB_DATABASE_URL=sqlite:" + file

	port := freeLocalPort(t)
	server := exec.Command(binaries[goStockServer])
	server.Env = append(os.Environ(), databaseURL, "PORT="+port)
	output := &syncBuffer{}
	server.Stdout, server.Stderr = output, output
	if err := server.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = server.Process.Kill(); _, _ = server.Process.Wait() })
	base := "http://127.0.0.1:" + port
	deadline := time.Now().Add(30 * time.Second)
	for {
		if status, _ := httpGet(base + "/readyz"); status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server is not ready within 30s:\n%s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}

	product := "00000000-0000-4000-8000-000000000001"
	status, body := httpDo(t, http.MethodPost, base+"/api/restocks", `{"productId":"`+product+`"}`)
	if status != http.StatusOK && status != http.StatusCreated {
		t.Fatalf("POST /api/restocks = %d %s\n%s", status, body, output.String())
	}
	created := restockOf(t, body)
	if created["status"] != "open" || created["productId"] == "" || created["id"] == "" {
		t.Fatalf("the created restock request = %v", created)
	}
	status, body = httpDo(t, http.MethodGet, base+"/api/restocks/"+created["id"], "")
	if got := restockOf(t, body); status != http.StatusOK || got["id"] != created["id"] || got["status"] != "open" {
		t.Fatalf("GET /api/restocks/%s = %d %s", created["id"], status, body)
	}
	if status, body = httpDo(t, http.MethodGet, base+"/api/restocks/00000000-0000-4000-8000-0000000000ff", ""); status != http.StatusNotFound {
		t.Errorf("GET of a restock request no row holds = %d %s, want 404", status, body)
	}

	// The job closes it on the same file, and the server reads it closed.
	job := exec.Command(binaries[goStockJob])
	job.Env = append(os.Environ(), databaseURL)
	if out, err := job.CombinedOutput(); err != nil || !strings.Contains(string(out), `"msg":"closed restocks"`) {
		t.Fatalf("the job = %v:\n%s", err, out)
	}
	status, body = httpDo(t, http.MethodGet, base+"/api/restocks/"+created["id"], "")
	if got := restockOf(t, body); status != http.StatusOK || got["status"] != "closed" {
		t.Errorf("GET after the job = %d %s, want it closed", status, body)
	}

	if err := server.Process.Signal(syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- server.Wait() }()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("the server exited with %v after SIGTERM:\n%s", err, output.String())
		}
	case <-time.After(15 * time.Second):
		t.Errorf("the server did not stop within 15s of SIGTERM:\n%s", output.String())
	}
}

// writeGoStockImplementation fills the scaffold of deps-go-stock in implDir
// with an implementation over the ORM's interfaces.
func writeGoStockImplementation(t *testing.T, implDir string) {
	t.Helper()
	file := filepath.Join(implDir, "implementation.go")
	source, err := os.ReadFile(file)
	if err != nil {
		t.Fatal(err)
	}
	implemented := string(source)
	for old, replacement := range goStockImplementation {
		if !strings.Contains(implemented, old) {
			t.Fatalf("%s holds no %q:\n%s", file, old, source)
		}
		implemented = strings.Replace(implemented, old, replacement, 1)
	}
	if err := os.WriteFile(file, []byte(implemented), 0o644); err != nil {
		t.Fatal(err)
	}
}

// goStockImplementation are the scaffold's not-implemented bodies and what
// replaces each.
var goStockImplementation = map[string]string{
	"import (\n\t\"context\"\n": "import (\n\t\"context\"\n\t\"errors\"\n\n\torm \"example.com/schemas/orm/deps-stock-db\"\n\tdbtypes \"example.com/schemas/types/go/deps-stock-db\"\n\t\"go.uber.org/zap\"\n",
	`return api.NotImplementedError("job CloseRestocks")`: `open, closed := "open", "closed"
	n, err := j.deps.DB.GetRestockRequestRepository().UpdateMany(ctx, &orm.RestockRequestFilter{Status: &orm.StringFilter{Eq: &open}}, &orm.RestockRequestUpdate{Status: &closed})
	if err != nil {
		return err
	}
	j.deps.Logger.Info("closed restocks", zap.Int("rows", n))
	return nil`,
	`return nil, api.NotImplementedError("Restocks.RequestRestock")`: `product, err := dbtypes.ParseIdentityUUID(productId)
	if err != nil {
		return nil, err
	}
	row, err := impl.deps.DB.GetRestockRequestRepository().CreateOne(ctx, &dbtypes.RestockRequest{ProductId: product, Status: "open"})
	if err != nil {
		return nil, err
	}
	return restockView(row), nil`,
	`return nil, api.NotImplementedError("Restocks.GetRestock")
}`: `key, err := dbtypes.ParseIdentityUUID(id)
	if err != nil {
		return nil, err
	}
	row, err := impl.deps.DB.GetRestockRequestRepository().GetOne(ctx, key, nil)
	if errors.Is(err, orm.ErrNotFound) {
		return nil, api.NotFoundError("RestockRequest", err)
	}
	if err != nil {
		return nil, err
	}
	return restockView(row), nil
}

// restockView is a restock request as the API answers it.
func restockView(row *dbtypes.RestockRequest) *types.RestockView {
	return &types.RestockView{Id: row.Id.ToUUID().String(), ProductId: row.ProductId.ToUUID().String(), Status: row.Status}
}`,
}

// restockOf is the data of a response's envelope, a restock request.
func restockOf(t *testing.T, body string) map[string]string {
	t.Helper()
	var envelope struct {
		Data map[string]string `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &envelope); err != nil {
		t.Fatalf("the response %s: %v", body, err)
	}
	return envelope.Data
}

// goIn runs go args in dir.
func goIn(t *testing.T, dir string, env []string, args ...string) {
	t.Helper()
	cmd := exec.Command("go", args...)
	cmd.Dir = dir
	cmd.Env = append(cmd.Environ(), env...)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("go %s in %s: %v\n%s", strings.Join(args, " "), dir, err, out)
	}
}

// freeLocalPort is a port nothing listens on.
func freeLocalPort(t *testing.T) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return strconv.Itoa(l.Addr().(*net.TCPAddr).Port)
}

func httpGet(url string) (int, string) {
	resp, err := http.Get(url)
	if err != nil {
		return 0, err.Error()
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(body)
}

func httpDo(t *testing.T, method, url, body string) (int, string) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, method, url, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, url, err)
	}
	defer resp.Body.Close()
	out, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(out)
}
