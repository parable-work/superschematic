package deriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// table is a table in memory whose derived fields are stale until a settle
// writes a value or counts an attempt, as Derived's core would: an item's
// attempt rises with each failure, and a settle of a hash it did not hand
// out is skipped.
type table struct {
	mu       sync.Mutex
	name     string
	items    []Item
	values   map[string]json.RawMessage
	failures map[string][]Failure
	settles  [][]Settlement
	pulls    []int
	staleErr []error
	// settleErr fails the next settles, one each.
	settleErr []error
}

func newTable(name string, ids ...string) *table {
	t := &table{name: name, values: map[string]json.RawMessage{}, failures: map[string][]Failure{}}
	for _, id := range ids {
		t.items = append(t.items, Item{ID: id, Field: "category", SourceHash: "h-" + id, Attempt: 1, Model: "standard", Instruction: "Classify it.", Inputs: json.RawMessage(`{"subject":"` + id + `"}`), Output: json.RawMessage(`{"type":"string"}`)})
	}
	return t
}

func (t *table) stale(_ context.Context, limit int, cursor *string) (Page, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	t.pulls = append(t.pulls, limit)
	if len(t.staleErr) > 0 {
		err := t.staleErr[0]
		t.staleErr = t.staleErr[1:]
		return Page{}, err
	}
	var open []Item
	for _, item := range t.items {
		if _, done := t.values[item.ID]; !done && len(t.failures[item.ID]) < 2 {
			item.Attempt = int64(len(t.failures[item.ID]) + 1)
			open = append(open, item)
		}
	}
	start := 0
	if cursor != nil {
		fmt.Sscan(*cursor, &start)
	}
	end := min(start+limit, len(open))
	page := Page{Items: open[start:end]}
	if end < len(open) {
		next := fmt.Sprint(end)
		page.Next = &next
	}
	return page, nil
}

func (t *table) settle(_ context.Context, items []Settlement) (SettleResult, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	if len(t.settleErr) > 0 {
		err := t.settleErr[0]
		t.settleErr = t.settleErr[1:]
		return SettleResult{}, err
	}
	t.settles = append(t.settles, items)
	var out SettleResult
	for _, s := range items {
		switch {
		case s.SourceHash != "h-"+s.ID:
			out.Skipped = append(out.Skipped, Skipped{ID: s.ID, Field: s.Field, Reason: "moved"})
		case s.Failure != nil:
			t.failures[s.ID] = append(t.failures[s.ID], *s.Failure)
			out.Failed++
		default:
			t.values[s.ID] = s.Value
			out.Settled++
		}
	}
	return out, nil
}

func (t *table) of() Table { return Table{Name: t.name, Stale: t.stale, Settle: t.settle} }

func (t *table) settled() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	return len(t.values)
}

// run runs the deriver until done reports true, then stops it, and returns
// what Run returned.
func run(t *testing.T, opts Options, done func() bool) error {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	stopped := make(chan error, 1)
	go func() { stopped <- Run(ctx, opts) }()
	deadline := time.After(5 * time.Second)
	for !done() {
		select {
		case err := <-stopped:
			return err
		case <-deadline:
			cancel()
			t.Fatal("the deriver did not get there in five seconds")
		case <-time.After(5 * time.Millisecond):
		}
	}
	cancel()
	select {
	case err := <-stopped:
		return err
	case <-time.After(5 * time.Second):
		t.Fatal("the deriver did not stop with its context")
	}
	return nil
}

func value(v string) Answer { return Answer{Value: json.RawMessage(`"` + v + `"`)} }

