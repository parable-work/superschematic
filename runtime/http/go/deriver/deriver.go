// Package deriver is the loop of a stack's Go deriver over tables (D146,
// docs/stack-model.md, section 8.6): the process that fills the Derived
// fields of tables that implement Derived (D73, D103). The behaviors' core
// calls no model. A deriver, holding the permission each Derived config
// names, pulls the stale fields of its tables (staleDerivations), hands
// each to the project's answer, which calls a model, and settles the
// answers (settleDerivations) with the hash it pulled. It is the loop the
// TypeScript runtime's runDeriver runs (@superschematic/http-runtime/
// deriver), over a table's repository.
//
// The deriver's generated main.go builds a Table from each table's
// repository, opened as the deriver (the ORM's WithBehaviorCaller), with
// TableOf, a typed method of the API's Derivers for each derived field with
// Method, and calls Run. The package knows no database: a table's
// operations are its repository's functions, so the loop is the same for
// every table.
//
// Run pages each table until nothing is stale, answering a page's items
// Concurrency at a time, and settles each page in one call. An answer that
// returns an error or panics settles as a failure, so Derived counts the
// attempt, and so does an answer of no shape a settle takes (no value and
// no failure, a confidence outside 0 to 1): the loop holds each answer to
// the settle's parameters before it sends the page, so one bad answer
// costs its item an attempt and never refuses the page. When a pass
// settles and fails nothing it waits Idle and pulls again: a table has no
// commit to wake it, and an item the core skips without counting an
// attempt (another behavior's guard holds it) would otherwise be pulled
// and answered back to back.
//
// A pull or a settle that fails for a reason time cures (the database is
// down, a table its migration has not made yet, a command the core refuses
// as busy) is logged and its table waited out, a second, doubling to
// thirty, as a worker waits out a claim that failed, so a deriver may start
// before its database does. The other tables go on meanwhile. A failure
// time does not cure, one Options.Permanent reports (a refusal of the
// core's other than busy: a caller without the config's permission, among
// them) or a result of no shape the loop reads, ends Run with the table's
// name, so the process ends and says why. Run returns nil once its context
// ends, and starts no answer after that.
package deriver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"sync"
	"time"
	"unicode/utf8"

	"go.uber.org/zap"
)

const (
	// DefaultBatchSize is how many stale fields a pull asks for unless set.
	DefaultBatchSize = 50
	// MaxBatchSize is the most stale fields a pull may ask for, and a
	// settle take.
	MaxBatchSize = 100
	// DefaultConcurrency is how many answers run at a time unless set.
	DefaultConcurrency = 4
	// DefaultIdle is how long a pass that found nothing waits before it
	// pulls again unless set.
	DefaultIdle = 5 * time.Second
)

// The first wait after a pull or a settle that failed, which doubles to the
// longest.
const (
	defaultRetryMin = time.Second
	defaultRetryMax = 30 * time.Second
)

// The longest message a failure records, the longest class, and the
// longest model id a usage names, as settleDerivations takes them.
const (
	maxFailure      = 2000
	maxFailureClass = 64
	maxModelID      = 200
)

// Item is one stale derived field, as a table's staleDerivations lists it.
type Item struct {
	// ID is the row's key.
	ID    string `json:"id"`
	Field string `json:"field"`
	// SourceHash is the hash a settle presents.
	SourceHash string `json:"sourceHash"`
	// Attempt is which attempt this is, from 1.
	Attempt     int64  `json:"attempt"`
	Instruction string `json:"instruction"`
	// Model is the config's model label, which the project maps to a
	// provider's model.
	Model string `json:"model"`
	// Inputs are the inputs that hold a value, by field name.
	Inputs json.RawMessage `json:"inputs"`
	// Output is the derived field's JSON Schema: what the value must be.
	Output json.RawMessage `json:"output"`
}

// Page is a page of a table's stale fields: its items, and the cursor of
// the next page, nil on the last.
type Page struct {
	Items []Item  `json:"items"`
	Next  *string `json:"next"`
}

