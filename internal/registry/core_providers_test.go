package registry

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

// TestProvidersDecoratorReadsTheSlots (D159): @providers maps config
// fields to the runtime interfaces their providers implement.
func TestProvidersDecoratorReadsTheSlots(t *testing.T) {
	spec := providersDecorator()
	td := &ir.TypeDef{Name: "Config", EnvVars: true}
	if err := spec.Apply(Node{Type: td}, []any{map[string]any{"SMS": "channel", "VAULT": "credentials"}}, Site{}); err != nil {
		t.Fatal(err)
	}
	if td.ProviderSlots["SMS"] != ir.RuntimeChannel || td.ProviderSlots["VAULT"] != ir.RuntimeCredentials {
		t.Fatalf("ProviderSlots = %v", td.ProviderSlots)
	}
	for name, tc := range map[string]struct {
		args []any
		want string
	}{
		"twice":             {[]any{map[string]any{"SMS": "channel"}}, "a class takes one @providers"},
		"no interface":      {[]any{map[string]any{"SMS": "mail"}}, `slot SMS takes "mail", which is no runtime interface`},
		"a lower-case slot": {[]any{map[string]any{"sms": "channel"}}, `slot "sms" is a config field's name`},
		"no object":         {[]any{"SMS"}, "takes an object of one slot or more"},
		"an empty object":   {[]any{map[string]any{}}, "takes an object of one slot or more"},
	} {
		t.Run(name, func(t *testing.T) {
			target := &ir.TypeDef{Name: "Config"}
			if name == "twice" {
				target.ProviderSlots = map[string]string{"X": ir.RuntimeModel}
			}
			err := spec.Apply(Node{Type: target}, tc.args, Site{})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Apply() = %v, want it to say %q", err, tc.want)
			}
		})
	}
}

// TestRegisterRuntimeFeature (D159): a feature names the interface it
// needs and how to tell an API uses it, and RuntimeFeaturesUsed answers
// the ones an API uses. This is the call sections 3, 4 and 8 make; the
// core registers notifications itself (D153).
func TestRegisterRuntimeFeature(t *testing.T) {
	r := New(naming.Naming{})
	usesNotices := func(in RuntimeFeatureInput) bool {
		for _, db := range in.Databases {
			if db.Types["Notice"] != nil {
				return true
			}
		}
		return false
	}
	if err := r.RegisterRuntimeFeature(RuntimeFeature{Name: "notices", Interface: ir.RuntimeChannel, Uses: usesNotices}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterRuntimeFeature(RuntimeFeature{Name: "previews", Interface: ir.RuntimeFiles, Uses: func(RuntimeFeatureInput) bool { return true }}); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]RuntimeFeature{
		"registered twice": {Name: "notices", Interface: ir.RuntimeChannel, Uses: usesNotices},
		"the core's":       {Name: "notifications", Interface: ir.RuntimeChannel, Uses: usesNotices},
		"no interface":     {Name: "payments", Interface: "payments", Uses: usesNotices},
		"no Uses":          {Name: "attachments", Interface: ir.RuntimeFiles},
		"no name":          {Interface: ir.RuntimeFiles, Uses: usesNotices},
	} {
		if err := r.RegisterRuntimeFeature(f); err == nil {
			t.Errorf("%s: registered", name)
		}
	}
	api := &ir.Schema{Name: "notes-api"}
	db := &ir.Schema{Name: "notes-db", Types: map[string]*ir.TypeDef{"Notice": {Name: "Notice"}}}
	got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{"notes-db": db}})
	if len(got) != 2 || got[0] != (ir.RuntimeFeatureUse{Feature: "notices", Interface: ir.RuntimeChannel}) || got[1] != (ir.RuntimeFeatureUse{Feature: "previews", Interface: ir.RuntimeFiles}) {
		t.Fatalf("RuntimeFeaturesUsed = %v", got)
	}
	if got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api}); len(got) != 1 || got[0].Feature != "previews" {
		t.Fatalf("with no database: %v", got)
	}
}

// TestAgentsNeedASandboxAndAModel (D158): the core registers agents twice,
// once per interface, which an API uses when one of its databases declares
// an @agents class.
func TestAgentsNeedASandboxAndAModel(t *testing.T) {
	r := New(naming.Naming{})
	api := &ir.Schema{Name: "work-api"}
	db := &ir.Schema{Name: "work-db", Types: map[string]*ir.TypeDef{"AgentRun": {Name: "AgentRun", Agents: &ir.AgentsDef{Part: ir.AgentRuns}}}}
	got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{"work-db": db}})
	if len(got) != 2 || got[0] != (ir.RuntimeFeatureUse{Feature: "agents", Interface: ir.RuntimeModel}) || got[1] != (ir.RuntimeFeatureUse{Feature: "agents", Interface: ir.RuntimeSandbox}) {
		t.Fatalf("RuntimeFeaturesUsed = %v", got)
	}
	plain := &ir.Schema{Name: "work-db", Types: map[string]*ir.TypeDef{"Task": {Name: "Task"}}}
	if got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{"work-db": plain}}); len(got) != 0 {
		t.Errorf("a database with no agents uses %v", got)
	}
}

