// Package graphroutes holds what a generated Go router's graph operations
// (D138) share: the routes the loader adds for an API's @graphOperations
// sets, which run each operation of a version graph through the graph's
// generated facade in the database's ORM, with no implementation method.
// The generated graphs.go of an API reads the root, the ref and the commit
// a route names through the ORM, holds an operation that writes a change
// set to the set's row rule, calls the facade as the request's caller, and
// answers what the facade returns as the API's types hold it. This package
// gives it, as the TypeScript runtime's graph-routes.ts does:
//
//   - the problems a refusal is answered with, by its code: not_found 404;
//     no_actor 403; version_conflict, ref_sealed, primary_merge_only,
//     schema_epoch, name_taken, nothing_to_commit, not_tagged, no_parent,
//     merge_into_itself and root_mismatch 409; and an input the engine or
//     the core refuses 422 (invalid_tree with the core's findings as
//     details, invalid_row, entity_not_found, invalid_arrangement,
//     order_not_a_key, invalid_resolution, unmatched_resolution and the
//     rest). Any other error is the server's own;
//   - the members of each operation's body, decoded from the API's input
//     type (BranchInput, SaveInput and the rest);
//   - a save's edits as the facade takes them, and the rows of a tree the
//     facade returns as the API's row types hold them: each row's entity
//     key and its content members, a to-one relation as its key;
//   - a ref, a commit and a release pointer as the API's views hold them,
//     from the rows of the graph's own tables the facade returns.
//
// It imports neither the ORM nor the version-graph engine, so the HTTP
// runtime stays free of them: the generated code passes what it read of
// them as values, and names a refusal by the engine's code of it.
package graphroutes

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"

	scalars "github.com/parable-work/superscalar/go"

	"github.com/parable-work/superschematic/runtime/http/go/apperror"
)

// PrimaryName is the name of the primary line branch creates for a root
// that has none.
const PrimaryName = "main"

// Relation is a field of a member kind that holds a to-one relation: the
// row's member that holds its key (authorId), the facade's field (author)
// and the target's key (id).
type Relation struct {
	Member string
	Field  string
	Key    string
}

// Kind is one member kind of a served graph.
type Kind struct {
	// Kind is the kind as the graph's descriptor names it (line_item).
	Kind string
	// Member is the member of a tree and of the edits that holds its
	// rows, as the API's types name it (lineItem).
	Member string
	// Fields are the members of its row beside entityKey: the content
	// fields a typed upsert writes.
	Fields []string
	// Relations are the fields among them that hold a to-one relation's
	// key.
	Relations []Relation
}

// relation is the relation whose row member is member, or nil.
func (k *Kind) relation(member string) *Relation {
	for i := range k.Relations {
		if k.Relations[i].Member == member {
			return &k.Relations[i]
		}
	}
	return nil
}

// Set is a version graph as a generated router serves it (D138).
type Set struct {
	// Graph is the graph's name, which a refusal names, and Root the
	// root's type.
	Graph string
	Root  string
	// RootKey is the root's key, which a row of the graph's own tables
	// holds the root under.
	RootKey string
	// Kinds are the graph's member kinds, in descriptor order.
	Kinds []Kind
}

// Kind is the member kind the descriptor names kind, or nil.
func (s *Set) Kind(kind string) *Kind {
	for i := range s.Kinds {
		if s.Kinds[i].Kind == kind {
			return &s.Kinds[i]
		}
	}
	return nil
}

// statuses are the refusals of the graph's engine and core the handlers
// answer, by code, with the status of each.
var statuses = map[string]int{
	"no_actor":             http.StatusForbidden,
	"not_found":            http.StatusNotFound,
	"version_conflict":     http.StatusConflict,
	"ref_sealed":           http.StatusConflict,
	"primary_merge_only":   http.StatusConflict,
	"schema_epoch":         http.StatusConflict,
	"name_taken":           http.StatusConflict,
	"nothing_to_commit":    http.StatusConflict,
	"not_tagged":           http.StatusConflict,
	"no_parent":            http.StatusConflict,
	"merge_into_itself":    http.StatusConflict,
	"root_mismatch":        http.StatusConflict,
	"invalid_tree":         http.StatusUnprocessableEntity,
	"entity_not_found":     http.StatusUnprocessableEntity,
	"invalid_row":          http.StatusUnprocessableEntity,
	"unknown_kind":         http.StatusUnprocessableEntity,
	"duplicate_entity_key": http.StatusUnprocessableEntity,
	"order_out_of_range":   http.StatusUnprocessableEntity,
	"invalid_resolution":   http.StatusUnprocessableEntity,
	"unmatched_resolution": http.StatusUnprocessableEntity,
	"no_order_key":         http.StatusUnprocessableEntity,
	"order_not_a_key":      http.StatusUnprocessableEntity,
	"invalid_arrangement":  http.StatusUnprocessableEntity,
}

