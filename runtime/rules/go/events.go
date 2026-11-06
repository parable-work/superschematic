package rules

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// A behavior's own events as messages of a table's event queue (D104): the
// layout's Table.Events names the queue's table and columns. A change
// set's event is written in the command's unit, after the sub-table rows
// and before the deadline changes, as one message of the event queue of
// its row's table: the row's key, the use that emitted it, its kind, the
// command's time in epoch milliseconds and its payload. On Postgres its
// due_at and created_at are the statement's clock_timestamp(), so one
// transaction's events of a row keep their order in the queue's claim,
// which holds each row's line in the order written; on SQLite the table's
// defaults stamp them to the millisecond. An insert never conflicts, so it
// is fenced on nothing. A row's delete leaves its events, which the app
// may still need to hear. A behavior worker claims each message and hands
// it to the project's handler in a transaction that first spends it
// (SpendEvent), so the handler's writes and the completion commit together
// or not at all; delivery is at least once.

// EventRun is what spending an event's claim came to (D104): spent, or
// nothing spent, Why saying why ("claim lost": the lease ran out and
// another claim holds the message, or another run spent it).
type EventRun struct {
	Ran bool
	Why string
}

// SpendEvent spends a worker's claim of a message of a type's event queue
// in the store's transaction (D104): it deletes the claimed message by its
// id and claim token, held to delete one row; a message the claim no
// longer holds is a claim lost, and nothing is spent. The handler runs in
// the same transaction after it, so its writes and the completion commit
// together. Without a claim nothing is spent and it runs. A type whose
// table keeps no event queue is refused as internal.
func SpendEvent(ctx context.Context, host Host, typeName string, claim *Claim) (*EventRun, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	if table.Events == nil {
		return nil, Internal("a claim of an event of %s was spent, and the behaviors' layout gives %s no event queue", typeName, typeName)
	}
	if claim == nil {
		return &EventRun{Ran: true}, nil
	}
	applied, err := host.Store.Apply(ctx, []Statement{claimSpend(host.Store.Dialect(), table.Events.Table, claim)})
	if err != nil {
		return nil, err
	}
	if !applied {
		return &EventRun{Why: WhyClaimLost}, nil
	}
	return &EventRun{Ran: true}, nil
}

// QueuedEvent is a message of a table's event queue as an adapter reads it
// back: the row's key in the core's form, the use, the kind, the command's
// time in epoch milliseconds and the payload (null for none).
type QueuedEvent struct {
	Key, UseID, Kind string
	At               int64
	Payload          json.RawMessage
}

// ReadQueuedEvents reads every message of a type's event queue, whatever
// its state, in the order written: created_at then id on Postgres, where
// each insert has its own clock_timestamp(), and created_at then the rowid
// on SQLite, whose millisecond created_at one command's events share (as
// the TypeScript and Rust adapters read them); none for a table with no
// event queue.
func ReadQueuedEvents(ctx context.Context, host Host, typeName string) ([]QueuedEvent, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	queue := table.Events
	if queue == nil {
		return []QueuedEvent{}, nil
	}
	tie := `"id"`
	if host.Store.Dialect() == DialectSQLite {
		tie = "rowid"
	}
	rows, err := host.Store.Query(ctx, fmt.Sprintf(`SELECT %s, %s, %s, %s, %s FROM %s ORDER BY "created_at", %s`,
		quote(queue.RowKey), quote(queue.Use), quote(queue.Kind), quote(queue.At), quote(queue.Payload), quote(queue.Table), tie))
	if err != nil {
		return nil, err
	}
	payloadColumn := Column{Field: "payload", Column: queue.Payload, Type: "Generic.JSON"}
	out := make([]QueuedEvent, 0, len(rows))
	for _, row := range rows {
		key, err := keyOfColumn(table, row[0])
		if err != nil {
			return nil, err
		}
		at, err := int64Of(row[3])
		if err != nil {
			return nil, Internal("%s %s's event %v of %v: %v", typeName, key, row[2], row[1], err)
		}
		payload, present, err := fromColumn(row[4], payloadColumn, table)
		if err != nil {
			return nil, err
		}
		if !present {
			payload = json.RawMessage(`null`)
		}
		out = append(out, QueuedEvent{Key: key, UseID: textOf(row[1]), Kind: textOf(row[2]), At: at, Payload: payload})
	}
	return out, nil
}

// eventStatement is the insert of one event of a change set as a message
// of its row's table's event queue (D104), at the command's time: the
// row's key bound as its table's key binds (the command's own key for an
// event of its row that names none), the use, the kind, the time and the
// payload's JSON text, NULL for null. An event of a row whose table keeps
// no event queue refuses the command as internal.
func eventStatement(layout *Layout, dialect Dialect, typeName string, command Command, event OutboxEvent) (Statement, error) {
	key := ""
	switch {
	case event.Row.Key != nil:
		key = *event.Row.Key
	case event.Row.Type == typeName && keyOfCommand(command) != "":
		key = keyOfCommand(command)
	default:
		return Statement{}, Internal("an event %s of %s's use %s reached the adapter without its row's key", event.Kind, event.Row.Type, event.UseID)
	}
	table, err := layout.table(event.Row.Type)
	if err != nil {
		return Statement{}, err
	}
	queue := table.Events
	if queue == nil {
		return Statement{}, Internal("%s of %s emits %s of %s on %s %s, and %s's table %s keeps no event queue in the behaviors' layout: a behavior whose declaration lists its events gives its table one (D104)",
			command.Action.name(), typeName, event.Kind, event.UseID, event.Row.Type, key, event.Row.Type, table.Table)
	}
	bound, err := keyValue(table, key)
	if err != nil {
		return Statement{}, err
	}
	var payload any
	if !isNull(event.Payload) {
		if !json.Valid(event.Payload) {
			return Statement{}, Internal("the payload of %s's event %s of %s is no JSON: %s", event.Row.Type, event.Kind, event.UseID, event.Payload)
		}
		payload = compactText(event.Payload)
	}
	columns := []string{quote(queue.RowKey), quote(queue.Use), quote(queue.Kind), quote(queue.At), quote(queue.Payload)}
	var values string
	if dialect == DialectPostgres {
		columns = append(columns, `"due_at"`, `"created_at"`)
		values = "$1, $2, $3, $4, $5::jsonb, clock_timestamp(), clock_timestamp()"
	} else {
		values = "?1, ?2, ?3, ?4, ?5"
	}
	return Statement{
		Text:   fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quote(queue.Table), strings.Join(columns, ", "), values),
		Values: []any{bound, event.UseID, event.Kind, command.Now, payload},
		Expect: 1,
	}, nil
}
