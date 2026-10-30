package rules

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"sort"
	"strings"
	"time"
)

// The adapter of the behaviors' pure core (D103): the one host every Go
// database runs it through, generic and small, which holds no behavior's
// name. Execute runs one command inside the caller's transaction, as the
// core's README (runtime/engine/rust/rules/README.md, "The host loop" and
// "The adapter") says a host does: it runs the core with the answers it
// has; for a step's reads it reads each row by its key through the store
// (ReadRow), a soft-deletable table's live rows alone, and answers; for a
// step's locks it takes each lock and records it; and for a done step it
// returns the refusal or applies the change set whole, every row write
// fenced on the version the change set expects it at. On Postgres a lock is
// pg_advisory_xact_lock on a hash of the key, which the transaction holds
// until it ends. SQLite takes none, so there the unit first checks that
// each read made under a lock still answers as it did (LocksFenced): of two
// commands that take one key, the second then runs again over the first's
// writes. When a fenced write finds its row moved, or a check its read
// changed, the unit applies nothing, the adapter forgets its answers and
// runs the command again from fresh state with the same request id, up to
// MaxAttempts runs, and then refuses it as busy. A kind of read, step or
// write it does not know (the core's enums are open) refuses the command as
// internal, and so do sub-table rows, outbox events and deadlines until
// each has a table (D103's order of the work, step 3).
//
// A generated repository calls it: each field-anchored use's operations
// run RunOperation, and a table that composes behaviors creates, updates
// and deletes its rows through the core (RunCreate, RunUpdate, RunDelete)
// so its uses' initializers, guards and validation run, and binds in an
// upsert's insert the owned fields the core's create of each row gives it
// (PrepareCreate). A hand-written server runs Execute on a store of its
// own transaction. The owned columns are read-only in a repository's
// inputs; a raw SQL write of one is never refused (D103).

const (
	// MaxAttempts is how many times Execute runs a command whose row moved
	// before it refuses it as busy (D103).
	MaxAttempts = 8
	// MaxRounds is how many runs of one attempt Execute makes before it
	// refuses a run still asking as internal, the core's MAX_ROUNDS.
	MaxRounds = 256
)

// Host is what a command runs with: per transaction.
type Host struct {
	// Core is the core the command runs on; nil is Native().
	Core Core
	// Layout maps the core's logical names onto tables and columns.
	Layout *Layout
	// Store is the caller's transaction.
	Store Store
	// Now is the time a command the wrappers build runs at, in epoch
	// milliseconds; time.Now().UnixMilli() when nil.
	Now func() int64
	// Request is a request id unique per command; a random UUID's
	// hyphenated text when nil.
	Request func() string
}

func (h Host) core() Core {
	if h.Core == nil {
		return Native()
	}
	return h.Core
}

func (h Host) now() int64 {
	if h.Now == nil {
		return time.Now().UnixMilli()
	}
	return h.Now()
}

func (h Host) request() string {
	if h.Request == nil {
		return hyphenatedBytes(randomUUID())
	}
	return h.Request()
}

// check refuses a host that cannot run a command: no layout or no store.
func (h Host) check() error {
	if h.Layout == nil {
		return errors.New("rules: the host has no layout")
	}
	if h.Store == nil {
		return errors.New("rules: the host has no store")
	}
	return nil
}

// ExecuteOptions is what Execute takes beside the command.
type ExecuteOptions struct {
	// OwnColumns are the own columns of the command's own row as the
	// store binds them, by column: a repository's encoding of its input
	// (the driver's values, audit stamps among them), bound in place of the
	// change set's own fields by a create's insert and an update's set,
	// columns sorted by name. Nil: the change set's own fields, converted
	// by the layout.
	OwnColumns map[string]any
	// SoftDelete are the columns a delete of the command's own row sets in
	// place of deleting it, by column, as the store binds them, fenced on
	// the version as the delete would be.
	SoftDelete map[string]any
}

// CreateOptions is what a create through the core takes beside its own
// fields.
type CreateOptions struct {
	// ID is the key in its wire form; "" for one the adapter makes.
	ID string
	// Params are each use's create parameters, by use id.
	Params map[string]any
	// ExecuteOptions are ignored by PrepareCreate.
	ExecuteOptions
}

// Owned is a row's owned fields by use id, then declared name, as the core
// gives them.
type Owned map[string]map[string]json.RawMessage

