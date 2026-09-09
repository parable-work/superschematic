package registry

import (
	"embed"
	"strings"
	"testing"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/apigen"
	"github.com/parable-work/superschematic/internal/generator/apigen/sessionauth"
	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

type fakeAuthProvider struct{ name string }

func (p fakeAuthProvider) Name() string { return p.name }
func (fakeAuthProvider) Analyze(_, upstream *ir.Schema) (apigen.AuthModel, error) {
	return apigen.AnalyzeSessionStores(upstream), nil
}
func (fakeAuthProvider) Endpoint(*ir.FieldDef, *ir.OperationSet, *apigen.EndpointInfo) error {
	return nil
}
func (fakeAuthProvider) Templates() embed.FS                                  { return embed.FS{} }
func (fakeAuthProvider) Funcs() template.FuncMap                              { return nil }
func (fakeAuthProvider) Files(*apigen.APIOutput) []codegen.ConditionalFile    { return nil }
func (fakeAuthProvider) OpenAPIParameters(*apigen.APIOutput) []map[string]any { return nil }

func TestNewRegistersOnlyTheSessionAuthProvider(t *testing.T) {
	reg := New(naming.Default())
	p, ok := reg.AuthProvider(sessionauth.Name)
	if !ok || p.Name() != sessionauth.Name {
		t.Fatalf("AuthProvider(%q) = %v, %v", sessionauth.Name, p, ok)
	}
	if got := reg.AuthProviders(); len(got) != 1 || got[0] != sessionauth.Name {
		t.Fatalf("AuthProviders() = %v, want [session]", got)
	}
	n := naming.Default()
	n.AuthProvider = sessionauth.Name
	selected, err := New(n).SelectedAuthProvider()
	if err != nil || selected.Name() != sessionauth.Name {
		t.Fatalf("SelectedAuthProvider() = %v, %v; want session", selected, err)
	}
}

// TestFinalizeRejectsAnUnregisteredAuthProvider: a naming that names a
// provider no extension registered must fail at Finalize with a message
// that names the provider and lists what is registered.
func TestFinalizeRejectsAnUnregisteredAuthProvider(t *testing.T) {
	n := naming.Default()
	n.AuthProvider = "acme"
	reg := New(n)
	err := reg.Finalize()
	if err == nil {
		t.Fatal("Finalize accepted an auth_provider no extension registered")
	}
	for _, want := range []string{`auth_provider "acme"`, "names no registered auth provider", "[session]"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Finalize() error %q does not contain %q", err.Error(), want)
		}
	}
}

func TestRegisterAuthProviderRejectsDuplicatesNilAndUnnamed(t *testing.T) {
	reg := New(naming.Default())
	if err := reg.RegisterAuthProvider(fakeAuthProvider{name: "acme"}); err != nil {
		t.Fatalf("register acme: %v", err)
	}
	err := reg.RegisterAuthProvider(fakeAuthProvider{name: "acme"})
	if err == nil || !strings.Contains(err.Error(), `"acme" is already registered`) {
		t.Fatalf("duplicate register error = %v", err)
	}
	if err := reg.RegisterAuthProvider(nil); err == nil {
		t.Fatal("nil provider must be rejected")
	}
	if err := reg.RegisterAuthProvider(fakeAuthProvider{}); err == nil {
		t.Fatal("unnamed provider must be rejected")
	}
	if got := reg.AuthProviders(); len(got) != 2 || got[0] != "acme" || got[1] != sessionauth.Name {
		t.Fatalf("AuthProviders() = %v, want [acme session]", got)
	}
}

func TestFinalizeRejectsUnregisteredAuthProviderName(t *testing.T) {
	n := naming.Default()
	n.AuthProvider = "missing"
	reg := New(n)
	err := reg.Finalize()
	if err == nil || !strings.Contains(err.Error(), `auth_provider "missing"`) {
		t.Fatalf("Finalize() error = %v, want unregistered auth_provider", err)
	}
	if _, err := reg.SelectedAuthProvider(); err == nil {
		t.Fatal("SelectedAuthProvider must fail for an unregistered name")
	}
}

func TestRegisterAuthProviderAfterFinalizeFails(t *testing.T) {
	reg := New(naming.Default())
	reg.finalized = true
	if err := reg.RegisterAuthProvider(fakeAuthProvider{name: "late"}); err == nil {
		t.Fatal("registration after Finalize must fail")
	}
}
