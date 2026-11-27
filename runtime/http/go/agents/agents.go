// Package agents is D158's agent orchestration in Go (D192): agents are
// rows, each run gets a fresh sandbox instance and a token held to its
// task, and autonomy is set per operation, with budgets and stop switches.
// It is the TypeScript runtime's agents.ts, with its tables, statements,
// states and refusals, with D183's trust and narrowed paths, and keeps to
// the points the Rust port (D193) settled.
//
// The tables are the database's classes marked @agents("<part>"), whose
// fields the loader fills: the definitions (a @versioned table, so a run
// records the version it ran), the runs, the dispatcher's request queue,
// the autonomy policies, the approvals an ask writes, and the controls
// (one row per agent and one for every agent: the stop switch, and a
// Budget whose meter each agent's usage counts in, and every agent's
// through its scope). A table that composes behaviors or is @versioned is
// written through its repository, and its Lease, Retries, Workflow and
// Budget calls are the behaviors' operations through the core; the
// policies and the approvals, which compose none, by statements over the
// columns the generated ORM's definitions name.
//
// Triggers. A run request is a message of the request queue: a route
// naming an agent (the notifier's { agent } recipient, D153), a row
// assigned to an agent, a schedule, a principal asking, or an inbound
// message from a party (D157), { trigger: "inbound", party, message, grant
// }, which names no agent: the definition whose inbound is set takes it.
// Request enqueues one in the caller's transaction. A principal of kind
// agent may not request a run: only the dispatcher starts one.
//
// The dispatcher (Handle) is the queue's worker. It refuses a request
// while the agent or every agent is paused or over its budget, writes the
// run row, holds the run's Lease (D104) while its instance runs, mints the
// run's token (a grant per target row, D156, held by the run, ended when
// the run ends), and starts a sandbox instance (D159) with the definition,
// the token, the endpoint's URL and the credential references, never their
// values. A failure to start is recorded with Retries, whose backoff sets
// when the start is tried again. A watch message polls the instance until
// it ends; the dispatcher then revokes the token, meters the instance's
// cost, writes the redacted transcript and records the outcome. The
// outcome, the cost and the transcript go in before the status moves, so a
// reader that sees a run ended also sees its outcome.
//
// The run's principal (PrincipalOf) is kind agent, acting for whom the run
// acts for, holding the agent's role's permissions narrowed to those whom
// it acts for holds, and reaching the run's targets and the rows the
// role's paths reach from them; a run acting for a party reaches through
// only the paths the party's grant of the same target reaches too, read on
// every call (D183).
//
// Autonomy. Each tool call passes Gate: the most specific policy row for
// the agent and the operation or its class says auto, tell or ask, else
// the tool's mode, else the class's default. A money movement asks
// whatever a row says, and so does every operation but a read of a run
// whose sender was entered by hand (D183). ask writes an approval holding
// the exact parameters and their hash, raises approval.requested on it,
// and answers 428 approval_required; the same call runs once an approval
// of the same hash is approved, and spends it.
//
// Budgets, stop switches, asking a principal and transcripts are as
// agents.ts has them: each turn's usage counts on the agent's control
// row's Budget meter; pause cancels every run in flight through its
// sandbox provider; ask_principal posts in a target row's thread and
// enqueues a follow-up at the deadline; a transcript is redacted as each
// entry is written, and each turn replays the run's earlier completions
// with their tool calls and the provider's state of them.
//
// The pure parts are shared with the TypeScript and Rust runtimes through
// runtime/http/testdata/agents_parity.json. The dispatcher is Postgres's
// alone, as the loader holds it to.
package agents

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/parable-work/superschematic/runtime/http/go/inbound"
	"github.com/parable-work/superschematic/runtime/http/go/notify"
	"github.com/parable-work/superschematic/runtime/http/go/providers"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"
	"github.com/parable-work/superschematic/runtime/schema/go/notifications"
	"github.com/parable-work/superschematic/runtime/schema/go/orchestration"
	"github.com/parable-work/superschematic/runtime/schema/go/rowrefs"
)

// The triggers of a run (D158).
const (
	TriggerRoute     = "route"
	TriggerAssigned  = "assigned"
	TriggerSchedule  = "schedule"
	TriggerPrincipal = "principal"
	TriggerInbound   = "inbound"
)

// The permissions of administering agents (D158).
const (
	PermissionDefine      = "agents.define"
	PermissionAutonomy    = "agents.autonomy"
	PermissionBudgets     = "agents.budgets"
	PermissionCredentials = "agents.credentials"
	PermissionPause       = "agents.pause"
	PermissionApprove     = "agents.approve"
)

// DefaultTranscriptRetentionDays is how long a transcript is kept after
// its run ends.
const DefaultTranscriptRetentionDays = 30

// overBudget is the operation an approval to run past every agent's budget
// is of.
const overBudget = "agents.overBudget"

// PrincipalRef is who asked for a run, or whom it acts for: kind user,
// party, agent or route, its subject, and a party's table.
type PrincipalRef struct {
	Kind    string `json:"kind"`
	Subject string `json:"subject"`
	Table   string `json:"table,omitempty"`
}

// AskDeadline is an ask's deadline: the row it was asked on, the thread's
// message and the question.
type AskDeadline struct {
	Table    string `json:"table"`
	Key      string `json:"key"`
	Message  string `json:"message"`
	Question string `json:"question"`
}

// Request is a message of the dispatcher's queue (AgentRunRequest): a run
// request, whose step is start (or ""), and the dispatcher's own watch and
// followUp steps. Section 7's inbound message (D157) is { trigger:
// "inbound", party, message, grant }.
type Request struct {
	Step string `json:"step,omitempty"`
	// Agent is the agent, by name or key; "" for an inbound message,
	// which the definition whose inbound is set takes.
	Agent   string `json:"agent,omitempty"`
	Trigger string `json:"trigger,omitempty"`
	// SubjectTable and SubjectKey are the row the run is about.
	SubjectTable string `json:"subjectTable,omitempty"`
	SubjectKey   string `json:"subjectKey,omitempty"`
	// Targets are more rows of the run's scope.
	Targets []rowrefs.RowRef `json:"targets,omitempty"`
	// Event is a route's event.
	Event any `json:"event,omitempty"`
	// Input is what the run is told: { task }.
	Input       map[string]any `json:"input,omitempty"`
	RequestedBy *PrincipalRef  `json:"requestedBy,omitempty"`
	ActingFor   *PrincipalRef  `json:"actingFor,omitempty"`
	// Party is an inbound message's party (D157).
	Party *rowrefs.RowRef `json:"party,omitempty"`
	// Message is an inbound message: its row's key, its row as { table,
	// key }, or its text as { body }.
	Message any `json:"message,omitempty"`
	// Grant is the party's active grant, whose target is the run's scope.
	Grant string `json:"grant,omitempty"`
	// Run is the run a watch, a retry, a follow-up or an approved start is
	// of.
	Run string       `json:"run,omitempty"`
	Ask *AskDeadline `json:"ask,omitempty"`
}

// Error is a refused administrative call or request: its HTTP status and
// its code.
type Error struct {
	Status  int
	Code    string
	Message string
}

func (e *Error) Error() string { return e.Message }

func refuse(status int, code, format string, args ...any) error {
	return &Error{Status: status, Code: code, Message: fmt.Sprintf(format, args...)}
}

// Caller is a behaviors' caller: the subject the core's commands run as,
// and the permissions it holds.
type Caller struct {
	Subject     string
	Permissions []string
}

// ToolContext is what a tool of the app is given: Ctx acts as the run's
// principal, whose reads and writes keep to its reach and whose behaviors'
// commands run as it (the repositories' operations), in Tx, the ORM's
// transaction (its TxInterface) of Host.
type ToolContext struct {
	Ctx       context.Context
	Tx        any
	Host      orchestration.Host
	Principal RunPrincipal
	Run       orchestration.Row
	Args      map[string]any
}

// Tool is a tool of the app an agent may call through the run's MCP
// endpoint.
type Tool struct {
	Description string
	InputSchema map[string]any
	// Class is what the tool is to the autonomy policy.
	Class string
	// Mode is the autonomy when no policy row names it; the class's
	// default when "".
	Mode string
	// Permission is a permission the run's principal holds for the tool to
	// be offered.
	Permission string
	// Untrusted marks a tool whose results carry outside content.
	Untrusted bool
	Run       func(tc ToolContext) (any, error)
}

// SandboxTool is a tool the sandbox runs itself (a browser, a fetch with a
// credential): offered to the model, run inside the instance.
type SandboxTool struct {
	Name        string
	Description string
	InputSchema map[string]any
	Untrusted   bool
}

// TranscriptStore is where a run's transcript is kept once it ends.
type TranscriptStore interface {
	Put(ctx context.Context, name string, body []byte) error
	Get(ctx context.Context, name string) ([]byte, error)
	Delete(ctx context.Context, name string) error
}

