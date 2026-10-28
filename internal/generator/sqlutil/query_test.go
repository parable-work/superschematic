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

// TestScanQuery: a query's :name parameters are found outside strings,
// escaped strings, quoted identifiers, dollar-quoted bodies and comments,
// and past a :: cast; a comment's @notNull names fields; LEFT and FULL
// joins name the tables they may leave out (D86).
func TestScanQuery(t *testing.T) {
	for _, tc := range []struct {
		name     string
		sql      string
		params   []string
		notNull  []string
		outer    []string
		allOuter bool
		end      int
		err      string
	}{
		{name: "names and casts", sql: "SELECT :a::uuid[], x::int FROM t WHERE y = ANY(:a) AND z = :b_2", params: []string{"a", "a", "b_2"}},
		{name: "not in strings or identifiers", sql: `SELECT ':x', E'\':y', "c:d", $$ :e $$, $tag$ :f $tag$ FROM t WHERE a = :g`, params: []string{"g"}},
		{name: "not in comments", sql: "SELECT 1 -- :a\n/* :b /* :c */ */ WHERE :d", params: []string{"d"}},
		{name: "a notNull hint", sql: "SELECT a, b -- @notNull a, b\n/* @notNull c */ FROM t", notNull: []string{"a", "b", "c"}},
		{name: "a trailing semicolon", sql: "SELECT :a;  -- done\n", params: []string{"a"}, end: len("SELECT :a")},
		{name: "outer joins", sql: `SELECT 1 FROM line_item li JOIN account a ON true LEFT JOIN category c ON true LEFT OUTER JOIN public."Budget" b ON true`, outer: []string{"category", "Budget"}},
		{name: "a full join", sql: "SELECT 1 FROM a FULL OUTER JOIN b ON true", outer: []string{"b"}, allOuter: true},
		{name: "a right join", sql: "SELECT 1 FROM a RIGHT JOIN b ON true", allOuter: true},
		{name: "a positional parameter", sql: "SELECT $1", err: "the SQL holds the positional parameter $1"},
		{name: "two statements", sql: "SELECT 1; SELECT 2", err: "the SQL holds more than one statement"},
		{name: "an open string", sql: "SELECT 'x", err: "a string is not closed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			scan, err := ScanQuery(tc.sql)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("err = %v, want %q", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			var params []string
			for _, p := range scan.Params {
				params = append(params, p.Name)
				if tc.sql[p.Start:p.End] != ":"+p.Name {
					t.Errorf("parameter %s spans %q", p.Name, tc.sql[p.Start:p.End])
				}
			}
			end := tc.end
			if end == 0 {
				end = len(tc.sql)
			}
			if !reflect.DeepEqual(params, tc.params) || !reflect.DeepEqual(scan.NotNull, tc.notNull) ||
				!reflect.DeepEqual(scan.OuterTables, tc.outer) || scan.AllOuter != tc.allOuter || scan.End != end {
				t.Fatalf("scan = params %v, notNull %v, outer %v %v, end %d", params, scan.NotNull, scan.OuterTables, scan.AllOuter, scan.End)
			}
		})
	}
}

