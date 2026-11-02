package rules

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// The JSON contract of the pure core (D103), as superschematic-rules reads
// and writes it, in serde's shape for each Rust type: Go mirrors of
// @superschematic/rules's contract.ts. Every value that is the core's JSON
// (a config, parameters, a result, an own or owned value, details) stays
// json.RawMessage, so a number keeps its text and an object its members'
// order, as the core's JSON features keep them. The enums the core's README
// calls open (ReadRequest's kinds, Step, Outcome, RowWrite) are read
// without refusing a kind the binding does not know; the adapter refuses
// the command as internal instead.

// Anchor is where a use is anchored: Field "" is the whole type, which
// writes as "type"; otherwise {"field": Field}. Always written.
type Anchor struct{ Field string }

// MarshalJSON writes the anchor as serde writes the core's: "type", or
// {"field": name}.
func (a Anchor) MarshalJSON() ([]byte, error) {
	if a.Field == "" {
		return []byte(`"type"`), nil
	}
	return json.Marshal(struct {
		Field string `json:"field"`
	}{a.Field})
}

// UnmarshalJSON reads the anchor as serde reads the core's; null is the
// whole type, as an absent anchor is.
func (a *Anchor) UnmarshalJSON(data []byte) error {
	if string(bytes.TrimSpace(data)) == "null" {
		a.Field = ""
		return nil
	}
	var whole string
	if json.Unmarshal(data, &whole) == nil {
		if whole != "type" {
			return fmt.Errorf("rules: anchor %q is not \"type\" or {\"field\": name}", whole)
		}
		a.Field = ""
		return nil
	}
	var field struct {
		Field string `json:"field"`
	}
	if err := json.Unmarshal(data, &field); err != nil || field.Field == "" {
		return fmt.Errorf("rules: anchor %s is not \"type\" or {\"field\": name}", data)
	}
	a.Field = field.Field
	return nil
}

// UseSpec is one behavior on a type: superschematic_rules::UseSpec.
type UseSpec struct {
	Behavior string `json:"behavior"`
	// Config is the config as the schema gives it: nil when it gives none,
	// which the core checks as {}; "null" is a config given.
	Config json.RawMessage `json:"config,omitempty"`
	Anchor Anchor          `json:"anchor"`
}

// TypeSpec is a type the core runs: its name, its uses in list order, and
// its own fields, which a config that names a field is held to (a Timers
// field start).
type TypeSpec struct {
	Name   string      `json:"name"`
	Uses   []UseSpec   `json:"uses,omitempty"`
	Fields []FieldSpec `json:"fields,omitempty"`
}

// FieldSpec is one own field of a type: superschematic_rules::FieldSpec.
type FieldSpec struct {
	Name string `json:"name"`
	// Type is its IR type's name (string, Temporal.DateTime).
	Type string `json:"type"`
	// DateTime is true for a field whose values are instants: epoch
	// milliseconds from a table's adapter.
	DateTime bool `json:"dateTime,omitempty"`
	// Relation is the to-one relation whose target's key the field holds
	// (projectId, the relation project to Project); nil for any other
	// field (D103).
	Relation *RelationSpec `json:"relation,omitempty"`
}

// RelationSpec is a to-one relation an own field holds the key of, as the
// core's spec names it: the relation field's name and its target type.
type RelationSpec struct {
	Name string `json:"name"`
	Type string `json:"type"`
}

// Caller is who makes a call: a subject and the permissions it holds;
// StandsIn for a service standing in for an end user, which holds none.
type Caller struct {
	Subject     string   `json:"subject"`
	Permissions []string `json:"permissions,omitempty"`
	StandsIn    bool     `json:"standsIn,omitempty"`
}

// ActionKind is what an Action does.
type ActionKind string

// The kinds of Action.
const (
	ActionDefine    ActionKind = "define"
	ActionPublish   ActionKind = "publish"
	ActionCreate    ActionKind = "create"
	ActionUpdate    ActionKind = "update"
	ActionDelete    ActionKind = "delete"
	ActionOperation ActionKind = "operation"
	ActionDeadline  ActionKind = "deadline"
	// ActionChange is a change command (D103): a write of a row, whoever
	// made it, that the table's change queue hands the core.
	ActionChange ActionKind = "change"
)

