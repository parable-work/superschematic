package loader

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixture of D103's step 4's Revisions: a database whose Policy keeps
// its history with a review step and whose Note keeps its history alone.
const revisionsDBFixture = "tsreader/testdata/services/fixture-revisions-db"

// TestRevisionsFixtureLoads: the fixture loads with no warning and no
// error; Policy and Note stay tables that implement Revisions, each config
// read as the TypeScript form wrote it; the loader adds each use's owned
// revision, optional, and for each use its sub-tables revisions and
// proposals, each keyed by its number and related to its row, and no
// queue, since Revisions arms no deadline, hears no write and emits no
// event; and the expanded IR is held to its golden (D103's step 4).
func TestRevisionsFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(revisionsDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	policy, note := db.Types["Policy"], db.Types["Note"]
	if policy == nil || len(policy.TypeBehaviors()) > 0 || !policy.Optimistic || note == nil || !note.Optimistic {
		t.Fatalf("Policy = %s, Note = %s", marshal(t, policy), marshal(t, note))
	}
	if refs := policy.Behaviors; len(refs) != 1 || refs[0].Name != "Revisions" || refs[0].Field != "" ||
		!strings.Contains(string(refs[0].Config), `"permission":"policies.review"`) {
		t.Errorf("Policy's uses = %s", marshal(t, refs))
	}
	for name, td := range map[string]*ir.TypeDef{"Policy": policy, "Note": note} {
		var owned []string
		for _, fd := range td.Fields {
			if fd.Owned() {
				owned = append(owned, fd.Name+":"+fd.TypeRef.Name)
			}
		}
		if want := []string{"revisionsRevision:Generic.Int64"}; !slices.Equal(owned, want) {
			t.Errorf("%s's owned fields = %v, want %v", name, owned, want)
		}
		for _, table := range []struct{ name, key string }{{name + "RevisionsRevisions", "revision"}, {name + "RevisionsProposals", "proposal"}} {
			sub := db.Types[table.name]
			if sub == nil || sub.Origin != ir.OriginBehavior || sub.BehaviorTable == nil || sub.BehaviorTable.RowField == "" {
				t.Fatalf("%s = %s", table.name, marshal(t, sub))
			}
		}
		for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
			if queue := db.BehaviorQueueOf(name, kind); queue != nil {
				t.Errorf("%s's %s queue = %s, want none", name, kind, queue.Name)
			}
		}
	}
	compareExpanded(t, db, "fixture-revisions-db.expanded.json")
}