// Usage is what a model's call cost, which the settle's event keeps for
// reports.
type Usage struct {
	InputTokens  *int64  `json:"inputTokens,omitempty"`
	OutputTokens *int64  `json:"outputTokens,omitempty"`
	CostMicros   *int64  `json:"costMicros,omitempty"`
	ModelID      *string `json:"modelId,omitempty"`
}

// Failure is why an answer holds no value; Derived counts it as an attempt.
type Failure struct {
	Class   string `json:"class,omitempty"`
	Message string `json:"message"`
}

// Answer is what the project's answer gives for an item: a value, as JSON,
// with how sure the model is and what the call cost, or a failure.
type Answer struct {
	Value      json.RawMessage
	Confidence *float64
	Usage      *Usage
	Failure    *Failure
}

// Settlement is one answer a settle takes: the item's row, field and hash,
// with its value or its failure.
type Settlement struct {
	ID         string          `json:"id"`
	Field      string          `json:"field"`
	SourceHash string          `json:"sourceHash"`
	Value      json.RawMessage `json:"value,omitempty"`
	Confidence *float64        `json:"confidence,omitempty"`
	Usage      *Usage          `json:"usage,omitempty"`
	Failure    *Failure        `json:"failure,omitempty"`
}

// Skipped is an item a settle wrote nothing for, and why.
type Skipped struct {
	ID       string  `json:"id"`
	Field    string  `json:"field"`
	Reason   string  `json:"reason"`
	Behavior *string `json:"behavior,omitempty"`
	Code     *string `json:"code,omitempty"`
}

// SettleResult is what a table's settleDerivations returns: how many values
// it wrote, how many failures it counted, and what it skipped.
type SettleResult struct {
	Settled int64     `json:"settled"`
	Failed  int64     `json:"failed"`
	Skipped []Skipped `json:"skipped"`
}

// Table is a table's Derived operations, as its repository has them under
// the use's member, opened as the deriver: Stale pulls a page of limit
// items after cursor, and Settle settles items. TableOf builds one from a
// repository's typed operations.
type Table struct {
	Name   string
	Stale  func(ctx context.Context, limit int, cursor *string) (Page, error)
	Settle func(ctx context.Context, items []Settlement) (SettleResult, error)
}

// shapeError is a parameter or a result of no shape the loop reads: a
// programming error, which time does not cure.
type shapeError struct{ err error }

func (e *shapeError) Error() string { return e.err.Error() }
func (e *shapeError) Unwrap() error { return e.err }

// convert reads from, as JSON, into a value of To.
func convert[To any](what string, from any) (To, error) {
	var out To
	data, err := json.Marshal(from)
	if err != nil {
		return out, &shapeError{fmt.Errorf("deriver: %s: %w", what, err)}
	}
	if err := json.Unmarshal(data, &out); err != nil {
		return out, &shapeError{fmt.Errorf("deriver: %s, %s: %w", what, data, err)}
	}
	return out, nil
}

// TableOf is the Table named name over a repository's typed derived
// operations, staleDerivations and settleDerivations, whose parameters and
// results it reads and writes as JSON: stale's parameters take a limit and
// a cursor, and settle's the items.
func TableOf[SP, SR, TP, TR any](name string, stale func(context.Context, SP) (SR, error), settle func(context.Context, TP) (TR, error)) Table {
	return Table{
		Name: name,
		Stale: func(ctx context.Context, limit int, cursor *string) (Page, error) {
			params := map[string]any{"limit": limit}
			if cursor != nil {
				params["cursor"] = *cursor
			}
			typed, err := convert[SP]("the parameters of "+name+"'s staleDerivations", params)
			if err != nil {
				return Page{}, err
			}
			result, err := stale(ctx, typed)
			if err != nil {
				return Page{}, err
			}
			return convert[Page]("the result of "+name+"'s staleDerivations", result)
		},
		Settle: func(ctx context.Context, items []Settlement) (SettleResult, error) {
			typed, err := convert[TP]("the parameters of "+name+"'s settleDerivations", map[string]any{"items": items})
			if err != nil {
				return SettleResult{}, err
			}
			result, err := settle(ctx, typed)
			if err != nil {
				return SettleResult{}, err
			}
			return convert[SettleResult]("the result of "+name+"'s settleDerivations", result)
		},
	}
}

