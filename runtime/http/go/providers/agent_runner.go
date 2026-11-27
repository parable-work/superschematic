package providers

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// The program a sandbox instance runs for an agent run (D158, D192), the
// other side of the dispatcher's agent routes: TypeScript's
// runAgentInstance and Rust's run_agent_instance. It reads what the
// dispatcher gave the instance: AGENT_ENDPOINT, AGENT_RUN_ID,
// AGENT_CREDENTIALS (references alone) and the secret AGENT_RUN_TOKEN.
// Each turn it posts the tool results it has to <endpoint>/turn, where the
// app's model provider completes the turn with the run's tools, meters it
// against the budget and writes the transcript, so the instance holds no
// model key and no budget of its own. Each tool call it runs through the
// run's MCP endpoint (<endpoint>/mcp) as the run's token, or, for a tool
// the sandbox runs itself, in process. A call an approval holds (428
// approval_required) is tried again until it runs, is denied, or
// ApprovalWait passes. A credential a tool needs is resolved here, with
// UseCredential: its value goes to the runner that uses it and never to
// the endpoint, the model or a log; the endpoint is told the reference,
// the provider, the target and the time. When the model ends its turns,
// the runner reports the outcome at <endpoint>/finish and returns; the
// dispatcher's watch ends the run.

// RunCredential is one of a run's credential references, as
// AGENT_CREDENTIALS gives them.
type RunCredential struct {
	Ref      string `json:"ref"`
	Site     string `json:"site,omitempty"`
	Header   string `json:"header,omitempty"`
	Selector string `json:"selector,omitempty"`
}

// SandboxToolContext is what a tool the sandbox runs is given.
type SandboxToolContext struct {
	// Credentials are the run's credential references.
	Credentials []RunCredential
	use         func(ctx context.Context, ref string, target InjectionTarget, runner InjectionRunner) (CredentialUse, error)
}

// UseCredential resolves a reference the run holds into target and hands
// the value to runner, recording the use with the run.
func (c SandboxToolContext) UseCredential(ctx context.Context, ref string, target InjectionTarget, runner InjectionRunner) (CredentialUse, error) {
	return c.use(ctx, ref, target, runner)
}

// SandboxToolHandler is a tool the sandbox runs in process.
type SandboxToolHandler func(ctx context.Context, input map[string]any, tc SandboxToolContext) (any, error)

// InstanceCredentials are the credential provider an instance resolves
// references through, and its name for the record.
type InstanceCredentials struct {
	Provider CredentialProvider
	Name     string
}

// AgentInstanceOptions are what RunAgentInstance takes.
type AgentInstanceOptions struct {
	// Env are the instance's settings and Secrets its secrets, as its
	// SandboxSpec gave them; AGENT_RUN_TOKEN is read from either.
	Env     map[string]string
	Secrets map[string]SecretValue
	// Client is the HTTP client; http.DefaultClient unless set.
	Client *http.Client
	// Credentials is what the instance resolves references through.
	Credentials *InstanceCredentials
	// Tools are the tools the sandbox runs itself, by name.
	Tools map[string]SandboxToolHandler
	// ApprovalWait is how long a call waits for its approval (a minute
	// unless set), Poll how often it is tried again (a second), and
	// MaxTurns the most turns it takes (20).
	ApprovalWait time.Duration
	Poll         time.Duration
	MaxTurns     int
}

// AgentInstanceResult is what a run of the instance came to: done,
// failed or stopped.
type AgentInstanceResult struct {
	Outcome string
	Turns   int
	Summary string
}

// rpcBody is the JSON-RPC answer of an MCP endpoint: its body, or the data
// of the one message event it streams.
func rpcBody(text string) []byte {
	var data strings.Builder
	for _, line := range strings.Split(text, "\n") {
		line = strings.TrimRight(line, "\r")
		if strings.HasPrefix(line, "data:") {
			data.WriteString(strings.TrimSpace(line[len("data:"):]))
		}
	}
	if data.Len() > 0 {
		return []byte(data.String())
	}
	return []byte(text)
}

