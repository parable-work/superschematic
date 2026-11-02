package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// Deadlines as messages of a table's deadline queue (D103): the layout's
// Table.Deadlines names the queue's table and columns. A change set's arm
// deletes the messages of its row, use and name, ready, claimed or dead,
// and inserts one due at its time; a clear deletes them; a row's delete
// deletes every message of the row. So one message per row, use and name
// is live, a claim on an old arming loses its message to the command that
// moved it, and a dead arming the newer one supersedes goes with it, so an
// operator's retry of the queue's dead messages never meets a live one of
// the same deadline. A deadline command spends the arming it runs in its
// own unit, after its row writes and before its deadline changes: a
// worker's by completing the message it claimed, by its id and claim
// token; any other by deleting the live message armed at the time the
// command names, which it read before the run. Either is held to delete one
// row, so a command that moved the deadline first leaves the run nothing to
// spend, and the unit applies nothing. The arming the fired command makes
// again is inserted after the spend, and survives it.

// Deadline is a due deadline a host runs (D103): its row's key in the
// core's form, the use, the deadline's name, the time it was armed to fall
// due at, in epoch milliseconds, and its payload (nil for null).
type Deadline struct {
	Key, UseID, Name string
	At               int64
	Payload          json.RawMessage
}

// Claim is a worker's claim of a deadline's message: its id and its claim
// token, as the queue client's Claim gives them.
type Claim struct{ ID, Token string }

// DeadlineRun is what running a due deadline came to: run, with its
// Result, or nothing run, Why saying why: "claim lost" (another command
// moved, cleared or ran the deadline, or a lease ran out and another claim
// holds the message) or "gone" (the row or the use is gone, its Refusal
// the core's, and the arming spent).
type DeadlineRun struct {
	Ran     bool
	Why     string
	Refusal *Refusal
	Result  *Result
}

// The reasons a DeadlineRun ran nothing.
const (
	WhyClaimLost = "claim lost"
	WhyGone      = "gone"
)

