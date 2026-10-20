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
	"github.com/parable-work/superschematic/internal/generator/naming"
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

// requireEngineServersEnv, set to 1, makes the engine servers' tests fail
// rather than skip when a toolchain, a built runtime or the Postgres they
// run on is missing: CI's engine-servers job sets it, where a skip would
// hide a broken gate.
const requireEngineServersEnv = "SUPERSCHEMATIC_REQUIRE_ENGINE_SERVERS"

// skipEngineServers skips the test, saying why, or fails it under
// requireEngineServersEnv.
func skipEngineServers(t testing.TB, format string, args ...any) {
	t.Helper()
	if os.Getenv(requireEngineServersEnv) == "1" {
		t.Fatalf("%s=1 requires this test to run: %s", requireEngineServersEnv, fmt.Sprintf(format, args...))
	}
	t.Skipf(format, args...)
}

// engineDatabase is the Postgres engineDatabaseEnv names, or "" when it is
// unset, which fails the test under requireEngineServersEnv.
func engineDatabase(t *testing.T) string {
	t.Helper()
	postgres := os.Getenv(engineDatabaseEnv)
	if postgres == "" && os.Getenv(requireEngineServersEnv) == "1" {
		t.Fatalf("%s=1 requires %s, the Postgres the servers run on", requireEngineServersEnv, engineDatabaseEnv)
	}
	return postgres
}

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

// requireEngineArchive skips, or fails under requireEngineServersEnv,
// unless the engine's archive a Go server links is built for this host.
func requireEngineArchive(t *testing.T, paths naming.LocalPaths) {
	t.Helper()
	archive := filepath.Join(paths.EngineGo, "lib", runtime.GOOS+"_"+runtime.GOARCH, "libsuperschematic_engine.a")
	if _, err := os.Stat(archive); err != nil {
		skipEngineServers(t, "the engine's archive is not built (run scripts/engine-archive.sh): %v", err)
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
		skipEngineServers(t, "cargo not available: the Rust server cannot be built")
	}
	bun, err := exec.LookPath("bun")
	if err != nil {
		skipEngineServers(t, "bun not available: the TypeScript server cannot run: %v", err)
	}
	paths := testpaths.Local(t)
	requireEngineArchive(t, paths)
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
	// script writes into the ids, the slug, the titles and the login it
	// chooses, which the transcript writes {{run}}: the three runs share one
	// namespace.
	api, language, tag string
	s                  *started
	// token is the session the script's member signed in with on it.
	token string
}

