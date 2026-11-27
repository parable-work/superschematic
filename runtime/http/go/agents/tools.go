package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"

	"github.com/parable-work/superschematic/runtime/http/go/inbound"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// MCPTool is a tool as tools/list answers it.
type MCPTool struct {
	Name        string         `json:"name"`
	Title       string         `json:"title,omitempty"`
	Description string         `json:"description,omitempty"`
	InputSchema map[string]any `json:"inputSchema"`
	Annotations map[string]any `json:"annotations,omitempty"`
	Meta        map[string]any `json:"_meta,omitempty"`
}

// Problem is a refusal a tool call answers as a tool error, the problem
// document its route would.
type Problem struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	Status   int    `json:"status"`
	Detail   string `json:"detail"`
	Code     string `json:"code"`
	Approval string `json:"approval,omitempty"`
}

// CallOutcome is what a tool call answers: its result, or its problem.
type CallOutcome struct {
	Result  any
	Problem *Problem
}

func problem(status int, code, detail string, approval string) CallOutcome {
	return CallOutcome{Problem: &Problem{Type: "about:blank", Title: code, Status: status, Detail: detail, Code: code, Approval: approval}}
}

// ToolSet is the MCP tool set a run's principal is served.
type ToolSet struct {
	tools []MCPTool
	calls map[string]func(ctx context.Context, args map[string]any) (CallOutcome, error)
}

// List is the tools offered.
func (s *ToolSet) List() []MCPTool { return s.tools }

// Call runs the tool named name with args.
func (s *ToolSet) Call(ctx context.Context, name string, args map[string]any) (CallOutcome, error) {
	call, ok := s.calls[name]
	if !ok {
		return problem(404, "not_found", "no tool "+name, ""), nil
	}
	return call(ctx, args)
}

func (s *ToolSet) add(tool MCPTool, call func(ctx context.Context, args map[string]any) (CallOutcome, error)) {
	s.tools = append(s.tools, tool)
	s.calls[tool.Name] = call
}

// allowed reports whether definition lets the run use the tool name: every
// tool for a definition that lists none.
func allowed(definition orchestration.Row, name string) bool {
	if definition.IsNull("tools") {
		return true
	}
	return contains(definition.Strings("tools"), name)
}

func counter(class string) string {
	switch class {
	case Read:
		return "reads"
	case MessageParty:
		return "messagesSent"
	}
	return "writes"
}

// record counts a call on the run and writes it to the transcript.
func (a *Agents) record(ctx context.Context, runID, name string, args any, outcome string, counts map[string]int64) error {
	if len(counts) > 0 {
		if err := a.inTransaction(ctx, func(h orchestration.Host) error {
			current, err := a.run(ctx, h, runID)
			if err != nil || current == nil {
				return err
			}
			update := map[string]any{}
			for field, by := range counts {
				update[field] = current.Int(field) + by
			}
			return a.write(a.self(ctx), h, a.runs, runID, update)
		}); err != nil {
			return err
		}
	}
	return a.appendEntries(ctx, runID, TranscriptEntry{Kind: "call", Tool: name, Args: args, Outcome: outcome})
}

// acting runs fn in a transaction as the run's principal, whose behaviors'
// commands run as it.
func (a *Agents) acting(ctx context.Context, principal RunPrincipal, fn func(ctx context.Context, h orchestration.Host) error) error {
	as := a.host.Acting(ctx, principal.Principal.Principal, principal.Permissions)
	return a.host.Transaction(as, func(nh notifications.Host) error {
		h, err := orchestration.Of(nh)
		if err != nil {
			return err
		}
		return fn(as, h)
	})
}

// statusOf is the HTTP status of a tool's error: its own, the
// repositories' 404 and 403, else 500.
func (a *Agents) statusOf(err error) int {
	var refusal *Error
	if errors.As(err, &refusal) {
		return refusal.Status
	}
	var status interface{ HTTPStatus() int }
	if errors.As(err, &status) {
		return status.HTTPStatus()
	}
	if s := a.host.ErrorStatus(err); s != 0 {
		return s
	}
	var in *inbound.Error
	if errors.As(err, &in) {
		return 403
	}
	return 500
}

func codeOf(status int) string {
	switch status {
	case 404:
		return "not_found"
	case 403:
		return "forbidden"
	}
	return "failed"
}

