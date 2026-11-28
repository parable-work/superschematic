// Package jsonschemas compiles JSON Schema documents and checks JSON
// values against them (santhosh-tekuri/jsonschema), the one copy the
// registry, the loader and the tasks package share.
package jsonschemas

import (
	"bytes"

	validator "github.com/santhosh-tekuri/jsonschema/v6"
)

// Compiler is a compiler that holds the JSON Schema document schema,
// registered under url, for the caller to compile the document or any of
// its definitions from (url + "#/$defs/<name>").
func Compiler(url string, schema []byte) (*validator.Compiler, error) {
	resource, err := validator.UnmarshalJSON(bytes.NewReader(schema))
	if err != nil {
		return nil, err
	}
	compiler := validator.NewCompiler()
	if err := compiler.AddResource(url, resource); err != nil {
		return nil, err
	}
	return compiler, nil
}

// Compile compiles the JSON Schema document schema, registered under url.
func Compile(url string, schema []byte) (*validator.Schema, error) {
	compiler, err := Compiler(url, schema)
	if err != nil {
		return nil, err
	}
	return compiler.Compile(url)
}

// Validate checks the JSON value data against sch. A value that is not
// JSON fails with the decoder's error.
func Validate(sch *validator.Schema, data []byte) error {
	instance, err := validator.UnmarshalJSON(bytes.NewReader(data))
	if err != nil {
		return err
	}
	return sch.Validate(instance)
}
