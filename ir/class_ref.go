package ir

// ClassRef is a schema class named as a value in a decorator argument. The
// TypeScript form writes the class itself (`@pairsWith({ of: Accessory })`);
// the IR and the data forms hold the object {"class": "Accessory"}, so a
// decorator's argument has one JSON shape in every form.
//
// Class is the class's declared name, unqualified. A class imported from
// another service's package is listed in [Schema.Imports] under that
// package, as an imported field type is, and the reference holds only its
// name.
//
// The shape is reserved: in a decorator's value under an Extensions slot, an
// object whose only key is "class", holding a string, is a class reference
// wherever it appears, and the loader checks that the schema declares or
// imports the class it names.
type ClassRef struct {
	Class string `json:"class" yaml:"class"`
}

// AsClassRef reports whether v, a decoded JSON value, has the shape of a
// class reference: an object whose only key is "class", holding a string.
func AsClassRef(v any) (ClassRef, bool) {
	obj, ok := v.(map[string]any)
	if !ok || len(obj) != 1 {
		return ClassRef{}, false
	}
	name, ok := obj["class"].(string)
	if !ok {
		return ClassRef{}, false
	}
	return ClassRef{Class: name}, true
}
