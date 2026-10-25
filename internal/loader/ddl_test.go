package loader

import (
	"path/filepath"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// TestVerifyDDLTS: the @index options and the @rawSql pieces the DDL cannot
// honor fail the load, each naming the fix (D83).
func TestVerifyDDLTS(t *testing.T) {
	_, err := LoadService(filepath.Join("tsreader", "testdata", "services", "broken-ddl-db"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Note: @index(["body"]) uses the hnsw method, which indexes a vector column, and Note.body is TEXT; vector columns follow`,
		`Note: @index(["body"]) where must be one SQL expression, with no semicolon`,
		`Note: @rawSql "notes_view" up statement 2 cannot run again over what it made`,
		`Note: @rawSql "orphan" runs after "missing", which no @rawSql of the service declares`,
		`Note: @rawSql "first" runs after itself, through the pieces it runs after`,
		`Attachment: @rawSql is on a @jsonField type, which gets no table; write it on a table of the service`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}

// TestLoadDDLFixture: the fixture's options and pieces load as declared,
// its multi-line SQL from a template literal.
func TestLoadDDLFixture(t *testing.T) {
	schema, err := LoadService(filepath.Join("tsreader", "testdata", "services", "fixture-ddl-db"))
	if err != nil {
		t.Fatal(err)
	}
	category := schema.Types["Category"]
	if len(category.Indexes) != 2 || category.Indexes[0].Expression != "lower(label)" || category.Indexes[1].Where != "active" {
		t.Errorf("Category's indexes = %+v", category.Indexes)
	}
	line := schema.Types["LineItem"]
	if len(line.Indexes) != 1 || line.Indexes[0].Method != ir.IndexMethodBTree || line.Indexes[0].Concurrently == nil || *line.Indexes[0].Concurrently {
		t.Errorf("LineItem's index = %+v", line.Indexes)
	}
	if len(line.RawSQL) != 2 || line.RawSQL[0].ID != "category_totals" || !strings.Contains(line.RawSQL[0].Up, "\n") ||
		line.RawSQL[1].After != "category_totals" || line.RawSQL[0].Down == "" || len(line.RawSQL[1].Dialects) != 1 {
		t.Errorf("LineItem's raw SQL = %+v", line.RawSQL)
	}
}

// TestVerifyDDLJSON: the data forms have no compiler, so the verify pass
// refuses a method, an operator class and an engine type's index options
// they can write.
func TestVerifyDDLJSON(t *testing.T) {
	dir := writeService(t, map[string]string{
		"schema.config.json": `{"name": "temp-db", "kind": "DB", "outputs": {}}`,
		"src/tenant.schema.json": `{"types": {"Tenant": {"name": "Tenant", "role": "DBTable", "fields": [
			{"name": "id", "typeRef": {"name": "string"}, "required": true, "key": true},
			{"name": "slug", "typeRef": {"name": "string"}, "required": true}
		], "indexes": [{"keys": ["slug"], "method": "hash"}, {"keys": ["slug"], "name": "ops", "ops": "Bad Ops"}],
		"rawSql": [{"id": "t", "up": "CREATE TABLE extra (a int)", "dialects": ["oracle"]}]}}}`,
	})
	_, err := LoadService(dir)
	if err == nil {
		t.Fatal("expected schema errors")
	}
	for _, want := range []string{
		`Tenant: @index(["slug"]) method "hash" is not one of btree, gin, gist, brin, hnsw, ivfflat`,
		`Tenant: @index(["slug"]) ops "Bad Ops" must name an operator class`,
		`Tenant: @rawSql "t" names the dialect "oracle"`,
		`Tenant: @rawSql "t" up statement 1 cannot run again`,
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q is missing %q", err.Error(), want)
		}
	}
}
