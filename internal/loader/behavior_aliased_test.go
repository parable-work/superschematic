package loader

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader/verify"
	ir "github.com/parable-work/superschematic/ir"
)

// The fixture of D147's Aliased: a database whose Company carries a name
// with aliases, unique with its case ignored, and implements Mergeable,
// beside a Contact of a company and a Tag whose label is read as a slug.
const aliasedDBFixture = "tsreader/testdata/services/fixture-aliased-db"

// TestAliasedFixtureLoads: the fixture loads with no warning and no error;
// Company and Tag stay tables whose fields carry Aliased, each config read
// as the TypeScript form wrote it; the loader adds each use's name on the
// field's own column, required and given at a create (FromCreate), and
// for each use a sub-table of its aliases, a child of the row keyed by the
// normalized form, and a shared table of its names, keyed by the
// normalized form alone, whose holder takes the table's key type with an
// index of its own; no queue, since Aliased arms no deadline, hears no
// write and emits no event; and the expanded IR is held to its golden.
func TestAliasedFixtureLoads(t *testing.T) {
	var warnings []string
	warn := WithWarnings(func(d *verify.Diagnostic) { warnings = append(warnings, d.Msg) })
	db, err := LoadService(filepath.FromSlash(aliasedDBFixture), warn)
	if err != nil {
		t.Fatal(err)
	}
	if len(warnings) != 0 {
		t.Errorf("the fixture's warnings =\n%q\nwant none", warnings)
	}
	company, tag := db.Types["Company"], db.Types["Tag"]
	if company == nil || !company.Optimistic || !company.Mergeable || tag == nil || !tag.Optimistic {
		t.Fatalf("Company = %s, Tag = %s", marshal(t, company), marshal(t, tag))
	}
	if refs := company.FieldBehaviors(); len(refs) != 1 || refs[0].Name != "Aliased" || refs[0].Field != "name" || len(refs[0].Config) != 0 {
		t.Errorf("Company's uses = %s", marshal(t, refs))
	}
	if refs := tag.FieldBehaviors(); len(refs) != 1 || refs[0].Field != "label" || string(refs[0].Config) != `{"normalize":"slug"}` {
		t.Errorf("Tag's uses = %s", marshal(t, refs))
	}
	for td, field := range map[*ir.TypeDef]string{company: "name", tag: "label"} {
		var owned []*ir.FieldDef
		for _, fd := range td.Fields {
			if fd.Owned() {
				owned = append(owned, fd)
			}
		}
		if len(owned) != 1 || owned[0].Name != field || owned[0].TypeRef.Name != "string" || !owned[0].Required || !owned[0].GivenAtCreate() {
			t.Errorf("%s's owned fields = %s, want %s, a required string a create gives", td.Name, marshal(t, owned), field)
		}
	}
	aliases, names := db.Types["CompanyNameAliases"], db.Types["CompanyNameNames"]
	if aliases == nil || aliases.BehaviorTable == nil || aliases.BehaviorTable.RowField != "company" || aliases.BehaviorTable.Key != "normalized" {
		t.Fatalf("CompanyNameAliases = %s", marshal(t, aliases))
	}
	if names == nil || names.BehaviorTable == nil || names.BehaviorTable.RowField != "" || !names.Optimistic {
		t.Fatalf("CompanyNameNames = %s", marshal(t, names))
	}
	var fields []string
	for _, fd := range names.Fields {
		fields = append(fields, fd.Name+":"+fd.TypeRef.Name)
	}
	if !slices.Equal(fields, []string{"normalized:string", "holder:Identity.UUID"}) || !names.Fields[0].Key || !names.Fields[1].Required {
		t.Errorf("CompanyNameNames' fields = %v", fields)
	}
	if len(names.Indexes) != 1 || !slices.Equal(names.Indexes[0].Keys, []string{"holder"}) || names.Indexes[0].Unique {
		t.Errorf("CompanyNameNames' indexes = %s", marshal(t, names.Indexes))
	}
	if db.Types["TagLabelAliases"] == nil || db.Types["TagLabelNames"] == nil {
		t.Error("Tag's label keeps no aliases or names table")
	}
	for _, kind := range []string{ir.BehaviorQueueDeadlines, ir.BehaviorQueueChanges, ir.BehaviorQueueEvents} {
		if queue := db.BehaviorQueueOf("Company", kind); queue != nil {
			t.Errorf("Company's %s queue = %s, want none", kind, queue.Name)
		}
	}
	compareExpanded(t, db, "fixture-aliased-db.expanded.json")
}

// TestAliasedRefusals: Aliased is a field's, T is string, and its config's
// normalize is lower or slug; the TypeScript checker refuses a T that is no
// string, a missing T and an implements of Aliased, which resolves to its
// T, before the reader does.
func TestAliasedRefusals(t *testing.T) {
	cases := []struct {
		name, field, implements, want string
	}{
		{"a number for T", "name: Aliased<number>;", "", "Type 'number' does not satisfy the constraint 'string'"},
		{"no type argument", "name: Aliased;", "", "Generic type 'Aliased' requires between 1 and 2 type arguments"},
		{"a normalize it does not take", `name: Aliased<string, { normalize: "upper" }>;`, "", "normalize"},
		{"implements", "title: string;", " implements Aliased<string>", "A class can only implement an object type"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			dir := t.TempDir()
			if err := os.CopyFS(dir, os.DirFS(filepath.FromSlash(aliasedDBFixture))); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(dir, "tsconfig.json"), []byte(viewTSConfig(t)), 0o644); err != nil {
				t.Fatal(err)
			}
			schema := `import { Identity } from "superscalar";
import { Aliased, AutoGenerate, key } from "@superschematic/db";

export abstract class Company` + c.implements + ` {
  @key
  id: AutoGenerate<Identity.UUID>;
  ` + c.field + `
}
`
			if err := os.WriteFile(filepath.Join(dir, "src", "companies.schema.ts"), []byte(schema), 0o644); err != nil {
				t.Fatal(err)
			}
			_, err := LoadService(dir)
			if err == nil || !strings.Contains(err.Error(), c.want) {
				t.Errorf("load = %v, want an error holding %q", err, c.want)
			}
		})
	}
}
