package ir

import (
	"fmt"
	"regexp"
	"slices"
	"sort"
)

// Events, notifications and channels (D153).
//
// A DB table declares the events its rows raise with @event: a kind, the
// fields of the row its payload carries, the field a notice is titled by,
// and a default urgency. An operation, a handler or a behavior raises one
// in the transaction of the change that caused it. The notifier, a
// runtime of the core, routes each event to its recipients by the rows of
// a route table, holds each recipient to the record's audience at send
// time, and delivers it per recipient through the channel adapters an
// environment configures (D159), over the rows of a preference table.
//
// The notifier's tables are classes the app declares and the loader
// fills, one class per part, each marked @notifier("<part>"): the inbox
// (one notice per recipient and event, the in-app inbox), the deliveries
// (one row per recipient, channel and attempt), the routes, the
// preferences, the subscriptions (digests and briefs), the trust of each
// channel kind, the replies (the audit of every message a principal sent
// back), and the dispatch queue, a D80 queue the notifier's worker drains.
//
// A class marked @channelIdentities is the table of channel identities:
// an address on a channel (an email address, a phone number, a messaging
// handle) and the principal it resolves to, verified by a code sent to it
// and replied with or entered by hand at a lower trust. A reply on a
// verified identity runs as its principal (ground rule 5); section 7
// (D157) resolves an identity to a party row through the same columns.

// EventDef is one @event of a DB table (D153).
//
// In TypeScript: @event<T>({ kind, payload?, title?, urgency?, description? })
type EventDef struct {
	// Kind names the event: dotted lower-camel words, ticket.assigned. A
	// kind a behavior of the table emits (D104) is its use's member, a
	// dot and the behavior's kind (recurrence.missed), which an @event
	// may declare to set its urgency and title.
	Kind string `json:"kind" yaml:"kind"`

	// Payload are the fields of the row the event's payload carries, as
	// the row holds them when the event is raised. Empty carries none.
	Payload []string `json:"payload,omitempty" yaml:"payload,omitempty"`

	// Title is the field of the row a notice of the event is titled by;
	// empty titles it by the kind and the row's key.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// Urgency is the event's default urgency, one of Urgencies; empty is
	// UrgencyNormal. A route may override it.
	Urgency string `json:"urgency,omitempty" yaml:"urgency,omitempty"`

	// Description says what the event means, for a reader of the routes.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`
}

// The urgency of an event (D153).
const (
	// UrgencyUrgent is sent now, ignores quiet hours, and needs an
	// acknowledgement: unacknowledged, it follows its route's escalation.
	UrgencyUrgent = "urgent"
	// UrgencyNormal is sent now, or at the end of the recipient's quiet
	// hours.
	UrgencyNormal = "normal"
	// UrgencyDigest is batched into the recipient's next digest.
	UrgencyDigest = "digest"
	// UrgencyRecord is history alone: a notice in the inbox, nothing sent.
	UrgencyRecord = "record"
)

// Urgencies are the urgencies of an event, from the most pressing.
var Urgencies = []string{UrgencyUrgent, UrgencyNormal, UrgencyDigest, UrgencyRecord}

// EventKindPattern is the form of an event's kind: dotted lower-camel
// words, at least two (ticket.assigned).
var EventKindPattern = regexp.MustCompile(`^[a-z][A-Za-z0-9]*(\.[a-z][A-Za-z0-9]*)+$`)

// OriginNotifications marks a field or a type the loader added for the
// notifier (D153): a @notifier class's fields and a @channelIdentities
// class's.
const OriginNotifications = "notifications"

// NotifierDef marks a DB class that is one of the notifier's tables
// (D153), its part one of NotifierParts. The loader fills its fields.
type NotifierDef struct {
	Part string `json:"part" yaml:"part"`
}

// The parts of the notifier, each a class the app declares with
// @notifier("<part>").
const (
	// NotifierInbox is the in-app inbox: one notice per recipient and
	// event, numbered per recipient, which a reply names.
	NotifierInbox = "inbox"
	// NotifierDeliveries is one row per recipient, channel and attempt.
	NotifierDeliveries = "deliveries"
	// NotifierRoutes is the routes: which events reach whom, how urgent,
	// and how an unacknowledged one escalates.
	NotifierRoutes = "routes"
	// NotifierPreferences is each principal's channels, quiet hours, time
	// zone and digest schedule by event kind and urgency.
	NotifierPreferences = "preferences"
	// NotifierSubscriptions is the digests and briefs: a saved query, a
	// recurrence rule, a recipient and its channels.
	NotifierSubscriptions = "subscriptions"
	// NotifierChannelTrust is the trust level of each channel kind and the
	// permissions a reply on it may use.
	NotifierChannelTrust = "channelTrust"
	// NotifierReplies is the audit of each message a principal sent back:
	// its channel, what interpreted it, what it ran, and its undo.
	NotifierReplies = "replies"
	// NotifierDispatch is the D80 queue the notifier's worker drains: an
	// event to route, a delivery to send, a timeout, an escalation, a
	// digest or a subscription's brief, each due when its message is.
	NotifierDispatch = "dispatch"
)

