package mcp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimiddleware "github.com/go-chi/chi/v5/middleware"

	"github.com/parable-work/superschematic/runtime/http/go/requestctx"
	"github.com/parable-work/superschematic/runtime/http/go/response"
)

// CallGate holds a call of a generated router's tool before its route runs
// (D158, D183): the agent dispatcher's, which holds an agent run's call to
// the run's autonomy by the tool's @autonomy (agents.Agents.MCPCallGate).
// A problem refuses the call with it; nil lets it run.
type CallGate interface {
	CheckCall(ctx context.Context, tool RouteTool, args map[string]any) (json.RawMessage, error)
}

// CallGateFunc is a CallGate of a function.
type CallGateFunc func(ctx context.Context, tool RouteTool, args map[string]any) (json.RawMessage, error)

// CheckCall implements CallGate.
func (f CallGateFunc) CheckCall(ctx context.Context, tool RouteTool, args map[string]any) (json.RawMessage, error) {
	return f(ctx, tool, args)
}

// ServedTool is a tool the endpoint serves beside its routes' (D152,
// D155): the calendar and search across tables, which read as the caller
// themselves. It is offered to every caller the endpoint admits, and its
// call runs with the endpoint request's context, whose caller it holds to
// what that caller reads. A refusal is its problem's status, title and
// detail; none of them confirms or is checked before its call.
type ServedTool struct {
	Tool Tool
	// Call runs a call with args, numbers as float64 (PlainArgs): its
	// result, or its refusal.
	Call func(ctx context.Context, args map[string]any) (any, *ServedProblem, error)
}

// ServedProblem is a served tool's refusal.
type ServedProblem struct {
	Status int
	Title  string
	Detail string
}

// EndpointOptions are what the API's Config says of its MCP endpoint.
type EndpointOptions struct {
	// ServerInfo is the name and version initialize reports; the API's
	// name and its served OpenAPI version unless set.
	ServerInfo *ServerInfo
	// Instructions initialize gives the client.
	Instructions string
	// ConfirmationKey is the key the endpoint signs its confirmations with
	// (D93): at least 32 bytes, random, the same on every replica.
	// Required when a tool confirms; the generated entrypoint passes the
	// @envVars secret outputs.api.mcpConfirmationKey names.
	ConfirmationKey []byte
	// ConfirmationTTL is how long a confirmation is good for;
	// DefaultConfirmationTTL unless set.
	ConfirmationTTL time.Duration
	// ConfirmationStore is where a confirmation is claimed so it redeems
	// once; one in process memory unless given, so a token is single-use
	// per replica.
	ConfirmationStore ConfirmationStore
	// CallGate checks every call before its route runs (D158), after the
	// person's confirmation (D93): an agent run's autonomy policy.
	CallGate CallGate
	// Tools are the tools served beside the routes' (ServedTool). A name a
	// route's tool takes is refused at mount.
	Tools []ServedTool
	// Log hears a tool's failure, which the wire answers as the internal
	// error.
	Log func(err error)
}

// Mount is what the generated router says about its endpoint.
type Mount struct {
	// API is the API's name: the server's default name, and the name in
	// an unknown tool's error.
	API string
	// Version is the server's default version.
	Version string
	// RequiresAuth is true when a route of the API needs a caller, so the
	// endpoint does too and answers 401 without one.
	RequiresAuth bool
	// Tools are the API's tools, as the build wrote them.
	Tools []RouteTool
	// Routes is the router a call of a tool is a request to: the API's
	// own, whose steps run for the call as for any request.
	Routes http.Handler
	// Permits are each tool's route's permission check, by the tool's
	// name, which tools/list asks of the caller: a tool is offered when
	// the check would let the caller through. A tool with none is offered
	// to every caller the endpoint admits.
	Permits map[string]func(http.Handler) http.Handler
	// Checked are the tools whose route stops at its implementation for a
	// route's check (requestctx.WithRouteCheck, D98): a generated handler,
	// which calls CheckContext before the implementation. A tool that asks
	// and is not here, a manual route's or the user model's, is confirmed
	// unchecked.
	Checked map[string]bool
}

type toolCallKey struct{}

// ToolCallOf is the tool whose call ctx's request is, when the endpoint
// made the request for a tool's call or its check; nil for any other. An
// authenticator that holds a request to a policy of its own leaves a call
// the endpoint's CallGate held alone (agents.Authenticator).
func ToolCallOf(ctx context.Context) *RouteTool {
	tool, _ := ctx.Value(toolCallKey{}).(*RouteTool)
	return tool
}

