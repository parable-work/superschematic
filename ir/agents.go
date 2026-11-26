package ir

import (
	"fmt"
	"slices"
	"sort"
)

// Agent orchestration (D158).
//
// An agent is a row: its definition is a @versioned table the app
// declares, holding its name, purpose, instructions, the role it acts
// with (each agent is its own principal), its tools and credential
// references, its model and the provider slots of its sandbox and model,
// and its limits. A run is a row too: what started it, the definition
// version it ran, its scope, what it read and wrote, the messages it sent,
// its approvals, its cost and its outcome. The dispatcher, a worker of the
// run-request queue, claims a request, writes the run, mints the run's
// token (a grant, D156) and starts a fresh sandbox instance (D159).
//
// The orchestration's tables are classes the app declares and the loader
// fills, one class per part, each marked @agents("<part>"): the
// definitions, the runs, the requests (the dispatcher's queue), the
// autonomy policies, the approvals an ask writes, and the controls (one
// row per agent and one for every agent: the stop switch and the budget).
// A class may declare fields of its own: the loader adds each of its
// part's fields the class does not declare, so the controls may implement
// Budget (D104) over a limit field, and the runs Lease and Retries over a
// Workflow status the app declares.

// AgentsDef marks a DB class that is one of the agent orchestration's
// tables (D158), its part one of AgentParts.
type AgentsDef struct {
	Part string `json:"part" yaml:"part"`
}

// The parts of the agent orchestration, each a class the app declares with
// @agents("<part>").
const (
	// AgentDefinitions is the agent definitions, a @versioned table: a
	// run records the version of the row it ran.
	AgentDefinitions = "definitions"
	// AgentRuns is the run records.
	AgentRuns = "runs"
	// AgentRequests is the dispatcher's D80 queue: a run request, an
	// instance to watch, an ask's deadline.
	AgentRequests = "requests"
	// AgentPolicies is the autonomy policy: auto, tell or ask per agent
	// and per operation or operation class.
	AgentPolicies = "policies"
	// AgentApprovals is the approvals an ask writes: the exact parameters
	// and their hash, and who decided on which channel.
	AgentApprovals = "approvals"
	// AgentControls is one row per agent and one for every agent (its
	// agent null): whether it is paused, and its budget.
	AgentControls = "controls"
)

// AgentParts are the parts, in order. Every one is required.
var AgentParts = []string{AgentDefinitions, AgentRuns, AgentRequests, AgentPolicies, AgentApprovals, AgentControls}

// OriginAgents marks a field the loader added to an @agents class (D158).
const OriginAgents = "agents"

// The classes of operation an autonomy policy row may name in place of an
// operation (D158).
const (
	// AgentClassRead reads and changes nothing.
	AgentClassRead = "read"
	// AgentClassInternalWrite writes rows of the app.
	AgentClassInternalWrite = "internalWrite"
	// AgentClassMessageParty sends a message to a party outside the app
	// (D157).
	AgentClassMessageParty = "messageParty"
	// AgentClassMoneyMovement moves money. Its autonomy is ask and nothing
	// else: the loader refuses another on an operation, and the runtime
	// a policy row that says another.
	AgentClassMoneyMovement = "moneyMovement"
)

// AgentClasses are the operation classes, from the least consequential.
var AgentClasses = []string{AgentClassRead, AgentClassInternalWrite, AgentClassMessageParty, AgentClassMoneyMovement}

// The autonomy an agent has over an operation (D158).
const (
	// AutonomyAuto runs the operation.
	AutonomyAuto = "auto"
	// AutonomyTell runs it and tells the principals the routes name.
	AutonomyTell = "tell"
	// AutonomyAsk writes an approval holding the exact parameters and
	// their hash, and runs the operation only once someone holding the
	// approval's permission approves it, on any channel.
	AutonomyAsk = "ask"
)

// AutonomyModes are the modes, from the most autonomous.
var AutonomyModes = []string{AutonomyAuto, AutonomyTell, AutonomyAsk}

