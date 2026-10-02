package tsgen

import (
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/registry"
)

// TestScalarFieldsAreBranded builds a types package whose fields are typed
// by their scalars' symbols, type-checks it and runs a script under bun. A
// plain string does not assign to a branded field and a value superscalar
// parsed does; a scalar @default is cast to its symbol, an empty list
// default is not; a mask writes a required secret's zero value cast to its
// symbol; the validators still take unvalidated input.
func TestScalarFieldsAreBranded(t *testing.T) {
	scalars := []string{"Contact.Email", "Identity.UUID", "Temporal.TimeZone", "Generic.Int64", "Identity.Slug", "Auth.Password", "Temporal.DateTime", "Generic.JSON"}
	schema := loadScalarService(t, "branded-fixture", scalars, "BrandedFixture", []map[string]any{
		{"name": "email", "typeRef": map[string]any{"name": "Contact.Email"}, "required": true},
		{"name": "owner", "typeRef": map[string]any{"name": "Identity.UUID"}},
		{"name": "zone", "typeRef": map[string]any{"name": "Temporal.TimeZone"}, "required": true, "default": "UTC"},
		{"name": "seats", "typeRef": map[string]any{"name": "Generic.Int64"}, "required": true, "default": "5"},
		{"name": "tags", "typeRef": map[string]any{"name": "Identity.Slug", "isArray": true}, "required": true, "default": "[]"},
		{"name": "password", "typeRef": map[string]any{"name": "Auth.Password"}, "required": true, "secret": true},
		{"name": "rotatedAt", "typeRef": map[string]any{"name": "Temporal.DateTime"}, "required": true, "secret": true},
		{"name": "meta", "typeRef": map[string]any{"name": "Generic.JSON"}},
	})
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"email": "ContactEmail", "owner": "IdentityUUID", "zone": "TemporalTimeZone", "seats": "GenericInt64",
		"tags": "IdentitySlug[]", "password": "AuthPassword", "rotatedAt": "TemporalDateTime", "meta": "GenericJSON",
	}
	defaults := map[string]string{"zone": `"UTC" as TemporalTimeZone`, "seats": "5 as GenericInt64", "tags": "[]"}
	for _, typ := range output.Types {
		if typ.Name != "BrandedFixture" {
			continue
		}
		for _, field := range typ.Fields {
			if field.TSType != want[field.Name] {
				t.Errorf("%s: TSType = %q, want %q", field.Name, field.TSType, want[field.Name])
			}
			if field.DefaultLiteral != defaults[field.Name] {
				t.Errorf("%s: DefaultLiteral = %q, want %q", field.Name, field.DefaultLiteral, defaults[field.Name])
			}
		}
	}

	const runtimeTest = `
import type { BrandedFixture } from './types';
import { BrandedFixtureDefaults } from './types';
import { maskSecretsBrandedFixture } from './mask/types/brandedfixture';
import { parseBrandedFixtureJson, validateBrandedFixture } from './validators/types/brandedfixture';
import { validateContactEmailRequired } from './validators/scalars/contact_email';
import { parseAuthPasswordStrict, parseContactEmailStrict, parseIdentityUUID } from 'superscalar/scalars';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

const fixture: BrandedFixture = {
  email: parseContactEmailStrict('ops@example.com'),
  owner: parseIdentityUUID('not a uuid'),
  zone: BrandedFixtureDefaults.zone!,
  seats: 2,
  tags: [],
  password: parseAuthPasswordStrict('correct horse battery staple'),
  rotatedAt: new Date(Date.UTC(2026, 0, 2)),
  meta: { plan: 'gold' },
};
assert(fixture.owner === null, 'a value superscalar refuses parses to null');
assert(validateBrandedFixture(fixture) === true, 'the type validator rejected a parsed value');

// @ts-expect-error a plain string is not a ContactEmail.
const plain: BrandedFixture = { ...fixture, email: 'ops@example.com' };
assert(validateBrandedFixture(plain) === true, 'the validator checks the value, not the type');
// The scalar validators take the base type, so they check unvalidated input.
assert(!validateContactEmailRequired('not an email')[0], 'the scalar validator accepted an invalid email');

assert(BrandedFixtureDefaults.zone === 'UTC' && BrandedFixtureDefaults.seats === 5, 'the defaults hold the literals');
const parsed = parseBrandedFixtureJson(JSON.stringify({ email: 'ops@example.com', password: 'hunter22', rotatedAt: '2026-01-02T00:00:00Z' }));
assert(parsed.zone === 'UTC' && parsed.seats === 5 && Array.isArray(parsed.tags), 'parsing applies the defaults');

const masked = maskSecretsBrandedFixture(fixture);
assert(masked.password === '', 'a masked password is the empty string');
assert(masked.rotatedAt.getTime() === 0, 'a masked date is the epoch');
assert(masked.email === fixture.email, 'a field that is not secret is kept');
`
	runGeneratedPackageScript(t, output, runtimeTest)
}

// TestEveryCatalogScalarCompilesBranded types a required, an optional and a
// list field with every scalar in the linked catalog and type-checks the
// package: every symbol the fields name is exported by the scalar package,
// and the validators, masks and parsers compile against the brands.
func TestEveryCatalogScalarCompilesBranded(t *testing.T) {
	names := registry.CoreScalars().Names()
	fields := make([]map[string]any, 0, 3*len(names))
	for _, name := range names {
		base := strings.ToLower(strings.ReplaceAll(name, ".", "_"))
		fields = append(fields,
			map[string]any{"name": base, "typeRef": map[string]any{"name": name}, "required": true, "secret": true},
			map[string]any{"name": base + "_optional", "typeRef": map[string]any{"name": name}},
			map[string]any{"name": base + "_list", "typeRef": map[string]any{"name": name, "isArray": true}},
		)
	}
	schema := loadScalarService(t, "catalog-branded-fixture", names, "CatalogFixture", fields)
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	runGeneratedPackageScript(t, output, "import type { CatalogFixture } from './types';\nexport type Fixture = CatalogFixture;\n")
}