// Handler is the endpoint over m with opts, which a generated router
// mounts at POST, GET and DELETE /api/mcp; an error when two tools share a
// name, when a tool that asks takes the confirmation argument, and when a
// tool confirms and opts has no confirmation key or a short one.
func (m Mount) Handler(opts EndpointOptions) (http.Handler, error) {
	byName := map[string]*RouteTool{}
	var confirming string
	for i := range m.Tools {
		tool := &m.Tools[i]
		if byName[tool.Name] != nil {
			return nil, fmt.Errorf("mcp: two tools are named %s", tool.Name)
		}
		for _, argument := range tool.Arguments {
			if tool.Confirm && argument.Key == ConfirmationArgument {
				return nil, fmt.Errorf("mcp: tool %s asks the person before it runs, and takes an argument %s, which carries its confirmation", tool.Name, ConfirmationArgument)
			}
		}
		if tool.Confirm && confirming == "" {
			confirming = tool.Name
		}
		byName[tool.Name] = tool
	}
	served := map[string]ServedTool{}
	for _, extra := range opts.Tools {
		if byName[extra.Tool.Name] != nil || served[extra.Tool.Name].Call != nil {
			return nil, fmt.Errorf("mcp: two tools are named %s", extra.Tool.Name)
		}
		served[extra.Tool.Name] = extra
	}
	var confirmations *Confirmations
	if confirming != "" {
		if len(opts.ConfirmationKey) == 0 {
			return nil, fmt.Errorf("mcp: tool %s runs only once the person confirms it, so the MCP endpoint's ConfirmationKey is required: a random secret of at least %d bytes, the same on every replica (D93)", confirming, MinConfirmationKeyBytes)
		}
		var err error
		if confirmations, err = NewConfirmations(ConfirmationOptions{Key: opts.ConfirmationKey, Scope: m.API, TTL: opts.ConfirmationTTL, Store: opts.ConfirmationStore}); err != nil {
			return nil, err
		}
	}
	info := ServerInfo{Name: m.API, Version: m.Version}
	if opts.ServerInfo != nil {
		info = *opts.ServerInfo
	}
	endpoint := &endpoint{mount: m, byName: byName, served: served, order: opts.Tools, gate: opts.CallGate}
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		if m.RequiresAuth && CallerOf(ctx) == `[null,null]` {
			response.RequestError(w, r, http.StatusUnauthorized, "Authentication required", "unauthorized")
			return
		}
		requestID := chimiddleware.GetReqID(ctx)
		if requestID == "" {
			requestID = newRequestID()
		}
		Serve(w, r, &requestTools{endpoint: endpoint, request: r, requestID: requestID}, Options{
			ServerInfo:    info,
			Instructions:  opts.Instructions,
			Confirmations: confirmations,
			Caller:        CallerOf(ctx),
			RequestID:     requestID,
			Log:           opts.Log,
		})
	}), nil
}

// endpoint is a mounted endpoint: what every request to it is served with.
type endpoint struct {
	mount  Mount
	byName map[string]*RouteTool
	served map[string]ServedTool
	order  []ServedTool
	gate   CallGate
}

// requestTools are the tools one request to the endpoint is served, as
// its caller.
type requestTools struct {
	endpoint  *endpoint
	request   *http.Request
	requestID string
}

// offered reports whether the caller may call tool's route: its
// permission check lets the request through.
func (t *requestTools) offered(tool *RouteTool) bool {
	permit := t.endpoint.mount.Permits[tool.Name]
	if permit == nil {
		return true
	}
	reached := false
	permit(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { reached = true })).ServeHTTP(discard{}, t.request)
	return reached
}

func (t *requestTools) tool(name string) (*RouteTool, error) {
	tool := t.endpoint.byName[name]
	if tool == nil || !t.offered(tool) {
		return nil, &UnknownToolError{Message: t.endpoint.mount.API + " has no tool " + string(marshal(name))}
	}
	return tool, nil
}

func (t *requestTools) List(context.Context) ([]Tool, error) {
	tools := []Tool{}
	for i := range t.endpoint.mount.Tools {
		tool := &t.endpoint.mount.Tools[i]
		if t.offered(tool) {
			tools = append(tools, tool.Listed())
		}
	}
	for _, extra := range t.endpoint.order {
		tools = append(tools, extra.Tool)
	}
	return tools, nil
}

func (t *requestTools) Confirms(_ context.Context, name string) (*Tool, error) {
	if _, ok := t.endpoint.served[name]; ok {
		return nil, nil
	}
	tool := t.endpoint.byName[name]
	if tool == nil || !tool.Confirm || !t.offered(tool) {
		return nil, nil
	}
	listed := tool.Tool
	return &listed, nil
}

