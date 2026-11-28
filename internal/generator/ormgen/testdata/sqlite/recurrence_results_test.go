// fixture-recurrence-db's Go repositories end to end on a SQLite file
// (D152, D186, #615): a Filing's Recurrence through the behaviors' core in
// the SQLite repositories' transactions, and the calendar read through the
// schema runtime's crosstable over the SQLite database's CrossTables.
// TestTheGoRecurrenceAndCalendarOnSQLite writes this file into the
// generated ORM module, applies the SQLite DDL the build writes to a file,
// and runs these cases; with the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL the same cases run on Postgres
// too, with the same keys, and each case's answers on the two are held
// equal: the same occurrences, the same dates and the same calendar.
//
// A Filing implements Recurrence in Europe/London: each occurrence opens
// two weeks before it is due, reminds three days before, and is missed a
// day after; its due date moves after weekends and the days of the holiday
// calendar its holidayCalendar names. Every plan of the 400 days' horizon
// crosses a change of the clocks, and each occurrence's due instant is held
// to the wall time its due date-time is in London, as Go's own zone
// database reads it, and to the core's expand of the same recurrence.
package orm_test

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"

	orm "example.com/schemas/orm/fixture-recurrence-db"
	types "example.com/schemas/types/go/fixture-recurrence-db"
	_ "modernc.org/sqlite"
)

const (
	manager = "00000000-0000-4000-8000-0000000000a1"
	staff   = "00000000-0000-4000-8000-0000000000a2"
	day     = 24 * time.Hour
)

var permissions = map[string][]string{
	manager: {"filings.manage", "filings.work"},
	staff:   {"filings.work"},
}

// as is ctx running a behavior's command as who, holding its permissions.
func as(who string) context.Context {
	return orm.WithBehaviorCaller(context.Background(), rules.Caller{Subject: who, Permissions: permissions[who]})
}

// queryFunc reads rows of a raw statement written with $n parameters, each
// row its columns' values.
type queryFunc func(ctx context.Context, sql string, args ...any) ([][]any, error)

// recurrenceDB is the ORM on SQLite or on Postgres, as a case reaches it.
type recurrenceDB interface {
	orm.DatabaseInterface
	CrossTables() crosstable.Host
}

// openSQLite is the ORM on a SQLite file the SQLite DDL is applied to.
func openSQLite(t *testing.T) (recurrenceDB, queryFunc) {
	t.Helper()
	ctx := context.Background()
	ddl, err := os.ReadFile("testdata/sqlite.sql")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := sql.Open("sqlite", orm.SQLiteDSN(filepath.Join(t.TempDir(), "filings.sqlite")))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := raw.ExecContext(ctx, string(ddl)); err != nil {
		t.Fatalf("the SQLite DDL: %v", err)
	}
	db, err := orm.OpenSQLite(ctx, raw)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return db, func(ctx context.Context, query string, args ...any) ([][]any, error) {
		rows, err := raw.QueryContext(ctx, strings.ReplaceAll(query, "$", "?"), args...)
		if err != nil {
			return nil, err
		}
		defer func() { _ = rows.Close() }()
		columns, _ := rows.Columns()
		var out [][]any
		for rows.Next() {
			cells := make([]any, len(columns))
			targets := make([]any, len(cells))
			for i := range cells {
				targets[i] = &cells[i]
			}
			if err := rows.Scan(targets...); err != nil {
				return nil, err
			}
			out = append(out, cells)
		}
		return out, rows.Err()
	}
}

