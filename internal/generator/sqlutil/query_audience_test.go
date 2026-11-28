package sqlutil_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// TestScanQueryAudienceMarks: a :audience(alias) mark is no parameter;
// its alias is read as Postgres reads one, a quoted one as it is; a
// :audience with no parenthesis is a parameter; and the tables a statement
// reads are each FROM's, JOIN's, comma's and UPDATE's, by their alias
// (#735).
func TestScanQueryAudienceMarks(t *testing.T) {
	scan, err := ScanQuery(`SELECT d.id FROM public.document AS d JOIN folder f ON f.id = d.folder_id AND :audience( F )
		LEFT JOIN "Note" ON true, tag t2, generate_series(1, 2) g
		WHERE :audience("d") AND x = :audience AND EXISTS (SELECT 1 FROM document WHERE :audience(document))
		AND a IS DISTINCT FROM b`)
	if err != nil {
		t.Fatal(err)
	}
	var aliases []string
	for _, use := range scan.Audience {
		aliases = append(aliases, use.Alias)
	}
	if want := []string{"f", "d", "document"}; !reflect.DeepEqual(aliases, want) {
		t.Errorf("marks = %v, want %v", aliases, want)
	}
	if len(scan.Params) != 1 || scan.Params[0].Name != "audience" {
		t.Errorf("params = %+v", scan.Params)
	}
	want := []QueryTableRef{{Name: "document", Alias: "d"}, {Name: "folder", Alias: "f"}, {Name: "Note", Alias: "Note"}, {Name: "tag", Alias: "t2"}, {Name: "document", Alias: "document"}}
	if !reflect.DeepEqual(scan.TableRefs, want) {
		t.Errorf("refs = %+v\nwant %+v", scan.TableRefs, want)
	}
	for _, sql := range []string{"SELECT 1 FROM t WHERE :audience()", "SELECT 1 FROM t WHERE :audience(a b)", `SELECT 1 FROM t WHERE :audience("a)`} {
		if _, err := ScanQuery(sql); err == nil || !strings.Contains(err.Error(), ":audience mark") {
			t.Errorf("%s: %v", sql, err)
		}
	}
}

// TestFillAudienceMarks: a directive is replaced, its condition's % kept
// a literal of the format string; a %% and another directive are left.
func TestFillAudienceMarks(t *testing.T) {
	got := FillAudienceMarks("a %%A{0} %A{0} %s %A{1}%d", func(i int) string { return []string{"x LIKE '5%'", "TRUE"}[i] })
	if want := "a %%A{0} x LIKE '5%%' %s TRUE%d"; got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// TestQueriesOfAudienceMarks: fixture-audience-queries-db's queries carry
// each mark, its directive in the text in the order of the marks, its
// columns through the alias, and a view's mark its key in its source's
// rows; a paged query's mark stays in its page statements; verification
// prepares each with a condition that binds no parameter (#735).
func TestQueriesOfAudienceMarks(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-audience-queries-db"))
	if err != nil {
		t.Fatal(err)
	}
	queries, err := QueriesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]QueryModel{}
	for _, q := range queries {
		byName[q.Name] = q
		if len(q.Unmarked) > 0 {
			t.Errorf("%s: unmarked %v", q.Name, q.Unmarked)
		}
	}
	folder := byName["folderDocuments"]
	if want := []QueryAudienceMark{
		{Alias: "f", Table: "Folder", Effective: `f.effective_audience`, Agents: `f.effective_agents`},
		{Alias: "d", Table: "Document", Effective: `d.effective_audience`, Agents: `d.effective_agents`},
	}; !reflect.DeepEqual(folder.Audience, want) {
		t.Errorf("folderDocuments marks = %+v", folder.Audience)
	}
	if !strings.Contains(folder.Text, "f.id = d.folder_id AND %A{0}\n    WHERE %A{1} AND f.id = $1") {
		t.Errorf("folderDocuments text = %s", folder.Text)
	}
	statements := folder.Statements()
	if len(statements) != 1 || !strings.Contains(statements[0].SQL, `AND (f.effective_audience IS NULL AND f.effective_agents <> 'none')`) || strings.Contains(statements[0].SQL, "%") {
		t.Errorf("folderDocuments statement = %+v", statements)
	}

	page := byName["documentPage"]
	if !strings.Contains(page.Page.After, "%A{0}") || !strings.Contains(page.Page.After, `(q.title, q.id) > ($2, $3)`) {
		t.Errorf("documentPage after = %s", page.Page.After)
	}
	for _, st := range page.Statements() {
		if strings.Contains(st.SQL, "%A") || !strings.Contains(st.SQL, `d.effective_audience IS NULL`) {
			t.Errorf("documentPage %s = %s", st.Label, st.SQL)
		}
	}

	listed := byName["listedDocuments"]
	if len(listed.Audience) != 1 || listed.Audience[0].Probe != `l.id IN (SELECT document.id FROM document%s)` || listed.Audience[0].Effective != `document.effective_audience` {
		t.Errorf("listedDocuments marks = %+v", listed.Audience)
	}
	if st := listed.Statements()[0].SQL; !strings.Contains(st, `WHERE l.id IN (SELECT document.id FROM document WHERE document.effective_audience IS NULL AND document.effective_agents <> 'none')`) {
		t.Errorf("listedDocuments statement = %s", st)
	}
}

// TestQueriesOfRefuseAStrayMarkAndListTheUnmarked: a mark whose alias is
// no reference to an @audience table is refused; a reference no mark
// holds, to the table or to its projection's view, is listed for
// verification to refuse (#735).
func TestQueriesOfRefuseAStrayMarkAndListTheUnmarked(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-audience-queries-db"))
	if err != nil {
		t.Fatal(err)
	}
	def := schema.Types["VisibleDocuments"].Query
	set := func(sql string) {
		copied := *def
		copied.SQL = sql
		schema.Types["VisibleDocuments"].Query = &copied
	}
	set("SELECT d.id, d.title FROM document d WHERE :audience(x) AND :search::text IS NULL")
	if _, err := QueriesOf(schema, nil); err == nil || !strings.Contains(err.Error(), "query visibleDocuments: :audience(x) names x, which is no alias of an @audience table") {
		t.Errorf("a stray mark: %v", err)
	}
	set("SELECT d.id, d.title FROM document d, document_list l, folder WHERE l.id = d.id AND :audience(folder) AND :search::text IS NULL")
	queries, err := QueriesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, q := range queries {
		if q.Name == "visibleDocuments" {
			if want := []string{"document (as d)", "document_list (as l)"}; !reflect.DeepEqual(q.Unmarked, want) {
				t.Errorf("unmarked = %v", q.Unmarked)
			}
		}
	}
	_ = ir.RoleQuery
}