// Check is the call's request through its route's check (D98): the same
// request Call makes, through every step of the route and its decoding,
// stopped before the implementation.
func (t *requestTools) Check(ctx context.Context, name string, args map[string]any) (json.RawMessage, error) {
	if _, ok := t.endpoint.served[name]; ok {
		return nil, nil
	}
	tool, err := t.tool(name)
	if err != nil {
		return nil, err
	}
	if !t.endpoint.mount.Checked[tool.Name] {
		return nil, nil
	}
	reached := false
	request, refusal := ToolRequest(requestctx.WithRouteCheck(t.callContext(ctx, tool), &reached), *tool, args, t.request)
	if refusal != nil {
		return t.refusalProblem(refusal), nil
	}
	answer := t.serve(request)
	if reached || answer.status < 400 {
		return nil, nil
	}
	return ProblemOf(answer.status, answer.header, answer.body.Bytes(), t.requestID), nil
}

func (t *requestTools) Call(ctx context.Context, name string, args map[string]any) (CallOutcome, error) {
	if extra, ok := t.endpoint.served[name]; ok {
		result, problem, err := extra.Call(ctx, PlainArgs(args))
		if err != nil {
			return CallOutcome{}, err
		}
		if problem != nil {
			return ProblemOutcome(Problem{Type: "about:blank", Title: problem.Title, Status: problem.Status, Detail: problem.Detail, RequestID: t.requestID}), nil
		}
		return ResultOf(result), nil
	}
	tool, err := t.tool(name)
	if err != nil {
		return CallOutcome{}, err
	}
	request, refusal := ToolRequest(t.callContext(ctx, tool), *tool, args, t.request)
	if refusal != nil {
		return CallOutcome{Problem: t.refusalProblem(refusal)}, nil
	}
	if t.endpoint.gate != nil {
		problem, err := t.endpoint.gate.CheckCall(ctx, *tool, args)
		if err != nil {
			return CallOutcome{}, err
		}
		if problem != nil {
			return CallOutcome{Problem: problem}, nil
		}
	}
	answer := t.serve(request)
	return OutcomeOf(answer.status, answer.header, answer.body.Bytes(), t.requestID, request.URL.EscapedPath()), nil
}

// callContext is the context of a call's request: the endpoint request's,
// with no route of its own, so the router routes the call afresh, and
// naming the tool.
func (t *requestTools) callContext(ctx context.Context, tool *RouteTool) context.Context {
	ctx = context.WithValue(ctx, chi.RouteCtxKey, (*chi.Context)(nil))
	return context.WithValue(ctx, toolCallKey{}, tool)
}

func (t *requestTools) refusalProblem(refusal *RequestRefusal) json.RawMessage {
	return marshal(Problem{Type: "about:blank", Title: http.StatusText(refusal.Status), Status: refusal.Status, Detail: refusal.Detail, Code: refusal.Code, RequestID: t.requestID})
}

// serve runs request through the router in process.
func (t *requestTools) serve(request *http.Request) *recorder {
	answer := &recorder{header: http.Header{}, status: http.StatusOK}
	t.endpoint.mount.Routes.ServeHTTP(answer, request)
	return answer
}

// recorder is a response the router writes in process.
type recorder struct {
	header      http.Header
	status      int
	wroteHeader bool
	body        bytes.Buffer
}

func (r *recorder) Header() http.Header { return r.header }

func (r *recorder) WriteHeader(status int) {
	if r.wroteHeader {
		return
	}
	r.status, r.wroteHeader = status, true
}

func (r *recorder) Write(data []byte) (int, error) {
	r.wroteHeader = true
	return r.body.Write(data)
}

// Flush lets a handler that flushes write on.
func (r *recorder) Flush() {}

// discard is a response nothing reads: a permission check's probe.
type discard struct{}

func (discard) Header() http.Header         { return http.Header{} }
func (discard) Write(b []byte) (int, error) { return len(b), nil }
func (discard) WriteHeader(int)             {}

// ToolOf is a tool as a runtime package describes it, a map of what
// tools/list answers (crosstable.CalendarMCPTool, crosstable.SearchMCPTool).
func ToolOf(spec map[string]any) Tool {
	tool := Tool{InputSchema: json.RawMessage(`{"type":"object"}`)}
	tool.Name, _ = spec["name"].(string)
	tool.Title, _ = spec["title"].(string)
	tool.Description, _ = spec["description"].(string)
	if schema, ok := spec["inputSchema"]; ok {
		tool.InputSchema = marshal(schema)
	}
	if annotations, ok := spec["annotations"]; ok {
		tool.Annotations = marshal(annotations)
	}
	if meta, ok := spec["_meta"]; ok {
		tool.Meta = marshal(meta)
	}
	return tool
}
