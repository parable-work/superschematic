package tsgen

import "testing"

// TestGeneratedGenericJSONAcceptsAnyJSONValue pins the TypeScript side of
// Generic.JSON as any JSON value but null: a field is typed with the
// scalar's alias (GenericJSON, which is superscalar's JSONValue), so a
// number, string or array type-checks where Record<string, any> accepted
// only objects; a required field is missing when it is null or undefined,
// as the schema runtimes decide; a map value may still be null; and
// superscalar's validator rejects what JSON cannot carry.
func TestGeneratedGenericJSONAcceptsAnyJSONValue(t *testing.T) {
	schema := loadScalarService(t, "json-value-fixture", []string{"Generic.JSON"}, "JsonValueFixture", []map[string]any{
		{"name": "payload", "typeRef": map[string]any{"name": "Generic.JSON"}, "required": true},
		{"name": "extra", "typeRef": map[string]any{"name": "Generic.JSON"}},
		{"name": "byName", "typeRef": map[string]any{"name": "Generic.JSON", "isMap": true}, "required": true},
	})
	output, err := Generate(schema, Options{SchemaName: schema.Name})
	if err != nil {
		t.Fatal(err)
	}
	for _, scalar := range output.Scalars {
		if scalar.Name == "Generic.JSON" && scalar.TSType != "JSONValue" {
			t.Fatalf("Generic.JSON TypeScript type = %q, want JSONValue", scalar.TSType)
		}
	}
	if len(schema.Types) == 0 {
		t.Fatal("fixture has no type")
	}
	for _, typ := range schema.Types {
		for _, field := range typ.Fields {
			if field.Name == "byName" && !field.TypeRef.IsMap {
				t.Fatal("byName is not a map field")
			}
		}
	}
	const runtimeTest = `
import type { GenericJSON, JsonValueFixture } from './types';
import { validateGenericJSON, validateGenericJSONRequired } from './validators/scalars/generic_json';
import { validateJsonValueFixture } from './validators/types/jsonvaluefixture';

function assert(condition: unknown, message: string): asserts condition {
  if (!condition) throw new Error(message);
}

const roots: GenericJSON[] = [{ k: 1, none: null }, [1, 'two', null], 'text', '', 42, 1.5, true, false];
for (const payload of roots) {
  const value: JsonValueFixture = { payload, extra: null, byName: { set: payload, empty: null } };
  assert(validateJsonValueFixture(value) === true, 'a JSON root was refused: ' + JSON.stringify(payload));
  assert(validateGenericJSONRequired(payload)[0], 'a required JSON root was refused: ' + JSON.stringify(payload));
}
assert(!validateGenericJSONRequired(undefined)[0], 'a missing required value was accepted');
assert(!validateGenericJSONRequired(null)[0], 'a null required value was accepted');
const nullRoot = validateJsonValueFixture({ payload: null, extra: null, byName: {} });
assert(nullRoot !== true && JSON.stringify(nullRoot).includes('"required"'), 'a null required field was accepted');
assert(validateGenericJSON(undefined)[0], 'an absent optional value was refused');
assert(validateGenericJSON(null)[0], 'a null optional value was refused');
for (const invalid of [Number.NaN, Number.POSITIVE_INFINITY, { k: undefined }, () => 1]) {
  assert(!validateGenericJSON(invalid as unknown as GenericJSON)[0], 'a value JSON cannot carry was accepted');
}
`
	runGeneratedPackageScript(t, output, runtimeTest)
}