// openPostgres is the ORM on the Postgres at
// SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL, in a schema of its own with
// create.sql applied.
func openPostgres(t *testing.T) (recurrenceDB, queryFunc) {
	t.Helper()
	target := os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL")
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, target)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("recurrence_sqlite_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(target)
	if err != nil {
		t.Fatal(err)
	}
	cfg.ConnConfig.RuntimeParams["search_path"] = schema + ",public"
	pool, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	ddl, err := os.ReadFile("testdata/create.sql")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, string(ddl)); err != nil {
		t.Fatalf("apply create.sql: %v", err)
	}
	db, err := orm.ConnectWithPool(pool)
	if err != nil {
		t.Fatal(err)
	}
	return db, func(ctx context.Context, query string, args ...any) ([][]any, error) {
		rows, err := pool.Query(ctx, query, args...)
		if err != nil {
			return nil, err
		}
		defer rows.Close()
		var out [][]any
		for rows.Next() {
			values, err := rows.Values()
			if err != nil {
				return nil, err
			}
			out = append(out, values)
		}
		return out, rows.Err()
	}
}

// TestRecurrenceOnSQLite runs the cases on SQLite and, with a Postgres,
// on Postgres, and holds each case's answers on the two equal.
func TestRecurrenceOnSQLite(t *testing.T) {
	var answers [2]map[string]any
	t.Run("sqlite", func(t *testing.T) {
		db, query := openSQLite(t)
		answers[0] = recurrenceCases(t, db, query)
	})
	if os.Getenv("SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL") == "" {
		t.Log("set SUPERSCHEMATIC_ORMGEN_TEST_DATABASE_URL to run the same cases on Postgres")
		return
	}
	t.Run("postgres", func(t *testing.T) {
		db, query := openPostgres(t)
		answers[1] = recurrenceCases(t, db, query)
	})
	names := make([]string, 0, len(answers[0]))
	for name := range answers[0] {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		sqlite, _ := json.MarshalIndent(answers[0][name], "", " ")
		postgres, _ := json.MarshalIndent(answers[1][name], "", " ")
		if string(sqlite) != string(postgres) {
			t.Errorf("%s: SQLite and Postgres differ\nsqlite:   %s\npostgres: %s", name, sqlite, postgres)
		}
	}
	if !t.Failed() {
		t.Logf("SQLite and Postgres answered %d cases alike", len(names))
	}
}

var london = func() *time.Location {
	at, err := time.LoadLocation("Europe/London")
	if err != nil {
		panic(err)
	}
	return at
}()

// today is the day the cases run, at midnight UTC: a case's days count from
// it, so the two databases' runs plan the same days.
var today = time.Now().UTC().Truncate(day)

// londonDay is a wall date-time at 09:00 of the first weekday at least
// some days from today, as setRecurrence takes it.
func londonDay(days int) string {
	at := today.Add(time.Duration(days) * day)
	for at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		at = at.Add(day)
	}
	return at.Format("2006-01-02") + "T09:00"
}

func ptr[T any](v T) *T { return &v }

// keyOf is the n-th key of a kind, the same on both databases.
func keyOf(kind, n int) string { return fmt.Sprintf("00000000-0000-4000-8%03x-%012x", kind, n) }