// Action is what a command does. MarshalJSON writes exactly its kind's
// members; UnmarshalJSON reads them.
type Action struct {
	Kind ActionKind
	// From is a publish's published version: {"kind","from"}, from null
	// when nil, a first version.
	From *TypeSpec
	// ID is a create's key in its wire form: {"kind","id","own","params"},
	// id null when "", own and params {} when nil.
	ID  string
	Own map[string]json.RawMessage
	// Params are a create's parameters, an object by use id, or an
	// operation's parameters; {} when nil.
	Params json.RawMessage
	// Key is the row of an update {"kind","key","patch"} (patch {} when
	// nil), a delete {"kind","key"}, an operation or a deadline.
	Key   string
	Patch map[string]json.RawMessage
	// UseID and Operation name an operation:
	// {"kind","key","useId","operation","params"}. UseID names a
	// deadline's use too.
	UseID     string
	Operation string
	// Name, At and Payload are a deadline's:
	// {"kind","key","useId","name","at","payload"}, payload null when nil.
	Name    string
	At      int64
	Payload json.RawMessage
	// Change is a change command's write of the row Key names:
	// {"kind","key","change"}.
	Change *RowChange
}

// RowChange is a write of a row as the core takes it (D103): what it did
// (insert, update or delete), when, in epoch milliseconds by the database's
// clock, and the row before it (nil for an insert) and after it (nil for a
// delete).
type RowChange struct {
	Write  string    `json:"write"`
	At     int64     `json:"at"`
	Before *RowImage `json:"before,omitempty"`
	After  *RowImage `json:"after,omitempty"`
}

// RowImage is a row's fields as a write found or left them, by the core's
// logical names: its own fields by name and its uses' owned fields by use
// id and declared name, a field with no value absent.
type RowImage struct {
	Own   map[string]json.RawMessage            `json:"own"`
	Owned map[string]map[string]json.RawMessage `json:"owned"`
}

// MarshalJSON writes the image with its own and owned fields, {} when nil.
func (i RowImage) MarshalJSON() ([]byte, error) {
	owned := i.Owned
	if owned == nil {
		owned = map[string]map[string]json.RawMessage{}
	}
	return json.Marshal(struct {
		Own   map[string]json.RawMessage            `json:"own"`
		Owned map[string]map[string]json.RawMessage `json:"owned"`
	}{objectOf(i.Own), owned})
}

// name is what a refusal names as refused: the operation's name, a
// deadline's, else the kind (change for a change command).
func (a Action) name() string {
	switch a.Kind {
	case ActionOperation:
		return a.Operation
	case ActionDeadline:
		return a.Name
	default:
		return string(a.Kind)
	}
}

// emptyObject is {}, what a nil object member writes as.
var emptyObject = json.RawMessage(`{}`)

// rawOr is raw, or fallback when raw is nil or empty.
func rawOr(raw json.RawMessage, fallback json.RawMessage) json.RawMessage {
	if len(raw) == 0 {
		return fallback
	}
	return raw
}

// objectOf is a map as a JSON object, {} when nil.
func objectOf(values map[string]json.RawMessage) map[string]json.RawMessage {
	if values == nil {
		return map[string]json.RawMessage{}
	}
	return values
}