// AskThread is how a principal is asked in a row's thread; the row's
// Comments unless the options give one.
type AskThread interface {
	// Post posts question on row as the run (ctx acts as it, in h),
	// mentioning principal, and answers the message's id.
	Post(ctx context.Context, h orchestration.Host, row rowrefs.RowRef, question, principal string) (string, error)
	// Answered reports whether someone other than the run answered the
	// message.
	Answered(ctx context.Context, h orchestration.Host, row rowrefs.RowRef, message, run string) (bool, error)
}

// SandboxExtra is what SandboxSpec adds to a run's sandbox: plain
// settings and secrets beside the run's own.
type SandboxExtra struct {
	Env     map[string]string
	Secrets map[string]providers.SecretValue
}

// Options are what New takes.
type Options struct {
	// Endpoint is the URL the sandbox reaches the agent routes at.
	Endpoint string
	// Providers are the environment's providers (D159): the sandbox and
	// model slots the definitions name, or the defaults below.
	Providers *providers.Providers
	// SandboxSlot and ModelSlot are the slots of a definition that names
	// none; the only one configured unless set.
	SandboxSlot string
	ModelSlot   string
	// Notifier raises an approval's request, a budget warning and a tell;
	// SetNotifier gives it to a dispatcher built before it.
	Notifier *notify.Notifier
	// Inbound is the inbound path (D157): an inbound message's run reads
	// the message through it, and message_party sends through it;
	// SetInbound gives it to a dispatcher built before it.
	Inbound *inbound.Inbound
	// Transcripts is where a run's transcript goes when it ends; on the
	// run row alone when nil.
	Transcripts TranscriptStore
	// Tools are the app's tools, by name, and SandboxTools the tools the
	// sandbox runs itself.
	Tools        map[string]Tool
	SandboxTools []SandboxTool
	// Search is search across tables (D155) as the tool search, run as the
	// run's principal; nil offers none.
	Search *crosstable.SearchOptions
	// Thread is how a principal is asked in a row's thread; the row's
	// Comments unless set.
	Thread AskThread
	// FollowUp makes the follow-up row of an ask nobody answered by its
	// deadline, in h.
	FollowUp func(ctx context.Context, h orchestration.Host, run orchestration.Row, row rowrefs.RowRef, question string) error
	// OnReturn returns a row assigned to an agent to its queue when the
	// agent is paused and its run cancelled, in h.
	OnReturn func(ctx context.Context, h orchestration.Host, run orchestration.Row, row rowrefs.RowRef) error
	// SandboxSpec is what the sandbox gets beside the run's own.
	SandboxSpec func(run, definition orchestration.Row) SandboxExtra
	// Dispatcher is the behaviors' caller the dispatcher writes runs and
	// controls as, and holds a run's lease as; agent-dispatcher unless set.
	Dispatcher Caller
	// CostOf is the units a turn's usage costs (its tokens unless set), and
	// SandboxCostOf an instance's (its wall seconds).
	CostOf        func(providers.ModelUsage) int64
	SandboxCostOf func(providers.SandboxCost) int64
	// Meter is the Budget meter of the controls; cost unless set.
	Meter string
	// WatchSeconds (5), AskSeconds (86400), ApprovalTTLSeconds (86400) and
	// TranscriptRetentionDays (30) are the defaults' unless set.
	WatchSeconds            int
	AskSeconds              int
	ApprovalTTLSeconds      int
	TranscriptRetentionDays int
	// Redact are more texts a transcript entry is redacted of.
	Redact []string
	// Now is the clock; time.Now unless set.
	Now func() time.Time
	// Logger logs what the dispatcher skips; a no-op unless set.
	Logger *zap.Logger
}

// Agents is the orchestration over a database's orchestration.Host. It is
// safe for concurrent use.
type Agents struct {
	host     orchestration.Host
	opts     Options
	log      *zap.Logger
	endpoint string
	base     string

	definitions, runs, policies, approvals, controls orchestration.Table
	// total is the relation of the controls to the table of every agent's
	// budget, its Budget meter's scope; nil for none.
	total        *orchestration.Relation
	messagesType string

	mu       sync.RWMutex
	notifier *notify.Notifier
	inbound  *inbound.Inbound
}

// New is the orchestration over host, the generated ORM's
// Orchestration(): it refuses a database with no @agents tables.
func New(host orchestration.Host, opts Options) (*Agents, error) {
	if host == nil {
		return nil, errors.New("agents: New needs the database's Orchestration()")
	}
	defs := host.Orchestration()
	a := &Agents{host: host, opts: opts, log: opts.Logger, notifier: opts.Notifier, inbound: opts.Inbound}
	if a.log == nil {
		a.log = zap.NewNop()
	}
	for part, into := range map[string]*orchestration.Table{"definitions": &a.definitions, "runs": &a.runs, "policies": &a.policies, "approvals": &a.approvals, "controls": &a.controls} {
		p, ok := defs.PartOf(part)
		if !ok {
			return nil, fmt.Errorf("agents: the database has no @agents(%q) table", part)
		}
		*into = orchestration.Table{Part: p}
	}
	if defs.Requests == "" || host.QueueOf(orchestration.Requests) == nil {
		return nil, errors.New(`agents: the database has no @agents("requests") queue`)
	}
	for _, rel := range defs.Tables[a.controls.Type].Relations {
		if rel.Target != a.controls.Type {
			r := rel
			a.total = &r
			break
		}
	}
	if messages, ok := defs.PartOf("messages"); ok {
		a.messagesType = messages.Type
	}
	endpoint := strings.TrimRight(opts.Endpoint, "/")
	if endpoint == "" {
		return nil, errors.New("agents: New needs the endpoint the sandbox reaches the agent routes at")
	}
	a.endpoint = endpoint
	a.base = pathOf(endpoint)
	if a.opts.Dispatcher.Subject == "" {
		a.opts.Dispatcher = Caller{Subject: "agent-dispatcher", Permissions: []string{}}
	}
	if a.opts.Meter == "" {
		a.opts.Meter = "cost"
	}
	if a.opts.WatchSeconds <= 0 {
		a.opts.WatchSeconds = 5
	}
	if a.opts.AskSeconds <= 0 {
		a.opts.AskSeconds = 86_400
	}
	if a.opts.ApprovalTTLSeconds <= 0 {
		a.opts.ApprovalTTLSeconds = 86_400
	}
	if a.opts.TranscriptRetentionDays <= 0 {
		a.opts.TranscriptRetentionDays = DefaultTranscriptRetentionDays
	}
	if a.opts.CostOf == nil {
		a.opts.CostOf = func(u providers.ModelUsage) int64 { return int64(u.InputTokens + u.OutputTokens) }
	}
	if a.opts.SandboxCostOf == nil {
		a.opts.SandboxCostOf = func(c providers.SandboxCost) int64 { return int64(math.Ceil(c.WallSeconds)) }
	}
	if a.opts.Tools == nil {
		a.opts.Tools = map[string]Tool{}
	}
	return a, nil
}

// SetNotifier gives the dispatcher the notifier, which is built after it
// since it takes the dispatcher's reply actions and its requests (D193).
func (a *Agents) SetNotifier(n *notify.Notifier) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.notifier = n
}

// SetInbound gives the dispatcher the inbound path, which is built after
// it since its send approval is the dispatcher's (D193).
func (a *Agents) SetInbound(in *inbound.Inbound) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.inbound = in
}

func (a *Agents) notifierOf() *notify.Notifier {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.notifier
}

func (a *Agents) inboundOf() *inbound.Inbound {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return a.inbound
}

// Host is the database the dispatcher runs through.
func (a *Agents) Host() orchestration.Host { return a.host }

func (a *Agents) now() time.Time {
	if a.opts.Now != nil {
		return a.opts.Now()
	}
	return time.Now()
}

// self is ctx as the dispatcher: every row, its behaviors' commands run as
// the dispatcher's caller.
func (a *Agents) self(ctx context.Context) context.Context {
	return a.host.Behaving(ctx, a.opts.Dispatcher.Subject, a.opts.Dispatcher.Permissions)
}

// ---- rows ----

// updateJSON is values as a repository's update takes them: a null as the
// field's SetNull.
func updateJSON(values map[string]any) (json.RawMessage, error) {
	out := make(map[string]any, len(values))
	for field, value := range values {
		if value == nil {
			out[field+"SetNull"] = true
			continue
		}
		out[field] = value
	}
	return json.Marshal(out)
}

// write sets values on the row of t whose id is id, through its
// repository as ctx's caller.
func (a *Agents) write(ctx context.Context, h orchestration.Host, t orchestration.Table, id string, values map[string]any) error {
	data, err := updateJSON(values)
	if err != nil {
		return err
	}
	return h.Update(ctx, t.Type, id, data)
}

