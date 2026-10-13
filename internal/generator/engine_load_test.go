package generator

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// engineFixtureInstances are the instances the load test creates: an
// account, then a ticket that links to it, with its contact's email in
// another case than the engine stores it and no priority, which takes
// its default.
const engineFixtureInstances = `[
  {"schema": "Account", "id": "acme", "data": {"name": "Acme", "slug": "acme", "address": {"line1": "1 Main St", "city": "Springfield", "country": "US"}}},
  {"schema": "Ticket", "id": "t1", "data": {"title": "The printer is on fire", "contact": {"email": "Ada@Example.COM"}, "tags": ["hardware"]}, "behaviors": {"Links": {"account": "acme"}}}
]`

// TestEngineDocumentLoadsInTheEngine builds the engine fixture as build
// does and loads its engine document into today's TypeScript engine,
// @superschematic/engine, as a server will (D58): testdata/engine-document/
// load.ts opens an engine with the document's meta-schema, tool options and
// namespace, defines and publishes every schema-file document and creates
// an instance of each engine type. The engine package must be built
// (runtime/engine/typescript: bun install --frozen-lockfile && bun run
// build, after runtime/schema, runtime/http and runtime/versiongraph's
// TypeScript); without it, or without bun, the gate skips, or fails under
// SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1.
//
// It also type-checks the fixture's TypeScript types package, whose
// engine.ts exports the document as engineDocument.
func TestEngineDocumentLoadsInTheEngine(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping TypeScript gate in -short mode")
	}
	bunPath, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	engineDir := filepath.Join(testpaths.RepoRoot(t), "runtime", "engine", "typescript")
	if _, err := os.Stat(filepath.Join(engineDir, "dist", "index.js")); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("@superschematic/engine is not built (bun install --frozen-lockfile && bun run build in %s): %v", engineDir, err))
	}

	schema, cfg, err := loader.LoadServiceWithConfig(engineFixtureDir)
	if err != nil {
		t.Fatal(err)
	}
	root := testpaths.TempDir(t)
	if _, err := Run(schema, cfg, Options{OutputRoot: root, Paths: testpaths.Local(t), Naming: naming.Default()}); err != nil {
		t.Fatalf("Run: %v", err)
	}
	instances := filepath.Join(t.TempDir(), "instances.json")
	if err := os.WriteFile(instances, []byte(engineFixtureInstances), 0o644); err != nil {
		t.Fatal(err)
	}
	load := exec.Command(bunPath, filepath.Join("testdata", "engine-document", "load.ts"), engineDir, EngineDocumentPath(root, engineFixture), instances)
	stdout, err := load.Output()
	if err != nil {
		stderr := ""
		if exit, ok := err.(*exec.ExitError); ok {
			stderr = string(exit.Stderr)
		}
		t.Fatalf("the engine refused the engine document: %v\n%s", err, stderr)
	}

	var got struct {
		Published []struct {
			Namespace string `json:"namespace"`
			Name      string `json:"name"`
			Version   int    `json:"version"`
			Published bool   `json:"published"`
		} `json:"published"`
		Created []struct {
			Namespace string                     `json:"namespace"`
			Schema    string                     `json:"schema"`
			ID        string                     `json:"id"`
			Data      map[string]any             `json:"data"`
			Behaviors map[string]json.RawMessage `json:"behaviors"`
		} `json:"created"`
	}
	if err := json.Unmarshal(stdout, &got); err != nil {
		t.Fatalf("decode the harness's output: %v\n%s", err, stdout)
	}
	if len(got.Published) != 2 {
		t.Fatalf("published = %+v, want Account and Ticket", got.Published)
	}
	for i, name := range []string{"Account", "Ticket"} {
		p := got.Published[i]
		if p.Namespace != engineFixture || p.Name != name || p.Version != 1 || !p.Published {
			t.Errorf("published[%d] = %+v, want version 1 of %s in %s", i, p, name, engineFixture)
		}
	}
	if len(got.Created) != 2 {
		t.Fatalf("created = %+v", got.Created)
	}
	ticket := got.Created[1]
	if ticket.Schema != "Ticket" || ticket.ID != "t1" || ticket.Namespace != engineFixture {
		t.Fatalf("created[1] = %+v", ticket)
	}
	// The engine stores what the schema's parse makes of the data: the
	// email in its canonical form, and the priority's default.
	if want := map[string]any{"email": "ada@example.com"}; !reflect.DeepEqual(ticket.Data["contact"], want) {
		t.Errorf("contact = %v, want %v", ticket.Data["contact"], want)
	}
	if ticket.Data["priority"] != "normal" {
		t.Errorf("priority = %v, want the default, normal", ticket.Data["priority"])
	}
	var behaviors struct {
		Workflow struct {
			Status string `json:"status"`
		}
		Links struct {
			Targets map[string]struct {
				Schema string `json:"schema"`
				ID     string `json:"id"`
			} `json:"targets"`
		}
		Timers struct {
			Deadlines map[string]struct {
				Fired bool `json:"fired"`
			} `json:"deadlines"`
		}
		Comments struct {
			CommentCount int `json:"commentCount"`
		}
	}
	raw, err := json.Marshal(ticket.Behaviors)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &behaviors); err != nil {
		t.Fatal(err)
	}
	if behaviors.Workflow.Status != "open" {
		t.Errorf("Workflow.status = %q, want open", behaviors.Workflow.Status)
	}
	if target := behaviors.Links.Targets["account"]; target.Schema != "Account" || target.ID != "acme" {
		t.Errorf("Links.targets.account = %+v, want Account acme", target)
	}
	if deadline, ok := behaviors.Timers.Deadlines["respond"]; !ok || deadline.Fired {
		t.Errorf("Timers.deadlines = %+v, want respond, not fired", behaviors.Timers.Deadlines)
	}
	if _, ok := ticket.Behaviors["Comments"]; !ok {
		t.Errorf("the ticket has no Comments: %v", ticket.Behaviors)
	}

	typesDir := TypesDir(root, LangTypeScript, engineFixture)
	install := exec.Command(bunPath, "install")
	install.Dir = typesDir
	if out, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed for the fixture's types (likely offline): %v\n%s", err, out))
	}
	typecheck := exec.Command(bunPath, "x", "tsc", "--noEmit")
	typecheck.Dir = typesDir
	if out, err := typecheck.CombinedOutput(); err != nil {
		t.Fatalf("the fixture's TypeScript types, with engine.ts, do not type-check: %v\n%s", err, out)
	}
}