// MarshalJSON writes the action's kind and exactly its kind's members.
func (a Action) MarshalJSON() ([]byte, error) {
	switch a.Kind {
	case ActionDefine:
		return json.Marshal(struct {
			Kind ActionKind `json:"kind"`
		}{a.Kind})
	case ActionPublish:
		return json.Marshal(struct {
			Kind ActionKind `json:"kind"`
			From *TypeSpec  `json:"from"`
		}{a.Kind, a.From})
	case ActionCreate:
		var id *string
		if a.ID != "" {
			id = &a.ID
		}
		return json.Marshal(struct {
			Kind   ActionKind                 `json:"kind"`
			ID     *string                    `json:"id"`
			Own    map[string]json.RawMessage `json:"own"`
			Params json.RawMessage            `json:"params"`
		}{a.Kind, id, objectOf(a.Own), rawOr(a.Params, emptyObject)})
	case ActionUpdate:
		return json.Marshal(struct {
			Kind  ActionKind                 `json:"kind"`
			Key   string                     `json:"key"`
			Patch map[string]json.RawMessage `json:"patch"`
		}{a.Kind, a.Key, objectOf(a.Patch)})
	case ActionDelete:
		return json.Marshal(struct {
			Kind ActionKind `json:"kind"`
			Key  string     `json:"key"`
		}{a.Kind, a.Key})
	case ActionOperation:
		return json.Marshal(struct {
			Kind      ActionKind      `json:"kind"`
			Key       string          `json:"key"`
			UseID     string          `json:"useId"`
			Operation string          `json:"operation"`
			Params    json.RawMessage `json:"params"`
		}{a.Kind, a.Key, a.UseID, a.Operation, rawOr(a.Params, emptyObject)})
	case ActionDeadline:
		return json.Marshal(struct {
			Kind    ActionKind      `json:"kind"`
			Key     string          `json:"key"`
			UseID   string          `json:"useId"`
			Name    string          `json:"name"`
			At      int64           `json:"at"`
			Payload json.RawMessage `json:"payload"`
		}{a.Kind, a.Key, a.UseID, a.Name, a.At, rawOr(a.Payload, json.RawMessage(`null`))})
	case ActionChange:
		if a.Change == nil {
			return nil, fmt.Errorf("rules: a change of %s holds no write", a.Key)
		}
		return json.Marshal(struct {
			Kind   ActionKind `json:"kind"`
			Key    string     `json:"key"`
			Change *RowChange `json:"change"`
		}{a.Kind, a.Key, a.Change})
	default:
		return nil, fmt.Errorf("rules: no action of kind %q", a.Kind)
	}
}

// UnmarshalJSON reads an action of any kind the binding knows.
func (a *Action) UnmarshalJSON(data []byte) error {
	var wire struct {
		Kind      ActionKind                 `json:"kind"`
		From      *TypeSpec                  `json:"from"`
		ID        *string                    `json:"id"`
		Own       map[string]json.RawMessage `json:"own"`
		Params    json.RawMessage            `json:"params"`
		Key       string                     `json:"key"`
		Patch     map[string]json.RawMessage `json:"patch"`
		UseID     string                     `json:"useId"`
		Operation string                     `json:"operation"`
		Name      string                     `json:"name"`
		At        int64                      `json:"at"`
		Payload   json.RawMessage            `json:"payload"`
		Change    *RowChange                 `json:"change"`
	}
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	out := Action{Kind: wire.Kind}
	switch wire.Kind {
	case ActionDefine:
	case ActionPublish:
		out.From = wire.From
	case ActionCreate:
		if wire.ID != nil {
			out.ID = *wire.ID
		}
		out.Own, out.Params = wire.Own, wire.Params
	case ActionUpdate:
		out.Key, out.Patch = wire.Key, wire.Patch
	case ActionDelete:
		out.Key = wire.Key
	case ActionOperation:
		out.Key, out.UseID, out.Operation, out.Params = wire.Key, wire.UseID, wire.Operation, wire.Params
	case ActionDeadline:
		out.Key, out.UseID, out.Name, out.At, out.Payload = wire.Key, wire.UseID, wire.Name, wire.At, wire.Payload
	case ActionChange:
		if wire.Change == nil {
			return fmt.Errorf("rules: a change of %s holds no write", wire.Key)
		}
		out.Key, out.Change = wire.Key, wire.Change
	default:
		return fmt.Errorf("rules: no action of kind %q", wire.Kind)
	}
	*a = out
	return nil
}

// Command is one command: who calls, when (epoch milliseconds), what, and
// the host's id for the request, the same for every run of the command.
type Command struct {
	Caller  Caller `json:"caller"`
	Now     int64  `json:"now"`
	Action  Action `json:"action"`
	Request string `json:"request,omitempty"`
}

// ReadKind is what a ReadRequest asks for. The list is open: a kind the
// adapter does not know refuses the command as internal.
type ReadKind string

// The kinds of ReadRequest the adapter answers.
const (
	ReadKindRow     ReadKind = "row"
	ReadKindHasRows ReadKind = "hasRows"
	ReadKindSpec    ReadKind = "spec"
	ReadKindSubRows ReadKind = "subRows"
	// ReadKindLinked is the rows of a type whose own field holds a key
	// (D103): what a rule over the rows that link to a row reads.
	ReadKindLinked ReadKind = "linked"
)

