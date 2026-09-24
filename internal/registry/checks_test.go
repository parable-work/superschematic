package registry

import (
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func noopCheck(*ir.Schema, VerifyReporter) {}

func noopEdit(*ir.Schema, map[string]any) error { return nil }

func finalizeWithCoreGenerators(t *testing.T, reg *Registry) {
	t.Helper()
	for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig"} {
		if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Finalize(); err != nil {
		t.Fatal(err)
	}
}

// TestChecksFilterByKindInRegistrationOrder: a check without Kinds runs on
// every kind, core ones included; a check with Kinds runs only on those.
func TestChecksFilterByKindInRegistrationOrder(t *testing.T) {
	reg := New(naming.Default())
	for _, spec := range []CheckSpec{
		{Name: "everywhere", Extension: "policy", Verify: noopCheck},
		{Name: "apiOnly", Extension: "policy", Kinds: []string{string(ir.SchemaKindAPI)}, Verify: noopCheck},
		{Name: "alsoEverywhere", Verify: noopCheck},
	} {
		if err := reg.RegisterCheck(spec); err != nil {
			t.Fatal(err)
		}
	}
	names := func(kind string) string {
		var out []string
		for _, spec := range reg.Checks(kind) {
			out = append(out, spec.Name)
		}
		return strings.Join(out, ",")
	}
	if got := names("API"); got != "everywhere,apiOnly,alsoEverywhere" {
		t.Errorf("Checks(API) = %s", got)
	}
	if got := names("DB"); got != "everywhere,alsoEverywhere" {
		t.Errorf("Checks(DB) = %s", got)
	}
	if got := strings.Join(reg.Extensions(), ","); got != "policy" {
		t.Errorf("Extensions() = %s, want the check's extension noted", got)
	}
}

func TestRegisterCheckRejectsInvalidAndLateRegistrations(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterCheck(CheckSpec{Verify: noopCheck}); err == nil {
		t.Error("nameless check: want error")
	}
	if err := reg.RegisterCheck(CheckSpec{Name: "audience"}); err == nil {
		t.Error("check without Verify: want error")
	}
	if err := reg.RegisterCheck(CheckSpec{Name: "audience", Verify: noopCheck}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterCheck(CheckSpec{Name: "audience", Verify: noopCheck}); err == nil {
		t.Error("duplicate check: want error")
	}
	finalizeWithCoreGenerators(t, reg)
	err := reg.RegisterCheck(CheckSpec{Name: "late", Verify: noopCheck})
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration: got %v, want an after-Finalize error", err)
	}
	if got := reg.Checks("API"); len(got) != 1 {
		t.Fatalf("Checks after a refused registration = %+v, want the one accepted check", got)
	}
}

func TestRegisterOpenAPIHookKeepsOrderAndRejectsInvalid(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterOpenAPIHook(OpenAPIHook{Edit: noopEdit}); err == nil {
		t.Error("nameless hook: want error")
	}
	if err := reg.RegisterOpenAPIHook(OpenAPIHook{Name: "keys"}); err == nil {
		t.Error("hook without Edit: want error")
	}
	for _, name := range []string{"second", "first"} {
		if err := reg.RegisterOpenAPIHook(OpenAPIHook{Name: name, Extension: "policy", Edit: noopEdit}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.RegisterOpenAPIHook(OpenAPIHook{Name: "first", Edit: noopEdit}); err == nil {
		t.Error("duplicate hook: want error")
	}
	hooks := reg.OpenAPIHooks()
	if len(hooks) != 2 || hooks[0].Name != "second" || hooks[1].Name != "first" {
		t.Fatalf("OpenAPIHooks() = %+v", hooks)
	}
	finalizeWithCoreGenerators(t, reg)
	err := reg.RegisterOpenAPIHook(OpenAPIHook{Name: "late", Edit: noopEdit})
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration: got %v, want an after-Finalize error", err)
	}
}

func noopToolEdit(*ir.Schema, *apigen.ToolSet) error { return nil }

func TestRegisterToolHookKeepsOrderAndRejectsInvalid(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterToolHook(ToolHook{Edit: noopToolEdit}); err == nil {
		t.Error("nameless hook: want error")
	}
	if err := reg.RegisterToolHook(ToolHook{Name: "keys"}); err == nil {
		t.Error("hook without Edit: want error")
	}
	for _, name := range []string{"second", "first"} {
		if err := reg.RegisterToolHook(ToolHook{Name: name, Extension: "policy", Edit: noopToolEdit}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.RegisterToolHook(ToolHook{Name: "first", Edit: noopToolEdit}); err == nil {
		t.Error("duplicate hook: want error")
	}
	hooks := reg.ToolHooks()
	if len(hooks) != 2 || hooks[0].Name != "second" || hooks[1].Name != "first" {
		t.Fatalf("ToolHooks() = %+v", hooks)
	}
	if !slices.Contains(reg.Extensions(), "policy") {
		t.Fatalf("Extensions() = %v, want the hook's extension", reg.Extensions())
	}
	finalizeWithCoreGenerators(t, reg)
	err := reg.RegisterToolHook(ToolHook{Name: "late", Edit: noopToolEdit})
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("post-Finalize registration: got %v, want an after-Finalize error", err)
	}
}