// create writes a row of t through its repository as ctx's caller and
// reads it back by its statements.
func (a *Agents) create(ctx context.Context, h orchestration.Host, t orchestration.Table, values map[string]any) (orchestration.Row, error) {
	input := make(map[string]any, len(values))
	for field, value := range values {
		if value != nil {
			input[field] = value
		}
	}
	data, err := json.Marshal(input)
	if err != nil {
		return nil, err
	}
	created, err := h.Create(ctx, t.Type, data)
	if err != nil {
		return nil, err
	}
	row, err := orchestration.ByID(ctx, h, t, orchestration.TextOf(created["id"]))
	if err != nil || row == nil {
		return nil, fmt.Errorf("agents: read back a row of %s: %v", t.Type, err)
	}
	return row, nil
}

func (a *Agents) run(ctx context.Context, h orchestration.Statements, id string) (orchestration.Row, error) {
	return orchestration.ByID(ctx, h, a.runs, id)
}

func (a *Agents) definitionByID(ctx context.Context, id string) (orchestration.Row, error) {
	return orchestration.ByID(ctx, a.host, a.definitions, id)
}

// findDefinition is the agent by name, then by key; nil for none.
func (a *Agents) findDefinition(ctx context.Context, agent string) (orchestration.Row, error) {
	found, err := orchestration.FindOne(ctx, a.host, a.definitions.Where().Eq("name", agent), "")
	if err != nil || found != nil {
		return found, err
	}
	return a.definitionByID(ctx, agent)
}

// agentID is the key of the agent named or keyed agent, agent itself for
// none.
func (a *Agents) agentID(ctx context.Context, agent string) (string, error) {
	found, err := a.findDefinition(ctx, agent)
	if err != nil {
		return "", err
	}
	if found != nil {
		return found.Str("id"), nil
	}
	return agent, nil
}

func ended(status string) bool {
	return status == "done" || status == "failed" || status == "cancelled"
}

func targetsOf(run orchestration.Row) []rowrefs.RowRef {
	var out []rowrefs.RowRef
	list, _ := run["targets"].([]any)
	for _, item := range list {
		m, _ := item.(map[string]any)
		out = append(out, rowrefs.RowRef{Table: orchestration.TextOf(m["table"]), Key: orchestration.TextOf(m["key"])})
	}
	return out
}

// ---- redaction and the transcript ----

// TranscriptEntry is one entry of a run's transcript, redacted as it is
// written.
type TranscriptEntry struct {
	At         string           `json:"at,omitempty"`
	Kind       string           `json:"kind"`
	Role       string           `json:"role,omitempty"`
	Content    string           `json:"content,omitempty"`
	ToolCallID string           `json:"toolCallId,omitempty"`
	ToolCalls  []TranscriptCall `json:"toolCalls,omitempty"`
	// ProviderState is a completion's providerState, which the turns after
	// it pass back on its assistant message: the one member kept
	// unredacted while the run lives; a read of the transcript, and the
	// transcript the run leaves, hold it redacted.
	ProviderState json.RawMessage `json:"providerState,omitempty"`
	Tool          string          `json:"tool,omitempty"`
	Args          any             `json:"args,omitempty"`
	Outcome       string          `json:"outcome,omitempty"`
	Untrusted     bool            `json:"untrusted,omitempty"`
	Ref           string          `json:"ref,omitempty"`
}

// TranscriptCall is a tool call a completion made.
type TranscriptCall struct {
	ID    string          `json:"id"`
	Name  string          `json:"name"`
	Input json.RawMessage `json:"input"`
}

func (a *Agents) redactText(text string, secrets ...string) string {
	return RedactTokens(text, secrets, a.opts.Redact)
}

// redactJSON is v as JSON, redacted as text.
func (a *Agents) redactJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		return json.RawMessage("null")
	}
	text := a.redactText(string(data))
	if !json.Valid([]byte(text)) {
		return data
	}
	return json.RawMessage(text)
}

// redactEntries are entries with every member redacted, providerState
// among them.
func (a *Agents) redactEntries(entries []TranscriptEntry) []TranscriptEntry {
	var out []TranscriptEntry
	if err := json.Unmarshal(a.redactJSON(entries), &out); err != nil {
		return entries
	}
	return out
}

func entriesOf(v any) []TranscriptEntry {
	if v == nil {
		return nil
	}
	data, err := json.Marshal(v)
	if err != nil {
		return nil
	}
	var out []TranscriptEntry
	_ = json.Unmarshal(data, &out)
	return out
}

// appendEntries writes entries at the end of the run's transcript, every
// member redacted but a completion's providerState.
func (a *Agents) appendEntries(ctx context.Context, runID string, entries ...TranscriptEntry) error {
	return a.host.Transaction(ctx, func(nh notifications.Host) error {
		h, err := orchestration.Of(nh)
		if err != nil {
			return err
		}
		row, err := a.run(ctx, h, runID)
		if err != nil || row == nil {
			return err
		}
		at := a.now().UTC().Format("2006-01-02T15:04:05.000Z")
		next := entriesOf(row["transcript"])
		for _, entry := range entries {
			state := entry.ProviderState
			entry.ProviderState = nil
			entry.At = at
			var redacted TranscriptEntry
			if err := json.Unmarshal(a.redactJSON(entry), &redacted); err != nil {
				redacted = entry
			}
			redacted.ProviderState = state
			next = append(next, redacted)
		}
		return a.write(a.self(ctx), h, a.runs, runID, map[string]any{"transcript": next})
	})
}

// ---- definitions, controls and budgets ----

// controlOf is the control row of agent, or of every agent for "", made
// when there is none.
func (a *Agents) controlOf(ctx context.Context, agent string) (orchestration.Row, error) {
	w := a.controls.Where()
	if agent == "" {
		w.Null("agent")
	} else {
		w.Eq("agent", agent)
	}
	found, err := orchestration.FindOne(ctx, a.host, w, "")
	if err != nil || found != nil {
		return found, err
	}
	// The column's default, which a create of the Go ORM's would write as
	// its zero.
	values := map[string]any{"warnAt": 80}
	if agent != "" {
		values["agent"] = agent
		if a.total != nil {
			every, err := a.controlOf(ctx, "")
			if err != nil {
				return nil, err
			}
			if key := every.Str(a.total.Column); key != "" {
				values[a.total.Member] = map[string]any{"id": key}
			}
		}
	}
	return a.create(a.self(ctx), a.host, a.controls, values)
}

// operation runs a behavior's operation of typeName's use under member on
// the row key names, as ctx's behavior caller.
func (a *Agents) operation(ctx context.Context, typeName, member, key, operation string, params any) (map[string]any, bool, error) {
	use, ok := a.host.Orchestration().UseOf(typeName, member)
	if !ok {
		return nil, false, nil
	}
	data, err := json.Marshal(params)
	if err != nil {
		return nil, true, err
	}
	raw, err := a.host.Operation(ctx, typeName, key, use, operation, data)
	if err != nil {
		return nil, true, err
	}
	var out map[string]any
	if len(raw) > 0 && string(raw) != "null" {
		if err := json.Unmarshal(raw, &out); err != nil {
			return nil, true, fmt.Errorf("agents: %s's %s %s answered %s: %w", typeName, member, operation, raw, err)
		}
	}
	return out, true, nil
}

// fits reports whether a Budget table's meter fits one more unit on the
// row key names; true for a table with no Budget.
func (a *Agents) fits(ctx context.Context, typeName, key string) (bool, error) {
	if key == "" {
		return true, nil
	}
	answer, ok, err := a.operation(a.self(ctx), typeName, "budget", key, "checkReserve", map[string]any{"meter": a.opts.Meter, "amount": 1})
	if err != nil || !ok {
		return true, err
	}
	fits, _ := answer["fits"].(bool)
	return fits, nil
}

// standing is where the agent stands: paused (itself or every agent), and
// whether it or every agent is over its cap.
type standing struct {
	paused                   bool
	reason                   string
	agentCapped, totalCapped bool
}

func (a *Agents) standingOf(ctx context.Context, agent string) (standing, error) {
	every, err := a.controlOf(ctx, "")
	if err != nil {
		return standing{}, err
	}
	own, err := a.controlOf(ctx, agent)
	if err != nil {
		return standing{}, err
	}
	var s standing
	if a.total != nil {
		holder := every
		if own.Str(a.total.Column) != "" {
			holder = own
		}
		fits, err := a.fits(ctx, a.total.Target, holder.Str(a.total.Column))
		if err != nil {
			return s, err
		}
		s.totalCapped = !fits
	}
	// The agent's own meter answers for every scope it spends within:
	// past every agent's cap it fits nowhere, which is not its own cap.
	if !s.totalCapped {
		fits, err := a.fits(ctx, a.controls.Type, own.Str("id"))
		if err != nil {
			return s, err
		}
		s.agentCapped = !fits
	}
	reasonOf := func(r orchestration.Row) string {
		if why := r.Str("pausedReason"); why != "" {
			return why
		}
		return "stopped"
	}
	switch {
	case every.Bool("paused"):
		s.paused, s.reason = true, reasonOf(every)
	case own.Bool("paused"):
		s.paused, s.reason = true, reasonOf(own)
	}
	return s, nil
}

