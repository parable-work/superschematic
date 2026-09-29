package tsreader

import (
	"testing"
)

// TestEncryptedFieldArgumentsLoad: an argument declared EncryptedField<T>
// loads with ArgumentDef.Encrypted, whatever wrappers surround it, and its
// type and other flags load as they would without it. Before, the walker
// read the wrapper and dropped it, so an operation outside an Encrypted set
// was plain to every generator.
func TestEncryptedFieldArgumentsLoad(t *testing.T) {
	dir := decoratorTestService(t, "API", map[string]string{"src/a.schema.ts": `import { Nullable, Validate } from "@superschematic/schema";
import { EncryptedField, HttpMethod, rest } from "@superschematic/api";
export abstract class CardInput {
  number: string;
}
export abstract class Receipt {
  ok: boolean;
}
export class CardMutations {
  @rest(HttpMethod.POST, "cards/{id}")
  storeCard(
    id: string,
    number: EncryptedField<string>,
    pin: Nullable<EncryptedField<string>>,
    note: EncryptedField<Nullable<string>>,
    tags: EncryptedField<string[]>,
    code: EncryptedField<Validate<string, { minLength: 4 }>>,
    label: string
  ): Receipt {
    throw new Error("schema declaration only");
  }

  @rest(HttpMethod.PUT, "cards")
  replaceCard(input: EncryptedField<CardInput>): EncryptedField<Receipt> {
    throw new Error("schema declaration only");
  }
}
`})
	schema, _, err := LoadService(dir)
	if err != nil {
		t.Fatalf("LoadService: %v", err)
	}
	type want struct {
		encrypted, required, isArray bool
		minLength                    int
	}
	wants := map[string]want{
		"storeCard.id":      {required: true},
		"storeCard.number":  {encrypted: true, required: true},
		"storeCard.pin":     {encrypted: true},
		"storeCard.note":    {encrypted: true},
		"storeCard.tags":    {encrypted: true, required: true, isArray: true},
		"storeCard.code":    {encrypted: true, required: true, minLength: 4},
		"storeCard.label":   {required: true},
		"replaceCard.input": {encrypted: true, required: true},
	}
	seen := 0
	for _, op := range schema.OperationSets[0].Operations {
		if op.Encrypted != (op.Name == "replaceCard") {
			t.Errorf("%s: operation Encrypted = %t; only an EncryptedField<T> result sets it", op.Name, op.Encrypted)
		}
		for _, arg := range op.Arguments {
			key := op.Name + "." + arg.Name
			w, ok := wants[key]
			if !ok {
				t.Errorf("unexpected argument %s", key)
				continue
			}
			seen++
			minLength := 0
			if arg.ValidateMinLength != nil {
				minLength = *arg.ValidateMinLength
			}
			if arg.Encrypted != w.encrypted || arg.Required != w.required || arg.TypeRef.IsArray != w.isArray || minLength != w.minLength {
				t.Errorf("%s = encrypted %t, required %t, array %t, minLength %d; want %+v",
					key, arg.Encrypted, arg.Required, arg.TypeRef.IsArray, minLength, w)
			}
		}
	}
	if seen != len(wants) {
		t.Errorf("saw %d arguments, want %d", seen, len(wants))
	}
}
