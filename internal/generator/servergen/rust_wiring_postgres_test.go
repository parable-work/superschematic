package servergen_test

import (
	_ "embed"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/rustrestgen"
	"github.com/parable-work/superschematic/internal/testpaths"
)

// wiredImplementation is the implementation of fixture-wired-api the
// end-to-end test builds its server and workers with.
//
//go:embed testdata/rust-wiring/implementation.rs
var wiredImplementation string

// buildWiredFixture builds the fixture into a repository of its own with
// this checkout's runtimes, writes the test's implementation over the
// scaffold, builds the stack again, which copies it into the output root's
// mirror, and builds the workspace's server and workers with cargo, frozen
// to the lockfile the stack's build wrote, after clippy with warnings
// denied. It returns the repository root and the binaries' directory.
func buildWiredFixture(t *testing.T, cargo string) (string, string) {
	t.Helper()
	return buildWiredFixtureOn(t, cargo, wiredImplementation, "postgres")
}

// buildWiredFixtureOn is buildWiredFixture with implementation as the
// API's and dialects as its database's SQL dialects (#725).
func buildWiredFixtureOn(t *testing.T, cargo, implementation string, dialects ...any) (string, string) {
	t.Helper()
	paths := testpaths.Local(t)
	repoRoot := testpaths.TempDir(t)
	f := loadWiredFixtureOn(t, dialects...)
	f.build(t, repoRoot, paths, wiredOrder...)
	lib := filepath.Join(naming.Default().RustImplementationDir(repoRoot, wiredAPI), rustrestgen.ImplementationLib)
	if err := os.WriteFile(lib, []byte(implementation), 0o644); err != nil {
		t.Fatal(err)
	}
	f.build(t, repoRoot, paths, wiredStack)
	out := filepath.Join(repoRoot, "schemas", "dist")
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "clippy", "--locked", "--workspace", "--quiet", "--", "-D", "warnings")
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	return repoRoot, filepath.Join(target, "debug")
}