// raise raises kind on the row of table key names in h when the table
// declares it and there is a notifier.
func (a *Agents) raise(ctx context.Context, h orchestration.Host, table, key, kind string, payload map[string]any, urgency string) error {
	n := a.notifierOf()
	if n == nil {
		return nil
	}
	if _, ok := a.host.Definitions().EventOf(table, kind); !ok {
		return nil
	}
	_, err := n.Raise(ctx, h, notify.RaiseInput{Table: table, Key: key, Kind: kind, Payload: payload, Urgency: urgency})
	return err
}

func (a *Agents) inTransaction(ctx context.Context, fn func(h orchestration.Host) error) error {
	return a.host.Transaction(ctx, func(nh notifications.Host) error {
		h, err := orchestration.Of(nh)
		if err != nil {
			return err
		}
		return fn(h)
	})
}

// meterUsage records usage on the agent's control row: warns at its
// threshold and pauses the agent at its cap.
func (a *Agents) meterUsage(ctx context.Context, agent string, amount int64) error {
	if amount <= 0 {
		return nil
	}
	own, err := a.controlOf(ctx, agent)
	if err != nil {
		return err
	}
	used, ok, err := a.operation(a.self(ctx), a.controls.Type, "budget", own.Str("id"), "recordUsage", map[string]any{"meter": a.opts.Meter, "amount": amount})
	if err != nil || !ok {
		return err
	}
	var limit *int64
	if !own.IsNull("budgetLimit") {
		l := own.Int("budgetLimit")
		limit = &l
	}
	after := orchestration.IntOf(used["used"])
	before := after - amount
	warnAt := int64(80)
	if !own.IsNull("warnAt") {
		warnAt = own.Int("warnAt")
	}
	warns, capped := BudgetCrossing(before, after, limit, warnAt)
	if warns {
		payload := map[string]any{"used": after, "warnAt": warnAt}
		if limit != nil {
			payload["limit"] = *limit
		}
		if err := a.inTransaction(ctx, func(h orchestration.Host) error {
			return a.raise(ctx, h, a.controls.Type, own.Str("id"), "agent.budgetWarning", payload, "")
		}); err != nil {
			return err
		}
	}
	overrun := false
	if list, ok := used["overruns"].([]any); ok {
		for _, item := range list {
			m, _ := item.(map[string]any)
			if orchestration.TextOf(m["schema"]) == a.controls.Type && orchestration.SameUUID(orchestration.TextOf(m["id"]), own.Str("id")) {
				overrun = true
			}
		}
	}
	if overrun || capped {
		return a.setPaused(ctx, agent, true, "budget", "budget")
	}
	return nil
}

// ---- runs ----

func (a *Agents) sandboxOf(ctx context.Context, definition orchestration.Row) (string, providers.SandboxProvider, error) {
	if a.opts.Providers == nil {
		return "", nil, errors.New("agents: no providers, and a run needs a sandbox provider (D159)")
	}
	slot := definition.Str("sandboxSlot")
	if slot == "" {
		slot = a.opts.SandboxSlot
	}
	if slot == "" {
		if configured := a.opts.Providers.Configured(providers.Sandbox); len(configured) > 0 {
			slot = configured[0].Slot
		}
	}
	if slot == "" {
		return "", nil, errors.New("agents: no sandbox slot is configured")
	}
	provider, err := a.opts.Providers.Sandbox(ctx, slot)
	return slot, provider, err
}

func (a *Agents) modelOf(ctx context.Context, definition orchestration.Row) (string, providers.ModelProvider, error) {
	if a.opts.Providers == nil {
		return "", nil, errors.New("agents: no providers, and a run needs a model provider (D159)")
	}
	slot := definition.Str("modelSlot")
	if slot == "" {
		slot = a.opts.ModelSlot
	}
	if slot == "" {
		if configured := a.opts.Providers.Configured(providers.Model); len(configured) > 0 {
			slot = configured[0].Slot
		}
	}
	if slot == "" {
		return "", nil, errors.New("agents: no model slot is configured")
	}
	provider, err := a.opts.Providers.Model(ctx, slot)
	return slot, provider, err
}

// leaseCall calls an operation of the run's Lease as the dispatcher,
// presenting the row's current token; nothing for a runs table with no
// Lease.
func (a *Agents) leaseCall(ctx context.Context, runID, operation string) {
	use, ok := a.host.Orchestration().UseOf(a.runs.Type, "lease")
	if !ok {
		return
	}
	call := a.self(ctx)
	if operation != "acquire" {
		row, err := a.run(ctx, a.host, runID)
		if err != nil || row == nil || row.IsNull("leaseToken") {
			return
		}
		call = a.host.Presenting(call, map[string]any{use: map[string]any{"token": row.Int("leaseToken")}})
	}
	if _, _, err := a.operation(call, a.runs.Type, "lease", runID, operation, map[string]any{}); err != nil {
		// A lease that lapsed is Lease's to expire; one never taken has
		// nothing to give back.
		a.log.Warn("a run's lease did not hold", zap.String("operation", operation), zap.String("run", runID), zap.Error(err))
	}
}

// moveRun moves a run's status, through its Workflow's transition when
// the runs table composes one (Lease's and Retries' states are its), and
// writes extra beside it; a move its transitions lack is logged and left.
// The fields go in before the status moves, so a reader that sees a run
// ended also sees its outcome, cost and transcript.
func (a *Agents) moveRun(ctx context.Context, runID, status string, extra map[string]any) error {
	if _, ok := a.host.Orchestration().UseOf(a.runs.Type, "status"); !ok {
		values := map[string]any{"status": status}
		for k, v := range extra {
			values[k] = v
		}
		return a.write(a.self(ctx), a.host, a.runs, runID, values)
	}
	if len(extra) > 0 {
		if err := a.write(a.self(ctx), a.host, a.runs, runID, extra); err != nil {
			return err
		}
	}
	current, err := a.run(ctx, a.host, runID)
	if err != nil {
		return err
	}
	if current != nil && current.Str("status") != status {
		if _, _, err := a.operation(a.self(ctx), a.runs.Type, "status", runID, "transition", map[string]any{"to": status}); err != nil {
			a.log.Warn("a run did not move", zap.String("run", runID), zap.String("from", current.Str("status")), zap.String("to", status), zap.Error(err))
		}
	}
	return nil
}

// finish ends a run: its token revoked, its instance's cost metered, its
// transcript written and its outcome recorded.
func (a *Agents) finish(ctx context.Context, runID, outcome, detail string) error {
	run, err := a.run(ctx, a.host, runID)
	if err != nil || run == nil || ended(run.Str("status")) {
		return err
	}
	grants, err := a.host.Grants(ctx, runID, true, a.now())
	if err != nil {
		return err
	}
	for _, grant := range grants {
		if err := a.host.RevokeGrant(ctx, grant.ID, a.now()); err != nil {
			return err
		}
	}
	cost := run.Int("cost")
	var spent int64
	if run.Str("instance") != "" && run.Str("sandboxSlot") != "" && a.opts.Providers != nil {
		if provider, err := a.opts.Providers.Sandbox(ctx, run.Str("sandboxSlot")); err != nil {
			a.log.Warn("a sandbox cost was not read", zap.String("run", runID), zap.Error(err))
		} else if c, err := provider.Cost(ctx, run.Str("instance")); err != nil {
			a.log.Warn("a sandbox cost was not read", zap.String("run", runID), zap.Error(err))
		} else {
			spent = a.opts.SandboxCostOf(c)
			cost += spent
		}
	}
	// The transcript a run leaves is redacted whole, its completions'
	// providerState with the rest, since no later turn passes it back.
	entries := a.redactEntries(entriesOf(run["transcript"]))
	if entries == nil {
		entries = []TranscriptEntry{}
	}
	transcriptObject := nullable(run.Str("transcriptObject"))
	var transcript any = entries
	if a.opts.Transcripts != nil {
		name := "agent-runs/" + runID + ".json"
		body, err := json.Marshal(entries)
		if err != nil {
			return err
		}
		if err := a.opts.Transcripts.Put(ctx, name, body); err != nil {
			return err
		}
		transcriptObject, transcript = name, nil
	}
	status := "cancelled"
	switch outcome {
	case "done":
		status = "done"
	case "failed":
		status = "failed"
	}
	// A run that asked and then ended runs again on its way to done or
	// failed.
	if run.Str("status") == "waiting" && status != "cancelled" {
		if err := a.moveRun(ctx, runID, "running", nil); err != nil {
			return err
		}
	}
	if err := a.moveRun(ctx, runID, status, map[string]any{"outcome": outcome, "detail": nullable(detail), "cost": cost, "transcript": transcript, "transcriptObject": transcriptObject, "endedAt": a.now()}); err != nil {
		return err
	}
	// The lease is given back once the run has ended, so its end moves no
	// state of a run in flight.
	a.leaseCall(ctx, runID, "release")
	// The instance's cost counts once the run has ended, so a cap it
	// reaches pauses the agent without ending this run again.
	if err := a.meterUsage(ctx, run.Str("agent"), spent); err != nil {
		return err
	}
	if outcome != "done" && run.Str("trigger") == TriggerAssigned && run.Str("triggerTable") != "" && run.Str("triggerKey") != "" && a.opts.OnReturn != nil {
		return a.inTransaction(ctx, func(h orchestration.Host) error {
			return a.opts.OnReturn(ctx, h, run, rowrefs.RowRef{Table: run.Str("triggerTable"), Key: run.Str("triggerKey")})
		})
	}
	return nil
}

