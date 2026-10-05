// Package versiongraph is the Go binding of the version-graph core
// (runtime/versiongraph/rust): compose, merge, diff, hash and validate trees
// of versioned rows, described by a graph descriptor.
//
// Every function sends one JSON document through the core's C ABI and reads
// one back. Trees and rows stay json.RawMessage: a row is a JSON object keyed
// by column name, a canonical row once a storage adapter has normalized it
// (package canonical), and the core reads it as such. The descriptor is
// version 3. runtime/versiongraph/README.md is the contract.
//
// The package links libsuperschematic_versiongraph.a through cgo. Build it
// with `make versiongraph` (scripts/versiongraph-archive.sh), which stages it
// under lib/<goos>_<goarch>; outside this checkout, put that directory in
// CGO_LDFLAGS as -L<dir>.
package versiongraph

/*
#cgo CFLAGS: -I${SRCDIR}/include
#include "versiongraph.h"
*/
import "C"

import (
	"encoding/json"
	"errors"
	"fmt"
	"unsafe"
)

// Error is an input the core refused. Code is stable; Message names the
// offending part of the input.
type Error struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

func (e *Error) Error() string {
	return "versiongraph: " + e.Code + ": " + e.Message
}

// Finding is a problem in a tree that Compose reports and Validate lists.
// Code is absent_parent, duplicate_entity_key, singleton, parent_cycle or
// order_out_of_range. EntityKey is empty for a finding about a whole kind.
type Finding struct {
	Code      string `json:"code"`
	Kind      string `json:"kind"`
	EntityKey string `json:"entityKey,omitempty"`
	Message   string `json:"message"`
}

// ComposeRequest lays Overlay, one ref's rows, over Base. Descriptor, Base
// and Overlay are JSON documents; a nil member is left out of the request.
type ComposeRequest struct {
	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	Base       json.RawMessage `json:"base,omitempty"`
	Overlay    json.RawMessage `json:"overlay,omitempty"`
}

// ComposeResult is the composed tree, with every kind of the descriptor, and
// its findings.
type ComposeResult struct {
	Tree     json.RawMessage `json:"tree"`
	Findings []Finding       `json:"findings"`
}

// Take names the input a Resolution takes a unit's value from.
type Take string

const (
	TakeBase   Take = "base"
	TakeOurs   Take = "ours"
	TakeTheirs Take = "theirs"
)

// Resolution settles the conflict at Path of one entity, by taking one
// input's value (Take) or by giving the value (Value, where JSON null is a
// value). A whole-entity conflict (Path "") takes a side.
type Resolution struct {
	Kind      string          `json:"kind"`
	EntityKey string          `json:"entityKey"`
	Path      string          `json:"path"`
	Take      Take            `json:"take,omitempty"`
	Value     json.RawMessage `json:"value,omitempty"`
}

// MergeRequest merges Ours and Theirs against their common Base.
type MergeRequest struct {
	Descriptor  json.RawMessage `json:"descriptor,omitempty"`
	Base        json.RawMessage `json:"base,omitempty"`
	Ours        json.RawMessage `json:"ours,omitempty"`
	Theirs      json.RawMessage `json:"theirs,omitempty"`
	Resolutions []Resolution    `json:"resolutions,omitempty"`
}

// Conflict is one unit both sides changed differently, or an edit against a
// delete (Path ""). Base, Ours and Theirs are the unit's values, nil where
// the unit is absent on that side.
type Conflict struct {
	Kind         string          `json:"kind"`
	EntityKey    string          `json:"entityKey"`
	Path         string          `json:"path"`
	Base         json.RawMessage `json:"base,omitempty"`
	Ours         json.RawMessage `json:"ours,omitempty"`
	Theirs       json.RawMessage `json:"theirs,omitempty"`
	OursAuthor   json.RawMessage `json:"oursAuthor,omitempty"`
	TheirsAuthor json.RawMessage `json:"theirsAuthor,omitempty"`
}

// Outcome says which input an entity's merged result came from: "ours"
// (nothing to write onto ours), "theirs", "merged" or "conflict". Deleted
// is true when the result is a delete.
type Outcome struct {
	Kind      string `json:"kind"`
	EntityKey string `json:"entityKey"`
	Side      string `json:"side"`
	Deleted   bool   `json:"deleted,omitempty"`
}