// Result is what an applied command came to.
type Result struct {
	// Changes is the change set applied.
	Changes ChangeSet
	// Result is what the command returns: an operation's result; null for
	// the rest.
	Result json.RawMessage
	// Key is the command's row's key: a create's (the adapter's when it
	// named none); "" for define and publish.
	Key string
	// Attempts is how many times the command ran: 1 unless a row moved.
	Attempts int
}

// Execute runs one command on a type of the layout to its outcome, in the
// store's transaction: answers its reads, takes its locks, applies its
// change set whole with each row fenced on its version, and runs it again
// from fresh state when a row moved, up to MaxAttempts times. It returns a
// *Refusal for the core's refusal, a busy one after the last attempt or
// when the store's error wraps ErrBusy, and an internal one for a kind it
// does not know or a type the layout lacks; any other failure of the
// database is the store's error.
//
// A command with no request id gets one (host.Request), kept for every
// run; a create with no id gets one when its key is a UUID or a string,
// and is refused otherwise, since the change set names every row it writes
// by its key. A create of a key the table holds is refused as conflict, as
// the reference host refuses it.
func Execute(ctx context.Context, host Host, typeName string, command Command, opts *ExecuteOptions) (*Result, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &ExecuteOptions{}
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	core := host.core()
	refused, err := host.Layout.refusalOf(core, typeName)
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return nil, refused
	}
	run, err := prepared(host, table, command)
	if err != nil {
		return nil, err
	}
	if err := refuseOwnedWrites(table, run); err != nil {
		return nil, err
	}
	for attempt := 1; attempt <= MaxAttempts; attempt++ {
		changes, reads, err := settle(ctx, host, core, table.Spec, run)
		if err != nil {
			return nil, busyOr(err, typeName, run, attempt)
		}
		fences, err := fencesOf(host, reads)
		if err != nil {
			return nil, err
		}
		statements, err := statementsOf(ctx, host, typeName, run, changes, opts)
		if err != nil {
			return nil, busyOr(err, typeName, run, attempt)
		}
		applied, err := host.Store.Apply(ctx, append(fences, statements...))
		if err != nil {
			return nil, busyOr(err, typeName, run, attempt)
		}
		if applied {
			result := changes.Result
			if len(result) == 0 {
				result = json.RawMessage(`null`)
			}
			return &Result{Changes: *changes, Result: result, Key: keyOfCommand(run), Attempts: attempt}, nil
		}
	}
	return nil, BusyRefusal(typeName, run, MaxAttempts)
}

// busyOr is a store's error as a command returns it: the busy refusal,
// after the runs made so far, when it wraps ErrBusy (another's lock kept
// the store from the database, and nothing was written); the error itself
// otherwise.
func busyOr(err error, typeName string, command Command, attempts int) error {
	if errors.Is(err, ErrBusy) {
		return BusyRefusal(typeName, command, attempts)
	}
	return err
}

// RunOperation runs an operation of a use on a row and returns its result.
// Nil params, or params that encode as null, are {}.
func RunOperation(ctx context.Context, host Host, typeName, key, useID, operation string, params any, caller Caller, opts *ExecuteOptions) (json.RawMessage, error) {
	raw := emptyObject
	if params != nil {
		encoded, err := encodeValue(params)
		if err != nil {
			return nil, fmt.Errorf("rules: encode the parameters of %s: %w", operation, err)
		}
		if !isNull(encoded) {
			raw = encoded
		}
	}
	command := commandOf(host, caller, Action{Kind: ActionOperation, Key: key, UseID: useID, Operation: operation, Params: raw})
	result, err := Execute(ctx, host, typeName, command, opts)
	if err != nil {
		return nil, err
	}
	return result.Result, nil
}

// RunCreate creates a row through the core, its uses' initializers,
// validation and guards run, and returns its key in its wire form.
func RunCreate(ctx context.Context, host Host, typeName string, own map[string]any, caller Caller, opts *CreateOptions) (string, error) {
	if opts == nil {
		opts = &CreateOptions{}
	}
	action, err := createAction(own, opts)
	if err != nil {
		return "", err
	}
	result, err := Execute(ctx, host, typeName, commandOf(host, caller, action), &opts.ExecuteOptions)
	if err != nil {
		return "", err
	}
	return result.Key, nil
}

// RunUpdate updates a row's own fields through the core with a merge
// patch; a patch that names an owned field is refused (invalid_instance).
func RunUpdate(ctx context.Context, host Host, typeName, key string, patch map[string]any, caller Caller, opts *ExecuteOptions) error {
	encoded, err := encodeValues(patch)
	if err != nil {
		return fmt.Errorf("rules: encode the patch of %s %s: %w", typeName, key, err)
	}
	_, err = Execute(ctx, host, typeName, commandOf(host, caller, Action{Kind: ActionUpdate, Key: key, Patch: encoded}), opts)
	return err
}

