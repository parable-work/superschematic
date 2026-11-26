// Package notifications expands a DB schema's notifier declarations
// (D153) into ordinary fields, as the access expansion does for the grants
// table: the fields of each class marked @notifier("<part>") and of the
// class marked @channelIdentities, when it declares none. The sql, orm and
// types generators emit the result as any table's or queue's.
//
// It runs before the verification pass, so the pass reads the added fields
// as the class's own. Every field it adds carries ir.OriginNotifications,
// so the schema writer can skip it and write the declaration instead.
package notifications

import (
	"github.com/parable-work/superschematic/internal/generator/codegen"
	ir "github.com/parable-work/superschematic/ir"
)

// Scalars the added fields use.
const (
	uuidScalar     = "Identity.UUID"
	dateTimeScalar = "Temporal.DateTime"
	int64Scalar    = "Generic.Int64"
	jsonScalar     = "Generic.JSON"
)

// Expand fills the fields of every notifier class and of the channel
// identities class that declares none. It returns the scalar names the
// added fields use, so the caller can hydrate any the schema did not
// already reference.
func Expand(schema *ir.Schema) []string {
	used := false
	for _, name := range sortedTypes(schema) {
		td := schema.Types[name]
		if td == nil || len(td.Fields) > 0 {
			continue
		}
		var fields []*ir.FieldDef
		var indexes []ir.IndexDef
		switch {
		case td.ChannelIdentities != nil:
			fields, indexes = identityFields()
		case td.Notifier != nil:
			fields, indexes = partFields(td.Notifier.Part, td.Queue != nil)
		}
		if fields == nil {
			continue
		}
		fill(td, fields, indexes)
		used = true
	}
	if !used {
		return nil
	}
	scalars := []string{uuidScalar, dateTimeScalar, int64Scalar, jsonScalar}
	for _, name := range scalars {
		if schema.Scalars[name] == nil {
			schema.Scalars[name] = &ir.ScalarDef{Name: name}
		}
	}
	return scalars
}

// ExpandExternal fills the notifier's classes of another service the schema
// reads (a DB dependency's, which a @source view names), as the
// dependency's own load fills them, so a view may hold their fields.
func ExpandExternal(types map[string]*ir.TypeDef) {
	for _, td := range types {
		if td == nil || len(td.Fields) > 0 {
			continue
		}
		switch {
		case td.ChannelIdentities != nil:
			fields, indexes := identityFields()
			fill(td, fields, indexes)
		case td.Notifier != nil:
			if fields, indexes := partFields(td.Notifier.Part, td.Queue != nil); fields != nil {
				fill(td, fields, indexes)
			}
		}
	}
}

func fill(td *ir.TypeDef, fields []*ir.FieldDef, indexes []ir.IndexDef) {
	for _, fd := range fields {
		fd.Origin = ir.OriginNotifications
	}
	for i := range indexes {
		indexes[i].Origin = ir.OriginNotifications
	}
	td.Fields = fields
	td.Indexes = append(td.Indexes, indexes...)
}

// partFields are the fields and indexes of the notifier's part. The
// dispatch part is a @queue class; every other part a table, whose key
// the fields begin with. A part of the wrong role gets no fields, which
// the verification pass refuses.
func partFields(part string, queue bool) ([]*ir.FieldDef, []ir.IndexDef) {
	if (part == ir.NotifierDispatch) != queue {
		return nil, nil
	}
	switch part {
	case ir.NotifierInbox:
		return inboxFields()
	case ir.NotifierDeliveries:
		return deliveryFields()
	case ir.NotifierRoutes:
		return routeFields()
	case ir.NotifierPreferences:
		return preferenceFields()
	case ir.NotifierSubscriptions:
		return subscriptionFields()
	case ir.NotifierChannelTrust:
		return trustFields()
	case ir.NotifierReplies:
		return replyFields()
	case ir.NotifierDispatch:
		return dispatchFields(), nil
	}
	return nil, nil
}

