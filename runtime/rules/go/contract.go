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

// TypeSpec is a type the core runs: its name and its uses in list order.
type TypeSpec struct {
	Name string    `json:"name"`
	Uses []UseSpec `json:"uses,omitempty"`
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
}

// name is what a refusal names as refused: the operation's name, a
// deadline's, else the kind.
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
)

// ReadRequest is what the core asks the host to read: a row of a type by
// its key, whether a type has rows, or a type's spec.
type ReadRequest struct {
	Kind ReadKind `json:"kind"`
	Type string   `json:"type"`
	Key  string   `json:"key,omitempty"`
}

// Row is one row as the host read it: its own fields, its owned fields by
// use id then declared name, and its version.
type Row struct {
	Own     map[string]json.RawMessage            `json:"own,omitempty"`
	Owned   map[string]map[string]json.RawMessage `json:"owned,omitempty"`
	Version int64                                 `json:"version"`
}

// Answer is the host's answer to one request, of its kind:
// {"row": Row|null}, {"hasRows": bool} or {"spec": TypeSpec|null}.
type Answer struct {
	Kind    ReadKind
	Row     *Row
	HasRows bool
	Spec    *TypeSpec
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
)

// RowWrite is the write of one row a change set holds: a create of a key
// with its own and owned fields, an update of a row held to the version
// the host read it at, or a delete held alike.
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

// ChangeSet is everything one command changes. Sub-table rows, outbox
// events and deadlines stay raw until step 3 gives each a table; the
// adapter refuses a change set holding any.
type ChangeSet struct {
	Rows      []RowWrite        `json:"rows"`
	SubRows   []json.RawMessage `json:"subRows"`
	Events    []json.RawMessage `json:"events"`
	Deadlines []json.RawMessage `json:"deadlines"`
	Result    json.RawMessage   `json:"result"`
}