// RunDelete deletes a row through the core, its uses' guards run;
// opts.SoftDelete stamps it deleted in place of deleting it.
func RunDelete(ctx context.Context, host Host, typeName, key string, caller Caller, opts *ExecuteOptions) error {
	_, err := Execute(ctx, host, typeName, commandOf(host, caller, Action{Kind: ActionDelete, Key: key}), opts)
	return err
}

// PrepareCreate runs a create of a row through the core to its change set
// and applies nothing: its uses' create parameters, validation, guards and
// initializers run, its reads are answered and its locks taken as Execute
// answers and takes them, and it returns the owned fields the change set
// gives the row, or the core's refusal. An upsert's insert binds them, so
// the row it inserts holds what a create would have given it, in the
// caller's transaction, which holds the locks. A create with no id runs
// with none, the key being the insert's to make. A change set that writes
// anything but the one row, or that reads under a lock a store fences
// (only its unit could check them), refuses the create as internal.
func PrepareCreate(ctx context.Context, host Host, typeName string, own map[string]any, caller Caller, opts *CreateOptions) (Owned, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	if opts == nil {
		opts = &CreateOptions{}
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	core := host.core()
	refused, err := host.Layout.refusalOf(core, typeName)
	if err != nil {
		return nil, err
	}
	if refused != nil {
		return nil, refused
	}
	action, err := createAction(own, opts)
	if err != nil {
		return nil, err
	}
	command := commandOf(host, caller, action)
	if err := refuseOwnedWrites(table, command); err != nil {
		return nil, err
	}
	changes, reads, err := settle(ctx, host, core, table.Spec, command)
	if err != nil {
		return nil, busyOr(err, typeName, command, 1)
	}
	alone := len(changes.Rows) == 1 && changes.Rows[0].Kind == RowCreate && changes.Rows[0].Type == typeName
	if !alone || len(changes.SubRows) > 0 || len(changes.Events) > 0 || len(changes.Deadlines) > 0 {
		return nil, Internal("an upsert of %s inserts the row a create of the core gives it, and that create writes more than the row; create the row with a create of its own", typeName)
	}
	fences, err := fencesOf(host, reads)
	if err != nil {
		return nil, err
	}
	if len(fences) > 0 {
		return nil, Internal("an upsert of %s binds what a create of the core gives the row, and that create reads under a lock this store holds only in a unit of its own; create the row with a create of its own", typeName)
	}
	owned := Owned{}
	for useID, fields := range changes.Rows[0].Owned {
		owned[useID] = fields
	}
	return owned, nil
}

// ReadRow reads a row of a type by its key as the core reads it: its own
// fields and owned fields, each column with no value absent, and its
// version; nil, nil when the table has no such row, or none that is live.
func ReadRow(ctx context.Context, host Host, typeName, key string) (*Row, error) {
	if err := host.check(); err != nil {
		return nil, err
	}
	table, err := host.Layout.table(typeName)
	if err != nil {
		return nil, err
	}
	return readRow(ctx, host.Store, table, key)
}

func readRow(ctx context.Context, store Store, table *Table, key string) (*Row, error) {
	dialect := store.Dialect()
	selected := make([]string, 0, len(table.Own)+1)
	at := 0
	alias := func(column string) {
		// Each column under an alias of its place, so no two columns'
		// names meet in the row.
		selected = append(selected, fmt.Sprintf("%s AS c%d", quote(column), at))
		at++
	}
	for _, column := range table.Own {
		alias(column.Column)
	}
	for _, use := range table.Uses {
		for _, field := range use.Fields {
			alias(field.Column.Column)
		}
	}
	selected = append(selected, quote(table.Version)+" AS v")
	text := fmt.Sprintf("SELECT %s FROM %s WHERE %s = %s%s", strings.Join(selected, ", "), quote(table.Table), quote(table.Key.Column), placeholder(dialect, 1), live(table))
	bound, err := keyValue(table, key)
	if err != nil {
		return nil, err
	}
	rows, err := store.Query(ctx, text, bound)
	if err != nil {
		return nil, err
	}
	if len(rows) == 0 {
		return nil, nil
	}
	values := rows[0]
	if len(values) != at+1 {
		return nil, Internal("a read of %s selected %d columns and the store gave %d", table.Type, at+1, len(values))
	}
	row := &Row{Own: map[string]json.RawMessage{}, Owned: map[string]map[string]json.RawMessage{}}
	at = 0
	for _, column := range table.Own {
		value, present, err := fromColumn(values[at], column, table)
		if err != nil {
			return nil, err
		}
		if present {
			row.Own[column.Field] = value
		}
		at++
	}
	for _, use := range table.Uses {
		fields := map[string]json.RawMessage{}
		for _, field := range use.Fields {
			value, present, err := fromColumn(values[at], field.Column, table)
			if err != nil {
				return nil, err
			}
			if present {
				fields[field.Name] = value
			}
			at++
		}
		if len(fields) > 0 {
			row.Owned[use.ID] = fields
		}
	}
	version, err := versionOf(values[at])
	if err != nil {
		return nil, Internal("%s %s's version: %v", table.Type, key, err)
	}
	row.Version = version
	return row, nil
}

// commandOf is the command a wrapper builds: the caller, the host's time
// and a request id of its own.
func commandOf(host Host, caller Caller, action Action) Command {
	return Command{Caller: caller, Now: host.now(), Action: action, Request: host.request()}
}

// createAction is a create's action from a wrapper's own fields and
// options.
func createAction(own map[string]any, opts *CreateOptions) (Action, error) {
	encodedOwn, err := encodeValues(own)
	if err != nil {
		return Action{}, fmt.Errorf("rules: encode a create's own fields: %w", err)
	}
	action := Action{Kind: ActionCreate, ID: opts.ID, Own: encodedOwn}
	if opts.Params != nil {
		params, err := encodeValues(opts.Params)
		if err != nil {
			return Action{}, fmt.Errorf("rules: encode a create's parameters: %w", err)
		}
		action.Params, err = json.Marshal(params)
		if err != nil {
			return Action{}, fmt.Errorf("rules: encode a create's parameters: %w", err)
		}
	}
	return action, nil
}

// encodeValues is each value as the core's JSON.
func encodeValues(values map[string]any) (map[string]json.RawMessage, error) {
	if values == nil {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(values))
	for name, value := range values {
		encoded, err := encodeValue(value)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = encoded
	}
	return out, nil
}

// encodeValue is a Go value as the core's JSON: a json.RawMessage as it
// is, nil as null, any other as encoding/json writes it.
func encodeValue(value any) (json.RawMessage, error) {
	if raw, ok := value.(json.RawMessage); ok {
		if len(raw) == 0 {
			return json.RawMessage(`null`), nil
		}
		if !json.Valid(raw) {
			return nil, fmt.Errorf("%q is no JSON", raw)
		}
		return raw, nil
	}
	return json.Marshal(value)
}

// prepared is the command as every run of it runs: a request id, and a
// create's key.
func prepared(host Host, table *Table, command Command) (Command, error) {
	run := command
	if run.Request == "" {
		run.Request = host.request()
	}
	if run.Action.Kind == ActionCreate && run.Action.ID == "" {
		key, err := newKey(table)
		if err != nil {
			return Command{}, err
		}
		run.Action.ID = key
	}
	return run, nil
}

// newKey is a key for a create that names none: a random UUID's wire form,
// or its hyphenated text for a string key.
func newKey(table *Table) (string, error) {
	switch {
	case uuidTypes[table.Key.Type]:
		raw := randomUUID()
		return base62(new(big.Int).SetBytes(raw[:])), nil
	case table.Key.Type == "string":
		return hyphenatedBytes(randomUUID()), nil
	}
	return "", &Refusal{
		Code:    CodeInvalidArgument,
		Status:  400,
		Class:   "EngineError",
		Message: fmt.Sprintf("a create of %s names no key, and the adapter makes none for a key of type %s: pass the id", table.Type, table.Key.Type),
	}
}

// keyOfCommand is the key of a command's row: a create's id or the key it
// names; "" for a define or publish check.
func keyOfCommand(command Command) string {
	switch command.Action.Kind {
	case ActionCreate:
		return command.Action.ID
	case ActionUpdate, ActionDelete, ActionOperation, ActionDeadline:
		return command.Action.Key
	}
	return ""
}

// refuseOwnedWrites refuses a create or an update whose own fields name a
// field a behavior owns under its field's IR name (statusEnteredAt), as
// the core refuses one that names a field anchor's own field (rule owned):
// only the behavior's operations change it. The anchor field itself is
// left to the core, which refuses it in its own words.
func refuseOwnedWrites(table *Table, command Command) error {
	var own map[string]json.RawMessage
	switch command.Action.Kind {
	case ActionCreate:
		own = command.Action.Own
	case ActionUpdate:
		own = command.Action.Patch
	default:
		return nil
	}
	ownFields := map[string]bool{}
	for _, column := range table.Own {
		ownFields[column.Field] = true
	}
	var issues []ownedIssue
	for _, use := range table.Uses {
		for _, field := range use.Fields {
			if _, named := own[field.Field]; (field.Value && use.Anchor != "") || ownFields[field.Field] || !named {
				continue
			}
			issues = append(issues, ownedIssue{
				Path:    field.Field,
				Rule:    "owned",
				Message: fmt.Sprintf("%s is behavior %s's field: only its operations change it", field.Field, use.Behavior),
			})
		}
	}
	if len(issues) == 0 {
		return nil
	}
	parts := make([]string, len(issues))
	for i, issue := range issues {
		parts[i] = issue.Path + ": " + issue.Message
	}
	details, err := json.Marshal(struct {
		Issues []ownedIssue `json:"issues"`
	}{issues})
	if err != nil {
		return fmt.Errorf("rules: encode the owned fields' issues: %w", err)
	}
	return &Refusal{
		Code:    CodeInvalidInstance,
		Status:  422,
		Class:   "InstanceValidationError",
		Message: table.Type + ": " + strings.Join(parts, "; "),
		Details: details,
	}
}

// settle runs the core until its outcome: it answers each read through
// the store, recording with it how many locks were held when it was made,
// and takes each lock asked for. It returns the change set and the answers
// the core decided on, or the core's refusal.
func settle(ctx context.Context, host Host, core Core, spec TypeSpec, command Command) (*ChangeSet, []Answered, error) {
	var reads []Answered
	var locks []string
	for round := 0; round < MaxRounds; round++ {
		step, err := core.Run(spec, command, Answers{Reads: reads, Locks: locks})
		if err != nil {
			return nil, nil, err
		}
		switch {
		case step.Done != nil:
			switch outcome := step.Done; {
			case outcome.Refused != nil:
				return nil, nil, outcome.Refused
			case outcome.Changed != nil:
				return outcome.Changed, reads, nil
			default:
				return nil, nil, Internal("the core's outcome is of a kind the adapter does not take: %s", jsonText(outcome))
			}
		case step.Read != nil:
			for _, request := range step.Read {
				answer, err := answerOf(ctx, host, request)
				if err != nil {
					return nil, nil, err
				}
				answered := Answered{Request: request, Answer: answer, Held: len(locks)}
				replaced := false
				for i := range reads {
					if reads[i].Request == request {
						reads[i] = answered
						replaced = true
						break
					}
				}
				if !replaced {
					reads = append(reads, answered)
				}
			}
		case step.Lock != nil:
			for _, key := range step.Lock {
				if contains(locks, key) {
					continue
				}
				if err := host.Store.Lock(ctx, key); err != nil {
					return nil, nil, err
				}
				locks = append(locks, key)
			}
		default:
			return nil, nil, Internal("the core's step is of a kind the adapter does not take: %s", jsonText(step))
		}
	}
	return nil, nil, Internal("%s of %s: the run still asked for reads or locks after %d rounds", command.Action.name(), spec.Name, MaxRounds)
}

// jsonText is a value's JSON, for a message.
func jsonText(value any) string {
	out, err := json.Marshal(value)
	if err != nil {
		return fmt.Sprintf("%+v", value)
	}
	return string(out)
}

func contains(list []string, item string) bool {
	for _, each := range list {
		if each == item {
			return true
		}
	}
	return false
}

// answerOf is the answer to one of the core's reads, through the store; a
// kind of read the adapter does not take refuses the command as internal.
func answerOf(ctx context.Context, host Host, request ReadRequest) (Answer, error) {
	switch request.Kind {
	case ReadKindRow:
		table := host.Layout.Types[request.Type]
		if table == nil {
			return Answer{Kind: ReadKindRow}, nil
		}
		row, err := readRow(ctx, host.Store, table, request.Key)
		if err != nil {
			return Answer{}, err
		}
		return Answer{Kind: ReadKindRow, Row: row}, nil
	case ReadKindHasRows:
		table := host.Layout.Types[request.Type]
		if table == nil {
			return Answer{Kind: ReadKindHasRows}, nil
		}
		where := ""
		if table.Live != nil {
			where = fmt.Sprintf(" WHERE %s IS NULL", quote(table.Live.Column))
		}
		rows, err := host.Store.Query(ctx, fmt.Sprintf("SELECT 1 AS one FROM %s%s LIMIT 1", quote(table.Table), where))
		if err != nil {
			return Answer{}, err
		}
		return Answer{Kind: ReadKindHasRows, HasRows: len(rows) > 0}, nil
	case ReadKindSpec:
		table := host.Layout.Types[request.Type]
		if table == nil {
			return Answer{Kind: ReadKindSpec}, nil
		}
		spec := table.Spec
		return Answer{Kind: ReadKindSpec, Spec: &spec}, nil
	default:
		return Answer{}, Internal("the adapter answers no read of this kind: %s", jsonText(request))
	}
}

// fencesOf is the checks that hold a store whose locks are fenced to the
// reads a command made under a lock, each a condition the unit checks
// before its writes: a row read is still at the version read, a row read
// absent is still absent, and a table read to have rows still has them, or
// still none, each of a soft-deletable table's live rows. A read of a type
// the layout lacks, or of a spec, answers from the layout and needs none.
// Answers made before the latest lock are asked for again, so a command
// that takes a lock decides on reads made under it alone.
func fencesOf(host Host, reads []Answered) ([]Statement, error) {
	if host.Store.Locks() != LocksFenced {
		return nil, nil
	}
	dialect := host.Store.Dialect()
	var fences []Statement
	for _, read := range reads {
		if read.Held == 0 || read.Request.Kind == ReadKindSpec {
			continue
		}
		table := host.Layout.Types[read.Request.Type]
		if table == nil {
			continue
		}
		switch {
		case read.Request.Kind == ReadKindRow && read.Answer.Kind == ReadKindRow:
			key, err := keyValue(table, read.Request.Key)
			if err != nil {
				return nil, err
			}
			if read.Answer.Row == nil {
				found := fmt.Sprintf("SELECT 1 FROM %s WHERE %s = %s%s", quote(table.Table), quote(table.Key.Column), placeholder(dialect, 1), live(table))
				fences = append(fences, Statement{Text: "NOT EXISTS (" + found + ")", Values: []any{key}, Check: true})
				continue
			}
			found := fmt.Sprintf("SELECT 1 FROM %s WHERE %s = %s AND %s = %s%s", quote(table.Table), quote(table.Key.Column), placeholder(dialect, 1), quote(table.Version), placeholder(dialect, 2), live(table))
			fences = append(fences, Statement{Text: "EXISTS (" + found + ")", Values: []any{key, read.Answer.Row.Version}, Check: true})
		case read.Request.Kind == ReadKindHasRows && read.Answer.Kind == ReadKindHasRows:
			rows := "SELECT 1 FROM " + quote(table.Table)
			if table.Live != nil {
				rows += fmt.Sprintf(" WHERE %s IS NULL", quote(table.Live.Column))
			}
			not := ""
			if !read.Answer.HasRows {
				not = "NOT "
			}
			fences = append(fences, Statement{Text: not + "EXISTS (" + rows + ")", Values: []any{}, Check: true})
		}
	}
	return fences, nil
}

// live is the condition a fenced statement or a read adds for a
// soft-deletable table: the row is live.
func live(table *Table) string {
	if table.Live == nil {
		return ""
	}
	return fmt.Sprintf(" AND %s IS NULL", quote(table.Live.Column))
}

// statementsOf is the statements of a change set, in its order: a create's
// insert of its key, own fields and owned columns, an update's set of the
// patch's own columns, its owned columns and the next version where the
// row is live and at the version expected, a delete where the row is live
// and at the version expected (a soft delete's set of opts.SoftDelete for
// the command's own row), each fenced to change one row. The command's own
// row binds its own columns from opts.OwnColumns when the caller encoded
// them. A create of a key the table holds is refused as conflict;
// sub-table rows, outbox events and deadlines, and a kind of write the
// adapter does not know, refuse the command as internal.
func statementsOf(ctx context.Context, host Host, typeName string, command Command, changes *ChangeSet, opts *ExecuteOptions) ([]Statement, error) {
	notYet := func(what string) error {
		return Internal("%s of %s writes %s, which the adapter takes once each has a table of its own (D103's order of the work, step 3)", command.Action.name(), typeName, what)
	}
	switch {
	case len(changes.SubRows) > 0:
		return nil, notYet("sub-table rows")
	case len(changes.Events) > 0:
		return nil, notYet("outbox events")
	case len(changes.Deadlines) > 0:
		return nil, notYet("deadlines")
	}
	dialect := host.Store.Dialect()
	commandKey := keyOfCommand(command)
	var statements []Statement
	for _, write := range changes.Rows {
		table, err := host.Layout.table(write.Type)
		if err != nil {
			return nil, err
		}
		var values []any
		bind := func(value any) string {
			values = append(values, value)
			return placeholder(dialect, len(values))
		}
		mine := write.Type == typeName && write.Key != nil && *write.Key == commandKey
		var encoded map[string]any
		if mine {
			encoded = opts.OwnColumns
		}
		switch write.Kind {
		case RowCreate:
			if write.Key == nil {
				return nil, Internal("a create of %s reached the adapter without a key", write.Type)
			}
			key, err := keyValue(table, *write.Key)
			if err != nil {
				return nil, err
			}
			found, err := host.Store.Query(ctx, fmt.Sprintf("SELECT 1 AS one FROM %s WHERE %s = %s", quote(table.Table), quote(table.Key.Column), placeholder(dialect, 1)), key)
			if err != nil {
				return nil, err
			}
			if len(found) > 0 {
				return nil, conflictRefusal(write.Type, *write.Key)
			}
			own, err := ownColumns(table, dialect, write.Own, encoded)
			if err != nil {
				return nil, err
			}
			owned, err := ownedColumns(table, dialect, write.Owned)
			if err != nil {
				return nil, err
			}
			assigned := append(append([]assignment{{table.Key.Column, key}}, own...), owned...)
			columns := make([]string, len(assigned))
			placeholders := make([]string, len(assigned))
			for i, a := range assigned {
				columns[i] = quote(a.column)
				placeholders[i] = bind(a.value)
			}
			statements = append(statements, Statement{
				Text:   fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)", quote(table.Table), strings.Join(columns, ", "), strings.Join(placeholders, ", ")),
				Values: values,
				Expect: 1,
			})
		case RowUpdate:
			if write.Key == nil {
				return nil, Internal("an update of %s reached the adapter without a key", write.Type)
			}
			own, err := ownColumns(table, dialect, write.Own, encoded)
			if err != nil {
				return nil, err
			}
			owned, err := ownedColumns(table, dialect, write.Owned)
			if err != nil {
				return nil, err
			}
			sets := make([]string, 0, len(own)+len(owned)+1)
			for _, a := range append(own, owned...) {
				sets = append(sets, quote(a.column)+" = "+bind(a.value))
			}
			// The version moves on with every write, the core's own: on
			// Postgres @optimistic's trigger sets it to the same value.
			sets = append(sets, quote(table.Version)+" = "+quote(table.Version)+" + 1")
			where, err := fencedWhere(table, *write.Key, write.ExpectedVersion, bind)
			if err != nil {
				return nil, err
			}
			statements = append(statements, Statement{
				Text:   fmt.Sprintf("UPDATE %s SET %s WHERE %s", quote(table.Table), strings.Join(sets, ", "), where),
				Values: values,
				Expect: 1,
			})
		case RowDelete:
			if write.Key == nil {
				return nil, Internal("a delete of %s reached the adapter without a key", write.Type)
			}
			if mine && opts.SoftDelete != nil {
				soft := sortedAssignments(opts.SoftDelete)
				sets := make([]string, 0, len(soft)+1)
				for _, a := range soft {
					sets = append(sets, quote(a.column)+" = "+bind(a.value))
				}
				sets = append(sets, quote(table.Version)+" = "+quote(table.Version)+" + 1")
				where, err := fencedWhere(table, *write.Key, write.ExpectedVersion, bind)
				if err != nil {
					return nil, err
				}
				statements = append(statements, Statement{
					Text:   fmt.Sprintf("UPDATE %s SET %s WHERE %s", quote(table.Table), strings.Join(sets, ", "), where),
					Values: values,
					Expect: 1,
				})
				continue
			}
			where, err := fencedWhere(table, *write.Key, write.ExpectedVersion, bind)
			if err != nil {
				return nil, err
			}
			statements = append(statements, Statement{
				Text:   fmt.Sprintf("DELETE FROM %s WHERE %s", quote(table.Table), where),
				Values: values,
				Expect: 1,
			})
		default:
			return nil, Internal("the adapter takes no row write of this kind: %s", rawOrJSON(write))
		}
	}
	return statements, nil
}

