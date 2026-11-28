package ir

import (
	"fmt"
	"sort"
)

// Per-record access (D156): who may see a row, the grants that let a caller
// who is no user reach rows, and the log of sensitive reads.
//
// A DB table marked @audience holds, on each row, who may see it: everyone
// (the default), private (its owner alone) or named principals, and what
// an agent run may do with it. A table whose rows sit under another's
// inherits the parent's through a to-one relation (inheritFrom). The
// loader adds the columns ([AudienceFields]); a trigger the build writes
// stores each row's effective audience beside them, so a read is one
// filter on the row (D156).
//
// A class marked @accessGrants is the table of grants: each lets a party or
// an agent run reach a target row, and the rows its role's paths reach from
// it, between a start and an end. A class marked @accessLog is the table
// the runtime records reveals, agent runs' reads and API keys' calls in.
// The loader fills both classes' fields ([AccessGrantFields],
// [AccessLogFields]).

// AudienceDef is the @audience declaration of a DB table (D156).
//
// In TypeScript: @audience<T>({ owner?, inheritFrom?, default?, agents? })
type AudienceDef struct {
	// Owner is the field that holds the row's owner, the one principal a
	// private row is visible to and a named row always is: a field that
	// holds a UUID, or a to-one relation, whose key is it. Empty, the
	// owner is createdBy, which the table then declares.
	Owner string `json:"owner,omitempty" yaml:"owner,omitempty"`

	// InheritFrom is a to-one relation of the table to a table that is
	// @audience too: a row whose audience is inherit sees what its parent
	// sees, and the build keeps it so when the parent changes.
	InheritFrom string `json:"inheritFrom,omitempty" yaml:"inheritFrom,omitempty"`

	// Default is the audience a row is created with when its create names
	// none: AudienceEveryone, AudiencePrivate or AudienceNamed, or
	// AudienceInherit on a table with InheritFrom. Empty is inherit on a
	// table with InheritFrom and everyone on any other.
	Default string `json:"default,omitempty" yaml:"default,omitempty"`

	// Agents is what an agent run may do with a row when its create names
	// nothing: AgentsFollowRole, AgentsReadOnly or AgentsNone. Empty is
	// followRole.
	Agents string `json:"agents,omitempty" yaml:"agents,omitempty"`
}

// The audiences a row may have (AudienceDef, the audience column).
const (
	// AudienceEveryone is every principal the route admits.
	AudienceEveryone = "everyone"
	// AudiencePrivate is the row's owner alone.
	AudiencePrivate = "private"
	// AudienceNamed is the owner and the principals audienceNamed lists.
	AudienceNamed = "named"
	// AudienceInherit is the parent's effective audience, through the
	// table's inheritFrom; everyone for a row with no parent.
	AudienceInherit = "inherit"
)

// Audiences are the values of the audience column, in order.
var Audiences = []string{AudienceEveryone, AudiencePrivate, AudienceNamed, AudienceInherit}

// What an agent run may do with a row (AudienceDef.Agents, the agentAccess
// column). An agent run sees a row its audience admits for the agent or for
// whom the run acts for, and then:
const (
	// AgentsFollowRole reads and writes as the run's role allows.
	AgentsFollowRole = "followRole"
	// AgentsReadOnly reads and never writes.
	AgentsReadOnly = "readOnly"
	// AgentsNone neither reads nor writes: the row is not there for it.
	AgentsNone = "none"
)

// AgentAccesses are the values of the agentAccess column, from the most
// open to the least. A row's effective agent access is the least open of
// its own and its parent's.
var AgentAccesses = []string{AgentsFollowRole, AgentsReadOnly, AgentsNone}

// The fields @audience adds to a table (D156), each with Origin
// OriginAccess. The first three are the row's to write; the effective ones
// are the trigger's, which sets them at every insert and update whatever a
// write gives them.
const (
	// AudienceField holds the row's audience, one of Audiences.
	AudienceField = "audience"
	// AudienceNamedField lists the principals a named row is visible to,
	// beside its owner, by their UUIDs.
	AudienceNamedField = "audienceNamed"
	// AgentAccessField holds what an agent run may do with the row, one
	// of AgentAccesses.
	AgentAccessField = "agentAccess"
	// EffectiveAudienceField is the principals the row is visible to, by
	// their UUIDs, or null for everyone: the audience resolved, through
	// the parent for inherit.
	EffectiveAudienceField = "effectiveAudience"
	// EffectiveAgentsField is the agent access resolved: the least open of
	// the row's own and its parent's effective one.
	EffectiveAgentsField = "effectiveAgents"
)

