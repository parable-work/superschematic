package verify

import (
	ir "github.com/parable-work/superschematic/ir"
)

// checkParties checks a schema's parties, conversations and run requests
// (D157), once the conversations expansion filled the classes' fields.
//
// A @party is a table of the database with one key, a UUID, which a grant
// names as its holder and a channel identity as its principal; its title
// names a field of it. Each conversations part is declared once, a table,
// and a schema that declares one declares both, beside the channel
// identities (D153) its senders resolve through. The conversations class
// takes nothing but its fields; the messages class may take @references
// and @attachments, its references and its files. A @runRequests class is
// a @queue, once. On Postgres, SQLite and D1 in TypeScript, as the notifier
// is (D189).
func checkParties(schema *ir.Schema, in Input, r *Result) {
	if !schema.UsesParties() {
		return
	}
	var first *ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		if td := schema.Types[name]; td != nil && (td.Party != nil || td.Conversations != nil || td.RunRequests != nil) {
			first = td
			break
		}
	}
	if schema.Kind != ir.SchemaKindDB {
		r.errorf(first.Owner, "%s: parties, conversations and run requests are a DB service's, and %s is a %s service (D157)", first.Name, schema.Name, schema.Kind)
		return
	}
	parts := map[string]*ir.TypeDef{}
	var runs *ir.TypeDef
	for _, name := range sortedTypeNames(schema.Types) {
		td := schema.Types[name]
		if td == nil {
			continue
		}
		switch {
		case td.Party != nil:
			checkParty(schema, td, r)
		case td.Conversations != nil:
			decorator := `@conversations("` + td.Conversations.Part + `")`
			if other := parts[td.Conversations.Part]; other != nil {
				r.errorf(td.Owner, "%s marks %s and %s; a schema has one", decorator, other.Name, td.Name)
				continue
			}
			parts[td.Conversations.Part] = td
			switch {
			case td.Role != ir.RoleDBTable || td.Queue != nil:
				r.errorf(td.Owner, "%s on %s: the class is a table of the database, and no queue", decorator, td.Name)
			case td.Audience != nil || len(td.TableBehaviors()) > 0 || len(td.Events) > 0:
				r.errorf(td.Owner, "%s on %s: the class is the core's own, and takes no @audience, @event or behavior", decorator, td.Name)
			case td.Conversations.Part == ir.ConversationsPart && (td.RowReferences != nil || td.Attachments != nil):
				r.errorf(td.Owner, "%s on %s: a conversation's references and files are its messages'; put @references and @attachments on the @conversations(\"messages\") class", decorator, td.Name)
			}
			ownFields(td, decorator, r)
		case td.RunRequests != nil:
			if runs != nil {
				r.errorf(td.Owner, "@runRequests marks %s and %s; a schema has one", runs.Name, td.Name)
				continue
			}
			runs = td
			if td.Queue == nil {
				r.errorf(td.Owner, "@runRequests on %s: the class is a queue; mark it @queue() too", td.Name)
			}
			ownFields(td, "@runRequests", r)
		}
	}
	if len(parts) > 0 {
		for _, part := range ir.ConversationParts {
			if parts[part] == nil {
				r.errorf(first.Owner, "%s declares the conversations (D157), and no @conversations(%q): an inbound message writes both; declare an empty class for each", schema.Name, part)
			}
		}
		if schema.ChannelIdentitiesTable() == nil {
			r.errorf(first.Owner, "%s declares the conversations (D157), and no @channelIdentities table, which a sender resolves through (D153)", schema.Name)
		}
	}
	if len(schema.PartyTables()) > 0 && schema.AccessGrantsTable() == nil {
		r.errorf(first.Owner, "%s declares a @party table, and no @accessGrants table: a party acts through grants alone (D156, D157)", schema.Name)
	}
}

// checkParty holds a @party table to what a party is: a table of the
// database with one UUID key, which grants and channel identities name.
func checkParty(schema *ir.Schema, td *ir.TypeDef, r *Result) {
	fail := func(format string, args ...any) {
		r.errorf(td.Owner, "@party on %s: "+format, append([]any{td.Name}, args...)...)
	}
	if td.Role != ir.RoleDBTable || td.Queue != nil || td.Projection != nil {
		fail("a party is a row of a table of the database, and %s is none", td.Name)
		return
	}
	var key *ir.FieldDef
	for _, fd := range td.Fields {
		if fd != nil && fd.Key {
			if key != nil {
				fail("%s has more than one key field; a grant and a channel identity name a party by one UUID", td.Name)
				return
			}
			key = fd
		}
	}
	if key == nil || !isUUIDField(schema, key) {
		fail("%s's key is no UUID; a grant's holder and a channel identity's principal name a party by one", td.Name)
	}
	if title := td.Party.Title; title != "" && fieldNamed(td, title) == nil {
		fail("title %s is no field of %s", title, td.Name)
	}
}

// ownFields refuses a class the build fills that declares a field of its
// own.
func ownFields(td *ir.TypeDef, decorator string, r *Result) {
	for _, fd := range td.Fields {
		if fd != nil && fd.Origin != ir.OriginConversations {
			r.errorf(td.Owner, "%s on %s: the class declares %s, and the build fills its fields; leave the class empty", decorator, td.Name, fd.Name)
			return
		}
	}
}