// LinkedPage is how many rows a linked read asks for at a time, the core's
// LINKED_PAGE.
const LinkedPage = 500

// ReadRequest is what the core asks the host to read: a row of a type by
// its key, whether a type has rows, a type's spec, the rows of one
// sub-table of one use on one row (UseID and Table name it), or one page of
// the rows of a type whose own field Field holds Key, in key order, at
// most Limit, after the key After when it is not "".
type ReadRequest struct {
	Kind  ReadKind `json:"kind"`
	Type  string   `json:"type"`
	Key   string   `json:"key,omitempty"`
	UseID string   `json:"useId,omitempty"`
	Table string   `json:"table,omitempty"`
	Field string   `json:"field,omitempty"`
	After string   `json:"after,omitempty"`
	Limit int      `json:"limit,omitempty"`
}

// MarshalJSON writes the request with its kind's members: a linked read's
// limit always, any other kind's members as they are set.
func (r ReadRequest) MarshalJSON() ([]byte, error) {
	if r.Kind == ReadKindLinked {
		var after *string
		if r.After != "" {
			after = &r.After
		}
		return json.Marshal(struct {
			Kind  ReadKind `json:"kind"`
			Type  string   `json:"type"`
			Field string   `json:"field"`
			Key   string   `json:"key"`
			After *string  `json:"after,omitempty"`
			Limit int      `json:"limit"`
		}{r.Kind, r.Type, r.Field, r.Key, after, r.Limit})
	}
	type plain ReadRequest
	return json.Marshal(plain(r))
}

// Row is one row as the host read it: its own fields, its owned fields by
// use id then declared name, and its version.
type Row struct {
	Own     map[string]json.RawMessage            `json:"own,omitempty"`
	Owned   map[string]map[string]json.RawMessage `json:"owned,omitempty"`
	Version int64                                 `json:"version"`
}

// SubRow is one sub-table row as the host read it: its key, and its values
// by the declared field's name, the key's field left out and a NULL column
// absent.
type SubRow struct {
	Key    string                     `json:"key"`
	Values map[string]json.RawMessage `json:"values"`
}

// MarshalJSON writes the row with its values, {} when nil.
func (r SubRow) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		Key    string                     `json:"key"`
		Values map[string]json.RawMessage `json:"values"`
	}{r.Key, objectOf(r.Values)})
}

// LinkedRows are the rows a linked read asks for, each by its key, and
// whether more follow the page.
type LinkedRows struct {
	Rows []KeyedRow `json:"rows"`
	More bool       `json:"more"`
}

// KeyedRow is a row of a linked read: its key in the core's form and the
// row as a row read gives it.
type KeyedRow struct {
	Key string `json:"key"`
	Row Row    `json:"row"`
}

// Answer is the host's answer to one request, of its kind:
// {"row": Row|null}, {"hasRows": bool}, {"spec": TypeSpec|null},
// {"subRows": [SubRow]} or {"linked": LinkedRows}.
type Answer struct {
	Kind    ReadKind
	Row     *Row
	HasRows bool
	Spec    *TypeSpec
	SubRows []SubRow
	Linked  *LinkedRows
}

// MarshalJSON writes the answer as the variant its kind names.
func (a Answer) MarshalJSON() ([]byte, error) {
	switch a.Kind {
	case ReadKindRow:
		return json.Marshal(struct {
			Row *Row `json:"row"`
		}{a.Row})
	case ReadKindHasRows:
		return json.Marshal(struct {
			HasRows bool `json:"hasRows"`
		}{a.HasRows})
	case ReadKindSpec:
		return json.Marshal(struct {
			Spec *TypeSpec `json:"spec"`
		}{a.Spec})
	case ReadKindSubRows:
		rows := a.SubRows
		if rows == nil {
			rows = []SubRow{}
		}
		return json.Marshal(struct {
			SubRows []SubRow `json:"subRows"`
		}{rows})
	case ReadKindLinked:
		linked := LinkedRows{Rows: []KeyedRow{}}
		if a.Linked != nil {
			linked.More = a.Linked.More
			if a.Linked.Rows != nil {
				linked.Rows = a.Linked.Rows
			}
		}
		return json.Marshal(struct {
			Linked LinkedRows `json:"linked"`
		}{linked})
	default:
		return nil, fmt.Errorf("rules: no answer of kind %q", a.Kind)
	}
}

