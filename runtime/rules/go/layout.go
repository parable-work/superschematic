package rules

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
)

// Convert is how the adapter converts a value between the core's JSON and
// its column.
type Convert string

// The conversions a layout column names.
const (
	// ConvertNone binds and reads the value as it is.
	ConvertNone Convert = ""
	// ConvertEpochMillis is a timestamp the core holds as epoch
	// milliseconds: bound and read as a time.Time on Postgres, and as the
	// text Date.prototype.toISOString writes on SQLite.
	ConvertEpochMillis Convert = "epochMillis"
)

// Layout is a database's layout document, as internal/generator/behaviorlayout
// writes it and a generated ORM embeds it: each table that composes
// behaviors, by its type's name.
type Layout struct {
	Types map[string]*Table `json:"types"`

	// checks is the define check of each type, made once per core a
	// command runs on over this layout.
	checks layoutChecks
}

// maxCheckedCores is how many cores' define checks a layout keeps; a check
// by one more forgets them all, to be made again.
const maxCheckedCores = 16

// layoutChecks is a layout's define checks, each by the core that made
// it.
type layoutChecks struct {
	mu   sync.Mutex
	made []checkBy
}

// checkBy is one core's define check: the refusal of each type whose spec
// it refused.
type checkBy struct {
	core     Core
	refusals map[string]*Refusal
}

// sameCore reports whether a and b are one core. A core whose value Go
// cannot compare (a struct holding a slice, say) is no other's, so each
// command's check by it is made again.
func sameCore(a, b Core) (same bool) {
	defer func() {
		if recover() != nil {
			same = false
		}
	}()
	return a == b
}

// Table is the layout of one table that composes behaviors.
type Table struct {
	// Type is the type's name, as the core names it.
	Type string `json:"type"`
	// Table is the table, unquoted.
	Table string `json:"table"`
	// Key is the primary key. The core's key of a row is the key's wire
	// form (a UUID's base62 text, any other key's text), which the adapter
	// converts by its type.
	Key Column `json:"key"`
	// Version is the version column, _version, which @optimistic's trigger
	// bumps on every update.
	Version string `json:"version"`
	// Live is the condition a soft-deletable table's live rows meet, its
	// deletedAt column NULL; nil for a table without deletedAt. The adapter
	// reads and writes a live row alone, so the core refuses a command on a
	// row the repository holds deleted as not_found, and a soft delete
	// another makes under a command moves the row.
	Live *Live `json:"live,omitempty"`
	// Own are the own fields the core reads (a row's own) and a create and
	// an update write, in column order.
	Own []Column `json:"own"`
	// Uses are the field-anchored uses, in the type's list order.
	Uses []Use `json:"uses"`
	// Spec is the type as the core runs it.
	Spec TypeSpec `json:"spec"`
}

// Live is what a live row of a soft-deletable table holds: NULL in its
// deletedAt column.
type Live struct {
	// Column is the deletedAt column, unquoted.
	Column string `json:"column"`
}

// Column is a field of a table and the column that holds it.
type Column struct {
	// Field is the field's name in the IR: the member a generated row holds
	// it under, and the name the core gives an own field.
	Field string `json:"field"`
	// Column is its column, unquoted.
	Column string `json:"column"`
	// Type is its IR type's name: string, number, boolean or a scalar
	// (Temporal.DateTime, Identity.UUID, Generic.JSON).
	Type string `json:"type"`
	// Convert is how the adapter converts it; ConvertNone for none.
	Convert Convert `json:"convert,omitempty"`
}

// OwnedField is a behavior's declared field and the column it takes.
type OwnedField struct {
	// Name is the declared name, as the core names it in a row's owned
	// fields (enteredAt).
	Name string `json:"name"`
	Column
	// Value is true for the use's value field, the anchor field itself.
	Value bool `json:"value,omitempty"`
}

// Use is one field-anchored use of a behavior on a table.
type Use struct {
	// ID is the use's id, its anchor field's name.
	ID       string `json:"id"`
	Behavior string `json:"behavior"`
	// Anchor is the anchor field; "" for a type anchor.
	Anchor     string       `json:"anchor,omitempty"`
	Fields     []OwnedField `json:"fields"`
	Operations []Operation  `json:"operations"`
}

// Operation is an operation a behavior declares on a row.
type Operation struct {
	Name   string `json:"name"`
	Writes bool   `json:"writes"`
}

// ParseLayout reads a layout document, refusing one whose table names no
// table, key column or version column, or whose entry's type is not its
// name.
func ParseLayout(data []byte) (*Layout, error) {
	var layout Layout
	if err := json.Unmarshal(data, &layout); err != nil {
		return nil, fmt.Errorf("rules: the behaviors' layout does not parse: %w", err)
	}
	if layout.Types == nil {
		return nil, fmt.Errorf("rules: the behaviors' layout holds no types member")
	}
	for name, table := range layout.Types {
		switch {
		case table == nil:
			return nil, fmt.Errorf("rules: the behaviors' layout's type %s is null", name)
		case table.Type != name:
			return nil, fmt.Errorf("rules: the behaviors' layout holds type %s under %s", table.Type, name)
		case table.Table == "" || table.Key.Column == "" || table.Version == "":
			return nil, fmt.Errorf("rules: the behaviors' layout's type %s names no table, key column or version column", name)
		}
	}
	return &layout, nil
}

// MustParseLayout is ParseLayout for a generated package's variable; it
// panics on a document that does not parse.
func MustParseLayout(data []byte) *Layout {
	layout, err := ParseLayout(data)
	if err != nil {
		panic(err)
	}
	return layout
}

// table is the table of a type, or the internal refusal naming the types
// the layout holds.
func (l *Layout) table(typeName string) (*Table, error) {
	if table := l.Types[typeName]; table != nil {
		return table, nil
	}
	known := make([]string, 0, len(l.Types))
	for name := range l.Types {
		known = append(known, name)
	}
	sort.Strings(known)
	held := "none"
	if len(known) > 0 {
		held = strings.Join(known, ", ")
	}
	return nil, Internal("the behaviors' layout has no type %s; it holds %s", typeName, held)
}

// refusalOf is the define check's refusal of a type: nil for a type the
// core takes. The check of every type the layout holds is made once per
// core, the first time a command runs on it over the layout, as rules.ts
// keeps it per core and layout; a check that fails with an error is made
// again by the next command.
func (l *Layout) refusalOf(core Core, typeName string) (*Refusal, error) {
	l.checks.mu.Lock()
	defer l.checks.mu.Unlock()
	var refusals map[string]*Refusal
	for _, made := range l.checks.made {
		if sameCore(made.core, core) {
			refusals = made.refusals
			break
		}
	}
	if refusals == nil {
		names := make([]string, 0, len(l.Types))
		for name := range l.Types {
			names = append(names, name)
		}
		sort.Strings(names)
		specs := make([]TypeSpec, len(names))
		for i, name := range names {
			specs[i] = l.Types[name].Spec
		}
		found, err := core.Check(specs)
		if err != nil {
			return nil, err
		}
		refusals = map[string]*Refusal{}
		for i, name := range names {
			if i < len(found) && found[i] != nil {
				refusals[name] = found[i]
			}
		}
		if len(l.checks.made) >= maxCheckedCores {
			l.checks.made = nil
		}
		l.checks.made = append(l.checks.made, checkBy{core: core, refusals: refusals})
	}
	refusal := refusals[typeName]
	if refusal == nil {
		return nil, nil
	}
	// A copy, so no command's caller changes another's refusal.
	copied := *refusal
	return &copied, nil
}
