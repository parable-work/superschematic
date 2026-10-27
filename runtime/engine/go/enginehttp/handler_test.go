package enginehttp_test

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"

	engine "github.com/parable-work/superschematic/runtime/engine/go"
	"github.com/parable-work/superschematic/runtime/engine/go/enginehttp"
)

var noteSchema = json.RawMessage(`{
	"kind": "General",
	"name": "Note",
	"types": {"Note": {"name": "Note", "role": "EmbeddedStruct", "fields": [
		{"name": "body", "typeRef": {"name": "string"}, "required": true}
	]}}
}`)

func openNotes(t *testing.T) *engine.Engine {
	t.Helper()
	e, err := engine.Open(engine.Config{Path: filepath.Join(t.TempDir(), "engine.db"), Policy: engine.AllowAll()})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = e.Close() })
	if _, err := e.Deploy(context.Background(), []json.RawMessage{noteSchema}, engine.DeployOptions{}); err != nil {
		t.Fatal(err)
	}
	return e
}

// bearer is the tests' authentication: a bearer token is the subject.
func bearer(r *http.Request) engine.Caller {
	subject, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
	if !ok {
		return engine.Caller{}
	}
	return engine.Caller{Principal: &engine.Principal{Subject: subject}}
}

func mark(name string) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Add("X-Wrapped", name)
			next.ServeHTTP(w, r)
		})
	}
}

func send(t *testing.T, method, url, body string, headers ...string) (*http.Response, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	for i := 0; i+1 < len(headers); i += 2 {
		req.Header.Set(headers[i], headers[i+1])
	}
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	data, _ := io.ReadAll(response.Body)
	return response, string(data)
}

