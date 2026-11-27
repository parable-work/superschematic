// fixture-recurrence-api's filings end to end in a generated Go server
// (D152, D187), run by TestGoRecurrenceEndToEnd with the API module's
// testdata/create.sql fixture-recurrence-db's and
// SUPERSCHEMATIC_RULES_TEST_DATABASE_URL the database. The generated
// routes serve the API over the generated ORM of fixture-recurrence-db,
// in a schema of the test's own, behind an authenticator that reads
// `Authorization: Bearer <subject>[:<permission>,...]`; the behavior
// worker FilingDeadlines runs in process, its queues as the API's module
// generates them (FilingDeadlinesQueues: Filing's deadlines, changes and
// events) under worker.RunAll, with Workers of the test's own that record
// each event Recurrence emits as it opens, reminds and misses an
// occurrence (D153). The calendar is read through the schema runtime's
// crosstable over the ORM's CrossTables. The cases are the TypeScript
// server's (tsrestgen's behavior_recurrence_runtime.test.ts) and the Rust
// one's (D188).
//
// A Filing implements Recurrence in Europe/London: each occurrence opens
// two weeks before it is due, reminds three days before, and is missed a
// day after; its due date moves to the next business day of the holiday
// calendar its holidayCalendar names, whose days are rows of Holiday. A
// deadline's fire is driven as the TypeScript test drives one: its message
// made due with SQL, then the worker's queues until the row shows it.
package fixturerecurrenceapi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"reflect"
	"slices"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/parable-work/superschematic/runtime/http/go/session"
	"github.com/parable-work/superschematic/runtime/http/go/worker"
	"github.com/parable-work/superschematic/runtime/schema/go/access"
	"github.com/parable-work/superschematic/runtime/schema/go/crosstable"
	"go.uber.org/zap"

	api "example.com/schemas/api/fixture-recurrence-api"
	orm "example.com/schemas/orm/fixture-recurrence-db"
	types "example.com/schemas/types/go/fixture-recurrence-api"
	dbtypes "example.com/schemas/types/go/fixture-recurrence-db"
)

const (
	manager = "00000000-0000-4000-8000-0000000000a1"
	staff   = "00000000-0000-4000-8000-0000000000a2"
)

var permissions = map[string][]string{
	manager: {"filings.manage", "filings.work"},
	staff:   {"filings.work"},
}

// authenticate is the test's authenticator: Authorization: Bearer
// <subject>[:<permission>[,...]] puts that subject on the request, holding
// those permissions.
func authenticate(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if token, ok := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer "); ok && token != "" {
			subject, held, _ := strings.Cut(token, ":")
			var granted []string
			if held != "" {
				granted = strings.Split(held, ",")
			}
			ctx := session.ContextWithPrincipalID(r.Context(), subject)
			ctx = session.ContextWithRoles(ctx, []session.Role{{Name: "test", Permissions: granted}})
			r = r.WithContext(ctx)
		}
		next.ServeHTTP(w, r)
	})
}

func openDatabase(t *testing.T) (*orm.Database, *pgxpool.Pool) {
	t.Helper()
	url := os.Getenv("SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	if url == "" {
		t.Skip("set SUPERSCHEMATIC_RULES_TEST_DATABASE_URL")
	}
	ctx := context.Background()
	admin, err := pgxpool.New(ctx, url)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(admin.Close)
	schema := fmt.Sprintf("recurrence_go_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE SCHEMA "+schema); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(context.Background(), "DROP SCHEMA "+schema+" CASCADE") })
	cfg, err := pgxpool.ParseConfig(url)
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
	return db, pool
}

const day = 24 * time.Hour

// londonDay is a wall date-time at 09:00 of the first weekday at least
// some days from now, as setRecurrence takes it: no weekend moves it.
func londonDay(days int) string {
	at := time.Now().UTC().Add(time.Duration(days) * day)
	for at.Weekday() == time.Saturday || at.Weekday() == time.Sunday {
		at = at.Add(day)
	}
	return at.Format("2006-01-02") + "T09:00"
}

