package generator

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/pgtest"
	"github.com/parable-work/superschematic/internal/testpaths"
)

//go:embed testdata/wiring_probe_go.go
var wiringProbe string

// wiringRelayToken is the stand-in SMS relay's token, which signs its
// callbacks.
const wiringRelayToken = "relay-token-wiring-5e1f"

// wiringPhoneAnn is the number Ann verified (testdata/wiring_probe_go.go).
const wiringPhoneAnn = "+15550100001"

// wiringCases are the cases TestGoServerWiresTheRuntimeEndToEnd runs.
var wiringCases = []string{
	"the notifier's worker routes the app's event to its recipient by SMS, and a route's agent recipient's run request is started by the dispatcher's worker",
	"a reply on the relay's callback takes the inbound path and runs as its sender, and an unknown sender's text lands in triage",
	"a grant's token is the caller of the API's routes, the inbox and search, and the indexing worker keeps search current",
	"the agent routes are the dispatcher's, under its endpoint's path",
}

// TestGoServerWiresTheRuntimeEndToEnd builds fixture-agent-runs-go-api and
// its stack as TestGoServerWiresTheRuntime does, with an implementation
// whose NewWiring names the agent endpoint from the API's config, and runs
// the generated server and its four workers as processes on the Postgres
// behaviorServerDatabaseEnv names, in a schema of their own, over a
// stand-in SMS relay that sms-relay-a sends to and signs the callbacks of.
// The app's side, testdata/wiring_probe_go.go, seeds the rows an app
// writes and raises an event; every case of wiringCases must pass. It
// skips without the database and in -short mode.
func TestGoServerWiresTheRuntimeEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the Go server's wiring end to end in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run the Go server's wiring against Postgres")
	}
	repoRoot := testpaths.TempDir(t)
	outputRoot := buildWiringStack(t, repoRoot)
	implFile := filepath.Join(naming.Default().GoImplementationDir(repoRoot, wiringAPI), "implementation.go")
	impl, err := os.ReadFile(implFile)
	if err != nil {
		t.Fatal(err)
	}
	scaffolded := `Agents: agents.Options{Endpoint: "http://localhost:8080/api/agents"},`
	if !strings.Contains(string(impl), scaffolded) {
		t.Fatalf("the scaffold's NewWiring names no agent endpoint:\n%s", impl)
	}
	// The implementation's one change: the endpoint its config names.
	if err := os.WriteFile(implFile, []byte(strings.Replace(string(impl), scaffolded, `Agents: agents.Options{Endpoint: deps.Config.AgentEndpoint},`, 1)), 0o644); err != nil {
		t.Fatal(err)
	}

	serverDir := servergen.ServerDir(outputRoot, wiringStack, wiringServer)
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
	for class, deployable := range wiringWorkers {
		dir := servergen.ServerDir(outputRoot, wiringStack, deployable)
		goIn(t, dir, env, "mod", "tidy")
		goIn(t, dir, env, "build", "-o", filepath.Join(bin, class), ".")
	}

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
	schema := fmt.Sprintf("wiring_go_%d", time.Now().UnixNano())
	createSQL := filepath.Join(t.TempDir(), "create.sql")
	pgtest.WriteCreateSQL(t, filepath.Join(SQLDir(outputRoot, wiringDB), "create.sql"), createSQL)
	probe(dsn, "setup", schema, createSQL)
	t.Cleanup(func() { probe(dsn, "drop", schema) })
	database := withSearchPath(t, dsn, schema)
	var seeded map[string]string
	if err := json.Unmarshal([]byte(probe(database, "seed")), &seeded); err != nil {
		t.Fatal(err)
	}
	rows := func(sql string, args ...string) []map[string]string {
		t.Helper()
		var out []map[string]string
		if err := json.Unmarshal([]byte(probe(database, append([]string{"query", sql}, args...)...)), &out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	relay := newStandInRelay(t)
	port := freeLocalPort(t)
	base := "http://127.0.0.1:" + port
	runEnv := append(os.Environ(),
		"FIXTURE_AGENT_RUNS_DB_DATABASE_URL="+database,
		"SMS=sms-relay-a", "SMS_URL="+relay.server.URL, "SMS_TOKEN="+wiringRelayToken, "SMS_SENDER=+15550109999",
		"AGENT_ENDPOINT="+base+"/api/agents",
	)
	output := &syncBuffer{}
	start := func(name string, extra ...string) *exec.Cmd {
		t.Helper()
		cmd := exec.Command(filepath.Join(bin, name))
		cmd.Env = append(slices.Clone(runEnv), extra...)
		cmd.Stdout, cmd.Stderr = output, output
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = cmd.Process.Kill(); _, _ = cmd.Process.Wait() })
		return cmd
	}
	server := start("server", "PORT="+port)
	for deadline := time.Now().Add(30 * time.Second); ; {
		if status, _ := httpGet(base + "/readyz"); status == http.StatusOK {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the server is not ready within 30s:\n%s", output.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	for class := range wiringWorkers {
		start(class)
	}
	eventually := func(t *testing.T, what string, ok func() bool) {
		t.Helper()
		for deadline := time.Now().Add(30 * time.Second); !ok(); {
			if time.Now().After(deadline) {
				t.Fatalf("%s within 30s:\n%s", what, output.String())
			}
			time.Sleep(200 * time.Millisecond)
		}
	}
	call := func(t *testing.T, method, path, token, body string) (int, string) {
		t.Helper()
		req, err := http.NewRequest(method, base+path, strings.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		if body != "" {
			req.Header.Set("Content-Type", "application/json")
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("%s %s: %v", method, path, err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}
	// callback posts a text from from to the server as the relay does,
	// signed with its token.
	callback := func(t *testing.T, text, from string) (int, string) {
		t.Helper()
		body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"id": fmt.Sprintf("in-%d", time.Now().UnixNano()), "from": from, "text": text, "at": time.Now().UTC()}}})
		if err != nil {
			t.Fatal(err)
		}
		req, err := http.NewRequest(http.MethodPost, base+"/webhooks/sms/inbound", bytes.NewReader(body))
		if err != nil {
			t.Fatal(err)
		}
		mac := hmac.New(sha256.New, []byte(wiringRelayToken))
		mac.Write(body)
		req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
		req.Header.Set("Content-Type", "application/json")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		out, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, string(out)
	}

	t.Run(wiringCases[0], func(t *testing.T) {
		eventually(t, "no text reached Ann", func() bool { return len(relay.sentTo(wiringPhoneAnn)) > 0 })
		notices := rows(`SELECT reference::text AS reference, state FROM notice`)
		if len(notices) != 1 || !strings.Contains(relay.sentTo(wiringPhoneAnn)[0], notices[0]["reference"]) {
			t.Fatalf("the notices %v, and Ann's texts %q", notices, relay.sentTo(wiringPhoneAnn))
		}
		eventually(t, "the dispatcher's worker started no run of the reviewer", func() bool {
			return len(rows(`SELECT r.status FROM agent_run r JOIN agent a ON a.id = r.agent WHERE a.name = 'reviewer' AND r.trigger = 'route'`)) == 1
		})
	})

	t.Run(wiringCases[1], func(t *testing.T) {
		reference := rows(`SELECT reference::text AS reference FROM notice`)[0]["reference"]
		status, body := callback(t, "ack "+reference, wiringPhoneAnn)
		if status != http.StatusOK || !strings.Contains(body, `"outcome":"reply"`) {
			t.Fatalf("Ann's reply = %d %s", status, body)
		}
		if notices := rows(`SELECT state FROM notice`); notices[0]["state"] != "acknowledged" {
			t.Errorf("the notice after Ann's ack: %v", notices)
		}
		status, body = callback(t, "who is this", "+15550100777")
		if status != http.StatusOK || !strings.Contains(body, `"outcome":"unknown"`) || !strings.Contains(body, `"message":"`) {
			t.Errorf("an unknown sender's text = %d %s", status, body)
		}
		if triaged := rows(`SELECT id FROM message WHERE triage = 'open'`); len(triaged) != 1 {
			t.Errorf("the messages in triage: %v", triaged)
		}
		if status, _ := call(t, http.MethodPost, "/webhooks/sms/inbound", "", `{"messages":[]}`); status == http.StatusOK {
			t.Errorf("an unsigned callback was read")
		}
	})

	t.Run(wiringCases[2], func(t *testing.T) {
		token, task := seeded["token"], seeded["task"]
		if status, body := call(t, http.MethodGet, "/api/tasks/"+task, token, ""); status != http.StatusOK || !strings.Contains(body, "Pump overhaul") {
			t.Errorf("the customer's read of its task = %d %s", status, body)
		}
		if status, _ := call(t, http.MethodGet, "/api/tasks/"+task, "", ""); status != http.StatusUnauthorized {
			t.Errorf("an anonymous read of the task = %d, want 401", status)
		}
		if status, body := call(t, http.MethodGet, "/api/inbox", token, ""); status != http.StatusOK || !strings.Contains(body, `"data":[]`) {
			t.Errorf("the customer's inbox = %d %s", status, body)
		}
		if status, _ := call(t, http.MethodGet, "/api/inbox", "", ""); status != http.StatusUnauthorized {
			t.Errorf("an anonymous inbox = %d, want 401", status)
		}
		eventually(t, "search finds no task of the customer's", func() bool {
			status, body := call(t, http.MethodGet, "/api/search?q=pump", token, "")
			return status == http.StatusOK && strings.Contains(body, "Pump overhaul")
		})
		if status, _ := call(t, http.MethodGet, "/api/search?q=pump", "", ""); status != http.StatusUnauthorized {
			t.Errorf("an anonymous search = %d, want 401", status)
		}
	})

	t.Run(wiringCases[3], func(t *testing.T) {
		if status, body := call(t, http.MethodPost, "/api/agents/turn", "", "{}"); status != http.StatusUnauthorized || !strings.Contains(body, "no running run holds this token") {
			t.Errorf("a turn with no run's token = %d %s", status, body)
		}
		if status, _ := call(t, http.MethodPost, "/api/agents/approvals/none/approve", "", ""); status != http.StatusUnauthorized {
			t.Errorf("an anonymous approval = %d, want 401", status)
		}
	})

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
	if strings.Contains(output.String(), wiringRelayToken) {
		t.Errorf("the relay's token reached the logs")
	}
}

