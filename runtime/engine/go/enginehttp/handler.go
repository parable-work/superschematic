// Package enginehttp mounts the engine's HTTP API, its event stream and its
// MCP endpoint (D58) on a chi router: one chi route per route the engine
// lists, each handing its request to the engine through the binding and
// copying the response back, an event stream frame by frame.
//
// The engine owns routing, decoding, ETags and preconditions, the success
// envelope and the RFC 9457 problems, the service-caller rule (D37), every
// byte of the event stream and MCP's JSON-RPC. The host keeps what this
// package leaves to its Options: authentication (Caller), request ids,
// middleware such as rate limits, the timeout every route but a stream's
// and MCP's takes, and the body limit of the routes that take a body.
//
//	r := chi.NewRouter()
//	r.Mount("/engine/shop", enginehttp.Handler(e, enginehttp.Options{
//		Prefix:  "/engine/shop",
//		Caller:  authenticate,
//		Timeout: runtimemiddleware.Timeout(10*time.Second, getLogger),
//	}))
//
// An event stream is pulled without waiting (Stream.Poll) whenever the
// engine's one waiter sees a commit (Engine.Changed) or a quarter of the
// heartbeat passes, each frame flushed with http.ResponseController, the
// connection's write deadline lifted for as long as the stream lasts.
package enginehttp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"

	engine "github.com/parable-work/superschematic/runtime/engine/go"
)

// DefaultBodyLimitBytes is the body limit of a route that takes a body
// unless Options set one: the HTTP runtime's.
const DefaultBodyLimitBytes = 1 << 20

// manualBodyLimitBytes bounds the body of a route the body limit does not
// apply to (MCP's), as the TypeScript runtime's manual routes take theirs.
const manualBodyLimitBytes = 16 << 20

// bodyRoutes are the routes whose spec declares a body: the body limit
// applies to them, not to the manual routes (the event stream, MCP).
var bodyRoutes = map[string]bool{
	"createNamespace":       true,
	"defineSchema":          true,
	"createInstance":        true,
	"updateInstance":        true,
	"invokeOperation":       true,
	"invokeSchemaOperation": true,
	"searchSchemas":         true,
}

// Serve is which of the engine's routes a handler serves.
type Serve int

const (
	// ServeAll serves the HTTP API, the event stream and MCP.
	ServeAll Serve = iota
	// ServeAPI serves the HTTP API and the event stream, not MCP.
	ServeAPI
	// ServeMCP serves the MCP endpoint alone.
	ServeMCP
)

func (s Serve) serves(route engine.Route) bool {
	switch s {
	case ServeAPI:
		return !route.MCP
	case ServeMCP:
		return route.MCP
	default:
		return true
	}
}

// Options are what the host keeps.
type Options struct {
	// Prefix is the path the handler is mounted at (/api, or empty): the
	// engine's routes are the path after it, and its Location headers
	// carry it.
	Prefix string
	// Caller is who the host's authentication established for a request:
	// its end user's principal (D50), the calling service (D37), or the
	// host's own refusal, which the engine answers in its place. Nil, or
	// a caller with neither a principal nor a service, is 401 on every
	// route.
	Caller func(*http.Request) engine.Caller
	// RequestID is the id a request's response carries; unless set, the
	// client's X-Request-Id when it is sane, else chi's middleware's, else
	// a random UUID.
	RequestID func(*http.Request) string
	// Middleware wraps every route: rate limits, logging.
	Middleware []func(http.Handler) http.Handler
	// Timeout wraps every route but the event stream's and MCP's, whose
	// responses a timeout must not cut.
	Timeout func(http.Handler) http.Handler
	// BodyLimitBytes is the most a route that takes a body accepts:
	// DefaultBodyLimitBytes unless set, none when negative.
	BodyLimitBytes int64
	// Stream is the event stream's page size and heartbeat.
	Stream engine.StreamOptions
	// MCP is the MCP endpoint's server info and instructions.
	MCP engine.MCPOptions
	// Serve is which routes the handler serves: all of them unless set.
	Serve Serve
	// Done, when set, is closed when the host stops serving: every event
	// stream ends then, as when its client goes away, so a server's
	// shutdown waits for no stream (http.Server.RegisterOnShutdown).
	Done <-chan struct{}
}

