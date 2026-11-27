package agents

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
)

func coversPermission(held, need string) bool { return session.Covers(held, need) }

func secondsOf(n int64) time.Duration { return time.Duration(n) * time.Second }

// pathOf is the path of endpoint, without a trailing slash.
func pathOf(endpoint string) string {
	u, err := url.Parse(endpoint)
	if err != nil {
		return ""
	}
	return strings.TrimRight(u.Path, "/")
}

// modelTools are the tools offered to the model: the run's MCP tools and
// the sandbox's own, and which of them are untrusted.
func (a *Agents) modelTools(ctx context.Context, principal RunPrincipal, definition orchestration.Row) ([]providers.ModelTool, map[string]bool, error) {
	set, err := a.MCPTools(ctx, principal)
	if err != nil {
		return nil, nil, err
	}
	untrusted := map[string]bool{}
	var list []providers.ModelTool
	for _, t := range set.List() {
		if u, _ := t.Meta["untrusted"].(bool); u {
			untrusted[t.Name] = true
		}
		description := t.Description
		if description == "" {
			description = t.Name
		}
		list = append(list, providers.ModelTool{Name: t.Name, Description: description, InputSchema: t.InputSchema})
	}
	for _, tool := range a.opts.SandboxTools {
		if !allowed(definition, tool.Name) {
			continue
		}
		if tool.Untrusted {
			untrusted[tool.Name] = true
		}
		list = append(list, providers.ModelTool{Name: tool.Name, Description: tool.Description, InputSchema: tool.InputSchema})
	}
	return list, untrusted, nil
}