// NotifierParts are the parts of the notifier, in order.
var NotifierParts = []string{NotifierInbox, NotifierDeliveries, NotifierRoutes, NotifierPreferences, NotifierSubscriptions, NotifierChannelTrust, NotifierReplies, NotifierDispatch}

// NotifierRequiredParts are the parts a schema that uses the notifier
// declares: what routing and sending an event write.
var NotifierRequiredParts = []string{NotifierInbox, NotifierDeliveries, NotifierRoutes, NotifierDispatch}

// ChannelIdentitiesDef marks the DB class that is the table of channel
// identities (D153). The loader fills its fields. A schema has at most
// one.
type ChannelIdentitiesDef struct{}

// The fields of a @channelIdentities table, which the loader adds.
const (
	// ChannelIdentityKindField is the kind of address: one of
	// AddressKinds.
	ChannelIdentityKindField = "kind"
	// ChannelIdentityAddressField is the address, normalised: an email
	// address in lower case, a phone number as + and its digits, a handle
	// as its adapter writes it.
	ChannelIdentityAddressField = "address"
	// ChannelIdentityPrincipalKindField is the kind of principal the
	// address resolves to: PrincipalUser, or PrincipalParty once D157
	// adds parties.
	ChannelIdentityPrincipalKindField = "principalKind"
	// ChannelIdentityPrincipalTableField is the table of a party the
	// address resolves to, by its type's name; null for a user.
	ChannelIdentityPrincipalTableField = "principalTable"
	// ChannelIdentityPrincipalField is the principal's id: the user's, or
	// the party row's key.
	ChannelIdentityPrincipalField = "principal"
	// ChannelIdentityTrustField is how the address was confirmed: one of
	// IdentityTrusts.
	ChannelIdentityTrustField = "trust"
	// ChannelIdentityVerifiedAtField is when it was verified or entered.
	ChannelIdentityVerifiedAtField = "verifiedAt"
	// ChannelIdentityCodeHashField, ChannelIdentityCodePurposeField,
	// ChannelIdentityCodeExpiresAtField and ChannelIdentityCodeAttemptsField
	// hold a code sent to the address and not yet replied with: its
	// SHA-256, what it is for (verify, reset), when it expires and how
	// many wrong answers it has had.
	ChannelIdentityCodeHashField      = "codeHash"
	ChannelIdentityCodePurposeField   = "codePurpose"
	ChannelIdentityCodeExpiresAtField = "codeExpiresAt"
	ChannelIdentityCodeAttemptsField  = "codeAttempts"
	// ChannelIdentityCreatedAtField and ChannelIdentityRevokedAtField are
	// when the identity was added and when it stopped resolving.
	ChannelIdentityCreatedAtField = "createdAt"
	ChannelIdentityRevokedAtField = "revokedAt"
)

// The kinds of address a channel identity holds.
const (
	AddressEmail  = "email"
	AddressPhone  = "phone"
	AddressHandle = "handle"
)

// AddressKinds are the kinds of address, in order.
var AddressKinds = []string{AddressEmail, AddressPhone, AddressHandle}

// How a channel identity was confirmed.
const (
	// IdentityUnverified is an address added and not yet confirmed: it
	// resolves to no one.
	IdentityUnverified = "unverified"
	// IdentityVerified is an address that replied with the code sent to
	// it.
	IdentityVerified = "verified"
	// IdentityEntered is an address entered by hand by someone with the
	// permission to: it resolves, and a reply on it runs at the lowest
	// trust.
	IdentityEntered = "entered"
)

// IdentityTrusts are how a channel identity may be confirmed, in order.
var IdentityTrusts = []string{IdentityUnverified, IdentityVerified, IdentityEntered}

// The levels of trust a channel kind may have (the channelTrust table's
// level). A level is a label; what a reply may run is the row's
// permissions.
const (
	TrustLow      = "low"
	TrustStandard = "standard"
	TrustHigh     = "high"
)

