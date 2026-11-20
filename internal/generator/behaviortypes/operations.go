package behaviortypes

import (
	"fmt"
	"slices"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/behaviorlayout"
	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
)

// TableOperations are the operations of the uses of one table that
// composes behaviors (D103), as a types generator that names its object
// types writes a typed method per operation: each operation's parameters
// and result as a Shape, and the structs and enums they name. The Go ORM
// types them so in ormgen; the Rust repositories from this model.
type TableOperations struct {
	// Type is the table's type name.
	Type string
	// Uses are the table's uses that have operations, in the layout's
	// order.
	Uses []UseOperations
	// Structs and Enums are the types the operations name, in the order
	// they are first named: each use's narrowing's aliases, and each
	// operation's parameters and result and the objects they hold.
	Structs []Struct
	Enums   []Enum
}

// UseOperations are one use's operations.
type UseOperations struct {
	// ID is the use's id, Behavior its registered name and Member the name
	// its operations sit under: its field, or the behavior's storage key
	// for a use the table implements (timers).
	ID       string
	Behavior string
	Member   string
	// Base names the types its operations name: the type's name and the
	// member in PascalCase (OrderStatus, TicketTimers), as the Go ORM
	// names them.
	Base string
	// Anchored is a use on a field, which a doc names by its field.
	Anchored   bool
	Operations []Operation
}

// Operation is one operation of a use: its name, its declaration's
// description, its parameters and its result, and whether it runs on the
// table as a whole (D104), taking no key.
type Operation struct {
	Name   string
	Doc    string
	Params Shape
	Result Shape
	Table  bool
}

// Operations models the operations of each table of the schema that
// composes behaviors (D103), in the order of the types' names; nil when
// none does. A use with no operations (Reactions) is left out, and so is
// a table with no use that has one. reg declares the behaviors.
func Operations(schema *ir.Schema, reg *registry.Registry) ([]TableOperations, error) {
	layout, err := behaviorlayout.Of(schema, reg)
	if err != nil || layout == nil {
		return nil, err
	}
	var out []TableOperations
	for _, name := range SortedKeys(layout.Types) {
		table := layout.Types[name]
		td := schema.Types[name]
		if td == nil {
			continue
		}
		t := Type{Name: name}
		b := typeBuilder{t: &t, aliases: map[string]Alias{}, operations: true}
		ops := TableOperations{Type: name}
		for _, use := range table.Uses {
			if len(use.Operations) == 0 {
				continue
			}
			member := use.Member
			if member == "" {
				member = use.ID
			}
			var ref ir.BehaviorRef
			for _, each := range td.TableBehaviors() {
				if each.UseID() == use.ID {
					ref = each
				}
			}
			declaration, ok := reg.Behavior(use.Behavior)
			if !ok {
				return nil, fmt.Errorf("table %s: use %s: behavior %s is not registered", name, use.ID, use.Behavior)
			}
			base := name + MemberName(member)
			narrowed, err := Narrow(declaration, ref.Config, Target{Schema: schema.Name, Base: base, Document: schema, Instance: td})
			if err != nil {
				return nil, fmt.Errorf("table %s: use %s: behavior %s: %w", name, use.ID, use.Behavior, err)
			}
			// A narrowing's doc names the type it narrows by its base, which
			// for a use is the type's name and the use's member: its docs
			// name the field instead (Order's status), or the table for a
			// use it implements.
			named := name + "'s " + member
			if use.Anchor == "" {
				named = name + "'s"
			}
			for _, alias := range narrowed.Aliases {
				alias.Doc = strings.ReplaceAll(alias.Doc, An(base)+"'s", named)
				b.aliases[alias.Name] = alias
			}
			uses := UseOperations{ID: use.ID, Behavior: use.Behavior, Member: member, Base: base, Anchored: use.Anchor != ""}
			for _, op := range use.Operations {
				var doc string
				for _, declared := range declaration.Operations {
					if declared.Name == op.Name {
						doc = declared.Description
					}
				}
				opName := MemberName(op.Name)
				params, err := b.shape(WithoutDescription(narrowed.Params[op.Name]), base+opName+"Params", fmt.Sprintf("the parameters of %s's %s", named, op.Name))
				if err != nil {
					return nil, fmt.Errorf("table %s: use %s: operation %s's parameters: %w", name, use.ID, op.Name, err)
				}
				result, err := b.shape(WithoutDescription(narrowed.Results[op.Name]), base+opName+"Result", fmt.Sprintf("what %s's %s returns", named, op.Name))
				if err != nil {
					return nil, fmt.Errorf("table %s: use %s: operation %s's result: %w", name, use.ID, op.Name, err)
				}
				uses.Operations = append(uses.Operations, Operation{Name: op.Name, Doc: doc, Params: params, Result: result, Table: op.Table})
			}
			ops.Uses = append(ops.Uses, uses)
		}
		if len(ops.Uses) == 0 {
			continue
		}
		ops.Structs, ops.Enums = t.Structs, t.Enums
		seen := map[string]bool{}
		for _, typeName := range t.names() {
			if seen[typeName] {
				return nil, fmt.Errorf("table %s: the operations of its behaviors would name %s twice", name, typeName)
			}
			seen[typeName] = true
		}
		out = append(out, ops)
	}
	slices.SortFunc(out, func(a, b TableOperations) int { return strings.Compare(a.Type, b.Type) })
	return out, nil
}

// Names are the type names the operations write: their structs' and
// their enums'.
func (t TableOperations) Names() []string {
	return Type{Structs: t.Structs, Enums: t.Enums}.names()
}
