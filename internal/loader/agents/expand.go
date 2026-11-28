// Package agents expands a DB schema's agent orchestration declarations
// (D158) into ordinary fields: each class marked @agents("<part>") gains
// the fields of its part that it does not declare itself, so a class may
// add fields of its own and compose behaviors over them: the controls
// Budget over a limit field, the runs Lease and Retries over a Workflow
// status the app declares. The sql, orm and types generators emit the
// result as any table's or queue's.
//
// It runs before the verification pass, so the pass reads the added fields
// as the class's own. Every field it adds carries ir.OriginAgents, so the
// schema writer can skip it and write the declaration instead.
package agents

import (
	"github.com/parable-work/superschematic/internal/loader/fielddef"
	ir "github.com/parable-work/superschematic/ir"
)

// Scalars the added fields use.
const (
	uuidScalar     = "Identity.UUID"
	dateTimeScalar = "Temporal.DateTime"
	int64Scalar    = "Generic.Int64"
	jsonScalar     = "Generic.JSON"
	sha256Scalar   = "Crypto.SHA256"
)

// Expand adds the fields of every @agents class's part that the class does
// not declare. It returns the scalar names the added fields use, so the
// caller can hydrate any the schema did not already reference.
func Expand(schema *ir.Schema) []string {
	used := false
	for _, td := range schema.Types {
		if td == nil || td.Agents == nil {
			continue
		}
		fields, indexes := PartFields(td.Agents.Part)
		if fields == nil {
			continue
		}
		fill(td, fields, indexes)
		used = true
	}
	if !used {
		return nil
	}
	scalars := []string{uuidScalar, dateTimeScalar, int64Scalar, jsonScalar, sha256Scalar}
	for _, name := range scalars {
		if schema.Scalars[name] == nil {
			schema.Scalars[name] = &ir.ScalarDef{Name: name}
		}
	}
	return scalars
}

// ExpandExternal fills the @agents classes of another service the schema
// reads (a DB dependency's, which a @source view names), as the
// dependency's own load fills them.
func ExpandExternal(types map[string]*ir.TypeDef) {
	for _, td := range types {
		if td == nil || td.Agents == nil {
			continue
		}
		if fields, indexes := PartFields(td.Agents.Part); fields != nil {
			fill(td, fields, indexes)
		}
	}
}

// fill adds each of fields td does not declare, in order after its own,
// and each index whose fields td then holds and that it does not already
// name. A part's key is added only to a class that declares none.
func fill(td *ir.TypeDef, fields []*ir.FieldDef, indexes []ir.IndexDef) {
	declared := map[string]bool{}
	hasKey := false
	for _, fd := range td.Fields {
		if fd == nil {
			continue
		}
		declared[fd.Name] = true
		hasKey = hasKey || fd.Key
	}
	var added []*ir.FieldDef
	for _, fd := range fields {
		if declared[fd.Name] || (fd.Key && hasKey) {
			continue
		}
		fd.Origin = ir.OriginAgents
		added = append(added, fd)
	}
	// The key leads, as a class's own key does.
	if len(added) > 0 && added[0].Key {
		td.Fields = append([]*ir.FieldDef{added[0]}, append(td.Fields, added[1:]...)...)
	} else {
		td.Fields = append(td.Fields, added...)
	}
	names := map[string]bool{}
	for _, index := range td.Indexes {
		names[index.Name] = true
	}
	for _, index := range indexes {
		if names[index.Name] {
			continue
		}
		index.Origin = ir.OriginAgents
		td.Indexes = append(td.Indexes, index)
	}
}

// PartFields are the fields and indexes of the part. The requests part is
// a @queue class; every other part a table, whose key its fields begin
// with.
func PartFields(part string) ([]*ir.FieldDef, []ir.IndexDef) {
	switch part {
	case ir.AgentDefinitions:
		return definitionFields()
	case ir.AgentRuns:
		return runFields()
	case ir.AgentRequests:
		return requestFields(), nil
	case ir.AgentPolicies:
		return policyFields()
	case ir.AgentApprovals:
		return approvalFields()
	case ir.AgentControls:
		return controlFields()
	}
	return nil, nil
}

