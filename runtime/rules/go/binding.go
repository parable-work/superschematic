// Package rules is the Go binding and adapter of the behaviors' pure core
// (D103): runtime/engine/rust/rules with every behavior ported onto it,
// reached through the JSON C ABI of superschematic-rules-wasm's native
// build, and the one host loop every Go caller runs it through.
//
// The binding (RunJSON, DeclarationsJSON, CheckJSON, ExpandJSON, Expand,
// DeclareJSON and Declare, and Native's Core over them) sends one JSON document through the C ABI and reads one back. The
// contract's types mirror the core's JSON as @superschematic/rules's
// contract.ts does, every value of the core's own (a config, a parameter,
// an own or owned value) held as json.RawMessage, so a number keeps its
// text. runtime/engine/rust/rules/README.md is the contract's prose.
//
// The adapter (Execute and its wrappers) answers the core's reads through
// the caller's transaction (a Store: package postgres over pgx, package
// sqlite over database/sql), takes its locks, applies each change set
// whole with every row write fenced on its version, and runs a command
// whose row moved again from fresh state, MaxAttempts times, before it
// refuses it as busy. It holds no behavior's name: the layout document the
// generator writes (internal/generator/behaviorlayout), which a generated
// ORM embeds, maps the core's logical names onto tables and columns. It is
// the TypeScript adapter's (runtime/http/typescript/src/rules.ts) port,
// with its refusals in its words.
//
// The package links libsuperschematic_rules.a through cgo. Build it with
// `make rules-archive` (scripts/rules-archive.sh), which stages it under
// lib/<goos>_<goarch>; outside this checkout, put that directory in
// CGO_LDFLAGS as -L<dir>.
package rules

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include "superschematic_rules.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"sync"
	"unsafe"
)

// Error is an input the core refused, an error document: code
// invalid_json, invalid_input or internal. A command the core refuses is
// a step ({done: {refused}}), never an Error.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return "rules: " + e.Code + ": " + e.Message
}

// RunJSON is rules_run: {"spec", "command", "answers"?} to the step's JSON.
func RunJSON(input []byte) ([]byte, error) {
	return call(opRun, input)
}

// DeclarationsJSON is rules_declarations: the registered behaviors'
// declaration files, a JSON array.
func DeclarationsJSON() ([]byte, error) {
	return call(opDeclarations, nil)
}

// CheckJSON is rules_check: {"specs", "caller"?, "now"?} to a JSON array of
// null or a refusal per spec.
func CheckJSON(input []byte) ([]byte, error) {
	return call(opCheck, input)
}

// DeclareJSON is rules_declare: {"declarations": [file]}, the declaration
// files of declarative behaviors (D103's step 5), each whole, to
// {"declared": [name]}, every declarative behavior the core runs since.
func DeclareJSON(input []byte) ([]byte, error) {
	return call(opDeclare, input)
}

// Declare hands the native core the declaration files of declarative
// behaviors (D103's step 5): behaviors whose declarations state their rules
// in the vocabulary the core interprets, which no Rust implements. The
// process's core runs them from then on, after its own, and Native's
// Declarations lists their files after its own. It returns the name of
// every declarative behavior the core runs. A file the vocabulary does not
// read, one that names a behavior the core implements, and a second file of
// a declared name that differs are an *Error of code invalid_input; a file
// declared again is one behavior.
func Declare(files ...json.RawMessage) ([]string, error) {
	if files == nil {
		files = []json.RawMessage{}
	}
	in, err := json.Marshal(struct {
		Declarations []json.RawMessage `json:"declarations"`
	}{files})
	if err != nil {
		return nil, fmt.Errorf("rules: encode the declarations: %w", err)
	}
	out, err := DeclareJSON(in)
	declared.forget()
	if err != nil {
		return nil, err
	}
	var answer struct {
		Declared []string `json:"declared"`
	}
	if err := json.Unmarshal(out, &answer); err != nil {
		return nil, fmt.Errorf("rules: decode the core's declare: %w", err)
	}
	return answer.Declared, nil
}

// ExpandJSON is rules_expand: a recurrence (D152) to its dated
// occurrences, {"occurrences": [...], "more"}; a recurrence the core
// refuses is an *Error of code invalid_input whose message, after
// "expand: ", says why.
func ExpandJSON(input []byte) ([]byte, error) {
	return call(opExpand, input)
}

