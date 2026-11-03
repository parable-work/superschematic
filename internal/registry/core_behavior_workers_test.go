package registry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestBehaviorWorkerApply (D103): @behaviorWorker(Table, options?) records
// a worker of the API with the table and no queue, which the loader fills,
// and refuses, in these words, a class with fields or heritage, other
// arguments, a table that is no class, an option it does not take, a
// concurrency, grace or permissions of the wrong form, and a class
// declared twice.
func TestBehaviorWorkerApply(t *testing.T) {
	reg := New(naming.Naming{})
	spec, ok := reg.Decorator("behaviorWorker", TargetType)
	if !ok || !spec.DeclaredIn("@superschematic/api") || !spec.AllowsKind(string(ir.SchemaKindAPI)) || spec.AllowsKind(string(ir.SchemaKindDB)) {
		t.Fatalf("@behaviorWorker = %+v, want an API's class decorator from @superschematic/api", spec)
	}
	apply := func(td *ir.TypeDef, schema *ir.Schema, args ...any) error {
		return spec.Apply(Node{Schema: schema, Type: td}, args, Site{})
	}
	ticket := map[string]any{"class": "Ticket"}

	schema := ir.NewSchema("tickets-api", ir.SchemaKindAPI)
	if err := apply(&ir.TypeDef{Name: "TicketTimers", Comment: "Runs them."}, schema, ticket,
		map[string]any{"concurrency": float64(2), "grace": "30s", "permissions": []any{"tickets.escalate"}}); err != nil {
		t.Fatal(err)
	}
	want := []*ir.Worker{{Name: "TicketTimers", Comment: "Runs them.", Concurrency: 2, Grace: "30s",
		BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Ticket", Permissions: []string{"tickets.escalate"}}}}
	if !reflect.DeepEqual(schema.Workers, want) {
		t.Errorf("Workers = %+v, want %+v", schema.Workers[0], want[0])
	}
	if err := apply(&ir.TypeDef{Name: "TicketTimers"}, schema, ticket); err == nil || err.Error() != "@worker class TicketTimers is declared twice" {
		t.Errorf("twice: %v", err)
	}
	bare := ir.NewSchema("tickets-api", ir.SchemaKindAPI)
	if err := apply(&ir.TypeDef{Name: "TicketTimers"}, bare, ticket); err != nil || bare.Workers[0].Concurrency != 0 || bare.Workers[0].BehaviorWorker.Permissions != nil {
		t.Errorf("no options: %v, %+v", err, bare.Workers)
	}

	for _, tc := range []struct {
		name string
		td   *ir.TypeDef
		args []any
		want string
	}{
		{"fields", &ir.TypeDef{Name: "TicketTimers", Fields: []*ir.FieldDef{{Name: "x"}}}, []any{ticket},
			"@behaviorWorker class TicketTimers has fields; it runs its table's behaviors, so its class holds none"},
		{"heritage", &ir.TypeDef{Name: "TicketTimers", Extends: "Base"}, []any{ticket},
			"@behaviorWorker class TicketTimers extends or implements another class; a worker's class declares the worker and nothing else"},
		{"no arguments", &ir.TypeDef{Name: "TicketTimers"}, nil,
			"@behaviorWorker takes the table and an optional options object: { concurrency, grace, permissions }"},
		{"three arguments", &ir.TypeDef{Name: "TicketTimers"}, []any{ticket, map[string]any{}, map[string]any{}},
			"@behaviorWorker takes the table and an optional options object: { concurrency, grace, permissions }"},
		{"a table that is no class", &ir.TypeDef{Name: "TicketTimers"}, []any{"Ticket"},
			"@behaviorWorker's table is a class of the API's database, imported from it: "},
		{"an unknown option", &ir.TypeDef{Name: "TicketTimers"}, []any{ticket, map[string]any{"queue": "X"}},
			`@behaviorWorker has no option "queue"; it takes concurrency, grace and permissions`},
		{"a concurrency of none", &ir.TypeDef{Name: "TicketTimers"}, []any{ticket, map[string]any{"concurrency": float64(0)}},
			"@behaviorWorker concurrency is 0; it is a whole number, one or more"},
		{"a grace that is no duration", &ir.TypeDef{Name: "TicketTimers"}, []any{ticket, map[string]any{"grace": "soon"}},
			`@behaviorWorker grace is soon; it is a duration such as "8s"`},
		{"permissions that are no list", &ir.TypeDef{Name: "TicketTimers"}, []any{ticket, map[string]any{"permissions": "tickets.escalate"}},
			"@behaviorWorker permissions is a list of permissions, each a non-empty string"},
		{"an empty permission", &ir.TypeDef{Name: "TicketTimers"}, []any{ticket, map[string]any{"permissions": []any{""}}},
			"@behaviorWorker permissions is a list of permissions, each a non-empty string"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := apply(tc.td, ir.NewSchema("tickets-api", ir.SchemaKindAPI), tc.args...)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply = %v, want an error holding %q", err, tc.want)
			}
		})
	}
}

// A behavior worker names a table and no queue until the loader fills it,
// which CheckWorker takes; an ordinary worker still names its queue.
func TestCheckWorkerOfABehaviorWorker(t *testing.T) {
	if err := CheckWorker(&ir.Worker{Name: "TicketTimers", BehaviorWorker: &ir.BehaviorWorkerConfig{Type: "Ticket"}}); err != nil {
		t.Errorf("a behavior worker: %v", err)
	}
	if err := CheckWorker(&ir.Worker{Name: "TicketTimers", BehaviorWorker: &ir.BehaviorWorkerConfig{}}); err == nil || err.Error() != "names no table" {
		t.Errorf("a behavior worker with no table: %v", err)
	}
	if err := CheckWorker(&ir.Worker{Name: "FulfilOrders"}); err == nil || err.Error() != "names no queue" {
		t.Errorf("a worker with no queue: %v", err)
	}
}
