package mcp

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"
)

// The first revision that carries its version in each request's
// envelope; revisions are dates, so a later one compares greater.
const firstModern = "2026-07-28"

const (
	versionKey            = "io.modelcontextprotocol/protocolVersion"
	clientInfoKey         = "io.modelcontextprotocol/clientInfo"
	clientCapabilitiesKey = "io.modelcontextprotocol/clientCapabilities"
	serverInfoKey         = "io.modelcontextprotocol/serverInfo"
)

const (
	parseError         = -32700
	invalidRequest     = -32600
	methodNotFound     = -32601
	invalidParams      = -32602
	internalError      = -32603
	serverError        = -32000
	headerMismatch     = -32020
	unsupportedVersion = -32022
)

// rpcError is a JSON-RPC error: its code, message and data.
type rpcError struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    any    `json:"data,omitempty"`
}

func (e *rpcError) Error() string { return e.Message }

func newRPCError(code int, message string, data any) *rpcError {
	return &rpcError{Code: code, Message: message, Data: data}
}

// mismatch is the SDK's refusal of headers and a body that disagree.
func mismatch(header, body string) *rpcError {
	return newRPCError(headerMismatch, "Bad Request: the request headers and body disagree: "+body, map[string]any{"mismatch": map[string]any{"header": header, "body": body}})
}

// Serve serves one request to the MCP endpoint with tools, its caller
// already authenticated: the JSON-RPC exchange as the official SDK's
// stateless transport answers it.
func Serve(w http.ResponseWriter, r *http.Request, tools ToolSet, opts Options) {
	if !strings.EqualFold(r.Method, http.MethodPost) {
		entryError(w, http.StatusMethodNotAllowed, newRPCError(serverError, "Method not allowed.", nil), nil)
		return
	}
	if !isJSONType(r.Header.Get("Content-Type")) {
		entryError(w, http.StatusUnsupportedMediaType, newRPCError(serverError, "Unsupported Media Type: Content-Type must be application/json", nil), nil)
		return
	}
	data, ok := readCapped(r, MaxBodyBytes)
	if !ok {
		entryError(w, http.StatusRequestEntityTooLarge, newRPCError(serverError, fmt.Sprintf("Payload Too Large: Request body must not exceed %d bytes", MaxBodyBytes), nil), nil)
		return
	}
	if !utf8.Valid(data) {
		data = bytes.ToValidUTF8(data, []byte("�"))
	}
	var body any
	parsed := false
	if len(data) > 0 {
		if value, err := decodeJSON(data); err == nil {
			body, parsed = value, true
		}
	}
	if !parsed {
		if refuseAccept(w, r) {
			return
		}
		entryError(w, http.StatusBadRequest, newRPCError(parseError, "Parse error: Invalid JSON", nil), nil)
		return
	}
	versionHeader, hasVersion := header(r, "Mcp-Protocol-Version")
	methodHeader, hasMethod := header(r, "Mcp-Method")
	var method *string
	if hasMethod {
		method = &methodHeader
	}
	var version *string
	if hasVersion {
		version = &versionHeader
	}
	s := &server{tools: tools, opts: opts}
	era := classify(body, version, method)
	switch era.kind {
	case eraRefused:
		entryError(w, era.status, era.err, echoableID(body))
	case eraLegacy:
		s.legacy(w, r, body, version)
	case eraModern:
		s.modern(w, r.Context(), body, era.revision, era.notification, version)
	}
}

// header is a request header trimmed, and whether it was sent.
func header(r *http.Request, name string) (string, bool) {
	values := r.Header.Values(name)
	if len(values) == 0 {
		return "", false
	}
	return strings.TrimSpace(strings.Join(values, ", ")), true
}

type server struct {
	tools ToolSet
	opts  Options
}