// Expand dates a recurrence (D152) as the core does for every language:
// ExpandInput's anchor, adjustment and lead times in, its occurrences in
// due order out.
func Expand(input ExpandInput) (Expansion, error) {
	in, err := json.Marshal(input)
	if err != nil {
		return Expansion{}, fmt.Errorf("rules: encode the recurrence: %w", err)
	}
	out, err := ExpandJSON(in)
	if err != nil {
		return Expansion{}, err
	}
	var expansion Expansion
	if err := json.Unmarshal(out, &expansion); err != nil {
		return Expansion{}, fmt.Errorf("rules: decode the core's expansion: %w", err)
	}
	return expansion, nil
}

// ExpandInput is a recurrence the core dates (D152), as
// @superschematic/rules's ExpandInput: its anchor (calendar, afterDone,
// relative), the zone its wall times are read in, the adjustment of a due
// date that falls on a weekend day or a holiday, the lead times (ISO 8601
// spans) and the window [From, To) of due instants in epoch milliseconds.
// Date is a wall date or date-time text, or epoch milliseconds.
type ExpandInput struct {
	Anchor       string          `json:"anchor"`
	TimeZone     string          `json:"timeZone,omitempty"`
	Rule         string          `json:"rule,omitempty"`
	Start        string          `json:"start,omitempty"`
	Every        string          `json:"every,omitempty"`
	LastDone     *int64          `json:"lastDone,omitempty"`
	Date         json.RawMessage `json:"date,omitempty"`
	Offset       string          `json:"offset,omitempty"`
	Adjust       string          `json:"adjust,omitempty"`
	WeekendDays  []string        `json:"weekendDays,omitempty"`
	Holidays     []string        `json:"holidays,omitempty"`
	OpensBefore  string          `json:"opensBefore,omitempty"`
	RemindBefore string          `json:"remindBefore,omitempty"`
	OverdueAfter string          `json:"overdueAfter,omitempty"`
	From         *int64          `json:"from,omitempty"`
	To           *int64          `json:"to,omitempty"`
	Limit        int             `json:"limit,omitempty"`
}

// ExpandedOccurrence is one dated occurrence (D152): Key is its nominal
// wall date-time before the adjustment, Due the wall date-time it falls due
// at after it, Shifted why it moved (weekend, holiday), and Projected an
// afterDone occurrence past the first.
type ExpandedOccurrence struct {
	Key       string `json:"key"`
	Due       string `json:"due"`
	DueAt     int64  `json:"dueAt"`
	OpensAt   *int64 `json:"opensAt,omitempty"`
	RemindAt  *int64 `json:"remindAt,omitempty"`
	OverdueAt int64  `json:"overdueAt"`
	Shifted   string `json:"shifted,omitempty"`
	Projected bool   `json:"projected,omitempty"`
}

// Expansion is what the core's expand answers: the occurrences in due
// order, and whether more lie in the window past the limit.
type Expansion struct {
	Occurrences []ExpandedOccurrence `json:"occurrences"`
	More        bool                 `json:"more"`
}

type op int

const (
	opRun op = iota
	opDeclarations
	opCheck
	opExpand
	opDeclare
)

// call runs one operation of the core on a JSON input and returns its
// output document, or the error document's Error. The input is passed in
// place (a []byte holds no Go pointer, so cgo lets C read it for the
// call), and the output is copied into Go memory and released with
// rules_free before call returns.
func call(o op, in []byte) ([]byte, error) {
	var inPtr *C.uint8_t
	if len(in) > 0 {
		inPtr = (*C.uint8_t)(unsafe.Pointer(&in[0]))
	}
	inLen := C.size_t(len(in))
	var outPtr *C.uint8_t
	var outLen C.size_t
	var code C.int32_t
	switch o {
	case opRun:
		code = C.rules_run(inPtr, inLen, &outPtr, &outLen)
	case opDeclarations:
		code = C.rules_declarations(inPtr, inLen, &outPtr, &outLen)
	case opCheck:
		code = C.rules_check(inPtr, inLen, &outPtr, &outLen)
	case opExpand:
		code = C.rules_expand(inPtr, inLen, &outPtr, &outLen)
	case opDeclare:
		code = C.rules_declare(inPtr, inLen, &outPtr, &outLen)
	default:
		return nil, fmt.Errorf("rules: unknown operation %d", o)
	}
	if code == C.RULES_BAD_ARGUMENTS {
		return nil, errors.New("rules: the core refused its arguments")
	}
	defer C.rules_free(outPtr, outLen)
	out := C.GoBytes(unsafe.Pointer(outPtr), C.int(outLen))
	if code != C.RULES_OK {
		return nil, decodeError(out)
	}
	return out, nil
}