// TestQueriesOf: fixture-queries-db's queries as both ORMs run them: each
// parameter positional by first use, a name used twice one parameter, a
// literal % doubled in the format string, an order's %s where its
// parameter stood, and a paged query in a subquery ordered by its key,
// the key's values after the query's own parameters (D86).
func TestQueriesOf(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queries-db"))
	if err != nil {
		t.Fatal(err)
	}
	queries, err := QueriesOf(schema, nil)
	if err != nil {
		t.Fatal(err)
	}
	byName := map[string]QueryModel{}
	var names []string
	for _, q := range queries {
		byName[q.Name] = q
		names = append(names, q.Name)
	}
	if want := []string{"accountLines", "accountNamed", "budgets", "categoryTotals", "dashboardCategories", "dashboardLines", "dashboardTotals", "matchingLine", "timeBuckets", "transactionList", "transferCandidates"}; !reflect.DeepEqual(names, want) {
		t.Fatalf("queries = %v, want %v", names, want)
	}
	args := func(q QueryModel) []string {
		var out []string
		for _, a := range q.Args {
			out = append(out, a.Name+"=$"+string(rune('0'+a.Position))+":"+a.SQLType)
		}
		return out
	}

	totals := byName["categoryTotals"]
	if want := []string{"start=$1:TIMESTAMPTZ", "end=$2:TIMESTAMPTZ", "accountIds=$3:UUID[]"}; !reflect.DeepEqual(args(totals), want) {
		t.Errorf("categoryTotals args = %v", args(totals))
	}
	if !strings.Contains(totals.Text, `li."date" BETWEEN $1 AND $2`) || !strings.Contains(totals.Text, "($3::uuid[] IS NULL OR li.account_id = ANY($3))") {
		t.Errorf("categoryTotals text = %s", totals.Text)
	}
	if !totals.NotNull["spent"] || !totals.NotNull["earned"] || !reflect.DeepEqual(totals.OuterTables, []string{"category", "category"}) {
		t.Errorf("categoryTotals notNull %v, outer %v", totals.NotNull, totals.OuterTables)
	}
	if len(totals.Tables) != 2 || !strings.HasPrefix(totals.Tables[0], "category(") || !strings.HasPrefix(totals.Tables[1], "line_item(") ||
		!strings.Contains(totals.Tables[1], "amount BIGINT NOT NULL") {
		t.Errorf("categoryTotals tables = %v", totals.Tables)
	}

	lines := byName["accountLines"]
	if want := []string{"accountId=$1:UUID", "limit=$2:BIGINT"}; !reflect.DeepEqual(args(lines), want) {
		t.Errorf("accountLines args = %v", args(lines))
	}
	if !strings.Contains(lines.Text, "ORDER BY %s, li.id\n    LIMIT $2") || lines.OrderBy == nil || lines.OrderBy.Param.Name != "order" || lines.OrderBy.Default != "newest" {
		t.Errorf("accountLines text %s, order %+v", lines.Text, lines.OrderBy)
	}
	statements := lines.Statements()
	if len(statements) != 3 || statements[0].Label != "order largest" || !strings.Contains(statements[0].SQL, "ORDER BY abs(li.amount) DESC, li.id") {
		t.Errorf("accountLines statements = %+v", statements)
	}

	list := byName["transactionList"]
	if !strings.Contains(list.Text, `ILIKE '%%' || $3 || '%%'`) {
		t.Errorf("transactionList text = %s", list.Text)
	}
	if list.Page == nil || !strings.HasSuffix(list.Page.First, `) AS q ORDER BY q."date" DESC, q.id DESC LIMIT %d`) ||
		!strings.Contains(list.Page.After, `) AS q WHERE (q."date", q.id) < ($5, $6) ORDER BY q."date" DESC, q.id DESC LIMIT %d`) {
		t.Errorf("transactionList page = %+v", list.Page)
	}
	if st := list.Statements(); len(st) != 2 || !strings.Contains(st[0].SQL, "ILIKE '%' || $3 || '%'") || !strings.HasSuffix(st[1].SQL, "LIMIT 101") {
		t.Errorf("transactionList statements = %+v", st)
	}

	buckets := byName["timeBuckets"]
	if want := []string{"unit=$1:TEXT", "start=$2:TIMESTAMPTZ", "end=$3:TIMESTAMPTZ"}; !reflect.DeepEqual(args(buckets), want) {
		t.Errorf("timeBuckets args = %v", args(buckets))
	}
	if !buckets.NotNull["bucket"] || !buckets.NotNull["lines"] {
		t.Errorf("timeBuckets notNull = %v", buckets.NotNull)
	}
	row := map[string]string{}
	for _, f := range byName["transactionList"].RowFields {
		row[f.Name] = f.Column + ":" + f.SQLType
	}
	if row["accountName"] != "account_name:VARCHAR(80)" || row["categoryId"] != "category_id:UUID" {
		t.Errorf("transactionList row = %v", row)
	}
	if totals.Returns != ir.QueryReturnsMany || byName["dashboardTotals"].Returns != ir.QueryReturnsOne || byName["matchingLine"].Returns != ir.QueryReturnsOptional {
		t.Errorf("returns = %s, %s, %s", totals.Returns, byName["dashboardTotals"].Returns, byName["matchingLine"].Returns)
	}
}