// UnmarshalJSON reads an answer of any variant the binding knows.
func (a *Answer) UnmarshalJSON(data []byte) error {
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	if len(wire) != 1 {
		return fmt.Errorf("rules: an answer is one variant, not %s", data)
	}
	out := Answer{}
	for variant, value := range wire {
		out.Kind = ReadKind(variant)
		var err error
		switch out.Kind {
		case ReadKindRow:
			err = json.Unmarshal(value, &out.Row)
		case ReadKindHasRows:
			err = json.Unmarshal(value, &out.HasRows)
		case ReadKindSpec:
			err = json.Unmarshal(value, &out.Spec)
		case ReadKindSubRows:
			err = json.Unmarshal(value, &out.SubRows)
		case ReadKindLinked:
			err = json.Unmarshal(value, &out.Linked)
		default:
			err = fmt.Errorf("rules: no answer of kind %q", variant)
		}
		if err != nil {
			return err
		}
	}
	*a = out
	return nil
}

// Answered is one answered request, and how many locks the host held when
// it made the read.
type Answered struct {
	Request ReadRequest `json:"request"`
	Answer  Answer      `json:"answer"`
	Held    int         `json:"held"`
}

// Answers is what the host has answered so far for one command: each
// request with its answer, and the lock keys it holds in the order it took
// them.
type Answers struct {
	Reads []Answered `json:"reads,omitempty"`
	Locks []string   `json:"locks,omitempty"`
}

// Step is one run's answer: exactly one of Read, Lock and Done is set for a
// step the binding knows; none is set for another kind (the enum is open),
// whose JSON Raw holds.
type Step struct {
	Read []ReadRequest
	Lock []string
	Done *Outcome
	Raw  json.RawMessage
}

// MarshalJSON writes the step as the variant it is, or Raw for another kind.
func (s Step) MarshalJSON() ([]byte, error) {
	switch {
	case s.Done != nil:
		return json.Marshal(struct {
			Done *Outcome `json:"done"`
		}{s.Done})
	case s.Read != nil:
		return json.Marshal(struct {
			Read []ReadRequest `json:"read"`
		}{s.Read})
	case s.Lock != nil:
		return json.Marshal(struct {
			Lock []string `json:"lock"`
		}{s.Lock})
	case len(s.Raw) > 0:
		return s.Raw, nil
	default:
		return nil, fmt.Errorf("rules: a step of no kind")
	}
}

// UnmarshalJSON reads a step; one of another kind keeps its JSON in Raw.
func (s *Step) UnmarshalJSON(data []byte) error {
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	out := Step{}
	known := false
	if len(wire) == 1 {
		for variant, value := range wire {
			var err error
			switch variant {
			case "read":
				err, known = json.Unmarshal(value, &out.Read), true
				if out.Read == nil {
					out.Read = []ReadRequest{}
				}
			case "lock":
				err, known = json.Unmarshal(value, &out.Lock), true
				if out.Lock == nil {
					out.Lock = []string{}
				}
			case "done":
				out.Done = &Outcome{}
				err, known = json.Unmarshal(value, out.Done), true
			}
			if err != nil {
				return err
			}
		}
	}
	if !known {
		out = Step{Raw: append(json.RawMessage(nil), data...)}
	}
	*s = out
	return nil
}

// Outcome is a command's outcome: a refusal or a change set; neither set
// for another kind, whose JSON Raw holds.
type Outcome struct {
	Refused *Refusal
	Changed *ChangeSet
	Raw     json.RawMessage
}

// MarshalJSON writes the outcome as the variant it is, or Raw for another
// kind.
func (o Outcome) MarshalJSON() ([]byte, error) {
	switch {
	case o.Refused != nil:
		return json.Marshal(struct {
			Refused *Refusal `json:"refused"`
		}{o.Refused})
	case o.Changed != nil:
		return json.Marshal(struct {
			Changed *ChangeSet `json:"changed"`
		}{o.Changed})
	case len(o.Raw) > 0:
		return o.Raw, nil
	default:
		return nil, fmt.Errorf("rules: an outcome of no kind")
	}
}

