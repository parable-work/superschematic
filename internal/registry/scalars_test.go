package registry

import (
	"reflect"
	"strings"
	"testing"

	scalars "github.com/parable-work/superscalar/go"
)

func TestScalarsFallsBackToCoreScalars(t *testing.T) {
	reg := New(coreNaming())

	if got, want := reg.Scalars().Names(), CoreScalars().Names(); !reflect.DeepEqual(got, want) {
		t.Fatalf("Scalars() without RegisterScalars = %d names, want the core's %d", len(got), len(want))
	}
	if _, ok := reg.Scalars().Scalar("Identity.UUID"); !ok {
		t.Fatal("core catalog does not resolve Identity.UUID")
	}
}

func TestRegisterScalarsInstallsOneCatalogPerRegistry(t *testing.T) {
	reg := New(coreNaming())
	row := scalars.ScalarMetadataByCanonical["Identity.UUID"]
	first := ScalarCatalogOf(map[string]*scalars.ScalarMetadata{"Identity.UUID": row})

	if err := reg.RegisterScalars("acme", first); err != nil {
		t.Fatalf("RegisterScalars: %v", err)
	}
	if got := reg.Scalars().Names(); !reflect.DeepEqual(got, []string{"Identity.UUID"}) {
		t.Fatalf("Scalars().Names() = %v, want the registered catalog", got)
	}
	if got := reg.Extensions(); !reflect.DeepEqual(got, []string{"acme"}) {
		t.Fatalf("Extensions() = %v, want the catalog owner", got)
	}

	err := reg.RegisterScalars("other", CoreScalars())
	if err == nil || !strings.Contains(err.Error(), "already registered by acme") || !strings.Contains(err.Error(), "other") {
		t.Fatalf("second RegisterScalars: err = %v, want both owners named", err)
	}
	if got := reg.Scalars().Names(); !reflect.DeepEqual(got, []string{"Identity.UUID"}) {
		t.Fatalf("a rejected second catalog replaced the first: %v", got)
	}
}

func TestRegisterScalarsRejectsMissingOwnerAndNilCatalog(t *testing.T) {
	reg := New(coreNaming())
	if err := reg.RegisterScalars("", CoreScalars()); err == nil || !strings.Contains(err.Error(), "no owner") {
		t.Fatalf("empty owner: err = %v", err)
	}
	if err := reg.RegisterScalars("acme", nil); err == nil || !strings.Contains(err.Error(), "nil scalar catalog") {
		t.Fatalf("nil catalog: err = %v", err)
	}
	if reg.scalars != nil {
		t.Fatal("a rejected RegisterScalars left a catalog installed")
	}
}

func TestRegisterScalarsAfterFinalizeIsAnError(t *testing.T) {
	reg := New(coreNaming())
	for _, name := range []string{"types", "sql", "orm", "api", "sdks", "envConfig", "transform", "ontology"} {
		if err := reg.RegisterGenerator(GeneratorSpec{Name: name, Generate: noopGenerate}); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	err := reg.RegisterScalars("acme", CoreScalars())
	if err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Fatalf("RegisterScalars after Finalize: err = %v", err)
	}
}

func TestScalarCatalogOfSkipsNilRows(t *testing.T) {
	catalog := ScalarCatalogOf(map[string]*scalars.ScalarMetadata{
		"Identity.UUID": scalars.ScalarMetadataByCanonical["Identity.UUID"],
		"Acme.Gone":     nil,
	})
	if got := catalog.Names(); !reflect.DeepEqual(got, []string{"Identity.UUID"}) {
		t.Fatalf("Names() = %v, want nil rows dropped", got)
	}
	if _, ok := catalog.Scalar("Acme.Gone"); ok {
		t.Fatal("Scalar() resolved a nil row")
	}
}