// legacy serves the 2025 revisions as the SDK's stateless transport does:
// every request's response as one event of a stream that ends with the
// last.
func (s *server) legacy(w http.ResponseWriter, r *http.Request, body any, version *string) {
	if refuseAccept(w, r) {
		return
	}
	messages, batch := body.([]any)
	if !batch {
		messages = []any{body}
	}
	if len(messages) > MaxBatch {
		entryError(w, http.StatusBadRequest, newRPCError(invalidRequest, fmt.Sprintf("Invalid Request: Batch must not exceed %d messages", MaxBatch), nil), nil)
		return
	}
	for _, message := range messages {
		if !isMessage(message) {
			entryError(w, http.StatusBadRequest, newRPCError(parseError, "Parse error: Invalid JSON-RPC message", nil), nil)
			return
		}
	}
	initializes := slices.ContainsFunc(messages, func(m any) bool { return isRequest(m) && objectOf(m)["method"] == "initialize" })
	if initializes && len(messages) > 1 {
		entryError(w, http.StatusBadRequest, newRPCError(invalidRequest, "Invalid Request: Only one initialization request is allowed", nil), nil)
		return
	}
	if !initializes && version != nil && !slices.Contains(InitializeVersions, *version) {
		entryError(w, http.StatusBadRequest, newRPCError(serverError, fmt.Sprintf("Bad Request: Unsupported protocol version: %s (supported versions: %s)", *version, strings.Join(InitializeVersions, ", ")), nil), nil)
		return
	}
	var frames bytes.Buffer
	for _, message := range messages {
		if !isRequest(message) {
			continue
		}
		m := objectOf(message)
		var reply []byte
		result, err := s.dispatch(r.Context(), m, false)
		if err != nil {
			reply = errorMessage(s.rpcErrorOf(err), m["id"])
		} else {
			reply = resultMessage(result, m["id"])
		}
		frames.WriteString("event: message\ndata: ")
		frames.Write(reply)
		frames.WriteString("\n\n")
	}
	// Notifications and responses alone: nothing to answer.
	if frames.Len() == 0 {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)
	_, _ = w.Write(frames.Bytes())
}

// modern serves one 2026-07-28 message as one JSON body.
func (s *server) modern(w http.ResponseWriter, ctx context.Context, body any, revision string, notification bool, version *string) {
	if !slices.Contains(DiscoverVersions, revision) {
		entryError(w, http.StatusBadRequest, newRPCError(unsupportedVersion, "Unsupported protocol version: "+revision, map[string]any{"supported": DiscoverVersions, "requested": revision}), echoableID(body))
		return
	}
	if notification {
		w.WriteHeader(http.StatusAccepted)
		return
	}
	if version == nil {
		entryError(w, http.StatusBadRequest, mismatch("(missing)", "the body envelope names protocol version "+revision+" but the required MCP-Protocol-Version header is absent"), echoableID(body))
		return
	}
	message := objectOf(body)
	id := message["id"]
	method, _ := message["method"].(string)
	if !slices.Contains([]string{"server/discover", "ping", "tools/list", "tools/call"}, method) {
		// A method the era does not define is refused before any handler
		// runs, with the ladder's status.
		jsonResponse(w, http.StatusNotFound, errorMessage(newRPCError(methodNotFound, "Method not found", nil), id))
		return
	}
	result, err := s.dispatch(ctx, message, true)
	if err != nil {
		jsonResponse(w, http.StatusOK, errorMessage(s.rpcErrorOf(err), id))
		return
	}
	out, isObject := result.(map[string]any)
	if !isObject {
		jsonResponse(w, http.StatusOK, resultMessage(result, id))
		return
	}
	stamped := make(map[string]any, len(out)+4)
	for key, value := range out {
		if key != "_meta" {
			stamped[key] = value
		}
	}
	// A call that asks the person first is input_required (D93).
	if _, ok := out["resultType"].(string); !ok {
		stamped["resultType"] = "complete"
	}
	if method == "server/discover" || method == "tools/list" {
		// Cache hints: none, and for this caller alone.
		stamped["ttlMs"] = 0
		stamped["cacheScope"] = "private"
	}
	meta := map[string]any{}
	if given, ok := out["_meta"].(map[string]any); ok {
		for key, value := range given {
			meta[key] = value
		}
	}
	meta[serverInfoKey] = s.opts.ServerInfo
	stamped["_meta"] = meta
	jsonResponse(w, http.StatusOK, resultMessage(stamped, id))
}