// decodeError reads an error document, {"error": {"code", "message"}}.
func decodeError(out []byte) error {
	var document struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(out, &document); err != nil || document.Error == nil {
		return fmt.Errorf("rules: unreadable error document: %q", out)
	}
	return document.Error
}

// Core is the core the adapter runs: Native's over the static archive, or a
// test's own. Every method is safe for concurrent use.
type Core interface {
	// Run runs a command on a type with the answers so far and returns one
	// step. The same inputs give the same step.
	Run(spec TypeSpec, command Command, answers Answers) (Step, error)
	// Declarations are the registered behaviors' declaration files, in the
	// core's order.
	Declarations() ([]Declaration, error)
	// Check is the define check of each spec, in order: nil for a spec the
	// core takes, its refusal (invalid_schema) for one it refuses. The
	// caller is a subject of "" with no permissions, the time 0.
	Check(specs []TypeSpec) ([]*Refusal, error)
}

// Declarer is a core that runs declarative behaviors (D103's step 5) once
// it is handed their declaration files: Native's, whose Declare is the
// package's. The adapter hands a layout's declarations to a core that is
// one before it checks the layout's specs.
type Declarer interface {
	Declare(files ...json.RawMessage) ([]string, error)
}

// Native is the core the static archive holds, with every behavior ported
// so far registered. The archive builds it once per process, the first
// time any operation runs, and every goroutine shares it; each call is a
// cgo call that copies its output, so Native's core is safe for concurrent
// use by any number of goroutines.
func Native() Core {
	return nativeCore{}
}

type nativeCore struct{}

// Declare is the package's Declare: the process's native core runs the
// files' behaviors from then on.
func (nativeCore) Declare(files ...json.RawMessage) ([]string, error) {
	return Declare(files...)
}

// runInput is rules_run's input document.
type runInput struct {
	Spec    TypeSpec `json:"spec"`
	Command Command  `json:"command"`
	Answers Answers  `json:"answers"`
}

func (nativeCore) Run(spec TypeSpec, command Command, answers Answers) (Step, error) {
	in, err := json.Marshal(runInput{Spec: spec, Command: command, Answers: answers})
	if err != nil {
		return Step{}, fmt.Errorf("rules: encode the run: %w", err)
	}
	out, err := RunJSON(in)
	if err != nil {
		return Step{}, err
	}
	var step Step
	if err := json.Unmarshal(out, &step); err != nil {
		return Step{}, fmt.Errorf("rules: decode the core's step: %w", err)
	}
	return step, nil
}

// declared holds the archive's declaration files, read once and again
// after a Declare, which adds the files of the behaviors it declares.
var declared declarationsCache

type declarationsCache struct {
	mu   sync.Mutex
	text []byte
}

func (c *declarationsCache) read() ([]byte, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.text != nil {
		return c.text, nil
	}
	out, err := DeclarationsJSON()
	if err != nil {
		return nil, err
	}
	c.text = out
	return out, nil
}

func (c *declarationsCache) forget() {
	c.mu.Lock()
	c.text = nil
	c.mu.Unlock()
}

func (nativeCore) Declarations() ([]Declaration, error) {
	out, err := declared.read()
	if err != nil {
		return nil, err
	}
	var files []Declaration
	if err := json.Unmarshal(out, &files); err != nil {
		return nil, fmt.Errorf("rules: decode the core's declarations: %w", err)
	}
	return files, nil
}

// checkInput is rules_check's input document.
type checkInput struct {
	Specs []TypeSpec `json:"specs"`
}