// Options says what Run fills and how.
type Options struct {
	// Tables are the tables whose Derived fields it fills, in the order it
	// pulls them.
	Tables []Table
	// Answer is the project's model call for one item of a table.
	Answer func(ctx context.Context, table string, item Item) (Answer, error)
	// BatchSize is how many items a pull asks for: 1 to MaxBatchSize,
	// DefaultBatchSize unless set.
	BatchSize int
	// Concurrency is how many answers run at a time: DefaultConcurrency
	// unless set.
	Concurrency int
	// Idle is how long a pass that found nothing waits before it pulls
	// again: DefaultIdle unless set.
	Idle time.Duration
	// RetryMin is the first wait of a table whose pull or settle failed,
	// which doubles to RetryMax: a second, and thirty, unless set.
	RetryMin time.Duration
	RetryMax time.Duration
	// Permanent reports a failure of a pull or a settle that time does not
	// cure, which ends Run: the generated entrypoint's reports a refusal of
	// the core's other than busy. Nil treats every failure as one time
	// cures, but a result of no shape the loop reads.
	Permanent func(error) bool
	// Logger logs each settle that did something, and each failure the
	// loop waits out; nil logs nothing.
	Logger *zap.Logger
}

// Run runs a deriver over tables until ctx ends, when it returns nil. It
// returns an error on a failure of a pull or a settle that time does not
// cure.
func Run(ctx context.Context, opts Options) error {
	if opts.Answer == nil {
		return errors.New("deriver: no answer")
	}
	batch := opts.BatchSize
	if batch <= 0 {
		batch = DefaultBatchSize
	}
	batch = min(batch, MaxBatchSize)
	concurrency := max(opts.Concurrency, 1)
	if opts.Concurrency == 0 {
		concurrency = DefaultConcurrency
	}
	idle := opts.Idle
	if idle <= 0 {
		idle = DefaultIdle
	}
	retryMin := opts.RetryMin
	if retryMin <= 0 {
		retryMin = defaultRetryMin
	}
	retryMax := max(opts.RetryMax, retryMin)
	if opts.RetryMax <= 0 {
		retryMax = max(defaultRetryMax, retryMin)
	}
	logger := opts.Logger
	if logger == nil {
		logger = zap.NewNop()
	}
	permanent := func(err error) bool {
		var shape *shapeError
		if errors.As(err, &shape) {
			return true
		}
		return opts.Permanent != nil && opts.Permanent(err)
	}
	// Each table that failed, with its wait and when it is tried again.
	type held struct {
		backoff time.Duration
		retryAt time.Time
	}
	waiting := map[string]held{}
	for ctx.Err() == nil {
		progressed := false
		for _, table := range opts.Tables {
			if ctx.Err() != nil {
				return nil
			}
			h, isHeld := waiting[table.Name]
			if isHeld && h.retryAt.After(time.Now()) {
				continue
			}
			did, err := derive(ctx, opts.Answer, table, batch, concurrency, logger)
			progressed = progressed || did
			if err == nil {
				delete(waiting, table.Name)
				continue
			}
			if ctx.Err() != nil {
				return nil
			}
			if permanent(err) {
				return fmt.Errorf("deriver: table %s: %w", table.Name, err)
			}
			backoff := retryMin
			if isHeld {
				backoff = min(h.backoff*2, retryMax)
			}
			waiting[table.Name] = held{backoff: backoff, retryAt: time.Now().Add(backoff)}
			logger.Warn("deriver waits", zap.String("table", table.Name), zap.Error(err), zap.Duration("wait", backoff))
		}
		if progressed || ctx.Err() != nil {
			continue
		}
		// Until the idle wait is over, or a table that failed is due again.
		wait := idle
		for _, h := range waiting {
			wait = min(wait, max(time.Until(h.retryAt), 0))
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			timer.Stop()
		case <-timer.C:
		}
	}
	return nil
}