func nullable(s string) any {
	if s == "" {
		return nil
	}
	return s
}

// Cancel ends a run in flight: cancels its instance and revokes its token;
// outcome is cancelled unless set.
func (a *Agents) Cancel(ctx context.Context, runID, outcome, detail string) error {
	if outcome == "" {
		outcome = "cancelled"
	}
	run, err := a.run(ctx, a.host, runID)
	if err != nil || run == nil || ended(run.Str("status")) {
		return err
	}
	if run.Str("instance") != "" && run.Str("sandboxSlot") != "" && a.opts.Providers != nil {
		provider, err := a.opts.Providers.Sandbox(ctx, run.Str("sandboxSlot"))
		if err == nil {
			err = provider.Cancel(ctx, run.Str("instance"))
		}
		if err != nil {
			a.log.Warn("an instance was not cancelled", zap.String("run", runID), zap.Error(err))
		}
	}
	return a.finish(ctx, runID, outcome, detail)
}

func (a *Agents) runFields(request Request, definition orchestration.Row, targets []rowrefs.RowRef) map[string]any {
	trigger := request.Trigger
	if trigger == "" {
		trigger = TriggerPrincipal
	}
	or := func(values ...string) any {
		for _, v := range values {
			if v != "" {
				return v
			}
		}
		return nil
	}
	party := rowrefs.RowRef{}
	if request.Party != nil {
		party = *request.Party
	}
	input := map[string]any{}
	for k, v := range request.Input {
		input[k] = v
	}
	if request.Message != nil {
		input["message"] = request.Message
	}
	fields := map[string]any{
		"agent":             definition.Str("id"),
		"agentName":         definition.Str("name"),
		"definitionVersion": definition.Int("_version"),
		"trigger":           trigger,
		"triggerTable":      or(request.SubjectTable, party.Table),
		"triggerKey":        or(request.SubjectKey, party.Key),
		"event":             request.Event,
		"input":             input,
		"targets":           targets,
	}
	if request.RequestedBy != nil {
		fields["requestedBy"] = nullable(request.RequestedBy.Subject)
		fields["requestedByKind"] = nullable(request.RequestedBy.Kind)
	}
	switch {
	case request.ActingFor != nil:
		fields["actingForKind"] = nullable(request.ActingFor.Kind)
		fields["actingFor"] = nullable(request.ActingFor.Subject)
		fields["actingForTable"] = or(request.ActingFor.Table, party.Table)
	case request.Party != nil:
		fields["actingForKind"] = "party"
		fields["actingFor"] = nullable(party.Key)
		fields["actingForTable"] = nullable(party.Table)
	}
	if targets == nil {
		fields["targets"] = []rowrefs.RowRef{}
	}
	return fields
}

// inboundMessage is an inbound message by its key: its text, its row and
// the trust its row records; nil when the dispatcher cannot read it.
type inboundMessage struct {
	text  string
	row   *rowrefs.RowRef
	trust string
}

func (a *Agents) readInbound(ctx context.Context, message string) (*inboundMessage, error) {
	in := a.inboundOf()
	if in == nil {
		return nil, nil
	}
	row, err := in.Message(ctx, message)
	if err != nil || row == nil {
		return nil, err
	}
	out := &inboundMessage{text: row.Str("body"), trust: row.Str("trust")}
	if a.messagesType != "" {
		out.row = &rowrefs.RowRef{Table: a.messagesType, Key: row.Str("id")}
	}
	return out, nil
}

// scopeOf is the run's scope: the row it is about, the rows the request
// adds, and an inbound message's grant's target; or why it has none.
func (a *Agents) scopeOf(ctx context.Context, request Request) ([]rowrefs.RowRef, string, error) {
	var out []rowrefs.RowRef
	add := func(ref rowrefs.RowRef) {
		for _, r := range out {
			if r.Table == ref.Table && r.Key == ref.Key {
				return
			}
		}
		out = append(out, ref)
	}
	if request.Trigger == TriggerInbound {
		if request.Grant == "" || request.Party == nil {
			return nil, "an inbound message names its party and the party's grant", nil
		}
		grants, err := a.host.Grants(ctx, request.Party.Key, true, a.now())
		if err != nil {
			return nil, "", err
		}
		var grant *access.Grant
		for i := range grants {
			if orchestration.SameUUID(grants[i].ID, request.Grant) {
				grant = &grants[i]
			}
		}
		if grant == nil {
			return nil, "the party holds no such active grant", nil
		}
		add(rowrefs.RowRef{Table: grant.TargetType, Key: grant.TargetID})
		// The party's own row, which a message to it reads, and the
		// message's.
		add(*request.Party)
		if key, ok := request.Message.(string); ok {
			read, err := a.readInbound(ctx, key)
			if err != nil {
				return nil, "", err
			}
			if read != nil && read.row != nil {
				add(*read.row)
			}
		}
		return out, "", nil
	}
	if request.SubjectTable != "" && request.SubjectKey != "" {
		add(rowrefs.RowRef{Table: request.SubjectTable, Key: request.SubjectKey})
	}
	for _, t := range request.Targets {
		add(t)
	}
	return out, "", nil
}

func (a *Agents) refuseRun(ctx context.Context, request Request, definition orchestration.Row, outcome, detail string) error {
	if definition == nil {
		a.log.Warn("a run request names no agent", zap.String("agent", request.Agent), zap.String("trigger", request.Trigger))
		return nil
	}
	refused, err := a.create(a.self(ctx), a.host, a.runs, a.runFields(request, definition, nil))
	if err != nil {
		return err
	}
	if err := a.moveRun(ctx, refused.Str("id"), "cancelled", map[string]any{"outcome": outcome, "detail": detail, "endedAt": a.now()}); err != nil {
		return err
	}
	if request.Trigger == TriggerAssigned && request.SubjectTable != "" && request.SubjectKey != "" && a.opts.OnReturn != nil {
		row := rowrefs.RowRef{Table: request.SubjectTable, Key: request.SubjectKey}
		return a.inTransaction(ctx, func(h orchestration.Host) error {
			return a.opts.OnReturn(ctx, h, orchestration.Row{"agent": definition.Str("id"), "trigger": TriggerAssigned}, row)
		})
	}
	return nil
}

func (a *Agents) enqueue(ctx context.Context, h orchestration.Host, request Request, delay time.Duration) error {
	data, err := json.Marshal(request)
	if err != nil {
		return err
	}
	return h.EnqueueOn(ctx, orchestration.Requests, data, delay)
}

