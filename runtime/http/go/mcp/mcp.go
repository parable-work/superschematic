// Package mcp is the MCP endpoint of a generated Go API (D81, D116, #660):
// POST /api/mcp serves the API's visible @mcp operations as MCP tools over
// the streamable HTTP transport, statelessly, as the TypeScript runtime's
// mcp.ts and the Rust runtime's mcp module serve them. The three are held
// to the exchanges the engine recorded before it went (D103, #587),
// runtime/engine/testdata/http/mcp.json.
//
//   - The 2025 revisions (a request with no per-request _meta envelope):
//     initialize, negotiating one of InitializeVersions, ping, tools/list
//     and tools/call, alone or in a batch. A request answers 200 with a
//     text/event-stream body of one message event per request;
//     notifications and posted responses alone answer 202.
//   - 2026-07-28 (a request whose params._meta carries the
//     io.modelcontextprotocol/protocolVersion claim): server/discover,
//     ping, tools/list and tools/call, each answered as one JSON body,
//     every result marked complete and stamped with the server's identity.
//   - GET and DELETE answer 405: a stateless server keeps no session and
//     opens no stream.
//
// Serve is the transport over any ToolSet. Mount is the endpoint of a
// generated router (mount.go): a tool set whose call is a request to the
// tool's route, served through the router in process as the caller the
// endpoint's own request authenticated, so the route's rate limit, body
// limit, service step, permission check, validation and timeout apply
// unchanged. A call the route refuses returns a tool error (isError) whose
// structured content is the problem document the route answered; a tool
// the caller has not got is a JSON-RPC invalid-params error.
//
// A tool whose invocation policy is ask runs only once the person confirms
// the call (D93, confirm.go): a 2026-07-28 client whose envelope's
// capabilities elicit forms is asked in band (input_required with one
// elicitation/create); any other client gets a two-step confirmation, a
// tool error confirmation_required whose details hold the summary and a
// confirmationToken, which the same call sends back in its
// confirmationToken argument to run. Only a call that would reach the
// tool's implementation is confirmed (D98): before the endpoint mints a
// confirmation, asks a client to elicit, or redeems a token, it checks the
// call (ToolSet.Check), on a generated router through its route's own
// steps and decoding, and answers a refusal at once.
package mcp

import (
	"context"
	"encoding/json"
	"errors"
)

// InitializeVersions are the 2025 revisions initialize negotiates, the
// newest first: the one a client asks for when it is here, else the first.
var InitializeVersions = []string{"2025-11-25", "2025-06-18", "2025-03-26", "2024-11-05", "2024-10-07"}

// DiscoverVersions are the revisions server/discover offers.
var DiscoverVersions = []string{"2026-07-28"}

const (
	// MaxBodyBytes is the largest body the endpoint reads, the official
	// SDK's limit.
	MaxBodyBytes = 4 * 1024 * 1024
	// MaxBatch is the most messages a batch holds.
	MaxBatch = 100
	// Path is where a generated router serves its MCP endpoint.
	Path = "/api/mcp"
)

// ServerInfo is the name and version initialize and every 2026-07-28
// result report.
type ServerInfo struct {
	Name    string `json:"name"`
	Version string `json:"version"`
}

// Options are what the transport says about the server, and what one
// request brings to it: its caller and its id.
type Options struct {
	ServerInfo ServerInfo
	// Instructions initialize gives the client; none when empty.
	Instructions string
	// Confirmations mints and redeems the confirmations of the tools the
	// tool set's Confirms names (D93). Without it a call of such a tool is
	// refused and runs nothing.
	Confirmations *Confirmations
	// Caller is who is calling, which a confirmation is bound to
	// (CallerOf); empty for no one.
	Caller string
	// RequestID is the request id a confirmation's problem carries; a
	// fresh one unless given.
	RequestID string
	// Log hears a tool set's failure, which the wire answers as the
	// internal error with no message of its own; nil drops it.
	Log func(err error)
}

// Tool is a tool as tools/list answers it.
type Tool struct {
	Name        string          `json:"name"`
	Title       string          `json:"title,omitempty"`
	Description string          `json:"description,omitempty"`
	InputSchema json.RawMessage `json:"inputSchema"`
	Annotations json.RawMessage `json:"annotations,omitempty"`
	Meta        json.RawMessage `json:"_meta,omitempty"`
}

// Content is one item of a tool result's content: text, or a resource
// whose bytes are base64.
type Content struct {
	Type     string           `json:"type"`
	Text     string           `json:"text,omitempty"`
	Resource *ContentResource `json:"resource,omitempty"`
}

// ContentResource is an embedded resource.
type ContentResource struct {
	URI      string `json:"uri"`
	MimeType string `json:"mimeType"`
	Blob     string `json:"blob"`
}

// CallOutcome is what a tool call came to: a JSON result, sent as text
// and, for an object, as structured content; content of another kind,
// sent as it is; or a refusal, a problem document, sent as a tool error
// whose structured content it is. Exactly one is set; none is the result
// null.
type CallOutcome struct {
	Result  json.RawMessage
	Content []Content
	Problem json.RawMessage
}

// ResultOf is the outcome whose result is value's JSON.
func ResultOf(value any) CallOutcome {
	return CallOutcome{Result: marshal(value)}
}

// ProblemOutcome is the outcome that refuses the call with problem.
func ProblemOutcome(problem any) CallOutcome {
	return CallOutcome{Problem: marshal(problem)}
}

// UnknownToolError is a tool the set has not got, or does not offer the
// caller: the JSON-RPC invalid-params error, with its message.
type UnknownToolError struct{ Message string }

func (e *UnknownToolError) Error() string { return e.Message }

// ToolSet is the tools one request is served: those its caller is
// offered, and how a call runs. Arguments are as a decoder gives them,
// numbers as json.Number.
type ToolSet interface {
	// List is the tools the caller is offered.
	List(ctx context.Context) ([]Tool, error)
	// Call runs the tool name with args; an *UnknownToolError for one the
	// set has not got.
	Call(ctx context.Context, name string, args map[string]any) (CallOutcome, error)
	// Confirms is the tool name names when a call of it runs only once the
	// person confirms it (D93); nil for one that runs as called, or one the
	// set has not got. The endpoint confirms the call before it calls
	// Call, which never sees the confirmationToken argument.
	Confirms(ctx context.Context, name string) (*Tool, error)
	// Check is the refusal a call of the tool name with args would meet
	// before its implementation runs, its problem document; nil for a call
	// that would reach it (D98). An *UnknownToolError for a tool the set
	// has not got.
	Check(ctx context.Context, name string, args map[string]any) (json.RawMessage, error)
}

// isUnknown reports whether err is a tool the set has not got.
func isUnknown(err error) (*UnknownToolError, bool) {
	var unknown *UnknownToolError
	ok := errors.As(err, &unknown)
	return unknown, ok
}