func (nativeCore) Check(specs []TypeSpec) ([]*Refusal, error) {
	if specs == nil {
		specs = []TypeSpec{}
	}
	in, err := json.Marshal(checkInput{Specs: specs})
	if err != nil {
		return nil, fmt.Errorf("rules: encode the check: %w", err)
	}
	out, err := CheckJSON(in)
	if err != nil {
		return nil, err
	}
	var refusals []*Refusal
	if err := json.Unmarshal(out, &refusals); err != nil {
		return nil, fmt.Errorf("rules: decode the core's check: %w", err)
	}
	if len(refusals) != len(specs) {
		return nil, fmt.Errorf("rules: the core checked %d specs of %d", len(refusals), len(specs))
	}
	return refusals, nil
}

// Declaration is a behavior's declaration file, as the core registers it:
// the members a host reads (the others are ignored).
type Declaration struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Value       string          `json:"value,omitempty"`
	Fields      []DeclaredField `json:"fields,omitempty"`
	// Tables are the sub-tables the behavior keeps per row, ArmsDeadlines
	// whether it arms deadlines, and HearsChanges whether it hears the
	// writes of its row, whoever makes them (D103).
	Tables        []DeclaredTable     `json:"tables,omitempty"`
	ArmsDeadlines bool                `json:"armsDeadlines,omitempty"`
	HearsChanges  bool                `json:"hearsChanges,omitempty"`
	Operations    []DeclaredOperation `json:"operations,omitempty"`
	// Events are the events the behavior emits about a row (D104), which
	// its table's event queue carries to the API's behavior worker.
	Events []DeclaredEvent `json:"events,omitempty"`
	// Contracts are the contracts between behaviors the behavior implements
	// or asks of other uses (D104): Budget implements Capacity, which Pool
	// asks. Only the core reads them.
	Contracts []DeclaredContract `json:"contracts,omitempty"`
}

// DeclaredContract is a contract between behaviors a declaration names
// (D103, D104): its name, whether the behavior implements it or asks it of
// other uses (implements or asks), and what it means there.
type DeclaredContract struct {
	Name        string `json:"name"`
	Role        string `json:"role"`
	Description string `json:"description,omitempty"`
}

// DeclaredEvent is an event a behavior declares (D104): its kind, which
// names it in its table's event queue, what it says, and the JSON Schema of
// its payload as the declaration writes it.
type DeclaredEvent struct {
	Kind          string          `json:"kind"`
	Description   string          `json:"description,omitempty"`
	PayloadSchema json.RawMessage `json:"payloadSchema,omitempty"`
}

// DeclaredTable is a sub-table a behavior declares: its name, the field
// whose value keys a row within its row, and its typed fields, the key
// among them. One whose Scope is schema is kept for the table as a whole,
// its rows shared by every row of the type (D103's step 4).
type DeclaredTable struct {
	Name        string          `json:"name"`
	Description string          `json:"description,omitempty"`
	Key         string          `json:"key"`
	Scope       string          `json:"scope,omitempty"`
	Fields      []DeclaredField `json:"fields"`
}

// Shared reports whether the table is kept for the table as a whole.
func (t DeclaredTable) Shared() bool {
	return t.Scope == "schema"
}

// DeclaredField is a field a behavior declares: its declared name, and its
// IR type when the field is a column of the type's table (D103). Optional
// is a value field the behavior does not set when a row is created, whose
// column takes NULL (D104); every other owned column takes NULL already.
type DeclaredField struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Optional    bool   `json:"optional,omitempty"`
	Volatile    bool   `json:"volatile,omitempty"`
	PerReader   bool   `json:"perReader,omitempty"`
}

// DeclaredOperation is an operation a behavior declares, with the schemas
// of its parameters and its result as the declaration writes them, and its
// scope: "" or instance for one of a row, schema for one of the table as a
// whole (D104), which runs as a command without a row, and namespace for
// one the core runs no command for. Beside is true for a writing operation
// of a row that writes beside it (D77's primitive 1, Comments'): its use's
// sub-tables and shared tables alone, so the row's version stays and no
// other use's guard is asked.
type DeclaredOperation struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`
	ResultSchema json.RawMessage `json:"resultSchema,omitempty"`
	Writes       bool            `json:"writes,omitempty"`
	Scope        string          `json:"scope,omitempty"`
	Beside       bool            `json:"beside,omitempty"`
}