// occurrence is a row of a filing's occurrences.
type occurrence struct {
	Key                                           string
	Due, State, Shifted                           *string
	DueAt, OpensAt, RemindAt, OverdueAt           *time.Time
	RemindedAt                                    *time.Time
	Revision                                      *int64
	CompletedByTable, CompletedByKey, CompletedBy *string
}

func millis(t *time.Time) int64 {
	if t == nil {
		return 0
	}
	return t.UnixMilli()
}

func text(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

// heard is what Recurrence emitted, as the project's Workers methods heard
// it (D153): the kind, the occurrence's key and the row's.
type event struct{ kind, key, row string }

type workers struct {
	mu    sync.Mutex
	heard []event
}

func (w *workers) hear(kind, key, row string) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.heard = append(w.heard, event{kind, key, row})
	return nil
}

func (w *workers) of(row string) []event {
	w.mu.Lock()
	defer w.mu.Unlock()
	var out []event
	for _, e := range w.heard {
		if e.row == row {
			out = append(out, e)
		}
	}
	return out
}

func (w *workers) FilingDeadlinesRecurrenceOpened(_ context.Context, _ orm.TxInterface, e types.FilingRecurrenceOpenedEvent) error {
	return w.hear("opened", e.Payload.Key, e.Key.String())
}

func (w *workers) FilingDeadlinesRecurrenceReminded(_ context.Context, _ orm.TxInterface, e types.FilingRecurrenceRemindedEvent) error {
	return w.hear("reminded", e.Payload.Key, e.Key.String())
}

func (w *workers) FilingDeadlinesRecurrenceMissed(_ context.Context, _ orm.TxInterface, e types.FilingRecurrenceMissedEvent) error {
	return w.hear("missed", e.Payload.Key, e.Key.String())
}

