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
// the ones an API uses. This is the call sections 3, 4 and 8 make.
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
	if err := r.RegisterRuntimeFeature(RuntimeFeature{Name: "notifications", Interface: ir.RuntimeChannel, Uses: usesNotices}); err != nil {
		t.Fatal(err)
	}
	if err := r.RegisterRuntimeFeature(RuntimeFeature{Name: "agents", Interface: ir.RuntimeSandbox, Uses: func(RuntimeFeatureInput) bool { return true }}); err != nil {
		t.Fatal(err)
	}
	for name, f := range map[string]RuntimeFeature{
		"registered twice": {Name: "notifications", Interface: ir.RuntimeChannel, Uses: usesNotices},
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
	if len(got) != 2 || got[0] != (ir.RuntimeFeatureUse{Feature: "agents", Interface: ir.RuntimeSandbox}) || got[1] != (ir.RuntimeFeatureUse{Feature: "notifications", Interface: ir.RuntimeChannel}) {
		t.Fatalf("RuntimeFeaturesUsed = %v", got)
	}
	if got := r.RuntimeFeaturesUsed(RuntimeFeatureInput{API: api}); len(got) != 1 || got[0].Feature != "agents" {
		t.Fatalf("with no database: %v", got)
	}
}