// UnmarshalJSON reads an outcome; one of another kind keeps its JSON in
// Raw.
func (o *Outcome) UnmarshalJSON(data []byte) error {
	var wire map[string]json.RawMessage
	if err := json.Unmarshal(data, &wire); err != nil {
		return err
	}
	out := Outcome{}
	known := false
	if len(wire) == 1 {
		for variant, value := range wire {
			var err error
			switch variant {
			case "refused":
				out.Refused = &Refusal{}
				err, known = json.Unmarshal(value, out.Refused), true
			case "changed":
				out.Changed = &ChangeSet{}
				err, known = json.Unmarshal(value, out.Changed), true
			}
			if err != nil {
				return err
			}
		}
	}
	if !known {
		out = Outcome{Raw: append(json.RawMessage(nil), data...)}
	}
	*o = out
	return nil
}

// RowWriteKind is what a RowWrite does. The list is open: a kind the
// adapter does not know refuses the command as internal.
type RowWriteKind string

// The kinds of RowWrite the adapter applies.
const (
	RowCreate RowWriteKind = "create"
	RowUpdate RowWriteKind = "update"
	RowDelete RowWriteKind = "delete"
	// RowGone is a row a change command found gone (D103): no write of the
	// row; its deadlines go, and its sub-table rows when no row of the key
	// is left at all.
	RowGone RowWriteKind = "gone"
)

// RowWrite is the write of one row a change set holds: a create of a key
// with its own and owned fields, an update of a row held to the version
// the host read it at, a delete held alike, or a row a change command found
// gone.
type RowWrite struct {
	Kind RowWriteKind `json:"kind"`
	Type string       `json:"type"`
	// Key is the row's key; nil only for a create without an id.
	Key             *string                               `json:"key"`
	ExpectedVersion int64                                 `json:"expectedVersion,omitempty"`
	Own             map[string]json.RawMessage            `json:"own,omitempty"`
	Owned           map[string]map[string]json.RawMessage `json:"owned,omitempty"`
	// Raw is the write as the core wrote it, for an unknown kind's message.
	Raw json.RawMessage `json:"-"`
}

// UnmarshalJSON reads a row write of any kind, keeping its JSON in Raw.
func (w *RowWrite) UnmarshalJSON(data []byte) error {
	type plain RowWrite
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	out.Raw = append(json.RawMessage(nil), data...)
	*w = RowWrite(out)
	return nil
}

// RowRef is a row a change set names: its type and its key, nil only for
// the row a create without an id makes.
type RowRef struct {
	Type string  `json:"type"`
	Key  *string `json:"key"`
}

// is reports whether ref names the row of a type by its key.
func (ref RowRef) is(typeName, key string) bool {
	return ref.Type == typeName && ref.Key != nil && *ref.Key == key
}

// SubRowChangeKind is what a SubRowChange does. The list is open: a kind
// the adapter does not know refuses the command as internal.
type SubRowChangeKind string

// The kinds of SubRowChange the adapter applies.
const (
	SubRowInsert SubRowChangeKind = "insert"
	SubRowUpdate SubRowChangeKind = "update"
	SubRowDelete SubRowChangeKind = "delete"
)