// derive pages one table's stale fields, answers and settles each page,
// and says whether a settle wrote a value or counted an attempt: a pass
// that only met items the core skips made no progress.
func derive(ctx context.Context, answer func(context.Context, string, Item) (Answer, error), table Table, batch, concurrency int, logger *zap.Logger) (bool, error) {
	var cursor *string
	progressed := false
	for {
		page, err := table.Stale(ctx, batch, cursor)
		if err != nil {
			return progressed, err
		}
		cursor = page.Next
		if len(page.Items) > 0 {
			answers := answerAll(ctx, answer, table.Name, page.Items, concurrency)
			if ctx.Err() != nil {
				return progressed, nil
			}
			settlements := make([]Settlement, len(page.Items))
			for i, item := range page.Items {
				settlements[i] = settlementOf(item, answers[i])
			}
			result, err := table.Settle(ctx, settlements)
			if err != nil {
				return progressed, err
			}
			progressed = progressed || result.Settled > 0 || result.Failed > 0
			if result.Settled > 0 || result.Failed > 0 || len(result.Skipped) > 0 {
				logger.Info("derivations settled", zap.String("table", table.Name), zap.Int64("settled", result.Settled), zap.Int64("failed", result.Failed), zap.Int("skipped", len(result.Skipped)))
			}
		}
		if cursor == nil || ctx.Err() != nil {
			return progressed, nil
		}
	}
}

// answered is an answer, or why there is none.
type answered struct {
	answer Answer
	err    error
	done   bool
}

// answerAll answers items, concurrency at a time, in their order; an error
// or a panic is the item's failure. Once ctx ends it starts no answer more,
// and the items it did not reach hold none.
func answerAll(ctx context.Context, answer func(context.Context, string, Item) (Answer, error), table string, items []Item, concurrency int) []answered {
	out := make([]answered, len(items))
	var (
		mu   sync.Mutex
		next int
		wg   sync.WaitGroup
	)
	lane := func() {
		defer wg.Done()
		for {
			mu.Lock()
			if next >= len(items) || ctx.Err() != nil {
				mu.Unlock()
				return
			}
			i := next
			next++
			mu.Unlock()
			out[i] = answerOne(ctx, answer, table, items[i])
		}
	}
	for range min(concurrency, len(items)) {
		wg.Add(1)
		go lane()
	}
	wg.Wait()
	return out
}

// answerOne runs the answer of one item, holding a panic to its failure.
func answerOne(ctx context.Context, answer func(context.Context, string, Item) (Answer, error), table string, item Item) (out answered) {
	defer func() {
		if r := recover(); r != nil {
			out = answered{answer: Answer{Failure: &Failure{Class: "panicked", Message: fmt.Sprint("the answer panicked: ", r)}}, done: true}
		}
	}()
	a, err := answer(ctx, table, item)
	return answered{answer: a, err: err, done: true}
}

// settlementOf holds an answer to what settleDerivations takes of an item,
// so that one answer of another shape fails its own item and the page
// still settles: a failure keeps its message and class, cut to their
// lengths; a value keeps a confidence from 0 to 1 and what of its usage is
// well formed; and anything else is the item's failure.
func settlementOf(item Item, a answered) Settlement {
	out := Settlement{ID: item.ID, Field: item.Field, SourceHash: item.SourceHash}
	refused := func(message string) Settlement {
		out.Failure = &Failure{Class: "invalid_answer", Message: cut(message, maxFailure)}
		return out
	}
	switch {
	case !a.done:
		return refused("the deriver stopped before it answered")
	case a.err != nil:
		out.Failure = &Failure{Class: "error", Message: cut(a.err.Error(), maxFailure)}
		return out
	case a.answer.Failure != nil:
		out.Failure = &Failure{Class: cut(a.answer.Failure.Class, maxFailureClass), Message: cut(a.answer.Failure.Message, maxFailure)}
		return out
	case len(a.answer.Value) == 0 || !json.Valid(a.answer.Value):
		return refused("the answer holds neither a value nor a failure")
	}
	out.Value = a.answer.Value
	if c := a.answer.Confidence; c != nil {
		if math.IsNaN(*c) || *c < 0 || *c > 1 {
			return refused(fmt.Sprintf("the answer's confidence, %v, is no number from 0 to 1", *c))
		}
		out.Confidence = c
	}
	// What a call cost is kept for reports: a count below zero is left
	// out, and so is a usage that keeps nothing.
	if u := a.answer.Usage; u != nil {
		kept := Usage{}
		for _, count := range []struct{ from, to **int64 }{{&u.InputTokens, &kept.InputTokens}, {&u.OutputTokens, &kept.OutputTokens}, {&u.CostMicros, &kept.CostMicros}} {
			if *count.from != nil && **count.from >= 0 {
				*count.to = *count.from
			}
		}
		if u.ModelID != nil {
			id := cut(*u.ModelID, maxModelID)
			kept.ModelID = &id
		}
		if kept != (Usage{}) {
			out.Usage = &kept
		}
	}
	return out
}