// definitionFields are an agent's: who it is, what it may use and how it
// runs. The table is @versioned, so a run records the version it ran.
func definitionFields() ([]*ir.FieldDef, []ir.IndexDef) {
	off := "false"
	return []*ir.FieldDef{
		fielddef.Key(),
		fielddef.Required("name", "string", "The agent's name, which a route, an assignment and a request name it by."),
		fielddef.Optional("purpose", "string", "What the agent is for, for a reader of the definitions."),
		fielddef.Optional("instructions", "string", "What every run of the agent is told first: its system message."),
		fielddef.Optional("task", "string", "The first message of a run whose request gives none."),
		fielddef.Required("role", "string", "The UserRole the agent's runs act with: its permissions, and the relation paths its reaches name (D156). Each agent is its own principal."),
		fielddef.Optional("tools", jsonScalar, "The tools a run may call, by name: tools of the app the role allows, the sandbox's own and search; every tool the role allows when null."),
		fielddef.Optional("credentials", jsonScalar, "The credential references a run may use, each { ref, site, header? or selector? }: references alone, which the sandbox resolves (D159)."),
		fielddef.Optional("model", "string", "The model, by the provider's name for it; the provider's default when null."),
		fielddef.Optional("modelSlot", "string", "The provider slot of the model provider (D159); the dispatcher's default when null."),
		fielddef.Optional("sandboxSlot", "string", "The provider slot of the sandbox provider (D159); the dispatcher's default when null."),
		fielddef.Optional("image", "string", "What the sandbox runs: an image for a container platform."),
		fielddef.Optional("command", jsonScalar, "What the sandbox runs: a program and its arguments for a local process."),
		fielddef.Optional("limits", jsonScalar, "Per-run limits: { maxSeconds, maxTurns, wallSeconds, cpuSeconds, memoryMb }."),
		fielddef.WithDefault(fielddef.Required("inbound", "boolean", "Whether the agent answers inbound messages from parties (D157) that name no agent."), &off),
		fielddef.WithDefault(fielddef.Required("createdAt", dateTimeScalar, ""), fielddef.Now()),
	}, []ir.IndexDef{
		{Keys: []string{"name"}, Unique: true, Name: "name"},
	}
}