func TestRecurrence(t *testing.T) {
	ctx := context.Background()
	db, pool := openDatabase(t)
	router := chi.NewRouter()
	router.Use(authenticate)
	if err := api.RegisterRoutes(router, api.Config{DB: db, Logger: zap.NewNop()}); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	project := &workers{}

	// call calls an operation of a filing's Recurrence as who: its status
	// and its answer's data, or the refusal whole.
	call := func(t *testing.T, who, id, operation string, body any) (int, map[string]any) {
		t.Helper()
		payload, _ := json.Marshal(body)
		request, err := http.NewRequest(http.MethodPost, server.URL+"/api/filing/"+id+"/recurrence/"+operation, bytes.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		request.Header.Set("Content-Type", "application/json")
		request.Header.Set("Authorization", "Bearer "+who+":"+strings.Join(permissions[who], ","))
		response, err := http.DefaultClient.Do(request)
		if err != nil {
			t.Fatal(err)
		}
		defer response.Body.Close()
		var out map[string]any
		_ = json.NewDecoder(response.Body).Decode(&out)
		if data, ok := out["data"].(map[string]any); ok {
			return response.StatusCode, data
		}
		return response.StatusCode, out
	}
	filings := 0
	newFiling := func(t *testing.T, title, holidays string) (string, string) {
		t.Helper()
		filings++
		if title == "" {
			title = fmt.Sprintf("Filing %d", filings)
		}
		row, err := db.GetFilingRepository().CreateOne(ctx, &orm.FilingCreate{Title: title, HolidayCalendar: holidays})
		if err != nil {
			t.Fatal(err)
		}
		return row.Id.String(), row.Id.ToUUID().String()
	}
	holiday := func(t *testing.T, calendar, date string) {
		t.Helper()
		on, err := dbtypes.ParseTemporalDate(date)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := db.GetHolidayRepository().CreateOne(ctx, &orm.HolidayCreate{Calendar: calendar, Date: on, Name: "Closed"}); err != nil {
			t.Fatal(err)
		}
	}
	// occurrences are a filing's occurrences by SQL, in due order.
	occurrences := func(t *testing.T, column string) []occurrence {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT key, due, due_at, opens_at, remind_at, overdue_at, state, revision, shifted, reminded_at, completed_by_table, completed_by_key, completed_by_principal FROM filing_recurrence_occurrences WHERE filing_id = $1 ORDER BY due_at, key`, column)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var out []occurrence
		for rows.Next() {
			var o occurrence
			if err := rows.Scan(&o.Key, &o.Due, &o.DueAt, &o.OpensAt, &o.RemindAt, &o.OverdueAt, &o.State, &o.Revision, &o.Shifted, &o.RemindedAt, &o.CompletedByTable, &o.CompletedByKey, &o.CompletedBy); err != nil {
				t.Fatal(err)
			}
			out = append(out, o)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		return out
	}
	// deadlines are a filing's live deadline messages, by name.
	deadlines := func(t *testing.T, column string) map[string]int64 {
		t.Helper()
		rows, err := pool.Query(ctx, `SELECT name, at FROM filing_deadline_queue WHERE row_key = $1 AND state IN ('ready', 'claimed')`, column)
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		out := map[string]int64{}
		for rows.Next() {
			var name string
			var at int64
			if err := rows.Scan(&name, &at); err != nil {
				t.Fatal(err)
			}
			out[name] = at
		}
		return out
	}
	makeDue := func(t *testing.T, column, name string) {
		t.Helper()
		tag, err := pool.Exec(ctx, `UPDATE filing_deadline_queue SET due_at = now() WHERE row_key = $1 AND name = $2 AND state IN ('ready', 'claimed')`, column, name)
		if err != nil || tag.RowsAffected() != 1 {
			t.Fatalf("make %s due: %d rows, %v", name, tag.RowsAffected(), err)
		}
	}
	// workUntil runs the worker's queues in process, as its main does,
	// until done holds or fifteen seconds pass.
	workUntil := func(t *testing.T, done func() bool) {
		t.Helper()
		running, stop := context.WithCancel(ctx)
		stopped := make(chan error, 1)
		go func() {
			stopped <- worker.RunAll(running, api.FilingDeadlinesQueues(db, project, zap.NewNop()), worker.Options{Concurrency: 2, Idle: 100 * time.Millisecond, Grace: 2 * time.Second})
		}()
		deadline := time.Now().Add(15 * time.Second)
		for !done() && time.Now().Before(deadline) {
			time.Sleep(100 * time.Millisecond)
		}
		stop()
		if err := <-stopped; err != nil {
			t.Fatalf("the worker: %v", err)
		}
		if !done() {
			t.Fatal("the worker did not get there within fifteen seconds")
		}
	}

	t.Run("setRecurrence plans the occurrences up to the horizon, moved off the row calendar holidays, and arms the open, remind and overdue deadlines", func(t *testing.T) {
		start := londonDay(40)
		// The first due day is a holiday of the east calendar: it moves to
		// the next business day.
		holiday(t, "east", start[:10])
		id, column := newFiling(t, "", "east")
		status, set := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=MONTHLY", "start": start})
		if status != http.StatusOK {
			t.Fatalf("set-recurrence: %d %v", status, set)
		}
		if recurrence, _ := set["recurrence"].(map[string]any); recurrence["revision"] != float64(1) {
			t.Errorf("the recurrence: %v", set["recurrence"])
		}
		rows := occurrences(t, column)
		if len(rows) < 12 {
			t.Fatalf("%d occurrences, want a year's at least", len(rows))
		}
		if rows[0].Key != start || text(rows[0].Shifted) != "holiday" || text(rows[0].Due) == start {
			t.Errorf("the first occurrence: key %s, shifted %s, due %s", rows[0].Key, text(rows[0].Shifted), text(rows[0].Due))
		}
		for _, row := range rows {
			if text(row.State) != "upcoming" {
				t.Errorf("occurrence %s is %s", row.Key, text(row.State))
			}
		}
		armed := deadlines(t, column)
		if armed["open"] != millis(rows[0].OpensAt) || armed["remind"] != millis(rows[0].RemindAt) {
			t.Errorf("armed %v, want open at %d and remind at %d", armed, millis(rows[0].OpensAt), millis(rows[0].RemindAt))
		}
		if _, ok := armed["overdue"]; ok {
			t.Error("an overdue deadline is armed with nothing open")
		}
		_, listed := call(t, staff, id, "list-occurrences", map[string]any{"states": []string{"upcoming"}})
		items, _ := listed["items"].([]any)
		if len(items) == 0 || items[0].(map[string]any)["key"] != start {
			t.Errorf("list-occurrences: %v", listed)
		}
	})

	t.Run("a rule change leaves the open occurrence untouched and replans the ones that have not opened", func(t *testing.T) {
		id, column := newFiling(t, "", "")
		// Due in ten days: its two-week window is open at once.
		if status, body := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=MONTHLY", "start": londonDay(10)}); status != http.StatusOK {
			t.Fatalf("set-recurrence: %d %v", status, body)
		}
		before := occurrences(t, column)
		if text(before[0].State) != "open" {
			t.Fatalf("the first occurrence is %s, want open", text(before[0].State))
		}
		status, changed := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=WEEKLY", "start": londonDay(20)})
		if status != http.StatusOK {
			t.Fatalf("the change: %d %v", status, changed)
		}
		if kept, _ := changed["kept"].([]any); len(kept) != 1 || kept[0] != before[0].Key {
			t.Errorf("kept %v, want %s", changed["kept"], before[0].Key)
		}
		after := occurrences(t, column)
		if !reflect.DeepEqual(after[0], before[0]) {
			t.Errorf("the open occurrence changed:\n%+v\n%+v", before[0], after[0])
		}
		for _, row := range after[1:] {
			if row.Revision == nil || *row.Revision != 2 {
				t.Errorf("occurrence %s is of revision %v", row.Key, row.Revision)
			}
		}
		// The rest are the weekly rule's: a week apart, give or take a
		// change of the clocks.
		var gaps []time.Duration
		for i := 2; i < len(after); i++ {
			gaps = append(gaps, after[i].DueAt.Sub(*after[i-1].DueAt))
		}
		if len(gaps) <= 40 {
			t.Errorf("%d weeks planned", len(gaps))
		}
		for _, gap := range gaps {
			if math.Abs(float64(gap-7*day)) > float64(time.Hour) {
				t.Errorf("a gap of %s", gap)
			}
		}
	})

	t.Run("the remind deadline fires on the worker, records the reminder and emits the opening and the reminder", func(t *testing.T) {
		id, column := newFiling(t, "", "")
		if status, body := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=YEARLY", "start": londonDay(30)}); status != http.StatusOK {
			t.Fatalf("set-recurrence: %d %v", status, body)
		}
		first := occurrences(t, column)[0]
		if first.RemindedAt != nil {
			t.Fatal("reminded before the deadline")
		}
		makeDue(t, column, "remind")
		workUntil(t, func() bool { return occurrences(t, column)[0].RemindedAt != nil })
		fired := occurrences(t, column)[0]
		if millis(fired.RemindedAt) < millis(first.RemindAt) {
			t.Errorf("reminded at %v, before its remind time %v", fired.RemindedAt, first.RemindAt)
		}
		// Three days before its due, its two-week window has opened too.
		if text(fired.State) != "open" {
			t.Errorf("the reminded occurrence is %s, want open", text(fired.State))
		}
		// The fire emitted both moves, which the worker handed to the
		// project's Workers methods (D153).
		workUntil(t, func() bool {
			return slices.ContainsFunc(project.of(id), func(e event) bool { return e.kind == "reminded" })
		})
		var got []string
		for _, e := range project.of(id) {
			got = append(got, e.kind+" "+e.key)
		}
		sort.Strings(got)
		if want := []string{"opened " + fired.Key, "reminded " + fired.Key}; !slices.Equal(got, want) {
			t.Errorf("heard %q, want %q", got, want)
		}
	})

	t.Run("complete records what completed an occurrence and who, and an afterDone recurrence dates its next from it", func(t *testing.T) {
		id, column := newFiling(t, "", "")
		if status, body := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "afterDone", "every": "P90D", "start": londonDay(5)}); status != http.StatusOK {
			t.Fatalf("set-recurrence: %d %v", status, body)
		}
		status, done := call(t, staff, id, "complete", map[string]any{"completedBy": map[string]any{"table": "Payment", "key": "p-17"}})
		if status != http.StatusOK {
			t.Fatalf("complete: %d %v", status, done)
		}
		by, _ := done["completedBy"].(map[string]any)
		if done["state"] != "done" || by["table"] != "Payment" || by["key"] != "p-17" || done["completedByPrincipal"] != staff {
			t.Errorf("the completion: %v", done)
		}
		rows := occurrences(t, column)
		var states []string
		for _, row := range rows {
			states = append(states, text(row.State))
		}
		if !slices.Equal(states, []string{"done", "upcoming"}) {
			t.Fatalf("the states are %q", states)
		}
		if text(rows[0].CompletedByTable) != "Payment" || text(rows[0].CompletedByKey) != "p-17" {
			t.Errorf("the done occurrence: %+v", rows[0])
		}
	})

	t.Run("without filings.manage setRecurrence is 403 and a rule that is no RRULE is 400", func(t *testing.T) {
		id, _ := newFiling(t, "", "")
		if status, body := call(t, staff, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=YEARLY", "start": londonDay(5)}); status != http.StatusForbidden {
			t.Errorf("without filings.manage: %d %v", status, body)
		}
		if status, body := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=SOMETIMES", "start": londonDay(5)}); status != http.StatusBadRequest {
			t.Errorf("a rule that is no RRULE: %d %v", status, body)
		}
	})

	t.Run("the calendar merges a contract end date and a filing occurrences, each dated as its source says", func(t *testing.T) {
		ends := londonDay(30)[:10]
		on, err := dbtypes.ParseTemporalDate(ends)
		if err != nil {
			t.Fatal(err)
		}
		contract, err := db.GetContractRepository().CreateOne(ctx, &orm.ContractCreate{Title: "Lease", EndsOn: on})
		if err != nil {
			t.Fatal(err)
		}
		id, _ := newFiling(t, "Quarterly return", "")
		if status, body := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=MONTHLY", "start": londonDay(10)}); status != http.StatusOK {
			t.Fatalf("set-recurrence: %d %v", status, body)
		}
		caller := crosstable.Caller{Principal: access.Principal{Subject: manager, Kind: access.KindUser}, Permissions: permissions[manager]}
		now := time.Now()
		read, err := crosstable.ReadCalendar(ctx, db.CrossTables(), caller, crosstable.CalendarRead{From: now.UTC().Format(time.RFC3339), To: now.Add(45 * day).UTC().Format(time.RFC3339)}, crosstable.CalendarOptions{})
		if err != nil {
			t.Fatal(err)
		}
		var renewal *crosstable.CalendarEntry
		var filed []crosstable.CalendarEntry
		for i, entry := range read.Entries {
			switch {
			case entry.Source == "contract" && entry.Key == contract.Id.String():
				renewal = &read.Entries[i]
			case entry.Source == "filing" && entry.Key == id:
				filed = append(filed, entry)
			}
		}
		if renewal == nil || renewal.Kind != "renewal" || renewal.Title != "Lease" || renewal.Start != ends || !renewal.AllDay || renewal.Type != "Contract" {
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
		if !slices.IsSorted(starts) {
			t.Errorf("the entries are not in start order: %q", starts)
		}
	})

	t.Run("a raw SQL write of a filing holiday calendar is heard by the worker, which plans again", func(t *testing.T) {
		start := londonDay(60)
		holiday(t, "west", start[:10])
		id, column := newFiling(t, "", "")
		if status, body := call(t, manager, id, "set-recurrence", map[string]any{"anchor": "calendar", "rule": "FREQ=MONTHLY", "start": start}); status != http.StatusOK {
			t.Fatalf("set-recurrence: %d %v", status, body)
		}
		if occurrences(t, column)[0].Shifted != nil {
			t.Fatal("shifted with no holiday calendar")
		}
		if _, err := pool.Exec(ctx, `UPDATE filing SET holiday_calendar = 'west' WHERE id = $1`, column); err != nil {
			t.Fatal(err)
		}
		workUntil(t, func() bool { return text(occurrences(t, column)[0].Shifted) == "holiday" })
		_ = id
	})
}
