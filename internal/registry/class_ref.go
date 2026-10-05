package registry

import (
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// ClassRef is a class named as a value in a decorator argument, in the shape
// every form gives Apply and the IR stores: {"class": "Accessory"}. See
// ir.ClassRef. An extension that decodes its argument with DecodeArgs gives
// the struct a ClassRef member where the argument takes a class.
type ClassRef = ir.ClassRef

// ClassRefSchema is the JSON Schema of a class reference. A DecoratorSpec's
// Args uses it wherever the argument takes a class, so a string where a
// class belongs fails validation in every form:
//
//	{"type": "object", "properties": {"of": <ClassRefSchema>}, ...}
//
// Whether the schema declares or imports the class is checked after the
// schema is assembled, in every form (docs/extension-model.md section 3.4).
var ClassRefSchema = json.RawMessage(`{"type":"object","required":["class"],"additionalProperties":false,"properties":{"class":{"type":"string","minLength":1}}}`)

// DecodeClassRef decodes a class reference, a whole decorator argument or a
// value inside one, into the name of the class it names: the declared name,
// resolved through any import alias by the TypeScript frontend and written
// as is by the data forms. It is DecodeArgs for one class, and fails on any
// other value.
func DecodeClassRef(v any) (string, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return "", err
	}
	var decoded any
	if err := json.Unmarshal(raw, &decoded); err != nil {
		return "", err
	}
	ref, ok := ir.AsClassRef(decoded)
	if !ok || ref.Class == "" {
		return "", fmt.Errorf("want a class, written {\"class\": name} in the data forms, got %s", raw)
	}
	return ref.Class, nil
}