// cut is s at most n characters long.
func cut(s string, n int) string {
	if utf8.RuneCountInString(s) <= n {
		return s
	}
	return string([]rune(s)[:n])
}

// DerivedItem is a stale derived field as a deriver's method gets it (D74):
// the row's id, which attempt this is, from 1, the model label and the
// instruction of the field's Derived config, the inputs that hold a value,
// read as I, and the field's JSON Schema, which a model is held to.
type DerivedItem[I any] struct {
	ID          string
	Attempt     int64
	Model       string
	Instruction string
	Inputs      I
	Output      json.RawMessage
}

// DerivedAnswer is what a deriver's method answers: a value of the field's
// type T, with how sure the model is, from 0 to 1, and what the call cost,
// which the settle's event keeps; or, with Failure set, why there is none.
// A failure, and a method that returns an error, count an attempt, and the
// field waits once its attempts are spent until its inputs move.
type DerivedAnswer[T any] struct {
	Value      T
	Confidence *float64
	Usage      *Usage
	Failure    *Failure
}

// Value is the answer value, with no confidence or usage.
func Value[T any](value T) DerivedAnswer[T] {
	return DerivedAnswer[T]{Value: value}
}

// Failed is the answer of no value, a failure of class.
func Failed[T any](class, message string) DerivedAnswer[T] {
	return DerivedAnswer[T]{Failure: &Failure{Class: class, Message: message}}
}

// Method is the answer of a typed method of a deriver: it reads an item's
// inputs as I and writes the method's value as JSON. Inputs its struct
// does not read fail the item, which counts an attempt.
func Method[I, T any](method func(context.Context, DerivedItem[I]) (DerivedAnswer[T], error)) func(context.Context, Item) (Answer, error) {
	return func(ctx context.Context, item Item) (Answer, error) {
		derived := DerivedItem[I]{ID: item.ID, Attempt: item.Attempt, Model: item.Model, Instruction: item.Instruction, Output: item.Output}
		if len(item.Inputs) > 0 {
			if err := json.Unmarshal(item.Inputs, &derived.Inputs); err != nil {
				return Answer{Failure: &Failure{Class: "inputs", Message: fmt.Sprintf("the inputs of %s: %v", item.Field, err)}}, nil
			}
		}
		answer, err := method(ctx, derived)
		if err != nil {
			return Answer{}, err
		}
		if answer.Failure != nil {
			return Answer{Failure: answer.Failure}, nil
		}
		value, err := json.Marshal(answer.Value)
		if err != nil {
			return Answer{Failure: &Failure{Class: "serialize", Message: err.Error()}}, nil
		}
		return Answer{Value: value, Confidence: answer.Confidence, Usage: answer.Usage}, nil
	}
}

// Methods is the answer of a deriver of methods, by each table and field,
// dotted (Ticket.category): a field it has no method for fails, which
// counts an attempt.
func Methods(deriver string, methods map[string]func(context.Context, Item) (Answer, error)) func(context.Context, string, Item) (Answer, error) {
	return func(ctx context.Context, table string, item Item) (Answer, error) {
		method, ok := methods[table+"."+item.Field]
		if !ok {
			return Answer{Failure: &Failure{Class: "unknown", Message: fmt.Sprintf("deriver %s has no method for %s.%s", deriver, table, item.Field)}}, nil
		}
		return method(ctx, item)
	}
}
