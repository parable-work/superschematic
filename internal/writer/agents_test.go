package writer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// agentRunsSources are fixture-agent-runs-db's files: the orchestration,
// with its @event and @references declarations (D153, D154), and the
// tables and the notifier beside it.
func agentRunsSources(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for _, name := range []string{"agents.schema.ts", "work.schema.ts"} {
		data, err := os.ReadFile(filepath.Join(tsFixtures, "fixture-agent-runs-db", "src", name))
		if err != nil {
			t.Fatal(err)
		}
		files[name] = string(data)
	}
	return files
}

// loadSources loads a TypeScript service of kind named name whose src
// holds files, with the project files the round trip writes.
func loadSources(t *testing.T, name string, kind ir.SchemaKind, files map[string]string) *ir.Schema {
	t.Helper()
	dir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(dir, "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	for file, text := range files {
		if err := os.WriteFile(filepath.Join(dir, "src", file), []byte(text), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	project := &ir.Schema{Name: name, Kind: kind}
	writeServiceConfig(t, dir, project)
	writeTSProject(t, dir, project)
	schema, err := loader.LoadService(dir)
	if err != nil {
		dumpService(t, dir)
		t.Fatalf("load %s: %v", name, err)
	}
	return schema
}

// TestTSWriterWritesAgentOrchestration (D158, D183): the TypeScript writer
// writes each class of the agent orchestration as its @agents("<part>")
// declaration, without the fields and indexes the loader adds for its
// part, and an operation's @autonomy as @autonomy(class, mode?); reading
// each written schema back, in every format, gives the same IR.
func TestTSWriterWritesAgentOrchestration(t *testing.T) {
	db := loadSources(t, "fixture-agents-orchestration-db", ir.SchemaKindDB, agentRunsSources(t))
	source := writtenSource(t, db, FormatTS)
	for _, want := range []string{
		"@versioned\n@agents(\"definitions\")\nexport abstract class Agent {}",
		"@agents(\"runs\")\n@references<AgentRun>()\nexport abstract class AgentRun implements Lease<AgentRun,",
		"@queue()\n@agents(\"requests\")\nexport abstract class AgentRunRequest {}",
		"@agents(\"policies\")\nexport abstract class AutonomyPolicy {}",
		"@agents(\"approvals\")\n@event<AgentApproval>({ kind: \"approval.requested\", title: \"summary\", urgency: \"urgent\" })\nexport abstract class AgentApproval {",
		"@agents(\"controls\")\n@event<AgentControl>({ kind: \"agent.budgetWarning\", urgency: \"normal\" })\nexport abstract class AgentControl implements Budget<AgentControl,",
		`import { AutoGenerate, Budget, Lease, Relation, Retries, Workflow, agents, event, key, queue, references, versioned } from "@superschematic/db";`,
	} {
		if !strings.Contains(source, want) {
			t.Errorf("written TS lacks %q:\n%s", want, source)
		}
	}
	// The part's fields and indexes are the loader's: the written classes
	// hold only what the schema declares.
	for _, added := range []string{"definitionVersion", "paramsHash", "transcriptObject", "pausedReason", "operationClass", "trust:", "@index"} {
		if strings.Contains(source, added) {
			t.Errorf("written TS carries the loader's %s:\n%s", added, source)
		}
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got, want := normalizeIR(t, writeAndReload(t, db, format)), normalizeIR(t, db); got != want {
			t.Fatalf("IR changed after a %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
		}
	}

	api := loadSources(t, "fixture-autonomy-api", ir.SchemaKindAPI, map[string]string{"work.schema.ts": `import { Generic, Identity } from "superscalar";
import { Authenticated, HttpMethod, autonomy, rest } from "@superschematic/api";

export abstract class Payment {
  invoice: Identity.UUID;
  amount: Generic.Int64;
}

export class Work extends Authenticated {
  @rest(HttpMethod.GET, "tasks/{id}")
  @autonomy("read")
  getTask(id: Identity.UUID): string {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "tasks/{id}/notes")
  @autonomy("internalWrite", "tell")
  noteTask(id: Identity.UUID): string {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.POST, "invoices/pay")
  @autonomy("moneyMovement", "ask")
  payInvoice(input: Payment): string {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.GET, "tasks")
  listTasks(): string[] {
    throw new Error("schema declaration only");
  }
}
`})
	source = writtenSource(t, api, FormatTS)
	for _, want := range []string{
		"  @autonomy(\"read\")\n  @rest(HttpMethod.GET, \"tasks/{id}\")",
		"  @autonomy(\"internalWrite\", \"tell\")\n",
		"  @autonomy(\"moneyMovement\", \"ask\")\n",
	} {
		if !strings.Contains(source, want) {
			t.Errorf("written TS lacks %q:\n%s", want, source)
		}
	}
	if strings.Count(source, "@autonomy(") != 3 {
		t.Errorf("written TS has %d @autonomy, want 3:\n%s", strings.Count(source, "@autonomy("), source)
	}
	for _, format := range []Format{FormatTS, FormatJSON, FormatYAML} {
		if got, want := normalizeIR(t, writeAndReload(t, api, format)), normalizeIR(t, api); got != want {
			t.Fatalf("IR changed after a %s round trip\nwant:\n%s\ngot:\n%s", format, want, got)
		}
	}
}