func writeJSON(w http.ResponseWriter, status int, body any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

func refusal(w http.ResponseWriter, status int, code, detail string) {
	writeJSON(w, status, map[string]any{"type": "about:blank", "title": code, "status": status, "code": code, "detail": detail})
}

// turnBody is what an instance posts to <endpoint>/turn: the tool results
// and messages it has since its last turn.
type turnBody struct {
	Messages []providers.ModelMessage `json:"messages"`
}

func (a *Agents) turn(ctx context.Context, principal RunPrincipal, body turnBody) (int, any) {
	run, err := a.runOf(ctx, principal)
	if err != nil {
		return a.failure(err)
	}
	if status := run.Str("status"); status != "running" && status != "waiting" {
		return refusalBody(409, "run_ended", "the run has ended")
	}
	agent := run.Str("agent")
	stand, err := a.standingOf(ctx, agent)
	if err != nil {
		return a.failure(err)
	}
	if stand.paused {
		if err := a.Cancel(ctx, run.Str("id"), "paused", "the agent is paused ("+stand.reason+")"); err != nil {
			return a.failure(err)
		}
		return refusalBody(409, "agent_paused", "the agent is paused")
	}
	if stand.agentCapped {
		if err := a.setPaused(ctx, agent, true, "budget", "budget"); err != nil {
			return a.failure(err)
		}
		return refusalBody(409, "agent_paused", "the agent reached its budget's cap")
	}
	definition, err := a.definitionByID(ctx, agent)
	if err != nil || definition == nil {
		return a.failure(errors.Join(err, errors.New("the run's agent is gone")))
	}
	limits := definition.Object("limits")
	if v, ok := limits["maxTurns"]; ok && v != nil && run.Int("turns") >= orchestration.IntOf(v) {
		return refusalBody(409, "max_turns", "the run took its turns")
	}
	tools, untrusted, err := a.modelTools(ctx, principal, definition)
	if err != nil {
		return a.failure(err)
	}
	recorded := entriesOf(run["transcript"])
	untrustedCalls := map[string]bool{}
	for _, id := range run.Strings("untrustedCalls") {
		untrustedCalls[id] = true
	}
	var incoming []TranscriptEntry
	outside := int64(0)
	for _, m := range body.Messages {
		if m.Role != providers.RoleTool && m.Role != providers.RoleUser {
			continue
		}
		entry := TranscriptEntry{Kind: "message", Role: m.Role, Content: m.Content, ToolCallID: m.ToolCallID}
		// A tool result of a call marked untrusted, and anything a runner
		// sends as a user, is data.
		if (m.ToolCallID != "" && untrustedCalls[m.ToolCallID]) || m.Role == providers.RoleUser {
			entry.Untrusted = true
			if m.Role == providers.RoleTool {
				outside++
			}
		}
		incoming = append(incoming, entry)
	}
	if len(incoming) > 0 {
		if err := a.appendEntries(ctx, run.Str("id"), incoming...); err != nil {
			return a.failure(err)
		}
	}
	if outside > 0 {
		if err := a.write(a.self(ctx), a.host, a.runs, run.Str("id"), map[string]any{"untrusted": run.Int("untrusted") + outside}); err != nil {
			return a.failure(err)
		}
	}
	history := append([]TranscriptEntry{}, recorded...)
	for _, e := range incoming {
		var redacted TranscriptEntry
		if json.Unmarshal(a.redactJSON(e), &redacted) == nil {
			e = redacted
		}
		history = append(history, e)
	}
	var messages []providers.ModelMessage
	if instructions := definition.Str("instructions"); instructions != "" {
		messages = append(messages, providers.ModelMessage{Role: providers.RoleSystem, Content: instructions})
	}
	for _, entry := range history {
		switch entry.Kind {
		case "task":
			messages = append(messages, providers.ModelMessage{Role: providers.RoleUser, Content: entry.Content, Untrusted: entry.Untrusted})
		case "message":
			role := entry.Role
			if role == "" {
				role = providers.RoleTool
			}
			messages = append(messages, providers.ModelMessage{Role: role, Content: entry.Content, ToolCallID: entry.ToolCallID, Untrusted: entry.Untrusted})
		case "completion":
			// An earlier completion, with the tool calls the tool messages
			// after it answer and the provider's state of it, as the
			// provider returned it (D158).
			message := providers.ModelMessage{Role: providers.RoleAssistant, Content: entry.Content, ProviderState: entry.ProviderState}
			for _, c := range entry.ToolCalls {
				message.ToolCalls = append(message.ToolCalls, providers.ModelToolCall{ID: c.ID, Name: c.Name, Input: c.Input})
			}
			messages = append(messages, message)
		}
	}
	slot, provider, err := a.modelOf(ctx, definition)
	if err != nil {
		return a.failure(err)
	}
	completion, err := provider.Complete(ctx, providers.ModelTurn{Model: definition.Str("model"), Messages: messages, Tools: tools})
	if err != nil {
		return refusalBody(502, "model_failed", a.redactText(err.Error()))
	}
	for _, call := range completion.ToolCalls {
		if untrusted[call.Name] {
			untrustedCalls[call.ID] = true
		}
	}
	calls := make([]string, 0, len(untrustedCalls))
	for id := range untrustedCalls {
		calls = append(calls, id)
	}
	spent := a.opts.CostOf(completion.Usage)
	if err := a.inTransaction(ctx, func(h orchestration.Host) error {
		current, err := a.run(ctx, h, run.Str("id"))
		if err != nil || current == nil {
			return err
		}
		return a.write(a.self(ctx), h, a.runs, run.Str("id"), map[string]any{
			"turns":          current.Int("turns") + 1,
			"inputTokens":    current.Int("inputTokens") + int64(completion.Usage.InputTokens),
			"outputTokens":   current.Int("outputTokens") + int64(completion.Usage.OutputTokens),
			"cost":           current.Int("cost") + spent,
			"untrustedCalls": calls,
			"modelSlot":      slot,
		})
	}); err != nil {
		return a.failure(err)
	}
	entry := TranscriptEntry{Kind: "completion", Role: providers.RoleAssistant, Content: completion.Content, ToolCalls: []TranscriptCall{}, ProviderState: completion.ProviderState}
	for _, c := range completion.ToolCalls {
		entry.ToolCalls = append(entry.ToolCalls, TranscriptCall{ID: c.ID, Name: c.Name, Input: c.Input})
	}
	if err := a.appendEntries(ctx, run.Str("id"), entry); err != nil {
		return a.failure(err)
	}
	if err := a.meterUsage(ctx, agent, spent); err != nil {
		return a.failure(err)
	}
	toolCalls := completion.ToolCalls
	if toolCalls == nil {
		toolCalls = []providers.ModelToolCall{}
	}
	return 200, map[string]any{"content": a.redactText(completion.Content), "toolCalls": json.RawMessage(a.redactJSON(toolCalls)), "stop": completion.Stop, "usage": completion.Usage}
}

func refusalBody(status int, code, detail string) (int, any) {
	return status, map[string]any{"type": "about:blank", "title": code, "status": status, "code": code, "detail": detail}
}

func (a *Agents) failure(err error) (int, any) {
	var refused *Error
	if errors.As(err, &refused) {
		return refusalBody(refused.Status, refused.Code, refused.Message)
	}
	a.log.Error("an agent route failed")
	return refusalBody(500, "failed", a.redactText(err.Error()))
}

// RoutesOptions are what Routes takes: how a caller of the approval routes
// is established, which serve only with one.
type RoutesOptions struct {
	Authenticate func(r *http.Request) (*orchestration.Principal, error)
}

// Owns reports whether path is under the agent routes' path.
func (a *Agents) Owns(path string) bool { return strings.HasPrefix(path, a.base+"/") }

// Routes are the agent routes under the endpoint's path: POST /turn,
// /mcp, /credential and /finish with the run's token, and with
// Authenticate, /approvals/<id>/approve and /deny; any other path answers
// 404. Each call's work runs to its end whether or not its caller stays,
// so a run a call cancels (a pause, a budget's cap), whose instance's
// connection the cancel closes, is not left half done (D193).
func (a *Agents) Routes(opts RoutesOptions) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !a.Owns(r.URL.Path) {
			http.NotFound(w, r)
			return
		}
		ctx := context.WithoutCancel(r.Context())
		rest := strings.TrimPrefix(r.URL.Path, a.base+"/")
		if parts := strings.Split(rest, "/"); len(parts) == 3 && parts[0] == "approvals" && (parts[2] == "approve" || parts[2] == "deny") {
			if r.Method != http.MethodPost {
				refusal(w, 405, "method_not_allowed", "a decision is a POST")
				return
			}
			var who *orchestration.Principal
			if opts.Authenticate != nil {
				var err error
				if who, err = opts.Authenticate(r); err != nil {
					status, body := a.failure(err)
					writeJSON(w, status, body)
					return
				}
			}
			if who == nil {
				refusal(w, 401, "unauthorized", "Authentication required")
				return
			}
			id, _ := url.PathUnescape(parts[1])
			state := "approved"
			if parts[2] == "deny" {
				state = "denied"
			}
			if err := a.decide(ctx, a.host, *who, id, state, "api"); err != nil {
				status, body := a.failure(err)
				writeJSON(w, status, body)
				return
			}
			writeJSON(w, 200, map[string]any{"state": state})
			return
		}
		switch rest {
		case "turn", "mcp", "credential", "finish":
		default:
			http.NotFound(w, r)
			return
		}
		if r.Method != http.MethodPost {
			refusal(w, 405, "method_not_allowed", rest+" is a POST")
			return
		}
		token, _ := session.BearerToken(r.Header)
		principal, err := a.PrincipalOf(ctx, token)
		if err != nil {
			status, body := a.failure(err)
			writeJSON(w, status, body)
			return
		}
		if principal == nil {
			refusal(w, 401, "unauthorized", "no running run holds this token")
			return
		}
		data, err := io.ReadAll(io.LimitReader(r.Body, 4<<20))
		if err != nil {
			refusal(w, 400, "invalid_body", "the body could not be read")
			return
		}
		if rest == "mcp" {
			a.serveMCP(ctx, w, *principal, data)
			return
		}
		switch rest {
		case "turn":
			var body turnBody
			_ = json.Unmarshal(data, &body)
			status, answer := a.turn(ctx, *principal, body)
			writeJSON(w, status, answer)
		case "credential":
			status, answer := a.credential(ctx, *principal, data)
			writeJSON(w, status, answer)
		case "finish":
			status, answer := a.finishReport(ctx, *principal, data)
			writeJSON(w, status, answer)
		}
	})
}