// dispatch runs one request's method; a refusal is its *rpcError.
func (s *server) dispatch(ctx context.Context, message map[string]any, modern bool) (any, error) {
	method, _ := message["method"].(string)
	params := message["params"]
	switch {
	case method == "initialize" && !modern:
		return s.initialize(params)
	case method == "server/discover" && modern:
		return s.withInstructions(map[string]any{"supportedVersions": DiscoverVersions, "capabilities": capabilities()}), nil
	case method == "ping":
		return map[string]any{}, nil
	case method == "tools/list":
		return s.list(ctx)
	case method == "tools/call":
		return s.call(ctx, params, modern)
	}
	return nil, newRPCError(methodNotFound, "Method not found", nil)
}

func (s *server) initialize(params any) (any, error) {
	requested, ok := objectOf(params)["protocolVersion"].(string)
	if !ok {
		return nil, newRPCError(invalidParams, "Invalid params for initialize: protocolVersion must be a string", nil)
	}
	version := InitializeVersions[0]
	if slices.Contains(InitializeVersions, requested) {
		version = requested
	}
	return s.withInstructions(map[string]any{"protocolVersion": version, "capabilities": capabilities(), "serverInfo": s.opts.ServerInfo}), nil
}

func (s *server) withInstructions(result map[string]any) map[string]any {
	if s.opts.Instructions != "" {
		result["instructions"] = s.opts.Instructions
	}
	return result
}

func (s *server) list(ctx context.Context) (any, error) {
	tools, err := s.tools.List(ctx)
	if err != nil {
		return nil, err
	}
	if tools == nil {
		tools = []Tool{}
	}
	return map[string]any{"tools": tools}, nil
}

// call runs a tool: its result as text and, for an object, structured
// content; a refusal as a tool error carrying the problem document. A
// tool that asks the person first runs only once they confirm the call.
func (s *server) call(ctx context.Context, params any, modern bool) (any, error) {
	message := objectOf(params)
	name, ok := message["name"].(string)
	if !ok {
		return nil, newRPCError(invalidParams, "Invalid params for tools/call: name must be a string", nil)
	}
	raw, present := message["arguments"]
	args, isObject := raw.(map[string]any)
	if present && raw != nil && !isObject {
		return nil, newRPCError(invalidParams, "Invalid params for tools/call: arguments must be an object", nil)
	}
	if args == nil {
		args = map[string]any{}
	}
	asks, err := s.tools.Confirms(ctx, name)
	if err != nil {
		return nil, s.toolFailure(err)
	}
	if asks != nil {
		confirmed, answer, err := s.confirm(ctx, *asks, args, message, modern)
		if err != nil {
			return nil, s.toolFailure(err)
		}
		if answer != nil {
			return answer, nil
		}
		args = confirmed
	}
	outcome, err := s.tools.Call(ctx, name, args)
	if err != nil {
		return nil, s.toolFailure(err)
	}
	return toolResult(outcome), nil
}

// toolResult is what a call answers for outcome.
func toolResult(outcome CallOutcome) map[string]any {
	switch {
	case outcome.Problem != nil:
		return toolError(outcome.Problem)
	case outcome.Content != nil:
		return map[string]any{"content": outcome.Content}
	}
	result := outcome.Result
	if len(bytes.TrimSpace(result)) == 0 {
		result = json.RawMessage("null")
	}
	text := string(compact(result))
	answer := map[string]any{"content": []Content{{Type: "text", Text: text}}}
	if trimmed := bytes.TrimSpace(result); len(trimmed) > 0 && trimmed[0] == '{' {
		answer["structuredContent"] = json.RawMessage(compact(result))
	}
	return answer
}

