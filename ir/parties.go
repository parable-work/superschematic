package ir

import (
	"fmt"
	"slices"
)

// Outside parties and inbound messages (D157).
//
// A DB table marked @party holds parties: people or organisations outside
// the User table whose rows are principals of kind PrincipalParty (D156). A
// party acts only through the grants it holds (the @accessGrants table's
// rows whose holder is the party row's key), and is reached at the
// addresses of the channel identities (D153) that resolve to it, a
// principalKind of party, a principalTable of the party table's name and
// a principal of the row's key.
//
// The conversations are the core's tables, as the channel identities are:
// classes the app declares and the loader fills, marked
// @conversations("<part>"). A conversation is one per party and channel,
// or per unknown address and channel; a message is one inbound or outbound
// message of a conversation, its attachments the message table's
// @attachments (D154), its references the message table's @references, so
// a message appears on each row it references. A message held in triage
// is one the inbound path could not give to anyone: an unknown sender, an
// email that failed its authentication, a party with no active grant.
//
// A @queue class marked @runRequests is the queue an inbound message from
// a party with an active grant puts a run request on, which an agent run
// (D158) takes: { trigger: "inbound", party, message, grant }.

// PartyDef is the @party declaration of a DB table (D157).
//
// In TypeScript: @party<T>({ title? })
type PartyDef struct {
	// Title is the field a party is named by in triage and on its
	// timeline; empty names it by its key.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`
}

// ConversationsDef marks a DB class that is one of the conversations'
// tables (D157), its part one of ConversationParts. The loader fills its
// fields.
type ConversationsDef struct {
	Part string `json:"part" yaml:"part"`
}

// RunRequestsDef marks the @queue class that is the queue of run requests
// (D157): the loader fills its fields. A schema has at most one.
type RunRequestsDef struct{}

// The parts of the conversations, each a class the app declares with
// @conversations("<part>").
const (
	// ConversationsPart is the conversations: one per party and channel,
	// or per unknown address and channel.
	ConversationsPart = "conversations"
	// MessagesPart is the messages: each inbound or outbound message of a
	// conversation.
	MessagesPart = "messages"
)

// ConversationParts are the parts of the conversations, in order. A schema
// that declares one declares both.
var ConversationParts = []string{ConversationsPart, MessagesPart}

// OriginConversations marks a field the loader added for the
// conversations (D157): a @conversations class's fields and a @runRequests
// queue's.
const OriginConversations = "conversations"

// The directions of a message.
const (
	MessageInbound  = "inbound"
	MessageOutbound = "outbound"
)

// What became of an inbound message (the message's disposition, D157).
const (
	// DispositionRun is a message from a party with an active grant: a
	// run request went on the @runRequests queue.
	DispositionRun = "run"
	// DispositionTimeline is a message from a party with no active grant:
	// on the party's timeline, and held in triage.
	DispositionTimeline = "timeline"
	// DispositionUnknown is a message from no identity the deployment
	// trusts: an unknown address, an email that failed its
	// authentication, or an address two parties share that no thread
	// settles. Held in triage, given to no one.
	DispositionUnknown = "unknown"
	// DispositionLapsed is a message from a party's identity whose grants
	// have all ended since it was verified: the address may have changed
	// hands, so it is held in triage as an unknown sender's until the
	// identity is verified again.
	DispositionLapsed = "lapsed"
	// DispositionOptOut is an opt-out or opt-in keyword the adapter read,
	// recorded on the address's identities.
	DispositionOptOut = "optOut"
	// DispositionVerified is a code that verified the sender's identity
	// again.
	DispositionVerified = "verified"
)

// Dispositions are what may become of an inbound message, in order.
var Dispositions = []string{DispositionRun, DispositionTimeline, DispositionUnknown, DispositionLapsed, DispositionOptOut, DispositionVerified}

// The states of an outbound message.
const (
	OutboundQueued   = "queued"
	OutboundSent     = "sent"
	OutboundFailed   = "failed"
	OutboundRefused  = "refused"
	OutboundApproval = "awaitingApproval"
)

// The states of a message in triage.
const (
	TriageOpen   = "open"
	TriageClosed = "closed"
)

// ChannelIdentityOptedOutAtField is the field of a @channelIdentities
// table that records when the address opted out (D157): the core sends
// nothing to it after.
const ChannelIdentityOptedOutAtField = "optedOutAt"

// PartyTables returns the schema's @party tables, in name order.
func (s *Schema) PartyTables() []*TypeDef {
	var out []*TypeDef
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Party != nil {
			out = append(out, td)
		}
	}
	return out
}

// ConversationsTable returns the schema's class marked
// @conversations(part), or nil.
func (s *Schema) ConversationsTable(part string) *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Conversations != nil && td.Conversations.Part == part {
			return td
		}
	}
	return nil
}

// RunRequestsQueue returns the schema's @runRequests queue, or nil.
func (s *Schema) RunRequestsQueue() *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.RunRequests != nil {
			return td
		}
	}
	return nil
}

// UsesParties reports whether the DB schema declares a @party table, a
// @conversations class or a @runRequests queue.
func (s *Schema) UsesParties() bool {
	if s == nil {
		return false
	}
	for _, td := range s.Types {
		if td != nil && (td.Party != nil || td.Conversations != nil || td.RunRequests != nil) {
			return true
		}
	}
	return false
}

// validateParties checks what td's @party, @conversations and
// @runRequests declarations alone decide. What needs the fields and the
// other tables is the loader's verification pass's.
func validateParties(td *TypeDef) []error {
	var errs []error
	if td.Conversations != nil && !slices.Contains(ConversationParts, td.Conversations.Part) {
		errs = append(errs, fmt.Errorf("%s: @conversations part %q is none of %v", td.Name, td.Conversations.Part, ConversationParts))
	}
	marks := 0
	for _, set := range []bool{td.Party != nil, td.Conversations != nil, td.RunRequests != nil, td.Notifier != nil, td.ChannelIdentities != nil, td.AccessGrants != nil, td.AccessLog != nil} {
		if set {
			marks++
		}
	}
	if marks > 1 && (td.Party != nil || td.Conversations != nil || td.RunRequests != nil) {
		errs = append(errs, fmt.Errorf("%s: a class is one of a party table, a conversations table, the run requests, the notifier's tables, the channel identities or the access tables, not more than one", td.Name))
	}
	return errs
}
