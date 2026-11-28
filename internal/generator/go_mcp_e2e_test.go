package generator

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
)

// The generated Go server's MCP endpoint over Postgres (#660, #621,
// #616): fixture-agent-runs-go-api, whose wiring builds the agent
// dispatcher and search across tables, with outputs.api.mcp on, its
// getTask a visible tool that asks (D93), and the confirmation key in its
// @envVars.

// mcpWiringKey is the endpoint's confirmation key in the test's
// environment.
const mcpWiringKey = "kkkkkkkkkkkkkkkkeeeeeeeeeeeeeeeey"

// buildMCPWiringStack builds fixture-agent-runs-db, fixture-agent-runs-go-api
// with its MCP endpoint on and its stack under repoRoot, as
// buildWiringStack does, and returns the output root.
func buildMCPWiringStack(t *testing.T, repoRoot string) string {
	t.Helper()
	outputRoot := filepath.Join(repoRoot, "schemas", "dist")
	schemas := map[string]*ir.Schema{}
	configs := map[string]*schemaconfig.SchemaConfig{}
	for name, dir := range map[string]string{wiringDB: tsFixtures, wiringAPI: tsFixtures, wiringStack: depsFixtures} {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		schemas[name], configs[name] = schema, cfg
	}
	db := *configs[wiringDB]
	db.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}},
		"sql":   map[string]any{"dialects": []any{"postgres"}},
	}
	configs[wiringDB] = &db
	api := *configs[wiringAPI]
	api.Outputs = map[string]any{
		"types": map[string]any{"go": map[string]any{"enabled": true}},
		"api":   map[string]any{"enabled": true, "mcp": true, "mcpConfirmationKey": "FixtureAgentRunsGoConfig.MCP_CONFIRMATION_KEY"},
	}
	configs[wiringAPI] = &api
	schema := schemas[wiringAPI]
	config := schema.Types["FixtureAgentRunsGoConfig"]
	config.Fields = append(config.Fields, &ir.FieldDef{Name: "MCP_CONFIRMATION_KEY", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Secret: true})
	for _, set := range schema.OperationSets {
		for _, op := range set.Operations {
			if op.Name == "getTask" {
				op.Docs = &ir.OperationDocs{Title: "Get a task", Description: "Reads one task as its caller.", Capability: "tasks.get", Lifecycle: ir.DocsLifecycleActive, Visibility: ir.DocsVisibilityPublic, MappingStatus: ir.DocsMappingStatusMapped, ReplayMode: ir.DocsReplayModeReadOnly}
				op.MCP = &ir.OperationMCP{Handle: "get_task", Invocation: ir.MCPInvocation{Key: "invocationPolicy", Value: "ask"}}
				op.Autonomy = &ir.OperationAutonomy{Class: "read"}
			}
		}
	}
	for _, service := range []string{wiringDB, wiringAPI, wiringStack} {
		if _, err := Run(schemas[service], configs[service], Options{
			OutputRoot:     outputRoot,
			Paths:          testpaths.Local(t),
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
	return outputRoot
}

// TestGoServerServesItsMCPEndpointEndToEnd builds the stack with the MCP
// endpoint on and runs the generated server and its indexing worker as
// processes on the Postgres behaviorServerDatabaseEnv names, in a schema
// of their own, with the app's rows the wiring probe seeds. main.go builds
// the API's router with the endpoint's key, the dispatcher's call gate and
// the search tool. Over the wire: tools/list as a grant's holder lists
// get_task, with its confirmation argument, and search; get_task asks, a
// call its route refuses answers the route's 400 at once, and the call
// with its token runs once as the caller, through the route; a task its
// grant does not reach is the route's 404 once confirmed; search keeps to
// what the grant reaches; and an anonymous caller is answered 401. It
// skips without the database and in -short mode.
func TestGoServerServesItsMCPEndpointEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go server's MCP endpoint end to end in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go server's MCP endpoint against Postgres")
	}
	repoRoot := testpaths.TempDir(t)
	outputRoot := buildMCPWiringStack(t, repoRoot)
	serverDir := servergen.ServerDir(outputRoot, wiringStack, wiringServer)
	main, err := os.ReadFile(filepath.Join(serverDir, servergen.MainFile))
	if err != nil {
		t.Fatal(err)
	}
	// gofmt aligns the members, so they are read with their spaces as one.
	words := strings.Join(strings.Fields(string(main)), " ")
	for _, want := range []string{
		"MCP: fixtureagentrunsgoapi.MCPOptions{",
		"ConfirmationKey: []byte(fixtureAgentRunsGoApiConfig.McpConfirmationKey),",
		"CallGate: fixtureAgentRunsGoApiDeps.Agents.MCPCallGate(),",
		"Tools: fixtureagentrunsgoapi.MCPTools(fixtureAgentRunsDb.CrossTables(), fixtureAgentRunsGoApiWiring),",
	} {
		if !strings.Contains(words, want) {
			t.Errorf("main.go lacks %s:\n%s", want, main)
		}
	}
	implFile := filepath.Join(naming.Default().GoImplementationDir(repoRoot, wiringAPI), "implementation.go")
	impl, err := os.ReadFile(implFile)
	if err != nil {
		t.Fatal(err)
	}
	scaffolded := `Agents: agents.Options{Endpoint: "http://localhost:8080/api/agents"},`
	if err := os.WriteFile(implFile, []byte(strings.Replace(string(impl), scaffolded, `Agents: agents.Options{Endpoint: deps.Config.AgentEndpoint},`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(serverDir, "probe"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(serverDir, "probe", "main.go"), []byte(wiringProbe), 0o644); err != nil {
		t.Fatal(err)
	}
	env := []string{"CGO_LDFLAGS=" + archiveLDFlags(t)}
	bin := t.TempDir()
	goIn(t, serverDir, env, "mod", "tidy")
	goIn(t, serverDir, env, "vet", ".", "./probe")
	goIn(t, serverDir, env, "build", "-o", filepath.Join(bin, "server"), ".")
	goIn(t, serverDir, env, "build", "-o", filepath.Join(bin, "probe"), "./probe")
	indexer := servergen.ServerDir(outputRoot, wiringStack, wiringWorkers["IndexSearch"])
	goIn(t, indexer, env, "mod", "tidy")
	goIn(t, indexer, env, "build", "-o", filepath.Join(bin, "IndexSearch"), ".")

	probe := func(database string, args ...string) string {
		t.Helper()
		cmd := exec.Command(filepath.Join(bin, "probe"), args...)
		cmd.Env = append(os.Environ(), "PROBE_DATABASE_URL="+database)
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("probe %s: %v\n%s", strings.Join(args, " "), err, out)
		}
		return string(out)
	}
	schema := fmt.Sprintf("mcp_go_%d", time.Now().UnixNano())
	createSQL := filepath.Join(t.TempDir(), "create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, wiringDB), "create.sql"), createSQL)
	probe(dsn, "setup", schema, createSQL)
	t.Cleanup(func() { probe(dsn, "drop", schema) })
	database := withSearchPath(t, dsn, schema)
	var seeded map[string]string
	if err := json.Unmarshal([]byte(probe(database, "seed")), &seeded); err != nil {
		t.Fatal(err)
	}
	// A second task, which the customer's grant does not reach.
	var other []map[string]string
	if err := json.Unmarshal([]byte(probe(database, "query", `INSERT INTO task (title, details, state) VALUES ('Boiler repair', 'the boiler leaks', 'open') RETURNING CAST(id AS TEXT) AS id`)), &other); err != nil || len(other) != 1 {
		t.Fatalf("the second task: %v %v", other, err)
	}

	relay := newStandInRelay(t)
	port := freeLocalPort(t)
	base := "http://127.0.0.1:" + port
	runEnv := append(os.Environ(),
		"FIXTURE_AGENT_RUNS_DB_DATABASE_URL="+database,
		"SMS=sms-relay-a", "SMS_URL="+relay.server.URL, "SMS_TOKEN="+wiringRelayToken, "SMS_SENDER=+15550109999",
		"AGENT_ENDPOINT="+base+"/api/agents",
		"MCP_CONFIRMATION_KEY="+mcpWiringKey,
	)
	output := &syncBuffer{}
	start := func(name string, extra ...string) {
		t.Helper()
		cmd := exec.Command(filepath.Join(bin, name))
		cmd.Env = append(slices.Clone(runEnv), extra...)
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
	}
	start("server", "PORT="+port)
	for deadline := time.Now().Add(30 * time.Second); ; {
		if status, _ := httpGet(base + "/readyz"); status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server is not ready within 30s:\n%s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	start("IndexSearch")

	token, task, boiler := seeded["token"], seeded["task"], other[0]["id"]
	rpc := func(t *testing.T, bearer, body string) (int, map[string]any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodPost, base+"/api/mcp", strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "application/json, text/event-stream")
		if bearer != "" {
			req.Header.Set("Authorization", "Bearer "+bearer)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		text := strings.TrimSuffix(strings.TrimPrefix(string(data), "event: message\ndata: "), "\n\n")
		var answer map[string]any
		_ = json.Unmarshal([]byte(text), &answer)
		return resp.StatusCode, answer
	}
	callTool := func(t *testing.T, tool, arguments string) map[string]any {
		t.Helper()
		_, answer := rpc(t, token, `{"jsonrpc":"2.0","id":1,"method":"tools/call","params":{"name":"`+tool+`","arguments":`+arguments+`}}`)
		result, _ := answer["result"].(map[string]any)
		if result == nil {
			t.Fatalf("tools/call %s answered %v\n%s", tool, answer, output.String())
		}
		return result
	}
	structuredOf := func(result map[string]any) map[string]any {
		out, _ := result["structuredContent"].(map[string]any)
		return out
	}
	tokenOf := func(t *testing.T, result map[string]any) string {
		t.Helper()
		problem := structuredOf(result)
		details, _ := problem["details"].(map[string]any)
		confirmation, _ := details["confirmationToken"].(string)
		if problem["code"] != "confirmation_required" || confirmation == "" {
			t.Fatalf("get_task answered %v, want confirmation_required with a token", result)
		}
		return confirmation
	}

	if status, _ := rpc(t, "", `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`); status != http.StatusUnauthorized {
		t.Errorf("an anonymous tools/list = %d, want 401", status)
	}
	_, listed := rpc(t, token, `{"jsonrpc":"2.0","id":1,"method":"tools/list"}`)
	result, _ := listed["result"].(map[string]any)
	tools, _ := result["tools"].([]any)
	var names []string
	for _, tool := range tools {
		names = append(names, tool.(map[string]any)["name"].(string))
	}
	if strings.Join(names, ",") != "get_task,search" {
		t.Fatalf("tools/list = %v, want get_task and search", listed)
	}
	if properties := tools[0].(map[string]any)["inputSchema"].(map[string]any)["properties"].(map[string]any); properties["confirmationToken"] == nil {
		t.Errorf("get_task takes %v, want its confirmation argument", properties)
	}

	// get_task asks; a call its route refuses answers the route's 400 at
	// once and spends no token (D98); the call with its token runs once.
	confirmation := tokenOf(t, callTool(t, "get_task", `{"id":"`+task+`"}`))
	if p := structuredOf(callTool(t, "get_task", `{"id":"not a task","confirmationToken":"`+confirmation+`"}`)); p["status"] != float64(400) {
		t.Errorf("a call the route refuses answered %v, want its 400", p)
	}
	if got := structuredOf(callTool(t, "get_task", `{"id":"`+task+`","confirmationToken":"`+confirmation+`"}`)); got["title"] != "Pump overhaul" {
		t.Errorf("get_task with its token answered %v", got)
	}
	if p := structuredOf(callTool(t, "get_task", `{"id":"`+task+`","confirmationToken":"`+confirmation+`"}`)); p["code"] != "confirmation_required" || p["details"].(map[string]any)["refused"] != "used" {
		t.Errorf("a used token answered %v", p)
	}
	// A task the grant does not reach is the route's refusal, as the
	// caller reads it.
	confirmation = tokenOf(t, callTool(t, "get_task", `{"id":"`+boiler+`"}`))
	if p := structuredOf(callTool(t, "get_task", `{"id":"`+boiler+`","confirmationToken":"`+confirmation+`"}`)); p["status"] != float64(404) {
		t.Errorf("a task outside the grant answered %v, want the route's 404", p)
	}

	// Search through the endpoint keeps to what the grant reaches, as the
	// route does.
	hits := func(t *testing.T, q string) string {
		t.Helper()
		return fmt.Sprint(structuredOf(callTool(t, "search", `{"q":"`+q+`"}`))["items"])
	}
	for deadline := time.Now().Add(30 * time.Second); !strings.Contains(hits(t, "pump"), "Pump overhaul"); {
		if time.Now().After(deadline) {
			t.Fatalf("search through the endpoint found no task of the customer's: %v", callTool(t, "search", `{"q":"pump"}`))
		}
		time.Sleep(200 * time.Millisecond)
	}
	if got := hits(t, "boiler"); strings.Contains(got, "Boiler") {
		t.Errorf("the customer's search found a task its grant does not reach: %s", got)
	}
	if p := structuredOf(callTool(t, "search", `{}`)); p["status"] != float64(400) {
		t.Errorf("a search with no words answered %v", p)
	}
	if strings.Contains(output.String(), mcpWiringKey) {
		t.Errorf("the confirmation key reached the logs")
	}
}