// MCPTools is the MCP tool set a run's principal is served: the app's
// tools its definition allows and its permissions reach, search,
// message_party and ask_principal.
func (a *Agents) MCPTools(ctx context.Context, principal RunPrincipal) (*ToolSet, error) {
	run, err := a.runOf(ctx, principal)
	if err != nil {
		return nil, err
	}
	definition, err := a.definitionByID(ctx, run.Str("agent"))
	if err != nil {
		return nil, err
	}
	if definition == nil {
		return nil, refuse(404, "not_found", "the run's agent is gone")
	}
	runID := run.Str("id")
	set := &ToolSet{calls: map[string]func(context.Context, map[string]any) (CallOutcome, error){}}
	names := make([]string, 0, len(a.opts.Tools))
	for name := range a.opts.Tools {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		tool := a.opts.Tools[name]
		if !allowed(definition, name) {
			continue
		}
		if tool.Permission != "" && !holds(principal.Permissions, tool.Permission) {
			continue
		}
		listed := MCPTool{Name: name, Description: tool.Description, InputSchema: tool.InputSchema}
		if tool.Untrusted {
			listed.Meta = map[string]any{"untrusted": true}
		}
		set.add(listed, func(ctx context.Context, args map[string]any) (CallOutcome, error) {
			gated, err := a.Gate(ctx, principal, name, args, GateOptions{Class: tool.Class, Mode: tool.Mode})
			if err != nil {
				return CallOutcome{}, err
			}
			if !gated.Run {
				if err := a.record(ctx, runID, name, args, gated.Code, nil); err != nil {
					return CallOutcome{}, err
				}
				return problem(gated.Status, gated.Code, gated.Detail, gated.Approval), nil
			}
			var result any
			runErr := a.acting(ctx, principal, func(ctx context.Context, h orchestration.Host) error {
				var err error
				result, err = tool.Run(ToolContext{Ctx: ctx, Tx: h.Tx(), Host: h, Principal: principal, Run: run, Args: args})
				return err
			})
			if runErr != nil {
				status := a.statusOf(runErr)
				if err := a.record(ctx, runID, name, args, fmt.Sprintf("failed %d", status), nil); err != nil {
					return CallOutcome{}, err
				}
				return problem(status, codeOf(status), a.redactText(runErr.Error()), ""), nil
			}
			counts := map[string]int64{counter(tool.Class): 1}
			if tool.Untrusted {
				counts["untrusted"] = 1
			}
			if err := a.record(ctx, runID, name, args, "done", counts); err != nil {
				return CallOutcome{}, err
			}
			if gated.Mode == Tell {
				if err := a.inTransaction(ctx, func(h orchestration.Host) error {
					return a.raise(ctx, h, a.runs.Type, runID, "agent.acted", map[string]any{"tool": name}, "")
				}); err != nil {
					return CallOutcome{}, err
				}
			}
			if tool.Untrusted {
				return CallOutcome{Result: map[string]any{"untrusted": true, "content": result}}, nil
			}
			return CallOutcome{Result: result}, nil
		})
	}
	if a.opts.Search != nil && allowed(definition, "search") && a.host.CrossTables() != nil {
		spec := crosstable.SearchMCPTool()
		listed := MCPTool{Name: fmt.Sprint(spec["name"]), Title: fmt.Sprint(spec["title"]), Description: fmt.Sprint(spec["description"])}
		listed.InputSchema, _ = spec["inputSchema"].(map[string]any)
		listed.Annotations, _ = spec["annotations"].(map[string]any)
		options := *a.opts.Search
		set.add(listed, func(ctx context.Context, args map[string]any) (CallOutcome, error) {
			result, refusal, err := crosstable.CallSearchTool(ctx, a.host.CrossTables(), crosstable.Caller{Principal: principal.Principal.Principal, Permissions: principal.Permissions}, args, options)
			if err != nil {
				return CallOutcome{}, err
			}
			if refusal != nil {
				if err := a.record(ctx, runID, "search", args, "refused", nil); err != nil {
					return CallOutcome{}, err
				}
				return problem(refusal.Status, refusal.Title, refusal.Detail, ""), nil
			}
			if err := a.record(ctx, runID, "search", args, "done", map[string]int64{"reads": 1}); err != nil {
				return CallOutcome{}, err
			}
			return CallOutcome{Result: result}, nil
		})
	}
	if a.inboundOf() != nil && allowed(definition, "message_party") {
		set.add(MCPTool{
			Name:        "message_party",
			Description: "Sends a message to a party on a channel: the party the run acts for unless another is named. It is held for approval as the autonomy policy says.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"channel", "body"},
				"properties": map[string]any{"channel": map[string]any{"type": "string"}, "body": map[string]any{"type": "string", "minLength": 1}, "subject": map[string]any{"type": "string"}, "party": map[string]any{"type": "object", "properties": map[string]any{"table": map[string]any{"type": "string"}, "key": map[string]any{"type": "string"}}}},
			},
		}, func(ctx context.Context, args map[string]any) (CallOutcome, error) {
			return a.messageParty(ctx, principal, run, args)
		})
	}
	if allowed(definition, "ask_principal") {
		set.add(MCPTool{
			Name:        "ask_principal",
			Description: "Asks a principal a question in a target row's thread; if nobody answers by the deadline, a follow-up row is made.",
			InputSchema: map[string]any{
				"type": "object", "additionalProperties": false, "required": []string{"table", "key", "question"},
				"properties": map[string]any{"table": map[string]any{"type": "string"}, "key": map[string]any{"type": "string"}, "question": map[string]any{"type": "string", "minLength": 1}, "principal": map[string]any{"type": "string"}, "deadlineSeconds": map[string]any{"type": "integer", "minimum": 1}},
			},
		}, func(ctx context.Context, args map[string]any) (CallOutcome, error) {
			return a.askPrincipal(ctx, principal, run, args)
		})
	}
	return set, nil
}

