package enginedoc_test

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/enginedoc"
	ir "github.com/parable-work/superschematic/ir"
)

func str(name string) ir.TypeRef { return ir.TypeRef{Name: name} }

func field(name string, ref ir.TypeRef) *ir.FieldDef {
	return &ir.FieldDef{Name: name, TypeRef: ref, Required: true}
}

func table(name string, fields ...*ir.FieldDef) *ir.TypeDef {
	return &ir.TypeDef{Name: name, Role: ir.RoleDBTable, Fields: fields}
}

func valueType(name string, fields ...*ir.FieldDef) *ir.TypeDef {
	td := table(name, fields...)
	td.JsonField = true
	return td
}

func engineType(name string, behaviors []ir.BehaviorRef, fields ...*ir.FieldDef) *ir.TypeDef {
	td := table(name, fields...)
	td.Behaviors = behaviors
	return td
}

var comments = []ir.BehaviorRef{{Name: "Comments"}}

// desk is a DB schema with a table, a value type both hold and two engine
// types, one linking to the other.
func desk() *ir.Schema {
	s := ir.NewSchema("desk-db", ir.SchemaKindDB)
	s.Scalars["Temporal.DateTime"] = &ir.ScalarDef{Name: "Temporal.DateTime", LanguagePrimitive: ir.LanguageString, Primitive: "String",
		TypeMappings: map[string]string{"go": "TemporalDateTime", "sql": "TIMESTAMPTZ"}}
	s.Enums["Priority"] = &ir.EnumDef{Name: "Priority", Owner: "src/desk.schema.json", Values: []ir.EnumValueDef{{Name: "Low"}, {Name: "High"}}}
	s.Types["Customer"] = table("Customer", field("name", str("string")), field("address", str("Address")))
	s.Types["Address"] = valueType("Address", field("city", str("string")))
	s.Types["Account"] = engineType("Account", comments, field("name", str("string")))
	ticket := engineType("Ticket", []ir.BehaviorRef{
		{Name: "Workflow", Config: json.RawMessage(`{"states":["open","done"],"transitions":[{"from":"open","to":"done"}]}`)},
		{Name: "Links", Config: json.RawMessage(`{"links":{"account":{"required":true,"schema":"Account"}}}`)},
		{Name: "Comments"},
	}, field("title", str("string")), field("priority", str("Priority")), field("shipTo", str("Address")), field("dueAt", str("Temporal.DateTime")))
	ticket.Owner = "src/desk.schema.json"
	ticket.Extends = "Stamped"
	ticket.Implements = []ir.TraitRef{{Name: "Audited"}}
	ticket.Fields[0].InheritedFrom = "Stamped"
	s.Types["Ticket"] = ticket
	return s
}

