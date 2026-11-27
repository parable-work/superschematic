package rustgen

import (
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// Events and notifications (D153) as the Rust repositories read them
// (D191): a table's @event declarations and what the table is to the
// notifier, members of the table in the Postgres definition (orm::Table's
// events and notifier), which the runtime's notify module reads. They hold
// what tsgen writes into orm.ts (ORMEvent, notifierPartOf), member for
// member, so the two runtimes raise, route and deliver over the same
// tables.

// rustEvent is orm::EventDefinition: an event's kind, its default urgency,
// the member a notice is titled by and the members its payload carries.
type rustEvent struct {
	Kind    string   `json:"kind"`
	Urgency string   `json:"urgency"`
	Title   string   `json:"title,omitempty"`
	Payload []string `json:"payload,omitempty"`
}

// eventsOfTable is td's events, each with its default urgency; nil for a
// table with none.
func eventsOfTable(td *ir.TypeDef) []rustEvent {
	if td == nil {
		return nil
	}
	var out []rustEvent
	for _, event := range td.Events {
		if event == nil {
			continue
		}
		urgency := event.Urgency
		if urgency == "" {
			urgency = ir.UrgencyNormal
		}
		out = append(out, rustEvent{Kind: event.Kind, Urgency: urgency, Title: event.Title, Payload: event.Payload})
	}
	return out
}

// notifierOfTable is what td is to the notifier: its @notifier part, or
// channelIdentities for the table of channel identities; "" for any other.
func notifierOfTable(td *ir.TypeDef) string {
	switch {
	case td == nil:
		return ""
	case td.ChannelIdentities != nil:
		return "channelIdentities"
	case td.Notifier != nil:
		return td.Notifier.Part
	}
	return ""
}

// refuseNotifierOnSQLite refuses the notifier for a crate that lists the
// sqlite dialect, as the loader refuses it for every language (D153): its
// worker enqueues and claims through the Postgres queue client alone.
func refuseNotifierOnSQLite(schema *ir.Schema, sqlite bool) error {
	if !sqlite || schema == nil || !schema.UsesNotifications() {
		return nil
	}
	return fmt.Errorf("rustgen: %s lists the sqlite dialect, and the notifier (D153) runs on Postgres alone so far; build it for postgres alone (D191)", schema.Name)
}

// rustParty is orm::PartyDefinition: the field a @party table's parties
// are named by (D157).
type rustParty struct {
	Title string `json:"title,omitempty"`
}

// partiesOfTable is what td is to D157 and D158, as tsgen writes them into
// orm.ts (party, conversations, agents): its @party with its title, its
// @conversations part, and its @agents part (D193).
func partiesOfTable(td *ir.TypeDef) (*rustParty, string, string) {
	if td == nil {
		return nil, "", ""
	}
	var party *rustParty
	if td.Party != nil {
		party = &rustParty{Title: td.Party.Title}
	}
	conversations, agents := "", ""
	if td.Conversations != nil {
		conversations = td.Conversations.Part
	}
	if td.Agents != nil {
		agents = td.Agents.Part
	}
	return party, conversations, agents
}