func TestAMountedHandlerServesTheEnginesRoutes(t *testing.T) {
	e := openNotes(t)
	router := chi.NewRouter()
	router.Mount("/engine/notes", enginehttp.Handler(e, enginehttp.Options{
		Prefix:     "/engine/notes",
		Caller:     bearer,
		Middleware: []func(http.Handler) http.Handler{mark("all")},
		Timeout:    mark("timeout"),
	}))
	server := httptest.NewServer(router)
	defer server.Close()
	base := server.URL + "/engine/notes/namespaces/default/schemas/Note/instances"

	response, body := send(t, "POST", base, `{"id": "n1", "data": {"body": "hello"}}`,
		"Authorization", "Bearer alice", "Content-Type", "application/json", "X-Request-Id", "r1")
	if response.StatusCode != 201 {
		t.Fatalf("create: %d %s", response.StatusCode, body)
	}
	if got := response.Header.Get("Location"); got != "/engine/notes/namespaces/default/schemas/Note/instances/n1" {
		t.Fatalf("location = %q", got)
	}
	if response.Header.Get("ETag") != `"1"` || response.Header.Get("X-Request-Id") != "r1" {
		t.Fatalf("headers = %v", response.Header)
	}
	if got := response.Header.Values("X-Wrapped"); strings.Join(got, ",") != "all,timeout" {
		t.Fatalf("a route takes every middleware and the timeout: %v", got)
	}
	if !strings.Contains(body, `"requestId":"r1"`) {
		t.Fatalf("body = %s", body)
	}

	response, body = send(t, "GET", base+"/n1", "", "Authorization", "Bearer alice")
	if response.StatusCode != 200 || !strings.Contains(body, `"body":"hello"`) {
		t.Fatalf("get: %d %s", response.StatusCode, body)
	}
	id := response.Header.Get("X-Request-Id")
	if !regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$`).MatchString(id) {
		t.Fatalf("a request without an id gets a UUID: %q", id)
	}

	response, body = send(t, "GET", base+"/n1", "")
	if response.StatusCode != 401 || response.Header.Get("Content-Type") != "application/problem+json" {
		t.Fatalf("no caller: %d %s", response.StatusCode, body)
	}

	response, body = send(t, "GET", server.URL+"/engine/notes/nothing", "", "Authorization", "Bearer alice")
	if response.StatusCode != 404 || !strings.Contains(body, `"code":"not_found"`) {
		t.Fatalf("a path that names no route: %d %s", response.StatusCode, body)
	}
	response, _ = send(t, "PUT", base+"/n1", "{}", "Authorization", "Bearer alice")
	if response.StatusCode != 404 {
		t.Fatalf("a method the route does not take: %d", response.StatusCode)
	}
}

func TestABodyPastTheLimitIs413(t *testing.T) {
	e := openNotes(t)
	server := httptest.NewServer(enginehttp.Handler(e, enginehttp.Options{Caller: bearer, BodyLimitBytes: 16}))
	defer server.Close()
	response, body := send(t, "POST", server.URL+"/namespaces/default/schemas/Note/instances",
		`{"data": {"body": "longer than sixteen bytes"}}`, "Authorization", "Bearer alice", "Content-Type", "application/json")
	if response.StatusCode != 413 {
		t.Fatalf("status = %d %s", response.StatusCode, body)
	}
	var problem map[string]any
	if err := json.Unmarshal([]byte(body), &problem); err != nil {
		t.Fatal(err)
	}
	if problem["code"] != "payload_too_large" || problem["title"] != "Request Entity Too Large" || problem["type"] != "about:blank" {
		t.Fatalf("problem = %v", problem)
	}
}

func TestAHandlerServesUnderChisHandleAndAtTheRoot(t *testing.T) {
	e := openNotes(t)
	router := chi.NewRouter()
	router.Handle("/api/*", enginehttp.Handler(e, enginehttp.Options{Prefix: "/api", Caller: bearer}))
	server := httptest.NewServer(router)
	defer server.Close()
	response, body := send(t, "GET", server.URL+"/api/namespaces/default/schemas", "", "Authorization", "Bearer alice")
	if response.StatusCode != 200 || !strings.Contains(body, `"Note"`) {
		t.Fatalf("under Handle: %d %s", response.StatusCode, body)
	}
	root := httptest.NewServer(enginehttp.Handler(e, enginehttp.Options{Caller: bearer}))
	defer root.Close()
	response, body = send(t, "GET", root.URL+"/namespaces", "", "Authorization", "Bearer alice")
	if response.StatusCode != 200 || !strings.Contains(body, `"default"`) {
		t.Fatalf("at the root: %d %s", response.StatusCode, body)
	}
}

func TestAHandlerServesTheRoutesItIsGiven(t *testing.T) {
	e := openNotes(t)
	server := httptest.NewServer(enginehttp.Handler(e, enginehttp.Options{Caller: bearer, Serve: enginehttp.ServeMCP}))
	defer server.Close()
	response, _ := send(t, "GET", server.URL+"/namespaces", "", "Authorization", "Bearer alice")
	if response.StatusCode != 404 {
		t.Fatalf("an MCP handler serves no API route: %d", response.StatusCode)
	}
	response, body := send(t, "POST", server.URL+"/namespaces/default/mcp",
		`{"jsonrpc": "2.0", "id": 1, "method": "tools/list", "params": {"_meta": {
			"io.modelcontextprotocol/protocolVersion": "2026-07-28",
			"io.modelcontextprotocol/clientInfo": {"name": "enginehttp-test", "version": "1.0.0"},
			"io.modelcontextprotocol/clientCapabilities": {}}}}`,
		"Authorization", "Bearer alice", "Content-Type", "application/json",
		"Accept", "application/json, text/event-stream", "MCP-Protocol-Version", "2026-07-28")
	if response.StatusCode != 200 || !strings.Contains(body, `"list_schemas"`) {
		t.Fatalf("tools/list: %d %s", response.StatusCode, body)
	}
}

// requestLog sets the response's X-Request-Id before the handler runs, as a
// server's request log does (the HTTP runtime's middleware.Logger).
func requestLog(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-Request-Id", r.Header.Get("X-Request-Id"))
		next.ServeHTTP(w, r)
	})
}

func TestAnEventStreamIsCopiedAsItGoesAndEndsWithTheEngine(t *testing.T) {
	e := openNotes(t)
	server := httptest.NewServer(requestLog(enginehttp.Handler(e, enginehttp.Options{
		Caller:  bearer,
		Timeout: mark("timeout"),
		Stream:  engine.StreamOptions{HeartbeatMs: 60000},
	})))
	defer server.Close()
	req, _ := http.NewRequest("GET", server.URL+"/namespaces/default/events", nil)
	req.Header.Set("Authorization", "Bearer alice")
	req.Header.Set("Accept", "text/event-stream")
	req.Header.Set("X-Request-Id", "s1")
	response, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer response.Body.Close()
	if response.StatusCode != 200 || response.Header.Get("Content-Type") != "text/event-stream" {
		t.Fatalf("stream: %d %v", response.StatusCode, response.Header)
	}
	if response.Header.Get("X-Wrapped") != "" {
		t.Fatal("a stream takes no timeout")
	}
	// The engine's X-Request-Id replaces the one the request log set, which
	// a stream, with no timeout to copy its headers, would otherwise repeat.
	if got := response.Header.Values("X-Request-Id"); len(got) != 1 || got[0] != "s1" {
		t.Fatalf("the stream's X-Request-Id = %q, want s1 once", got)
	}
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(response.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	until := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("the stream ended before %q", want)
				}
				if strings.Contains(line, want) {
					return
				}
			case <-deadline:
				t.Fatalf("no %q within 10s", want)
			}
		}
	}
	until(": open")
	until("event: ready")
	if _, err := e.Call(context.Background(), "instances.create", &engine.Principal{Subject: "alice"},
		"Note", map[string]any{"body": "live"}, map[string]any{"id": "n2"}); err != nil {
		t.Fatal(err)
	}
	until(`"instanceId":"n2"`)
	if err := e.Close(); err != nil {
		t.Fatal(err)
	}
	deadline := time.After(10 * time.Second)
	for {
		select {
		case _, ok := <-lines:
			if !ok {
				return
			}
		case <-deadline:
			t.Fatal("the stream went on after the engine closed")
		}
	}
}

func TestAClosedEngineIs503(t *testing.T) {
	e := openNotes(t)
	server := httptest.NewServer(enginehttp.Handler(e, enginehttp.Options{Caller: bearer}))
	defer server.Close()
	_ = e.Close()
	response, body := send(t, "GET", server.URL+"/namespaces", "", "Authorization", "Bearer alice")
	if response.StatusCode != 503 || !strings.Contains(body, `"code":"unavailable"`) {
		t.Fatalf("closed: %d %s", response.StatusCode, body)
	}
}

// TestABackgroundEnginesRoutes: until a Background engine opens, every
// route under the prefix is 503 unavailable; once it opens, the engine's
// routes answer; and closing Done ends an event stream in flight, as a
// server's shutdown does.
func TestABackgroundEnginesRoutes(t *testing.T) {
	path := filepath.Join(t.TempDir(), "engine.db")
	release := make(chan struct{})
	b := engine.OpenInBackground(engine.Config{Path: path, Policy: engine.AllowAll()}, engine.BackgroundOptions{
		Setup: func(ctx context.Context, e *engine.Engine) error {
			<-release
			_, err := e.Deploy(ctx, []json.RawMessage{noteSchema}, engine.DeployOptions{})
			return err
		},
	})
	defer b.Close()
	done := make(chan struct{})
	router := chi.NewRouter()
	router.Mount("/engine/notes", enginehttp.Background(b, enginehttp.Options{Prefix: "/engine/notes", Caller: bearer, Done: done}))
	server := httptest.NewServer(router)
	defer server.Close()
	base := server.URL + "/engine/notes/namespaces/default/schemas/Note/instances"

	response, body := send(t, "GET", base, "", "Authorization", "Bearer alice", "X-Request-Id", "r1")
	if response.StatusCode != 503 || !strings.Contains(body, `"code":"unavailable"`) || !strings.Contains(body, `"requestId":"r1"`) {
		t.Fatalf("before the engine opens: %d %s", response.StatusCode, body)
	}
	close(release)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if _, err := b.Wait(ctx); err != nil {
		t.Fatal(err)
	}
	response, body = send(t, "POST", base, `{"id": "n1", "data": {"body": "hello"}}`, "Authorization", "Bearer alice", "Content-Type", "application/json")
	if response.StatusCode != 201 {
		t.Fatalf("once it opens: %d %s", response.StatusCode, body)
	}

	req, err := http.NewRequest("GET", server.URL+"/engine/notes/namespaces/default/events?after=head", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer alice")
	req.Header.Set("Accept", "text/event-stream")
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatalf("the stream answered %d", stream.StatusCode)
	}
	ended := make(chan error, 1)
	go func() {
		_, err := io.Copy(io.Discard, stream.Body)
		ended <- err
	}()
	close(done)
	select {
	case <-ended:
	case <-time.After(10 * time.Second):
		t.Fatal("the stream did not end once Done closed")
	}
}

// The signal route and a stream with signals (D77), over the core's
// archive, whose behaviors declare no signal: the route answers the
// engine's refusal, the call by name too, and a stream with signals=1
// replays and goes on as one without. The HTTP corpus's signal cases
// deliver signals through the test kit's archive (make engine-go).
func TestTheSignalRouteAndAStreamWithSignals(t *testing.T) {
	e := openNotes(t)
	server := httptest.NewServer(enginehttp.Handler(e, enginehttp.Options{
		Caller: bearer,
		Stream: engine.StreamOptions{HeartbeatMs: 60000},
	}))
	defer server.Close()
	if _, err := e.Call(context.Background(), "instances.create", &engine.Principal{Subject: "alice"},
		"Note", map[string]any{"body": "hello"}, map[string]any{"id": "n1"}); err != nil {
		t.Fatal(err)
	}
	route := server.URL + "/namespaces/default/schemas/Note/instances/n1/signals/Comments/typing"
	response, body := send(t, "POST", route, `{}`, "Authorization", "Bearer bob", "Content-Type", "application/json")
	if response.StatusCode != 404 || !strings.Contains(body, `"code":"not_found"`) || !strings.Contains(body, "does not compose behavior Comments") {
		t.Fatalf("a signal of a behavior the schema does not compose: %d %s", response.StatusCode, body)
	}
	_, err := e.Call(context.Background(), "sendSignal", &engine.Principal{Subject: "bob"},
		map[string]any{"schema": "Note", "instanceId": "n1", "behavior": "Comments", "name": "typing"})
	var refused *engine.Error
	if !errors.As(err, &refused) || refused.Code != engine.CodeNotFound {
		t.Fatalf("sendSignal: %v", err)
	}
	if engine.CodeRateLimited.Status() != http.StatusTooManyRequests {
		t.Fatalf("rate_limited answers %d", engine.CodeRateLimited.Status())
	}

	req, _ := http.NewRequest("GET", server.URL+"/namespaces/default/events?signals=1", nil)
	req.Header.Set("Authorization", "Bearer alice")
	req.Header.Set("Accept", "text/event-stream")
	stream, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Body.Close()
	if stream.StatusCode != 200 {
		t.Fatalf("a stream with signals: %d", stream.StatusCode)
	}
	lines := make(chan string, 100)
	go func() {
		defer close(lines)
		scanner := bufio.NewScanner(stream.Body)
		for scanner.Scan() {
			lines <- scanner.Text()
		}
	}()
	until := func(want string) {
		t.Helper()
		deadline := time.After(10 * time.Second)
		for {
			select {
			case line, ok := <-lines:
				if !ok {
					t.Fatalf("the stream ended before %q", want)
				}
				if strings.Contains(line, want) {
					return
				}
			case <-deadline:
				t.Fatalf("no %q within 10s", want)
			}
		}
	}
	until(`"instanceId":"n1"`)
	until("event: ready")
	if _, err := e.Call(context.Background(), "instances.create", &engine.Principal{Subject: "alice"},
		"Note", map[string]any{"body": "live"}, map[string]any{"id": "n2"}); err != nil {
		t.Fatal(err)
	}
	until(`"instanceId":"n2"`)
}

// The namespace operation route (D77), over the core's archive, whose
// behaviors declare no namespace operation: the route answers the engine's
// refusal of an operation of another scope and of a behavior it does not
// run, and behaviors.invoke by name too. The test kit's Rust suites run one
// that does (behavior_namespace.rs).
func TestTheNamespaceOperationRoute(t *testing.T) {
	e := openNotes(t)
	server := httptest.NewServer(enginehttp.Handler(e, enginehttp.Options{Caller: bearer}))
	defer server.Close()
	route := server.URL + "/namespaces/default/behaviors/Comments/operations/post"
	response, body := send(t, "POST", route, `{}`, "Authorization", "Bearer bob", "Content-Type", "application/json")
	if response.StatusCode != 404 || !strings.Contains(body, `"code":"not_found"`) || !strings.Contains(body, "Comments's post is an instance operation") {
		t.Fatalf("an instance operation called on the namespace: %d %s", response.StatusCode, body)
	}
	response, body = send(t, "POST", server.URL+"/namespaces/default/behaviors/acme.Nothing/operations/inbox", "", "Authorization", "Bearer bob")
	if response.StatusCode != 404 || !strings.Contains(body, "this engine runs no behavior acme.Nothing") {
		t.Fatalf("a behavior the engine does not run: %d %s", response.StatusCode, body)
	}
	_, err := e.Call(context.Background(), "behaviors.invoke", &engine.Principal{Subject: "bob"},
		map[string]any{"behavior": "Comments", "operation": "post", "params": map[string]any{}})
	var refused *engine.Error
	if !errors.As(err, &refused) || refused.Code != engine.CodeNotFound {
		t.Fatalf("behaviors.invoke: %v", err)
	}
}

// The Acting-For header (D77) is ignored unless Options.ActingFor honors it;
// a function decides per request, given the engine's principal.
func TestTheActingForHeaderIsHonoredOnlyWhenTheOptionSaysSo(t *testing.T) {
	e := openNotes(t)
	var seen []string
	mounts := map[string]func(engine.Principal, string, *http.Request) bool{
		"/ignore": nil,
		"/honor":  enginehttp.HonorActingFor,
		"/decide": func(principal engine.Principal, subject string, _ *http.Request) bool {
			seen = append(seen, principal.Subject+" for "+subject)
			return subject != "carol"
		},
	}
	router := chi.NewRouter()
	for prefix, actingFor := range mounts {
		router.Mount(prefix, enginehttp.Handler(e, enginehttp.Options{Prefix: prefix, Caller: bearer, ActingFor: actingFor}))
	}
	server := httptest.NewServer(router)
	defer server.Close()

	forOf := func(prefix, id, subject string) string {
		t.Helper()
		response, body := send(t, "POST", server.URL+prefix+"/namespaces/default/schemas/Note/instances",
			`{"id": "`+id+`", "data": {"body": "hello"}}`,
			"Authorization", "Bearer agent", "Content-Type", "application/json", "Acting-For", subject)
		if response.StatusCode != 201 {
			t.Fatalf("%s create %s: %d %s", prefix, id, response.StatusCode, body)
		}
		response, body = send(t, "GET", server.URL+prefix+"/namespaces/default/events?schema=Note&limit=500", "", "Authorization", "Bearer agent")
		if response.StatusCode != 200 {
			t.Fatalf("events: %d %s", response.StatusCode, body)
		}
		var page struct {
			Data struct {
				Events []struct {
					InstanceID string `json:"instanceId"`
					Actor      string `json:"actor"`
					For        string `json:"for"`
				} `json:"events"`
			} `json:"data"`
		}
		if err := json.Unmarshal([]byte(body), &page); err != nil {
			t.Fatal(err)
		}
		for _, event := range page.Data.Events {
			if event.InstanceID == id {
				if event.Actor != "agent" {
					t.Fatalf("actor = %q", event.Actor)
				}
				return event.For
			}
		}
		t.Fatalf("no event of %s: %s", id, body)
		return ""
	}
	if got := forOf("/ignore", "n1", "alice"); got != "" {
		t.Fatalf("ignored by default: for = %q", got)
	}
	if got := forOf("/honor", "n2", "alice"); got != "alice" {
		t.Fatalf("honored: for = %q", got)
	}
	if got := forOf("/decide", "n3", "alice"); got != "alice" {
		t.Fatalf("decided yes: for = %q", got)
	}
	if got := forOf("/decide", "n4", "carol"); got != "" {
		t.Fatalf("decided no: for = %q", got)
	}
	if strings.Join(seen, ", ") != "agent for alice, agent for carol" {
		t.Fatalf("the function saw %v", seen)
	}
	// A subject that is the caller's own is the engine's invalid_argument.
	response, body := send(t, "POST", server.URL+"/honor/namespaces/default/schemas/Note/instances",
		`{"id": "n5", "data": {"body": "hello"}}`,
		"Authorization", "Bearer agent", "Content-Type", "application/json", "Acting-For", "agent")
	if response.StatusCode != 400 || !strings.Contains(body, `"code":"invalid_argument"`) {
		t.Fatalf("acting for itself: %d %s", response.StatusCode, body)
	}
}