// Problems refuses an engine type that reaches a table or another engine
// type, a table that references an engine type, what only a table has,
// a field the database generates, and a map or a union, each naming the
// way out.
func TestProblems(t *testing.T) {
	for _, tc := range []struct {
		name string
		edit func(*ir.Schema)
		want string
	}{
		{"reaches a table", func(s *ir.Schema) {
			s.Types["Ticket"].Fields = append(s.Types["Ticket"].Fields, field("customer", str("Customer")))
		}, "field Ticket.customer of engine type Ticket is typed as table Customer, but an engine type cannot reach a table: make Customer an engine type too and link to it with the Links behavior (D16), or hold its key in a plain field"},
		{"reaches a table through a value type", func(s *ir.Schema) {
			s.Types["Address"].Fields = append(s.Types["Address"].Fields, field("resident", str("Customer")))
		}, "field Address.resident, which engine type Ticket holds, is typed as table Customer, but an engine type cannot reach a table"},
		{"a relation", func(s *ir.Schema) {
			f := field("owner", str("Identity.UUID"))
			f.Relation = &ir.RelationDef{Type: "Customer"}
			s.Types["Account"].Fields = append(s.Types["Account"].Fields, f)
		}, "field Account.owner of engine type Account is typed as table Customer"},
		{"holds an engine type", func(s *ir.Schema) {
			s.Types["Ticket"].Fields = append(s.Types["Ticket"].Fields, field("account", str("Account")))
		}, "field Ticket.account of engine type Ticket holds engine type Account, whose instances the engine keeps apart: link to it with the Links behavior (D16) instead of a field"},
		{"a table references an engine type", func(s *ir.Schema) {
			s.Types["Customer"].Fields = append(s.Types["Customer"].Fields, field("account", str("Account")))
		}, "field Customer.account of table Customer references engine type Account, whose instances the engine keeps, not a table: hold the instance's id in a plain field, or make Customer an engine type too and link to Account with the Links behavior (D16)"},
		{"a table's relation", func(s *ir.Schema) {
			f := field("tickets", ir.TypeRef{Name: "Ticket", IsArray: true})
			f.HasMany = true
			s.Types["Customer"].Fields = append(s.Types["Customer"].Fields, f)
		}, "field Customer.tickets of table Customer references engine type Ticket"},
		{"a value type references an engine type", func(s *ir.Schema) {
			s.Types["Address"].Fields = append(s.Types["Address"].Fields, field("account", str("Account")))
		}, "field Address.account of type Address references engine type Account"},
		{"a projection reads an engine type", func(s *ir.Schema) {
			s.Types["TicketRow"] = &ir.TypeDef{Name: "TicketRow", Role: ir.RoleProjection, Projection: &ir.ProjectionDef{Source: "Ticket", Joins: []*ir.ProjectionJoin{{Type: "Account"}}}}
		}, "projection TicketRow reads engine type Ticket, which gets no table: the engine keeps its instances (D58)"},
		{"a projection joins an engine type", func(s *ir.Schema) {
			s.Types["TicketRow"] = &ir.TypeDef{Name: "TicketRow", Role: ir.RoleProjection, Projection: &ir.ProjectionDef{Source: "Customer", Joins: []*ir.ProjectionJoin{{Type: "Account"}}}}
		}, "projection TicketRow joins engine type Account"},
		{"versioned", func(s *ir.Schema) { s.Types["Account"].Versioned = true },
			"engine type Account is @versioned, which only a table has: a type that composes a behavior is an engine type, whose instances the engine keeps, and it gets no table (D58)"},
		{"the User trait", func(s *ir.Schema) { s.Types["Account"].User = &ir.UserTrait{Login: "name"} },
			"engine type Account has the User trait, which only a table has"},
		{"a value type", func(s *ir.Schema) { s.Types["Address"].Behaviors = comments },
			"engine type Address is a @jsonField value type, which a table holds as JSON"},
		{"generated by the database", func(s *ir.Schema) {
			f := field("id", str("Identity.UUID"))
			f.AutoGenerated, f.Key = true, true
			s.Types["Account"].Fields = append(s.Types["Account"].Fields, f)
		}, "field Account.id of engine type Account is generated by the database (AutoGenerate), but an engine type gets no table: the engine gives each instance an id of its own and generates no field, so drop it (D58)"},
		{"a map", func(s *ir.Schema) {
			s.Types["Account"].Fields = append(s.Types["Account"].Fields, field("labels", ir.TypeRef{Name: "string", IsMap: true}))
		}, "field Account.labels of engine type Account is a map, which the engine's schema runtime does not validate: hold a list of a value type instead"},
		{"a union", func(s *ir.Schema) {
			s.Unions["Channel"] = &ir.UnionDef{Name: "Channel", Types: []string{"Address"}}
			s.Types["Address"].Fields = append(s.Types["Address"].Fields, field("channel", str("Channel")))
		}, "field Address.channel, which engine type Ticket holds, has the union type Channel, which the engine's schema runtime does not validate"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := desk()
			tc.edit(s)
			problems := enginedoc.Problems(s)
			if !containsProblem(problems, tc.want) {
				t.Fatalf("Problems = %q, want one containing %q", problems, tc.want)
			}
		})
	}
	if problems := enginedoc.Problems(desk()); problems != nil {
		t.Errorf("the desk has problems: %q", problems)
	}
}

func containsProblem(problems []string, want string) bool {
	return slices.ContainsFunc(problems, func(problem string) bool { return strings.Contains(problem, want) })
}
