package servergen_test

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/testpaths"
	ir "github.com/parable-work/superschematic/ir"
	publicregistry "github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// engineTrioStack serves desk-db's three APIs, desk-api in Go, desk-rs-api
// in Rust and desk-ts-api in TypeScript, each from its default server over
// the one engine database, beside one runner of desk-db, in the Go of
// desk-api, the first of them by name (D58).
const engineTrioStack = "desk-trio-stack"

// engineTrioOrder is the order a build-all builds the three servers' fixture
// in.
var engineTrioOrder = []string{"desk-db", engineServer, rustEngineServer, engineTSServer, engineTrioStack}

// engineScriptDumpEnv names a directory the script test writes each
// server's transcript into, as JSON, when it is set: what to read when the
// transcripts differ.
const engineScriptDumpEnv = "SUPERSCHEMATIC_ENGINE_SCRIPT_DUMP"

// loadEngineTrioFixture loads the three servers' fixture with the core
// registry and stacktest's fake target, as loadEngineFixture does.
func loadEngineTrioFixture(t *testing.T) fixture {
	t.Helper()
	reg, err := publicregistry.Assemble(publicregistry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	f := fixture{reg: reg, schemas: map[string]*ir.Schema{}, configs: map[string]*schemaconfig.SchemaConfig{}}
	for _, name := range engineTrioOrder {
		schema, cfg, err := loader.LoadServiceWithConfig(filepath.Join(engineRoot, name), loader.WithRegistry(reg))
		if err != nil {
			t.Fatalf("load %s: %v", name, err)
		}
		f.schemas[name], f.configs[name] = schema, cfg
	}
	return f
}

// TestTheEngineRunnerOfThreeServersIsTheFirstAPIs: desk-trio-stack resolves
// one runner of desk-db, in the Go of desk-api, the first of desk-db's three
// APIs by name, whatever language the others are written in (D58).
func TestTheEngineRunnerOfThreeServersIsTheFirstAPIs(t *testing.T) {
	repoRoot := t.TempDir()
	f := loadEngineTrioFixture(t)
	paths := rustFakePaths(repoRoot)
	ts := tsEngineFakePaths(repoRoot)
	paths.ScalarTypeScript, paths.HTTPRuntimeTypeScript, paths.EngineTypeScript = ts.ScalarTypeScript, ts.HTTPRuntimeTypeScript, ts.EngineTypeScript
	f.build(t, repoRoot, paths, engineTrioOrder...)
	data, err := os.ReadFile(filepath.Join(repoRoot, "schemas", "dist", "stack", engineTrioStack, "Local", "environment.json"))
	if err != nil {
		t.Fatal(err)
	}
	var env ir.ResolvedEnvironment
	if err := json.Unmarshal(data, &env); err != nil {
		t.Fatal(err)
	}
	var runners []string
	for _, d := range env.Deployables {
		if d.Kind == ir.DeployableRunner {
			runners = append(runners, d.Name)
		}
	}
	runner := env.Deployable(engineRunner)
	if len(runners) != 1 || runner == nil || runner.Language != "GO" || runner.Runner == nil || runner.Runner.API != engineServer {
		t.Fatalf("the runners are %v, %s's %+v", runners, engineRunner, runner)
	}
	for _, api := range []string{engineServer, rustEngineServer, engineTSServer} {
		if env.Deployable(api) == nil {
			t.Errorf("the stack deploys no %s", api)
		}
	}
}

// engineTrio is the three servers, built, and the runner beside them.
type engineTrio struct {
	out string
	// goServer, rustServer and runner are binaries; tsServer is the
	// directory of the TypeScript server's package, which Bun runs.
	goServer, rustServer, tsServer, runner string
	bun                                    string
	// goDir is the Go server's module, which requires pgx: the database
	// tool runs in it.
	goDir string
}

// buildEngineTrio builds desk-trio-stack's three servers and its runner, or
// skips when a toolchain or a built runtime one of them needs is missing.
func buildEngineTrio(t *testing.T) engineTrio {
	t.Helper()
	cargo, err := exec.LookPath("cargo")
	if err != nil {
		t.Skip("cargo not available: the Rust server cannot be built")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun not available: %v", err))
	}
	paths := testpaths.Local(t)
	archive := filepath.Join(paths.EngineGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_engine.a")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("the engine's archive is not built (run scripts/engine-archive.sh): %v", err)
	}
	requireEngineTypeScript(t, paths.EngineTypeScript)
	installTypeScriptRuntime(t, bun, paths)

	repoRoot := testpaths.TempDir(t)
	f := loadEngineTrioFixture(t)
	f.build(t, repoRoot, paths, engineTrioOrder...)
	out := filepath.Join(repoRoot, "schemas", "dist")
	trio := engineTrio{out: out, bun: bun}

	trio.goDir = servergen.ServerDir(out, engineTrioStack, engineServer)
	goCommand(t, trio.goDir, "mod", "tidy")
	trio.goServer = filepath.Join(t.TempDir(), engineServer)
	goCommand(t, trio.goDir, "build", "-o", trio.goServer, ".")
	runnerDir := servergen.ServerDir(out, engineTrioStack, engineRunner)
	goCommand(t, runnerDir, "mod", "tidy")
	trio.runner = filepath.Join(t.TempDir(), engineRunner)
	goCommand(t, runnerDir, "build", "-o", trio.runner, ".")

	if _, err := os.Stat(filepath.Join(out, servergen.RustLockfileName)); err != nil {
		t.Fatalf("the stack's build wrote no lockfile: %v", err)
	}
	target := rustTargetDir(t)
	cargoCommand(t, cargo, out, target, "build", "--locked", "--workspace", "--quiet")
	trio.rustServer = filepath.Join(target, "debug", rustEngineServer)

	install := exec.Command(bun, "install")
	install.Dir = out
	if output, err := install.CombinedOutput(); err != nil {
		testpaths.RequireOrSkipTS(t, fmt.Sprintf("bun install failed at the output root (likely offline): %v\n%s", err, output))
	}
	trio.tsServer = servergen.ServerDir(out, engineTrioStack, engineTSServer)
	return trio
}

// scriptServer is one of the servers the script runs against.
type scriptServer struct {
	// api is the API it serves, language its language and tag the text the
	// script writes into the ids, the slug and the login it chooses, which
	// the transcript writes {{run}}: the three runs share one namespace.
	api, language, tag string
	s                  *started
	// token is the session the script's member signed in with on it.
	token string
}

// startEngineTrio starts the three servers with env, each with its
// identity config and its callers field, on the database at databaseURL.
func startEngineTrio(t *testing.T, trio engineTrio, databaseURL string, public ed25519.PublicKey) []*scriptServer {
	t.Helper()
	identityConfig := `{"trustedOrigins": ["https://app.example.com"], "cookie": {"secure": false}, "password": {"argon2": {"memoryKiB": 64, "iterations": 1, "parallelism": 1}}}`
	env := func(api string) []string {
		return append([]string{"DESK_DB_DATABASE_URL=" + databaseURL, ir.IdentityConfigField(api) + "=" + identityConfig}, opsCallersVariables(t, api, public, "ops-key")...)
	}
	return []*scriptServer{
		{api: engineServer, language: "Go", tag: "run1", s: start(t, trio.goServer, env(engineServer)...)},
		{api: rustEngineServer, language: "Rust", tag: "run2", s: start(t, trio.rustServer, env(rustEngineServer)...)},
		{api: engineTSServer, language: "TypeScript", tag: "run3", s: bunServer(t, trio.bun, trio.tsServer, env(engineTSServer)...)},
	}
}

// TestEngineServersAnswerOneScriptAlike is D58's proof: a Go, a Rust and a
// TypeScript server, generated from desk-db's three APIs in one stack, each
// mounting the engine at /engine/desk-db, answer one script alike. Offline,
// each answers what needs no database as the others do. On the Postgres
// engineDatabaseEnv names, the three open one database at once, so each
// deploys the same engine document into the one engine schema, and the
// script runs against each in turn: a member registers and signs in, is
// refused a write their roles lack and, granted Account and Ticket, creates
// an account, reads and looks it up, opens tickets linked to it, moves one
// through its workflow with If-Match, comments on it and lists its comments,
// patches and resolves another, lists the tickets with a filter and in
// pages, reads the event log as JSON and as an event stream from the head,
// resumes the stream from its Last-Event-ID, speaks MCP (initialize,
// tools/list, tools/call), is refused by the engine's checks, and a service
// stands in for an end user and is refused; then the runner, started beside
// them, fires the deadline of the ticket left open, which the server reads
// back escalated. Each server's transcript, normalised as normaliseScript
// says, must be the others', and where a step is one the HTTP corpus
// (runtime/engine/testdata/http) records, it must answer in the corpus's
// shape. The servers interoperate too: what one wrote another reads, and an
// event one commits shows on another's stream.
func TestEngineServersAnswerOneScriptAlike(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the three servers' builds in -short mode")
	}
	trio := buildEngineTrio(t)
	public, key, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}

	offline := startEngineTrio(t, trio, "postgres://desk@127.0.0.1:9/desk_db?connect_timeout=1&sslmode=disable", public)
	transcripts := map[string][]scriptExchange{}
	for _, server := range offline {
		transcripts[server.language] = runOfflineScript(t, server)
		server.s.stop(t)
	}
	compareTranscripts(t, "offline", offline, transcripts)

	postgres := os.Getenv(engineDatabaseEnv)
	if postgres == "" {
		t.Logf("%s is unset: the servers serve no database, so the script runs offline alone", engineDatabaseEnv)
		return
	}
	tool := engineDatabaseTool(t, trio.goDir)
	databaseURL := tool.create(t, postgres, filepath.Join(generator.SQLDir(trio.out, "desk-db"), "create.sql"))
	servers := startEngineTrio(t, trio, databaseURL, public)
	for _, server := range servers {
		server.s.waitReady(t)
	}
	transcripts = map[string][]scriptExchange{}
	for _, server := range servers {
		transcripts[server.language] = runEngineScript(t, server, key, func(login string) {
			tool.grant(t, databaseURL, login, "desk-"+server.tag, "Account", "Ticket")
		}, func() *started {
			return startBackground(t, trio.runner, "DESK_DB_DATABASE_URL="+databaseURL)
		})
	}
	compareTranscripts(t, "online", servers, transcripts)
	checkInterop(t, servers)
	for _, server := range servers {
		server.s.stop(t)
	}
}