// Status is the status a refusal of code is answered with, and false for
// a code no refusal has.
func Status(code string) (int, bool) {
	status, ok := statuses[code]
	return status, ok
}

// category is the AppError code of a status.
func category(status int) string {
	switch status {
	case http.StatusNotFound:
		return apperror.ErrCodeNotFound
	case http.StatusForbidden:
		return apperror.ErrCodeForbidden
	case http.StatusConflict:
		return apperror.ErrCodeConflict
	case http.StatusUnprocessableEntity:
		return apperror.ErrCodeUnprocessable
	}
	return apperror.ErrCodeInternal
}

// Problem is the problem of the refusal code, with message as its detail
// and details, when not nil, as its details.
func Problem(code, message string, details any, cause error) *apperror.AppError {
	status, ok := statuses[code]
	if !ok {
		status = http.StatusInternalServerError
	}
	return &apperror.AppError{Code: category(status), ErrorCode: code, Message: message, Err: cause, Details: details}
}

// NotFound is the 404 not_found of what the request named and the root
// holds none of: "Recipe not found", "ref not found".
func NotFound(message string) *apperror.AppError {
	return Problem("not_found", message, nil, nil)
}

// NoActor is the 403 no_actor of a write whose caller has no subject that
// is a UUID, which the graph records as the write's actor.
func NoActor() *apperror.AppError {
	return Problem("no_actor", "a write to a version graph records its caller, and the request's caller has no subject that is a UUID", nil, nil)
}

// PrimaryNotDiscarded is the 409 primary_merge_only of a discard of a
// primary line: discarding it would leave the root a line the next branch
// makes again, empty.
func PrimaryNotDiscarded() *apperror.AppError {
	return Problem("primary_merge_only", "the ref is a primary line, which is never discarded: discard a change set of it", nil, nil)
}

// NoParent is the 409 no_parent of a merge of a primary line that names no
// ref to merge into.
func NoParent() *apperror.AppError {
	return Problem("no_parent", "the ref is a primary line, which has no parent to merge into: name the ref to merge into", nil, nil)
}

// UnknownKind is the 422 unknown_kind of an arrange of a kind the graph
// has none of.
func UnknownKind(graph, kind string) *apperror.AppError {
	return Problem("unknown_kind", fmt.Sprintf("the %s graph has no kind %s", graph, kind), nil, nil)
}

// Refusal answers err, which a graph operation returned, as the problem of
// its code when the graph's engine or core refused the operation: code is
// the engine's code of it (engine.ErrorCode), message what the refusal
// says, and findings, when not nil, the core's findings of an invalid tree,
// answered as details.findings. A problem the handler made is returned as
// it is, and so is any other error, which the route answers as its
// internal error, with none of its words.
func Refusal(err error, code, message string, findings any) error {
	if err == nil {
		return nil
	}
	var problem *apperror.AppError
	if errors.As(err, &problem) {
		return err
	}
	if _, ok := statuses[code]; !ok {
		return err
	}
	var details any
	if findings != nil {
		details = map[string]any{"findings": findings}
	}
	return Problem(code, message, details, err)
}

// Values is a JSON object, decoded.
type Values = map[string]any

// Recode decodes into out the JSON of from: a value of one generated type
// read as another that holds the same members.
func Recode(from, out any) error {
	data, err := json.Marshal(from)
	if err != nil {
		return fmt.Errorf("graph operation: encode %T: %w", from, err)
	}
	if err := json.Unmarshal(data, out); err != nil {
		return fmt.Errorf("graph operation: decode %T as %T: %w", from, out, err)
	}
	return nil
}

// object is the JSON object of value, or nil for a value that is none.
func object(value any) (Values, error) {
	data, err := json.Marshal(value)
	if err != nil {
		return nil, fmt.Errorf("graph operation: encode %T: %w", value, err)
	}
	var out Values
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, nil
	}
	return out, nil
}

// member is the value of the member name of an object, matched as
// encoding/json matches a struct's field, and nil for none.
func member(values Values, name string) any {
	if value, ok := values[name]; ok {
		return value
	}
	for key, value := range values {
		if strings.EqualFold(key, name) {
			return value
		}
	}
	return nil
}

// keyOf is the key a related row's object holds under key, or nil.
func keyOf(value any, key string) any {
	related, ok := value.(Values)
	if !ok {
		return nil
	}
	return related[key]
}

// BranchInput is the body of branch.
type BranchInput struct {
	From *scalars.IdentityUUID `json:"from"`
	Name string                `json:"name"`
}

// VersionInput is the body of seal and discard.
type VersionInput struct {
	Version int64 `json:"version"`
}