// RunDeadline runs a due deadline of a type through the core in the
// store's transaction, as caller (a behavior worker's subject, D103), at
// the later of the host's time and the deadline's, so a host whose clock
// lags the database's never runs a deadline the database found due before
// it falls due. opts, nil for none, are its row write's stamps, as
// Execute's are. With a claim, its unit completes the claimed message; a
// unit that did not apply because the message is no longer claimed by it
// runs nothing ("claim lost"), and one that did not apply for a row that
// moved runs again, as Execute runs a command again. Without one, it runs
// as Execute runs a deadline command, the arming read before each run: a
// deadline no longer armed at its time is refused not_found, and one not
// due yet invalid_argument, as rules.ts's runDeadline and the Rust
// adapter's refuse them. A refusal of not_found by the core whose row or
// use is gone (the table holds no live row of the key, or the layout no
// such use) spends the arming in a unit of its own and runs nothing
// ("gone"); any other refusal, a not_found of something else a rule
// reached among them, is returned as the error, nothing of it applied, as
// is a failure of the database.
func RunDeadline(ctx context.Context, host Host, typeName string, deadline Deadline, caller Caller, claim *Claim, opts *ExecuteOptions) (*DeadlineRun, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	command := Command{
		Caller: caller,
		Now:    max(host.now(), deadline.At),
		Action: Action{Kind: ActionDeadline, Key: deadline.Key, UseID: deadline.UseID, Name: deadline.Name, At: deadline.At, Payload: deadline.Payload},
	}
	result, runErr := execute(ctx, host, typeName, command, opts, claim)
	switch {
	case runErr == nil:
		return &DeadlineRun{Ran: true, Result: result}, nil
	case errors.Is(runErr, errClaimLost):
		return &DeadlineRun{Why: WhyClaimLost}, nil
	}
	refusal, ok := AsRefusal(runErr)
	if !ok || refusal.Code != CodeNotFound {
		return nil, runErr
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	gone, err := deadlineGone(ctx, host.Store, table, command.Action)
	if err != nil {
		return nil, err
	}
	if !gone {
		return nil, runErr
	}
	spend, err := spendOf(host.Store.Dialect(), table, command.Action, claim)
	if err != nil {
		return nil, err
	}
	applied, err := host.Store.Apply(ctx, []Statement{spend})
	if err != nil {
		return nil, err
	}
	switch {
	case applied:
		return &DeadlineRun{Why: WhyGone, Refusal: refusal}, nil
	case claim == nil:
		// No arming at its time to spend: the arming's read refused the
		// run, or another command spent it since.
		return nil, runErr
	}
	return &DeadlineRun{Why: WhyClaimLost}, nil
}

// deadlineGone reports whether a deadline command's row or use is gone:
// the layout lists no such use of the table, or the table holds no live
// row of the key. A not_found the core gave while both are there is of
// something a rule reached, which the run refuses rather than spends.
func deadlineGone(ctx context.Context, store Store, table *Table, action Action) (bool, error) {
	if useOf(table, action.UseID) == nil {
		return true, nil
	}
	row, err := readRow(ctx, store, table, action.Key)
	if err != nil {
		return false, err
	}
	return row == nil, nil
}

// spendOf is the statement that spends a deadline command's arming, held
// to delete one message: with a claim, the claimed message by its id and
// token; without, the live message of the row, use and name armed at the
// command's time.
func spendOf(dialect Dialect, table *Table, action Action, claim *Claim) (Statement, error) {
	queue := table.Deadlines
	if queue == nil {
		return Statement{}, Internal("a deadline %s.%s of %s ran, and the behaviors' layout gives %s no deadline queue", action.UseID, action.Name, table.Type, table.Type)
	}
	if claim != nil {
		return Statement{
			Text:   fmt.Sprintf(`DELETE FROM %s WHERE "id" = %s AND "claim_token" = %s AND "state" = 'claimed'`, quote(queue.Table), uuidPlaceholder(dialect, 1), uuidPlaceholder(dialect, 2)),
			Values: []any{claim.ID, claim.Token},
			Expect: 1,
		}, nil
	}
	key, err := keyValue(table, action.Key)
	if err != nil {
		return Statement{}, err
	}
	return Statement{
		Text: fmt.Sprintf(`DELETE FROM %s WHERE %s = %s AND %s = %s AND %s = %s AND %s = %s AND "state" IN ('ready', 'claimed')`,
			quote(queue.Table), quote(queue.RowKey), placeholder(dialect, 1), quote(queue.Use), placeholder(dialect, 2),
			quote(queue.Name), placeholder(dialect, 3), quote(queue.At), placeholder(dialect, 4)),
		Values: []any{key, action.UseID, action.Name, action.At},
		Expect: 1,
	}, nil
}

// uuidPlaceholder is the nth placeholder of a UUID column's value bound as
// text: cast on Postgres.
func uuidPlaceholder(dialect Dialect, n int) string {
	if dialect == DialectPostgres {
		return placeholder(dialect, n) + "::uuid"
	}
	return placeholder(dialect, n)
}

// claimHeld reports whether the worker still holds its claim of the
// message: the message is there, claimed, with the claim's token.
func claimHeld(ctx context.Context, store Store, table *Table, claim *Claim) (bool, error) {
	dialect := store.Dialect()
	rows, err := store.Query(ctx, fmt.Sprintf(`SELECT 1 FROM %s WHERE "id" = %s AND "claim_token" = %s AND "state" = 'claimed'`, quote(table.Deadlines.Table), uuidPlaceholder(dialect, 1), uuidPlaceholder(dialect, 2)), claim.ID, claim.Token)
	if err != nil {
		return false, err
	}
	return len(rows) > 0, nil
}

// stillArmed refuses a deadline command whose deadline is not armed at the
// time it names, as not_found, or whose time falls after the command's, as
// invalid_argument, in the reference host's words
// (superschematic-rules-testkit's MemoryHost).
func stillArmed(ctx context.Context, store Store, table *Table, command Command) error {
	action := command.Action
	queue := table.Deadlines
	dialect := store.Dialect()
	key, err := keyValue(table, action.Key)
	if err != nil {
		return err
	}
	rows, err := store.Query(ctx, fmt.Sprintf(`SELECT %s, %s FROM %s WHERE %s = %s AND %s = %s AND %s = %s AND "state" IN ('ready', 'claimed')`,
		quote(queue.At), quote(queue.Payload), quote(queue.Table), quote(queue.RowKey), placeholder(dialect, 1),
		quote(queue.Use), placeholder(dialect, 2), quote(queue.Name), placeholder(dialect, 3)), key, action.UseID, action.Name)
	if err != nil {
		return err
	}
	armed := false
	for _, row := range rows {
		at, err := int64Of(row[0])
		if err != nil {
			return Internal("%s's deadline %s.%s: %v", table.Type, action.UseID, action.Name, err)
		}
		armed = armed || at == action.At
	}
	if !armed {
		return &Refusal{Code: CodeNotFound, Status: 404, Class: "EngineError", Message: fmt.Sprintf("%s %s has no deadline %s.%s armed at %d", table.Type, action.Key, action.UseID, action.Name, action.At)}
	}
	if action.At > command.Now {
		return &Refusal{Code: CodeInvalidArgument, Status: 400, Class: "EngineError", Message: fmt.Sprintf("deadline %s.%s of %s %s falls due at %d, after %d", action.UseID, action.Name, table.Type, action.Key, action.At, command.Now)}
	}
	return nil
}

// deadlineStatements are the statements of one deadline change: an arm's
// delete of the messages of its row, use and name, whatever their state,
// and insert of one due at its time with its payload; a clear's delete.
// Neither is held to a count. A change of a type whose layout names no
// deadline queue, or of a kind the adapter does not know, refuses the
// command as internal.
func deadlineStatements(layout *Layout, dialect Dialect, change DeadlineChange) ([]Statement, error) {
	if change.Kind != DeadlineArm && change.Kind != DeadlineClear {
		raw := change.Raw
		if len(raw) == 0 {
			raw = json.RawMessage(jsonText(change))
		}
		return nil, Internal("the adapter takes no deadline change of this kind: %s", compactText(raw))
	}
	if change.Row.Key == nil {
		return nil, Internal("a deadline %s.%s of %s reached the adapter without its row's key", change.UseID, change.Name, change.Row.Type)
	}
	table, err := layout.table(change.Row.Type)
	if err != nil {
		return nil, err
	}
	queue := table.Deadlines
	if queue == nil {
		return nil, Internal("%s's use %s arms the deadline %s, and the behaviors' layout gives %s no deadline queue", change.Row.Type, change.UseID, change.Name, change.Row.Type)
	}
	key, err := keyValue(table, *change.Row.Key)
	if err != nil {
		return nil, err
	}
	clear := Statement{
		Text: fmt.Sprintf(`DELETE FROM %s WHERE %s = %s AND %s = %s AND %s = %s`,
			quote(queue.Table), quote(queue.RowKey), placeholder(dialect, 1), quote(queue.Use), placeholder(dialect, 2), quote(queue.Name), placeholder(dialect, 3)),
		Values: []any{key, change.UseID, change.Name},
		Expect: AnyRows,
	}
	if change.Kind == DeadlineClear {
		return []Statement{clear}, nil
	}
	var payload any
	if !isNull(change.Payload) {
		if !json.Valid(change.Payload) {
			return nil, Internal("the payload of %s's deadline %s.%s is no JSON: %s", change.Row.Type, change.UseID, change.Name, change.Payload)
		}
		payload = compactText(change.Payload)
	}
	columns := strings.Join([]string{quote(queue.RowKey), quote(queue.Use), quote(queue.Name), quote(queue.At), quote(queue.Payload), `"due_at"`}, ", ")
	var values string
	if dialect == DialectPostgres {
		values = "$1, $2, $3, $4, $5::jsonb, to_timestamp($4::bigint / 1000.0)"
	} else {
		values = "?1, ?2, ?3, ?4, ?5, strftime('%Y-%m-%dT%H:%M:%fZ', ?4 / 1000.0, 'unixepoch')"
	}
	arm := Statement{
		Text:   fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quote(queue.Table), columns, values),
		Values: []any{key, change.UseID, change.Name, change.At, payload},
		Expect: 1,
	}
	return []Statement{clear, arm}, nil
}

