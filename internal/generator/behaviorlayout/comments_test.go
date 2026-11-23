package behaviorlayout_test

import (
	"slices"
	"testing"
)

// commentsFixture is the DB service whose Ticket and Review each keep a
// conversation (D103's step 4).
const commentsFixture = "../../loader/tsreader/testdata/services/fixture-comments-db"

// The layout of tables that implement Comments (D103's step 4): each use
// owns no column, since Comments computes its fields at a read; its
// sub-tables messages, drafts, reads, follows, deliveries, edits and
// reactions are tables of their own keyed by their declared keys beside
// the row's key column (a post writes its author's follow and marker, and
// its deliveries), and its shared table conversations is keyed by the
// row's key alone, with the version each write is held to; its operations
// are the ones a table runs, the streamed replies', read state's, code
// anchors' and the namespace's left out; and Comments keeps no queue,
// since the streamed replies whose idle deadlines it arms are left out
// (registry.TableArmsDeadlines).
func TestLayoutOfComments(t *testing.T) {
	layout := layoutOfFixture(t, commentsFixture, "fixture-comments-db")
	for _, name := range []string{"Ticket", "Review"} {
		td := layout.Types[name]
		if td == nil || len(td.Uses) != 1 {
			t.Fatalf("the layout's %s = %+v", name, td)
		}
		use := td.Uses[0]
		if use.ID != "Comments" || use.Member != "comments" || len(use.Fields) != 0 {
			t.Errorf("%s's use = %+v, want Comments' under comments, owning no column", name, use)
		}
		var tables []string
		for _, sub := range use.Tables {
			tables = append(tables, sub.Name+" "+sub.Table+" by "+sub.Key.Column.Column)
		}
		prefix := map[string]string{"Ticket": "ticket", "Review": "review"}[name]
		if want := []string{
			"messages " + prefix + "_comments_messages by message",
			"drafts " + prefix + "_comments_drafts by chunk",
			"reads " + prefix + "_comments_reads by subject",
			"follows " + prefix + "_comments_follows by follow",
			"deliveries " + prefix + "_comments_deliveries by delivery",
			"edits " + prefix + "_comments_edits by edit",
			"reactions " + prefix + "_comments_reactions by reaction",
		}; !slices.Equal(tables, want) {
			t.Errorf("%s's sub-tables = %v, want %v", name, tables, want)
		}
		if len(use.Shared) != 1 || use.Shared[0].Table != prefix+"_comments_conversations" || use.Shared[0].Key.Column.Column != "conversation" || use.Shared[0].Version == "" {
			t.Errorf("%s's shared tables = %+v", name, use.Shared)
		}
		var operations []string
		for _, op := range use.Operations {
			operations = append(operations, op.Name)
		}
		if want := []string{"post", "edit", "deleteMessage", "purge", "react", "unreact", "pin", "unpin", "resolve", "reopen", "listMessages", "getMessage", "listEdits", "listReactions", "listPinned"}; !slices.Equal(operations, want) {
			t.Errorf("%s's operations = %v, want %v", name, operations, want)
		}
		if td.Deadlines != nil || td.Changes != nil || td.Events != nil {
			t.Errorf("%s's queues = %+v %+v %+v, want none", name, td.Deadlines, td.Changes, td.Events)
		}
	}
}