// SaveInput is the body of save: the edits stay as the API's edits type
// holds them, for FacadeEdits.
type SaveInput struct {
	Version int64           `json:"version"`
	Edits   json.RawMessage `json:"edits"`
}

// ArrangeInput is the body of arrange.
type ArrangeInput struct {
	Version int64                  `json:"version"`
	Kind    string                 `json:"kind"`
	Parent  *scalars.IdentityUUID  `json:"parent"`
	Keys    []scalars.IdentityUUID `json:"keys"`
}

// CommitInput is the body of commit.
type CommitInput struct {
	Version int64   `json:"version"`
	Message *string `json:"message"`
	Tag     *bool   `json:"tag"`
}

// Resolution is one resolution of a merge's or a rebase's body.
type Resolution struct {
	Kind      string               `json:"kind"`
	EntityKey scalars.IdentityUUID `json:"entityKey"`
	Path      string               `json:"path"`
	Take      *string              `json:"take"`
	// Value is the unit's value as JSON text, "null" among them.
	Value *string `json:"value"`
}

// ValueJSON is the resolution's value as the facade takes it, nil for
// none.
func (r Resolution) ValueJSON() json.RawMessage {
	if r.Value == nil {
		return nil
	}
	return json.RawMessage(*r.Value)
}

// TakeOf is the side the resolution takes, "" for none.
func (r Resolution) TakeOf() string {
	if r.Take == nil {
		return ""
	}
	return *r.Take
}

// MergeInput is the body of merge.
type MergeInput struct {
	Into        *scalars.IdentityUUID `json:"into"`
	Version     int64                 `json:"version"`
	Resolutions []Resolution          `json:"resolutions"`
	Message     *string               `json:"message"`
	Tag         *bool                 `json:"tag"`
}

// RebaseInput is the body of rebase.
type RebaseInput struct {
	Version     int64        `json:"version"`
	Resolutions []Resolution `json:"resolutions"`
}

// RevertInput is the body of revert.
type RevertInput struct {
	Version  int64                `json:"version"`
	ToCommit scalars.IdentityUUID `json:"toCommit"`
}

// ReleaseInput is the body of release.
type ReleaseInput struct {
	Commit  scalars.IdentityUUID `json:"commit"`
	Version int64                `json:"version"`
}

// Message is a commit's message the body names, "" for none.
func Message(message *string) string {
	if message == nil {
		return ""
	}
	return *message
}

// Tagged reports whether the body tags the commit.
func Tagged(tag *bool) bool {
	return tag != nil && *tag
}

// FacadeEdits are a save's edits, as the API's edits type holds them, as
// the graph's facade takes them: each kind's upserts with each relation's
// key as the object that holds it under the target's key, its deletes and
// its unsets, under the kind's member. It returns their JSON, which the
// facade's edits type decodes from.
func FacadeEdits(set *Set, edits json.RawMessage) (json.RawMessage, error) {
	var byMember map[string]struct {
		Upsert []Values `json:"upsert"`
		Delete []any    `json:"delete"`
		Unset  []any    `json:"unset"`
	}
	if len(edits) > 0 && string(edits) != "null" {
		if err := json.Unmarshal(edits, &byMember); err != nil {
			return nil, fmt.Errorf("graph operation: the edits of the %s graph: %w", set.Graph, err)
		}
	}
	out := map[string]Values{}
	for i := range set.Kinds {
		kind := &set.Kinds[i]
		own, ok := byMember[kind.Member]
		if !ok {
			continue
		}
		upserts := make([]Values, 0, len(own.Upsert))
		for _, row := range own.Upsert {
			upserts = append(upserts, facadeValue(kind, row))
		}
		out[kind.Member] = Values{"upsert": upserts, "delete": nonNil(own.Delete), "unset": nonNil(own.Unset)}
	}
	return json.Marshal(out)
}

func nonNil(values []any) []any {
	if values == nil {
		return []any{}
	}
	return values
}

// facadeValue is the typed value the facade takes for a row the API's
// types hold: its members, each relation's key as the object that holds
// it.
func facadeValue(kind *Kind, row Values) Values {
	out := Values{}
	if key, ok := row["entityKey"]; ok && key != nil {
		out["entityKey"] = key
	}
	for _, field := range kind.Fields {
		value, present := row[field]
		if relation := kind.relation(field); relation != nil {
			if value == nil {
				out[relation.Field] = nil
			} else {
				out[relation.Field] = Values{relation.Key: value}
			}
			continue
		}
		if present {
			out[field] = value
		}
	}
	return out
}

// apiRow is the row the API's types hold of a typed value of the facade's:
// its entity key and its content members.
func apiRow(kind *Kind, value Values) Values {
	out := Values{"entityKey": value["entityKey"]}
	for _, field := range kind.Fields {
		if relation := kind.relation(field); relation != nil {
			out[field] = keyOf(value[relation.Field], relation.Key)
			continue
		}
		out[field] = value[field]
	}
	return out
}

