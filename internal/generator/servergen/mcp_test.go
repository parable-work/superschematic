package servergen_test

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

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
)

const (
	// agentAPI is a TypeScript API over the identity fixture's users-db
	// with outputs.api.mcp on (D81): its visible @mcp tools are greet,
	// which reads, set_motto, whose policy is ask, and audit, for a holder
	// of staff; forgetMotto is hidden and motto unclassified. agentStack
	// serves it from its default server, named after it.
	agentAPI   = "users-ts-agent"
	agentStack = "users-ts-agent-stack"
)

var agentOrder = []string{"users-db", agentAPI, agentStack}

// agentConfirmationKey is the environment's value of the secret
// users-ts-agent's outputs.api.mcpConfirmationKey names (D93).
const agentConfirmationKey = "AGENT_MCP_CONFIRMATION_KEY=a random key of at least thirty-two bytes"

// loadAgentFixture loads users-db and the agent API and stack with the
// core registry.
func loadAgentFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range agentOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(identityRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// agentImplementation answers as the signed-in user, by the name the
// identity runtime puts on the principal.
const agentImplementation = `import type { Constructor } from '@schemas/users-ts-agent-api';

let motto = 'none yet';
const nameOf = (ctx: { principal: { claims?: Readonly<Record<string, unknown>> } | null }) => String(ctx.principal?.claims?.name ?? 'nobody');

export const create: Constructor = () => ({
  agent: {
    async greet(_args, ctx) {
      return { message: 'Hello, ' + nameOf(ctx) };
    },
    async audit() {
      return { message: 'motto: ' + motto };
    },
    async motto() {
      return { message: motto };
    },
    async setMotto(args, ctx) {
      motto = args.motto;
      return { message: nameOf(ctx) + "'s motto: " + motto };
    },
    async forgetMotto() {
      motto = 'none yet';
      return { message: motto };
    },
  },
});
`

// TestTypeScriptMCPEndpointServes: a generated TypeScript server serves its
// API's OpenAPI document and page as the Go and Rust servers do, and, with
// outputs.api.mcp on, its visible @mcp operations as MCP tools at POST
// /api/mcp (D81). Bun runs it with no database up: the document and the
// page answer, and the endpoint refuses a request without a session. With
// the Postgres identityDatabaseEnv names, an agent registers and signs in
// through the identity routes, and with the bearer token it initializes,
// lists the tools (the hidden, the unclassified and the one it lacks the
// permission for absent, the ask policy in _meta) and calls them, each
// through its route: a result as the user, and a value the route refuses
// as a tool error with its problem. set_motto, whose policy is ask, runs
// only with the confirmation token its first call answered, signed with
// the key the entrypoint reads from the config field
// outputs.api.mcpConfirmationKey names, and that token once (D93). After
// logout the token is refused.
func TestTypeScriptMCPEndpointServes(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping compile check in -short mode")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	installTypeScriptRuntime(t, bun, paths)
	repoRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f := loadAgentFixture(t)
	f.build(t, repoRoot, paths, agentOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	impl := naming.Default().TypeScriptImplementationDir(repoRoot, agentAPI)
	if err := os.WriteFile(filepath.Join(impl, "index.ts"), []byte(agentImplementation), 0o644); err != nil {
		t.Fatal(err)
	}
	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	dir := servergen.ServerDir(out, agentStack, agentAPI)
	tsc := filepath.Join(paths.HTTPRuntimeTypeScript, "node_modules", ".bin", "tsc")
	for _, pkg := range []string{dir, impl} {
		cmd := exec.Command(tsc, "--noEmit", "-p", "tsconfig.json")
		cmd.Dir = pkg
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("tsc in %s: %v\n%s", pkg, err, output)
		}
	}

	field := ir.IdentityConfigField(agentAPI)
	identityConfig := field + `={"cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	unreachable := "USERS_DB_DATABASE_URL=postgres://users@127.0.0.1:9/users_db?connect_timeout=1&sslmode=disable"
	main, err := os.ReadFile(filepath.Join(dir, servergen.TypeScriptMainFile))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(main), "mcp: { confirmationKey: usersTsAgentConfig.AGENT_MCP_CONFIRMATION_KEY }") {
		t.Errorf("main.ts does not build the router with the confirmation key:\n%s", main)
	}
	offline := bunServer(t, bun, dir, unreachable, identityConfig, agentConfirmationKey)
	offline.expect(t, http.MethodGet, "/api/openapi.json", http.StatusOK, `"/api/greeting"`, `"version": "1.0.0"`, `"url": "http://localhost:8080"`)
	offline.expect(t, http.MethodGet, "/api/docs", http.StatusOK, "<rapi-doc", "users-ts-agent API Documentation")
	if status, _ := offline.rpc(t, "", initialize); status != http.StatusUnauthorized {
		t.Errorf("POST /api/mcp without a session = %d, want 401", status)
	}
	offline.stop(t)

	postgres := os.Getenv(identityDatabaseEnv)
	if postgres == "" {
		t.Logf("%s is unset: the entrypoint serves no database, so no agent signs in", identityDatabaseEnv)
		return
	}
	databaseURL := identitySchemaTS(t, bun, dir, postgres, filepath.Join(generator.SQLDir(out, "users-db"), "create.sql"))
	online := bunServer(t, bun, dir, "USERS_DB_DATABASE_URL="+databaseURL, identityConfig, agentConfirmationKey)
	online.send(t, http.MethodPost, "/api/auth/register", `{"login": "ada@example.com", "name": "Ada", "password": "ada's password"}`, http.StatusOK, `"token"`)
	token := online.login(t, "ada@example.com", "ada's password")

	status, init := online.rpc(t, token, initialize)
	if status != http.StatusOK || init["result"] == nil {
		t.Fatalf("initialize = %d %v", status, init)
	}
	if got := mustMarshal(t, init["result"].(map[string]any)["serverInfo"]); got != `{"name":"users-ts-agent","version":"1.0.0"}` {
		t.Errorf("initialize names the server %s", got)
	}
	if status, _ := online.rpc(t, token, `{"jsonrpc": "2.0", "method": "notifications/initialized"}`); status != http.StatusAccepted {
		t.Errorf("notifications/initialized = %d, want 202", status)
	}

	_, listed := online.rpc(t, token, `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`)
	var tools []struct {
		Name        string         `json:"name"`
		Title       string         `json:"title"`
		InputSchema map[string]any `json:"inputSchema"`
		Annotations map[string]any `json:"annotations"`
		Meta        map[string]any `json:"_meta"`
	}
	if err := json.Unmarshal([]byte(mustMarshal(t, listed["result"].(map[string]any)["tools"])), &tools); err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, tool := range tools {
		names = append(names, tool.Name)
	}
	if !slices.Equal(names, []string{"greet", "set_motto"}) {
		t.Fatalf("tools/list as a user without staff = %v, want greet and set_motto", names)
	}
	if tools[0].Title != "Greet the caller" || tools[0].Annotations["readOnlyHint"] != true || tools[0].Meta["invocationPolicy"] != "auto" {
		t.Errorf("greet is listed as %+v", tools[0])
	}
	if tools[1].Annotations["readOnlyHint"] != false || tools[1].Meta["invocationPolicy"] != "ask" || mustMarshal(t, tools[1].InputSchema["required"]) != `["motto"]` {
		t.Errorf("set_motto is listed as %+v", tools[1])
	}
	if properties, _ := tools[1].InputSchema["properties"].(map[string]any); properties["confirmationToken"] == nil {
		t.Errorf("set_motto, which asks, takes no confirmationToken: %+v", tools[1].InputSchema)
	}

	greeted := online.toolCall(t, token, "greet", `{}`)
	if testing.Verbose() {
		t.Logf("tools/list: %v; greet: %v", names, greeted)
	}
	if mustMarshal(t, greeted["structuredContent"]) != `{"message":"Hello, Ada"}` || greeted["isError"] != nil {
		t.Errorf("greet answered %v", greeted)
	}
	// set_motto asks: its first call runs nothing and answers a token, and
	// the same call with the token runs, once (D93).
	confirmation := func(arguments string) string {
		t.Helper()
		asked := online.toolCall(t, token, "set_motto", arguments)
		problem, _ := asked["structuredContent"].(map[string]any)
		details, _ := problem["details"].(map[string]any)
		confirmation, _ := details["confirmationToken"].(string)
		if asked["isError"] != true || problem["status"] != float64(428) || problem["code"] != "confirmation_required" || confirmation == "" {
			t.Fatalf("set_motto's first call answered %v, want confirmation_required with a token", asked)
		}
		return confirmation
	}
	withToken := func(arguments, confirmation string) string {
		return strings.TrimSuffix(arguments, "}") + `, "confirmationToken": "` + confirmation + `"}`
	}
	confirmed := confirmation(`{"motto": "Ship it"}`)
	set := online.toolCall(t, token, "set_motto", withToken(`{"motto": "Ship it"}`, confirmed))
	if mustMarshal(t, set["structuredContent"]) != `{"message":"Ada's motto: Ship it"}` {
		t.Errorf("set_motto with its token answered %v", set)
	}
	replayed := online.toolCall(t, token, "set_motto", withToken(`{"motto": "Ship it"}`, confirmed))
	if problem, _ := replayed["structuredContent"].(map[string]any); problem["code"] != "confirmation_required" || mustMarshal(t, problem["details"].(map[string]any)["refused"]) != `"used"` {
		t.Errorf("set_motto with a used token answered %v, want it asked about again", replayed)
	}
	refused := online.toolCall(t, token, "set_motto", withToken(`{"motto": ""}`, confirmation(`{"motto": ""}`)))
	if problem, _ := refused["structuredContent"].(map[string]any); refused["isError"] != true || problem["status"] != float64(400) || problem["code"] != "bad_request" {
		t.Errorf("set_motto with an empty motto answered %v, want the route's 400 as a tool error", refused)
	}
	for _, name := range []string{"forget_motto", "audit", "motto"} {
		_, reply := online.rpc(t, token, `{"jsonrpc": "2.0", "id": 9, "method": "tools/call", "params": {"name": "`+name+`", "arguments": {}}}`)
		if mustMarshal(t, reply["error"]) != `{"code":-32602,"message":"users-ts-agent has no tool \"`+name+`\""}` {
			t.Errorf("tools/call %s answered %v, want the invalid-params error of a tool the API does not offer", name, reply)
		}
	}

	online.authorized(t, token, http.MethodPost, "/api/auth/logout", http.StatusOK, `"data":true`)
	if status, _ := online.rpc(t, token, initialize); status != http.StatusUnauthorized {
		t.Errorf("POST /api/mcp after logout = %d, want 401", status)
	}
	online.stop(t)
}

// TestATypeScriptServerAnswersMCPForOneAPI: the APIs of one TypeScript
// server share its POST /api/mcp, so a server may serve one API with
// outputs.api.mcp on: users-ts-agents-stack's Agents serves users-ts-agent
// beside users-ts-notes, and builds, until users-ts-notes turns mcp on too
// (D81).
func TestATypeScriptServerAnswersMCPForOneAPI(t *testing.T) {
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming())
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	order := []string{"users-db", agentAPI, "users-ts-notes", "users-ts-agents-stack"}
	for _, name := range order {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(identityRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	repoRoot := t.TempDir()
	f.build(t, repoRoot, tsFakePaths(repoRoot), order...)

	notes := *f.configs["users-ts-notes"]
	notes.Outputs = map[string]any{
		"types": map[string]any{"typescript": map[string]any{"enabled": true}},
		"api":   map[string]any{"enabled": true, "language": "TYPESCRIPT", "mcp": true},
	}
	f.configs["users-ts-notes"] = &notes
	repoRoot = t.TempDir()
	options := f.options(repoRoot, tsFakePaths(repoRoot))
	for _, name := range order {
		_, err := generator.Run(f.schemas[name], f.configs[name], options)
		if name != "users-ts-agents-stack" {
			if err != nil {
				t.Fatalf("build %s: %v", name, err)
			}
			continue
		}
		want := "stack users-ts-agents-stack: server Agents serves users-ts-agent and users-ts-notes, which both turn on outputs.api.mcp, and one server answers POST /api/mcp for one API; serve them from two servers, or turn mcp off in one (D81)"
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Fatalf("build %s = %v, want it refused saying %q", name, err, want)
		}
	}
}

const initialize = `{"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {"protocolVersion": "2025-11-25", "capabilities": {}, "clientInfo": {"name": "servergen-test", "version": "1.0.0"}}}`

// rpc posts one JSON-RPC message to the server's MCP endpoint, with the
// bearer token unless it is empty, and returns the status and the message
// it answered: the one event of an event stream, or a JSON body.
func (s *started) rpc(t *testing.T, token, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.base+"/api/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	text := string(answer)
	if resp.Header.Get("Content-Type") == "text/event-stream" {
		data, ok := strings.CutPrefix(strings.TrimSpace(text), "event: message\ndata: ")
		if !ok {
			t.Fatalf("POST /api/mcp answered a stream that is not one message event: %q", text)
		}
		text = data
	}
	var message map[string]any
	if strings.TrimSpace(text) != "" {
		if err := json.Unmarshal([]byte(text), &message); err != nil {
			t.Fatalf("POST /api/mcp answered %d %q: %v", resp.StatusCode, text, err)
		}
	}
	return resp.StatusCode, message
}

// toolCall calls the tool with the arguments and returns its result.
func (s *started) toolCall(t *testing.T, token, name, arguments string) map[string]any {
	t.Helper()
	status, reply := s.rpc(t, token, `{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "`+name+`", "arguments": `+arguments+`}}`)
	result, ok := reply["result"].(map[string]any)
	if status != http.StatusOK || !ok {
		t.Fatalf("tools/call %s = %d %v", name, status, reply)
	}
	return result
}

func mustMarshal(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