// RunAgentInstance runs an agent run's turns inside its sandbox instance
// until the model ends them, the run is stopped, or ctx is done.
func RunAgentInstance(ctx context.Context, options AgentInstanceOptions) (AgentInstanceResult, error) {
	endpoint := strings.TrimRight(options.Env["AGENT_ENDPOINT"], "/")
	token := options.Env["AGENT_RUN_TOKEN"]
	if secret, ok := options.Secrets["AGENT_RUN_TOKEN"]; ok && !secret.IsZero() {
		token = secret.Reveal()
	}
	if endpoint == "" || token == "" {
		return AgentInstanceResult{}, errors.New("RunAgentInstance: AGENT_ENDPOINT and AGENT_RUN_TOKEN are the instance's")
	}
	client := options.Client
	if client == nil {
		client = http.DefaultClient
	}
	var credentials []RunCredential
	if text := options.Env["AGENT_CREDENTIALS"]; text != "" {
		if err := json.Unmarshal([]byte(text), &credentials); err != nil {
			return AgentInstanceResult{}, fmt.Errorf("RunAgentInstance: AGENT_CREDENTIALS: %w", err)
		}
	}
	post := func(path string, body any) (int, []byte, error) {
		data, err := json.Marshal(body)
		if err != nil {
			return 0, nil, err
		}
		request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint+"/"+path, bytes.NewReader(data))
		if err != nil {
			return 0, nil, err
		}
		request.Header.Set("Authorization", "Bearer "+token)
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Accept", "application/json, text/event-stream")
		response, err := client.Do(request)
		if err != nil {
			return 0, nil, err
		}
		defer response.Body.Close()
		answer, err := io.ReadAll(response.Body)
		return response.StatusCode, answer, err
	}
	tc := SandboxToolContext{
		Credentials: credentials,
		use: func(ctx context.Context, ref string, target InjectionTarget, runner InjectionRunner) (CredentialUse, error) {
			if options.Credentials == nil {
				return CredentialUse{}, errors.New("the instance has no credential provider")
			}
			held := false
			for _, c := range credentials {
				if c.Ref == ref {
					held = true
				}
			}
			if !held {
				return CredentialUse{}, fmt.Errorf("the run holds no credential %s", ref)
			}
			return UseCredential(ctx, UseCredentialInput{
				Provider: options.Credentials.Provider, ProviderName: options.Credentials.Name, Ref: ref, Target: target, Runner: runner,
				Record: func(ctx context.Context, use CredentialUse) error {
					_, _, err := post("credential", map[string]any{"ref": use.Ref, "provider": use.Provider, "target": use.Target, "usedAt": use.UsedAt.UTC().Format(time.RFC3339Nano)})
					return err
				},
			})
		},
	}

	rpc := 0
	mcp := func(name string, args json.RawMessage) (string, int, error) {
		rpc++
		if len(args) == 0 || string(args) == "null" {
			args = json.RawMessage("{}")
		}
		status, answer, err := post("mcp", map[string]any{"jsonrpc": "2.0", "id": rpc, "method": "tools/call", "params": map[string]any{"name": name, "arguments": args}})
		if err != nil {
			return "", 0, err
		}
		if status == http.StatusUnauthorized {
			return "the run has ended", 401, nil
		}
		var body struct {
			Result *struct {
				Content []struct {
					Text string `json:"text"`
				} `json:"content"`
				IsError           bool `json:"isError"`
				StructuredContent struct {
					Status int `json:"status"`
				} `json:"structuredContent"`
			} `json:"result"`
			Error *struct {
				Message string `json:"message"`
			} `json:"error"`
		}
		if err := json.Unmarshal(rpcBody(string(answer)), &body); err != nil {
			return "", 0, err
		}
		if body.Error != nil {
			return body.Error.Message, 400, nil
		}
		var text strings.Builder
		if body.Result != nil {
			for _, c := range body.Result.Content {
				text.WriteString(c.Text)
			}
			if body.Result.IsError {
				status := body.Result.StructuredContent.Status
				if status == 0 {
					var problem struct {
						Status int `json:"status"`
					}
					_ = json.Unmarshal([]byte(text.String()), &problem)
					status = problem.Status
				}
				return text.String(), status, nil
			}
		}
		return text.String(), 0, nil
	}
	wait := options.ApprovalWait
	if wait <= 0 {
		wait = time.Minute
	}
	poll := options.Poll
	if poll <= 0 {
		poll = time.Second
	}
	runTool := func(call ModelToolCall) (string, error) {
		if local, ok := options.Tools[call.Name]; ok {
			input := map[string]any{}
			if len(call.Input) > 0 {
				_ = json.Unmarshal(call.Input, &input)
			}
			result, err := local(ctx, input, tc)
			if err != nil {
				data, _ := json.Marshal(map[string]any{"error": err.Error()})
				return string(data), nil
			}
			data, err := json.Marshal(result)
			return string(data), err
		}
		until := time.Now().Add(wait)
		for {
			text, status, err := mcp(call.Name, call.Input)
			if err != nil {
				return "", err
			}
			if status != 428 || time.Now().After(until) {
				return text, nil
			}
			select {
			case <-ctx.Done():
				return "", ctx.Err()
			case <-time.After(poll):
			}
		}
	}

	maxTurns := options.MaxTurns
	if maxTurns <= 0 {
		maxTurns = 20
	}
	messages := []ModelMessage{}
	summary := ""
	for turn := 1; turn <= maxTurns; turn++ {
		status, answer, err := post("turn", map[string]any{"messages": messages})
		if err != nil {
			return AgentInstanceResult{Outcome: "stopped", Turns: turn - 1, Summary: summary}, err
		}
		messages = []ModelMessage{}
		if status == http.StatusConflict || status == http.StatusUnauthorized {
			return AgentInstanceResult{Outcome: "stopped", Turns: turn - 1, Summary: summary}, nil
		}
		if status < 200 || status > 299 {
			_, _, _ = post("finish", map[string]any{"outcome": "failed", "summary": fmt.Sprintf("the turn answered %d", status)})
			return AgentInstanceResult{Outcome: "failed", Turns: turn, Summary: summary}, nil
		}
		var completion ModelCompletion
		if err := json.Unmarshal(answer, &completion); err != nil {
			return AgentInstanceResult{Outcome: "failed", Turns: turn, Summary: summary}, err
		}
		summary = completion.Content
		if len(completion.ToolCalls) == 0 {
			_, _, err := post("finish", map[string]any{"outcome": "done", "summary": summary})
			return AgentInstanceResult{Outcome: "done", Turns: turn, Summary: summary}, err
		}
		for _, call := range completion.ToolCalls {
			content, err := runTool(call)
			if err != nil {
				return AgentInstanceResult{Outcome: "stopped", Turns: turn, Summary: summary}, err
			}
			messages = append(messages, ModelMessage{Role: RoleTool, ToolCallID: call.ID, Content: content})
		}
	}
	_, _, err := post("finish", map[string]any{"outcome": "done", "summary": summary})
	return AgentInstanceResult{Outcome: "done", Turns: maxTurns, Summary: summary}, err
}