// MergeResult is the merged tree, the conflicts left, and every entity's
// outcome. A conflicted entity is not in Merged; apply Merged only when
// Conflicts is empty.
type MergeResult struct {
	Merged    json.RawMessage `json:"merged"`
	Conflicts []Conflict      `json:"conflicts"`
	Entities  []Outcome       `json:"entities"`
}

// DiffRequest diffs From against To.
type DiffRequest struct {
	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	From       json.RawMessage `json:"from,omitempty"`
	To         json.RawMessage `json:"to,omitempty"`
}

// Change is one entity's difference: Operation is ADD, UPDATE or DELETE, and
// Row is To's row (its tombstone for a DELETE, when To has one).
type Change struct {
	Kind      string          `json:"kind"`
	EntityKey string          `json:"entityKey"`
	Operation string          `json:"operation"`
	Row       json.RawMessage `json:"row,omitempty"`
}

// DiffResult lists the changes by kind, in descriptor order, then entity key.
type DiffResult struct {
	Changes []Change `json:"changes"`
}

// TreeRequest is the input of ContentHash and Validate.
type TreeRequest struct {
	Descriptor json.RawMessage `json:"descriptor,omitempty"`
	Tree       json.RawMessage `json:"tree,omitempty"`
}

// ContentHashResult is the SHA-256, as lowercase hex, of a tree's content.
type ContentHashResult struct {
	ContentHash string `json:"contentHash"`
}

// ValidateResult lists a tree's findings; none means the tree is valid.
type ValidateResult struct {
	Findings []Finding `json:"findings"`
}

// Compose lays an overlay over a base tree.
func Compose(req ComposeRequest) (*ComposeResult, error) {
	return run[ComposeResult](opCompose, req)
}

// Merge merges two trees against their common base.
func Merge(req MergeRequest) (*MergeResult, error) {
	return run[MergeResult](opMerge, req)
}

// Diff lists the changes that turn one tree into another.
func Diff(req DiffRequest) (*DiffResult, error) {
	return run[DiffResult](opDiff, req)
}

// ContentHash hashes a tree's content columns.
func ContentHash(req TreeRequest) (*ContentHashResult, error) {
	return run[ContentHashResult](opContentHash, req)
}

// Validate lists what is wrong with a tree.
func Validate(req TreeRequest) (*ValidateResult, error) {
	return run[ValidateResult](opValidate, req)
}

type op int

const (
	opCompose op = iota
	opMerge
	opDiff
	opContentHash
	opValidate
)

func run[T any](o op, req any) (*T, error) {
	in, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("versiongraph: encode request: %w", err)
	}
	out, ok, err := call(o, in)
	if err != nil {
		return nil, err
	}
	if !ok {
		return nil, decodeError(out)
	}
	var result T
	if err := json.Unmarshal(out, &result); err != nil {
		return nil, fmt.Errorf("versiongraph: decode result: %w", err)
	}
	return &result, nil
}

func decodeError(out []byte) error {
	var document struct {
		Error *Error `json:"error"`
	}
	if err := json.Unmarshal(out, &document); err != nil || document.Error == nil {
		return fmt.Errorf("versiongraph: unreadable error document: %q", out)
	}
	return document.Error
}

// call runs one operation of the core on a JSON input. It returns the output
// document and whether it is the operation's result (true) or an error
// document (false).
func call(o op, in []byte) ([]byte, bool, error) {
	var inPtr *C.uint8_t
	if len(in) > 0 {
		inPtr = (*C.uint8_t)(unsafe.Pointer(&in[0]))
	}
	inLen := C.size_t(len(in))
	var outPtr *C.uint8_t
	var outLen C.size_t
	var code C.int32_t
	switch o {
	case opCompose:
		code = C.vg_compose(inPtr, inLen, &outPtr, &outLen)
	case opMerge:
		code = C.vg_merge(inPtr, inLen, &outPtr, &outLen)
	case opDiff:
		code = C.vg_diff(inPtr, inLen, &outPtr, &outLen)
	case opContentHash:
		code = C.vg_content_hash(inPtr, inLen, &outPtr, &outLen)
	case opValidate:
		code = C.vg_validate(inPtr, inLen, &outPtr, &outLen)
	default:
		return nil, false, fmt.Errorf("versiongraph: unknown operation %d", o)
	}
	if code == C.VG_BAD_ARGUMENTS {
		return nil, false, errors.New("versiongraph: the core refused its arguments")
	}
	defer C.vg_free(outPtr, outLen)
	return C.GoBytes(unsafe.Pointer(outPtr), C.int(outLen)), code == C.VG_OK, nil
}