func (a *Agents) credential(ctx context.Context, principal RunPrincipal, data []byte) (int, any) {
	run, err := a.runOf(ctx, principal)
	if err != nil {
		return a.failure(err)
	}
	var body struct {
		Ref      string `json:"ref"`
		Provider string `json:"provider"`
		Target   any    `json:"target"`
		UsedAt   string `json:"usedAt"`
	}
	_ = json.Unmarshal(data, &body)
	if body.UsedAt == "" {
		body.UsedAt = a.now().UTC().Format(time.RFC3339Nano)
	}
	// What the sandbox reports of a credential it used: its reference and
	// target, never its value.
	use := map[string]any{"ref": body.Ref, "provider": body.Provider, "target": a.redactJSON(body.Target), "usedAt": body.UsedAt}
	if err := a.inTransaction(ctx, func(h orchestration.Host) error {
		current, err := a.run(ctx, h, run.Str("id"))
		if err != nil || current == nil {
			return err
		}
		uses, _ := current["credentialUses"].([]any)
		return a.write(a.self(ctx), h, a.runs, run.Str("id"), map[string]any{"credentialUses": append(uses, use)})
	}); err != nil {
		return a.failure(err)
	}
	target, _ := json.Marshal(use["target"])
	if err := a.appendEntries(ctx, run.Str("id"), TranscriptEntry{Kind: "credential", Ref: body.Ref, Content: "used " + body.Ref + " on " + string(target)}); err != nil {
		return a.failure(err)
	}
	return 200, map[string]any{"recorded": true}
}

