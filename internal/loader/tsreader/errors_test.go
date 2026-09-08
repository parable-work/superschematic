package tsreader

import (
	"errors"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"testing"
)

// TestTypeErrorIsSchemaError: a semantic diagnostic in a schema file surfaces
// as a SchemaErrorList with file:line:col locations, and the walk never runs.
func TestTypeErrorIsSchemaError(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-type-error"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	var list SchemaErrorList
	if !errors.As(err, &list) {
		t.Fatalf("expected SchemaErrorList, got %T: %v", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "DoesNotExist") {
		t.Errorf("diagnostic should mention the missing type, got: %s", msg)
	}
	locRe := regexp.MustCompile(`broken\.schema\.ts:\d+:\d+:`)
	if !locRe.MatchString(msg) {
		t.Errorf("diagnostic should carry file:line:col, got: %s", msg)
	}
}

// TestImplementsNonTraitStillFails: implementing a field-bearing class
// directly fails TypeScript's member-redeclaration check (TS2720), and
// naming a non-trait class through the Trait<T> heritage carrier passes the
// compiler but fails the verification pass. Both paths must block the load.
func TestImplementsNonTraitStillFails(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-implements-non-trait"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	var list SchemaErrorList
	if !errors.As(err, &list) {
		t.Fatalf("expected SchemaErrorList, got %T: %v", err, err)
	}
	msg := err.Error()
	if !strings.Contains(msg, "incorrectly implements class 'Plain'") {
		t.Errorf("expected the TS2720 diagnostic for a direct implements, got: %s", msg)
	}
}

// TestTraitCarrierUnwrapsToTrait: `implements Trait<Expirable>` resolves the
// carrier's type argument as the implemented trait and flattens its fields.
func TestTraitCarrierUnwrapsToTrait(t *testing.T) {
	schema, _, err := LoadService(filepath.Join("testdata", "services", "fixture-traits"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	td := schema.Types["Notification"]
	if td == nil {
		t.Fatal("Notification not loaded")
	}
	var names []string
	for _, tr := range td.Implements {
		names = append(names, tr.Name)
	}
	if !slices.Contains(names, "Expirable") {
		t.Errorf("Implements should contain Expirable (unwrapped from Trait<Expirable>), got %v", names)
	}
	if slices.Contains(names, "Trait") {
		t.Errorf("the Trait carrier itself must not appear in Implements: %v", names)
	}
	var inherited []string
	for _, fd := range td.Fields {
		if fd.InheritedFrom == "Expirable" {
			inherited = append(inherited, fd.Name)
		}
	}
	if !slices.Contains(inherited, "expiresAt") {
		t.Errorf("expiresAt should flatten from Expirable, got %v", inherited)
	}
}

// TestImportSitesRecorded: the walk records every package import occurrence
// with its location for the verification pass; the kind/import rules
// themselves fire there, not at walk time.
func TestImportSitesRecorded(t *testing.T) {
	_, in, err := LoadService(filepath.Join("testdata", "services", "broken-illegal-import"))
	if err != nil {
		t.Fatalf("the walk itself should succeed (verification owns the import rules): %v", err)
	}
	found := false
	for _, site := range in.ImportSites {
		if site.Package == "@psgen/db" && strings.HasSuffix(site.File, "illegal.schema.ts") &&
			site.Line == 2 && site.Col == 1 {
			found = true
		}
	}
	if !found {
		t.Errorf("expected an import site for @psgen/db at illegal.schema.ts:2:1, got: %+v", in.ImportSites)
	}
}

// TestMissingService: a directory without a tsconfig is a plain error, not a
// panic.
func TestMissingService(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "does-not-exist"))
	if err == nil {
		t.Fatal("expected an error for a missing service")
	}
}

func TestExternalEnumsRejectBareNameCollisionAcrossDependencies(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-external-enum-collision"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	var list SchemaErrorList
	if !errors.As(err, &list) {
		t.Fatalf("expected SchemaErrorList, got %T: %v", err, err)
	}
	msg := err.Error()
	for _, want := range []string{
		`external enum "TenantStatus"`,
		"@parable-platform/fixture-db",
		"@parable-platform/fixture-enum-guardrails",
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing diagnostic %q in: %s", want, msg)
		}
	}
}

func TestExternalEnumStringLiteralMembersRemainSupported(t *testing.T) {
	_, in, err := LoadService(filepath.Join("testdata", "services", "fixture-external-enum-valid"))
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	enum := in.ExternalEnums["TenantStatus"]
	if enum == nil {
		t.Fatal("TenantStatus external enum was not recorded")
	}
	if enum.Owner != "@parable-platform/fixture-enum-guardrails" {
		t.Fatalf("Owner = %q, want @parable-platform/fixture-enum-guardrails", enum.Owner)
	}
	if len(enum.Values) != 2 || enum.Values[0].Name != "Active" || enum.Values[0].SerializedAs != "active" {
		t.Fatalf("Values = %+v, want the existing string-literal enum mapping", enum.Values)
	}
}

func TestExternalEnumRejectsNonStringLiteralInitializerAtMember(t *testing.T) {
	_, _, err := LoadService(filepath.Join("testdata", "services", "broken-external-enum-initializer"))
	if err == nil {
		t.Fatal("expected schema errors")
	}
	var list SchemaErrorList
	if !errors.As(err, &list) {
		t.Fatalf("expected SchemaErrorList, got %T: %v", err, err)
	}
	msg := err.Error()
	for _, want := range []string{
		"guardrail.schema.ts:7:",
		`external enum "NumericStatus"`,
		`member "Pending" must use a string literal initializer`,
	} {
		if !strings.Contains(msg, want) {
			t.Errorf("missing diagnostic %q in: %s", want, msg)
		}
	}
	if strings.Contains(msg, "unsupported type") {
		t.Errorf("diagnostic should point to the enum member, got: %s", msg)
	}
}