// scriptExchange is one request of the script and its answer, as a
// transcript records it.
type scriptExchange struct {
	Step     string         `json:"step"`
	Request  scriptRequest  `json:"request"`
	Response scriptResponse `json:"response"`
}

// scriptRequest is what the script sent: who it sent it as (a member's
// session, a service's credential, both, a forged credential or none), the
// headers the engine reads beside those, and the body, parsed.
type scriptRequest struct {
	Method  string            `json:"method"`
	Path    string            `json:"path"`
	As      string            `json:"as,omitempty"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
}

// scriptResponse is what the server answered: the status, the headers the
// corpus records (scriptResponseHeaders), and the body parsed as JSON, or
// the frames of an event stream the script took.
type scriptResponse struct {
	Status  int               `json:"status"`
	Headers map[string]string `json:"headers,omitempty"`
	Body    any               `json:"body,omitempty"`
	Frames  []sseFrame        `json:"frames,omitempty"`
}

// sseFrame is a server-sent event as a client takes it: a comment, or an
// event's name, id and data, parsed when it is JSON.
type sseFrame struct {
	Comment string  `json:"comment,omitempty"`
	Event   string  `json:"event,omitempty"`
	ID      *string `json:"id,omitempty"`
	Data    any     `json:"data,omitempty"`
}

// scriptResponseHeaders are the response headers a transcript records, the
// HTTP corpus's (runtime/engine/testdata/http/README.md) and the CORS
// headers of a preflight.
var scriptResponseHeaders = []string{
	"access-control-allow-credentials", "access-control-allow-headers", "access-control-allow-methods", "access-control-allow-origin",
	"allow", "cache-control", "content-type", "etag", "location", "mcp-session-id", "retry-after", "www-authenticate", "x-request-id",
}

// scriptRequestHeaders are the request headers a transcript records beside
// the credential, which it records as who the request was sent as.
var scriptRequestHeaders = []string{"accept", "content-type", "if-match", "last-event-id", "mcp-protocol-version", "origin", "access-control-request-method", "x-request-id"}

// scriptClient sends the script's requests to one server and records each
// exchange.
type scriptClient struct {
	t      *testing.T
	server *scriptServer
	// requests numbers the request ids the script sends, so that a server
	// that takes the request's own id answers with it.
	requests   int
	transcript []scriptExchange
}

// call is one request of the script.
type call struct {
	step   string
	method string
	path   string
	// as is who the request is sent as, by name, and headers the
	// credential headers that make it so.
	as      string
	headers map[string]string
	body    string
	status  int
}

// do sends c, records the exchange and returns the answer's body, parsed.
func (sc *scriptClient) do(c call) any {
	sc.t.Helper()
	resp, exchange := sc.send(c)
	defer resp.Body.Close()
	data, _ := io.ReadAll(resp.Body)
	if len(data) > 0 {
		var body any
		if strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
			exchange.Response.Frames = parseFrames(sc.t, data)
		} else if err := json.Unmarshal(data, &body); err != nil {
			exchange.Response.Body = map[string]any{"text": string(data)}
		} else {
			exchange.Response.Body = body
		}
	}
	sc.check(c, exchange)
	sc.transcript = append(sc.transcript, exchange)
	if exchange.Response.Frames != nil {
		return exchange.Response.Frames
	}
	return exchange.Response.Body
}

// send sends c and returns the response, its body unread, and the exchange
// so far.
func (sc *scriptClient) send(c call) (*http.Response, scriptExchange) {
	sc.t.Helper()
	var reader io.Reader
	if c.body != "" {
		reader = strings.NewReader(c.body)
	}
	req, err := http.NewRequest(c.method, sc.server.s.base+c.path, reader)
	if err != nil {
		sc.t.Fatal(err)
	}
	sc.requests++
	headers := map[string]string{"x-request-id": fmt.Sprintf("script-%d", sc.requests)}
	if c.body != "" {
		headers["content-type"] = "application/json"
	}
	for name, value := range c.headers {
		headers[strings.ToLower(name)] = value
	}
	exchange := scriptExchange{Step: c.step, Request: scriptRequest{Method: c.method, Path: c.path, As: c.as}}
	for name, value := range headers {
		req.Header.Set(name, value)
		if slices.Contains(scriptRequestHeaders, name) {
			if exchange.Request.Headers == nil {
				exchange.Request.Headers = map[string]string{}
			}
			exchange.Request.Headers[name] = value
		}
	}
	if c.body != "" {
		var body any
		if err := json.Unmarshal([]byte(c.body), &body); err != nil {
			sc.t.Fatalf("the script's body for %s is not JSON: %v", c.step, err)
		}
		exchange.Request.Body = body
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		sc.t.Fatalf("%s: %s %s on %s: %v", c.step, c.method, c.path, sc.server.language, err)
	}
	exchange.Response.Status = resp.StatusCode
	for _, name := range scriptResponseHeaders {
		if value := resp.Header.Values(name); len(value) > 0 {
			if exchange.Response.Headers == nil {
				exchange.Response.Headers = map[string]string{}
			}
			exchange.Response.Headers[name] = strings.Join(value, ", ")
		}
	}
	return resp, exchange
}

// check fails the test when the exchange's status is not the one the script
// expects.
func (sc *scriptClient) check(c call, exchange scriptExchange) {
	sc.t.Helper()
	if c.status != 0 && exchange.Response.Status != c.status {
		answer, _ := json.Marshal(exchange.Response)
		sc.t.Errorf("%s on the %s server: %s %s = %d, want %d: %s", c.step, sc.server.language, c.method, c.path, exchange.Response.Status, c.status, answer)
	}
}

// parseFrames reads the server-sent events of a complete stream.
func parseFrames(t *testing.T, data []byte) []sseFrame {
	t.Helper()
	var frames []sseFrame
	reader := newFrameReader(io.NopCloser(bytes.NewReader(data)))
	for {
		frame, ok := reader.next(time.Second)
		if !ok {
			return frames
		}
		frames = append(frames, frame)
	}
}

// frameReader reads the server-sent events of a stream as they come.
type frameReader struct {
	body   io.ReadCloser
	frames chan sseFrame
}

func newFrameReader(body io.ReadCloser) *frameReader {
	r := &frameReader{body: body, frames: make(chan sseFrame, 64)}
	go func() {
		defer close(r.frames)
		lines := bufio.NewScanner(body)
		lines.Buffer(make([]byte, 1<<20), 1<<24)
		var frame sseFrame
		var data []string
		empty := true
		for lines.Scan() {
			line := lines.Text()
			if line == "" {
				if !empty {
					if data != nil {
						joined := strings.Join(data, "\n")
						var parsed any
						if err := json.Unmarshal([]byte(joined), &parsed); err == nil {
							frame.Data = parsed
						} else {
							frame.Data = map[string]any{"text": joined}
						}
					}
					r.frames <- frame
				}
				frame, data, empty = sseFrame{}, nil, true
				continue
			}
			field, value, _ := strings.Cut(line, ":")
			value = strings.TrimPrefix(value, " ")
			empty = false
			switch field {
			case "":
				frame.Comment = value
			case "event":
				frame.Event = value
			case "id":
				id := value
				frame.ID = &id
			case "data":
				data = append(data, value)
			}
		}
		if !empty {
			r.frames <- frame
		}
	}()
	return r
}

// next is the stream's next frame but a heartbeat's keepalive comment,
// which a transcript leaves out since how many come is the test's timing,
// and false when the stream ended or nothing came within wait.
func (r *frameReader) next(wait time.Duration) (sseFrame, bool) {
	timer := time.NewTimer(wait)
	defer timer.Stop()
	for {
		select {
		case frame, ok := <-r.frames:
			if !ok {
				return sseFrame{}, false
			}
			if frame.Comment == "keepalive" && frame.Event == "" && frame.ID == nil && frame.Data == nil {
				continue
			}
			return frame, true
		case <-timer.C:
			return sseFrame{}, false
		}
	}
}

// openStream sends c, an event stream's request, records the exchange and
// takes frames until one is the ready event, returning the stream to read
// on; stream ends it and records what it took.
func (sc *scriptClient) openStream(c call) *scriptStream {
	sc.t.Helper()
	resp, exchange := sc.send(c)
	sc.check(c, exchange)
	stream := &scriptStream{sc: sc, resp: resp, reader: newFrameReader(resp.Body), index: len(sc.transcript)}
	sc.transcript = append(sc.transcript, exchange)
	if resp.StatusCode != http.StatusOK {
		data, _ := io.ReadAll(resp.Body)
		var body any
		if json.Unmarshal(data, &body) == nil {
			sc.transcript[stream.index].Response.Body = body
		}
		return stream
	}
	stream.take(func(f sseFrame) bool { return f.Event == "ready" })
	return stream
}

// scriptStream is an event stream the script holds open.
type scriptStream struct {
	sc     *scriptClient
	resp   *http.Response
	reader *frameReader
	index  int
}

// take reads frames into the stream's exchange until until is true of one,
// failing the test when none comes within ten seconds.
func (s *scriptStream) take(until func(sseFrame) bool) {
	s.sc.t.Helper()
	for {
		frame, ok := s.reader.next(10 * time.Second)
		if !ok {
			taken, _ := json.Marshal(s.sc.transcript[s.index].Response.Frames)
			s.sc.t.Fatalf("%s on the %s server: the stream ended or went quiet before the frame the script waits for, having sent %s", s.sc.transcript[s.index].Step, s.sc.server.language, taken)
		}
		s.sc.transcript[s.index].Response.Frames = append(s.sc.transcript[s.index].Response.Frames, frame)
		if until(frame) {
			return
		}
	}
}

// close ends the stream from the client's side.
func (s *scriptStream) close() {
	s.resp.Body.Close()
}


// engineMount is where each server mounts desk-db's engine, and
// engineNamespace the namespace its routes serve.
const (
	engineMount     = "/engine/desk-db"
	engineNamespace = engineMount + "/namespaces/desk-db"
)

// runOfflineScript runs what each server answers with no database up: its
// readiness names the engine, the engine's routes and its MCP endpoint
// answer 503 unavailable, before any authentication, and a trusted origin's
// preflight of an engine route gets the identity runtime's CORS.
func runOfflineScript(t *testing.T, server *scriptServer) []scriptExchange {
	t.Helper()
	sc := &scriptClient{t: t, server: server}
	sc.do(call{step: "the engine is not ready", method: http.MethodGet, path: "/readyz", status: http.StatusServiceUnavailable})
	sc.do(call{step: "a route before the engine opens", method: http.MethodGet, path: engineNamespace + "/schemas", status: http.StatusServiceUnavailable})
	sc.do(call{step: "a write before the engine opens", method: http.MethodPost, path: engineNamespace + "/schemas/Ticket/instances", body: `{"data": {"title": "Early"}}`, status: http.StatusServiceUnavailable})
	sc.do(call{step: "MCP before the engine opens", method: http.MethodPost, path: engineNamespace + "/mcp",
		headers: map[string]string{"Accept": "application/json, text/event-stream"},
		body:    `{"jsonrpc": "2.0", "id": 1, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "script", "version": "1"}}}`,
		status:  http.StatusServiceUnavailable})
	sc.do(call{step: "a trusted origin's preflight", method: http.MethodOptions, path: engineNamespace + "/schemas/Ticket/instances",
		headers: map[string]string{"Origin": "https://app.example.com", "Access-Control-Request-Method": http.MethodPost}, status: http.StatusNoContent})
	return sc.transcript
}

// credentials are the headers that send a request as each of the script's
// callers.
type credentials map[string]map[string]string

// runEngineScript runs the script against server: grant gives the member
// whose login it is the role the script needs, out of band, as an
// administrator would, and startRunner starts the engine's runner.
func runEngineScript(t *testing.T, server *scriptServer, key ed25519.PrivateKey, grant func(login string), startRunner func() *started) []scriptExchange {
	t.Helper()
	sc := &scriptClient{t: t, server: server}
	tag := server.tag
	login := "ada." + tag + "@example.com"
	accounts := engineNamespace + "/schemas/Account/instances"
	tickets := engineNamespace + "/schemas/Ticket/instances"
	account, t1, t2, t3 := "acme-"+tag, "t1-"+tag, "t2-"+tag, "t3-"+tag

	sc.do(call{step: "register", method: http.MethodPost, path: "/api/auth/register",
		body: fmt.Sprintf(`{"login": %q, "name": "Ada", "password": "ada's password"}`, login), status: http.StatusOK})
	signedIn := sc.do(call{step: "sign in", method: http.MethodPost, path: "/api/auth/login",
		body: fmt.Sprintf(`{"login": %q, "password": "ada's password"}`, login), status: http.StatusOK})
	token, _ := member(signedIn, "data", "token").(string)
	if token == "" {
		t.Fatalf("signing in on the %s server gave no token: %v", server.language, signedIn)
	}
	server.token = token
	_, forged, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	creds := credentials{
		"member":         {"Authorization": "Bearer " + token},
		"service":        {"Service-Authorization": "Bearer " + signEdgeToken(t, key, "ops-key", "ops", server.api)},
		"member+service": {"Authorization": "Bearer " + token, "Service-Authorization": "Bearer " + signEdgeToken(t, key, "ops-key", "ops", server.api)},
		"forged service": {"Service-Authorization": "Bearer " + signEdgeToken(t, forged, "ops-key", "ops", server.api)},
	}
	as := func(who string, c call) any {
		t.Helper()
		c.as = who
		headers := map[string]string{}
		for name, value := range creds[who] {
			headers[name] = value
		}
		for name, value := range c.headers {
			headers[name] = value
		}
		c.headers = headers
		return sc.do(c)
	}

	// Access: a request with no credential, then a write the member's roles
	// do not hold, before an administrator grants them a role that holds
	// Account and Ticket.
	as("none", call{step: "no credential", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusUnauthorized})
	as("member", call{step: "a write the member's roles lack", method: http.MethodPost, path: accounts,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Acme", "slug": %q}}`, account, account), status: http.StatusForbidden})
	grant(login)
	head := as("member", call{step: "the head of the event log", method: http.MethodGet, path: engineNamespace + "/events?after=head", status: http.StatusOK})
	after := fmt.Sprint(member(head, "data", "next"))

	// Instances: an account, read back and looked up by its slug, and three
	// tickets linked to it, after two the engine refuses.
	as("member", call{step: "the schemas the server deployed", method: http.MethodGet, path: engineNamespace + "/schemas", status: http.StatusOK})
	as("member", call{step: "create an account", method: http.MethodPost, path: accounts,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Acme", "slug": %q, "address": {"line1": "1 Main St", "city": "Springfield", "country": "US"}}}`, account, account), status: http.StatusCreated})
	as("member", call{step: "read the account", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusOK})
	as("member", call{step: "look the account up by its slug", method: http.MethodGet,
		path: engineNamespace + "/schemas/Account/lookup?key=" + url.QueryEscape(fmt.Sprintf(`{"slug":%q}`, account)), status: http.StatusOK})
	as("member", call{step: "an account whose slug another holds", method: http.MethodPost, path: accounts,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Acme again", "slug": %q}}`, account+"-again", account), status: http.StatusConflict})
	as("member", call{step: "a ticket whose title is too long", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": "t0-%s", "data": {"title": %q, "contact": {"email": "bob@example.com"}}, "behaviors": {"Links": {"account": %q}}}`, tag, strings.Repeat("x", 201), account), status: http.StatusUnprocessableEntity})
	as("member", call{step: "a ticket with no account", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": "t0-%s", "data": {"title": "Unlinked", "contact": {"email": "bob@example.com"}}}`, tag), status: http.StatusConflict})
	as("member", call{step: "open a ticket", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": %q, "data": {"title": "The printer is out of toner", "priority": "urgent", "contact": {"email": "bob@example.com"}, "tags": ["printer"]}, "behaviors": {"Links": {"account": %q}}}`, t1, account), status: http.StatusCreated})
	as("member", call{step: "open a second ticket", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": %q, "data": {"title": "The wifi drops", "contact": {"email": "carol@example.com", "phone": "555-0100"}}, "behaviors": {"Links": {"account": %q}}}`, t2, account), status: http.StatusCreated})
	as("member", call{step: "open a third ticket", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": %q, "data": {"title": "A new starter needs a laptop", "contact": {"email": "dan@example.com"}}, "behaviors": {"Links": {"account": %q}}}`, t3, account), status: http.StatusCreated})

	// The workflow: a transition at the sequence it expects, one at a stale
	// sequence, one the workflow does not allow, and comments.
	as("member", call{step: "move the ticket to waiting", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/transition",
		headers: map[string]string{"If-Match": `"1"`}, body: `{"to": "waiting"}`, status: http.StatusOK})
	as("member", call{step: "a transition at a stale sequence", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/transition",
		headers: map[string]string{"If-Match": `"1"`}, body: `{"to": "open"}`, status: http.StatusPreconditionFailed})
	as("member", call{step: "a transition the workflow does not allow", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/transition",
		body: `{"to": "escalated"}`, status: http.StatusConflict})
	as("member", call{step: "comment on the ticket", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/comment",
		body: `{"body": "A new cartridge is on its way."}`, status: http.StatusOK})
	as("member", call{step: "list the ticket's comments", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/listComments",
		body: `{"limit": 10}`, status: http.StatusOK})
	as("member", call{step: "patch a ticket", method: http.MethodPatch, path: tickets + "/" + t3,
		headers: map[string]string{"Content-Type": "application/merge-patch+json", "If-Match": `"1"`}, body: `{"priority": "low", "tags": ["hardware"]}`, status: http.StatusOK})
	as("member", call{step: "resolve the patched ticket", method: http.MethodPost, path: tickets + "/" + t3 + "/operations/transition",
		body: `{"to": "resolved"}`, status: http.StatusOK})

	// Lists: the account's tickets in pages, and with a filter.
	byAccount := url.QueryEscape(fmt.Sprintf(`{"account":%q}`, account))
	page := as("member", call{step: "list the account's tickets, a page", method: http.MethodGet, path: tickets + "?where=" + byAccount + "&limit=2", status: http.StatusOK})
	next, _ := member(page, "data", "next").(string)
	as("member", call{step: "the next page", method: http.MethodGet, path: tickets + "?where=" + byAccount + "&limit=2&cursor=" + url.QueryEscape(next), status: http.StatusOK})
	as("member", call{step: "the account's open tickets", method: http.MethodGet,
		path: tickets + "?where=" + url.QueryEscape(fmt.Sprintf(`{"account":%q,"status":"open"}`, account)), status: http.StatusOK})
	as("member", call{step: "a filter on a field no list filters on", method: http.MethodGet,
		path: tickets + "?where=" + url.QueryEscape(`{"contact":"bob@example.com"}`), status: http.StatusBadRequest})

	// The event log as JSON, then as an event stream from the head, which a
	// comment's event reaches, resumed from its Last-Event-ID.
	t1Events := "&schema=Ticket&instanceId=" + url.QueryEscape(t1)
	as("member", call{step: "the ticket's events", method: http.MethodGet, path: engineNamespace + "/events?after=" + after + t1Events, status: http.StatusOK})
	stream := sc.openStreamAs(creds["member"], call{step: "an event stream from the head", method: http.MethodGet, path: engineNamespace + "/events?after=head" + t1Events,
		as: "member", headers: map[string]string{"Accept": "text/event-stream"}, status: http.StatusOK})
	ready := stream.lastID()
	as("member", call{step: "comment while the stream is open", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/comment",
		body: `{"body": "It arrived."}`, status: http.StatusOK})
	stream.take(func(f sseFrame) bool { return f.Data != nil && f.Event == "" })
	stream.close()
	resumed := sc.openStreamAs(creds["member"], call{step: "resume the stream from its last event id", method: http.MethodGet, path: engineNamespace + "/events?after=head" + t1Events,
		as: "member", headers: map[string]string{"Accept": "text/event-stream", "Last-Event-ID": ready}, status: http.StatusOK})
	resumed.close()

	// MCP: initialize, the initialized notification, the tools the member
	// may call, and a call of one.
	mcp := engineNamespace + "/mcp"
	mcpHeaders := map[string]string{"Accept": "application/json, text/event-stream"}
	as("member", call{step: "MCP initialize", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body: `{"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "script", "version": "1"}}}`, status: http.StatusOK})
	mcpHeaders["MCP-Protocol-Version"] = "2025-06-18"
	as("member", call{step: "MCP initialized", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body: `{"jsonrpc": "2.0", "method": "notifications/initialized"}`, status: http.StatusAccepted})
	as("member", call{step: "MCP tools/list", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body: `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`, status: http.StatusOK})
	as("member", call{step: "MCP tools/call", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body: fmt.Sprintf(`{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "ticket_get", "arguments": {"id": %q}}}`, t1), status: http.StatusOK})

	// Service callers (D37): a service with no session stands in for an end
	// user, who holds no permission; one acting for the member acts as the
	// member; a credential that does not verify is refused before the
	// engine sees it.
	as("service", call{step: "a service standing in for an end user", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusForbidden})
	as("member+service", call{step: "a service acting for the member", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusOK})
	as("forged service", call{step: "a service credential that does not verify", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusUnauthorized})

	// Timers: the runner fires the deadline of the ticket left open, which
	// escalates it as the runner; the waiting ticket's deadline went when it
	// left open, and the resolved one's too.
	runner := startRunner()
	deadline := time.Now().Add(30 * time.Second)
	for {
		ticket := sc.peek(creds["member"], tickets+"/"+t2)
		if member(ticket, "data", "behaviors", "Workflow", "status") == "escalated" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the runner did not fire %s's deadline on the %s server: %v\n%s", t2, server.language, ticket, runner.out.String())
		}
		time.Sleep(100 * time.Millisecond)
	}
	runner.stop(t)
	as("member", call{step: "the escalated ticket", method: http.MethodGet, path: tickets + "/" + t2, status: http.StatusOK})
	as("member", call{step: "the waiting ticket", method: http.MethodGet, path: tickets + "/" + t1, status: http.StatusOK})
	as("member", call{step: "the escalation's events", method: http.MethodGet, path: engineNamespace + "/events?after=" + after + "&schema=Ticket&instanceId=" + url.QueryEscape(t2), status: http.StatusOK})
	return sc.transcript
}

// peek reads path with headers without recording it: the script's polls,
// whose count is the runner's timing.
func (sc *scriptClient) peek(headers map[string]string, path string) any {
	sc.t.Helper()
	req, err := http.NewRequest(http.MethodGet, sc.server.s.base+path, nil)
	if err != nil {
		sc.t.Fatal(err)
	}
	for name, value := range headers {
		req.Header.Set(name, value)
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		sc.t.Fatal(err)
	}
	defer resp.Body.Close()
	var body any
	data, _ := io.ReadAll(resp.Body)
	_ = json.Unmarshal(data, &body)
	return body
}

// openStreamAs is openStream with the credential headers creds.
func (sc *scriptClient) openStreamAs(creds map[string]string, c call) *scriptStream {
	sc.t.Helper()
	headers := map[string]string{}
	for name, value := range creds {
		headers[name] = value
	}
	for name, value := range c.headers {
		headers[name] = value
	}
	c.headers = headers
	return sc.openStream(c)
}

// lastID is the id of the last frame the stream took that carried one.
func (s *scriptStream) lastID() string {
	frames := s.sc.transcript[s.index].Response.Frames
	for i := len(frames) - 1; i >= 0; i-- {
		if frames[i].ID != nil {
			return *frames[i].ID
		}
	}
	s.sc.t.Fatalf("%s took no frame with an id", s.sc.transcript[s.index].Step)
	return ""
}

// member is the value at path in v, a JSON value, or nil.
func member(v any, path ...string) any {
	for _, key := range path {
		object, ok := v.(map[string]any)
		if !ok {
			return nil
		}
		v = object[key]
	}
	return v
}

// Normalising a transcript. What a server answers that is not the same
// from run to run, or from server to server by the script's own doing, is
// written as a symbol, as the corpus recorder writes it
// (runtime/engine/testdata/scenarios/README.md), so the three transcripts
// compare whole:
//
//   - The run's tag, which the script writes into the ids, the slug and the
//     login it chooses since the three runs share one namespace, is
//     {{run}}, wherever it appears.
//   - A UUID is {{uuid:N}}, the N-th distinct one in order of appearance,
//     wherever it appears: the member's id, which is the engine's subject,
//     and the identity runtime's ids.
//   - A session token is {{token:N}}.
//   - An event cursor is {{cursor:N}}: a number a cursor, next, head or
//     floor member holds, an event stream's frame id, a Last-Event-ID and
//     an after query parameter. Cursors are the namespace's, which the
//     three runs share, so each run's start further along the log.
//   - A list's opaque cursor, a next member that is a string and a cursor
//     query parameter, is {{page:N}}: it names a row of the instances
//     table, which the runs share too.
//   - A date-time as JavaScript writes one, to the millisecond in UTC, is
//     {{time}}, not numbered: each server's engine reads its own wall
//     clock, and two calls may fall in one millisecond on one server and
//     not on another. A date-time written any other way is left as it is,
//     so a server that writes one otherwise differs.
//
// Request ids are not symbols: the script sends its own, script-N, as
// X-Request-Id, which each server's runtime takes as the request's id.
// Bodies are compared parsed, so the order of a JSON object's members is
// not compared: Go's problem documents write theirs in another order than
// the TypeScript runtime's.

var (
	scriptUUID = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	scriptTime = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\.\d{3}Z`)
)

// scriptSymbols numbers each kind of symbol by value, in order of
// appearance.
type scriptSymbols struct {
	tag    string
	values map[string]map[string]string
}

func (s *scriptSymbols) symbol(kind, value string) string {
	if s.values == nil {
		s.values = map[string]map[string]string{}
	}
	if s.values[kind] == nil {
		s.values[kind] = map[string]string{}
	}
	if symbol, ok := s.values[kind][value]; ok {
		return symbol
	}
	symbol := fmt.Sprintf("{{%s:%d}}", kind, len(s.values[kind])+1)
	s.values[kind][value] = symbol
	return symbol
}

// normaliseScript is transcript as JSON values, with its symbols written in.
func normaliseScript(t *testing.T, transcript []scriptExchange, tag string) []any {
	t.Helper()
	data, err := json.Marshal(transcript)
	if err != nil {
		t.Fatal(err)
	}
	var steps []any
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}
	symbols := &scriptSymbols{tag: tag}
	for i, step := range steps {
		steps[i] = symbols.walk(step, nil)
	}
	return steps
}

// walk writes the symbols into v, the value at path.
func (s *scriptSymbols) walk(v any, path []string) any {
	key := ""
	if len(path) > 0 {
		key = path[len(path)-1]
	}
	switch v := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(v))
		for k := range v {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v[k] = s.walk(v[k], append(slices.Clone(path), k))
		}
		return v
	case []any:
		for i := range v {
			v[i] = s.walk(v[i], append(slices.Clone(path), strconv.Itoa(i)))
		}
		return v
	case float64:
		switch key {
		case "cursor", "next", "head", "floor":
			return s.symbol("cursor", strconv.FormatFloat(v, 'f', -1, 64))
		}
		return v
	case string:
		inFrame := len(path) >= 3 && path[len(path)-3] == "frames"
		switch {
		case key == "token":
			return s.symbol("token", v)
		case key == "next":
			return s.symbol("page", v)
		case key == "last-event-id", key == "id" && inFrame:
			return s.symbol("cursor", v)
		case key == "path":
			return s.path(v)
		}
		return s.text(v)
	}
	return v
}

// path is a request's path with its query's cursors written as symbols.
func (s *scriptSymbols) path(v string) string {
	p, query, ok := strings.Cut(v, "?")
	if !ok {
		return s.text(v)
	}
	var parts []string
	for _, part := range strings.Split(query, "&") {
		name, value, _ := strings.Cut(part, "=")
		decoded, err := url.QueryUnescape(value)
		if err != nil {
			decoded = value
		}
		switch {
		case name == "after" && decoded != "head":
			value = s.symbol("cursor", decoded)
		case name == "cursor":
			value = s.symbol("page", decoded)
		default:
			value = s.text(decoded)
		}
		parts = append(parts, name+"="+value)
	}
	return s.text(p) + "?" + strings.Join(parts, "&")
}

// text is v with the run's tag, its UUIDs and its times written as
// symbols.
func (s *scriptSymbols) text(v string) string {
	if s.tag != "" {
		v = strings.ReplaceAll(v, s.tag, "{{run}}")
	}
	v = scriptUUID.ReplaceAllStringFunc(v, func(id string) string { return s.symbol("uuid", id) })
	return scriptTime.ReplaceAllString(v, "{{time}}")
}

// compareTranscripts requires each server's transcript, normalised, to be
// the first server's, and writes them under engineScriptDumpEnv's directory
// when it is set.
func compareTranscripts(t *testing.T, phase string, servers []*scriptServer, transcripts map[string][]scriptExchange) {
	t.Helper()
	normalised := map[string][]any{}
	for _, server := range servers {
		normalised[server.language] = normaliseScript(t, transcripts[server.language], server.tag)
	}
	if dir := os.Getenv(engineScriptDumpEnv); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		for _, server := range servers {
			data, err := json.MarshalIndent(normalised[server.language], "", "  ")
			if err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, phase+"-"+strings.ToLower(server.language)+".json"), data, 0o644); err != nil {
				t.Fatal(err)
			}
		}
	}
	first := servers[0]
	want := normalised[first.language]
	for _, server := range servers[1:] {
		got := normalised[server.language]
		differences := 0
		for i := 0; i < max(len(want), len(got)); i++ {
			var w, g any
			if i < len(want) {
				w = want[i]
			}
			if i < len(got) {
				g = got[i]
			}
			wj, _ := json.Marshal(w)
			gj, _ := json.Marshal(g)
			if string(wj) == string(gj) {
				continue
			}
			differences++
			if differences <= 5 {
				t.Errorf("%s step %d: the %s server answered otherwise than the %s server:\n%s: %s\n%s: %s", phase, i+1, server.language, first.language, first.language, wj, server.language, gj)
			}
		}
		if differences > 5 {
			t.Errorf("%s: the %s server's transcript differs from the %s server's in %d steps", phase, server.language, first.language, differences)
		}
	}
}

// checkInterop holds the three servers to one database: what one wrote
// each other reads as it does, a session one signed in reaches the others,
// an event one commits shows on another's event stream, and the three
// deploys of one document at once minted one version.
func checkInterop(t *testing.T, servers []*scriptServer) {
	t.Helper()
	tickets := engineNamespace + "/schemas/Ticket/instances"
	accounts := engineNamespace + "/schemas/Account/instances"
	read := func(server *scriptServer, token, path string) (int, any) {
		t.Helper()
		req, err := http.NewRequest(http.MethodGet, server.s.base+path, nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		var body any
		data, _ := io.ReadAll(resp.Body)
		if err := json.Unmarshal(data, &body); err != nil {
			t.Fatalf("GET %s on the %s server answered %s", path, server.language, data)
		}
		if meta, ok := member(body, "meta").(map[string]any); ok {
			delete(meta, "requestId")
		}
		return resp.StatusCode, body
	}
	for _, writer := range servers {
		for _, path := range []string{accounts + "/acme-" + writer.tag, tickets + "/t1-" + writer.tag, tickets + "/t2-" + writer.tag} {
			status, want := read(writer, writer.token, path)
			if status != http.StatusOK {
				t.Fatalf("the %s server reads %s it wrote: %d %v", writer.language, path, status, want)
			}
			wantJSON, _ := json.Marshal(want)
			for _, reader := range servers {
				if reader == writer {
					continue
				}
				// The reader's own member, and the writer's session too: the
				// sessions are the user model's, in the one database.
				for _, token := range []string{reader.token, writer.token} {
					status, got := read(reader, token, path)
					gotJSON, _ := json.Marshal(got)
					if status != http.StatusOK || string(gotJSON) != string(wantJSON) {
						t.Errorf("the %s server reads %s, which the %s server wrote, as %d %s, not %s", reader.language, path, writer.language, status, gotJSON, wantJSON)
					}
				}
			}
		}
	}

	// An event one server commits reaches the stream another holds open.
	for i, reader := range servers {
		writer := servers[(i+1)%len(servers)]
		ticket := "t1-" + writer.tag
		req, err := http.NewRequest(http.MethodGet, reader.s.base+engineNamespace+"/events?after=head&schema=Ticket&instanceId="+url.QueryEscape(ticket), nil)
		if err != nil {
			t.Fatal(err)
		}
		req.Header.Set("Authorization", "Bearer "+reader.token)
		req.Header.Set("Accept", "text/event-stream")
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		frames := newFrameReader(resp.Body)
		for {
			frame, ok := frames.next(10 * time.Second)
			if !ok {
				t.Fatalf("the %s server's event stream sent no ready event", reader.language)
			}
			if frame.Event == "ready" {
				break
			}
		}
		comment := fmt.Sprintf(`{"body": "Seen from the %s server."}`, reader.language)
		creq, err := http.NewRequest(http.MethodPost, writer.s.base+tickets+"/"+ticket+"/operations/comment", strings.NewReader(comment))
		if err != nil {
			t.Fatal(err)
		}
		creq.Header.Set("Authorization", "Bearer "+writer.token)
		creq.Header.Set("Content-Type", "application/json")
		cresp, err := http.DefaultClient.Do(creq)
		if err != nil {
			t.Fatal(err)
		}
		cresp.Body.Close()
		if cresp.StatusCode != http.StatusOK {
			t.Fatalf("a comment on the %s server = %d", writer.language, cresp.StatusCode)
		}
		frame, ok := frames.next(10 * time.Second)
		resp.Body.Close()
		if !ok || member(frame.Data, "instanceId") != ticket || member(frame.Data, "kind") != "operation" {
			t.Errorf("the %s server's stream did not carry the comment the %s server committed: %+v", reader.language, writer.language, frame)
			continue
		}
		// The writer's own log holds the event the reader's stream sent.
		_, page := read(writer, writer.token, engineNamespace+"/events?after="+fmt.Sprint(int64(member(frame.Data, "cursor").(float64))-1)+"&schema=Ticket&instanceId="+url.QueryEscape(ticket))
		events, _ := member(page, "data", "events").([]any)
		streamed, _ := json.Marshal(frame.Data)
		var logged []byte
		if len(events) > 0 {
			logged, _ = json.Marshal(events[0])
		}
		if string(streamed) != string(logged) {
			t.Errorf("the %s server streamed %s, which the %s server's log holds as %s", reader.language, streamed, writer.language, logged)
		}
	}

	// Three servers deployed one document at once: one version of each
	// engine type was published, which each server reads.
	for _, server := range servers {
		_, page := read(server, server.token, engineNamespace+"/events?after=0&kind=publish")
		events, _ := member(page, "data", "events").([]any)
		var published []string
		for _, event := range events {
			published = append(published, fmt.Sprint(member(event, "schema"), " ", member(event, "version")))
		}
		slices.Sort(published)
		if want := []string{"Account 1", "Ticket 1"}; !slices.Equal(published, want) {
			t.Errorf("the %s server's log holds the publishes %v, want %v", server.language, published, want)
		}
	}
}