// TestQueriesOverAProjection: the dashboard's reports interpolate one
// filter; one reads the line_list projection's view, whose model, and the
// shapes of the tables it reads, join the tables its lock hash covers, and
// whose required columns are never null; a query of the tables alone holds
// no view (D95).
func TestQueriesOverAProjection(t *testing.T) {
	schema, err := loader.LoadService(filepath.Join(fixturesDir, "fixture-queries-db"))
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
	}
	filter := `li."date" BETWEEN $1 AND $2
      AND ($3::uuid[] IS NULL OR li.account_id = ANY($3))
      AND ($4::uuid[] IS NULL OR li.category_id = ANY($4))
      AND ($5::text IS NULL OR li."name" ILIKE '%%' || $5 || '%%')`
	for _, name := range []string{"dashboardTotals", "dashboardCategories", "dashboardLines", "matchingLine"} {
		if !strings.Contains(byName[name].Text, filter) {
			t.Errorf("%s does not hold the filter: %s", name, byName[name].Text)
		}
	}

	lines := byName["dashboardLines"]
	var shapes []string
	for _, table := range lines.Tables {
		shapes = append(shapes, strings.SplitN(table, "(", 2)[0])
	}
	if want := []string{"account", "category", "line_item", `view line_list {"def":{"name":"line_list","migration":"","source":"LineItem","joins":[{"type":"Account","alias":"account","kind":"inner","on":[{"left":"account.id","right":"base.account"}]},{"type":"Category","alias":"category","kind":"left","on":[{"left":"category.id","right":"base.category"}]}]},"columns":[{"name":"id","type":"UUID","from":"base.id"},{"name":"date","type":"TIMESTAMPTZ","from":"base.date"},{"name":"name","type":"TEXT","from":"base.name"},{"name":"account_id","type":"UUID","from":"base.account"},{"name":"account_name","type":"VARCHAR`}; len(shapes) != len(want) || !reflect.DeepEqual(shapes[:3], want[:3]) || !strings.HasPrefix(lines.Tables[3], want[3]) {
		t.Errorf("dashboardLines tables = %v", lines.Tables)
	}
	if !strings.Contains(lines.Tables[3], `{"name":"category_name","type":"VARCHAR(80)","nullable":true,"from":"category.name"}`) {
		t.Errorf("the view's shape = %s", lines.Tables[3])
	}
	for column, want := range map[string]bool{"line_list.id": true, "line_list.account_name": true, "line_list.category_id": false, "line_list.category_name": false} {
		if lines.ViewNotNull[column] != want {
			t.Errorf("view column %s never null = %v, want %v", column, lines.ViewNotNull[column], want)
		}
	}
	if totals := byName["dashboardTotals"]; len(totals.Tables) != 1 || !strings.HasPrefix(totals.Tables[0], "line_item(") || totals.ViewNotNull != nil {
		t.Errorf("dashboardTotals tables = %v, view columns %v", totals.Tables, totals.ViewNotNull)
	}

	// The view's shape follows the projection's declaration: a join made
	// left, and a column dropped.
	before := lines.Tables[3]
	projection := schema.Types["LineListEntry"]
	projection.Projection.Joins[0].Kind = "left"
	if after := ViewShape(schema, projection, ScalarSQLTypes(schema)); after == before {
		t.Error("a join made left leaves the view's shape")
	}
	projection.Projection.Joins[0].Kind = "inner"
	projection.Projection.Migration = "20991231000000"
	if after := ViewShape(schema, projection, ScalarSQLTypes(schema)); after != before {
		t.Errorf("a new migration stamp alone changes the view's shape:\n%s\n%s", before, after)
	}
	projection.Fields = projection.Fields[:len(projection.Fields)-1]
	if after := ViewShape(schema, projection, ScalarSQLTypes(schema)); after == before {
		t.Error("a column dropped leaves the view's shape")
	}
}