// Rows are each kind's rows of a tree the facade returned, as the API's
// row types hold them, under each kind's member: tree is the facade's
// tree type, whose field of each kind's rows is the member, matched as
// encoding/json matches a field.
func Rows(set *Set, tree any) (Values, error) {
	values, err := object(tree)
	if err != nil {
		return nil, err
	}
	out := Values{}
	for i := range set.Kinds {
		kind := &set.Kinds[i]
		rows, _ := member(values, kind.Member).([]any)
		converted := make([]Values, 0, len(rows))
		for _, row := range rows {
			typed, _ := row.(Values)
			converted = append(converted, apiRow(kind, typed))
		}
		out[kind.Member] = converted
	}
	return out, nil
}

// Tree is a tree the facade returned as the API's tree type holds it: each
// kind's rows, its content hash and its findings.
func Tree(set *Set, tree any, contentHash string, findings any) (Values, error) {
	out, err := Rows(set, tree)
	if err != nil {
		return nil, err
	}
	out["contentHash"] = contentHash
	found, err := findingValues(findings)
	if err != nil {
		return nil, err
	}
	out["findings"] = found
	return out, nil
}

// findingValues are the core's findings as the API's finding type holds
// them, a finding of a whole kind with no entity key.
func findingValues(findings any) ([]Values, error) {
	data, err := json.Marshal(findings)
	if err != nil {
		return nil, err
	}
	var list []Values
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("graph operation: the findings of a tree: %w", err)
	}
	out := make([]Values, 0, len(list))
	for _, finding := range list {
		if key, ok := finding["entityKey"].(string); ok && key == "" {
			delete(finding, "entityKey")
		}
		out = append(out, finding)
	}
	return out, nil
}

// RefView is a row of the graph's ref table, as the facade returns it, as
// the API's ref view holds it.
func RefView(set *Set, row any) (Values, error) {
	values, err := object(row)
	if err != nil || values == nil {
		return nil, errOr(err, "a ref")
	}
	out := Values{
		"id":        values["id"],
		"root":      keyOf(values["root"], set.RootKey),
		"name":      values["name"],
		"sealed":    values["sealedAt"] != nil,
		"discarded": values["deletedAt"] != nil,
		"version":   values["_version"],
	}
	optional(out, "parent", keyOf(values["parentRef"], "id"))
	optional(out, "base", keyOf(values["baseCommit"], "id"))
	optional(out, "head", keyOf(values["headCommit"], "id"))
	return out, nil
}

// CommitView is a row of the graph's commit table, as the facade returns
// it, as the API's commit view holds it; snapshot reports whether the
// commit's whole tree is stored.
func CommitView(set *Set, row any, snapshot bool) (Values, error) {
	values, err := object(row)
	if err != nil || values == nil {
		return nil, errOr(err, "a commit")
	}
	message, _ := values["message"].(string)
	out := Values{
		"id":          values["id"],
		"root":        keyOf(values["root"], set.RootKey),
		"ref":         keyOf(values["ref"], "id"),
		"message":     message,
		"schemaEpoch": values["schemaEpoch"],
		"contentHash": values["contentHash"],
		"createdAt":   values["createdAt"],
		"createdBy":   values["createdBy"],
		"snapshot":    snapshot,
	}
	optional(out, "parent", keyOf(values["parentCommit"], "id"))
	optional(out, "sequence", values["sequence"])
	return out, nil
}

// ReleaseView is a row of the graph's release table, as the facade
// returns it, as the API's release view holds it.
func ReleaseView(set *Set, row any) (Values, error) {
	values, err := object(row)
	if err != nil || values == nil {
		return nil, errOr(err, "a release pointer")
	}
	return Values{
		"id":      values["id"],
		"root":    keyOf(values["root"], set.RootKey),
		"commit":  keyOf(values["commit"], "id"),
		"version": values["_version"],
	}, nil
}

// optional sets name to value unless value is nil.
func optional(out Values, name string, value any) {
	if value != nil {
		out[name] = value
	}
}

func errOr(err error, what string) error {
	if err != nil {
		return err
	}
	return fmt.Errorf("graph operation: the facade returned no %s", what)
}

// JSONText is a value the engine returned as JSON as the API holds it: its
// JSON text, or nil for none.
func JSONText(value json.RawMessage) any {
	if len(value) == 0 {
		return nil
	}
	return string(value)
}

// Actor is the subject a JSON value of an author column names, or nil.
func Actor(value json.RawMessage) any {
	if len(value) == 0 || string(value) == "null" {
		return nil
	}
	var subject any
	if err := json.Unmarshal(value, &subject); err != nil {
		return nil
	}
	return subject
}