func toolError(problem json.RawMessage) map[string]any {
	compacted := compact(problem)
	return map[string]any{"content": []Content{{Type: "text", Text: string(compacted)}}, "structuredContent": json.RawMessage(compacted), "isError": true}
}

// toolFailure is a tool set's error as the call answers it: a tool it has
// not got is invalid params; any other is the internal error, whose
// message stays off the wire.
func (s *server) toolFailure(err error) error {
	if unknown, ok := isUnknown(err); ok {
		return newRPCError(invalidParams, unknown.Message, nil)
	}
	return err
}

// rpcErrorOf is err as a JSON-RPC error: its own, or the internal error.
func (s *server) rpcErrorOf(err error) *rpcError {
	if rpc, ok := err.(*rpcError); ok {
		return rpc
	}
	if s.opts.Log != nil {
		s.opts.Log(err)
	}
	return newRPCError(internalError, "Internal error", nil)
}

// What the person, or the agent, reads of a confirmation that did not
// redeem, before the call is asked about again.
var refusals = map[ConfirmationRefusal]string{
	RefusedInvalid:  "The confirmation sent is not one this endpoint gave.",
	RefusedExpired:  "The confirmation sent has expired.",
	RefusedMismatch: "The confirmation sent is for another call.",
	RefusedUsed:     "The confirmation sent was used already.",
}

