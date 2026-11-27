package access_test

import (
	"path/filepath"
	"slices"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TestAccessRecordsExampleLoads: examples/access-records's schemas load
// (D156): its database's @audience tables hold the audience columns, its
// @accessGrants and @accessLog classes their fields, and its API's views
// read the columns and the two classes' fields, its row rule follows a
// path through three relations and its view reveals behind a permission.
func TestAccessRecordsExampleLoads(t *testing.T) {
	services := filepath.Join("..", "..", "..", "examples", "access-records", "schemas", "services")
	reg := registry.New(naming.Default())
	db, err := loader.LoadService(filepath.Join(services, "records-db"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load records-db: %v", err)
	}
	for _, table := range []string{"Workspace", "Page"} {
		td := db.Types[table]
		if td == nil || td.Audience == nil {
			t.Fatalf("records-db's %s holds no audience", table)
		}
		for _, name := range ir.AudienceFields {
			if !slices.ContainsFunc(td.Fields, func(f *ir.FieldDef) bool { return f.Name == name }) {
				t.Errorf("records-db's %s has no %s", table, name)
			}
		}
	}
	if grants := db.AccessGrantsTable(); grants == nil || len(grants.Fields) == 0 {
		t.Errorf("records-db's grants table is %+v", grants)
	}
	if log := db.AccessLogTable(); log == nil || len(log.Fields) == 0 {
		t.Errorf("records-db's access log is %+v", log)
	}
	api, err := loader.LoadService(filepath.Join(services, "records-api"), loader.WithRegistry(reg))
	if err != nil {
		t.Fatalf("load records-api: %v", err)
	}
	var by []string
	for _, set := range api.OperationSets {
		for _, op := range set.Operations {
			if op.TableOperation != nil {
				by = append(by, op.TableOperation.By...)
			}
		}
	}
	if !slices.Contains(by, "site.agreement.members.member") {
		t.Errorf("records-api's row rules are %v", by)
	}
	for view, source := range map[string]string{"GrantView": "records-db.Grant", "AccessEntryView": "records-db.AccessEntry"} {
		if td := api.Types[view]; td == nil || td.Source == nil || td.Source.Target != source {
			t.Errorf("records-api's %s is no view of %s: %+v", view, source, td)
		}
	}
	if view := api.Types["AgreementView"]; view == nil || !slices.ContainsFunc(view.Fields, func(f *ir.FieldDef) bool {
		return f.Name == "terms" && f.Decrypted && f.RevealPermission == "agreements.reveal"
	}) {
		t.Errorf("records-api's AgreementView reveals no terms behind agreements.reveal")
	}
}
