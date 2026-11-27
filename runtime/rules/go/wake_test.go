package rules_test

import (
	"context"
	"fmt"
	"slices"
	"testing"

	rules "github.com/parable-work/superschematic/runtime/rules/go"
	"github.com/parable-work/superschematic/runtime/rules/go/internal/rulestest"
)

// The wake of a behavior worker's queues (D145, D151): once a command's
// unit has applied, the adapter names to the host's Wake the event queue of
// each event, the deadline queue of each deadline armed due within
// WakeHorizon of the command's time, and the change queue of each row it
// wrote, each once; a unit that does not apply names none.

// wakeTicketLayout is Ticket's layout with its event queue and a change
// queue, which no statement of the adapter's writes: its trigger does.
func wakeTicketLayout() *rules.Layout {
	layout := eventTicketLayout()
	layout.Types["Ticket"].Changes = &rules.Changes{Queue: "TicketChange", Table: "ticket_change_queue", RowKey: "row_key", Write: "write", At: "at", Before: "before", After: "after"}
	return layout
}

// armed is an arm of the Ticket's deadline name, due at at.
func armed(name string, at int64) string {
	return fmt.Sprintf(`{"kind":"arm","row":%s,"useId":"Timers","name":%q,"at":%d,"payload":null}`, ticketRef(), name, at)
}

func TestAUnitThatAppliedWakesTheQueuesOfItsEventsItsNearDeadlinesAndItsWrittenRowsEachOnce(t *testing.T) {
	rulestest.Each(t, func(t *testing.T, db *rulestest.DB) {
		createTicketEvents(t, db)
		insertTicket(t, db, uuidColumn, "Leak")
		layout := wakeTicketLayout()
		// fire runs at 5000: a deadline due at 9000 is near, one due past
		// the horizon is the tick's.
		far := 5000 + rules.WakeHorizon.Milliseconds() + 1
		run := func(core rules.Core, wrap func(*rulestest.Tx, rules.Store) rules.Store) ([]string, error) {
			var woken []string
			host := rules.Host{Core: core, Layout: layout, Wake: func(queue string) { woken = append(woken, queue) }}
			err := inTx(t, db, host, wrap, func(host rules.Host, _ *rulestest.Tx) error {
				_, err := rules.Execute(context.Background(), host, "Ticket", fire(uuidKey), nil)
				return err
			})
			return woken, err
		}
		woken, err := run(script(t, changed(`{"rows":[`+touched(1)+`],`+
			`"events":[`+emitted("run", `{}`)+`,`+emitted("run", `{"again":true}`)+`],`+
			`"deadlines":[`+armed("respond", 9000)+`]}`)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"TicketEvent", "TicketDeadline", "TicketChange"}; !slices.Equal(woken, want) {
			t.Errorf("woken %v, want %v", woken, want)
		}

		woken, err = run(script(t, changed(`{"rows":[`+touched(2)+`],"deadlines":[`+armed("respond", far)+`]}`)), nil)
		if err != nil {
			t.Fatal(err)
		}
		if want := []string{"TicketChange"}; !slices.Equal(woken, want) {
			t.Errorf("a deadline due past the horizon: woken %v, want %v", woken, want)
		}

		// A unit that keeps moving applies nothing and wakes nothing.
		woken, err = run(script(t, changed(`{"rows":[`+touched(3)+`],"events":[`+emitted("run", `{}`)+`]}`)), func(_ *rulestest.Tx, store rules.Store) rules.Store {
			wrapped, _ := record(store)
			wrapped.applies = make([]bool, rules.MaxAttempts)
			return wrapped
		})
		if err == nil || len(woken) != 0 {
			t.Errorf("a unit that never applied: err %v, woken %v", err, woken)
		}
	})
}