// runFields are a run's record: what started it, the definition version it
// ran, its scope, what it did and how it ended.
func runFields() ([]*ir.FieldDef, []ir.IndexDef) {
	queued := "queued"
	zero := "0"
	return []*ir.FieldDef{
		fielddef.Key(),
		fielddef.Required("agent", uuidScalar, "The agent definition's key."),
		fielddef.Required("agentName", "string", "The agent's name when the run started."),
		fielddef.Required("definitionVersion", int64Scalar, "The version of the definition the run ran (@versioned)."),
		fielddef.Required("trigger", "string", "What started it: route, assigned, schedule, principal or inbound."),
		fielddef.Optional("triggerTable", "string", "The table of the row that started it, by its type's name: the row whose timeline shows the run."),
		fielddef.Optional("triggerKey", "string", "That row's key as text."),
		fielddef.Optional("event", jsonScalar, "The event a route's run is of: { id, kind, payload }."),
		fielddef.Optional("input", jsonScalar, "What the request gave the run: its task, and the inbound message of a party."),
		fielddef.Optional("requestedBy", "string", "The principal that asked for the run, or the route that named the agent."),
		fielddef.Optional("requestedByKind", "string", "Its kind: user, party, or route."),
		fielddef.Optional("actingForKind", "string", "The kind of principal the run acts for: user or party; null for a run that acts for itself."),
		fielddef.Optional("actingFor", "string", "Whom the run acts for: a user's id, or a party row's key."),
		fielddef.Optional("actingForTable", "string", "The party's table."),
		fielddef.Optional("trust", "string", "The trust of the inbound message the run answers (D157, D183): verified, or entered for a sender whose identity was entered by hand or whose message the dispatcher could not read, whose every operation but a read asks; null for a run no message started."),
		fielddef.Required("targets", jsonScalar, "The run's scope: the rows its token names, each { table, key }."),
		fielddef.Optional("grants", jsonScalar, "The grants the run's token is, by id (D156); revoked when the run ends."),
		fielddef.Optional("instance", "string", "The sandbox instance the run runs in."),
		fielddef.Optional("sandboxSlot", "string", "The sandbox provider's slot."),
		fielddef.Optional("modelSlot", "string", "The model provider's slot."),
		fielddef.WithDefault(fielddef.Required("status", "string", "queued, running, waiting, done, failed or cancelled."), &queued),
		fielddef.Optional("outcome", "string", "How it ended: done, failed, cancelled, paused, budget or refused."),
		fielddef.Optional("detail", "string", "Why, for an outcome that is not done."),
		fielddef.WithDefault(fielddef.Required("attempts", int64Scalar, "How many sandbox instances it started."), &zero),
		fielddef.WithDefault(fielddef.Required("turns", int64Scalar, "How many model turns it took."), &zero),
		fielddef.WithDefault(fielddef.Required("reads", int64Scalar, "How many tool calls that read it made."), &zero),
		fielddef.WithDefault(fielddef.Required("writes", int64Scalar, "How many tool calls that write it made."), &zero),
		fielddef.WithDefault(fielddef.Required("messagesSent", int64Scalar, "How many messages to a party it sent."), &zero),
		fielddef.WithDefault(fielddef.Required("approvals", int64Scalar, "How many approvals it asked for."), &zero),
		fielddef.WithDefault(fielddef.Required("untrusted", int64Scalar, "How many tool results it read that carried outside content, marked untrusted."), &zero),
		fielddef.WithDefault(fielddef.Required("inputTokens", int64Scalar, "The model's input tokens."), &zero),
		fielddef.WithDefault(fielddef.Required("outputTokens", int64Scalar, "The model's output tokens."), &zero),
		fielddef.WithDefault(fielddef.Required("cost", int64Scalar, "What the run cost, in the budget's units."), &zero),
		fielddef.Optional("credentialUses", jsonScalar, "Each credential the sandbox used: its reference, provider, target and time; never its value."),
		fielddef.Optional("untrustedCalls", jsonScalar, "The tool calls whose results carried outside content, by id."),
		fielddef.Optional("transcript", jsonScalar, "The redacted transcript while the run runs; a bucket holds it once the run ends."),
		fielddef.Optional("transcriptObject", "string", "The bucket object the redacted transcript is in."),
		fielddef.Optional("transcriptPrunedAt", dateTimeScalar, "When the transcript was deleted, its retention passed."),
		fielddef.WithDefault(fielddef.Required("createdAt", dateTimeScalar, ""), fielddef.Now()),
		fielddef.Optional("startedAt", dateTimeScalar, "When its sandbox instance started."),
		fielddef.Optional("endedAt", dateTimeScalar, "When it ended."),
	}, []ir.IndexDef{
		{Keys: []string{"status"}, Name: "status"},
		{Keys: []string{"agent", "createdAt"}, Name: "agent"},
		{Keys: []string{"triggerTable", "triggerKey"}, Name: "trigger"},
	}
}

// requestFields are a message of the dispatcher's queue: a run request, an
// instance to watch, an ask's deadline, each due when its message is (D80).
// An inbound message from a party (D157) is { trigger: "inbound", party,
// message, grant }, which names no agent: the dispatcher takes the agent
// whose definition answers inbound messages.
func requestFields() []*ir.FieldDef {
	return []*ir.FieldDef{
		fielddef.Optional("step", "string", "start (null), watch or followUp."),
		fielddef.Optional("agent", "string", "The agent, by name or key; null for an inbound message, which the agent that answers inbound messages takes."),
		fielddef.Optional("trigger", "string", "route, assigned, schedule, principal or inbound."),
		fielddef.Optional("subjectTable", "string", "The table of the row the run is about, by its type's name."),
		fielddef.Optional("subjectKey", "string", "That row's key as text."),
		fielddef.Optional("targets", jsonScalar, "More rows of the run's scope, each { table, key }."),
		fielddef.Optional("event", jsonScalar, "The event a route's request is of."),
		fielddef.Optional("input", jsonScalar, "What the run is given: { task }."),
		fielddef.Optional("requestedBy", jsonScalar, "The principal that asked: { kind, subject }."),
		fielddef.Optional("actingFor", jsonScalar, "Whom the run acts for: { kind, subject, table? }."),
		fielddef.Optional("party", jsonScalar, "An inbound message's party: { table, key } (D157)."),
		fielddef.Optional("message", jsonScalar, "An inbound message (D157): its row as { table, key }, or its text as { body }."),
		fielddef.Optional("grant", "string", "The party's active grant an inbound message reaches rows through (D156)."),
		fielddef.Optional("run", uuidScalar, "The run a watch, a retry, a follow-up or an approved start is of."),
		fielddef.Optional("ask", jsonScalar, "An ask's deadline: { table, key, thread, question }."),
	}
}