// Handler serves the engine's routes. Mount it at Options.Prefix (chi's
// Mount, or Handle with the prefix and /*), or serve it at the root with
// an empty prefix. A path under the prefix that names no route it serves
// is 404, as the engine answers it.
func Handler(e *engine.Engine, options Options) http.Handler {
	h := &handler{e: e, options: options, prefix: strings.TrimSuffix(options.Prefix, "/")}
	h.limit = options.BodyLimitBytes
	if h.limit == 0 {
		h.limit = DefaultBodyLimitBytes
	}
	if options.Caller == nil {
		h.options.Caller = func(*http.Request) engine.Caller { return engine.Caller{} }
	}
	if options.RequestID == nil {
		h.options.RequestID = defaultRequestID
	}
	h.api = &engine.HTTPOptions{}
	if options.Stream != (engine.StreamOptions{}) {
		stream := options.Stream
		h.api.Stream = &stream
	}
	if options.MCP.ServerInfo != nil || options.MCP.Instructions != "" {
		mcp := options.MCP
		h.api.MCP = &mcp
	}
	heartbeat := time.Duration(options.Stream.HeartbeatMs) * time.Millisecond
	if heartbeat <= 0 {
		heartbeat = 5 * time.Second
	}
	h.pollEvery = min(max(heartbeat/4, 50*time.Millisecond), time.Second)
	routes, err := e.Routes(context.Background())
	if err != nil {
		h.broken = err
		return h
	}
	h.mux = chi.NewRouter()
	for _, route := range routes {
		if !options.Serve.serves(route) {
			continue
		}
		wrapped := append([]func(http.Handler) http.Handler(nil), options.Middleware...)
		if options.Timeout != nil && !route.Stream && !route.MCP {
			wrapped = append(wrapped, options.Timeout)
		}
		h.mux.With(wrapped...).Method(route.Method, route.Path, h.route(route))
	}
	h.mux.NotFound(h.notFound)
	h.mux.MethodNotAllowed(h.notFound)
	return h
}

type handler struct {
	e         *engine.Engine
	options   Options
	prefix    string
	limit     int64
	api       *engine.HTTPOptions
	pollEvery time.Duration
	mux       *chi.Mux
	broken    error
}

// ServeHTTP routes a request on the path after the prefix, whether chi
// mounted the handler or not.
func (h *handler) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if h.broken != nil {
		WriteProblem(w, http.StatusServiceUnavailable, "The engine is not available", "unavailable", h.options.RequestID(r), nil)
		return
	}
	path := r.URL.RawPath
	if path == "" {
		path = r.URL.Path
	}
	rest, ok := strings.CutPrefix(path, h.prefix)
	if !ok || (rest != "" && rest[0] != '/') {
		h.notFound(w, r)
		return
	}
	if rest == "" {
		rest = "/"
	}
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		rctx = chi.NewRouteContext()
		r = r.WithContext(context.WithValue(r.Context(), chi.RouteCtxKey, rctx))
	}
	rctx.RoutePath = rest
	h.mux.ServeHTTP(w, r)
}

func (h *handler) notFound(w http.ResponseWriter, r *http.Request) {
	WriteProblem(w, http.StatusNotFound, "Not Found", "not_found", h.options.RequestID(r), nil)
}

// route serves one of the engine's routes.
func (h *handler) route(route engine.Route) http.HandlerFunc {
	limit := int64(manualBodyLimitBytes)
	if bodyRoutes[route.Name] {
		limit = h.limit
	}
	return func(w http.ResponseWriter, r *http.Request) {
		id := h.options.RequestID(r)
		var body []byte
		if r.Body != nil && r.Body != http.NoBody {
			reader := r.Body
			if limit >= 0 {
				reader = io.NopCloser(io.LimitReader(r.Body, limit+1))
			}
			read, err := io.ReadAll(reader)
			if err != nil {
				WriteProblem(w, http.StatusBadRequest, "The request body could not be read", "bad_request", id, nil)
				return
			}
			if limit >= 0 && int64(len(read)) > limit {
				WriteProblem(w, http.StatusRequestEntityTooLarge, "Request body exceeds the accepted size", "payload_too_large", id, nil)
				return
			}
			body = read
		}
		caller := h.options.Caller(r)
		request := engine.HTTPRequest{
			Method:    r.Method,
			Prefix:    h.prefix,
			Path:      r.URL.EscapedPath(),
			Query:     r.URL.RawQuery,
			Headers:   headersOf(r),
			Caller:    caller,
			RequestID: id,
			Options:   h.api,
		}
		response, err := h.e.ServeHTTPRequest(r.Context(), request, body)
		switch {
		case errors.Is(err, engine.ErrClosed):
			WriteProblem(w, http.StatusServiceUnavailable, "The engine is shutting down", "unavailable", id, nil)
			return
		case errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded):
			// The client went away, or the host's timeout passed, while the
			// request waited for a slot.
			WriteProblem(w, http.StatusServiceUnavailable, "The engine is busy", "busy", id, nil)
			return
		case err != nil:
			WriteProblem(w, http.StatusInternalServerError, "An unexpected error occurred", "internal_error", id, nil)
			return
		}
		header := w.Header()
		for _, pair := range response.Headers {
			header.Add(pair[0], pair[1])
		}
		if response.Stream != nil {
			h.stream(w, r, response)
			return
		}
		w.WriteHeader(response.Status)
		_, _ = w.Write(response.Body)
	}
}