// withSearchPath is dsn whose connections search schema, then public.
func withSearchPath(t *testing.T, dsn, schema string) string {
	t.Helper()
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatal(err)
	}
	q := u.Query()
	q.Set("search_path", schema+",public")
	u.RawQuery = q.Encode()
	return u.String()
}

// standInRelay is an SMS relay as sms-relay-a calls it: it takes a text
// with the relay's token and answers its id.
type standInRelay struct {
	server *httptest.Server
	mu     sync.Mutex
	sent   []map[string]string
}

func newStandInRelay(t *testing.T) *standInRelay {
	t.Helper()
	r := &standInRelay{}
	r.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) {
		if req.Header.Get("Authorization") != "Bearer "+wiringRelayToken {
			http.Error(w, "who are you", http.StatusUnauthorized)
			return
		}
		switch req.URL.Path {
		case "/v1/health":
			w.WriteHeader(http.StatusOK)
		case "/v1/messages":
			var message map[string]string
			if err := json.NewDecoder(req.Body).Decode(&message); err != nil {
				http.Error(w, err.Error(), http.StatusBadRequest)
				return
			}
			r.mu.Lock()
			r.sent = append(r.sent, message)
			id := fmt.Sprintf("out-%d", len(r.sent))
			r.mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(map[string]string{"id": id, "status": "sent"})
		default:
			http.NotFound(w, req)
		}
	}))
	t.Cleanup(r.server.Close)
	return r
}

// sentTo are the texts the relay took for to.
func (r *standInRelay) sentTo(to string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, m := range r.sent {
		if m["to"] == to {
			out = append(out, m["text"])
		}
	}
	return out
}