// TestRunPullsAnswersAndSettles: each table's stale fields go to the answer
// with their table's name, and settle with the hash they were pulled with.
func TestRunPullsAnswersAndSettles(t *testing.T) {
	tickets, articles := newTable("Ticket", "t1", "t2"), newTable("Article", "a1")
	var mu sync.Mutex
	var asked []string
	opts := Options{
		Tables: []Table{tickets.of(), articles.of()},
		Answer: func(_ context.Context, table string, item Item) (Answer, error) {
			mu.Lock()
			asked = append(asked, table+"/"+item.ID)
			mu.Unlock()
			return value("bug-" + item.ID), nil
		},
		Idle: 10 * time.Millisecond,
	}
	if err := run(t, opts, func() bool { return tickets.settled() == 2 && articles.settled() == 1 }); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := string(tickets.values["t2"]); got != `"bug-t2"` {
		t.Errorf("t2 = %s", got)
	}
	for _, s := range tickets.settles[0] {
		if s.SourceHash != "h-"+s.ID {
			t.Errorf("settled %s with %s, not the hash it pulled", s.ID, s.SourceHash)
		}
	}
	mu.Lock()
	defer mu.Unlock()
	if strings.Join(asked, " ") != "Ticket/t1 Ticket/t2 Article/a1" && strings.Join(asked, " ") != "Ticket/t2 Ticket/t1 Article/a1" {
		t.Errorf("asked %v", asked)
	}
}

// TestRunPagesByCursor: a table is pulled a batch at a time by its cursor,
// and each page settles in one call.
func TestRunPagesByCursor(t *testing.T) {
	tickets := newTable("Ticket", "t1", "t2", "t3", "t4", "t5")
	opts := Options{
		Tables:    []Table{tickets.of()},
		Answer:    func(_ context.Context, _ string, item Item) (Answer, error) { return value(item.ID), nil },
		BatchSize: 2,
		Idle:      10 * time.Millisecond,
	}
	if err := run(t, opts, func() bool { return tickets.settled() == 5 }); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := len(tickets.settles[0]); got != 2 {
		t.Errorf("the first settle took %d items, want a page of 2", got)
	}
	if tickets.pulls[0] != 2 {
		t.Errorf("pulled %d at a time, want 2", tickets.pulls[0])
	}
}

// TestRunFailsWhatFails: an answer that returns an error, one that panics,
// one that gives a failure, and one of no shape a settle takes each settle
// as their own item's failure, which counts an attempt, and the page still
// settles the item that answered.
func TestRunFailsWhatFails(t *testing.T) {
	tickets := newTable("Ticket", "err", "panic", "failure", "empty", "sure", "ok")
	bad := 1.5
	opts := Options{
		Tables: []Table{tickets.of()},
		Answer: func(_ context.Context, _ string, item Item) (Answer, error) {
			switch item.ID {
			case "err":
				return Answer{}, errors.New("the model is down")
			case "panic":
				panic("boom")
			case "failure":
				return Answer{Failure: &Failure{Class: "refused", Message: strings.Repeat("x", 3000)}}, nil
			case "empty":
				return Answer{}, nil
			case "sure":
				return Answer{Value: json.RawMessage(`"x"`), Confidence: &bad}, nil
			}
			return value("ok"), nil
		},
		Idle: 10 * time.Millisecond,
	}
	if err := run(t, opts, func() bool {
		tickets.mu.Lock()
		defer tickets.mu.Unlock()
		return len(tickets.failures["sure"]) == 2 && len(tickets.values) == 1
	}); err != nil {
		t.Fatalf("Run = %v", err)
	}
	classes := map[string]string{}
	for id, failures := range tickets.failures {
		classes[id] = failures[0].Class
	}
	want := map[string]string{"err": "error", "panic": "panicked", "failure": "refused", "empty": "invalid_answer", "sure": "invalid_answer"}
	for id, class := range want {
		if classes[id] != class {
			t.Errorf("%s failed as %q, want %q", id, classes[id], class)
		}
	}
	if got := len(tickets.failures["failure"][0].Message); got != maxFailure {
		t.Errorf("a failure's message is %d long, want it cut to %d", got, maxFailure)
	}
}

