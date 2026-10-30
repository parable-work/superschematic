// Package rules is the Go binding and adapter of the behaviors' pure core
// (D103): runtime/engine/rust/rules with every behavior ported onto it,
// reached through the JSON C ABI of superschematic-rules-wasm's native
// build, and the one host loop every Go caller runs it through.
//
// The binding (RunJSON, DeclarationsJSON, CheckJSON, and Native's Core over
// them) sends one JSON document through the C ABI and reads one back. The
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

type op int

const (
	opRun op = iota
	opDeclarations
	opCheck
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

// Native is the core the static archive holds, with every behavior ported
// so far registered. The archive builds it once per process, the first
// time any operation runs, and every goroutine shares it; each call is a
// cgo call that copies its output, so Native's core is safe for concurrent
// use by any number of goroutines.
func Native() Core {
	return nativeCore{}
}

type nativeCore struct{}

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

// declarations are the archive's declaration files, read once: they are
// the documents the archive was built with, which no call changes.
var declarations = sync.OnceValues(DeclarationsJSON)

func (nativeCore) Declarations() ([]Declaration, error) {
	out, err := declarations()
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
	Name        string              `json:"name"`
	Description string              `json:"description,omitempty"`
	Value       string              `json:"value,omitempty"`
	Fields      []DeclaredField     `json:"fields,omitempty"`
	Operations  []DeclaredOperation `json:"operations,omitempty"`
}

// DeclaredField is a field a behavior declares: its declared name, and its
// IR type when the field is a column of the type's table (D103).
type DeclaredField struct {
	Name        string `json:"name"`
	Description string `json:"description,omitempty"`
	Type        string `json:"type,omitempty"`
	Volatile    bool   `json:"volatile,omitempty"`
	PerReader   bool   `json:"perReader,omitempty"`
}

// DeclaredOperation is an operation a behavior declares, with the schemas
// of its parameters and its result as the declaration writes them.
type DeclaredOperation struct {
	Name         string          `json:"name"`
	Description  string          `json:"description,omitempty"`
	ParamsSchema json.RawMessage `json:"paramsSchema,omitempty"`
	ResultSchema json.RawMessage `json:"resultSchema,omitempty"`
	Writes       bool            `json:"writes,omitempty"`
	Scope        string          `json:"scope,omitempty"`
}