// identityFields are a channel identity's: an address on a channel and
// the principal it resolves to, by kind, table and id, so a party row
// (D157) is one as a user is.
func identityFields() ([]*ir.FieldDef, []ir.IndexDef) {
	unverified := ir.IdentityUnverified
	user := ir.PrincipalUser
	zero := "0"
	return []*ir.FieldDef{
		keyField(),
		required(ir.ChannelIdentityKindField, "string", "The kind of address: email, phone or handle."),
		required(ir.ChannelIdentityAddressField, "string", "The address, normalised: an email address in lower case, a phone number as + and its digits, a handle as its adapter writes it."),
		withDefault(required(ir.ChannelIdentityPrincipalKindField, "string", "The kind of principal the address resolves to: user, or party."), &user),
		optional(ir.ChannelIdentityPrincipalTableField, "string", "The table of the party the address resolves to, by its type's name; null for a user."),
		required(ir.ChannelIdentityPrincipalField, uuidScalar, "The principal's id: the user's, or the party row's key."),
		withDefault(required(ir.ChannelIdentityTrustField, "string", "How the address was confirmed: unverified, verified (it replied with the code sent to it) or entered (by hand, at a lower trust)."), &unverified),
		optional(ir.ChannelIdentityVerifiedAtField, dateTimeScalar, "When it was verified or entered."),
		optional(ir.ChannelIdentityCodeHashField, "string", "The SHA-256 of a code sent to the address and not yet answered."),
		optional(ir.ChannelIdentityCodePurposeField, "string", "What the code is for: verify, or reset (a password reset, D81)."),
		optional(ir.ChannelIdentityCodeExpiresAtField, dateTimeScalar, "When the code expires."),
		withDefault(required(ir.ChannelIdentityCodeAttemptsField, int64Scalar, "How many wrong answers the code has had."), &zero),
		withDefault(required(ir.ChannelIdentityCreatedAtField, dateTimeScalar, "When the identity was added."), now()),
		optional(ir.ChannelIdentityRevokedAtField, dateTimeScalar, "When it stopped resolving to its principal."),
		optional(ir.ChannelIdentityOptedOutAtField, dateTimeScalar, "When the address opted out, by a keyword its adapter read (D157): nothing is sent to it after, until it opts in again."),
	}, []ir.IndexDef{
		{Keys: []string{ir.ChannelIdentityKindField, ir.ChannelIdentityAddressField}, Name: "address"},
		{Keys: []string{ir.ChannelIdentityPrincipalField}, Name: "principal"},
	}
}

// inboxFields are a notice's: one per recipient and event, numbered per
// recipient by its reference, the in-app inbox (D153).
func inboxFields() ([]*ir.FieldDef, []ir.IndexDef) {
	unread := ir.NoticeUnread
	user := ir.PrincipalUser
	zero := "0"
	return []*ir.FieldDef{
		keyField(),
		required("recipient", uuidScalar, "The principal the notice is for."),
		withDefault(required("recipientKind", "string", "Its kind: user, or party."), &user),
		required("reference", int64Scalar, "The notice's number among its recipient's, which a reply names: done 142."),
		required("eventId", uuidScalar, "The event the notice is of; a recipient has one notice of an event."),
		required("eventKind", "string", "The event's kind."),
		required("urgency", "string", "urgent, normal, digest or record."),
		optional("subjectTable", "string", "The table of the record the event is about, by its type's name; null for an event of no record."),
		optional("subjectKey", "string", "The record's key as text."),
		required("title", "string", "What the notice says in a line."),
		optional("body", "string", "What it says beyond its title."),
		optional("payload", jsonScalar, "The event's payload."),
		optional("route", uuidScalar, "The route that named the recipient; null for a notice sent directly."),
		withDefault(required("state", "string", "unread, read, acknowledged or done."), &unread),
		withDefault(required("escalation", int64Scalar, "How many steps of its route's escalation have run."), &zero),
		withDefault(required("createdAt", dateTimeScalar, ""), now()),
		optional("readAt", dateTimeScalar, "When its recipient read it."),
		optional("acknowledgedAt", dateTimeScalar, "When its recipient acknowledged it."),
		optional("acknowledgedVia", "string", "The channel kind it was acknowledged on: inapp, sms, email."),
		optional("digestedAt", dateTimeScalar, "When a digest carried it; null for one not batched or not yet sent."),
	}, []ir.IndexDef{
		{Keys: []string{"recipient", "reference"}, Unique: true, Name: "reference"},
		{Keys: []string{"eventId", "recipient"}, Unique: true, Name: "event"},
		{Keys: []string{"recipient", "state"}, Name: "waiting"},
	}
}

