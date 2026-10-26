package sqlutil_test

import (
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	. "github.com/parable-work/superschematic/internal/generator/sqlutil"
	"github.com/parable-work/superschematic/internal/loader"
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
	if want := []string{"accountLines", "budgets", "categoryTotals", "timeBuckets", "transactionList", "transferCandidates"}; !reflect.DeepEqual(names, want) {
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
}