// TestRunKeepsItsConcurrency: no more than Concurrency answers run at once.
func TestRunKeepsItsConcurrency(t *testing.T) {
	tickets := newTable("Ticket", "1", "2", "3", "4", "5", "6", "7", "8")
	var running, most atomic.Int32
	opts := Options{
		Tables: []Table{tickets.of()},
		Answer: func(_ context.Context, _ string, item Item) (Answer, error) {
			n := running.Add(1)
			defer running.Add(-1)
			for {
				m := most.Load()
				if n <= m || most.CompareAndSwap(m, n) {
					break
				}
			}
			time.Sleep(10 * time.Millisecond)
			return value(item.ID), nil
		},
		Concurrency: 3,
		Idle:        10 * time.Millisecond,
	}
	if err := run(t, opts, func() bool { return tickets.settled() == 8 }); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := most.Load(); got != 3 {
		t.Errorf("at most %d answers ran at once, want 3", got)
	}
	for i, s := range tickets.settles[0] {
		if s.ID != tickets.items[i].ID {
			t.Errorf("the settle's item %d is %s, want the pull's order", i, s.ID)
		}
	}
}

// TestRunWaitsOutWhatTimeCures: a pull and a settle that fail are waited
// out and tried again, the table that fails waiting while the next goes
// on; a failure Permanent reports ends Run with the table's name.
func TestRunWaitsOutWhatTimeCures(t *testing.T) {
	tickets, articles := newTable("Ticket", "t1"), newTable("Article", "a1")
	tickets.staleErr = []error{errors.New("connection refused"), errors.New("connection refused")}
	articles.settleErr = []error{errors.New("busy")}
	opts := Options{
		Tables:   []Table{tickets.of(), articles.of()},
		Answer:   func(_ context.Context, _ string, item Item) (Answer, error) { return value(item.ID), nil },
		Idle:     time.Hour,
		RetryMin: 10 * time.Millisecond,
		RetryMax: 20 * time.Millisecond,
	}
	if err := run(t, opts, func() bool { return tickets.settled() == 1 && articles.settled() == 1 }); err != nil {
		t.Fatalf("Run = %v", err)
	}

	refused := errors.New("forbidden: the caller holds no tickets.derive")
	denied := newTable("Ticket", "t1")
	denied.staleErr = []error{refused}
	err := Run(context.Background(), Options{
		Tables:    []Table{denied.of()},
		Answer:    func(context.Context, string, Item) (Answer, error) { return value("x"), nil },
		Permanent: func(err error) bool { return errors.Is(err, refused) },
	})
	if err == nil || !errors.Is(err, refused) || !strings.Contains(err.Error(), "table Ticket") {
		t.Errorf("Run = %v, want the refusal with the table's name", err)
	}
}

// TestRunWaitsWhenNothingProgresses: a pass that settles and fails nothing,
// one whose items the core skips, waits Idle before it pulls again.
func TestRunWaitsWhenNothingProgresses(t *testing.T) {
	tickets := newTable("Ticket", "t1")
	opts := Options{
		Tables: []Table{tickets.of()},
		Answer: func(_ context.Context, _ string, item Item) (Answer, error) { return value(item.ID), nil },
		Idle:   time.Hour,
	}
	// Every settle is skipped: the hash moved.
	moved := Table{Name: "Ticket", Stale: tickets.stale, Settle: func(ctx context.Context, items []Settlement) (SettleResult, error) {
		for i := range items {
			items[i].SourceHash = "moved"
		}
		return tickets.settle(ctx, items)
	}}
	opts.Tables = []Table{moved}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	if err := Run(ctx, opts); err != nil {
		t.Fatalf("Run = %v", err)
	}
	if got := len(tickets.pulls); got != 1 {
		t.Errorf("pulled %d times in 100ms, want once, then the idle wait", got)
	}
}