// startEngineTrio starts the three servers on the database at databaseURL,
// each with its identity config and its callers field, whose one issuer,
// ops, signs with the key public verifies.
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
// shape. The servers interoperate too (checkInterop): what one wrote each
// other reads alike, a session one signed in reaches the others, an event
// one commits shows on another's stream, and the three deploys at once
// published one version of each type.
//
// What the script found, and what was fixed: a Go server answered its
// event log's routes X-Request-Id twice (enginehttp added the engine's to
// the request log's), and the Go HTTP runtime answered without
// Cache-Control: no-store, a sign-in's token included, where the Rust and
// TypeScript runtimes mark every answer no-store. What is a host's own and
// normalised is listed where the normalising is (normaliseScript,
// hostHeaders). A request with no credential, which the TypeScript runtime
// refuses before the engine sees it, answers what the engine answers.
func TestEngineServersAnswerOneScriptAlike(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping the three servers' builds in -short mode")
	}
	since := time.Now()
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
	compareTranscripts(t, "offline", offline, transcripts, since)

	postgres := engineDatabase(t)
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
	compareTranscripts(t, "online", servers, transcripts, since)
	for _, server := range servers {
		checkCorpus(t, server, transcripts[server.language])
	}
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
	corpus   *corpusRef
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
	// corpus names the HTTP corpus's exchange the step's answer must have
	// the shape of, when the corpus records one.
	corpus *corpusRef
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
	exchange := scriptExchange{Step: c.step, Request: scriptRequest{Method: c.method, Path: c.path, As: c.as}, corpus: c.corpus}
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
// administrator would, and startRunner starts the engine's runner. A step
// the HTTP corpus records names its case (corpusRef), whose answer
// checkCorpus holds the step's to.
func runEngineScript(t *testing.T, server *scriptServer, key ed25519.PrivateKey, grant func(login string), startRunner func() *started) []scriptExchange {
	t.Helper()
	sc := &scriptClient{t: t, server: server}
	tag := server.tag
	login := "ada." + tag + "@example.com"
	accounts := engineNamespace + "/schemas/Account/instances"
	tickets := engineNamespace + "/schemas/Ticket/instances"
	account, t1, t2, t3 := "acme-"+tag, "t1-"+tag, "t2-"+tag, "t3-"+tag
	// The tickets' titles carry the run's tag, so a list filtered on them
	// reads this run's tickets alone: Ticket's list filters on its title,
	// its priority, its dueAt and Workflow.status, not on its link.
	title1, title2, title3 := "The printer is out of toner ("+tag+")", "The wifi drops ("+tag+")", "A new starter needs a laptop ("+tag+")"

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
	as("none", call{step: "no credential", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusUnauthorized,
		corpus: corpus("http.json", "authentication and access > no caller is 401", http.MethodGet, http.StatusUnauthorized, "/instances").exact()})
	as("member", call{step: "a write the member's roles lack", method: http.MethodPost, path: accounts,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Acme", "slug": %q}}`, account, account), status: http.StatusForbidden,
		corpus: corpus("http.json", "authentication and access > no caller is 401", http.MethodPost, http.StatusForbidden, "/instances")})
	grant(login)
	head := as("member", call{step: "the head of the event log", method: http.MethodGet, path: engineNamespace + "/events?after=head", status: http.StatusOK,
		corpus: corpus("http.json", "events as JSON > after=head answers the cursor", http.MethodGet, http.StatusOK, "/events").at(1)})
	after := fmt.Sprint(member(head, "data", "next"))

	// Instances: an account, read back and looked up by its slug, and three
	// tickets linked to it, after three the engine refuses.
	as("member", call{step: "the schemas the server deployed", method: http.MethodGet, path: engineNamespace + "/schemas", status: http.StatusOK,
		corpus: corpus("http.json", "schemas > define a draft", http.MethodGet, http.StatusOK, "/schemas")})
	as("member", call{step: "create an account", method: http.MethodPost, path: accounts,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Acme", "slug": %q, "address": {"line1": "1 Main St", "city": "Springfield", "country": "US"}}}`, account, account), status: http.StatusCreated,
		corpus: corpus("http.json", "instances > create, get, list, update and delete", http.MethodPost, http.StatusCreated, "/instances")})
	as("member", call{step: "read the account", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusOK,
		corpus: corpus("http.json", "instances > create, get, list, update and delete", http.MethodGet, http.StatusOK, "/instances/o1")})
	as("member", call{step: "look the account up by its slug", method: http.MethodGet,
		path: engineNamespace + "/schemas/Account/lookup?key=" + url.QueryEscape(fmt.Sprintf(`{"slug":%q}`, account)), status: http.StatusOK,
		corpus: corpus("http.json", "instances > lookup reads an instance", http.MethodGet, http.StatusOK, "/lookup")})
	as("member", call{step: "an account whose slug another holds", method: http.MethodPost, path: accounts,
		body: fmt.Sprintf(`{"id": %q, "data": {"name": "Acme again", "slug": %q}}`, account+"-again", account), status: http.StatusConflict,
		corpus: corpus("http.json", "instances > lookup reads an instance", http.MethodPost, http.StatusConflict, "/instances")})
	as("member", call{step: "a ticket whose title is too long", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": "t0-%s", "data": {"title": %q, "contact": {"email": "bob@example.com"}}, "behaviors": {"Links": {"account": %q}}}`, tag, strings.Repeat("x", 201), account), status: http.StatusUnprocessableEntity})
	as("member", call{step: "a ticket with no account", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": "t0-%s", "data": {"title": "Unlinked", "contact": {"email": "bob@example.com"}}}`, tag), status: http.StatusBadRequest})
	as("member", call{step: "open a ticket", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": %q, "data": {"title": %q, "priority": "urgent", "contact": {"email": "bob@example.com"}, "tags": ["printer"]}, "behaviors": {"Links": {"account": %q}}}`, t1, title1, account), status: http.StatusCreated})
	opened := as("member", call{step: "open a second ticket", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": %q, "data": {"title": %q, "contact": {"email": "carol@example.com", "phone": "555-0100"}}, "behaviors": {"Links": {"account": %q}}}`, t2, title2, account), status: http.StatusCreated})
	as("member", call{step: "open a third ticket", method: http.MethodPost, path: tickets,
		body: fmt.Sprintf(`{"id": %q, "data": {"title": %q, "contact": {"email": "dan@example.com"}}, "behaviors": {"Links": {"account": %q}}}`, t3, title3, account), status: http.StatusCreated})
	// The transcripts write every time {{time}}, so the script checks
	// what it relies on of them itself: a ticket opens at its creation, and
	// its deadline is a second and a half on.
	createdAt := member(opened, "data", "createdAt")
	if member(opened, "data", "updatedAt") != createdAt || member(opened, "data", "behaviors", "Workflow", "enteredAt") != createdAt ||
		!sameNumber(member(opened, "data", "behaviors", "Timers", "deadlines", "respond", "dueAt"), createdAt, 1500) {
		t.Errorf("on the %s server, %s opened with times %v", server.language, t2, member(opened, "data"))
	}

	// The workflow: a transition at the sequence it expects, one at a stale
	// sequence, one the workflow does not allow, and comments.
	transition := corpus("operations.json", "the core's behaviors over HTTP > a Workflow transition runs through the operation route", http.MethodPost, http.StatusOK, "/operations/transition")
	as("member", call{step: "move the ticket to waiting", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/transition",
		headers: map[string]string{"If-Match": `"1"`}, body: `{"to": "waiting"}`, status: http.StatusOK, corpus: transition})
	as("member", call{step: "a transition at a stale sequence", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/transition",
		headers: map[string]string{"If-Match": `"1"`}, body: `{"to": "open"}`, status: http.StatusPreconditionFailed, corpus: transition.with(http.StatusPreconditionFailed)})
	as("member", call{step: "a transition the workflow does not allow", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/transition",
		body: `{"to": "escalated"}`, status: http.StatusConflict, corpus: transition.with(http.StatusConflict)})
	as("member", call{step: "comment on the ticket", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/comment",
		body: `{"body": "A new cartridge is on its way."}`, status: http.StatusOK,
		corpus: corpus("operations.json", "the core's behaviors over HTTP > listComments is a read", http.MethodPost, http.StatusOK, "/operations/comment")})
	as("member", call{step: "list the ticket's comments", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/listComments",
		body: `{"limit": 10}`, status: http.StatusOK,
		corpus: corpus("operations.json", "the core's behaviors over HTTP > listComments is a read", http.MethodPost, http.StatusOK, "/operations/listComments")})
	as("member", call{step: "patch a ticket", method: http.MethodPatch, path: tickets + "/" + t3,
		headers: map[string]string{"Content-Type": "application/merge-patch+json", "If-Match": `"1"`}, body: `{"priority": "low", "tags": ["hardware"]}`, status: http.StatusOK,
		corpus: corpus("http.json", "instances > an update is a merge patch", http.MethodPatch, http.StatusOK, "/instances/o1")})
	as("member", call{step: "resolve the patched ticket", method: http.MethodPost, path: tickets + "/" + t3 + "/operations/transition",
		body: `{"to": "resolved"}`, status: http.StatusOK, corpus: transition})

	// Lists: this run's tickets in pages, and with a filter on a behavior's
	// field.
	byTitle := url.QueryEscape(fmt.Sprintf(`{"title":[%q,%q,%q]}`, title1, title2, title3))
	page := as("member", call{step: "list the run's tickets, a page", method: http.MethodGet, path: tickets + "?where=" + byTitle + "&limit=2", status: http.StatusOK,
		corpus: corpus("http.json", "instances > a list filters by a where query parameter", http.MethodGet, http.StatusOK, "/instances")})
	next, _ := member(page, "data", "next").(string)
	as("member", call{step: "the next page", method: http.MethodGet, path: tickets + "?where=" + byTitle + "&limit=2&cursor=" + url.QueryEscape(next), status: http.StatusOK,
		corpus: corpus("http.json", "instances > a list pages in creation order", http.MethodGet, http.StatusOK, "/instances")})
	as("member", call{step: "the run's open tickets", method: http.MethodGet,
		path: tickets + "?where=" + url.QueryEscape(fmt.Sprintf(`{"title":[%q,%q,%q],"Workflow.status":"open"}`, title1, title2, title3)), status: http.StatusOK})
	as("member", call{step: "a filter on a field no list filters on", method: http.MethodGet,
		path: tickets + "?where=" + url.QueryEscape(`{"contact":"bob@example.com"}`), status: http.StatusBadRequest,
		corpus: corpus("http.json", "instances > a list filters by a where query parameter", http.MethodGet, http.StatusBadRequest, "/instances").at(2)})

	// The event log as JSON, then as an event stream from the head, which a
	// comment's event reaches, resumed from its Last-Event-ID.
	t1Events := "&schema=Ticket&instanceId=" + url.QueryEscape(t1)
	as("member", call{step: "the ticket's events", method: http.MethodGet, path: engineNamespace + "/events?after=" + after + t1Events, status: http.StatusOK,
		corpus: corpus("http.json", "events as JSON > pages from a cursor, filtered by schema and instance", http.MethodGet, http.StatusOK, "/events")})
	streamCase := corpus("stream.json", "event stream > starts at the head with after=head", http.MethodGet, http.StatusOK, "/events")
	stream := sc.openStreamAs(creds["member"], call{step: "an event stream from the head", method: http.MethodGet, path: engineNamespace + "/events?after=head" + t1Events,
		as: "member", headers: map[string]string{"Accept": "text/event-stream"}, status: http.StatusOK, corpus: streamCase})
	ready := stream.lastID()
	as("member", call{step: "comment while the stream is open", method: http.MethodPost, path: tickets + "/" + t1 + "/operations/comment",
		body: `{"body": "It arrived."}`, status: http.StatusOK})
	stream.take(func(f sseFrame) bool { return f.Data != nil && f.Event == "" })
	stream.close()
	resumed := sc.openStreamAs(creds["member"], call{step: "resume the stream from its last event id", method: http.MethodGet, path: engineNamespace + "/events?after=head" + t1Events,
		as: "member", headers: map[string]string{"Accept": "text/event-stream", "Last-Event-ID": ready}, status: http.StatusOK, corpus: streamCase.second()})
	resumed.close()

	// MCP: initialize, the initialized notification, the tools the member
	// may call, a call of one and a call the workflow refuses.
	mcp := engineNamespace + "/mcp"
	mcpHeaders := map[string]string{"Accept": "application/json, text/event-stream"}
	mcpCase := "tools/call > runs the operation and returns its result as text"
	as("member", call{step: "MCP initialize", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body:   `{"jsonrpc": "2.0", "id": 0, "method": "initialize", "params": {"protocolVersion": "2025-06-18", "capabilities": {}, "clientInfo": {"name": "script", "version": "1"}}}`,
		status: http.StatusOK, corpus: corpus("mcp.json", mcpCase, http.MethodPost, http.StatusOK, "/mcp").exact()})
	mcpHeaders["MCP-Protocol-Version"] = "2025-06-18"
	as("member", call{step: "MCP initialized", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body: `{"jsonrpc": "2.0", "method": "notifications/initialized"}`, status: http.StatusAccepted, corpus: corpus("mcp.json", mcpCase, http.MethodPost, http.StatusAccepted, "/mcp")})
	as("member", call{step: "MCP tools/list", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body: `{"jsonrpc": "2.0", "id": 1, "method": "tools/list"}`, status: http.StatusOK,
		corpus: corpus("mcp.json", "tools/list > lists the visible tools by handle", http.MethodPost, http.StatusOK, "/mcp").at(1)})
	as("member", call{step: "MCP tools/call", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body:   fmt.Sprintf(`{"jsonrpc": "2.0", "id": 2, "method": "tools/call", "params": {"name": "ticket_get", "arguments": {"id": %q}}}`, t1),
		status: http.StatusOK, corpus: corpus("mcp.json", mcpCase, http.MethodPost, http.StatusOK, "/mcp").at(1)})
	as("member", call{step: "MCP tools/call the workflow refuses", method: http.MethodPost, path: mcp, headers: mcpHeaders,
		body:   fmt.Sprintf(`{"jsonrpc": "2.0", "id": 3, "method": "tools/call", "params": {"name": "ticket_transition", "arguments": {"id": %q, "params": {"to": "escalated"}}}}`, t1),
		status: http.StatusOK})

	// Service callers (D37): a service with no session stands in for an end
	// user, who holds no permission; one acting for the member acts as the
	// member; a credential that does not verify is refused before the
	// engine sees it.
	as("service", call{step: "a service standing in for an end user", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusForbidden,
		corpus: corpus("http.json", "service callers > a service with no end user stands in", http.MethodGet, http.StatusForbidden, "/instances/o1")})
	as("member+service", call{step: "a service acting for the member", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusOK,
		corpus: corpus("http.json", "service callers > a service acting for an end user", http.MethodGet, http.StatusOK, "/instances/o1")})
	as("forged service", call{step: "a service credential that does not verify", method: http.MethodGet, path: accounts + "/" + account, status: http.StatusUnauthorized,
		corpus: corpus("http.json", "service callers > a credential that does not verify", http.MethodGet, http.StatusUnauthorized, "/instances").exact()})

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
	escalated := as("member", call{step: "the escalated ticket", method: http.MethodGet, path: tickets + "/" + t2, status: http.StatusOK})
	fired := member(escalated, "data", "behaviors", "Timers", "deadlines", "respond", "firedAt")
	if member(escalated, "data", "updatedAt") != fired || member(escalated, "data", "behaviors", "Workflow", "enteredAt") != fired ||
		!sameNumber(fired, member(escalated, "data", "behaviors", "Timers", "deadlines", "respond", "dueAt"), 0) && !later(fired, member(escalated, "data", "behaviors", "Timers", "deadlines", "respond", "dueAt")) {
		t.Errorf("on the %s server, %s was escalated with times %v", server.language, t2, member(escalated, "data"))
	}
	as("member", call{step: "the waiting ticket", method: http.MethodGet, path: tickets + "/" + t1, status: http.StatusOK})
	as("member", call{step: "the escalation's events", method: http.MethodGet, path: engineNamespace + "/events?after=" + after + "&schema=Ticket&instanceId=" + url.QueryEscape(t2), status: http.StatusOK})
	return sc.transcript
}

// sameNumber reports whether a, a JSON number, is b's plus by.
func sameNumber(a, b any, by float64) bool {
	x, ok1 := a.(float64)
	y, ok2 := b.(float64)
	return ok1 && ok2 && x == y+by
}

// later reports whether a, a JSON number, is past b.
func later(a, b any) bool {
	x, ok1 := a.(float64)
	y, ok2 := b.(float64)
	return ok1 && ok2 && x > y
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
//   - The run's tag, which the script writes into the ids, the slug, the
//     titles and the login it chooses, since the three runs share one
//     namespace, is {{run}}, wherever it appears.
//   - The member's id, which the identity runtime draws (data.user.id of
//     the sign-in) and the engine names its subject by (createdBy, actor,
//     a refusal's detail), is {{user:N}}, wherever it appears; a UUID is
//     {{uuid:N}}; each the N-th distinct one in order of appearance.
//   - A session token is {{token:N}}.
//   - An event cursor is {{cursor:N}}: a number a cursor, next, head or
//     floor member holds, an event stream's frame id, a Last-Event-ID and
//     an after query parameter. Cursors are the namespace's, which the
//     three runs share, so each run's start further along the log.
//   - A list's opaque cursor, a next member that is a string and a cursor
//     query parameter, is {{page:N}}: it names a row of the instances
//     table, which the runs share too.
//   - A time is {{time}}, not numbered: an integer of epoch milliseconds
//     from a day before the test started to 400 days after, as the engine
//     writes every time, alone or inside a string (an MCP tool's text), and
//     an RFC 3339 date-time in UTC. Each server's engine reads its own wall
//     clock, and two calls may fall in one millisecond on one server and
//     not on another, so the script checks the times it relies on itself.
//     A date-time's precision is not compared: the identity runtimes write
//     a session's expiresAt to the millisecond (TypeScript), the
//     microsecond (Rust) and the nanosecond with its trailing zeros dropped
//     (Go), and D50 names a date-time and no precision.
//   - An MCP answer's X-Request-Id is left out (hostHeaders says why).
//
// Request ids are not symbols: the script sends its own, script-N, as
// X-Request-Id, which each server's runtime takes as the request's id.
// Bodies are compared parsed, so the order of a JSON object's members is
// not compared: Go's problem documents write theirs in another order than
// the TypeScript runtime's.

var (
	scriptUUID   = regexp.MustCompile(`[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}`)
	scriptTime   = regexp.MustCompile(`\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}(\.\d+)?Z`)
	scriptMillis = regexp.MustCompile(`\b\d{13}\b`)
)

// scriptSymbols numbers each kind of symbol by value, in order of
// appearance.
type scriptSymbols struct {
	tag string
	// users are the member ids the transcript's sign-ins answered, in
	// order, which text writes as symbols wherever they appear.
	users []string
	// from and to are the span of epoch milliseconds a time falls in.
	from, to float64
	values   map[string]map[string]string
}

// symbol is the symbol of kind that stands for value: the one it took at
// its first appearance, or the next of its kind.
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

// normaliseScript is transcript as JSON values, with its symbols written
// in; the test started at since.
func normaliseScript(t *testing.T, transcript []scriptExchange, tag string, since time.Time) []any {
	t.Helper()
	compared := make([]scriptExchange, len(transcript))
	for i, exchange := range transcript {
		exchange.Response.Headers = hostHeaders(exchange)
		compared[i] = exchange
	}
	data, err := json.Marshal(compared)
	if err != nil {
		t.Fatal(err)
	}
	var steps []any
	if err := json.Unmarshal(data, &steps); err != nil {
		t.Fatal(err)
	}
	symbols := &scriptSymbols{tag: tag,
		from: float64(since.Add(-24 * time.Hour).UnixMilli()), to: float64(since.Add(400 * 24 * time.Hour).UnixMilli())}
	for _, step := range steps {
		if id, ok := member(step, "response", "body", "data", "user", "id").(string); ok && !slices.Contains(symbols.users, id) {
			symbols.users = append(symbols.users, id)
		}
	}
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
		if s.isTime(v) {
			return "{{time}}"
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

// isTime reports whether v is an integer of epoch milliseconds in the
// span a time falls in.
func (s *scriptSymbols) isTime(v float64) bool {
	return v == float64(int64(v)) && v >= s.from && v <= s.to
}

// text is v with the run's tag, the members' ids, its UUIDs and its times
// written as symbols.
func (s *scriptSymbols) text(v string) string {
	if s.tag != "" {
		v = strings.ReplaceAll(v, s.tag, "{{run}}")
	}
	for _, id := range s.users {
		if strings.Contains(v, id) {
			v = strings.ReplaceAll(v, id, s.symbol("user", id))
		}
	}
	v = scriptUUID.ReplaceAllStringFunc(v, func(id string) string { return s.symbol("uuid", id) })
	v = scriptTime.ReplaceAllString(v, "{{time}}")
	return scriptMillis.ReplaceAllStringFunc(v, func(n string) string {
		if millis, err := strconv.ParseFloat(n, 64); err == nil && s.isTime(millis) {
			return "{{time}}"
		}
		return n
	})
}

// compareTranscripts requires each server's transcript, normalised, to be
// the first server's, and writes them under engineScriptDumpEnv's directory
// when it is set.
func compareTranscripts(t *testing.T, phase string, servers []*scriptServer, transcripts map[string][]scriptExchange, since time.Time) {
	t.Helper()
	normalised := map[string][]any{}
	for _, server := range servers {
		normalised[server.language] = normaliseScript(t, transcripts[server.language], server.tag, since)
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

// The HTTP corpus (runtime/engine/testdata/http) records the TypeScript
// engine's answers to cases of its own, over schemas, ids and policies of
// their own (Order, documents, a policy that answers each question as
// recorded), which the servers do not serve. So a step that corresponds to
// one of its exchanges is held to that exchange's shape, which is what a
// host could change: the status; the headers the corpus records, by name,
// and the values of Content-Type and Cache-Control; a problem's type,
// title, status and code, and its detail word for word where it names
// nothing of the case's own (exact); the members of an envelope, its meta,
// an instance, a list, an event page and its events, and of a JSON-RPC
// answer and its result, with MCP's serverInfo and capabilities; and the
// kinds of an event stream's frames, in order. A step with no counterpart
// names none: the identity routes, which the corpus leaves out, and what
// only desk-db composes (Links' required link, Timers' runner).

// corpusRef names the exchange of a corpus case a step corresponds to.
type corpusRef struct {
	file, name string
	method     string
	status     int
	path       string
	detail     bool
	nth        int
}

// corpus is the nth exchange (from 0) of the case of file whose name starts
// with name that answered method on a path ending in path with status.
func corpus(file, name, method string, status int, path string) *corpusRef {
	return &corpusRef{file: file, name: name, method: method, status: status, path: path}
}

// exact holds a problem's detail to the corpus's word for word.
func (c *corpusRef) exact() *corpusRef { copied := *c; copied.detail = true; return &copied }

// with is c answered with status.
func (c *corpusRef) with(status int) *corpusRef { copied := *c; copied.status = status; return &copied }

// at is the nth of c's matching exchanges.
func (c *corpusRef) at(n int) *corpusRef { copied := *c; copied.nth = n; return &copied }

// second is the second of c's matching exchanges.
func (c *corpusRef) second() *corpusRef { return c.at(1) }

// corpusFile is a corpus file as the shape check reads it.
type corpusFile struct {
	Shared map[string]any `json:"shared"`
	Cases  []struct {
		Name  string           `json:"name"`
		Steps []map[string]any `json:"steps"`
	} `json:"cases"`
}

// corpusExchange finds the exchange ref names, and the frames its stream
// sent, if it was one.
func corpusExchange(t *testing.T, files map[string]*corpusFile, ref *corpusRef) (response map[string]any, frames []any) {
	t.Helper()
	file := files[ref.file]
	if file == nil {
		data, err := os.ReadFile(filepath.Join(testpaths.RepoRoot(t), "runtime", "engine", "testdata", "http", ref.file))
		if err != nil {
			t.Fatal(err)
		}
		file = &corpusFile{}
		if err := json.Unmarshal(data, file); err != nil {
			t.Fatalf("%s: %v", ref.file, err)
		}
		files[ref.file] = file
	}
	for _, c := range file.Cases {
		if !strings.HasPrefix(c.Name, ref.name) {
			continue
		}
		n := 0
		for i, step := range c.Steps {
			request, _ := step["request"].(map[string]any)
			response, _ := step["response"].(map[string]any)
			if request == nil || request["method"] != ref.method || !strings.HasSuffix(fmt.Sprint(request["path"]), ref.path) || response["status"] != float64(ref.status) {
				continue
			}
			if n++; n <= ref.nth {
				continue
			}
			if name, ok := step["stream"].(string); ok {
				for _, later := range c.Steps[i+1:] {
					if later["stream"] == name {
						if taken, ok := resolveShared(file.Shared, later["frames"]).([]any); ok {
							frames = append(frames, taken...)
						}
					}
				}
			}
			body := resolveShared(file.Shared, response["body"])
			response["body"] = body
			return response, frames
		}
		t.Fatalf("%s's case %q has no exchange %d answering %s …%s %d", ref.file, c.Name, ref.nth, ref.method, ref.path, ref.status)
	}
	t.Fatalf("%s has no case %q", ref.file, ref.name)
	return nil, nil
}

// resolveShared is v with each {"$shared": name} the corpus's shared value.
func resolveShared(shared map[string]any, v any) any {
	switch v := v.(type) {
	case map[string]any:
		if name, ok := v["$shared"].(string); ok && len(v) == 1 {
			return resolveShared(shared, shared[name])
		}
		out := map[string]any{}
		for k, child := range v {
			out[k] = resolveShared(shared, child)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, child := range v {
			out[i] = resolveShared(shared, child)
		}
		return out
	}
	return v
}

// checkCorpus holds each step of transcript that names a corpus exchange
// to that exchange's shape.
func checkCorpus(t *testing.T, server *scriptServer, transcript []scriptExchange) {
	t.Helper()
	files := map[string]*corpusFile{}
	for _, exchange := range transcript {
		if exchange.corpus == nil {
			continue
		}
		recorded, frames := corpusExchange(t, files, exchange.corpus)
		var want, got []string
		headers, _ := recorded["headers"].(map[string]any)
		for name := range headers {
			want = append(want, name)
		}
		for name := range hostHeaders(exchange) {
			if !strings.HasPrefix(name, "access-control-") {
				got = append(got, name)
			}
		}
		slices.Sort(want)
		slices.Sort(got)
		if !slices.Equal(got, want) {
			t.Errorf("%s on the %s server answers the headers %v, where the corpus's %q answers %v", exchange.Step, server.language, got, exchange.corpus.name, want)
		}
		for _, name := range []string{"content-type", "cache-control"} {
			if fmt.Sprint(headers[name]) != fmt.Sprint(valueOr(exchange.Response.Headers[name])) && (headers[name] != nil || exchange.Response.Headers[name] != "") {
				t.Errorf("%s on the %s server answers %s %q, where the corpus's %q answers %v", exchange.Step, server.language, name, exchange.Response.Headers[name], exchange.corpus.name, headers[name])
			}
		}
		wantShape, _ := json.Marshal(answerShape(recorded["body"], frames, exchange.corpus.detail))
		var taken []any
		for _, frame := range exchange.Response.Frames {
			data, _ := json.Marshal(frame)
			var v any
			_ = json.Unmarshal(data, &v)
			taken = append(taken, v)
		}
		gotShape, _ := json.Marshal(answerShape(exchange.Response.Body, taken, exchange.corpus.detail))
		if string(gotShape) != string(wantShape) {
			t.Errorf("%s on the %s server answers in the shape\n%s\nwhere the corpus's %q answers\n%s", exchange.Step, server.language, gotShape, exchange.corpus.name, wantShape)
		}
	}
}

// valueOr is v, or nil for no value, as a recorded header compares.
func valueOr(v string) any {
	if v == "" {
		return nil
	}
	return v
}

// hostHeaders are the headers of exchange's answer a comparison reads:
// all it recorded but an MCP answer's X-Request-Id. A Go server's request
// log writes X-Request-Id on every answer of the API's router, MCP's
// included; the engine's MCP endpoint writes none (the corpus has none),
// and the Rust and TypeScript servers mount it outside their runtimes'
// request ids, so theirs answer none either.
func hostHeaders(exchange scriptExchange) map[string]string {
	if !strings.HasSuffix(strings.SplitN(exchange.Request.Path, "?", 2)[0], "/mcp") {
		return exchange.Response.Headers
	}
	headers := map[string]string{}
	for name, value := range exchange.Response.Headers {
		if name != "x-request-id" {
			headers[name] = value
		}
	}
	return headers
}

// answerShape is the shape of an answer's body or stream, as the corpus
// check compares it.
func answerShape(body any, frames []any, detail bool) any {
	if frames != nil {
		var kinds []any
		for _, frame := range frames {
			f, _ := frame.(map[string]any)
			kind := map[string]any{}
			if c, ok := f["comment"]; ok && c != "" {
				kind["comment"] = c
			}
			if e, ok := f["event"]; ok && e != "" {
				kind["event"] = e
			}
			if _, ok := f["id"]; ok {
				kind["id"] = true
			}
			if data, ok := f["data"]; ok && data != nil {
				kind["data"] = bodyShape(data, detail)
			}
			kinds = append(kinds, kind)
		}
		return map[string]any{"frames": kinds}
	}
	return bodyShape(body, detail)
}

// bodyShape is the shape of a JSON answer.
func bodyShape(body any, detail bool) any {
	object, ok := body.(map[string]any)
	if !ok {
		if body == nil {
			return nil
		}
		return fmt.Sprintf("%T", body)
	}
	switch {
	case object["type"] != nil && object["title"] != nil && object["status"] != nil:
		shape := map[string]any{"problem": keysOf(object), "type": object["type"], "title": object["title"], "status": object["status"], "code": object["code"]}
		if detail {
			shape["detail"] = object["detail"]
		}
		return shape
	case object["jsonrpc"] != nil:
		shape := map[string]any{"jsonrpc": keysOf(object)}
		if result, ok := object["result"].(map[string]any); ok {
			shape["result"] = keysOf(result)
			for _, k := range []string{"serverInfo", "capabilities"} {
				if v, ok := result[k]; ok {
					shape[k] = v
				}
			}
			if tools, ok := result["tools"].([]any); ok && len(tools) > 0 {
				shape["tool"] = keysOf(tools[0])
			}
		}
		return shape
	case object["data"] != nil || object["meta"] != nil:
		return map[string]any{"envelope": keysOf(object), "meta": keysOf(object["meta"]), "data": dataShape(object["data"])}
	case object["cursor"] != nil || object["kind"] != nil:
		return map[string]any{"event": keysOf(object)}
	}
	return map[string]any{"object": keysOf(object)}
}

// dataShape is the shape of an envelope's data: an instance, a list, a
// page of events, the list of schemas, or another operation's result,
// whose members are the operation's own.
func dataShape(data any) any {
	switch data := data.(type) {
	case []any:
		if len(data) == 0 {
			return "empty list"
		}
		return map[string]any{"each": keysOf(data[0])}
	case map[string]any:
		switch {
		case data["schema"] != nil && data["id"] != nil && data["seq"] != nil:
			return map[string]any{"instance": keysOf(data)}
		case data["items"] != nil:
			shape := map[string]any{"list": keysOf(data)}
			if items, _ := data["items"].([]any); len(items) > 0 {
				shape["item"] = dataShape(items[0])
			}
			return shape
		case data["events"] != nil:
			shape := map[string]any{"events": keysOf(data)}
			if events, _ := data["events"].([]any); len(events) > 0 {
				shape["event"] = keysOf(events[0])
			}
			return shape
		}
		return "an operation's result"
	}
	return fmt.Sprintf("%T", data)
}

// keysOf is the sorted member names of v, a JSON object.
func keysOf(v any) []string {
	object, _ := v.(map[string]any)
	keys := make([]string, 0, len(object))
	for k := range object {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}