// wiredCall is a request to the fixture's server as the user user holding
// permissions, with a bearer token when token is set: its status and its
// body.
func wiredCall(t *testing.T, s *started, method, path, user, permissions, token, body string) (int, string) {
	t.Helper()
	req, err := http.NewRequest(method, s.base+path, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if user != "" {
		req.Header.Set("x-user", user)
		req.Header.Set("x-permissions", permissions)
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(answer)
}

// wiredRPC sends an MCP message to the server's endpoint as the user
// user: its status and the message it answers, none for a notification.
func wiredRPC(t *testing.T, s *started, user, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, s.base+"/api/mcp", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json, text/event-stream")
	req.Header.Set("x-user", user)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	text := string(answer)
	if resp.Header.Get("Content-Type") == "text/event-stream" {
		text, _ = strings.CutPrefix(strings.TrimSpace(text), "event: message\ndata: ")
	}
	var message map[string]any
	if strings.TrimSpace(text) != "" {
		if err := json.Unmarshal([]byte(text), &message); err != nil {
			t.Fatalf("POST /api/mcp answered %d %q: %v", resp.StatusCode, text, err)
		}
	}
	return resp.StatusCode, message
}

// eventually calls check every 200ms until it answers true, for 30s.
func eventually(t *testing.T, what string, logs func() string, check func() bool) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for !check() {
		if time.Now().After(deadline) {
			t.Fatalf("%s did not happen:\n%s", what, logs())
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// runWorker starts the worker binary with env, and stops it with SIGTERM
// when the test ends.
func runWorker(t *testing.T, bin, name string, env []string) *lockedBuffer {
	t.Helper()
	cmd := exec.Command(filepath.Join(bin, name))
	cmd.Env = append(os.Environ(), env...)
	logs := &lockedBuffer{}
	cmd.Stdout, cmd.Stderr = logs, logs
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = cmd.Process.Signal(syscall.SIGTERM)
		done := make(chan struct{})
		go func() { _ = cmd.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			_ = cmd.Process.Kill()
		}
	})
	return logs
}

// TestRustWiringRunsOnPostgres: fixture-wired-api's generated server and
// workers run its database's runtime parts on Postgres with no wiring of
// the implementation's but its Wire (#620, #622, #617, #610), in a schema
// of the test's own that holds fixture-agent-runs-db's tables.
//
//   - The search worker indexes a seeded task, which the server's search
//     route finds as its caller, and so does the MCP endpoint's search
//     tool, which it lists beside the API's work_status.
//   - A review asks the dispatcher for a run: the dispatch worker starts
//     its instance on the local-sandbox provider the environment names,
//     which writes the run's token and exits; the instance reports its
//     outcome to the server's agent routes with that token, and the
//     dispatcher's watch, a second later as the implementation's Wire says,
//     ends the run done.
//   - A warning the implementation raises reaches the approvers: the
//     notify worker routes it to the in-app inbox of the seeded approver,
//     who reads it and acknowledges it through the server's inbox routes.
//   - The server's authenticator is the grants' around the
//     implementation's: a grant's token that no grant holds is refused
//     though the implementation's headers would admit the request, and the
//     agent routes refuse a request with no run's token.
//
// The test needs cargo and the database, and runs outside -short.
func TestRustWiringRunsOnPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	dsn := os.Getenv(queueDatabaseVariable)
	if dsn == "" {
		t.Skipf("set %s to run the generated Rust wiring against Postgres", queueDatabaseVariable)
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	repoRoot, bin := buildWiredFixture(t, cargo)
	out := filepath.Join(repoRoot, "schemas", "dist")
	databaseURL := identitySchema(t, pgxModule(t), dsn, filepath.Join(generator.SQLDir(out, wiredDB), "create.sql"))

	port := freePort(t)
	tokenFile := filepath.Join(t.TempDir(), "run-token")
	env := []string{
		"FIXTURE_AGENT_RUNS_DB_DATABASE_URL=" + libpqSearchPath(t, databaseURL),
		"SANDBOX=local-sandbox",
		"MODEL=stub-model",
		"SMS=sms-relay-a",
		"SMS_URL=http://127.0.0.1:9",
		"SMS_TOKEN=relay-token-of-the-test",
		"WIRED_TOKEN_FILE=" + tokenFile,
		"WIRED_AGENT_ENDPOINT=http://127.0.0.1:" + port + "/api/agents",
	}
	server := startOn(t, port, filepath.Join(bin, wiredAPI), env...)
	logs := func() string { return server.out.String() }

	status, body := wiredCall(t, server, http.MethodPost, "/api/seed", "", "", "", "{}")
	if status != http.StatusOK {
		t.Fatalf("POST /api/seed = %d: %s\n%s", status, body, logs())
	}
	var seeded struct {
		Data struct {
			User string `json:"user"`
			Task string `json:"task"`
		} `json:"data"`
	}
	if err := json.Unmarshal([]byte(body), &seeded); err != nil || seeded.Data.User == "" || seeded.Data.Task == "" {
		t.Fatalf("POST /api/seed answered %s: %v", body, err)
	}
	user := seeded.Data.User

	// The grants' authenticator is the server's.
	if status, body := wiredCall(t, server, http.MethodGet, "/api/status", user, "", "grant_no-such-grant", ""); status != http.StatusUnauthorized {
		t.Errorf("GET /api/status with a grant's token no grant holds = %d, want 401: %s", status, body)
	}
	if status, body := wiredCall(t, server, http.MethodPost, "/api/agents/turn", "", "", "", "{}"); status != http.StatusUnauthorized {
		t.Errorf("POST /api/agents/turn with no run's token = %d, want 401: %s", status, body)
	}

	// Search: the index worker indexes the task, which the route finds.
	index := runWorker(t, bin, "fixture-wired-api-index", env)
	eventually(t, "the search finding the seeded task", func() string { return logs() + index.String() }, func() bool {
		status, body := wiredCall(t, server, http.MethodGet, "/api/search?q=pump", user, "", "", "")
		return status == http.StatusOK && strings.Contains(body, "Pump overhaul")
	})

	// The MCP endpoint serves the search tool beside the routes' tools.
	if status, init := wiredRPC(t, server, user, initialize); status != http.StatusOK || init["result"] == nil {
		t.Fatalf("initialize = %d %v", status, init)
	}
	wiredRPC(t, server, user, `{"jsonrpc": "2.0", "method": "notifications/initialized"}`)
	if _, listed := wiredRPC(t, server, user, `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`); !strings.Contains(mustMarshal(t, listed), `"name":"search"`) || !strings.Contains(mustMarshal(t, listed), `"name":"work_status"`) {
		t.Errorf("tools/list = %v, want search and work_status", listed)
	}
	if _, called := wiredRPC(t, server, user, `{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "search", "arguments": {"q": "pump"}}}`); !strings.Contains(mustMarshal(t, called), "Pump overhaul") {
		t.Errorf("tools/call search = %v, want the seeded task", called)
	}

	// The dispatcher: a review's run starts on the sandbox and ends done.
	if status, body := wiredCall(t, server, http.MethodPost, "/api/tasks/"+seeded.Data.Task+"/review", user, "agents", "", "{}"); status != http.StatusOK {
		t.Fatalf("POST review = %d: %s\n%s", status, body, logs())
	}
	dispatch := runWorker(t, bin, "fixture-wired-api-dispatch", env)
	dispatchLogs := func() string { return logs() + dispatch.String() }
	var token string
	eventually(t, "the run's instance writing its token", dispatchLogs, func() bool {
		data, err := os.ReadFile(tokenFile)
		token = string(data)
		return err == nil && strings.HasPrefix(token, "grant_")
	})
	if status, body := wiredCall(t, server, http.MethodPost, "/api/agents/finish", "", "", token, `{"outcome":"done","summary":"reviewed"}`); status != http.StatusOK || !strings.Contains(body, `"recorded":true`) {
		t.Fatalf("POST /api/agents/finish with the run's token = %d: %s\n%s", status, body, dispatchLogs())
	}
	eventually(t, "the run ending done", dispatchLogs, func() bool {
		status, body := wiredCall(t, server, http.MethodGet, "/api/status", user, "", "", "")
		return status == http.StatusOK && strings.Contains(body, `"ok":true`)
	})

	// The notifier: a warning reaches the approver's inbox, who
	// acknowledges it.
	if status, body := wiredCall(t, server, http.MethodPost, "/api/warn", user, "", "", "{}"); status != http.StatusOK {
		t.Fatalf("POST /api/warn = %d: %s\n%s", status, body, logs())
	}
	notify := runWorker(t, bin, "fixture-wired-api-notify", env)
	var notice string
	eventually(t, "the warning reaching the approver's inbox", func() string { return logs() + notify.String() }, func() bool {
		status, body := wiredCall(t, server, http.MethodGet, "/api/inbox", user, "", "", "")
		if status != http.StatusOK || !strings.Contains(body, "agent.budgetWarning") {
			return false
		}
		var inbox struct {
			Data []map[string]any `json:"data"`
		}
		if json.Unmarshal([]byte(body), &inbox) != nil || len(inbox.Data) == 0 {
			return false
		}
		notice, _ = inbox.Data[0]["id"].(string)
		return notice != ""
	})
	if status, body := wiredCall(t, server, http.MethodPost, "/api/inbox/"+notice+"/ack", user, "", "", "{}"); status != http.StatusNoContent {
		t.Fatalf("POST ack = %d: %s", status, body)
	}
	if status, body := wiredCall(t, server, http.MethodGet, "/api/inbox?state=acknowledged", user, "", "", ""); status != http.StatusOK || !strings.Contains(body, notice) {
		t.Errorf("GET the acknowledged notices = %d: %s", status, body)
	}
	if status, body := wiredCall(t, server, http.MethodGet, "/api/inbox", "", "", "", ""); status != http.StatusUnauthorized {
		t.Errorf("GET /api/inbox with no caller = %d, want 401: %s", status, body)
	}

	// No secret reaches a log.
	for _, text := range []string{logs(), index.String(), dispatch.String(), notify.String()} {
		for _, secret := range []string{token, "relay-token-of-the-test"} {
			if secret != "" && strings.Contains(text, secret) {
				t.Errorf("a log holds a secret:\n%s", text)
			}
		}
	}
}