// ArmedDeadline is a deadline armed on a row: its use, its name, the time
// it falls due at in epoch milliseconds, and its payload (null for none).
type ArmedDeadline struct {
	UseID   string          `json:"useId"`
	Name    string          `json:"name"`
	At      int64           `json:"at"`
	Payload json.RawMessage `json:"payload"`
}

// ReadDeadlines reads a row's armed deadlines, the live messages of its
// table's deadline queue, by use id and then name in code point order;
// none for a table whose layout names no deadline queue.
func ReadDeadlines(ctx context.Context, host Host, typeName, key string) ([]ArmedDeadline, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	queue := table.Deadlines
	if queue == nil {
		return []ArmedDeadline{}, nil
	}
	dialect := host.Store.Dialect()
	collate := ""
	if dialect == DialectPostgres {
		collate = ` COLLATE "C"`
	}
	bound, err := keyValue(table, key)
	if err != nil {
		return nil, err
	}
	rows, err := host.Store.Query(ctx, fmt.Sprintf(`SELECT %s, %s, %s, %s FROM %s WHERE %s = %s AND "state" IN ('ready', 'claimed') ORDER BY %s%s, %s%s`,
		quote(queue.Use), quote(queue.Name), quote(queue.At), quote(queue.Payload), quote(queue.Table), quote(queue.RowKey), placeholder(dialect, 1),
		quote(queue.Use), collate, quote(queue.Name), collate), bound)
	if err != nil {
		return nil, err
	}
	payloadColumn := Column{Field: "payload", Column: queue.Payload, Type: "Generic.JSON"}
	out := make([]ArmedDeadline, 0, len(rows))
	for _, row := range rows {
		at, err := int64Of(row[2])
		if err != nil {
			return nil, Internal("%s %s's deadline %v.%v: %v", typeName, key, row[0], row[1], err)
		}
		payload, present, err := fromColumn(row[3], payloadColumn, table)
		if err != nil {
			return nil, err
		}
		if !present {
			payload = json.RawMessage(`null`)
		}
		out = append(out, ArmedDeadline{UseID: textOf(row[0]), Name: textOf(row[1]), At: at, Payload: payload})
	}
	return out, nil
}

// int64Of is an integer column's value as an int64.
func int64Of(value any) (int64, error) {
	return versionOf(value)
}