// fencedWhere is the condition an update or a delete holds its row to: its
// key, the version the change set expects, and live.
func fencedWhere(table *Table, key string, version int64, bind func(any) string) (string, error) {
	bound, err := keyValue(table, key)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s = %s AND %s = %s%s", quote(table.Key.Column), bind(bound), quote(table.Version), bind(version), live(table)), nil
}

// rawOrJSON is a row write as the core wrote it, for a message.
func rawOrJSON(write RowWrite) string {
	if len(write.Raw) > 0 {
		return compactText(write.Raw)
	}
	return jsonText(write)
}

// assignment is one column a write sets and the value it binds.
type assignment struct {
	column string
	value  any
}

// sortedAssignments is a caller's columns, sorted by name.
func sortedAssignments(columns map[string]any) []assignment {
	names := make([]string, 0, len(columns))
	for name := range columns {
		names = append(names, name)
	}
	sort.Strings(names)
	out := make([]assignment, len(names))
	for i, name := range names {
		out[i] = assignment{name, columns[name]}
	}
	return out
}

// ownColumns is the own columns a write sets: the caller's encoded columns
// for the command's own row, sorted by name, else each own field of the
// change set by the layout's column, in the layout's order. A null for a
// field the table has no column for clears nothing, as a merge patch's
// null for a member the document lacks; a value for one refuses the
// command as internal.
func ownColumns(table *Table, dialect Dialect, own map[string]json.RawMessage, encoded map[string]any) ([]assignment, error) {
	if encoded != nil {
		return sortedAssignments(encoded), nil
	}
	known := map[string]bool{}
	for _, column := range table.Own {
		known[column.Field] = true
	}
	names := make([]string, 0, len(own))
	for name := range own {
		names = append(names, name)
	}
	sort.Strings(names)
	for _, name := range names {
		if !known[name] && !isNull(own[name]) {
			return nil, Internal("%s has no own field %s in the behaviors' layout", table.Type, name)
		}
	}
	var out []assignment
	for _, column := range table.Own {
		raw, ok := own[column.Field]
		if !ok {
			continue
		}
		value, err := toColumn(raw, column, table, dialect)
		if err != nil {
			return nil, err
		}
		out = append(out, assignment{column.Column, value})
	}
	return out, nil
}