func (a *Agents) start(ctx context.Context, request Request) error {
	var definition, existing orchestration.Row
	var err error
	switch {
	case request.Run != "":
		if existing, err = a.run(ctx, a.host, request.Run); err != nil {
			return err
		}
		if existing == nil || (existing.Str("status") != "queued" && existing.Str("status") != "waiting") {
			return nil
		}
		if definition, err = a.definitionByID(ctx, existing.Str("agent")); err != nil {
			return err
		}
	case request.Agent != "":
		if definition, err = a.findDefinition(ctx, request.Agent); err != nil {
			return err
		}
	case request.Trigger == TriggerInbound:
		if definition, err = orchestration.FindOne(ctx, a.host, a.definitions.Where().Eq("inbound", true), a.definitions.C("name")); err != nil {
			return err
		}
	}
	if definition == nil {
		return a.refuseRun(ctx, request, nil, "refused", "no such agent")
	}
	agentID := definition.Str("id")
	stand, err := a.standingOf(ctx, agentID)
	if err != nil {
		return err
	}
	fail := func(outcome, detail string) error {
		if existing != nil {
			return a.finish(ctx, existing.Str("id"), outcome, detail)
		}
		return a.refuseRun(ctx, request, definition, outcome, detail)
	}
	if stand.paused {
		return fail("paused", fmt.Sprintf("the agent is paused (%s)", stand.reason))
	}
	if stand.agentCapped {
		if err := a.setPaused(ctx, agentID, true, "budget", "budget"); err != nil {
			return err
		}
		return fail("budget", "the agent reached its budget's cap")
	}
	trigger := request.Trigger
	if existing != nil && existing.Str("trigger") != "" {
		trigger = existing.Str("trigger")
	}
	if trigger == "" {
		trigger = TriggerPrincipal
	}
	if stand.totalCapped && trigger != TriggerPrincipal {
		return fail("budget", "every agent's budget reached its cap, and scheduled runs stop")
	}

	// The run row.
	run := existing
	if run == nil {
		targets, why, err := a.scopeOf(ctx, request)
		if err != nil {
			return err
		}
		if why != "" {
			return a.refuseRun(ctx, request, definition, "refused", why)
		}
		fields := a.runFields(request, definition, targets)
		if request.Trigger == TriggerInbound {
			input := fields["input"].(map[string]any)
			// The grant the run is under, which narrows its permissions and
			// its paths (D183).
			input["grant"] = request.Grant
			var read *inboundMessage
			if key, ok := request.Message.(string); ok {
				if read, err = a.readInbound(ctx, key); err != nil {
					return err
				}
			}
			recorded := ""
			if read != nil {
				input["inboundText"] = read.text
				recorded = read.trust
			}
			// The message's trust, as its row records it (D157): a sender
			// entered by hand, and a message the dispatcher cannot read or
			// that the request carries as text, is entered, whose run asks
			// before anything but a read (D183).
			fields["trust"] = TrustOf(recorded)
		}
		if run, err = a.create(a.self(ctx), a.host, a.runs, fields); err != nil {
			return err
		}
		// The run on the timeline of the row that started it (D154): a
		// reference from the run, which the row lists among its backlinks.
		if refs := a.host.References(); refs != nil && run.Str("triggerTable") != "" && run.Str("triggerKey") != "" {
			if _, err := refs.Add(a.host.System(ctx), rowrefs.AddInput{From: rowrefs.RowRef{Table: a.runs.Type, Key: run.Str("id")}, To: rowrefs.RowRef{Table: run.Str("triggerTable"), Key: run.Str("triggerKey")}, Role: "agentRun"}); err != nil {
				// A runs table without @references keeps no timeline entry.
				a.log.Debug("a run's timeline entry was not added", zap.Error(err))
			}
		}
	}
	runID := run.Str("id")

	// Every agent over its cap: a principal's run asks before it starts.
	if stand.totalCapped && trigger == TriggerPrincipal {
		pending, err := orchestration.FindOne(ctx, a.host, a.approvals.Where().Eq("run", runID).Eq("operation", overBudget).In("state", "pending", "approved"), "")
		if err != nil {
			return err
		}
		if pending != nil && pending.Str("state") == "approved" {
			if _, err := orchestration.UpdateID(ctx, a.host, a.approvals, pending.Str("id"), map[string]any{"state": "used", "usedAt": a.now()}); err != nil {
				return err
			}
		} else {
			if pending == nil {
				if _, err := a.askApproval(ctx, run, overBudget, MoneyMovement, map[string]any{"agent": definition.Str("name"), "run": runID}, PermissionBudgets, fmt.Sprintf("Run %s past every agent's budget", definition.Str("name"))); err != nil {
					return err
				}
			}
			if run.Str("status") == "queued" {
				if err := a.moveRun(ctx, runID, "running", nil); err != nil {
					return err
				}
			}
			return a.moveRun(ctx, runID, "waiting", nil)
		}
	}

	// The lease, the token and the instance.
	if run.Str("status") == "waiting" || run.Str("status") == "queued" {
		if err := a.moveRun(ctx, runID, "running", nil); err != nil {
			return err
		}
	}
	a.leaseCall(ctx, runID, "acquire")
	limits := definition.Object("limits")
	maxSeconds := int64(3600)
	if v, ok := limits["maxSeconds"]; ok && v != nil {
		maxSeconds = orchestration.IntOf(v)
	}
	endsAt := a.now().Add(time.Duration(maxSeconds) * time.Second)
	targets := targetsOf(run)
	if len(targets) == 0 {
		return a.finish(ctx, runID, "refused", "the run has no target rows, so its token would reach nothing")
	}
	token := ""
	var grants []string
	for _, target := range targets {
		grant, issued, err := a.host.IssueGrant(a.host.System(ctx), access.IssueGrantInput{Holder: runID, HolderKind: "agent", Role: definition.Str("role"), TargetType: target.Table, TargetID: target.Key, EndsAt: &endsAt})
		if err != nil {
			return err
		}
		grants = append(grants, grant.ID)
		if token == "" {
			token = issued
		}
	}
	// The task is the transcript's first entry before the instance can ask
	// for its first turn.
	if len(entriesOf(run["transcript"])) == 0 {
		if err := a.appendEntries(ctx, runID, TranscriptEntry{Kind: "task", Role: providers.RoleUser, Content: taskOf(run, definition), Untrusted: run.Str("trigger") == TriggerInbound}); err != nil {
			return err
		}
	}
	attempts := run.Int("attempts") + 1
	instance, slot, startErr := a.startInstance(ctx, run, definition, runID, token, maxSeconds, limits)
	if startErr != nil {
		detail := startErr.Error()
		a.log.Warn("a sandbox instance did not start", zap.String("run", runID), zap.String("detail", a.redactText(detail, token)))
		for _, id := range grants {
			if err := a.host.RevokeGrant(ctx, id, a.now()); err != nil {
				return err
			}
		}
		if err := a.write(a.self(ctx), a.host, a.runs, runID, map[string]any{"grants": grants, "attempts": attempts}); err != nil {
			return err
		}
		return a.retry(ctx, runID, request, a.redactText(detail, token))
	}
	if err := a.write(a.self(ctx), a.host, a.runs, runID, map[string]any{"grants": grants, "instance": instance.ID, "sandboxSlot": slot, "startedAt": instance.StartedAt, "attempts": attempts}); err != nil {
		return err
	}
	return a.enqueue(ctx, a.host, Request{Step: "watch", Run: runID}, time.Duration(a.opts.WatchSeconds)*time.Second)
}

func (a *Agents) startInstance(ctx context.Context, run, definition orchestration.Row, runID, token string, maxSeconds int64, limits map[string]any) (providers.SandboxInstance, string, error) {
	slot, provider, err := a.sandboxOf(ctx, definition)
	if err != nil {
		return providers.SandboxInstance{}, "", err
	}
	extra := SandboxExtra{}
	if a.opts.SandboxSpec != nil {
		extra = a.opts.SandboxSpec(run, definition)
	}
	var credentials []any
	if list, ok := definition["credentials"].([]any); ok {
		for _, c := range list {
			if s, ok := c.(string); ok {
				credentials = append(credentials, map[string]any{"ref": s})
			} else {
				credentials = append(credentials, c)
			}
		}
	}
	if credentials == nil {
		credentials = []any{}
	}
	credentialsJSON, err := json.Marshal(credentials)
	if err != nil {
		return providers.SandboxInstance{}, "", err
	}
	env := map[string]string{}
	for k, v := range extra.Env {
		env[k] = v
	}
	env["AGENT_RUN_ID"] = runID
	env["AGENT_ENDPOINT"] = a.endpoint
	env["AGENT_NAME"] = definition.Str("name")
	env["AGENT_DEFINITION_VERSION"] = fmt.Sprint(definition.Int("_version"))
	env["AGENT_CREDENTIALS"] = string(credentialsJSON)
	secrets := map[string]providers.SecretValue{}
	for k, v := range extra.Secrets {
		secrets[k] = v
	}
	secrets["AGENT_RUN_TOKEN"] = providers.NewSecret(token)
	spec := providers.SandboxSpec{RunID: runID, Image: definition.Str("image"), Command: definition.Strings("command"), Env: env, Secrets: secrets, Limits: &providers.SandboxLimits{WallSeconds: int(maxSeconds)}}
	if v, ok := limits["wallSeconds"]; ok && v != nil {
		spec.Limits.WallSeconds = int(orchestration.IntOf(v))
	}
	if v, ok := limits["cpuSeconds"]; ok && v != nil {
		spec.Limits.CPUSeconds = int(orchestration.IntOf(v))
	}
	if v, ok := limits["memoryMb"]; ok && v != nil {
		spec.Limits.MemoryMB = int(orchestration.IntOf(v))
	}
	instance, err := provider.Start(ctx, spec)
	return instance, slot, err
}