// policyFields are an autonomy policy row: per agent (or every agent) and
// per operation or operation class, auto, tell or ask.
func policyFields() ([]*ir.FieldDef, []ir.IndexDef) {
	return []*ir.FieldDef{
		fielddef.Key(),
		fielddef.Optional("agent", uuidScalar, "The agent; null for every agent."),
		fielddef.Optional("operation", "string", "The operation or tool, by name; null for a class."),
		fielddef.Optional("operationClass", "string", "read, internalWrite, messageParty or moneyMovement; null for an operation."),
		fielddef.Required("mode", "string", "auto, tell or ask. An operation that moves money is ask whatever a row says."),
		fielddef.Optional("permission", "string", "The permission an approver of an ask holds; agents.approve when null."),
		fielddef.WithDefault(fielddef.Required("createdAt", dateTimeScalar, ""), fielddef.Now()),
	}, []ir.IndexDef{
		{Keys: []string{"agent"}, Name: "agent"},
	}
}

// approvalFields are an approval an ask wrote: the exact parameters and
// their hash, who may decide and who did, on which channel.
func approvalFields() ([]*ir.FieldDef, []ir.IndexDef) {
	pending := "pending"
	return []*ir.FieldDef{
		fielddef.Key(),
		fielddef.Required("agent", uuidScalar, "The agent that asked."),
		fielddef.Optional("run", uuidScalar, "The run that asked."),
		fielddef.Required("operation", "string", "The operation or tool it asks to run."),
		fielddef.Required("operationClass", "string", "Its class."),
		fielddef.Required("params", jsonScalar, "The exact parameters it asks to run with."),
		fielddef.Required("paramsHash", sha256Scalar, "The SHA-256 of the operation and its parameters as canonical JSON: a call with any other parameter asks again."),
		fielddef.Required("summary", "string", "What an approver is shown."),
		fielddef.Required("permission", "string", "The permission an approver holds."),
		fielddef.WithDefault(fielddef.Required("state", "string", "pending, approved, denied, used or superseded."), &pending),
		fielddef.WithDefault(fielddef.Required("requestedAt", dateTimeScalar, ""), fielddef.Now()),
		fielddef.Optional("decidedBy", "string", "Who approved or denied it."),
		fielddef.Optional("decidedVia", "string", "The channel it was decided on: inapp, sms, email, api."),
		fielddef.Optional("decidedAt", dateTimeScalar, ""),
		fielddef.Optional("usedAt", dateTimeScalar, "When the call it approved ran."),
		fielddef.Optional("expiresAt", dateTimeScalar, "When a pending or approved one stops counting."),
		fielddef.Optional("holds", "string", "What the approval releases once approved, or refuses once denied: a message to a party held awaiting it (D157), by its row's key."),
	}, []ir.IndexDef{
		{Keys: []string{"run", "operation"}, Name: "run"},
		{Keys: []string{"state"}, Name: "state"},
	}
}

// controlFields are an agent's controls, or every agent's (agent null):
// the stop switch and the budget's limit, which a Budget meter the class
// composes reads through limitField.
func controlFields() ([]*ir.FieldDef, []ir.IndexDef) {
	off := "false"
	eighty := "80"
	return []*ir.FieldDef{
		fielddef.Key(),
		fielddef.Optional("agent", uuidScalar, "The agent; null for the row of every agent."),
		fielddef.WithDefault(fielddef.Required("paused", "boolean", "Whether the agent, or every agent, is paused: no run starts, and runs in flight were cancelled."), &off),
		fielddef.Optional("pausedReason", "string", "Why: stopped (a principal paused it) or budget (it reached its cap)."),
		fielddef.Optional("pausedBy", "string", "Who paused it."),
		fielddef.Optional("pausedAt", dateTimeScalar, ""),
		fielddef.Optional("budgetLimit", int64Scalar, "The budget's limit for its period, in its units, which a Budget meter reads through limitField: budgetLimit; no limit when null."),
		fielddef.WithDefault(fielddef.Required("warnAt", int64Scalar, "The percent of the limit at which a warning event is raised."), &eighty),
	}, []ir.IndexDef{
		{Keys: []string{"agent"}, Unique: true, Name: "agent"},
	}
}
