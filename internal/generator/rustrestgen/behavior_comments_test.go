package rustrestgen

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The comments fixture (D103's step 4): fixture-comments-api serves
// TicketComments, @behaviorOperations of Ticket for any signed-in caller,
// over fixture-comments-db, its one DB dependency, whose Ticket and Review
// each keep a conversation. Each of Comments' operations a table runs is a
// route of the row under comments, run through the behaviors' core with
// run_operation; the API runs no worker.
var commentsFixture = behaviorFixture{api: "fixture-comments-api", db: "fixture-comments-db"}

// TestWriteRustAPIGoldenComments pins the crate of fixture-comments-api:
// the router mounts each of Ticket's Comments' operations under comments,
// each run through the behaviors' core with run_operation, and the layout
// it embeds holds each use's sub-tables and its shared table of the
// conversations. Regenerate with:
// go test ./internal/generator/rustrestgen -run TestWriteRustAPIGoldenComments -update
func TestWriteRustAPIGoldenComments(t *testing.T) {
	_, db, reg := commentsFixture.load(t)
	output := commentsFixture.generate(t, db)
	deps := behaviorDeps(behaviorLayout(t, db, reg))
	deps.Database = commentsFixture.db
	deps.DatabaseTypesCrate = naming.Default().RustTypesCrate(commentsFixture.db)
	deps.DatabaseTypesPath = "../../types/rust/" + commentsFixture.db
	if err := output.SetDeps(deps); err != nil {
		t.Fatalf("set deps: %v", err)
	}
	var behaviors []string
	for _, endpoint := range output.Endpoints {
		if b := endpoint.Behavior; b != nil {
			behaviors = append(behaviors, strings.ToUpper(endpoint.Method)+" "+endpoint.Path+" "+b.Type+"."+b.Use+"."+b.Operation)
		}
	}
	slices.Sort(behaviors)
	var want []string
	for _, route := range []string{
		"delete-message deleteMessage", "edit edit", "follow follow", "get-message getMessage", "list-edits listEdits",
		"list-messages listMessages", "list-pinned listPinned", "list-reactions listReactions", "mark-read markRead", "pin pin",
		"post post", "purge purge", "react react", "reopen reopen", "resolve resolve", "unpin unpin", "unreact unreact",
	} {
		path, operation, _ := strings.Cut(route, " ")
		want = append(want, "POST /api/ticket/{id}/comments/"+path+" Ticket.Comments."+operation)
	}
	if !slices.Equal(behaviors, want) {
		t.Errorf("behavior operations\n%q, want\n%q", behaviors, want)
	}
	if len(output.Workers) != 0 || len(output.BehaviorWorkers) != 0 {
		t.Errorf("workers %+v and behavior workers %+v, want none", output.Workers, output.BehaviorWorkers)
	}
	generated := writeGoldenAPI(t, commentsFixture.api, output)
	for file, want := range map[string][]string{
		"src/router.rs": {
			"behaviors::run_operation(\n        &state.implementations.behaviors,\n        &ctx,\n        \"Ticket\",\n        &args.id,\n        \"Comments\",\n        \"post\",",
		},
		"src/behaviors.rs": {
			`pub const RULES_LAYOUT: &str = r#"{`,
			`"ticket_comments_messages"`,
			`"ticket_comments_conversations"`,
		},
	} {
		for _, text := range want {
			if !strings.Contains(generated[file], text) {
				t.Errorf("%s does not contain %q", file, text)
			}
		}
	}
}
