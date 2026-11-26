package tsreader

import (
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
)

// idempotentSource is an API with one operation, placeOrder, a POST, under
// decorator.
func idempotentSource(decorator string) string {
	return `import { HttpMethod, auth, idempotent, rest } from "@superschematic/api";

export class OrderMutations {
  @auth
  ` + decorator + `
  @rest(HttpMethod.POST, "orders")
  placeOrder(sku: string): string {
    throw new Error("schema declaration only");
  }
}
`
}

func loadIdempotent(t *testing.T, decorator string) (*ir.IdempotentConfig, error) {
	t.Helper()
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": idempotentSource(decorator)})
	schema, _, _, err := LoadServiceWithConfig(dir)
	if err != nil {
		return nil, err
	}
	return operationNamed(t, schema, "placeOrder").Idempotent, nil
}

// TestIdempotentLoads (D140): @idempotent is called, with no argument or
// a config of ttl, a duration, and required; an operation without it is
// not idempotent.
func TestIdempotentLoads(t *testing.T) {
	for decorator, want := range map[string]*ir.IdempotentConfig{
		"":                                 nil,
		"@idempotent()":                    {},
		"@idempotent({})":                  {},
		`@idempotent({ ttl: "1h" })`:       {TTLSeconds: 3600},
		"@idempotent({ required: false })": {},
		`@idempotent({ ttl: "2160h" })`:    {TTLSeconds: ir.MaxIdempotencyTTLSeconds},
		`@idempotent({ ttl: "90s", required: true })`: {TTLSeconds: 90, Required: true},
	} {
		got, err := loadIdempotent(t, decorator)
		if err != nil {
			t.Errorf("%s: %v", decorator, err)
			continue
		}
		if (got == nil) != (want == nil) || (got != nil && *got != *want) {
			t.Errorf("%s loads as %+v, want %+v", decorator, got, want)
		}
	}
	if (&ir.IdempotentConfig{}).TTL() != ir.DefaultIdempotencyTTLSeconds || ir.DefaultIdempotencyTTLSeconds != 24*60*60 {
		t.Errorf("an @idempotent with no ttl keeps its keys %d seconds, want a day", (&ir.IdempotentConfig{}).TTL())
	}
}

// TestIdempotentConfigIsChecked: a ttl the type checker lets through, a
// string, is read as a duration of at most 90 days; the checker refuses a
// config of another shape before the decorator reads it
// (internal/registry, TestIdempotentConfig, holds the decorator's own
// refusals).
func TestIdempotentConfigIsChecked(t *testing.T) {
	for decorator, want := range map[string]string{
		`@idempotent({ ttl: "soon" })`:     `@idempotent ttl "soon" is no duration`,
		`@idempotent({ ttl: "0s" })`:       `@idempotent ttl "0s" is not positive`,
		`@idempotent({ ttl: "2161h" })`:    `@idempotent ttl "2161h" is longer than 90 days`,
		`@idempotent({ required: "yes" })`: "is not assignable to type 'boolean",
		`@idempotent({ window: "1h" })`:    "'window' does not exist in type 'IdempotentConfig'",
		"@idempotent":                      "Did you mean to call it first and write '@idempotent()'?",
	} {
		_, err := loadIdempotent(t, decorator)
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("%s: %v, want an error holding %q", decorator, err, want)
		}
	}
}

// TestIdempotencyKeysTraitLoads (D140): a DB table that implements
// IdempotencyKeys is the table of the keys, which the loader expands; the
// trait appears once, marks a DB table of a DB schema alone, and is not
// the user model's table.
func TestIdempotencyKeysTraitLoads(t *testing.T) {
	const imports = `import { IdempotencyKeys, User, type UserConfig } from "@superschematic/db";
`
	dir := decoratorTestService(t, "DB", map[string]string{"src/a.schema.ts": imports + `
/** The keys of the @idempotent operations over this database. */
export abstract class IdempotencyKey implements IdempotencyKeys {}

export abstract class Order {
  sku!: string;
}
`})
	schema, _, _, err := LoadServiceWithConfig(dir)
	if err != nil {
		t.Fatal(err)
	}
	keys := schema.Types["IdempotencyKey"]
	if keys == nil || keys.IdempotencyKeys == nil || keys.Role != ir.RoleDBTable {
		t.Fatalf("IdempotencyKey = %+v, want a DB table with the trait", keys)
	}
	if schema.IdempotencyKeysTable() != keys || schema.Types["Order"].IdempotencyKeys != nil {
		t.Errorf("the schema's table of keys is %+v", schema.IdempotencyKeysTable())
	}

	for name, c := range map[string]struct{ kind, source, want string }{
		"twice":     {"DB", `export abstract class IdempotencyKey implements IdempotencyKeys, IdempotencyKeys {}`, "the IdempotencyKeys trait appears more than once"},
		"in an API": {"API", `export abstract class IdempotencyKey implements IdempotencyKeys {}`, "this service is kind API"},
		"with the user model": {"DB", `export abstract class Account implements User<{ login: "email" }>, IdempotencyKeys {
  email!: string;
}`, "a table takes one of the User, UserRole and IdempotencyKeys traits, not User and IdempotencyKeys"},
	} {
		dir := decoratorTestService(t, c.kind, map[string]string{"src/a.schema.ts": imports + c.source + "\n"})
		if _, _, _, err := LoadServiceWithConfig(dir); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: %v, want an error holding %q", name, err, c.want)
		}
	}
}
