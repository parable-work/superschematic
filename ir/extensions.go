package ir

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// ExtensionHolder is an IR node that carries an Extensions map: *Schema,
// *TypeDef, *FieldDef and *OperationSet. Operations are FieldDefs, so they
// are covered by FieldDef.
type ExtensionHolder interface {
	extensions() *map[string]json.RawMessage
}

func (s *Schema) extensions() *map[string]json.RawMessage       { return &s.Extensions }
func (t *TypeDef) extensions() *map[string]json.RawMessage      { return &t.Extensions }
func (f *FieldDef) extensions() *map[string]json.RawMessage     { return &f.Extensions }
func (o *OperationSet) extensions() *map[string]json.RawMessage { return &o.Extensions }

// GetExtension decodes h.Extensions[ext] into T. ok is false when the key is
// absent; err is set only for present-but-malformed data.
func GetExtension[T any](h ExtensionHolder, ext string) (v T, ok bool, err error) {
	return getRaw[T](*h.extensions(), ext, "extension")
}

// SetExtension encodes v in canonical form and stores it under ext. A value
// that encodes to `{}` deletes the key instead, so a node with no extension
// data marshals without an "extensions" key.
func SetExtension[T any](h ExtensionHolder, ext string, v T) error {
	raw, err := encodeRaw(v, "extension", ext)
	if err != nil {
		return err
	}
	m := h.extensions()
	if bytes.Equal(raw, emptyObject) {
		delete(*m, ext)
		return nil
	}
	if *m == nil {
		*m = make(map[string]json.RawMessage)
	}
	(*m)[ext] = raw
	return nil
}

// UpdateExtension is the read-modify-write most decorators need: decode
// the current value (zero T when absent), let fn mutate it, store it.
func UpdateExtension[T any](h ExtensionHolder, ext string, fn func(*T)) error {
	v, _, err := GetExtension[T](h, ext)
	if err != nil {
		return err
	}
	fn(&v)
	return SetExtension(h, ext, v)
}

// GetDocument decodes s.Documents[name] into T. ok is false when the key is
// absent; err is set only for present-but-malformed data.
func GetDocument[T any](s *Schema, name string) (v T, ok bool, err error) {
	return getRaw[T](s.Documents, name, "document")
}

// SetDocument encodes v in canonical form and stores it under name. Unlike
// [SetExtension], a value that encodes to `{}` is kept: a document's
// presence is meaningful to the generator that reads it.
func SetDocument[T any](s *Schema, name string, v T) error {
	raw, err := encodeRaw(v, "document", name)
	if err != nil {
		return err
	}
	if s.Documents == nil {
		s.Documents = make(map[string]json.RawMessage)
	}
	s.Documents[name] = raw
	return nil
}

// CanonicalJSON re-encodes raw in the form the IR persists for extension and
// document values: compact, object keys sorted, array order and number
// literals kept as written. Every path that stores a value runs it
// ([SetExtension], [SetDocument], and the data-form readers through
// [CanonicalizeExtensions] and [CanonicalizeDocuments]), so the persisted IR
// does not depend on the authoring format or the codec's field order.
func CanonicalJSON(raw json.RawMessage) (json.RawMessage, error) {
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	if dec.More() {
		return nil, fmt.Errorf("trailing data after JSON value")
	}
	return json.Marshal(value)
}

// CanonicalizeExtensions rewrites every value of an Extensions map with
// [CanonicalJSON] and drops entries that canonicalize to `{}`, the rule
// [SetExtension] applies.
func CanonicalizeExtensions(m map[string]json.RawMessage) error {
	for key, value := range m {
		canonical, err := CanonicalJSON(value)
		if err != nil {
			return fmt.Errorf("extension %q: %w", key, err)
		}
		if bytes.Equal(canonical, emptyObject) {
			delete(m, key)
			continue
		}
		m[key] = canonical
	}
	return nil
}

// CanonicalizeDocuments rewrites every value of a Documents map with
// [CanonicalJSON]. Empty objects stay, as in [SetDocument].
func CanonicalizeDocuments(m map[string]json.RawMessage) error {
	for key, value := range m {
		canonical, err := CanonicalJSON(value)
		if err != nil {
			return fmt.Errorf("document %q: %w", key, err)
		}
		m[key] = canonical
	}
	return nil
}

var emptyObject = []byte("{}")

func getRaw[T any](m map[string]json.RawMessage, key, what string) (v T, ok bool, err error) {
	raw, present := m[key]
	if !present {
		return v, false, nil
	}
	if err := json.Unmarshal(raw, &v); err != nil {
		return v, true, fmt.Errorf("decoding %s %q: %w", what, key, err)
	}
	return v, true, nil
}

func encodeRaw[T any](v T, what, key string) (json.RawMessage, error) {
	raw, err := json.Marshal(v)
	if err != nil {
		return nil, fmt.Errorf("encoding %s %q: %w", what, key, err)
	}
	canonical, err := CanonicalJSON(raw)
	if err != nil {
		return nil, fmt.Errorf("encoding %s %q: %w", what, key, err)
	}
	return canonical, nil
}