// stream copies an event stream to the client until it ends, the client
// goes away or the engine closes.
func (h *handler) stream(w http.ResponseWriter, r *http.Request, response *engine.HTTPResponse) {
	stream := response.Stream
	defer stream.Close()
	control := http.NewResponseController(w)
	// The server's write timeout is for a response, not a stream that
	// lasts as long as its client.
	_ = control.SetWriteDeadline(time.Time{})
	w.WriteHeader(response.Status)
	_ = control.Flush()
	ctx := r.Context()
	timer := time.NewTimer(h.pollEvery)
	defer timer.Stop()
	for {
		select {
		case <-h.options.Done:
			return
		default:
		}
		// Taken before the poll, so a commit while it reads wakes the
		// wait after it.
		changed := h.e.Changed()
		data, over, err := stream.Poll(ctx)
		if err != nil || over {
			return
		}
		if len(data) > 0 {
			if _, err := w.Write(data); err != nil {
				return
			}
			if err := control.Flush(); err != nil {
				return
			}
			continue
		}
		timer.Reset(h.pollEvery)
		select {
		case <-ctx.Done():
			return
		case <-h.options.Done:
			return
		case <-changed:
		case <-timer.C:
		}
	}
}

// headersOf is a request's headers as the engine reads them: lowercase
// names, each one's values joined.
func headersOf(r *http.Request) [][2]string {
	headers := make([][2]string, 0, len(r.Header))
	for name, values := range r.Header {
		headers = append(headers, [2]string{strings.ToLower(name), strings.Join(values, ", ")})
	}
	return headers
}

func defaultRequestID(r *http.Request) string {
	if id := strings.TrimSpace(r.Header.Get("X-Request-Id")); id != "" && len(id) <= 128 && !strings.ContainsAny(id, "\r\n") {
		return id
	}
	if id := middleware.GetReqID(r.Context()); id != "" {
		return id
	}
	return newUUID()
}

// newUUID is a random version 4 UUID.
func newUUID() string {
	var b [16]byte
	_, _ = rand.Read(b[:])
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
}

// WriteProblem writes an RFC 9457 problem document as the engine writes
// one: type about:blank, the status's text as its title, the detail, the
// code, the request's id and, when given, details. A host refusing a
// request before the engine sees it (a rate limit, say) answers with it.
func WriteProblem(w http.ResponseWriter, status int, detail, code, requestID string, details any) {
	problem := struct {
		Type      string `json:"type"`
		Title     string `json:"title"`
		Status    int    `json:"status"`
		Detail    string `json:"detail"`
		Code      string `json:"code,omitempty"`
		RequestID string `json:"requestId"`
		Details   any    `json:"details,omitempty"`
	}{"about:blank", http.StatusText(status), status, detail, code, requestID, details}
	if problem.Title == "" {
		problem.Title = "Error"
	}
	var body bytes.Buffer
	encoder := json.NewEncoder(&body)
	encoder.SetEscapeHTML(false)
	_ = encoder.Encode(problem)
	header := w.Header()
	header.Set("Content-Type", "application/problem+json")
	header.Set("Cache-Control", "no-store")
	header.Set("X-Request-Id", requestID)
	w.WriteHeader(status)
	_, _ = w.Write(bytes.TrimSuffix(body.Bytes(), []byte("\n")))
}

// Background serves the engine's routes for an engine a server opens in
// the background (engine.Background): until it opens, every request under
// the prefix is 503 unavailable ("The engine is not available"), and once
// it opens, the routes Handler serves with options. Mount it at
// options.Prefix as Handler.
func Background(b *engine.Background, options Options) http.Handler {
	requestID := options.RequestID
	if requestID == nil {
		requestID = defaultRequestID
	}
	pending := &pending{b: b, options: options, requestID: requestID}
	return pending
}

// pending answers 503 until its engine opens, then hands every request to
// the engine's Handler, built once.
type pending struct {
	b         *engine.Background
	options   Options
	requestID func(*http.Request) string

	once    sync.Once
	handler http.Handler
}

func (p *pending) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	e, err := p.b.Engine()
	if err != nil {
		WriteProblem(w, http.StatusServiceUnavailable, "The engine is not available", "unavailable", p.requestID(r), nil)
		return
	}
	p.once.Do(func() { p.handler = Handler(e, p.options) })
	p.handler.ServeHTTP(w, r)
}
