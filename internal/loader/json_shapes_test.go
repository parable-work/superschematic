package loader_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
)

// TestAClassOnlyJSONFieldsHoldIsNoTable: in fixture-json-shapes-db, Shown,
// a class with no key that only a @jsonField field holds, and Line, which
// only Shown holds in a list, are JSON shapes, as Margin, marked
// @jsonField, is; Person, which a @jsonField field holds but which has a
// key, stays a table. The database's tables can then be modelled, so
// Task's Approvals, whose resetOn names a field of Task, keeps its
// sub-table and the behaviors' layout builds, where a table of Shown
// failed the model and turned every behavior's checks off.
func TestAClassOnlyJSONFieldsHoldIsNoTable(t *testing.T) {
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-json-shapes-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	for _, name := range []string{"Line", "Margin", "Shown"} {
		if td := db.Types[name]; td == nil || !td.JsonField {
			t.Errorf("%s is no JSON shape: %+v", name, td)
		}
	}
	if td := db.Types["Person"]; td == nil || td.JsonField {
		t.Errorf("Person, which has a key, is a JSON shape: %+v", td)
	}
	var tables []string
	for _, td := range sqlutil.TableTypes(db) {
		tables = append(tables, td.Name)
	}
	if want := []string{"Note", "Person", "Task", "TaskApprovalsSignOffs"}; !slices.Equal(tables, want) {
		t.Errorf("the tables are %v, want %v", tables, want)
	}
	if _, err := sqlutil.TablesOf(db, nil); err != nil {
		t.Errorf("TablesOf: %v", err)
	}
	if _, err := behaviorlayout.Of(db, reg); err != nil {
		t.Errorf("behaviorlayout.Of: %v", err)
	}
}
