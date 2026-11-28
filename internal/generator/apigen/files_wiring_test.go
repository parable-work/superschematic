package apigen_test

import (
	"path/filepath"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/loader"
)

// TestTheFilePassNeedsAttachmentsAndAFilesSlot holds WithFiles to its
// rule (#603): a database with @attachments and an API with a files slot
// get the pass, in a wiring of its own that no entrypoint wires, or beside
// the notifier's, search's and the rest; a database with no @attachments,
// or an API with no files slot, gets none.
func TestTheFilePassNeedsAttachmentsAndAFilesSlot(t *testing.T) {
	withFiles, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-references-db"))
	if err != nil {
		t.Fatal(err)
	}
	without, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-db"))
	if err != nil {
		t.Fatal(err)
	}
	files := &apigen.DepsProviders{Slots: []apigen.DepsProviderSlot{{Slot: "SMS", Interface: "channel"}, {Slot: "PREVIEWS", Interface: "files"}}}
	sms := &apigen.DepsProviders{Slots: []apigen.DepsProviderSlot{{Slot: "SMS", Interface: "channel"}}}

	alone := apigen.WithFiles(nil, withFiles, files, false)
	if alone == nil || !alone.Files || alone.Wires() || alone.Constructs() || alone.Serves() || alone.Authenticates() {
		t.Errorf("the pass alone: %+v", alone)
	}
	if onSQLite := apigen.WithFiles(nil, withFiles, files, true); onSQLite == nil || !onSQLite.SQLite || onSQLite.DatabaseType() != "*orm.SQLiteDatabase" {
		t.Errorf("the pass alone on SQLite takes the pool's ORM: %+v", onSQLite)
	}
	search := &apigen.DepsWiring{Search: true, SearchQueues: []string{"SearchIndexing"}}
	if got := apigen.WithFiles(search, withFiles, files, false); got != search || !got.Files || !got.Wires() {
		t.Errorf("the pass beside search: %+v", got)
	}
	for name, got := range map[string]*apigen.DepsWiring{
		"no files slot":           apigen.WithFiles(nil, withFiles, sms, false),
		"no providers":            apigen.WithFiles(nil, withFiles, nil, false),
		"no @attachments":         apigen.WithFiles(nil, without, files, false),
		"no database":             apigen.WithFiles(nil, nil, files, false),
		"search, no slot":         apigen.WithFiles(&apigen.DepsWiring{Search: true}, withFiles, sms, false),
		"search, no @attachments": apigen.WithFiles(&apigen.DepsWiring{Search: true}, without, files, false),
	} {
		if got != nil && got.Files {
			t.Errorf("%s: the pass is wired: %+v", name, got)
		}
	}
	output := &apigen.APIOutput{Deps: apigen.DepsInfo{Wiring: alone}}
	if !output.HasFileProcessing() || !output.HasWiring() {
		t.Error("an API whose wiring holds the pass writes no ProcessPendingFiles")
	}
	if (&apigen.APIOutput{}).HasFileProcessing() {
		t.Error("an API with no wiring holds the pass")
	}
}
