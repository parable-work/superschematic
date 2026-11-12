package loader

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// loadIssuesDB loads a TypeScript DB service whose Issue holds status, a
// Workflow of the config written.
func loadIssuesDB(t *testing.T, config string) (*ir.Schema, error) {
	t.Helper()
	root := testpaths.RepoRoot(t)
	base := filepath.ToSlash(filepath.Join(root, "internal", "loader", "tsreader", "testdata", "tsconfig.base.json"))
	tmp, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(tmp, "issues-db")
	for rel, contents := range map[string]string{
		"schema.config.json": `{"name": "issues-db", "kind": "DB", "outputs": {}}`,
		"tsconfig.json":      `{"extends": "` + base + `", "include": ["src/**/*.ts"]}`,
		"src/issues.schema.ts": `import { Identity } from "superscalar";
import { AutoGenerate, Workflow, key } from "@superschematic/db";

export abstract class Issue {
  @key
  id: AutoGenerate<Identity.UUID>;

  title: string;

  status: Workflow<` + config + `>;
}
`,
	} {
		path := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return LoadService(dir)
}

// TestAWorkflowsListedTransitionsAreWrittenOut: a table's Workflow whose
// transitions list states loads as the plain transitions they stand for
// (D109), in the config every check and generator reads: five states that
// move freely among the three open ones and close from any of them are
// three lines, and the IR holds their thirteen transitions.
func TestAWorkflowsListedTransitionsAreWrittenOut(t *testing.T) {
	schema, err := loadIssuesDB(t, `{
    states: ["backlog", "todo", "started", "done", "canceled"];
    transitions: [
      { from: ["backlog", "todo", "started"]; to: ["backlog", "todo", "started"] },
      { from: ["backlog", "todo", "started"]; to: ["done", "canceled"]; permission: "issues.close" },
      { from: "done"; to: "todo" },
    ];
    outcomes: { canceled: "neutral" };
  }`)
	if err != nil {
		t.Fatal(err)
	}
	use, ok := schema.Types["Issue"].FieldBehavior("status")
	if !ok {
		t.Fatal("Issue.status anchors no behavior")
	}
	var config struct {
		Transitions []struct{ From, To, Permission string }
	}
	if err := json.Unmarshal(use.Config, &config); err != nil {
		t.Fatalf("the config's transitions are not each a from and a to: %v\n%s", err, use.Config)
	}
	var got []string
	for _, tr := range config.Transitions {
		got = append(got, strings.TrimSpace(tr.From+">"+tr.To+" "+tr.Permission))
	}
	want := []string{
		"backlog>todo", "backlog>started", "todo>backlog", "todo>started", "started>backlog", "started>todo",
		"backlog>done issues.close", "backlog>canceled issues.close", "todo>done issues.close", "todo>canceled issues.close", "started>done issues.close", "started>canceled issues.close",
		"done>todo",
	}
	if strings.Join(got, "; ") != strings.Join(want, "; ") {
		t.Errorf("transitions =\n%s\nwant\n%s", strings.Join(got, "; "), strings.Join(want, "; "))
	}
	// The field's type is still the union of the config's states, and the
	// table gained the columns Workflow owns.
	if status := fieldNamed(schema.Types["Issue"].Fields, "status"); status == nil || !status.Required {
		t.Errorf("Issue.status = %+v", status)
	}

	// What only the short form gets wrong fails the load at the config,
	// in the checks' own form.
	_, err = loadIssuesDB(t, `{
    states: ["backlog", "todo", "done"];
    transitions: [{ from: "todo"; to: "done" }, { from: ["backlog", "todo"]; to: "done" }];
  }`)
	const refusal = `type Issue: field status: behavior Workflow config: the transition from "todo" to "done" is listed twice: by the transition from "todo" to "done", and by the transition from ["backlog", "todo"] to "done"`
	if err == nil || !strings.Contains(err.Error(), refusal) {
		t.Errorf("a move listed twice: %v\nwant %s", err, refusal)
	}
	// A state the Workflow does not have is the config's own rule, which
	// the build holds each written-out transition to, in the core's words.
	_, err = loadIssuesDB(t, `{
    states: ["backlog", "done"];
    transitions: [{ from: ["backlog", "todo"]; to: "done" }];
  }`)
	const unknown = `type Issue: field status: behavior Workflow config: the transition from "todo" to "done" names "todo", which is not one of its states (backlog, done)`
	if err == nil || !strings.Contains(err.Error(), unknown) {
		t.Errorf("a list that names no state of the Workflow: %v\nwant %s", err, unknown)
	}
}
