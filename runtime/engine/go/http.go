package engine

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// HTTPRequest is a request the host hands the engine's routes: what it
// received, the caller its authentication established, the id it gave the
// request, and how the mount serves.
type HTTPRequest struct {
	Method string `json:"method"`
	// Prefix is the path the host mounts the engine at (/api, or empty):
	// routes match the path after it.
	Prefix string `json:"prefix,omitempty"`
	// Path is the path as the host received it, prefix and all, still
	// percent-encoded.
	Path string `json:"path"`
	// Query is the query string without ?.
	Query string `json:"query,omitempty"`
	// Headers are the request's headers, names in any case.
	Headers   [][2]string  `json:"headers,omitempty"`
	Caller    Caller       `json:"caller"`
	RequestID string       `json:"requestId,omitempty"`
	Options   *HTTPOptions `json:"options,omitempty"`
}

// Caller is who the host established for a request.
type Caller struct {
	// Principal is what the deployment's authenticator returned for the
	// end user; nil for none (401 unless a service stands in, D37).
	Principal *Principal `json:"principal,omitempty"`
	// Service is the calling service the host's service step verified.
	Service *ServiceCaller `json:"service,omitempty"`
	// Refused is the host's own refusal, which the engine answers in its
	// place on a route it serves.
	Refused *Refusal `json:"refused,omitempty"`
}

// ServiceCaller is a verified calling service (D37).
type ServiceCaller struct {
	Deployable string   `json:"deployable"`
	Serves     []string `json:"serves"`
	Subject    string   `json:"subject"`
}

// Refusal is a refusal the host's authentication made: its status, its
// code (service_unauthorized, service_forbidden, unauthorized) and,
// optionally, its detail.
type Refusal struct {
	Status int    `json:"status"`
	Code   string `json:"code"`
	Detail string `json:"detail,omitempty"`
}

// HTTPOptions are how a mount serves: the event stream's page size and
// heartbeat, the MCP endpoint's server info and instructions, and whether
// the request's Acting-For header is honored (D77).
type HTTPOptions struct {
	Stream *StreamOptions `json:"stream,omitempty"`
	MCP    *MCPOptions    `json:"mcp,omitempty"`
	// ActingFor honors the request's Acting-For header: the engine's
	// principal then acts for the subject it names. False ignores it.
	ActingFor bool `json:"actingFor,omitempty"`
}

// StreamOptions are the event stream's page size (1 to 500, 100 unless
// set) and heartbeat (5000 ms unless set).
type StreamOptions struct {
	PageSize    int `json:"pageSize,omitempty"`
	HeartbeatMs int `json:"heartbeatMs,omitempty"`
}

// MCPOptions are the MCP endpoint's server info and instructions.
type MCPOptions struct {
	ServerInfo   *ServerInfo `json:"serverInfo,omitempty"`
	Instructions string      `json:"instructions,omitempty"`
}

// ServerInfo is the MCP server's name and version.
type ServerInfo struct {
	Name    string `json:"name,omitempty"`
	Version string `json:"version,omitempty"`
}

// HTTPResponse is the engine's answer: its status, its headers (lowercase
// names), and its body's bytes or the stream that is its body.
type HTTPResponse struct {
	Status  int
	Headers [][2]string
	Body    []byte
	// Stream is the event stream the response's body is; nil for bytes.
	Stream *Stream
}

// Header is a response header's value, by its lowercase name.
func (r *HTTPResponse) Header(name string) string {
	name = strings.ToLower(name)
	for _, pair := range r.Headers {
		if pair[0] == name {
			return pair[1]
		}
	}
	return ""
}

// ServeHTTPRequest serves one request through the engine's routes.
func (e *Engine) ServeHTTPRequest(ctx context.Context, request HTTPRequest, body []byte) (*HTTPResponse, error) {
	head, err := json.Marshal(request)
	if err != nil {
		return nil, fmt.Errorf("engine: write the request's head: %w", err)
	}
	if err := e.acquire(ctx); err != nil {
		return nil, err
	}
	headOut, bodyOut, c := engHTTP(e.handle, head, body)
	e.release()
	if c != codeOK {
		return nil, failure(c, headOut)
	}
	var answered struct {
		Status  int         `json:"status"`
		Headers [][2]string `json:"headers"`
		Stream  *uint64     `json:"stream"`
	}
	if err := json.Unmarshal(headOut, &answered); err != nil {
		return nil, fmt.Errorf("engine: read the response's head: %w", err)
	}
	response := &HTTPResponse{Status: answered.Status, Headers: answered.Headers, Body: bodyOut}
	if answered.Stream != nil {
		response.Stream = &Stream{e: e, id: *answered.Stream}
	}
	return response, nil
}

// Stream is an event stream the engine holds open for a response: the
// host pulls its bytes and copies them to the client.
type Stream struct {
	e    *Engine
	id   uint64
	once sync.Once
}

// Poll takes the stream's next bytes without waiting: empty when it has
// none yet (wait on the engine's Changed, or the heartbeat, and poll
// again), and over once it ended (the engine closed, the read was refused,
// retention passed it).
func (s *Stream) Poll(ctx context.Context) (data []byte, over bool, err error) {
	if err := s.e.acquire(ctx); err != nil {
		return nil, true, err
	}
	out, c := engStreamPoll(s.e.handle, s.id)
	s.e.release()
	switch c {
	case codeOK:
		return out, false, nil
	case codeClosed:
		return nil, true, nil
	default:
		return nil, true, failure(c, out)
	}
}

// Close ends the stream: the client went away. Closing it again does
// nothing.
func (s *Stream) Close() {
	s.once.Do(func() { engStreamClose(s.e.handle, s.id) })
}

// Route is one route the engine serves, as its routes list them for a
// host's dispatch, CORS and timeout exemptions.
type Route struct {
	// Name is the operation's name (createInstance).
	Name   string `json:"name"`
	Method string `json:"method"`
	// Path is the path after the prefix, its parameters in braces, as chi
	// writes them.
	Path string `json:"path"`
	// Stream is whether its response may be an event stream, which a
	// host's timeout must not cut.
	Stream bool `json:"stream"`
	// MCP is whether it is the MCP endpoint.
	MCP bool `json:"mcp"`
}

// Routes lists every route the engine serves: the HTTP API's 26, then the
// MCP endpoint's POST, GET and DELETE.
func (e *Engine) Routes(ctx context.Context) ([]Route, error) {
	out, err := e.Call(ctx, "api.routes", nil)
	if err != nil {
		return nil, err
	}
	var routes []Route
	if err := json.Unmarshal(out, &routes); err != nil {
		return nil, fmt.Errorf("engine: read the routes: %w", err)
	}
	return routes, nil
}