func mustID(t *testing.T, text string) types.IdentityUUID {
	t.Helper()
	id, err := orm.ParseUserID(text)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

// occurrence is a row of a filing's occurrences as both databases read it:
// instants in epoch milliseconds.
type occurrence struct {
	Key, Due, State, Shifted                string
	DueAt, OpensAt, RemindAt, OverdueAt     int64
	Revision                                int64
	Reminded, Completed                     bool
	CompletedByTable, CompletedByKey, ByWho string
}

func millisOf(t *testing.T, v any) int64 {
	t.Helper()
	switch v := v.(type) {
	case nil:
		return 0
	case time.Time:
		return v.UnixMilli()
	case string:
		at, err := time.Parse(time.RFC3339Nano, v)
		if err != nil {
			t.Fatalf("an instant %q: %v", v, err)
		}
		return at.UnixMilli()
	}
	t.Fatalf("an instant of %T", v)
	return 0
}

func textOf(v any) string {
	switch v := v.(type) {
	case nil:
		return ""
	case []byte:
		return string(v)
	case string:
		return v
	}
	return fmt.Sprint(v)
}

func intOf(v any) int64 {
	switch v := v.(type) {
	case int64:
		return v
	case int32:
		return int64(v)
	case int:
		return int64(v)
	}
	n, _ := strconv.ParseInt(textOf(v), 10, 64)
	return n
}

func recurrenceCases(t *testing.T, db recurrenceDB, query queryFunc) map[string]any {
	ctx := context.Background()
	answers := map[string]any{}
	filings := 0
	newFiling := func(t *testing.T, title, holidays string, contract *types.Contract) types.IdentityUUID {
		t.Helper()
		filings++
		id := mustID(t, keyOf(1, filings))
		if title == "" {
			title = fmt.Sprintf("Filing %d", filings)
		}
		if _, err := db.GetFilingRepository().CreateOne(ctx, &orm.FilingCreate{Id: &id, Title: title, HolidayCalendar: holidays, Contract: contract}); err != nil {
			t.Fatal(err)
		}
		return id
	}
	holidays := 0
	holiday := func(t *testing.T, calendar, date string) {
		t.Helper()
		on, err := types.ParseTemporalDate(date)
		if err != nil {
			t.Fatal(err)
		}
		holidays++
		id := mustID(t, keyOf(2, holidays))
		if _, err := db.GetHolidayRepository().CreateOne(ctx, &orm.HolidayCreate{Id: &id, Calendar: calendar, Date: &on, Name: "Closed"}); err != nil {
			t.Fatal(err)
		}
	}
	// occurrences are a filing's occurrences by SQL, in due order.
	occurrences := func(t *testing.T, id types.IdentityUUID) []occurrence {
		t.Helper()
		rows, err := query(ctx, `SELECT key, due, due_at, opens_at, remind_at, overdue_at, state, revision, shifted, reminded_at, completed_at, completed_by_table, completed_by_key, completed_by_principal FROM filing_recurrence_occurrences WHERE filing_id = $1 ORDER BY due_at, key`, id.ToUUID().String())
		if err != nil {
			t.Fatal(err)
		}
		out := make([]occurrence, len(rows))
		for i, r := range rows {
			out[i] = occurrence{
				Key: textOf(r[0]), Due: textOf(r[1]), DueAt: millisOf(t, r[2]), OpensAt: millisOf(t, r[3]), RemindAt: millisOf(t, r[4]), OverdueAt: millisOf(t, r[5]),
				State: textOf(r[6]), Revision: intOf(r[7]), Shifted: textOf(r[8]), Reminded: r[9] != nil, Completed: r[10] != nil,
				CompletedByTable: textOf(r[11]), CompletedByKey: textOf(r[12]), ByWho: textOf(r[13]),
			}
		}
		return out
	}
	// deadlines are a filing's live deadline messages, by name.
	deadlines := func(t *testing.T, id types.IdentityUUID) map[string]int64 {
		t.Helper()
		rows, err := query(ctx, `SELECT name, at FROM filing_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed')`, id.ToUUID().String())
		if err != nil {
			t.Fatal(err)
		}
		out := map[string]int64{}
		for _, r := range rows {
			out[textOf(r[0])] = intOf(r[1])
		}
		return out
	}
	// inLondon holds each occurrence's due instant to its due date-time's
	// wall time in London, as Go's zone database reads it, and answers
	// whether the plan crossed a change of the clocks.
	inLondon := func(t *testing.T, rows []occurrence) bool {
		t.Helper()
		offsets := map[int]bool{}
		for _, row := range rows {
			wall, err := time.ParseInLocation("2006-01-02T15:04", row.Due, london)
			if err != nil {
				t.Fatalf("a due %q: %v", row.Due, err)
			}
			if wall.UnixMilli() != row.DueAt {
				t.Errorf("occurrence %s is due at %s, and %s in London is %s", row.Key, time.UnixMilli(row.DueAt).UTC(), row.Due, wall.UTC())
			}
			_, offset := wall.Zone()
			offsets[offset] = true
		}
		return len(offsets) > 1
	}
	// asExpanded holds each occurrence to the occurrence of the same key
	// the core's expand dates for input, as every language's runtime is
	// held to it (D152).
	asExpanded := func(t *testing.T, rows []occurrence, input rules.ExpandInput) {
		t.Helper()
		input.TimeZone, input.OpensBefore, input.RemindBefore, input.OverdueAfter = "Europe/London", "P14D", "P3D", "P1D"
		if input.Adjust == "" {
			input.Adjust = "after"
		}
		input.From, input.To, input.Limit = ptr(int64(0)), ptr(today.Add(800*day).UnixMilli()), 1000
		expansion, err := rules.Expand(input)
		if err != nil {
			t.Fatal(err)
		}
		byKey := map[string]rules.ExpandedOccurrence{}
		for _, o := range expansion.Occurrences {
			byKey[o.Key] = o
		}
		for _, row := range rows {
			want, ok := byKey[row.Key]
			if !ok {
				t.Errorf("occurrence %s is none the core expands", row.Key)
				continue
			}
			got := rules.ExpandedOccurrence{Key: row.Key, Due: row.Due, DueAt: row.DueAt, OpensAt: ptr(row.OpensAt), RemindAt: ptr(row.RemindAt), OverdueAt: row.OverdueAt, Shifted: row.Shifted, Projected: want.Projected}
			if !reflect.DeepEqual(got, want) {
				t.Errorf("occurrence %s:\n got  %+v %d %d\n want %+v %d %d", row.Key, got, *got.OpensAt, *got.RemindAt, want, deref(want.OpensAt), deref(want.RemindAt))
			}
		}
	}

	t.Run("a calendar rule plans the core's dates up to the horizon across a change of the clocks, moved after weekends and off the row calendar holidays", func(t *testing.T) {
		start := londonDay(40)
		// The first due day is a holiday of the east calendar, and so is a
		// day two months on: each moves to the next business day.
		later := today.Add(40 * day)
		later = time.Date(later.Year(), later.Month()+2, mustDay(start), 0, 0, 0, 0, time.UTC)
		holiday(t, "east", start[:10])
		holiday(t, "east", later.Format("2006-01-02"))
		id := newFiling(t, "", "east", nil)
		set, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=MONTHLY"), Start: ptr(start)})
		if err != nil {
			t.Fatal(err)
		}
		rows := occurrences(t, id)
		if len(rows) < 12 {
			t.Fatalf("%d occurrences, want a year's at least", len(rows))
		}
		if rows[0].Key != start || rows[0].Shifted != "holiday" || rows[0].Due == start {
			t.Errorf("the first occurrence: key %s, shifted %s, due %s", rows[0].Key, rows[0].Shifted, rows[0].Due)
		}
		for _, row := range rows {
			if row.State != "upcoming" || row.Revision != 1 {
				t.Errorf("occurrence %s is %s of revision %d", row.Key, row.State, row.Revision)
			}
		}
		if !inLondon(t, rows) {
			t.Error("the plan crossed no change of the clocks")
		}
		asExpanded(t, rows, rules.ExpandInput{Anchor: "calendar", Rule: "FREQ=MONTHLY", Start: start, Holidays: []string{start[:10], later.Format("2006-01-02")}})
		armed := deadlines(t, id)
		if armed["open"] != rows[0].OpensAt || armed["remind"] != rows[0].RemindAt {
			t.Errorf("armed %v, want open at %d and remind at %d", armed, rows[0].OpensAt, rows[0].RemindAt)
		}
		if _, ok := armed["overdue"]; ok {
			t.Error("an overdue deadline is armed with nothing open")
		}
		listed, err := db.GetFilingRepository().Recurrence().ListOccurrences(as(staff), id, orm.FilingRecurrenceListOccurrencesParams{States: []string{"upcoming"}})
		if err != nil {
			t.Fatal(err)
		}
		if len(listed.Items) == 0 || listed.Items[0].Key != start {
			t.Errorf("listOccurrences: %+v", listed)
		}
		answers["calendar"] = map[string]any{"set": set.Kept, "next": set.NextDueAt, "occurrences": rows, "armed": armed}
	})

	t.Run("a rule change leaves the open occurrence untouched and replans the ones that have not opened", func(t *testing.T) {
		id := newFiling(t, "", "", nil)
		// Due in ten days: its two-week window is open at once.
		if _, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=MONTHLY"), Start: ptr(londonDay(10))}); err != nil {
			t.Fatal(err)
		}
		before := occurrences(t, id)
		if before[0].State != "open" {
			t.Fatalf("the first occurrence is %s, want open", before[0].State)
		}
		changed, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=WEEKLY"), Start: ptr(londonDay(20))})
		if err != nil {
			t.Fatal(err)
		}
		if len(changed.Kept) != 1 || changed.Kept[0] != before[0].Key {
			t.Errorf("kept %v, want %s", changed.Kept, before[0].Key)
		}
		after := occurrences(t, id)
		if !reflect.DeepEqual(after[0], before[0]) {
			t.Errorf("the open occurrence changed:\n%+v\n%+v", before[0], after[0])
		}
		for _, row := range after[1:] {
			if row.Revision != 2 || row.State != "upcoming" {
				t.Errorf("occurrence %s is %s of revision %d", row.Key, row.State, row.Revision)
			}
		}
		// The rest are the weekly rule's, a week apart in London's wall
		// time across the change of the clocks.
		if !inLondon(t, after[1:]) {
			t.Error("the weekly plan crossed no change of the clocks")
		}
		asExpanded(t, after[1:], rules.ExpandInput{Anchor: "calendar", Rule: "FREQ=WEEKLY", Start: londonDay(20)})
		answers["rule change"] = map[string]any{"kept": changed.Kept, "before": before[0], "after": after}
	})

	t.Run("an afterDone recurrence is due at its start, and once completed, its interval after the completion, across a change of the clocks", func(t *testing.T) {
		id := newFiling(t, "", "", nil)
		start := londonDay(5)
		if _, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "afterDone", Every: ptr("P150D"), Start: ptr(start)}); err != nil {
			t.Fatal(err)
		}
		first := occurrences(t, id)
		if len(first) != 1 || first[0].Key != start || first[0].State != "open" {
			t.Fatalf("before a completion: %+v", first)
		}
		done, err := db.GetFilingRepository().Recurrence().Complete(as(staff), id, orm.FilingRecurrenceCompleteParams{CompletedBy: &orm.FilingRecurrenceCompleteParamsCompletedBy{Table: "Payment", Key: "p-17"}})
		if err != nil {
			t.Fatal(err)
		}
		if done.State != "done" {
			t.Errorf("the completion: %+v", done)
		}
		rows := occurrences(t, id)
		var states []string
		for _, row := range rows {
			states = append(states, row.State)
		}
		if !reflect.DeepEqual(states, []string{"done", "upcoming"}) {
			t.Fatalf("the states are %q", states)
		}
		if rows[0].CompletedByTable != "Payment" || rows[0].CompletedByKey != "p-17" || rows[0].ByWho != staff {
			t.Errorf("the done occurrence: %+v", rows[0])
		}
		next := rows[1]
		wall, _ := time.ParseInLocation("2006-01-02T15:04", next.Due, london)
		if gap := wall.Sub(time.Now()); gap < 145*day || gap > 155*day {
			t.Errorf("the next is due %s, not 150 days on", next.Due)
		}
		inLondon(t, rows)
		_, startOffset := mustWall(t, start).Zone()
		_, nextOffset := wall.Zone()
		if startOffset == nextOffset {
			t.Errorf("%s and %s are in one offset: the interval crossed no change of the clocks", start, next.Due)
		}
		// The completion's instant differs between runs; its date is the
		// day's.
		answers["afterDone"] = map[string]any{"first": first, "done": []any{rows[0].Key, rows[0].State, rows[0].CompletedByTable, rows[0].CompletedByKey, rows[0].ByWho}, "next": next.Due[:10]}
	})

	t.Run("a relative recurrence is due its offset before each anniversary of its contract's end, as the core expands it", func(t *testing.T) {
		ends := today.Add(200 * day).Format("2006-01-02")
		on, err := types.ParseTemporalDate(ends)
		if err != nil {
			t.Fatal(err)
		}
		contractID := mustID(t, keyOf(3, 1))
		contract, err := db.GetContractRepository().CreateOne(ctx, &orm.ContractCreate{Id: &contractID, Title: "Lease", EndsOn: on})
		if err != nil {
			t.Fatal(err)
		}
		id := newFiling(t, "", "", contract)
		if _, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "relative", Offset: ptr("-P60D"), Every: ptr("P1Y")}); err != nil {
			t.Fatal(err)
		}
		rows := occurrences(t, id)
		if len(rows) == 0 {
			t.Fatal("no occurrence of the relative recurrence")
		}
		inLondon(t, rows)
		date, _ := json.Marshal(ends)
		asExpanded(t, rows, rules.ExpandInput{Anchor: "relative", Date: date, Offset: "-P60D", Every: "P1Y"})
		answers["relative"] = rows
	})

	t.Run("without filings.manage setRecurrence is refused and a rule that is no RRULE is invalid", func(t *testing.T) {
		id := newFiling(t, "", "", nil)
		_, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(staff), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=YEARLY"), Start: ptr(londonDay(5))})
		refusal, ok := rules.AsRefusal(err)
		if !ok || refusal.Code != rules.CodeForbidden || refusal.Status != 403 {
			t.Fatalf("without filings.manage: %v", err)
		}
		_, err = db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=SOMETIMES"), Start: ptr(londonDay(5))})
		invalid, ok := rules.AsRefusal(err)
		if !ok || invalid.Status != 400 {
			t.Fatalf("a rule that is no RRULE: %v", err)
		}
		if rows := occurrences(t, id); len(rows) != 0 {
			t.Errorf("a refused setRecurrence planned %d occurrences", len(rows))
		}
		answers["refused"] = []any{refusal.Code, refusal.Status, invalid.Code, invalid.Status}
	})

	t.Run("the calendar merges a contract end date and a filing occurrences, each dated as its source says", func(t *testing.T) {
		ends := londonDay(30)[:10]
		on, err := types.ParseTemporalDate(ends)
		if err != nil {
			t.Fatal(err)
		}
		contractID := mustID(t, keyOf(3, 2))
		contract, err := db.GetContractRepository().CreateOne(ctx, &orm.ContractCreate{Id: &contractID, Title: "Licence", EndsOn: on})
		if err != nil {
			t.Fatal(err)
		}
		id := newFiling(t, "Quarterly return", "", nil)
		if _, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=MONTHLY"), Start: ptr(londonDay(10))}); err != nil {
			t.Fatal(err)
		}
		caller := crosstable.Caller{Principal: access.Principal{Subject: manager, Kind: access.KindUser}, Permissions: permissions[manager]}
		read, err := crosstable.ReadCalendar(ctx, db.CrossTables(), caller, crosstable.CalendarRead{From: today.Format(time.RFC3339), To: today.Add(45 * day).Format(time.RFC3339)}, crosstable.CalendarOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var renewal *crosstable.CalendarEntry
		var filed []crosstable.CalendarEntry
		for i, entry := range read.Entries {
			switch {
			case entry.Source == "contract" && entry.Key == contract.Id.String():
				renewal = &read.Entries[i]
			case entry.Source == "filing" && entry.Key == id.String():
				filed = append(filed, entry)
			}
		}
		if renewal == nil || renewal.Kind != "renewal" || renewal.Title != "Licence" || renewal.Start != ends || !renewal.AllDay || renewal.Type != "Contract" {
			t.Errorf("the renewal: %+v in %+v", renewal, read.Entries)
		}
		if len(filed) == 0 {
			t.Fatalf("no entry of the filing in %+v", read.Entries)
		}
		if f := filed[0]; f.Kind != "deadline" || f.Color != "filing" || f.Title != "Quarterly return" || f.Occurrence == nil || f.Occurrence.Key != londonDay(10) || f.Occurrence.State != "open" || f.AllDay {
			t.Errorf("the filing's entry: %+v %+v", f, f.Occurrence)
		}
		var starts []string
		for _, entry := range read.Entries {
			starts = append(starts, entry.Start)
		}
		if !sort.StringsAreSorted(starts) {
			t.Errorf("the entries are not in start order: %q", starts)
		}
		// The sources by name, and the feed's text of the entries.
		only, err := crosstable.ReadCalendar(ctx, db.CrossTables(), caller, crosstable.CalendarRead{From: today.Format(time.RFC3339), To: today.Add(45 * day).Format(time.RFC3339), Sources: []string{"contract"}}, crosstable.CalendarOptions{})
		if err != nil {
			t.Fatal(err)
		}
		for _, entry := range only.Entries {
			if entry.Source != "contract" {
				t.Errorf("a read of the contract source holds %+v", entry)
			}
		}
		stamp := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
		answers["calendar read"] = map[string]any{"entries": read.Entries, "more": read.More, "contracts": len(only.Entries), "ics": crosstable.CalendarICS(read.Entries, crosstable.DetailFull, stamp, "Filings")}
	})

	monthly := func(rule string) orm.CreateOptions {
		return orm.CreateOptions{Behaviors: map[string]any{"recurrence": orm.FilingRecurrenceCreateParams{Recurrence: &orm.FilingRecurrenceCreateParamsRecurrence{Anchor: "calendar", Rule: ptr(rule), Start: ptr(londonDay(30))}}}}
	}
	t.Run("a recurrence given at create is set in the create, its occurrences planned and its deadlines armed, and held to filings.manage", func(t *testing.T) {
		// The create gives the Recurrence Filing implements its recurrence,
		// by its member (#729), in one command.
		id := mustID(t, keyOf(1, 900))
		if _, err := db.GetFilingRepository().CreateOne(as(manager), &orm.FilingCreate{Id: &id, Title: "Given at create"}, monthly("FREQ=MONTHLY")); err != nil {
			t.Fatal(err)
		}
		rows := occurrences(t, id)
		if len(rows) == 0 || rows[0].Key != londonDay(30) || rows[0].Revision != 1 {
			t.Fatalf("the occurrences: %+v", rows)
		}
		armed := deadlines(t, id)
		if armed["open"] != rows[0].OpensAt {
			t.Errorf("armed %v, want open at %d", armed, rows[0].OpensAt)
		}
		refused := map[string]string{}
		for name, who := range map[string]string{"forbidden": staff, "invalid": manager} {
			rule := "FREQ=MONTHLY"
			if name == "invalid" {
				rule = "FREQ=SOMETIMES"
			}
			other := mustID(t, keyOf(1, 901))
			_, err := db.GetFilingRepository().CreateOne(as(who), &orm.FilingCreate{Id: &other, Title: "Refused"}, monthly(rule))
			var refusal *rules.Refusal
			if !errors.As(err, &refusal) {
				t.Fatalf("%s: %v", name, err)
			}
			refused[name] = string(refusal.Code)
			if got := occurrences(t, other); len(got) != 0 {
				t.Errorf("%s: a refused create kept %d occurrences", name, len(got))
			}
		}
		if refused["forbidden"] != "forbidden" || refused["invalid"] != "invalid_argument" {
			t.Errorf("the refusals: %v", refused)
		}
		answers["created"] = map[string]any{"rows": rows, "armed": armed, "refused": refused}
	})

	t.Run("a holiday calendar whose weekend is Friday and Saturday moves a due date off Saturday to the Sunday", func(t *testing.T) {
		// The gulf calendar's rows name its weekend (#729): a weekday and
		// no date, so a Saturday due date moves after to the Sunday, where
		// the default weekend moves it to the Monday.
		for n, weekday := range []string{"friday", "saturday"} {
			id := mustID(t, keyOf(2, 900+n))
			if _, err := db.GetHolidayRepository().CreateOne(ctx, &orm.HolidayCreate{Id: &id, Calendar: "gulf", Weekday: weekday, Name: "Weekend"}); err != nil {
				t.Fatal(err)
			}
		}
		at := today.Add(40 * day)
		for at.Weekday() != time.Saturday {
			at = at.Add(day)
		}
		start := at.Format("2006-01-02") + "T09:00"
		var dues []occurrence
		for calendar, want := range map[string]time.Time{"gulf": at.Add(day), "": at.Add(2 * day)} {
			id := newFiling(t, "", calendar, nil)
			if _, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "calendar", Rule: ptr("FREQ=YEARLY"), Start: ptr(start)}); err != nil {
				t.Fatal(err)
			}
			first := occurrences(t, id)[0]
			if first.Key != start || first.Due != want.Format("2006-01-02")+"T09:00" || first.Shifted != "weekend" {
				t.Errorf("calendar %q: the first occurrence %+v, want due %s", calendar, first, want.Format("2006-01-02"))
			}
			dues = append(dues, first)
		}
		sort.Slice(dues, func(i, j int) bool { return dues[i].Due < dues[j].Due })
		answers["weekend of a calendar"] = dues
	})

	t.Run("a write of a contract end date writes a change of each filing that names it, which moves nothing of the filing", func(t *testing.T) {
		// The trigger of the relative anchor's table (#626): a write of the
		// contract's end writes a message of the filing's change queue, its
		// images before and after alike; one that leaves the end as it was
		// writes none.
		ends, err := types.ParseTemporalDate(today.Add(120 * day).Format("2006-01-02"))
		if err != nil {
			t.Fatal(err)
		}
		contractID := mustID(t, keyOf(3, 900))
		contract, err := db.GetContractRepository().CreateOne(ctx, &orm.ContractCreate{Id: &contractID, Title: "Lease", EndsOn: ends})
		if err != nil {
			t.Fatal(err)
		}
		id := newFiling(t, "", "", contract)
		if _, err := db.GetFilingRepository().Recurrence().SetRecurrence(as(manager), id, orm.FilingRecurrenceSetRecurrenceParams{Anchor: "relative", Offset: ptr("-P60D")}); err != nil {
			t.Fatal(err)
		}
		untouched := func() int64 {
			rows, err := query(ctx, `SELECT count(*) FROM filing_change_queue WHERE row_key = $1 AND "before" = "after"`, id.ToUUID().String())
			if err != nil {
				t.Fatal(err)
			}
			return intOf(rows[0][0])
		}
		title := "Lease, renewed"
		if _, err := db.GetContractRepository().UpdateOne(ctx, contractID, &orm.ContractUpdate{Title: &title}); err != nil {
			t.Fatal(err)
		}
		if n := untouched(); n != 0 {
			t.Errorf("a write of the contract's title wrote %d changes", n)
		}
		later, err := types.ParseTemporalDate(today.Add(150 * day).Format("2006-01-02"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetContractRepository().UpdateOne(ctx, contractID, &orm.ContractUpdate{EndsOn: &later}); err != nil {
			t.Fatal(err)
		}
		if n := untouched(); n != 1 {
			t.Errorf("a write of the contract's end wrote %d changes, want 1", n)
		}
		answers["target changes"] = untouched()
	})
	return answers
}

func deref(p *int64) int64 {
	if p == nil {
		return 0
	}
	return *p
}

func mustDay(wall string) int {
	n, _ := strconv.Atoi(wall[8:10])
	return n
}

func mustWall(t *testing.T, wall string) time.Time {
	t.Helper()
	at, err := time.ParseInLocation("2006-01-02T15:04", wall, london)
	if err != nil {
		t.Fatal(err)
	}
	return at
}