// finishReport keeps the outcome the instance reports, for the watch that
// ends the run.
func (a *Agents) finishReport(ctx context.Context, principal RunPrincipal, data []byte) (int, any) {
	run, err := a.runOf(ctx, principal)
	if err != nil {
		return a.failure(err)
	}
	var body struct {
		Outcome string `json:"outcome"`
		Summary string `json:"summary"`
	}
	_ = json.Unmarshal(data, &body)
	outcome := "done"
	if body.Outcome == "failed" {
		outcome = "failed"
	}
	values := map[string]any{"outcome": outcome}
	if body.Summary != "" {
		detail := a.redactText(body.Summary)
		if len(detail) > 2000 {
			detail = detail[:2000]
		}
		values["detail"] = detail
	}
	if err := a.write(a.self(ctx), a.host, a.runs, run.Str("id"), values); err != nil {
		return a.failure(err)
	}
	if body.Summary != "" {
		if err := a.appendEntries(ctx, run.Str("id"), TranscriptEntry{Kind: "note", Content: body.Summary}); err != nil {
			return a.failure(err)
		}
	}
	return 200, map[string]any{"recorded": true}
}

// ---- the run's MCP endpoint ----

// The MCP protocol versions the run's endpoint answers initialize with.
var mcpVersions = []string{"2025-06-18", "2025-03-26"}

type rpcMessage struct {
	JSONRPC string          `json:"jsonrpc"`
	ID      json.RawMessage `json:"id,omitempty"`
	Method  string          `json:"method"`
	Params  json.RawMessage `json:"params,omitempty"`
}

func rpcError(id json.RawMessage, code int, message string) map[string]any {
	return map[string]any{"jsonrpc": "2.0", "id": id, "error": map[string]any{"code": code, "message": message}}
}

