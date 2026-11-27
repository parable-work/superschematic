package registry

import (
	"bytes"
	"encoding/json"
	"fmt"
)

// CanonicalDeclaration is the canonical copy of a declaration, the bytes
// `superschematic behaviors` writes into the crate that implements it and
// the core embeds: its keys in BehaviorDeclaration's order, each JSON
// Schema's object keys sorted and number literals as written (a signal's
// paramsSchema keeps the order it is written in, which the engine's
// describe returns as it reads it), two-space indents, no HTML escaping and
// a final newline. A declaration derived from
// a behavior's class (D103) is held to its committed copy in this form, so
// the two compare byte for byte whatever the source file's layout.
func CanonicalDeclaration(declaration BehaviorDeclaration) ([]byte, error) {
	var err error
	if declaration.ConfigSchema, err = CanonicalSchema(declaration.ConfigSchema); err != nil {
		return nil, fmt.Errorf("configSchema: %w", err)
	}
	if declaration.CreateParamsSchema, err = CanonicalSchema(declaration.CreateParamsSchema); err != nil {
		return nil, fmt.Errorf("createParamsSchema: %w", err)
	}
	operations := make([]BehaviorOperation, len(declaration.Operations))
	for i, operation := range declaration.Operations {
		if operation.ParamsSchema, err = CanonicalSchema(operation.ParamsSchema); err != nil {
			return nil, fmt.Errorf("operation %s paramsSchema: %w", operation.Name, err)
		}
		if operation.ResultSchema, err = CanonicalSchema(operation.ResultSchema); err != nil {
			return nil, fmt.Errorf("operation %s resultSchema: %w", operation.Name, err)
		}
		operations[i] = operation
	}
	declaration.Operations = operations
	// An event is a behavior's the behaviors' core alone implements
	// (RulesPackage), whose copy no engine reads.
	events := make([]BehaviorEvent, len(declaration.Events))
	for i, event := range declaration.Events {
		if event.PayloadSchema, err = CanonicalSchema(event.PayloadSchema); err != nil {
			return nil, fmt.Errorf("event %s payloadSchema: %w", event.Kind, err)
		}
		events[i] = event
	}
	declaration.Events = events
	if declaration.PreconditionSchema, err = CanonicalSchema(declaration.PreconditionSchema); err != nil {
		return nil, fmt.Errorf("preconditionSchema: %w", err)
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	enc.SetIndent("", "  ")
	if err := enc.Encode(declaration); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// CanonicalSchema re-encodes a JSON Schema with its object keys sorted and
// its number literals kept, without HTML escaping. An absent schema stays
// absent.
func CanonicalSchema(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 {
		return raw, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	var value any
	if err := dec.Decode(&value); err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(value); err != nil {
		return nil, err
	}
	return bytes.TrimRight(buf.Bytes(), "\n"), nil
}
