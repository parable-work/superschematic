// Package conversations expands a DB schema's conversations (D157) into
// ordinary fields, as the notifications expansion does for the notifier's
// tables: the fields of each class marked @conversations("<part>") and of
// the @queue class marked @runRequests, when it declares none. The sql,
// orm and types generators emit the result as any table's or queue's.
//
// It runs before the references and attachments expansion, so a messages
// class may take @references and @attachments, whose sub-tables are keyed
// by the key it adds. Every field it adds carries
// ir.OriginConversations, so the schema writer can skip it and write the
// declaration instead.
package conversations

import (
	"sort"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// Scalars the added fields use.
const (
	uuidScalar     = "Identity.UUID"
	dateTimeScalar = "Temporal.DateTime"
	jsonScalar     = "Generic.JSON"
)

// Expand fills the fields of every conversations class and of the run
// requests queue that declares none. It returns the scalar names the added
// fields use, so the caller can hydrate any the schema did not already
// reference.
func Expand(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	sort.Strings(names)
	used := false
	for _, name := range names {
		td := schema.Types[name]
		if td == nil || len(td.Fields) > 0 {
			continue
		}
		var fields []*ir.FieldDef
		var indexes []ir.IndexDef
		switch {
		case td.RunRequests != nil && td.Queue != nil:
			fields = runRequestFields()
		case td.Conversations != nil && td.Queue == nil && td.Conversations.Part == ir.ConversationsPart:
			fields, indexes = conversationFields()
		case td.Conversations != nil && td.Queue == nil && td.Conversations.Part == ir.MessagesPart:
			fields, indexes = messageFields()
		}
		if fields == nil {
			continue
		}
		for _, fd := range fields {
			fd.Origin = ir.OriginConversations
		}
		for i := range indexes {
			indexes[i].Origin = ir.OriginConversations
		}
		td.Fields = fields
		td.Indexes = append(td.Indexes, indexes...)
		used = true
	}
	if !used {
		return nil
	}
	scalars := []string{uuidScalar, dateTimeScalar, jsonScalar}
	for _, name := range scalars {
		if schema.Scalars[name] == nil {
			schema.Scalars[name] = &ir.ScalarDef{Name: name}
		}
	}
	return scalars
}

// conversationFields are a conversation's: one per party and channel, or
// per unknown address and channel.
func conversationFields() ([]*ir.FieldDef, []ir.IndexDef) {
	return []*ir.FieldDef{
		keyField(),
		required("channelKind", "string", "The kind of channel it is on: sms, email, chat."),
		required("address", "string", "The outside party's address on the channel, normalised as its channel identity holds it."),
		optional("partyTable", "string", "The table of the party it is with, by its type's name; null for an unknown sender's."),
		optional("party", uuidScalar, "The party row's key; null for an unknown sender's."),
		optional("subject", "string", "What it is about: the subject of its first email."),
		withDefault(required("createdAt", dateTimeScalar, ""), now()),
		optional("lastMessageAt", dateTimeScalar, "When its latest message came or went."),
	}, []ir.IndexDef{
		{Keys: []string{"channelKind", "address"}, Name: "address"},
		{Keys: []string{"party", "channelKind"}, Name: "party"},
	}
}

// messageFields are a message's: one inbound or outbound message of a
// conversation, what became of it, and who sent it for whom.
func messageFields() ([]*ir.FieldDef, []ir.IndexDef) {
	return []*ir.FieldDef{
		keyField(),
		required("conversation", uuidScalar, "The conversation it is a message of."),
		required("direction", "string", "inbound or outbound."),
		required("via", "string", "The kind of channel it came or went on: sms, email, chat."),
		optional("slot", "string", "The provider slot that received or sent it (D159)."),
		required("fromAddress", "string", "The address it came from, normalised; the deployment's for an outbound one."),
		optional("toAddress", "string", "The address it went to; the deployment's inbound address for an inbound email."),
		optional("subject", "string", "Its subject, for a channel that shows one."),
		required("body", "string", "Its text as it came or went."),
		optional("messageId", "string", "Its id in the channel's threads: an email's Message-ID."),
		optional("inReplyTo", "string", "The id of the message it answers: an email's In-Reply-To, or the service's id of the message a text replies to."),
		{Name: "threadRefs", Comment: "The ids of the messages of its thread before it: an email's References.", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
		optional("providerMessageId", "string", "The service's id of the message."),
		optional("authentication", jsonScalar, "What the adapter reported of an email's authentication: { spf, dkim, dmarc }, each pass, fail, softfail, neutral or none."),
		optional("identity", uuidScalar, "The channel identity its sender resolved through; null for an unknown sender."),
		optional("trust", "string", "How that identity was confirmed when it came: verified, or entered by hand."),
		optional("partyTable", "string", "The table of the party it came from or went to."),
		optional("party", uuidScalar, "The party row's key."),
		optional("author", "string", "The principal that sent an outbound message: a user's, an agent run's."),
		optional("authorKind", "string", "Its kind: user, agent."),
		optional("forSubject", "string", "On whose behalf it was sent: the subject the author acted for; null when it acted for itself."),
		optional("disposition", "string", "What became of an inbound message: run, timeline, unknown, lapsed, optOut or verified."),
		optional("state", "string", "Where an outbound message stands: queued, sent, failed, refused or awaitingApproval."),
		optional("triage", "string", "open while it is held in triage, closed once someone settled it; null for one never held."),
		optional("triageClosedBy", "string", "Who settled it."),
		optional("triageClosedAt", dateTimeScalar, "When."),
		optional("grant", uuidScalar, "The grant an inbound message's run request was made under."),
		optional("detail", "string", "Why it went where it went: an authentication that failed, a refusal."),
		withDefault(required("at", dateTimeScalar, "When it came, or when it was sent."), now()),
		withDefault(required("createdAt", dateTimeScalar, ""), now()),
	}, []ir.IndexDef{
		{Keys: []string{"conversation", "at"}, Name: "conversation"},
		{Keys: []string{"messageId"}, Name: "message_id"},
		{Keys: []string{"providerMessageId"}, Name: "provider_message"},
		{Keys: []string{"triage"}, Name: "triage"},
	}
}

// runRequestFields are a run request's: what started it, and for an
// inbound message the party, the message and the grant (D157). An agent
// run (D158) takes it.
func runRequestFields() []*ir.FieldDef {
	return []*ir.FieldDef{
		required("trigger", "string", "What asked for the run: inbound, for a message from a party with an active grant."),
		optional("party", jsonScalar, "The party the run acts for: { table, key }."),
		optional("message", uuidScalar, "The inbound message that asked for it."),
		optional("grant", uuidScalar, "The party's grant it acts under: the one the message's thread ran under, else the party's newest active grant."),
	}
}

func keyField() *ir.FieldDef {
	return &ir.FieldDef{Name: "id", TypeRef: ir.TypeRef{Name: uuidScalar}, Required: true, Key: true, AutoGenerated: true}
}

func required(name, typeName, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}, Required: true}
}

func optional(name, typeName, comment string) *ir.FieldDef {
	return &ir.FieldDef{Name: name, Comment: comment, TypeRef: ir.TypeRef{Name: typeName}}
}

func withDefault(fd *ir.FieldDef, value *string) *ir.FieldDef {
	fd.Default = value
	return fd
}

func now() *string {
	s := codegen.DefaultNow
	return &s
}