// confirm is the confirmation of a call of tool, which asks the person
// first (D93): the arguments it runs with, the token's taken out, once
// the call carries a confirmation that redeems, else what the call answers
// instead, the refusal its check met, a confirmation to ask for or the
// person's refusal.
func (s *server) confirm(ctx context.Context, tool Tool, args map[string]any, params map[string]any, modern bool) (map[string]any, map[string]any, error) {
	token, hasToken := args[ConfirmationArgument]
	rest := make(map[string]any, len(args))
	for key, value := range args {
		if key != ConfirmationArgument {
			rest[key] = value
		}
	}
	// A call that would not reach the implementation is refused before
	// anyone is asked, and mints and claims nothing (D98).
	refusal, err := s.tools.Check(ctx, tool.Name, rest)
	if err != nil {
		return nil, nil, err
	}
	if refusal != nil {
		return nil, toolError(refusal), nil
	}
	confirmations := s.opts.Confirmations
	if confirmations == nil {
		return nil, toolError(s.problem(http.StatusInternalServerError, tool.Name+" runs only once the person confirms it, and this endpoint has no confirmation key", "internal_error", nil)), nil
	}
	binding := ConfirmationBinding{Tool: tool.Name, Caller: s.opts.Caller, Args: rest}
	elicits := modern && elicitsForms(clientCapabilities(params))
	var refused ConfirmationRefusal
	switch {
	case hasToken:
		if refused, err = confirmations.Redeem(ctx, token, ModeToken, binding); err != nil {
			return nil, nil, err
		}
		if refused == "" {
			return rest, nil, nil
		}
	case elicits && params["requestState"] != nil:
		var action string
		if responses, ok := params["inputResponses"].(map[string]any); ok {
			if answer, ok := responses[ConfirmationInput].(map[string]any); ok {
				action, _ = answer["action"].(string)
			}
		}
		if action == "accept" || action == "decline" || action == "cancel" {
			if refused, err = confirmations.Redeem(ctx, params["requestState"], ModeElicit, binding); err != nil {
				return nil, nil, err
			}
			if refused == "" && action == "accept" {
				return rest, nil, nil
			}
			if refused == "" {
				what := "dismissed the confirmation of"
				if action == "decline" {
					what = "declined"
				}
				return nil, toolError(s.problem(http.StatusForbidden, "The person "+what+" "+tool.Name+", which did not run", ConfirmationDeclined, map[string]any{"tool": tool.Name, "action": action})), nil
			}
		}
	}
	summary := confirmationSummary(tool, rest)
	why := ""
	if refused != "" {
		why = refusals[refused] + " "
	}
	if elicits {
		minted, err := confirmations.Mint(ModeElicit, binding)
		if err != nil {
			return nil, nil, err
		}
		ask := map[string]any{
			"method": "elicitation/create",
			"params": map[string]any{"mode": "form", "message": why + "Run " + summary + "?", "requestedSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		}
		return nil, map[string]any{"resultType": "input_required", "inputRequests": map[string]any{ConfirmationInput: ask}, "requestState": minted.Token}, nil
	}
	minted, err := confirmations.Mint(ModeToken, binding)
	if err != nil {
		return nil, nil, err
	}
	detail := why + tool.Name + " runs only once the person confirms it: " + summary + ". Show them the call, and once they confirm, call " + tool.Name + " again with the same arguments and " + ConfirmationArgument + " set to details." + ConfirmationArgument + ", before " + minted.ExpiresAt
	details := map[string]any{"tool": tool.Name, "summary": summary, ConfirmationArgument: minted.Token, "expiresAt": minted.ExpiresAt}
	if refused != "" {
		details["refused"] = string(refused)
	}
	return nil, toolError(s.problem(http.StatusPreconditionRequired, detail, ConfirmationRequired, details)), nil
}

// problem is a problem document of the endpoint's own, as the routes write
// one: its type, title, status, detail, code, request id and details.
func (s *server) problem(status int, detail, code string, details any) json.RawMessage {
	return marshal(Problem{Type: "about:blank", Title: http.StatusText(status), Status: status, Detail: detail, Code: code, RequestID: s.requestID(), Details: details})
}

func (s *server) requestID() string {
	if s.opts.RequestID != "" {
		return s.opts.RequestID
	}
	return newRequestID()
}

// Problem is a problem document (RFC 9457) the endpoint writes.
type Problem struct {
	Type      string `json:"type"`
	Title     string `json:"title"`
	Status    int    `json:"status"`
	Detail    string `json:"detail"`
	Code      string `json:"code,omitempty"`
	RequestID string `json:"requestId,omitempty"`
	Details   any    `json:"details,omitempty"`
}

// newRequestID is a random UUID.
func newRequestID() string {
	b := make([]byte, 16)
	_, _ = rand.Read(b)
	b[6] = b[6]&0x0f | 0x40
	b[8] = b[8]&0x3f | 0x80
	h := hex.EncodeToString(b)
	return h[0:8] + "-" + h[8:12] + "-" + h[12:16] + "-" + h[16:20] + "-" + h[20:]
}

// summaryArguments is the longest arguments' text a confirmation's
// summary shows.
const summaryArguments = 2000

// confirmationSummary is what the person confirms: the tool's title and
// handle, and its arguments as canonical JSON, the part past
// summaryArguments characters counted rather than shown. The token binds
// every argument, whatever the summary shows.
func confirmationSummary(tool Tool, args map[string]any) string {
	name := tool.Name
	if tool.Title != "" && tool.Title != tool.Name {
		name = tool.Title + " (" + tool.Name + ")"
	}
	if len(args) == 0 {
		return name + " with no arguments"
	}
	text := CanonicalJSON(args)
	units := []rune(text)
	// JavaScript counts UTF-16 code units; the summary is cut at a code
	// point, which a model reads the same.
	if utf16Length(text) <= summaryArguments {
		return name + " with " + text
	}
	cut, length := 0, 0
	for i, r := range units {
		n := 1
		if r >= 0x10000 {
			n = 2
		}
		if length+n > summaryArguments {
			cut = i
			break
		}
		length += n
	}
	return name + " with " + string(units[:cut]) + "… (" + strconv.Itoa(utf16Length(text)-length) + " more characters)"
}

func utf16Length(s string) int {
	n := 0
	for _, r := range s {
		if r >= 0x10000 {
			n += 2
		} else {
			n++
		}
	}
	return n
}

// clientCapabilities is the capabilities a 2026-07-28 request's envelope
// declares.
func clientCapabilities(params map[string]any) any {
	meta, _ := params["_meta"].(map[string]any)
	return meta[clientCapabilitiesKey]
}

// elicitsForms is whether capabilities let the server elicit a form: an
// elicitation capability that names form, or, as the 2025 revisions
// declared it, one that names neither form nor url.
func elicitsForms(capabilities any) bool {
	caps, ok := capabilities.(map[string]any)
	if !ok {
		return false
	}
	elicitation, ok := caps["elicitation"].(map[string]any)
	if !ok {
		return false
	}
	if form, named := elicitation["form"]; named {
		_, isObject := form.(map[string]any)
		return isObject
	}
	_, url := elicitation["url"]
	return !url
}

func capabilities() map[string]any {
	return map[string]any{"tools": map[string]any{"listChanged": false}}
}

// era is how a parsed body is served.
type era struct {
	kind         int
	status       int
	err          *rpcError
	revision     string
	notification bool
}

const (
	eraRefused = iota
	eraLegacy
	eraModern
)

func refusedEra(err *rpcError) era {
	return era{kind: eraRefused, status: http.StatusBadRequest, err: err}
}

// classify routes a parsed body as the SDK's entry does.
func classify(body any, version, method *string) era {
	invalid := func(message string) era { return refusedEra(newRPCError(invalidRequest, message, nil)) }
	if batch, ok := body.([]any); ok {
		if len(batch) == 0 {
			return invalid("Bad Request: empty JSON-RPC batch")
		}
		for _, element := range batch {
			if _, claimed := claimOf(objectOf(element)["params"]); claimed {
				return invalid("Bad Request: JSON-RPC batches may not contain requests for protocol revision 2026-07-28 or later")
			}
			if !isMessage(element) {
				return invalid("Bad Request: JSON-RPC batch contains an invalid message")
			}
		}
		return era{kind: eraLegacy}
	}
	if isResponse(body) {
		return era{kind: eraLegacy}
	}
	message := objectOf(body)
	params := message["params"]
	name, _ := message["method"].(string)
	headerModern := version != nil && *version >= firstModern
	switch {
	case isRequest(body):
		if name == "initialize" && !validModernClaim(params) {
			if headerModern {
				return refusedEra(mismatch(*version, "an initialize request (legacy handshake) was sent with a modern MCP-Protocol-Version header"))
			}
			return era{kind: eraLegacy}
		}
		if claim, claimed := claimOf(params); claimed {
			if issues := envelopeIssues(params); len(issues) > 0 {
				key, problem := issues[0][0], issues[0][1]
				return refusedEra(newRPCError(invalidParams, "Invalid _meta envelope for protocol revision 2026-07-28: "+key+": "+problem, map[string]any{"envelope": map[string]any{"key": key, "problem": problem}}))
			}
			text, _ := claim.(string)
			if version != nil && *version != text {
				return refusedEra(mismatch(*version, "the body envelope names protocol version "+text+" but the MCP-Protocol-Version header names "+*version))
			}
			if refused, ok := methodMismatch(method, name, "body"); ok {
				return refused
			}
			return era{kind: eraModern, revision: text}
		}
		if headerModern {
			var missing []string
			paramsObject, isObject := params.(map[string]any)
			if _, hasMeta := paramsObject["_meta"]; !isObject || !hasMeta {
				missing = []string{"_meta"}
			} else {
				for _, issue := range envelopeIssues(params) {
					if issue[1] == "missing" {
						missing = append(missing, issue[0])
					}
				}
				if len(missing) == 0 {
					missing = []string{versionKey}
				}
			}
			return refusedEra(newRPCError(invalidParams, "Invalid params: the MCP-Protocol-Version header names protocol revision "+*version+", but the request is missing the required per-request envelope key(s): "+strings.Join(missing, ", "), map[string]any{"envelope": map[string]any{"missing": missing}}))
		}
		return era{kind: eraLegacy}
	case isNotification(body):
		if claim, claimed := claimOf(params); claimed {
			text, isText := claim.(string)
			if !isText {
				problem := "expected a protocol version string"
				return refusedEra(newRPCError(invalidParams, "Invalid _meta envelope for protocol revision 2026-07-28: "+versionKey+": "+problem, map[string]any{"envelope": map[string]any{"key": versionKey, "problem": problem}}))
			}
			if version != nil && *version != text {
				return refusedEra(mismatch(*version, "the notification envelope names protocol version "+text+" but the MCP-Protocol-Version header names "+*version))
			}
			if text >= firstModern {
				if refused, ok := methodMismatch(method, name, "notification body"); ok {
					return refused
				}
			}
			return era{kind: eraModern, revision: text, notification: true}
		}
		if headerModern {
			if refused, ok := methodMismatch(method, name, "notification body"); ok {
				return refused
			}
			return era{kind: eraModern, revision: *version, notification: true}
		}
		return era{kind: eraLegacy}
	}
	return invalid("Bad Request: the request body is not a valid JSON-RPC message")
}

func methodMismatch(header *string, method, what string) (era, bool) {
	if header == nil || *header == method {
		return era{}, false
	}
	return refusedEra(mismatch(*header, "the "+what+" names method "+method+" but the Mcp-Method header names "+*header)), true
}

// claimOf is the envelope's protocol-version claim, when the params carry
// one.
func claimOf(params any) (any, bool) {
	meta, ok := objectOf(params)["_meta"].(map[string]any)
	if !ok {
		return nil, false
	}
	claim, claimed := meta[versionKey]
	return claim, claimed
}

// validModernClaim is whether the params carry a well-formed envelope that
// names a 2026 revision, which makes even initialize a 2026 request.
func validModernClaim(params any) bool {
	claim, _ := claimOf(params)
	text, ok := claim.(string)
	return ok && text >= firstModern && len(envelopeIssues(params)) == 0
}

// envelopeIssues lists what is wrong with a 2026 request's envelope: each
// required key that is missing, then each that is malformed.
func envelopeIssues(params any) [][2]string {
	meta, _ := objectOf(params)["_meta"].(map[string]any)
	var issues [][2]string
	for _, key := range []string{versionKey, clientInfoKey, clientCapabilitiesKey} {
		if _, ok := meta[key]; !ok {
			issues = append(issues, [2]string{key, "missing"})
		}
	}
	if value, ok := meta[versionKey]; ok {
		if _, isText := value.(string); !isText {
			issues = append(issues, [2]string{versionKey, "expected string"})
		}
	}
	if value, ok := meta[clientInfoKey]; ok {
		info, isObject := value.(map[string]any)
		_, named := info["name"].(string)
		_, versioned := info["version"].(string)
		if !isObject || !named || !versioned {
			issues = append(issues, [2]string{clientInfoKey, "expected an object with a name and a version"})
		}
	}
	if value, ok := meta[clientCapabilitiesKey]; ok {
		if _, isObject := value.(map[string]any); !isObject {
			issues = append(issues, [2]string{clientCapabilitiesKey, "expected object"})
		}
	}
	return issues
}

// objectOf is value as an object, or an empty one.
func objectOf(value any) map[string]any {
	if object, ok := value.(map[string]any); ok {
		return object
	}
	return map[string]any{}
}

func isRPC(message any) bool {
	object, ok := message.(map[string]any)
	return ok && object["jsonrpc"] == "2.0"
}

func isID(id any) bool {
	switch v := id.(type) {
	case string:
		return true
	case json.Number:
		return isInteger(v)
	case float64:
		return v == math.Trunc(v) && !math.IsInf(v, 0)
	}
	return false
}

func isInteger(n json.Number) bool {
	f, err := strconv.ParseFloat(n.String(), 64)
	return err == nil && f == math.Trunc(f) && !math.IsInf(f, 0)
}

func paramsOK(message map[string]any) bool {
	params, present := message["params"]
	if !present {
		return true
	}
	_, isObject := params.(map[string]any)
	return isObject
}

func isRequest(message any) bool {
	if !isRPC(message) {
		return false
	}
	m := message.(map[string]any)
	_, named := m["method"].(string)
	id, hasID := m["id"]
	return named && hasID && isID(id) && paramsOK(m)
}

func isNotification(message any) bool {
	if !isRPC(message) {
		return false
	}
	m := message.(map[string]any)
	_, named := m["method"].(string)
	_, hasID := m["id"]
	return named && !hasID && paramsOK(m)
}

func isResponse(message any) bool {
	if !isRPC(message) {
		return false
	}
	m := message.(map[string]any)
	if _, hasMethod := m["method"]; hasMethod {
		return false
	}
	id, hasID := m["id"]
	_, resultObject := m["result"].(map[string]any)
	result := hasID && isID(id) && resultObject
	failure := false
	if e, ok := m["error"].(map[string]any); ok {
		code, isNumber := e["code"].(json.Number)
		_, isText := e["message"].(string)
		failure = isNumber && isInteger(code) && isText
	}
	return result || failure
}

func isMessage(message any) bool {
	return isRequest(message) || isNotification(message) || isResponse(message)
}

// echoableID is the id an entry-built error answers with: a single
// request's, else null.
func echoableID(body any) any {
	m, ok := body.(map[string]any)
	if !ok {
		return nil
	}
	if _, named := m["method"].(string); !named {
		return nil
	}
	if id, has := m["id"]; has && isID(id) {
		return id
	}
	return nil
}

// isJSONType is whether a Content-Type is JSON's, parameters aside.
func isJSONType(contentType string) bool {
	media, _, _ := strings.Cut(contentType, ";")
	return strings.ToLower(strings.TrimSpace(media)) == "application/json"
}

// refuseAccept answers the transport's 406 for a client that does not
// take both JSON and an event stream, and reports whether it did.
func refuseAccept(w http.ResponseWriter, r *http.Request) bool {
	accept := strings.Join(r.Header.Values("Accept"), ", ")
	if strings.Contains(accept, "application/json") && strings.Contains(accept, "text/event-stream") {
		return false
	}
	entryError(w, http.StatusNotAcceptable, newRPCError(serverError, "Not Acceptable: Client must accept both application/json and text/event-stream", nil), nil)
	return true
}

// readCapped reads the body, or false once it passes limit bytes; it
// stops reading there, so an oversized body is never held.
func readCapped(r *http.Request, limit int64) ([]byte, bool) {
	if r.ContentLength > limit {
		return nil, false
	}
	if r.Body == nil {
		return nil, true
	}
	data, err := io.ReadAll(io.LimitReader(r.Body, limit+1))
	if err != nil || int64(len(data)) > limit {
		return nil, false
	}
	return data, true
}

// idOf is a request's id as the reply carries it: a number as JavaScript
// writes it.
func idOf(id any) any {
	if n, ok := id.(json.Number); ok {
		return json.RawMessage(numberText(n))
	}
	return id
}

func resultMessage(result any, id any) []byte {
	return marshal(struct {
		Result  any    `json:"result"`
		JSONRPC string `json:"jsonrpc"`
		ID      any    `json:"id"`
	}{result, "2.0", idOf(id)})
}

func errorMessage(err *rpcError, id any) []byte {
	return marshal(struct {
		JSONRPC string    `json:"jsonrpc"`
		ID      any       `json:"id"`
		Error   *rpcError `json:"error"`
	}{"2.0", idOf(id), err})
}

// entryError answers an error the entry or the transport makes before any
// method runs, as the SDK writes it.
func entryError(w http.ResponseWriter, status int, err *rpcError, id any) {
	jsonResponse(w, status, marshal(struct {
		JSONRPC string    `json:"jsonrpc"`
		Error   *rpcError `json:"error"`
		ID      any       `json:"id"`
	}{"2.0", err, idOf(id)}))
}

func jsonResponse(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}