// AudienceFields are the fields @audience adds, in order.
var AudienceFields = []string{AudienceField, AudienceNamedField, AgentAccessField, EffectiveAudienceField, EffectiveAgentsField}

// OriginAccess marks a field or a type the loader added for per-record
// access (D156): an @audience table's columns, and an @accessGrants or
// @accessLog class's fields.
const OriginAccess = "access"

// AccessGrantsDef is the @accessGrants declaration of a DB class (D156):
// the class is the table of grants, whose fields the loader fills. A
// schema has at most one.
type AccessGrantsDef struct{}

// AccessLogDef is the @accessLog declaration of a DB class (D156): the
// class is the access log, whose fields the loader fills. A schema has at
// most one.
type AccessLogDef struct{}

// The fields of an @accessGrants table, which the loader adds.
const (
	// AccessGrantHolderField is the principal the grant lets reach rows:
	// a party's or an agent run's UUID.
	AccessGrantHolderField = "holder"
	// AccessGrantHolderKindField is the kind of that principal:
	// PrincipalParty or PrincipalAgent.
	AccessGrantHolderKindField = "holderKind"
	// AccessGrantRoleField is the name of the role whose permissions the
	// holder acts with, and whose reaches say which rows beside the
	// target it reaches: a row of the schema's UserRole table.
	AccessGrantRoleField = "role"
	// AccessGrantTargetTypeField is the table of the target row, by its
	// type's name.
	AccessGrantTargetTypeField = "targetType"
	// AccessGrantTargetIDField is the target row's key.
	AccessGrantTargetIDField = "targetId"
	// AccessGrantStartsAtField is when the grant starts.
	AccessGrantStartsAtField = "startsAt"
	// AccessGrantEndsAtField is when it ends; null for no date.
	AccessGrantEndsAtField = "endsAt"
	// AccessGrantEndsWhenFieldField and AccessGrantEndsWhenStateField end
	// the grant when the target's field holds the state: a status reaching
	// closed. Both null for no such end.
	AccessGrantEndsWhenFieldField = "endsWhenField"
	AccessGrantEndsWhenStateField = "endsWhenState"
	// AccessGrantRevokedAtField is when the grant was revoked.
	AccessGrantRevokedAtField = "revokedAt"
	// AccessGrantTokenHashField is the SHA-256 of the bearer token the
	// holder presents, which only the holder has; null for a grant the
	// runtime resolves another way (an agent run's, a channel's).
	AccessGrantTokenHashField = "tokenHash"
	// AccessGrantCreatedAtField and AccessGrantCreatedByField record who
	// made the grant, and when.
	AccessGrantCreatedAtField = "createdAt"
	AccessGrantCreatedByField = "createdBy"
)

// The fields of an @accessLog table, which the loader adds.
const (
	// AccessLogAtField is when the access happened.
	AccessLogAtField = "at"
	// AccessLogKindField is what happened, one of AccessLogKinds.
	AccessLogKindField = "kind"
	// AccessLogPrincipalField and AccessLogPrincipalKindField are who
	// acted: its subject and its kind.
	AccessLogPrincipalField     = "principal"
	AccessLogPrincipalKindField = "principalKind"
	// AccessLogActingForField is the subject the principal acted for; null
	// when it acted for itself.
	AccessLogActingForField = "actingFor"
	// AccessLogCredentialField names the credential the call came with: an
	// API key's or a grant's id; null for a session.
	AccessLogCredentialField = "credential"
	// AccessLogOperationField is the operation the call ran.
	AccessLogOperationField = "operation"
	// AccessLogTargetTypeField, AccessLogTargetIDField and
	// AccessLogFieldField are the row read and, for a reveal, its field.
	AccessLogTargetTypeField = "targetType"
	AccessLogTargetIDField   = "targetId"
	AccessLogFieldField      = "field"
)

// The kinds of an access log row.
const (
	// AccessLogReveal is a reveal of an Encrypted<> field's value.
	AccessLogReveal = "reveal"
	// AccessLogAgentRead is a read an agent run made.
	AccessLogAgentRead = "agentRead"
	// AccessLogKeyCall is a call made with an API key (D114).
	AccessLogKeyCall = "keyCall"
	// AccessLogGrantCall is a call made with a grant's token.
	AccessLogGrantCall = "grantCall"
	// AccessLogOAuthCall is a call made with an OAuth access token (#695),
	// its credential the OAuth grant's id.
	AccessLogOAuthCall = "oauthCall"
	// AccessLogOAuthGrant is an OAuth grant a person gave a client on the
	// consent page (#695).
	AccessLogOAuthGrant = "oauthGrant"
	// AccessLogOAuthRevoke is an OAuth grant revoked (#695): by its user,
	// an administrator, the client, or a replayed code or refresh token.
	AccessLogOAuthRevoke = "oauthRevoke"
)