// TestTableOfReadsTypedOperations: TableOf reads a repository's typed
// operations, its parameters a limit and a cursor and its items, and a
// result of no shape the loop reads ends Run.
func TestTableOfReadsTypedOperations(t *testing.T) {
	type staleParams struct {
		Cursor *string `json:"cursor,omitempty"`
		Limit  *int64  `json:"limit,omitempty"`
	}
	type settleParams struct {
		Items []struct {
			ID    string          `json:"id"`
			Value json.RawMessage `json:"value"`
		} `json:"items"`
	}
	var gotLimit int64
	var gotSettle settleParams
	tab := TableOf("Ticket",
		func(_ context.Context, p staleParams) (*Page, error) {
			gotLimit = *p.Limit
			return &Page{Items: []Item{{ID: "t1", Field: "category", SourceHash: "h"}}}, nil
		},
		func(_ context.Context, p settleParams) (map[string]any, error) {
			gotSettle = p
			return map[string]any{"settled": 1, "failed": 0, "skipped": []any{}}, nil
		})
	page, err := tab.Stale(context.Background(), 7, nil)
	if err != nil || len(page.Items) != 1 || gotLimit != 7 {
		t.Fatalf("Stale = %+v, %v; limit %d", page, err, gotLimit)
	}
	result, err := tab.Settle(context.Background(), []Settlement{{ID: "t1", Value: json.RawMessage(`"bug"`)}})
	if err != nil || result.Settled != 1 || len(gotSettle.Items) != 1 || string(gotSettle.Items[0].Value) != `"bug"` {
		t.Fatalf("Settle = %+v, %v; params %+v", result, err, gotSettle)
	}

	broken := TableOf("Ticket",
		func(context.Context, staleParams) (string, error) { return "no page", nil },
		func(context.Context, settleParams) (SettleResult, error) { return SettleResult{}, nil })
	err = Run(context.Background(), Options{Tables: []Table{broken}, Answer: func(context.Context, string, Item) (Answer, error) { return value("x"), nil }})
	if err == nil || !strings.Contains(err.Error(), "the result of Ticket's staleDerivations") {
		t.Errorf("Run = %v, want the result of no shape to end it", err)
	}
}

// TestMethodTypesAnItem: Method reads an item's inputs into the method's
// struct and writes its value as JSON; a failure passes through, inputs
// the struct does not read fail the item, and Methods fails a field with
// no method.
func TestMethodTypesAnItem(t *testing.T) {
	type inputs struct {
		Subject *string `json:"subject,omitempty"`
	}
	type category string
	answer := Methods("TriageTickets", map[string]func(context.Context, Item) (Answer, error){
		"Ticket.category": Method(func(_ context.Context, item DerivedItem[inputs]) (DerivedAnswer[category], error) {
			if item.Inputs.Subject == nil {
				return Failed[category]("inputs", "no subject"), nil
			}
			sure := 0.9
			return DerivedAnswer[category]{Value: category("bug:" + *item.Inputs.Subject), Confidence: &sure}, nil
		}),
	})
	got, err := answer(context.Background(), "Ticket", Item{ID: "t1", Field: "category", Inputs: json.RawMessage(`{"subject":"crash"}`)})
	if err != nil || string(got.Value) != `"bug:crash"` || *got.Confidence != 0.9 {
		t.Errorf("answer = %+v, %v", got, err)
	}
	if got, _ := answer(context.Background(), "Ticket", Item{Field: "category", Inputs: json.RawMessage(`{}`)}); got.Failure == nil || got.Failure.Message != "no subject" {
		t.Errorf("a failure = %+v", got)
	}
	if got, _ := answer(context.Background(), "Ticket", Item{Field: "category", Inputs: json.RawMessage(`{"subject":7}`)}); got.Failure == nil || got.Failure.Class != "inputs" {
		t.Errorf("unread inputs = %+v", got)
	}
	if got, _ := answer(context.Background(), "Ticket", Item{Field: "priority"}); got.Failure == nil || got.Failure.Message != "deriver TriageTickets has no method for Ticket.priority" {
		t.Errorf("no method = %+v", got)
	}
}