// retry records a failed start with Retries, whose backoff says when the
// next starts; exhausted, the run fails.
func (a *Agents) retry(ctx context.Context, runID string, request Request, detail string) error {
	if len(detail) > 500 {
		detail = detail[:500]
	}
	attempt, ok, err := a.operation(a.self(ctx), a.runs.Type, "retries", runID, "recordAttempt", map[string]any{"failure": "sandbox", "detail": map[string]any{"message": detail}})
	if !ok {
		return a.finish(ctx, runID, "failed", detail)
	}
	if err != nil {
		return a.finish(ctx, runID, "failed", detail+"; "+err.Error())
	}
	if exhausted, _ := attempt["exhausted"].(bool); exhausted {
		return a.finish(ctx, runID, "failed", detail)
	}
	if err := a.moveRun(ctx, runID, "queued", nil); err != nil {
		return err
	}
	a.leaseCall(ctx, runID, "release")
	var delay time.Duration
	if at, ok := attempt["retryAt"]; ok && at != nil {
		if wait := time.UnixMilli(orchestration.IntOf(at)).Sub(a.now()); wait > 0 {
			delay = time.Duration(math.Ceil(wait.Seconds())) * time.Second
		}
	}
	next := request
	next.Step = "start"
	next.Run = runID
	return a.enqueue(ctx, a.host, next, delay)
}

func taskOf(run, definition orchestration.Row) string {
	input := run.Object("input")
	if task, ok := input["task"].(string); ok {
		return task
	}
	if text, ok := input["inboundText"].(string); ok {
		return text
	}
	switch message := input["message"].(type) {
	case string:
		return "An inbound message, " + message
	case map[string]any:
		if body, ok := message["body"].(string); ok {
			return body
		}
	}
	if task := definition.Str("task"); task != "" {
		return task
	}
	if purpose := definition.Str("purpose"); purpose != "" {
		return purpose
	}
	table := run.Str("triggerTable")
	if table == "" {
		table = "the task"
	}
	return strings.TrimSpace("Work on " + table + " " + run.Str("triggerKey"))
}

func (a *Agents) watch(ctx context.Context, runID string) error {
	run, err := a.run(ctx, a.host, runID)
	if err != nil || run == nil {
		return err
	}
	if status := run.Str("status"); (status != "running" && status != "waiting") || run.Str("instance") == "" || run.Str("sandboxSlot") == "" || a.opts.Providers == nil {
		return nil
	}
	provider, err := a.opts.Providers.Sandbox(ctx, run.Str("sandboxSlot"))
	if err != nil {
		return err
	}
	status, err := provider.Status(ctx, run.Str("instance"))
	if err != nil {
		return err
	}
	switch status.State {
	case providers.SandboxRunning, providers.SandboxStarting:
		a.leaseCall(ctx, runID, "heartbeat")
		return a.enqueue(ctx, a.host, Request{Step: "watch", Run: runID}, time.Duration(a.opts.WatchSeconds)*time.Second)
	case providers.SandboxSucceeded:
		outcome := run.Str("outcome")
		if outcome == "" {
			outcome = "done"
		}
		return a.finish(ctx, runID, outcome, "")
	case providers.SandboxCancelled:
		return a.finish(ctx, runID, "cancelled", "the instance was cancelled")
	}
	detail := status.Detail
	if detail == "" {
		detail = "the instance exited"
		if status.ExitCode != nil {
			detail = fmt.Sprintf("the instance exited %d", *status.ExitCode)
		}
	}
	return a.finish(ctx, runID, "failed", detail)
}

func (a *Agents) followUp(ctx context.Context, request Request) error {
	if request.Ask == nil || request.Run == "" {
		return nil
	}
	run, err := a.run(ctx, a.host, request.Run)
	if err != nil || run == nil {
		return err
	}
	row := rowrefs.RowRef{Table: request.Ask.Table, Key: request.Ask.Key}
	var answered bool
	if err := a.inTransaction(ctx, func(h orchestration.Host) error {
		var err error
		answered, err = a.thread().Answered(a.self(ctx), h, row, request.Ask.Message, run.Str("id"))
		return err
	}); err != nil {
		return err
	}
	if answered {
		return nil
	}
	if a.opts.FollowUp != nil {
		if err := a.inTransaction(ctx, func(h orchestration.Host) error {
			return a.opts.FollowUp(a.host.System(ctx), h, run, row, request.Ask.Question)
		}); err != nil {
			return err
		}
	}
	return a.appendEntries(ctx, run.Str("id"), TranscriptEntry{Kind: "note", Content: "nobody answered by the deadline: " + request.Ask.Question})
}

// Handle handles one message of the request queue: the dispatcher's
// worker handler. A run request of the @runRequests queue (D157) is one
// with its trigger inbound.
func (a *Agents) Handle(ctx context.Context, message Request) error {
	switch message.Step {
	case "watch":
		return a.watch(ctx, message.Run)
	case "followUp":
		return a.followUp(ctx, message)
	}
	return a.start(ctx, message)
}

// HandleRunRequest hands a message of D157's @runRequests queue to Handle
// as it is.
func (a *Agents) HandleRunRequest(ctx context.Context, request inbound.RunRequest) error {
	party := rowrefs.RowRef{Table: request.Party.Table, Key: request.Party.Key}
	return a.Handle(ctx, Request{Trigger: request.Trigger, Party: &party, Message: request.Message, Grant: request.Grant})
}

// WorkerQueue is the request queue as a worker claims it, for worker.Run
// or worker.RunAll: each message handed to Handle, and its prune the
// queue's and the transcripts' past their retention.
func (a *Agents) WorkerQueue() worker.Queue {
	queue := a.host.QueueOf(orchestration.Requests)
	return worker.Queue{
		Name:  queue.Name(),
		Lease: queue.Lease(),
		Claim: func(ctx context.Context, count int, lease time.Duration) ([]worker.Message, error) {
			claims, err := queue.Claim(ctx, count, lease)
			out := make([]worker.Message, 0, len(claims))
			for _, claim := range claims {
				data := claim.Message
				out = append(out, worker.Message{ID: claim.ID, Token: claim.Token, Attempt: claim.Attempt, Handle: func(ctx context.Context) error {
					var request Request
					if err := json.Unmarshal(data, &request); err != nil {
						return fmt.Errorf("agents: a request %s: %w", data, err)
					}
					return a.Handle(ctx, request)
				}})
			}
			return out, err
		},
		Extend:   queue.Extend,
		Complete: queue.Complete,
		Fail:     queue.Fail,
		Release:  queue.Release,
		Prune: func(ctx context.Context, limit int) (int, error) {
			done, err := queue.Prune(ctx, limit)
			if err != nil {
				return done, err
			}
			pruned, err := a.Prune(ctx, limit)
			return done + pruned, err
		},
		Slot:    queue.Slot,
		NextDue: queue.NextDue,
	}
}

// RequestRun puts a run request on the dispatcher's queue in h, the
// caller's transaction (nil for one of its own); a principal of kind agent
// may not.
func (a *Agents) RequestRun(ctx context.Context, h orchestration.Host, input Request, delay time.Duration) error {
	if input.RequestedBy != nil && input.RequestedBy.Kind == string(access.KindAgent) {
		return refuse(403, "agents_cannot_request", "an agent run may not start a run: only the dispatcher starts one (D158)")
	}
	if input.Trigger == TriggerInbound && (input.Party == nil || input.Grant == "") {
		return refuse(400, "invalid_request", "an inbound message's request names its party and its grant")
	}
	if h == nil {
		h = a.host
	}
	input.Step = "start"
	return a.enqueue(ctx, h, input, delay)
}

// RouteRequest is the notifier's { agent } recipient (notify.Agents): a
// run request about the event's record in the routing transaction.
func (a *Agents) RouteRequest(ctx context.Context, h notifications.Host, request notify.AgentRequest) error {
	host, err := orchestration.Of(h)
	if err != nil {
		return err
	}
	input := Request{
		Trigger:      TriggerRoute,
		Agent:        request.Agent,
		SubjectTable: request.SubjectTable,
		SubjectKey:   request.SubjectKey,
		Event:        map[string]any{"id": request.EventID, "kind": request.EventKind, "payload": request.Payload},
		RequestedBy:  &PrincipalRef{Kind: "route", Subject: request.Route},
	}
	if request.Task != "" {
		input.Input = map[string]any{"task": request.Task}
	}
	return a.RequestRun(ctx, host, input, 0)
}

var _ notify.Agents = (*Agents)(nil)

// ---- administration ----

func need(as orchestration.Principal, permission string) error {
	if as.Kind == access.KindAgent {
		return refuse(403, "forbidden", "an agent run may not administer agents")
	}
	for _, held := range as.Permissions {
		if session.Covers(held, permission) {
			return nil
		}
	}
	return refuse(403, "forbidden", "this needs %s", permission)
}