// AccessLogKinds are the kinds of an access log row, in order. The OAuth
// kinds are the TypeScript runtime's alone until Go and Rust serve OAuth.
var AccessLogKinds = []string{AccessLogReveal, AccessLogAgentRead, AccessLogKeyCall, AccessLogGrantCall, AccessLogOAuthCall, AccessLogOAuthGrant, AccessLogOAuthRevoke}

// The kinds of principal (D156). Every route, MCP tool, reply and search
// resolves its caller to exactly one, and permissions are checked the same
// way for each.
const (
	// PrincipalUser is a row of the User table, signed in by a session or
	// an API key.
	PrincipalUser = "user"
	// PrincipalParty is a row of an app table that holds a grant: a person
	// or an organisation outside the User table.
	PrincipalParty = "party"
	// PrincipalAgent is an agent run, which acts through a grant and for
	// whom it serves.
	PrincipalAgent = "agent"
	// PrincipalExtension is a runtime provider (D159): a channel adapter,
	// a credential provider, a sandbox or model provider or a file
	// processor, acting as itself with the narrow role its package
	// declares (RuntimeProvider.Role), never for a user.
	PrincipalExtension = "extension"
)

// PrincipalKinds are the kinds of principal, in order.
var PrincipalKinds = []string{PrincipalUser, PrincipalParty, PrincipalAgent, PrincipalExtension}

// GrantHolderKinds are the kinds of principal a grant may be held by.
var GrantHolderKinds = []string{PrincipalParty, PrincipalAgent}

// UserRoleReachesField is the optional field of a UserRole table that
// lists the relation paths a role reaches from a grant's target (D156),
// each "Type:path", as "Request:unit.contract": a row of Request whose
// unit's contract is the target. A role without it reaches the target
// alone.
const UserRoleReachesField = "reaches"

// typeNames are the schema's type names, sorted.
func (s *Schema) typeNames() []string {
	names := make([]string, 0, len(s.Types))
	for name := range s.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// AudienceTables returns the schema's @audience tables, in name order.
func (s *Schema) AudienceTables() []*TypeDef {
	var out []*TypeDef
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Audience != nil {
			out = append(out, td)
		}
	}
	return out
}

// AccessGrantsTable returns the schema's @accessGrants table, or nil.
func (s *Schema) AccessGrantsTable() *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.AccessGrants != nil {
			return td
		}
	}
	return nil
}

// AccessLogTable returns the schema's @accessLog table, or nil.
func (s *Schema) AccessLogTable() *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.AccessLog != nil {
			return td
		}
	}
	return nil
}

// validateAudience checks what td's @audience alone decides, in every
// authoring form: its default and its agents are known values, and inherit
// is a default only beside inheritFrom. What needs the fields and the
// tables (that owner and inheritFrom name fields of the right kind) is the
// loader's verification pass's.
func validateAudience(td *TypeDef) []error {
	a := td.Audience
	if a == nil {
		return nil
	}
	var errs []error
	if a.Default != "" && !containsString(Audiences, a.Default) {
		errs = append(errs, fmt.Errorf("%s: @audience default %q is none of %v", td.Name, a.Default, Audiences))
	}
	if a.Default == AudienceInherit && a.InheritFrom == "" {
		errs = append(errs, fmt.Errorf("%s: @audience default inherit needs inheritFrom, the relation to the parent whose audience a row inherits", td.Name))
	}
	if a.Agents != "" && !containsString(AgentAccesses, a.Agents) {
		errs = append(errs, fmt.Errorf("%s: @audience agents %q is none of %v", td.Name, a.Agents, AgentAccesses))
	}
	if td.AccessGrants != nil || td.AccessLog != nil {
		errs = append(errs, fmt.Errorf("%s: @audience marks a table of the application's rows, and %s is the access tables' own", td.Name, td.Name))
	}
	return errs
}

// DefaultAudience is the audience a row of td is created with when its
// create names none.
func (a *AudienceDef) DefaultAudience() string {
	switch {
	case a == nil:
		return AudienceEveryone
	case a.Default != "":
		return a.Default
	case a.InheritFrom != "":
		return AudienceInherit
	}
	return AudienceEveryone
}

// DefaultAgents is what an agent run may do with a row whose create names
// nothing.
func (a *AudienceDef) DefaultAgents() string {
	if a == nil || a.Agents == "" {
		return AgentsFollowRole
	}
	return a.Agents
}

func containsString(list []string, value string) bool {
	for _, v := range list {
		if v == value {
			return true
		}
	}
	return false
}