// deliveryFields are a delivery's: one per recipient, channel and attempt,
// queued, sent, delivered and read, or ending failed or acknowledged.
func deliveryFields() ([]*ir.FieldDef, []ir.IndexDef) {
	queued := ir.DeliveryQueued
	notice := "notice"
	one := "1"
	return []*ir.FieldDef{
		keyField(),
		optional("notice", uuidScalar, "The notice it delivers; null for a message of no notice: a reply's echo, a code."),
		withDefault(required("purpose", "string", "notice, echo, alert, code or digest."), &notice),
		optional("recipient", uuidScalar, "The principal it is for; null for an address no principal holds yet."),
		required("channelKind", "string", "The kind of channel: inapp, sms, email, chat."),
		optional("slot", "string", "The provider slot it was sent through (D159); null for the in-app inbox."),
		optional("address", "string", "The address it was sent to."),
		withDefault(required("attempt", int64Scalar, "Which attempt of its notice it is, from 1."), &one),
		withDefault(required("state", "string", "queued, sent, delivered, read, failed or acknowledged."), &queued),
		optional("providerMessageId", "string", "The adapter's service's id of the message, which its statuses and replies name."),
		optional("detail", "string", "Why it failed, or what the service said."),
		withDefault(required("queuedAt", dateTimeScalar, ""), now()),
		optional("dueAt", dateTimeScalar, "When it is to be sent: later than queuedAt in quiet hours."),
		optional("sentAt", dateTimeScalar, ""),
		optional("deliveredAt", dateTimeScalar, ""),
		optional("readAt", dateTimeScalar, ""),
		optional("failedAt", dateTimeScalar, ""),
		optional("acknowledgedAt", dateTimeScalar, ""),
		optional("endedAt", dateTimeScalar, "When it reached a state it leaves no more, which its retention counts from."),
	}, []ir.IndexDef{
		{Keys: []string{"notice"}, Name: "notice"},
		{Keys: []string{"providerMessageId"}, Name: "provider_message"},
		{Keys: []string{"endedAt"}, Name: "ended"},
	}
}

// routeFields are a route's: an event kind and a filter on the record, its
// recipients, an urgency that overrides the event's, and an escalation.
func routeFields() ([]*ir.FieldDef, []ir.IndexDef) {
	on := "true"
	return []*ir.FieldDef{
		keyField(),
		required("name", "string", "What the route is, for whoever edits it."),
		required("eventKind", "string", "The event kind it matches, or * for every kind."),
		optional("subjectTable", "string", "The table whose events it matches, by its type's name; null for any."),
		optional("filter", jsonScalar, "A filter on the record, in the repositories' filter syntax: { status: { eq: \"open\" } }; null for every record."),
		required("recipients", jsonScalar, "Who it reaches: a list of { path }, a relation path of the record to principals (assignees, project.owner); { principal }, a user's id; { role }, the users holding a role; or { party: { table, key } } (D157)."),
		optional("urgency", "string", "The urgency it sends at in place of the event's; null for the event's."),
		optional("escalation", jsonScalar, "What an urgent notice not acknowledged in time does: a list of steps { afterSeconds, channels?, recipients? }, each after the one before."),
		withDefault(required("enabled", "boolean", "Whether it routes."), &on),
		withDefault(required("createdAt", dateTimeScalar, ""), now()),
		optional("createdBy", uuidScalar, "Who made it."),
	}, []ir.IndexDef{
		{Keys: []string{"eventKind"}, Name: "event_kind"},
	}
}

// preferenceFields are a principal's preference for an event kind and an
// urgency: the channels in the order they fall through, quiet hours, a time
// zone and a digest schedule. A row of no principal is the default, seed
// data the app writes.
func preferenceFields() ([]*ir.FieldDef, []ir.IndexDef) {
	any := "*"
	return []*ir.FieldDef{
		keyField(),
		optional("principal", uuidScalar, "Whose preference it is; null for the default every principal without one of their own takes."),
		withDefault(required("eventKind", "string", "The event kind it is for, or * for every kind."), &any),
		withDefault(required("urgency", "string", "The urgency it is for, or * for every one."), &any),
		{Name: "channels", Comment: "The channel kinds a notice goes out on, in the order a failure falls through: sms, email, chat; inapp is the inbox, which every notice reaches.", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true},
		optional("quietStart", "string", "When quiet hours start, HH:MM in the time zone; null for none."),
		optional("quietEnd", "string", "When they end, HH:MM."),
		optional("timeZone", "string", "The IANA time zone quiet hours and digests are read in; UTC when null."),
		optional("digest", "string", "The RRULE a digest goes out on (FREQ=DAILY;BYHOUR=8;BYMINUTE=0); null sends digest notices at once."),
	}, []ir.IndexDef{
		{Keys: []string{"principal"}, Name: "principal"},
	}
}