func holds(held []string, permission string) bool {
	for _, p := range held {
		if coversPermission(p, permission) {
			return true
		}
	}
	return false
}

func (a *Agents) messageParty(ctx context.Context, principal RunPrincipal, run orchestration.Row, args map[string]any) (CallOutcome, error) {
	runID := run.Str("id")
	var party *orchestration.PartyRef
	if named, ok := args["party"].(map[string]any); ok && orchestration.TextOf(named["table"]) != "" && orchestration.TextOf(named["key"]) != "" {
		party = &orchestration.PartyRef{Table: orchestration.TextOf(named["table"]), Key: orchestration.TextOf(named["key"])}
	} else if run.Str("actingForKind") == "party" && run.Str("actingForTable") != "" {
		party = &orchestration.PartyRef{Table: run.Str("actingForTable"), Key: run.Str("actingFor")}
	}
	if party == nil {
		return problem(400, "invalid_argument", "the run acts for no party; name one", ""), nil
	}
	subject, _ := args["subject"].(string)
	channel := orchestration.TextOf(args["channel"])
	body := orchestration.TextOf(args["body"])
	// The send's own approval hook is this run's policy (ApproveSend), so
	// the call is not gated twice.
	sent, err := a.inboundOf().Send(ctx, inbound.SendInput{As: principal.Principal, Party: *party, Channel: channel, Body: body, Subject: subject, About: targetsOf(run)})
	if err != nil {
		var refusal *inbound.Error
		if errors.As(err, &refusal) {
			if err := a.record(ctx, runID, "message_party", args, "refused", nil); err != nil {
				return CallOutcome{}, err
			}
			return problem(403, "refused", refusal.Error(), ""), nil
		}
		return CallOutcome{}, err
	}
	if sent.Outcome == inbound.AwaitingApproval && sent.Message != "" {
		params := map[string]any{"party": map[string]any{"table": party.Table, "key": party.Key}, "channel": channel, "body": body}
		if subject != "" {
			params["subject"] = subject
		}
		hash := ParamsHash("send_message", params)
		open, err := orchestration.FindOne(ctx, a.host, a.approvals.Where().Eq("run", runID).Eq("operation", "send_message").Eq("paramsHash", hash).Eq("state", "pending"), "")
		if err != nil {
			return CallOutcome{}, err
		}
		if open != nil {
			if _, err := orchestration.UpdateID(ctx, a.host, a.approvals, open.Str("id"), map[string]any{"holds": sent.Message}); err != nil {
				return CallOutcome{}, err
			}
		}
	}
	counts := map[string]int64{}
	if sent.Outcome == inbound.Sent {
		counts["messagesSent"] = 1
	}
	if err := a.record(ctx, runID, "message_party", args, sent.Outcome, counts); err != nil {
		return CallOutcome{}, err
	}
	switch sent.Outcome {
	case inbound.Refused:
		return problem(403, inbound.Refused, orDefault(sent.Detail, sent.Outcome), ""), nil
	case inbound.Failed:
		return problem(502, inbound.Failed, orDefault(sent.Detail, sent.Outcome), ""), nil
	}
	return CallOutcome{Result: sent}, nil
}

