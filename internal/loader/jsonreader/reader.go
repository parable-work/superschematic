// Package jsonreader is the JSON frontend: *.schema.json -> Schema IR.
//
// The on-disk JSON shape mirrors the IR directly, so reading is JSON Schema
// validation followed by a strict decode. The byte-level entrypoint [Read] is
// also the runtime-mode surface: persisted Plot rows and over-the-wire
// schemas validate through the same pipeline as build-time files.
package jsonreader

import (
	"fmt"
	"os"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/registry"
)

// Read validates and decodes a JSON schema payload against the core
// registry. The source argument names the payload origin (a file path or a
// runtime-mode label such as "plot-row") for error messages.
func Read(data []byte, source string) (*schemafile.Document, error) {
	return schemafile.Decode(data, source)
}

// ReadWith is Read against a registry (nil for the core one): its kinds,
// extensions and documents decide what the payload may carry.
func ReadWith(data []byte, source string, reg *registry.Registry) (*schemafile.Document, error) {
	return schemafile.DecodeWith(data, source, reg)
}

// ReadFile reads one *.schema.json file. The source argument is the path
// used in error messages and definition Owner stamps -- conventionally the
// path relative to the service directory.
func ReadFile(path, source string) (*schemafile.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return Read(data, source)
}

// ReadFileWith is ReadFile against a registry.
func ReadFileWith(path, source string, reg *registry.Registry) (*schemafile.Document, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", source, err)
	}
	return ReadWith(data, source, reg)
}
