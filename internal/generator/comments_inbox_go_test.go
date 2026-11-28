package generator

import (
	_ "embed"
	"os"
	"strings"
	"testing"
)

//go:embed testdata/comments_inbox_go.go
var commentsInboxGoTest string

// commentsInboxGoCases are the cases testdata/comments_inbox_go.go runs: a
// table's conversation in the notifier's inbox (#713) in the Go
// repositories and the HTTP runtime's notify package, the TypeScript
// repositories' cases (tsgen's commentsInboxCases).
var commentsInboxGoCases = []string{
	"a mention reaches the mentioned principal's inbox, and a principal who cannot read the row is not notified",
	"a reply reaches the thread's followers, a follower it mentions once, and never its author",
	"markRead clears a reader's unread, and follow turns a thread's replies on and off",
	"a message posted for a sender over a channel records its via and its for",
}

// TestGoCommentsInboxEndToEnd generates fixture-comments-inbox-db's Go
// types and ORM (#713), writes testdata/comments_inbox_go.go into the
// ORM's module, which requires the HTTP runtime for its notify package,
// and runs it, after go vet, on the Postgres behaviorServerDatabaseEnv
// names, in a schema of its own with the database's create.sql applied:
// a ticket's Comments emits its mentions and replies, the table's event
// queue hands each to the notifier, whose routes reach the subjects the
// events name who may read the ticket; markRead, follow and View run on
// the table; and a message posted as an agent for a party over SMS
// records its for and its via. Every case of commentsInboxGoCases must
// pass. It skips without the database and in -short mode.
func TestGoCommentsInboxEndToEnd(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping a table's conversation in the Go notifier in -short mode")
	}
	dsn := os.Getenv(behaviorServerDatabaseEnv)
	if dsn == "" {
		t.Skip("set " + behaviorServerDatabaseEnv + " to run a table's conversation in the Go notifier against Postgres")
	}
	out := goRuntimeModule(t, "fixture-comments-inbox-db", "comments_inbox", commentsInboxGoTest, "TestCommentsInbox", nil, "COMMENTS_TEST_DATABASE_URL="+dsn)
	t.Logf("generated ORM tests:\n%s", out)
	for _, name := range commentsInboxGoCases {
		if want := "--- PASS: TestCommentsInbox/" + strings.ReplaceAll(name, " ", "_") + " "; !strings.Contains(out, want) {
			t.Errorf("the generated module's test did not pass %q", name)
		}
	}
}