// serveMCP answers the run's MCP endpoint: initialize, ping, tools/list and
// tools/call, alone or in a batch, each answered as one JSON body; a call
// a route refuses is a tool error whose structured content is its problem
// document, as TypeScript's serveMcp answers.
func (a *Agents) serveMCP(ctx context.Context, w http.ResponseWriter, principal RunPrincipal, data []byte) {
	trimmed := bytes.TrimSpace(data)
	var messages []rpcMessage
	batch := len(trimmed) > 0 && trimmed[0] == '['
	if batch {
		if err := json.Unmarshal(trimmed, &messages); err != nil {
			writeJSON(w, 400, rpcError(nil, -32700, "Parse error: Invalid JSON"))
			return
		}
	} else {
		var one rpcMessage
		if err := json.Unmarshal(trimmed, &one); err != nil {
			writeJSON(w, 400, rpcError(nil, -32700, "Parse error: Invalid JSON"))
			return
		}
		messages = []rpcMessage{one}
	}
	var set *ToolSet
	tools := func() (*ToolSet, error) {
		if set != nil {
			return set, nil
		}
		var err error
		set, err = a.MCPTools(ctx, principal)
		return set, err
	}
	var replies []any
	for _, m := range messages {
		if len(m.ID) == 0 {
			// A notification answers nothing.
			continue
		}
		replies = append(replies, a.dispatchMCP(ctx, m, tools))
	}
	switch {
	case len(replies) == 0:
		w.WriteHeader(http.StatusAccepted)
	case batch:
		writeJSON(w, 200, replies)
	default:
		writeJSON(w, 200, replies[0])
	}
}

func (a *Agents) dispatchMCP(ctx context.Context, m rpcMessage, tools func() (*ToolSet, error)) any {
	result := func(v any) map[string]any { return map[string]any{"jsonrpc": "2.0", "id": m.ID, "result": v} }
	switch m.Method {
	case "initialize":
		var params struct {
			ProtocolVersion string `json:"protocolVersion"`
		}
		_ = json.Unmarshal(m.Params, &params)
		version := mcpVersions[0]
		if contains(mcpVersions, params.ProtocolVersion) {
			version = params.ProtocolVersion
		}
		return result(map[string]any{"protocolVersion": version, "capabilities": map[string]any{"tools": map[string]any{"listChanged": false}}, "serverInfo": map[string]any{"name": "agent-run", "version": "1"}})
	case "ping":
		return result(map[string]any{})
	case "tools/list":
		set, err := tools()
		if err != nil {
			return rpcError(m.ID, -32603, a.redactText(err.Error()))
		}
		listed := set.List()
		if listed == nil {
			listed = []MCPTool{}
		}
		return result(map[string]any{"tools": listed})
	case "tools/call":
		var params struct {
			Name      string         `json:"name"`
			Arguments map[string]any `json:"arguments"`
		}
		if err := json.Unmarshal(m.Params, &params); err != nil || params.Name == "" {
			return rpcError(m.ID, -32602, "Invalid params for tools/call: name must be a string")
		}
		if params.Arguments == nil {
			params.Arguments = map[string]any{}
		}
		set, err := tools()
		if err != nil {
			return rpcError(m.ID, -32603, a.redactText(err.Error()))
		}
		outcome, err := set.Call(ctx, params.Name, params.Arguments)
		if err != nil {
			status, body := a.failure(err)
			problem, _ := body.(map[string]any)
			problem["status"] = status
			text, _ := json.Marshal(problem)
			return result(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": problem, "isError": true})
		}
		if outcome.Problem != nil {
			text, _ := json.Marshal(outcome.Problem)
			return result(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": outcome.Problem, "isError": true})
		}
		text, _ := json.Marshal(outcome.Result)
		var object map[string]any
		if json.Unmarshal(text, &object) == nil && object != nil {
			return result(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}, "structuredContent": object})
		}
		return result(map[string]any{"content": []any{map[string]any{"type": "text", "text": string(text)}}})
	}
	return rpcError(m.ID, -32601, "Method not found")
}