// subscriptionFields are a digest's or a brief's: a saved query, a
// recurrence rule (D152), a recipient and its channels.
func subscriptionFields() ([]*ir.FieldDef, []ir.IndexDef) {
	on := "true"
	return []*ir.FieldDef{
		keyField(),
		required("name", "string", "What the brief is called."),
		required("principal", uuidScalar, "Whom it goes to; the query runs as them."),
		required("subjectTable", "string", "The table it reads, by its type's name."),
		optional("filter", jsonScalar, "The saved query: a filter in the repositories' syntax; null for every row the principal reads."),
		optional("titleField", "string", "The field each row is listed by; the key when null."),
		required("rule", "string", "The RRULE it goes out on (D152)."),
		required("start", "string", "The wall date-time the rule starts from: 2026-01-05T08:00."),
		required("timeZone", "string", "The IANA time zone the rule is read in."),
		{Name: "channels", Comment: "The channel kinds it goes out on.", TypeRef: ir.TypeRef{Name: "string", IsArray: true}, Required: true},
		withDefault(required("enabled", "boolean", "Whether it goes out."), &on),
		optional("nextAt", dateTimeScalar, "When it next goes out."),
		optional("lastSentAt", dateTimeScalar, "When it last went out."),
		withDefault(required("createdAt", dateTimeScalar, ""), now()),
	}, nil
}

// trustFields are a channel kind's trust: its level, and the permissions a
// reply on it may use, of those its principal holds.
func trustFields() ([]*ir.FieldDef, []ir.IndexDef) {
	return []*ir.FieldDef{
		keyField(),
		{Name: "channelKind", Comment: "The kind of channel: sms, email, chat.", TypeRef: ir.TypeRef{Name: "string"}, Required: true, Unique: true},
		required("level", "string", "low, standard or high."),
		{Name: "permissions", Comment: "The permissions a reply on the channel may use, of those its principal holds; null for all of them. Acknowledging a notice needs none.", TypeRef: ir.TypeRef{Name: "string", IsArray: true}},
	}, nil
}

// replyFields are a reply's audit: what came in, on which channel and from
// whom, what interpreted it, what it ran, and how to undo it.
func replyFields() ([]*ir.FieldDef, []ir.IndexDef) {
	return []*ir.FieldDef{
		keyField(),
		withDefault(required("receivedAt", dateTimeScalar, ""), now()),
		required("channelKind", "string", "The kind of channel it came on."),
		optional("slot", "string", "The provider slot that received it."),
		required("address", "string", "The sender's address, normalised."),
		optional("providerMessageId", "string", "The service's id of the message."),
		optional("identity", uuidScalar, "The channel identity it resolved through; null for an unknown sender."),
		optional("principal", uuidScalar, "The principal it ran as."),
		required("body", "string", "The text as it came."),
		required("interpreter", "string", "What read the text: keyword, the core's own reader, or the name of what the app plugged in."),
		optional("verb", "string", "What the text asked: ack, done, undo, a code."),
		optional("notice", uuidScalar, "The notice it answered."),
		optional("reference", int64Scalar, "The reference it named."),
		required("outcome", "string", "done, refused, unknown, ambiguous or failed."),
		optional("detail", "string", "Why, for an outcome that is not done."),
		optional("undo", jsonScalar, "What undoing it needs; null for what cannot be undone."),
		optional("undoneAt", dateTimeScalar, "When it was undone."),
	}, []ir.IndexDef{
		{Keys: []string{"principal", "receivedAt"}, Name: "principal"},
	}
}

// dispatchFields are a message of the notifier's queue: a step and what it
// is of, each due when its message is (D80).
func dispatchFields() []*ir.FieldDef {
	return []*ir.FieldDef{
		required("step", "string", "route, deliver, timeout, escalate, digest or brief."),
		optional("event", jsonScalar, "The event a route step routes."),
		optional("notice", uuidScalar, "The notice an escalate step is of."),
		optional("delivery", uuidScalar, "The delivery a deliver or timeout step is of."),
		optional("recipient", uuidScalar, "The principal a digest step is for."),
		optional("subscription", uuidScalar, "The subscription a brief step sends."),
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

func sortedTypes(schema *ir.Schema) []string {
	names := make([]string, 0, len(schema.Types))
	for name := range schema.Types {
		names = append(names, name)
	}
	for i := 1; i < len(names); i++ {
		for j := i; j > 0 && names[j] < names[j-1]; j-- {
			names[j], names[j-1] = names[j-1], names[j]
		}
	}
	return names
}
