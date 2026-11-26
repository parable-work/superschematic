package sqlgen

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/loader"
)

// TestAudienceTriggersOfTheFixture: each @audience table of
// fixture-access-db gets the trigger that resolves its rows' audience
// before every insert and update, and a table another inherits from the
// cascade that resolves its children again after an update of its own
// (D156). create.sql and drop.sql hold both.
func TestAudienceTriggersOfTheFixture(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-access-db"))
	if err != nil {
		t.Fatal(err)
	}
	out, err := Generate(schema, Options{SchemaName: "fixture-access-db"})
	if err != nil {
		t.Fatal(err)
	}
	var tables []string
	for _, a := range out.AudienceTriggers {
		tables = append(tables, a.Table)
		trigger := a.Trigger()
		if trigger.Timing != "BEFORE INSERT OR UPDATE" || trigger.Name != "trg_"+a.Table+"_audience" || trigger.Refuses {
			t.Errorf("%s's trigger is %+v", a.Table, trigger)
		}
	}
	if strings.Join(tables, ",") != "document,folder" {
		t.Fatalf("the audience triggers are on %v, want document and folder", tables)
	}
	document := out.AudienceTriggers[0].FunctionSQL()
	for _, want := range []string{
		`SELECT p.effective_audience, p.effective_agents INTO parent_audience, parent_agents`,
		`FROM folder p WHERE p.id = NEW.folder_id`,
		`own := array_remove(ARRAY[NEW.created_by]::UUID[], NULL);`,
		`WHEN 'named' THEN own || array_remove(COALESCE(NEW.audience_named, '{}'::UUID[]), NULL)`,
		`ELSE CASE WHEN has_parent THEN parent_audience ELSE own END`,
		`NOT IN ('everyone', 'private', 'named', 'inherit')`,
	} {
		if !strings.Contains(document, want) {
			t.Errorf("document's function lacks %s:\n%s", want, document)
		}
	}
	folder := out.AudienceTriggers[1].FunctionSQL()
	if strings.Contains(folder, "'inherit'") || !strings.Contains(folder, `ARRAY[NEW."owner"]`) {
		t.Errorf("folder's function takes inherit, or reads no owner:\n%s", folder)
	}
	if len(out.AudienceCascades) != 1 || out.AudienceCascades[0].Table != "folder" {
		t.Fatalf("the cascades are %+v, want folder's alone", out.AudienceCascades)
	}
	cascade := out.AudienceCascades[0]
	if trigger := cascade.Trigger(); trigger.Timing != "AFTER UPDATE" {
		t.Errorf("the cascade fires %s", trigger.Timing)
	}
	if body := cascade.FunctionSQL(); !strings.Contains(body, `UPDATE document SET audience = audience WHERE folder_id = NEW.id AND audience = 'inherit';`) {
		t.Errorf("the cascade does not resolve folder's children again:\n%s", body)
	}
	dir := t.TempDir()
	if err := WriteDDL(out, dir); err != nil {
		t.Fatal(err)
	}
	create, err := os.ReadFile(filepath.Join(dir, "create.sql"))
	if err != nil {
		t.Fatal(err)
	}
	drop, err := os.ReadFile(filepath.Join(dir, "drop.sql"))
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"trg_document_audience", "trg_folder_audience", "trg_folder_audience_cascade"} {
		if !strings.Contains(string(create), name) || !strings.Contains(string(drop), name) {
			t.Errorf("create.sql or drop.sql lacks %s", name)
		}
	}
}
