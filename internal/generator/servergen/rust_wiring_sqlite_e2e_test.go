package servergen_test

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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
)

// wiredSQLiteImplementation is the implementation of fixture-wired-api the
// end-to-end test on a SQLite file builds its server and workers with.
//
//go:embed testdata/rust-wiring/implementation_sqlite.rs
var wiredSQLiteImplementation string

// wiredRelayToken is the SMS relay's token, which signs its callbacks;
// wiredPhoneAnn the number Ann verified
// (testdata/rust-wiring/implementation_sqlite.rs).
const (
	wiredRelayToken = "relay-token-wired-sqlite-7c2d"
	wiredPhoneAnn   = "+15550100001"
)

// TestRustWiringRunsOnSQLite is TestRustWiringRunsOnPostgres over one
// SQLite file, with no Postgres (#725): fixture-wired-api's generated
// server and its four workers (notify, dispatch, inbound-runs and index)
// run fixture-agent-runs-db's runtime parts over the file the build's
// SQLite DDL made, with no wiring of the implementation's but its Wire.
//
//   - A grant's token is the caller of the API's routes and of search:
//     the customer the seed grants the task to reads the API's status and
//     finds the task, which the index worker indexed; an unknown grant's
//     token, and no caller, are refused. The MCP endpoint lists and calls
//     the search tool.
//   - A review asks the dispatcher for a run: the dispatch worker starts
//     its instance on local-sandbox, which writes the run's token; the
//     instance reports its outcome to the server's agent routes with it,
//     and the run is written done.
//   - A warning the implementation raises is routed by the notify worker
//     to Ann, whose preference sends it by SMS to her verified phone
//     (sms-relay-a's Rust crate sends through the process's outbox), and
//     her inbox holds it with its reference; Ann's "ack <reference>" on
//     the relay's signed callback takes the inbound path and acknowledges
//     the notice, an unknown sender's text lands in triage, and an
//     unsigned callback is refused.
//   - No secret reaches a log.
//
// The test needs cargo, and runs outside -short.
func TestRustWiringRunsOnSQLite(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cargo build in -short mode")
	}
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available; skipping the Rust build")
	}
	repoRoot, bin := buildWiredFixtureOn(t, cargo, wiredSQLiteImplementation, "sqlite", "postgres")
	out := filepath.Join(repoRoot, "schemas", "dist")
	ddl := filepath.Join(generator.SQLDir(out, wiredDB), generator.SQLiteSubdir, "create.sql")
	if _, err := os.Stat(ddl); err != nil {
		t.Fatalf("the build wrote no SQLite DDL for %s: %v", wiredDB, err)
	}
	dir := t.TempDir()
	file := filepath.Join(dir, wiredDB+".sqlite")
	if err := os.WriteFile(file, nil, 0o644); err != nil {
		t.Fatal(err)
	}

	port := freePort(t)
	tokenFile, grantFile := filepath.Join(dir, "run-token"), filepath.Join(dir, "grant-token")
	env := []string{
		"FIXTURE_AGENT_RUNS_DB_DATABASE_URL=sqlite:" + file,
		"SANDBOX=local-sandbox",
		"MODEL=stub-model",
		"SMS=sms-relay-a",
		"SMS_URL=http://127.0.0.1:9",
		"SMS_TOKEN=" + wiredRelayToken,
		"SMS_SENDER=+15550109999",
		"WIRED_SQLITE_DDL=" + ddl,
		"WIRED_TOKEN_FILE=" + tokenFile,
		"WIRED_GRANT_FILE=" + grantFile,
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
	ann := seeded.Data.User
	grant, err := os.ReadFile(grantFile)
	if err != nil || !strings.HasPrefix(string(grant), "grant_") {
		t.Fatalf("the seed wrote no grant token: %q %v", grant, err)
	}
	grantToken := string(grant)

	index := runWorker(t, bin, "fixture-wired-api-index", env)
	notify := runWorker(t, bin, "fixture-wired-api-notify", env)
	dispatch := runWorker(t, bin, "fixture-wired-api-dispatch", env)
	runs := runWorker(t, bin, "fixture-wired-api-inbound-runs", env)
	all := func() string {
		return logs() + index.String() + notify.String() + dispatch.String() + runs.String()
	}

	t.Run("a grant's token is the caller of the API's routes and of search, which the index worker keeps current", func(t *testing.T) {
		if status, body := wiredCall(t, server, http.MethodGet, "/api/status", "", "", grantToken, ""); status != http.StatusOK {
			t.Errorf("GET /api/status with the customer's grant = %d, want 200: %s", status, body)
		}
		if status, body := wiredCall(t, server, http.MethodGet, "/api/status", ann, "", "grant_no-such-grant", ""); status != http.StatusUnauthorized {
			t.Errorf("GET /api/status with a grant's token no grant holds = %d, want 401: %s", status, body)
		}
		if status, _ := wiredCall(t, server, http.MethodGet, "/api/status", "", "", "", ""); status != http.StatusUnauthorized {
			t.Errorf("GET /api/status with no caller = %d, want 401", status)
		}
		eventually(t, "the customer's search finding the seeded task", all, func() bool {
			status, body := wiredCall(t, server, http.MethodGet, "/api/search?q=pump", "", "", grantToken, "")
			return status == http.StatusOK && strings.Contains(body, "Pump overhaul")
		})
		if status, _ := wiredCall(t, server, http.MethodGet, "/api/search?q=pump", "", "", "", ""); status != http.StatusUnauthorized {
			t.Errorf("an anonymous search = %d, want 401", status)
		}
		if status, init := wiredRPC(t, server, ann, initialize); status != http.StatusOK || init["result"] == nil {
			t.Fatalf("initialize = %d %v", status, init)
		}
		wiredRPC(t, server, ann, `{"jsonrpc": "2.0", "method": "notifications/initialized"}`)
		if _, listed := wiredRPC(t, server, ann, `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`); !strings.Contains(mustMarshal(t, listed), `"name":"search"`) {
			t.Errorf("tools/list = %v, want search", listed)
		}
		if _, called := wiredRPC(t, server, ann, `{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "search", "arguments": {"q": "pump"}}}`); !strings.Contains(mustMarshal(t, called), "Pump overhaul") {
			t.Errorf("tools/call search = %v, want the seeded task", called)
		}
	})

	t.Run("the dispatch worker starts a review's run, whose instance ends it done through the agent routes", func(t *testing.T) {
		if status, body := wiredCall(t, server, http.MethodPost, "/api/agents/turn", "", "", "", "{}"); status != http.StatusUnauthorized {
			t.Errorf("POST /api/agents/turn with no run's token = %d, want 401: %s", status, body)
		}
		if status, body := wiredCall(t, server, http.MethodPost, "/api/tasks/"+seeded.Data.Task+"/review", ann, "agents", "", "{}"); status != http.StatusOK {
			t.Fatalf("POST review = %d: %s\n%s", status, body, all())
		}
		var token string
		eventually(t, "the run's instance writing its token", all, func() bool {
			data, err := os.ReadFile(tokenFile)
			token = string(data)
			return err == nil && strings.HasPrefix(token, "grant_")
		})
		if status, body := wiredCall(t, server, http.MethodPost, "/api/agents/finish", "", "", token, `{"outcome":"done","summary":"reviewed"}`); status != http.StatusOK || !strings.Contains(body, `"recorded":true`) {
			t.Fatalf("POST /api/agents/finish with the run's token = %d: %s\n%s", status, body, all())
		}
		eventually(t, "the run written done", all, func() bool {
			status, body := wiredCall(t, server, http.MethodGet, "/api/status", ann, "", "", "")
			return status == http.StatusOK && strings.Contains(body, `"ok":true`)
		})
		for _, secret := range []string{token} {
			if strings.Contains(all(), secret) {
				t.Errorf("a log holds the run's token")
			}
		}
	})

	t.Run("the notify worker routes a warning to Ann, whose ack on the relay's callback lands, and an unknown sender's text lands in triage", func(t *testing.T) {
		if status, body := wiredCall(t, server, http.MethodPost, "/api/warn", ann, "", "", "{}"); status != http.StatusOK {
			t.Fatalf("POST /api/warn = %d: %s\n%s", status, body, all())
		}
		var reference string
		eventually(t, "the warning reaching Ann", all, func() bool {
			status, body := wiredCall(t, server, http.MethodGet, "/api/inbox", ann, "", "", "")
			var inbox struct {
				Data []map[string]any `json:"data"`
			}
			if status != http.StatusOK || json.Unmarshal([]byte(body), &inbox) != nil || len(inbox.Data) == 0 || inbox.Data[0]["eventKind"] != "agent.budgetWarning" {
				return false
			}
			reference = fmt.Sprint(inbox.Data[0]["reference"])
			return reference != ""
		})
		status, body := relayCallback(t, server.base, "ack "+reference, wiredPhoneAnn)
		if status != http.StatusOK || !strings.Contains(body, `"outcome":"reply"`) {
			t.Fatalf("Ann's ack = %d %s\n%s", status, body, all())
		}
		if status, body := wiredCall(t, server, http.MethodGet, "/api/inbox?state=acknowledged", ann, "", "", ""); status != http.StatusOK || !strings.Contains(body, "agent.budgetWarning") {
			t.Errorf("Ann's acknowledged notices = %d: %s", status, body)
		}
		status, body = relayCallback(t, server.base, "who is this", "+15550100777")
		if status != http.StatusOK || !strings.Contains(body, `"outcome":"unknown"`) || !strings.Contains(body, `"message":"`) {
			t.Errorf("an unknown sender's text = %d %s", status, body)
		}
		if status, _ := wiredCall(t, server, http.MethodPost, "/webhooks/sms/inbound", "", "", "", `{"messages":[]}`); status == http.StatusOK {
			t.Errorf("an unsigned callback was read")
		}
	})

	for _, text := range []string{logs(), index.String(), notify.String(), dispatch.String(), runs.String()} {
		for _, secret := range []string{grantToken, wiredRelayToken} {
			if strings.Contains(text, secret) {
				t.Errorf("a log holds a secret:\n%s", text)
			}
		}
	}
}

// relayCallback posts a text from from to the server at base as the SMS
// relay does, signed with its token: the status and the body.
func relayCallback(t *testing.T, base, text, from string) (int, string) {
	t.Helper()
	body, err := json.Marshal(map[string]any{"messages": []any{map[string]any{"id": fmt.Sprintf("in-%d", time.Now().UnixNano()), "from": from, "text": text, "at": time.Now().UTC()}}})
	if err != nil {
		t.Fatal(err)
	}
	req, err := http.NewRequest(http.MethodPost, base+"/webhooks/sms/inbound", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	mac := hmac.New(sha256.New, []byte(wiredRelayToken))
	mac.Write(body)
	req.Header.Set("X-Relay-Signature", hex.EncodeToString(mac.Sum(nil)))
	req.Header.Set("Content-Type", "application/json")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	answer, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(answer)
}
