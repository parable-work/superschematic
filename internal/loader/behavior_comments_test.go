package loader

import (
	"encoding/json"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixtures of D103's step 4's Comments: a database whose Ticket and
// Review each keep a conversation, and the API that serves Ticket's.
const (
	commentsDBFixture  = "tsreader/testdata/services/fixture-comments-db"
	commentsAPIFixture = "tsreader/testdata/services/fixture-comments-api"
)

// TestCommentsFixtureLoads: the fixture loads with no warning and no
// error; Ticket and Review stay tables that implement Comments, each
// config read as the TypeScript form wrote it; the loader adds no owned
// field, since Comments computes its fields at a read, and for each use its
// sub-tables messages, edits and reactions, each keyed by its key and
// related to its row, and its shared table conversations, keyed by the
// row's key with no relation and @optimistic's version, which a delete of
// the row leaves; no queue, since Comments arms no deadline, hears no
// write and emits no event; and the expanded IR is held to its golden
// (D103's step 4).
func TestCommentsFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(commentsDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	ticket, review := db.Types["Ticket"], db.Types["Review"]
	if ticket == nil || len(ticket.TypeBehaviors()) > 0 || !ticket.Optimistic || review == nil || !review.Optimistic {
		t.Fatalf("Ticket = %s, Review = %s", marshal(t, ticket), marshal(t, review))
	}
	if refs := ticket.Behaviors; len(refs) != 1 || refs[0].Name != "Comments" || refs[0].Field != "" ||
		!strings.Contains(string(refs[0].Config), `"moderatePermission":"tickets.moderate"`) || !strings.Contains(string(refs[0].Config), `"editWindowMs":900000`) {
		t.Errorf("Ticket's uses = %s", marshal(t, refs))
	}
	if refs := review.Behaviors; len(refs) != 1 || !strings.Contains(string(refs[0].Config), `"blocks":["ref","code"]`) {
		t.Errorf("Review's uses = %s", marshal(t, refs))
	}
	for name, td := range map[string]*ir.TypeDef{"Ticket": ticket, "Review": review} {
		for _, fd := range td.Fields {
			if fd.Owned() {
				t.Errorf("%s's owned field %s: Comments computes its fields at a read", name, fd.Name)
			}
		}
		for _, table := range []struct{ name, key string }{
			{name + "CommentsMessages", "message"}, {name + "CommentsEdits", "edit"}, {name + "CommentsReactions", "reaction"},
		} {
			sub := db.Types[table.name]
			if sub == nil || sub.Origin != ir.OriginBehavior || sub.BehaviorTable == nil || sub.BehaviorTable.RowField == "" {
				t.Fatalf("%s = %s", table.name, marshal(t, sub))
			}
			if key := sub.Fields[1]; key.Name != table.key {
				t.Errorf("%s's key = %s, want %s", table.name, marshal(t, key), table.key)
			}
		}
		conversations := db.Types[name+"CommentsConversations"]
		if conversations == nil || conversations.BehaviorTable == nil || conversations.BehaviorTable.RowField != "" || !conversations.Optimistic {
			t.Fatalf("%sCommentsConversations = %s", name, marshal(t, conversations))
		}
		var fields []string
		for _, fd := range conversations.Fields {
			fields = append(fields, fd.Name)
		}
		if !slices.Equal(fields, []string{"conversation", "lastMessage", "lastReaction", "lastStand"}) || !conversations.Fields[0].Key {
			t.Errorf("%sCommentsConversations' fields = %v", name, fields)
		}
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	compareExpanded(t, db, "fixture-comments-db.expanded.json")
}

// TestCommentsAPIFixtureLoads: the API serves each of Comments' operations
// a table runs as a route of its row under the use's member, with its
// parameters as the declaration types them and an MCP tool each, and no
// worker, since Comments keeps nothing for one to run; it leaves out, with
// a warning each, the operations a table's Comments leaves out (streamed
// replies, code anchors) and the namespace's (D103's step 4), and serves
// read state and follows (markRead and follow, #713); and its expanded IR
// is held to its golden.
func TestCommentsAPIFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	api, err := LoadService(filepath.FromSlash(commentsAPIFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	var leftOut []string
	for _, warning := range warnings {
		if !strings.HasPrefix(warning, "TicketComments: Ticket's Comments is a Comments, whose operation ") {
			t.Errorf("a warning of something else: %q", warning)
			continue
		}
		leftOut = append(leftOut, strings.Fields(strings.TrimPrefix(warning, "TicketComments: Ticket's Comments is a Comments, whose operation "))[0])
	}
	if want := []string{"begin", "appendDraft", "finish", "interrupt", "moveAnchor", "inbox", "searchMessages", "claimDeliveries", "settleDeliveries"}; !slices.Equal(leftOut, want) {
		t.Errorf("the operations left out = %v, want %v", leftOut, want)
	}
	var operations []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if marker := op.BehaviorOperation; marker != nil {
				operations = append(operations, set.Name+"."+op.Name+" "+op.RestPath+" "+op.MCP.Handle+" "+marker.Use)
			}
		}
	}
	if want := []string{
		"TicketComments.commentsPost ticket/{id}/comments/post ticket_comments_post Comments",
		"TicketComments.commentsEdit ticket/{id}/comments/edit ticket_comments_edit Comments",
		"TicketComments.commentsDeleteMessage ticket/{id}/comments/delete-message ticket_comments_delete_message Comments",
		"TicketComments.commentsPurge ticket/{id}/comments/purge ticket_comments_purge Comments",
		"TicketComments.commentsReact ticket/{id}/comments/react ticket_comments_react Comments",
		"TicketComments.commentsUnreact ticket/{id}/comments/unreact ticket_comments_unreact Comments",
		"TicketComments.commentsPin ticket/{id}/comments/pin ticket_comments_pin Comments",
		"TicketComments.commentsUnpin ticket/{id}/comments/unpin ticket_comments_unpin Comments",
		"TicketComments.commentsResolve ticket/{id}/comments/resolve ticket_comments_resolve Comments",
		"TicketComments.commentsReopen ticket/{id}/comments/reopen ticket_comments_reopen Comments",
		"TicketComments.commentsMarkRead ticket/{id}/comments/mark-read ticket_comments_mark_read Comments",
		"TicketComments.commentsFollow ticket/{id}/comments/follow ticket_comments_follow Comments",
		"TicketComments.commentsListMessages ticket/{id}/comments/list-messages ticket_comments_list_messages Comments",
		"TicketComments.commentsGetMessage ticket/{id}/comments/get-message ticket_comments_get_message Comments",
		"TicketComments.commentsListEdits ticket/{id}/comments/list-edits ticket_comments_list_edits Comments",
		"TicketComments.commentsListReactions ticket/{id}/comments/list-reactions ticket_comments_list_reactions Comments",
		"TicketComments.commentsListPinned ticket/{id}/comments/list-pinned ticket_comments_list_pinned Comments",
	}; !slices.Equal(operations, want) {
		t.Errorf("the behavior operations =\n%s\nwant\n%s", strings.Join(operations, "\n"), strings.Join(want, "\n"))
	}
	if len(api.Workers) != 0 {
		t.Errorf("the API's workers = %s, want none", marshal(t, api.Workers))
	}
	compareExpanded(t, api, "fixture-comments-api.expanded.json")
}

// TestCommentsConfigsAreHeldToTheCore: the build refuses a table's
// Comments config member of a part the behaviors' core cannot run yet, in
// the core's words, from the JSON data form: a streamed reply's
// composeTimeoutMs, a delivery's deliverPermission, an anchor's
// anchorPermission, attachments, and a blocks list that names attachment;
// and takes one of the members a table takes (D103's step 4).
func TestCommentsConfigsAreHeldToTheCore(t *testing.T) {
	load := func(t *testing.T, config map[string]any) error {
		t.Helper()
		doc := map[string]any{
			"name": "tickets-db", "kind": "DB",
			"scalars": map[string]any{
				"Identity.UUID": map[string]any{"name": "Identity.UUID", "languagePrimitive": "string"},
			},
			"types": map[string]any{
				"Ticket": map[string]any{"name": "Ticket", "role": "DBTable",
					"behaviors": []any{map[string]any{"name": "Comments", "implements": true, "config": config}},
					"fields": []any{
						map[string]any{"name": "id", "typeRef": map[string]any{"name": "Identity.UUID"}, "required": true, "key": true, "autoGenerated": true},
						map[string]any{"name": "title", "typeRef": map[string]any{"name": "string"}, "required": true},
					}},
			},
		}
		data, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		_, err = LoadService(writeService(t, map[string]string{
			"schema.config.json":      `{"name": "tickets-db", "kind": "DB", "outputs": {}}`,
			"src/tickets.schema.json": string(data),
		}))
		return err
	}
	for _, tc := range []struct {
		name   string
		config map[string]any
		want   string
	}{
		{"a streamed reply's timeout", map[string]any{"composeTimeoutMs": 60000}, "type Ticket: behavior Comments config: composeTimeoutMs: a streamed reply (begin, appendDraft, finish, interrupt and the config's composeTimeoutMs and maxDrafts) waits for a table's generated API to serve the core's signals"},
		{"a delivery's permission", map[string]any{"deliverPermission": "tickets.notify"}, "type Ticket: behavior Comments config: deliverPermission: claiming and settling deliveries (claimDeliveries, settleDeliveries and the config's deliverPermission, maxAttempts, retryBackoffMs and deliveryRetentionMs) and searchMessages each run across a namespace"},
		{"an anchor's permission", map[string]any{"anchorPermission": "tickets.anchor"}, "type Ticket: behavior Comments config: anchorPermission: moving a code block's anchor (moveAnchor and anchorPermission) waits for code anchors on the core"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := load(t, tc.config); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("LoadService = %v, want it to hold %q", err, tc.want)
			}
		})
	}
	if err := load(t, map[string]any{"blocks": []any{"ref", "code"}, "maxBodyLength": 2000, "moderatePermission": "tickets.moderate"}); err != nil {
		t.Errorf("a config of the members a table takes: %v", err)
	}
	// The events a table's Comments emits for the notifier (#713).
	if err := load(t, map[string]any{"events": true}); err != nil {
		t.Errorf("a config of events: %v", err)
	}
	// An attachment block, and the config's attachments, a table takes
	// since D154.
	if err := load(t, map[string]any{"blocks": []any{"ref", "attachment"}, "attachments": map[string]any{"maxBytes": 1000, "schemes": []any{"attachment"}}}); err != nil {
		t.Errorf("a config of attachment blocks: %v", err)
	}
}
