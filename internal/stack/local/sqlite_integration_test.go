package local_test

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// TestLocalSQLiteDatabaseRuns applies a local environment of one
// TypeScript server on a sqlite-first database for real, with Bun, the
// migration runner built from this checkout and no Docker (D64;
// docs/stack-model.md, section 18.13): the provisioner creates the SQLite
// file under the environment's state directory, migrates it from the
// SQLite model with superschematic-migrate, and starts the server with the
// file's sqlite: URL, which the runner reports the model at. Applied
// again, the migration is up to date. Destroy keeps the file, and Purge
// removes it.
func TestLocalSQLiteDatabaseRuns(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: the local stack runs a server on Bun and builds the migration runner")
	}
	if _, err := exec.LookPath("bun"); err != nil {
		t.Skipf("bun is not available: %v", err)
	}
	stackName := fmt.Sprintf("local-target-sqlite-it-%d", os.Getpid())
	port := freePort(t)
	web := ir.ServiceRef{Name: "ledger-web", Kind: ir.SchemaKindAPI}
	services := []stack.Service{
		{Name: "ledger-db", Kind: ir.SchemaKindDB, Dialects: []string{registry.SQLDialectSQLite, registry.SQLDialectPostgres}},
		{
			Name: "ledger-web", Kind: ir.SchemaKindAPI, Language: registry.APILanguageTypeScript,
			Dependencies: []ir.ServiceRef{{Name: "ledger-db", Kind: ir.SchemaKindDB}},
			Operations:   []stack.Operation{{Name: "Pages.getHome"}},
		},
	}
	st := &ir.Stack{
		Name:   stackName,
		Deploy: []ir.ServiceRef{web},
		Environments: []*ir.Environment{{
			Name:     "Dev",
			Target:   local.Target,
			Settings: []*ir.DeployableSettings{{Of: ir.DeployableRef{Service: &web}, Values: map[string]any{"port": float64(port)}}},
		}},
	}
	reg, err := registry.Assemble(registry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Resolve(reg, stack.Input{Stack: st, Services: services, Environment: "Dev"})
	if err != nil {
		t.Fatal(err)
	}

	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	outputRoot := filepath.Join(root, "dist")
	if err := os.CopyFS(filepath.Join(outputRoot, local.ModulePath(stackName, "ledger-web")), os.DirFS(filepath.Join("testdata", "faketsserver"))); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outputRoot, "package.json"), []byte(`{"name": "ledger-workspace", "private": true, "workspaces": ["server/*/*"]}`+"\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stateDir, err := local.EnsureStateDir(filepath.Join(root, "schemas"), stackName, "Dev")
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(root, "program")
	model := vectorModel(t, filepath.Join("..", "sqlite-columns", "01-create.plan.json"), "ledger-db")
	data, err := model.CanonicalJSON()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, local.ModelsDir), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, local.ModelsDir, "ledger-db.json"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	runner := buildMigrateRunner(t)
	out := &syncBuffer{}
	prov := &local.Provisioner{Out: out, ReadyTimeout: time.Minute, Migrate: runner}
	req := registry.ProvisionRequest{Environment: env, Dir: dir, OutputRoot: outputRoot, Backend: local.StateBackend(stateDir)}
	t.Cleanup(func() {
		_ = prov.Destroy(context.Background(), req)
		if t.Failed() || testing.Verbose() {
			t.Logf("provisioner output:\n%s", out)
		}
	})
	if err := prov.Render(env, dir); err != nil {
		t.Fatal(err)
	}
	apply := func() {
		t.Helper()
		for _, step := range env.DeployOrder {
			if err := prov.Apply(context.Background(), req, *step); err != nil {
				t.Fatal(err)
			}
		}
	}
	apply()

	file := filepath.Join(stateDir, "databases", "ledger_db.sqlite")
	url := local.SQLiteURL(file)
	if _, err := os.Stat(file); err != nil {
		t.Fatalf("no SQLite file: %v", err)
	}
	resp, err := http.Get(local.ServerURL(port) + "/env")
	if err != nil {
		t.Fatal(err)
	}
	var got map[string]string
	err = json.NewDecoder(resp.Body).Decode(&got)
	_ = resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if got["LEDGER_DB_DATABASE_URL"] != url {
		t.Errorf("the server got LEDGER_DB_DATABASE_URL=%q, want %q", got["LEDGER_DB_DATABASE_URL"], url)
	}
	status, err := exec.Command(runner, "status", "--service", "ledger-db", "--model", "--database-url", url).Output()
	if err != nil {
		t.Fatalf("the runner's status at %s: %v", url, err)
	}
	var applied sqlmigrate.Model
	if err := json.Unmarshal(status, &applied); err != nil {
		t.Fatal(err)
	}
	want, err := model.Hash()
	if err != nil {
		t.Fatal(err)
	}
	if got, err := applied.Hash(); err != nil || got != want {
		t.Errorf("the database holds model %s (%v), want %s", got, err, want)
	}

	if err := prov.Destroy(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	apply()
	if !strings.Contains(out.String(), "migrate ledger-db: up to date at model") {
		t.Errorf("the second apply migrated again:\n%s", out)
	}
	if err := prov.Destroy(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); err != nil {
		t.Errorf("Destroy removed the SQLite file: %v", err)
	}
	if err := prov.Purge(context.Background(), req); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(file); !os.IsNotExist(err) {
		t.Errorf("Purge left the SQLite file: %v", err)
	}
}