// SubRowChange is what happens to a sub-table row: an insert of its values,
// an update of the values it sets, or a delete. Values are by the declared
// field's name.
type SubRowChange struct {
	Kind   SubRowChangeKind           `json:"kind"`
	Values map[string]json.RawMessage `json:"values,omitempty"`
	// Raw is the change as the core wrote it, for an unknown kind's
	// message.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON writes the change as serde writes the core's: an insert's or
// an update's kind and values, a delete's kind, another kind's Raw.
func (c SubRowChange) MarshalJSON() ([]byte, error) {
	switch c.Kind {
	case SubRowInsert, SubRowUpdate:
		return json.Marshal(struct {
			Kind   SubRowChangeKind           `json:"kind"`
			Values map[string]json.RawMessage `json:"values"`
		}{c.Kind, objectOf(c.Values)})
	case SubRowDelete:
		return json.Marshal(struct {
			Kind SubRowChangeKind `json:"kind"`
		}{c.Kind})
	}
	if len(c.Raw) > 0 {
		return c.Raw, nil
	}
	return nil, fmt.Errorf("rules: no sub-row change of kind %q", c.Kind)
}

// UnmarshalJSON reads a sub-row change of any kind, keeping its JSON in
// Raw.
func (c *SubRowChange) UnmarshalJSON(data []byte) error {
	type plain SubRowChange
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	out.Raw = append(json.RawMessage(nil), data...)
	*c = SubRowChange(out)
	return nil
}

// SubRowWrite is one write of a sub-table row: the row that owns it, the
// use that owns the sub-table, the sub-table's declared name, the row's key
// in it, and the change.
type SubRowWrite struct {
	Row    RowRef       `json:"row"`
	UseID  string       `json:"useId"`
	Table  string       `json:"table"`
	Key    string       `json:"key"`
	Change SubRowChange `json:"change"`
}

// DeadlineChangeKind is what a DeadlineChange does. The list is open: a
// kind the adapter does not know refuses the command as internal.
type DeadlineChangeKind string

// The kinds of DeadlineChange the adapter applies.
const (
	DeadlineArm   DeadlineChangeKind = "arm"
	DeadlineClear DeadlineChangeKind = "clear"
)

// DeadlineChange is a deadline of a row a use arms or clears, named by its
// row, its use and its name: an arm falls due at At (epoch milliseconds),
// when the host runs the deadline command with the time and the payload.
type DeadlineChange struct {
	Kind    DeadlineChangeKind `json:"kind"`
	Row     RowRef             `json:"row"`
	UseID   string             `json:"useId"`
	Name    string             `json:"name"`
	At      int64              `json:"at"`
	Payload json.RawMessage    `json:"payload"`
	// Raw is the change as the core wrote it, for an unknown kind's
	// message.
	Raw json.RawMessage `json:"-"`
}

// MarshalJSON writes the change as serde writes the core's: an arm's kind,
// row, use, name, time and payload (null when nil), a clear's kind, row,
// use and name, another kind's Raw.
func (c DeadlineChange) MarshalJSON() ([]byte, error) {
	switch c.Kind {
	case DeadlineArm:
		return json.Marshal(struct {
			Kind    DeadlineChangeKind `json:"kind"`
			Row     RowRef             `json:"row"`
			UseID   string             `json:"useId"`
			Name    string             `json:"name"`
			At      int64              `json:"at"`
			Payload json.RawMessage    `json:"payload"`
		}{c.Kind, c.Row, c.UseID, c.Name, c.At, rawOr(c.Payload, json.RawMessage(`null`))})
	case DeadlineClear:
		return json.Marshal(struct {
			Kind  DeadlineChangeKind `json:"kind"`
			Row   RowRef             `json:"row"`
			UseID string             `json:"useId"`
			Name  string             `json:"name"`
		}{c.Kind, c.Row, c.UseID, c.Name})
	}
	if len(c.Raw) > 0 {
		return c.Raw, nil
	}
	return nil, fmt.Errorf("rules: no deadline change of kind %q", c.Kind)
}

// UnmarshalJSON reads a deadline change of any kind, keeping its JSON in
// Raw.
func (c *DeadlineChange) UnmarshalJSON(data []byte) error {
	type plain DeadlineChange
	var out plain
	if err := json.Unmarshal(data, &out); err != nil {
		return err
	}
	out.Raw = append(json.RawMessage(nil), data...)
	*c = DeadlineChange(out)
	return nil
}

// ChangeSet is everything one command changes: its row writes, its
// sub-table rows and deadlines, which the adapter applies by the layout,
// and its outbox events of a behavior's own, which stay raw until the
// event stream gives them a table; the adapter refuses a change set
// holding any (D103).
type ChangeSet struct {
	Rows      []RowWrite        `json:"rows"`
	SubRows   []SubRowWrite     `json:"subRows"`
	Events    []json.RawMessage `json:"events"`
	Deadlines []DeadlineChange  `json:"deadlines"`
	Result    json.RawMessage   `json:"result"`
}