// ownedColumns is the owned columns a write sets, each use's declared
// field by its layout column, in the layout's order; null clears one.
func ownedColumns(table *Table, dialect Dialect, owned map[string]map[string]json.RawMessage) ([]assignment, error) {
	uses := make([]string, 0, len(owned))
	for useID := range owned {
		uses = append(uses, useID)
	}
	sort.Strings(uses)
	for _, useID := range uses {
		use := useOf(table, useID)
		if use == nil {
			return nil, Internal("%s has no use %s in the behaviors' layout", table.Type, useID)
		}
		names := make([]string, 0, len(owned[useID]))
		for name := range owned[useID] {
			names = append(names, name)
		}
		sort.Strings(names)
		for _, name := range names {
			if fieldOf(use, name) == nil {
				return nil, Internal("%s's use %s (%s) has no field %s in the behaviors' layout", table.Type, useID, use.Behavior, name)
			}
		}
	}
	var out []assignment
	for _, use := range table.Uses {
		fields, ok := owned[use.ID]
		if !ok {
			continue
		}
		for _, field := range use.Fields {
			raw, ok := fields[field.Name]
			if !ok {
				continue
			}
			value, err := toColumn(raw, field.Column, table, dialect)
			if err != nil {
				return nil, err
			}
			out = append(out, assignment{field.Column.Column, value})
		}
	}
	return out, nil
}

func useOf(table *Table, id string) *Use {
	for i := range table.Uses {
		if table.Uses[i].ID == id {
			return &table.Uses[i]
		}
	}
	return nil
}

func fieldOf(use *Use, name string) *OwnedField {
	for i := range use.Fields {
		if use.Fields[i].Name == name {
			return &use.Fields[i]
		}
	}
	return nil
}

// quote is an identifier quoted as Postgres and SQLite both read one.
func quote(identifier string) string {
	return `"` + strings.ReplaceAll(identifier, `"`, `""`) + `"`
}

// placeholder is the nth placeholder in a dialect: $n on Postgres, ?n on
// SQLite.
func placeholder(dialect Dialect, n int) string {
	if dialect == DialectPostgres {
		return fmt.Sprintf("$%d", n)
	}
	return fmt.Sprintf("?%d", n)
}