// TestQueryManyRows: what a statement's text shows of the rows it may
// return, which a query that returns one row, or one or none, is held to
// (D95): its top level's GROUP BY, UNION, VALUES of several rows and
// set-returning functions, unless an aggregate makes one row of a
// function's, or LIMIT 1 holds it to one.
func TestQueryManyRows(t *testing.T) {
	for _, tc := range []struct {
		sql, want string
	}{
		{"SELECT COALESCE(SUM(amount), 0)::bigint AS total, COUNT(*) AS lines FROM line_item li LEFT JOIN category c ON c.id = li.category_id WHERE li.amount > :min", ""},
		{"SELECT a.id FROM account a WHERE a.name = :name", ""},
		{"SELECT category_id, SUM(amount) FROM line_item GROUP BY 1", "groups its rows with GROUP BY, which returns a row per group"},
		{"SELECT category_id, SUM(amount) FROM line_item GROUP BY 1 ORDER BY 2 DESC LIMIT 1", ""},
		{"SELECT category_id FROM line_item GROUP BY 1 FETCH FIRST ROW ONLY", ""},
		{"SELECT category_id FROM line_item GROUP BY 1 LIMIT 1 + :n", "groups its rows with GROUP BY, which returns a row per group"},
		{"SELECT percentile_cont(0.5) WITHIN GROUP (ORDER BY amount) FROM line_item", ""},
		{"SELECT b FROM generate_series(1, 3) AS b", "reads the set-returning function generate_series in its FROM, which returns a row per value"},
		{"SELECT COUNT(*) FROM generate_series(1, 3) AS b", ""},
		{"SELECT SUM(x) OVER () FROM unnest(:xs) AS x", "reads the set-returning function unnest in its FROM, which returns a row per value"},
		{"SELECT SUM(x) FILTER (WHERE x > 0) FROM unnest(:xs) AS x", ""},
		{"SELECT li.id FROM line_item li CROSS JOIN LATERAL jsonb_array_elements(li.tags) AS t", "reads the set-returning function jsonb_array_elements in its FROM, which returns a row per value"},
		{"SELECT 1 FROM line_item, ROWS FROM (generate_series(1, 2)) AS g", "reads the set-returning function ROWS FROM in its FROM, which returns a row per value"},
		{"SELECT unnest(:ids) AS id", "calls the set-returning function unnest in its select list, which returns a row per value"},
		{"SELECT 1 + pg_catalog.generate_series(1, 3) AS n", "calls the set-returning function generate_series in its select list, which returns a row per value"},
		{"SELECT (SELECT array_agg(x) FROM unnest(:ids) AS x) AS ids", ""},
		{"SELECT id FROM line_item WHERE id IN (SELECT unnest(:ids))", ""},
		{"SELECT extract(epoch FROM now()) AS at", ""},
		{"WITH t AS (SELECT * FROM generate_series(1, 3) AS x) SELECT COUNT(*) FROM t", ""},
		{"WITH t AS (SELECT 1) SELECT * FROM t UNION ALL SELECT 2", "is a UNION, which returns the rows of each of its branches"},
		{"VALUES (1, 'a'), (2, 'b')", "is a VALUES list of 2 rows"},
		{"VALUES (1, 'a')", ""},
		{"SELECT * FROM (VALUES (1), (2)) AS v(n)", ""},
	} {
		t.Run(tc.sql, func(t *testing.T) {
			scan, err := ScanQuery(tc.sql)
			if err != nil {
				t.Fatal(err)
			}
			if scan.ManyRows != tc.want {
				t.Fatalf("many rows = %q, want %q", scan.ManyRows, tc.want)
			}
		})
	}
}
