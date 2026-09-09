// Package jsonwriter is the JSON back end of the schema writer:
// Document -> *.schema.json bytes. It is the inverse of jsonreader.
//
// The on-disk JSON shape mirrors the IR directly, so writing is
// encoding/json marshaling with stable key ordering: struct fields emit in
// declaration order and Go sorts map keys. Comments stay as explicit
// "comment" properties (JSON has no comment syntax); they are IR metadata
// and validate like any other key.
package jsonwriter

import (
	"encoding/json"

	"github.com/parable-work/superschematic/internal/loader/schemafile"
	ir "github.com/parable-work/superschematic/ir"
)

// Write renders a document as JSON. Documents holding exactly one definition
// and no document-level metadata are written in the single-definition file
// form (with the "kind" discriminator Decode strips), so a single-definition
// file converts back to a single-definition file.
func Write(doc *schemafile.Document) ([]byte, error) {
	payload := singleDefPayload(doc)
	if payload == nil {
		payload = doc
	}
	out, err := json.MarshalIndent(payload, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(out, '\n'), nil
}

// singleDefPayload returns the single-definition file payload for a
// one-definition document, or nil when the document form applies. The
// "kind" discriminator field is emitted first via struct embedding.
func singleDefPayload(doc *schemafile.Document) any {
	kind, def, ok := schemafile.SingleDefinition(doc)
	if !ok {
		return nil
	}
	switch kind {
	case schemafile.SingleDefType:
		return def
	case schemafile.SingleDefEnum:
		return struct {
			Kind string `json:"kind"`
			*ir.EnumDef
		}{string(kind), def.(*ir.EnumDef)}
	case schemafile.SingleDefUnion:
		return struct {
			Kind string `json:"kind"`
			*ir.UnionDef
		}{string(kind), def.(*ir.UnionDef)}
	case schemafile.SingleDefScalar:
		return struct {
			Kind string `json:"kind"`
			*ir.ScalarDef
		}{string(kind), def.(*ir.ScalarDef)}
	default:
		return struct {
			Kind string `json:"kind"`
			*ir.OperationSet
		}{string(kind), def.(*ir.OperationSet)}
	}
}