// The triggers a run request names (D158).
const (
	// AgentTriggerRoute is a notifier route that names the agent (D153).
	AgentTriggerRoute = "route"
	// AgentTriggerAssigned is a row assigned to the agent.
	AgentTriggerAssigned = "assigned"
	// AgentTriggerSchedule is a schedule (D152's recurrence, Schedules).
	AgentTriggerSchedule = "schedule"
	// AgentTriggerPrincipal is a principal asking.
	AgentTriggerPrincipal = "principal"
	// AgentTriggerInbound is an inbound message from a party (D157).
	AgentTriggerInbound = "inbound"
)

// AgentTriggers are the triggers, in order.
var AgentTriggers = []string{AgentTriggerRoute, AgentTriggerAssigned, AgentTriggerSchedule, AgentTriggerPrincipal, AgentTriggerInbound}

// OperationAutonomy is the @autonomy declaration of an API operation
// (D158): the class of operation it is to an agent's autonomy policy, and
// the autonomy an agent has over it when no policy row names it.
//
// In TypeScript: @autonomy({ class, mode? })
type OperationAutonomy struct {
	// Class is one of AgentClasses.
	Class string `json:"class" yaml:"class"`
	// Mode is one of AutonomyModes; empty is the class's default: auto
	// for a read and an internal write, ask for a message to a party and
	// a money movement.
	Mode string `json:"mode,omitempty" yaml:"mode,omitempty"`
}

// DefaultAutonomy is the autonomy an agent has over an operation of class
// when neither a policy row nor the operation's @autonomy says.
func DefaultAutonomy(class string) string {
	switch class {
	case AgentClassRead, AgentClassInternalWrite:
		return AutonomyAuto
	}
	return AutonomyAsk
}

// ValidateOperationAutonomy checks an @autonomy declaration: a known class
// and mode, and a money movement's mode ask alone.
func ValidateOperationAutonomy(a *OperationAutonomy) error {
	if a == nil {
		return nil
	}
	if !slices.Contains(AgentClasses, a.Class) {
		return fmt.Errorf("class %q is none of %v", a.Class, AgentClasses)
	}
	if a.Mode != "" && !slices.Contains(AutonomyModes, a.Mode) {
		return fmt.Errorf("mode %q is none of %v", a.Mode, AutonomyModes)
	}
	if a.Class == AgentClassMoneyMovement && a.Mode != "" && a.Mode != AutonomyAsk {
		return fmt.Errorf("an operation that moves money is ask and nothing else, and this one says %s: an agent may only ask to move money (D158)", a.Mode)
	}
	return nil
}

// AgentsTable returns the schema's class marked @agents(part), or nil.
func (s *Schema) AgentsTable(part string) *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Agents != nil && td.Agents.Part == part {
			return td
		}
	}
	return nil
}

// UsesAgents reports whether the schema uses the agent orchestration: a DB
// schema that declares an @agents class, or an API with an operation an
// @autonomy declares. An API over such a database needs a sandbox provider
// and a model provider (D159's coverage).
func (s *Schema) UsesAgents() bool {
	if s == nil {
		return false
	}
	for _, td := range s.Types {
		if td != nil && td.Agents != nil {
			return true
		}
	}
	return false
}

// AgentPartsOf returns the parts the schema declares, sorted.
func (s *Schema) AgentPartsOf() []string {
	var parts []string
	for _, td := range s.Types {
		if td != nil && td.Agents != nil {
			parts = append(parts, td.Agents.Part)
		}
	}
	sort.Strings(parts)
	return parts
}

// validateAgents checks what td's @agents declaration alone decides.
func validateAgents(td *TypeDef) []error {
	if td.Agents == nil {
		return nil
	}
	var errs []error
	if !slices.Contains(AgentParts, td.Agents.Part) {
		errs = append(errs, fmt.Errorf("%s: @agents part %q is none of %v", td.Name, td.Agents.Part, AgentParts))
	}
	if td.Notifier != nil || td.ChannelIdentities != nil {
		errs = append(errs, fmt.Errorf("%s: a class is one of the agent orchestration's tables or the notifier's, not both", td.Name))
	}
	return errs
}