func (a *Agents) setPaused(ctx context.Context, agent string, paused bool, reason, by string) error {
	control, err := a.controlOf(ctx, agent)
	if err != nil {
		return err
	}
	values := map[string]any{"paused": paused, "pausedReason": nil, "pausedBy": nil, "pausedAt": nil}
	if paused {
		values["pausedReason"], values["pausedBy"], values["pausedAt"] = nullable(reason), nullable(by), a.now()
	}
	if err := a.write(a.self(ctx), a.host, a.controls, control.Str("id"), values); err != nil {
		return err
	}
	if !paused {
		return nil
	}
	w := a.runs.Where().In("status", "queued", "running", "waiting")
	if agent != "" {
		w.Eq("agent", agent)
	}
	inFlight, err := orchestration.Find(ctx, a.host, w, "", 10000)
	if err != nil {
		return err
	}
	outcome, detail := "paused", "the agent is paused"
	if agent == "" {
		detail = "the agents are paused"
	}
	if reason == "budget" {
		outcome, detail = "budget", "the agent reached its budget's cap"
	}
	for _, run := range inFlight {
		if err := a.Cancel(ctx, run.Str("id"), outcome, detail); err != nil {
			return err
		}
	}
	return nil
}

// Pause pauses an agent, by name or key, or every agent for "", cancelling
// its runs in flight; reason is stopped unless set.
func (a *Agents) Pause(ctx context.Context, as orchestration.Principal, agent, reason string) error {
	if err := need(as, PermissionPause); err != nil {
		return err
	}
	if reason == "" {
		reason = "stopped"
	}
	id := ""
	if agent != "" {
		var err error
		if id, err = a.agentID(ctx, agent); err != nil {
			return err
		}
	}
	return a.setPaused(ctx, id, true, reason, as.Subject)
}

// Resume resumes an agent, or every agent for "".
func (a *Agents) Resume(ctx context.Context, as orchestration.Principal, agent string) error {
	if err := need(as, PermissionPause); err != nil {
		return err
	}
	id := ""
	if agent != "" {
		var err error
		if id, err = a.agentID(ctx, agent); err != nil {
			return err
		}
	}
	return a.setPaused(ctx, id, false, "", "")
}

// Policy is an autonomy policy row SetPolicy writes: for an agent (by
// name or key, "" for every agent), an operation or a class, one of them,
// its mode, and the permission an approval of it needs.
type Policy struct {
	Agent          string
	Operation      string
	OperationClass string
	Mode           string
	Permission     string
}

// SetPolicy writes an autonomy policy row; it refuses one that lets an
// agent move money without asking.
func (a *Agents) SetPolicy(ctx context.Context, as orchestration.Principal, policy Policy) (orchestration.Row, error) {
	if err := need(as, PermissionAutonomy); err != nil {
		return nil, err
	}
	if policy.Mode != Auto && policy.Mode != Tell && policy.Mode != Ask {
		return nil, refuse(400, "invalid_mode", "mode %s is none of auto, tell and ask", policy.Mode)
	}
	if (policy.Operation == "") == (policy.OperationClass == "") {
		return nil, refuse(400, "invalid_policy", "a policy names an operation or a class, one of them")
	}
	class := policy.OperationClass
	if class == "" {
		class = a.opts.Tools[policy.Operation].Class
	}
	if class == MoneyMovement && policy.Mode != Ask {
		return nil, refuse(400, "money_asks", "an operation that moves money is ask and nothing else: an agent may only ask to move money (D158)")
	}
	values := map[string]any{"mode": policy.Mode, "operation": nullable(policy.Operation), "operationClass": nullable(policy.OperationClass), "permission": nullable(policy.Permission)}
	if policy.Agent != "" {
		id, err := a.agentID(ctx, policy.Agent)
		if err != nil {
			return nil, err
		}
		values["agent"] = id
	}
	return orchestration.Insert(ctx, a.host, a.policies, values)
}

// Budget is a budget SetBudget writes: its limit (nil for none) and the
// percent it warns at (0 leaves it).
type Budget struct {
	Limit  *int64
	WarnAt int64
}

// SetBudget sets an agent's budget, or every agent's for "".
func (a *Agents) SetBudget(ctx context.Context, as orchestration.Principal, agent string, budget Budget) error {
	if err := need(as, PermissionBudgets); err != nil {
		return err
	}
	id := ""
	if agent != "" {
		var err error
		if id, err = a.agentID(ctx, agent); err != nil {
			return err
		}
	}
	control, err := a.controlOf(ctx, id)
	if err != nil {
		return err
	}
	// A Budget's limit changes only for a caller holding its
	// limitPermission: the administrator's own.
	admin := a.host.Behaving(ctx, as.Subject, as.Permissions)
	var limit any
	if budget.Limit != nil {
		limit = *budget.Limit
	}
	values := map[string]any{"budgetLimit": limit}
	if budget.WarnAt > 0 {
		values["warnAt"] = budget.WarnAt
	}
	if err := a.write(admin, a.host, a.controls, control.Str("id"), values); err != nil {
		return err
	}
	// Every agent's limit is the limit of the budget their meters spend
	// within.
	if id == "" && a.total != nil && control.Str(a.total.Column) != "" {
		return a.write(admin, a.host, orchestration.Table{Part: notifications.Part{Type: a.total.Target}}, control.Str(a.total.Column), map[string]any{"budgetLimit": limit})
	}
	return nil
}

// Define writes or edits an agent's definition, by its name; changing its
// credential references needs agents.credentials too.
func (a *Agents) Define(ctx context.Context, as orchestration.Principal, definition map[string]any) (orchestration.Row, error) {
	if err := need(as, PermissionDefine); err != nil {
		return nil, err
	}
	var existing orchestration.Row
	if name, ok := definition["name"].(string); ok {
		var err error
		if existing, err = orchestration.FindOne(ctx, a.host, a.definitions.Where().Eq("name", name), ""); err != nil {
			return nil, err
		}
	}
	if given, ok := definition["credentials"]; ok {
		var was any
		if existing != nil {
			was = existing["credentials"]
		}
		if CanonicalJSON(given) != CanonicalJSON(was) {
			if err := need(as, PermissionCredentials); err != nil {
				return nil, err
			}
		}
		list, _ := normalise(given).([]any)
		for _, ref := range list {
			text, _ := ref.(string)
			if m, ok := ref.(map[string]any); ok {
				text, _ = m["ref"].(string)
			}
			if text == "" {
				return nil, refuse(400, "invalid_credentials", "a credential is named by its reference alone")
			}
		}
	}
	var written orchestration.Row
	if existing != nil {
		if err := a.write(a.host.System(ctx), a.host, a.definitions, existing.Str("id"), definition); err != nil {
			return nil, err
		}
		var err error
		if written, err = a.definitionByID(ctx, existing.Str("id")); err != nil {
			return nil, err
		}
	} else {
		var err error
		if written, err = a.create(a.host.System(ctx), a.host, a.definitions, definition); err != nil {
			return nil, err
		}
	}
	if _, err := a.controlOf(ctx, written.Str("id")); err != nil {
		return nil, err
	}
	return written, nil
}

// Transcript is a run's redacted transcript.
func (a *Agents) Transcript(ctx context.Context, runID string) ([]TranscriptEntry, error) {
	run, err := a.run(ctx, a.host, runID)
	if err != nil || run == nil {
		return nil, err
	}
	if object := run.Str("transcriptObject"); object != "" && a.opts.Transcripts != nil && run.IsNull("transcriptPrunedAt") {
		body, err := a.opts.Transcripts.Get(ctx, object)
		if err != nil {
			return nil, err
		}
		var out []TranscriptEntry
		if err := json.Unmarshal(body, &out); err != nil {
			return nil, err
		}
		return out, nil
	}
	// A live run's completions keep their providerState unredacted for the
	// turns after them; a reader gets it redacted, as the rest.
	return a.redactEntries(entriesOf(run["transcript"])), nil
}

// Prune deletes up to limit transcripts past their retention and stamps
// their runs, and answers how many.
func (a *Agents) Prune(ctx context.Context, limit int) (int, error) {
	if a.opts.Transcripts == nil {
		return 0, nil
	}
	before := a.now().Add(-time.Duration(a.opts.TranscriptRetentionDays) * 24 * time.Hour)
	old, err := orchestration.Find(ctx, a.host, a.runs.Where().Cmp("endedAt", "<", before).NotNull("transcriptObject").Null("transcriptPrunedAt"), "", limit)
	if err != nil {
		return 0, err
	}
	for _, run := range old {
		if err := a.opts.Transcripts.Delete(ctx, run.Str("transcriptObject")); err != nil {
			return 0, err
		}
		if err := a.write(a.self(ctx), a.host, a.runs, run.Str("id"), map[string]any{"transcriptPrunedAt": a.now()}); err != nil {
			return 0, err
		}
	}
	return len(old), nil
}