// TestAutonomyDecorator (D158): @autonomy reads a class and a mode, and
// refuses an operation that moves money with any mode but ask.
func TestAutonomyDecorator(t *testing.T) {
	for name, tc := range map[string]struct {
		args []any
		want *ir.OperationAutonomy
		err  string
	}{
		"a class":                    {args: []any{map[string]any{"class": "read"}}, want: &ir.OperationAutonomy{Class: "read"}},
		"money that asks":            {args: []any{map[string]any{"class": "moneyMovement", "mode": "ask"}}, want: &ir.OperationAutonomy{Class: "moneyMovement", Mode: "ask"}},
		"money with no mode":         {args: []any{map[string]any{"class": "moneyMovement"}}, want: &ir.OperationAutonomy{Class: "moneyMovement"}},
		"money that runs on its own": {args: []any{map[string]any{"class": "moneyMovement", "mode": "auto"}}, err: "an agent may only ask to move money"},
		"money that tells":           {args: []any{map[string]any{"class": "moneyMovement", "mode": "tell"}}, err: "an agent may only ask to move money"},
		"no such class":              {args: []any{map[string]any{"class": "spend"}}, err: `class "spend" is none of`},
		"no class":                   {args: []any{map[string]any{"mode": "ask"}}, err: "needs a class"},
		"an unknown option":          {args: []any{map[string]any{"class": "read", "why": "x"}}, err: `no option "why"`},
	} {
		t.Run(name, func(t *testing.T) {
			got, err := autonomyOf("payInvoice", tc.args)
			if tc.err != "" {
				if err == nil || !strings.Contains(err.Error(), tc.err) {
					t.Fatalf("autonomyOf = %v, %v; want an error saying %q", got, err, tc.err)
				}
				return
			}
			if err != nil || *got != *tc.want {
				t.Fatalf("autonomyOf = %+v, %v; want %+v", got, err, tc.want)
			}
		})
	}
}

// TestNotificationsNeedAChannel (D153): the core registers notifications,
// which an API uses when one of its databases declares an @event, a
// @notifier class or a @channelIdentities class.
func TestNotificationsNeedAChannel(t *testing.T) {
	r := New(naming.Naming{})
	api := &ir.Schema{Name: "tickets-api"}
	for name, db := range map[string]*ir.TypeDef{
		"an event":           {Name: "Ticket", Events: []*ir.EventDef{{Kind: "ticket.assigned"}}},
		"an inbox":           {Name: "Notice", Notifier: &ir.NotifierDef{Part: ir.NotifierInbox}},
		"channel identities": {Name: "ChannelIdentity", ChannelIdentities: &ir.ChannelIdentitiesDef{}},
	} {
		schema := &ir.Schema{Name: "tickets-db", Types: map[string]*ir.TypeDef{db.Name: db}}
		got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{"tickets-db": schema}})
		if len(got) != 1 || got[0] != (ir.RuntimeFeatureUse{Feature: "notifications", Interface: ir.RuntimeChannel}) {
			t.Errorf("%s: RuntimeFeaturesUsed = %v", name, got)
		}
	}
	plain := &ir.Schema{Name: "tickets-db", Types: map[string]*ir.TypeDef{"Ticket": {Name: "Ticket"}}}
	if got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api, Databases: map[string]*ir.Schema{"tickets-db": plain}}); len(got) != 0 {
		t.Errorf("a database that raises no event uses %v", got)
	}
}

// TestEventDecorator (D153): @event reads its kind, payload, title,
// urgency and description, and refuses an unknown option or no kind.
func TestEventDecorator(t *testing.T) {
	r := New(naming.Naming{})
	spec, ok := r.Decorator("event", TargetType)
	if !ok {
		t.Fatal("no @event")
	}
	td := &ir.TypeDef{Name: "Ticket"}
	args := []any{map[string]any{"kind": "ticket.assigned", "payload": []any{"title", "priority"}, "title": "title", "urgency": "urgent"}}
	if err := spec.Apply(Node{Type: td}, args, Site{}); err != nil {
		t.Fatal(err)
	}
	if err := spec.Apply(Node{Type: td}, []any{map[string]any{"kind": "ticket.closed"}}, Site{}); err != nil {
		t.Fatal(err)
	}
	if len(td.Events) != 2 || td.Events[0].Kind != "ticket.assigned" || len(td.Events[0].Payload) != 2 || td.Events[0].Urgency != "urgent" || td.Events[1].Kind != "ticket.closed" {
		t.Fatalf("Events = %+v", td.Events)
	}
	for name, a := range map[string][]any{
		"no kind":        {map[string]any{"urgency": "urgent"}},
		"unknown option": {map[string]any{"kind": "a.b", "channels": "sms"}},
		"payload text":   {map[string]any{"kind": "a.b", "payload": "title"}},
		"no options":     {},
	} {
		if err := spec.Apply(Node{Type: &ir.TypeDef{Name: "T"}}, a, Site{}); err == nil {
			t.Errorf("%s: applied", name)
		}
	}
	notifier, _ := r.Decorator("notifier", TargetType)
	if err := notifier.Apply(Node{Type: &ir.TypeDef{Name: "N"}}, []any{"outbox"}, Site{}); err == nil {
		t.Error("@notifier took a part it does not have")
	}
}