func orDefault(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

func (a *Agents) askPrincipal(ctx context.Context, principal RunPrincipal, run orchestration.Row, args map[string]any) (CallOutcome, error) {
	runID := run.Str("id")
	row := rowrefs.RowRef{Table: orchestration.TextOf(args["table"]), Key: orchestration.TextOf(args["key"])}
	inScope := false
	for _, t := range targetsOf(run) {
		if t.Table == row.Table && t.Key == row.Key {
			inScope = true
		}
	}
	if !inScope {
		if err := a.record(ctx, runID, "ask_principal", args, "forbidden", nil); err != nil {
			return CallOutcome{}, err
		}
		return problem(403, "forbidden", "a run asks only in the thread of one of its target rows", ""), nil
	}
	// Posting in a thread is an internal write: an entered sender's run
	// asks first (D183).
	gated, err := a.Gate(ctx, principal, "ask_principal", args, GateOptions{Class: InternalWrite})
	if err != nil {
		return CallOutcome{}, err
	}
	if !gated.Run {
		if err := a.record(ctx, runID, "ask_principal", args, gated.Code, nil); err != nil {
			return CallOutcome{}, err
		}
		return problem(gated.Status, gated.Code, gated.Detail, gated.Approval), nil
	}
	question := orchestration.TextOf(args["question"])
	asked, _ := args["principal"].(string)
	var message string
	if err := a.acting(ctx, principal, func(ctx context.Context, h orchestration.Host) error {
		var err error
		message, err = a.thread().Post(ctx, h, row, question, asked)
		return err
	}); err != nil {
		status := a.statusOf(err)
		if err := a.record(ctx, runID, "ask_principal", args, fmt.Sprintf("failed %d", status), nil); err != nil {
			return CallOutcome{}, err
		}
		return problem(status, codeOf(status), a.redactText(err.Error()), ""), nil
	}
	delay := int64(a.opts.AskSeconds)
	if d, ok := args["deadlineSeconds"]; ok && d != nil {
		delay = orchestration.IntOf(d)
	}
	if err := a.enqueue(ctx, a.host, Request{Step: "followUp", Run: runID, Ask: &AskDeadline{Table: row.Table, Key: row.Key, Message: message, Question: question}}, secondsOf(delay)); err != nil {
		return CallOutcome{}, err
	}
	if err := a.record(ctx, runID, "ask_principal", args, "asked", map[string]int64{"messagesSent": 1}); err != nil {
		return CallOutcome{}, err
	}
	return CallOutcome{Result: map[string]any{"asked": true, "message": message, "deadlineSeconds": delay}}, nil
}

// thread is how a principal is asked: the app's, or the row's Comments.
func (a *Agents) thread() AskThread {
	if a.opts.Thread != nil {
		return a.opts.Thread
	}
	return commentsThread{a: a}
}

// commentsThread asks in a row's Comments, through the core's post and
// listMessages of the row's table.
type commentsThread struct{ a *Agents }

func (t commentsThread) Post(ctx context.Context, h orchestration.Host, row rowrefs.RowRef, question, principal string) (string, error) {
	use, ok := h.Orchestration().UseOf(row.Table, "comments")
	if !ok {
		return "", refuse(400, "no_thread", "%s keeps no Comments to ask in", row.Table)
	}
	params := map[string]any{"body": question}
	if principal != "" {
		params["mentions"] = []string{principal}
	}
	data, err := json.Marshal(params)
	if err != nil {
		return "", err
	}
	raw, err := h.Operation(ctx, row.Table, row.Key, use, "post", data)
	if err != nil {
		return "", err
	}
	var posted struct {
		ID      json.Number `json:"id"`
		Message json.Number `json:"message"`
	}
	if err := json.Unmarshal(raw, &posted); err != nil {
		return "", err
	}
	if posted.ID != "" {
		return posted.ID.String(), nil
	}
	return posted.Message.String(), nil
}

func (t commentsThread) Answered(ctx context.Context, h orchestration.Host, row rowrefs.RowRef, message, run string) (bool, error) {
	use, ok := h.Orchestration().UseOf(row.Table, "comments")
	if !ok {
		return false, nil
	}
	thread, err := strconv.ParseInt(message, 10, 64)
	if err != nil {
		return false, nil
	}
	data, err := json.Marshal(map[string]any{"thread": thread, "limit": 100})
	if err != nil {
		return false, err
	}
	raw, err := h.Operation(ctx, row.Table, row.Key, use, "listMessages", data)
	if err != nil {
		return false, err
	}
	var listed struct {
		Items []struct {
			ID     int64  `json:"id"`
			Author string `json:"author"`
		} `json:"items"`
	}
	if err := json.Unmarshal(raw, &listed); err != nil {
		return false, err
	}
	for _, m := range listed.Items {
		if m.ID != thread && !orchestration.SameUUID(m.Author, run) {
			return true, nil
		}
	}
	return false, nil
}