// TrustLevels are the levels, from the least.
var TrustLevels = []string{TrustLow, TrustStandard, TrustHigh}

// The states of a notice in the inbox.
const (
	NoticeUnread       = "unread"
	NoticeRead         = "read"
	NoticeAcknowledged = "acknowledged"
	NoticeDone         = "done"
)

// The states of a delivery: queued, then sent, delivered and read, or it
// ends failed or acknowledged.
const (
	DeliveryQueued       = "queued"
	DeliverySent         = "sent"
	DeliveryDelivered    = "delivered"
	DeliveryRead         = "read"
	DeliveryFailed       = "failed"
	DeliveryAcknowledged = "acknowledged"
)

// DeliveryStates are the states of a delivery, in order.
var DeliveryStates = []string{DeliveryQueued, DeliverySent, DeliveryDelivered, DeliveryRead, DeliveryFailed, DeliveryAcknowledged}

// EventTables returns the schema's tables that declare an @event, in name
// order.
func (s *Schema) EventTables() []*TypeDef {
	var out []*TypeDef
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && len(td.Events) > 0 {
			out = append(out, td)
		}
	}
	return out
}

// NotifierTable returns the schema's class marked @notifier(part), or nil.
func (s *Schema) NotifierTable(part string) *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.Notifier != nil && td.Notifier.Part == part {
			return td
		}
	}
	return nil
}

// ChannelIdentitiesTable returns the schema's @channelIdentities class, or
// nil.
func (s *Schema) ChannelIdentitiesTable() *TypeDef {
	for _, name := range s.typeNames() {
		if td := s.Types[name]; td != nil && td.ChannelIdentities != nil {
			return td
		}
	}
	return nil
}

// UsesNotifications reports whether the DB schema uses the notifier: it
// declares an @event, a @notifier class or a @channelIdentities class. An
// API over such a database needs a channel adapter (D159's coverage).
func (s *Schema) UsesNotifications() bool {
	if s == nil {
		return false
	}
	for _, td := range s.Types {
		if td != nil && (len(td.Events) > 0 || td.Notifier != nil || td.ChannelIdentities != nil) {
			return true
		}
	}
	return false
}

// EventOf returns td's @event of kind, or nil.
func (td *TypeDef) EventOf(kind string) *EventDef {
	for _, event := range td.Events {
		if event != nil && event.Kind == kind {
			return event
		}
	}
	return nil
}

// validateEvents checks what td's @event declarations alone decide: each
// kind's form, each urgency a known one, no kind twice. What needs the
// fields (that payload and title name fields of the table) is the loader's
// verification pass's.
func validateEvents(td *TypeDef) []error {
	var errs []error
	seen := map[string]bool{}
	for _, event := range td.Events {
		if event == nil {
			continue
		}
		if !EventKindPattern.MatchString(event.Kind) {
			errs = append(errs, fmt.Errorf("%s: @event kind %q is dotted lower-camel words, at least two, as ticket.assigned", td.Name, event.Kind))
		}
		if seen[event.Kind] {
			errs = append(errs, fmt.Errorf("%s: @event kind %q is declared twice", td.Name, event.Kind))
		}
		seen[event.Kind] = true
		if event.Urgency != "" && !slices.Contains(Urgencies, event.Urgency) {
			errs = append(errs, fmt.Errorf("%s: @event %s: urgency %q is none of %v", td.Name, event.Kind, event.Urgency, Urgencies))
		}
	}
	if td.Notifier != nil && !slices.Contains(NotifierParts, td.Notifier.Part) {
		errs = append(errs, fmt.Errorf("%s: @notifier part %q is none of %v", td.Name, td.Notifier.Part, NotifierParts))
	}
	if len(td.Events) > 0 && (td.Notifier != nil || td.ChannelIdentities != nil) {
		errs = append(errs, fmt.Errorf("%s: @event marks a table of the application's rows, and %s is the notifier's own", td.Name, td.Name))
	}
	if td.Notifier != nil && td.ChannelIdentities != nil {
		errs = append(errs, fmt.Errorf("%s: a class is one of the notifier's tables or the channel identities, not both", td.Name))
	}
	return errs
}

// NotifierPartsOf returns the parts the schema declares, sorted.
func (s *Schema) NotifierPartsOf() []string {
	var parts []string
	for _, td := range s.Types {
		if td != nil && td.Notifier != nil {
			parts = append(parts, td.Notifier.Part)
		}
	}
	sort.Strings(parts)
	return parts
}
