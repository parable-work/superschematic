package runtime

import (
	"github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/runtime/schema/go/mask"
	"github.com/parable-work/superschematic/runtime/schema/go/merge"
	"github.com/parable-work/superschematic/runtime/schema/go/parse"
	"github.com/parable-work/superschematic/runtime/schema/go/serialize"
	"github.com/parable-work/superschematic/runtime/schema/go/validate"
)

// Runtime is the composed entry point for schema-runtime. It caches
// per-package state (parsers, validators, serializers) so that a service
// can construct one Runtime per schema at startup and reuse it for every
// request. Package-level helpers (LoadType, ParseType, ...) construct an
// ephemeral Runtime with sensible defaults.
type Runtime struct {
	schema         *ir.Schema
	parseReg       *parse.ParseRegistry
	normReg        *parse.NormalizeRegistry
	validReg       *validate.Registry
	strict         bool
	strictRegistry bool
	maskSecrets    bool

	parser     *parse.Parser
	validator  *validate.Validator
	masker     *mask.Masker
	merger     *merge.Merger
	serializer *serialize.Serializer

	parserStrict     *parse.Parser
	serializerStrict *serialize.Serializer
}

// New creates a Runtime for the given schema. Scalar registries come from
// [WithRegistry] or the per-registry options; any part left unset falls
// back to [Default]. Serialize runs without secret masking. Override via
// the Option set.
func New(schema *ir.Schema, opts ...Option) *Runtime {
	rt := &Runtime{schema: schema}
	for _, opt := range opts {
		opt(rt)
	}
	if rt.parseReg == nil || rt.normReg == nil || rt.validReg == nil {
		fallback := Default()
		if rt.parseReg == nil {
			rt.parseReg = fallback.Parse
		}
		if rt.normReg == nil {
			rt.normReg = fallback.Normalize
		}
		if rt.validReg == nil {
			rt.validReg = fallback.Validate
		}
	}

	parseOpts := []parse.Option{
		parse.WithParseRegistry(rt.parseReg),
		parse.WithNormalizeRegistry(rt.normReg),
	}
	rt.parser = parse.New(schema, parseOpts...)
	rt.parserStrict = parse.New(schema, append(parseOpts, parse.WithStrict(true))...)

	validateOpts := []validate.Option{
		validate.WithRegistry(rt.validReg),
	}
	if rt.strictRegistry {
		validateOpts = append(validateOpts, validate.WithStrictRegistry(true))
	}
	rt.validator = validate.New(schema, validateOpts...)

	rt.masker = mask.New(schema)
	rt.merger = merge.New(schema)

	rt.serializer = serialize.New(schema, serialize.WithMaskSecrets(rt.maskSecrets))
	rt.serializerStrict = serialize.New(schema,
		serialize.WithMaskSecrets(rt.maskSecrets),
		serialize.WithStrict(true),
	)

	return rt
}

// LoadType parses JSON bytes for a named object type and runs schema
// validation. Errors from both phases are merged into one ValidationErrors.
// Equivalent to the generated MyType.FromJSON + MyType.Validate() pair.
func (r *Runtime) LoadType(typeName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	p := r.parserFor(r.strict)
	data, parseErrs := p.ParseTypeJSON(typeName, jsonBytes)
	if parseErrs.HasErrors() {
		return data, parseErrs
	}
	valErrs := r.validator.ValidateType(typeName, data)
	if valErrs.HasErrors() {
		mergeErrors(parseErrs, valErrs)
		return data, parseErrs
	}
	return data, parseErrs
}

// LoadInput is LoadType for input definitions.
func (r *Runtime) LoadInput(inputName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	p := r.parserFor(r.strict)
	data, parseErrs := p.ParseInputJSON(inputName, jsonBytes)
	if parseErrs.HasErrors() {
		return data, parseErrs
	}
	valErrs := r.validator.ValidateInput(inputName, data)
	if valErrs.HasErrors() {
		mergeErrors(parseErrs, valErrs)
		return data, parseErrs
	}
	return data, parseErrs
}

// LoadTypeStrict is LoadType with strict mode (unknown fields rejected, no
// lenient coercion).
func (r *Runtime) LoadTypeStrict(typeName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	data, parseErrs := r.parserStrict.ParseTypeJSON(typeName, jsonBytes)
	if parseErrs.HasErrors() {
		return data, parseErrs
	}
	valErrs := r.validator.ValidateType(typeName, data)
	if valErrs.HasErrors() {
		mergeErrors(parseErrs, valErrs)
		return data, parseErrs
	}
	return data, parseErrs
}

// LoadInputStrict is LoadInput with strict mode.
func (r *Runtime) LoadInputStrict(inputName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	data, parseErrs := r.parserStrict.ParseInputJSON(inputName, jsonBytes)
	if parseErrs.HasErrors() {
		return data, parseErrs
	}
	valErrs := r.validator.ValidateInput(inputName, data)
	if valErrs.HasErrors() {
		mergeErrors(parseErrs, valErrs)
		return data, parseErrs
	}
	return data, parseErrs
}

// LoadTypeYAML is LoadType but the input is YAML bytes.
func (r *Runtime) LoadTypeYAML(typeName string, yamlBytes []byte) (map[string]any, ValidationErrors) {
	p := r.parserFor(r.strict)
	data, parseErrs := p.ParseTypeYAML(typeName, yamlBytes)
	if parseErrs.HasErrors() {
		return data, parseErrs
	}
	valErrs := r.validator.ValidateType(typeName, data)
	if valErrs.HasErrors() {
		mergeErrors(parseErrs, valErrs)
		return data, parseErrs
	}
	return data, parseErrs
}

// LoadInputYAML is LoadInput but the input is YAML bytes.
func (r *Runtime) LoadInputYAML(inputName string, yamlBytes []byte) (map[string]any, ValidationErrors) {
	p := r.parserFor(r.strict)
	data, parseErrs := p.ParseInputYAML(inputName, yamlBytes)
	if parseErrs.HasErrors() {
		return data, parseErrs
	}
	valErrs := r.validator.ValidateInput(inputName, data)
	if valErrs.HasErrors() {
		mergeErrors(parseErrs, valErrs)
		return data, parseErrs
	}
	return data, parseErrs
}

// ParseType parses JSON bytes for a named object type WITHOUT running
// validation. Use this when staging data through multiple steps before
// finalizing (e.g. merge-then-validate).
func (r *Runtime) ParseType(typeName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	return r.parserFor(r.strict).ParseTypeJSON(typeName, jsonBytes)
}

// ParseInput parses JSON bytes for a named input definition without
// running validation.
func (r *Runtime) ParseInput(inputName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	return r.parserFor(r.strict).ParseInputJSON(inputName, jsonBytes)
}

// ParseTypeMap runs the parse phase on an already-decoded map.
func (r *Runtime) ParseTypeMap(typeName string, raw map[string]any) (map[string]any, ValidationErrors) {
	return r.parserFor(r.strict).ParseType(typeName, raw)
}

// ParseInputMap runs the parse phase on an already-decoded map.
func (r *Runtime) ParseInputMap(inputName string, raw map[string]any) (map[string]any, ValidationErrors) {
	return r.parserFor(r.strict).ParseInput(inputName, raw)
}

// ValidateType runs the validate phase on an already-parsed map.
func (r *Runtime) ValidateType(typeName string, data map[string]any) ValidationErrors {
	return r.validator.ValidateType(typeName, data)
}

// ValidateInput runs the validate phase on an already-parsed map.
func (r *Runtime) ValidateInput(inputName string, data map[string]any) ValidationErrors {
	return r.validator.ValidateInput(inputName, data)
}

// MarshalType serializes a map for the named object type to JSON bytes.
func (r *Runtime) MarshalType(typeName string, data map[string]any) ([]byte, ValidationErrors) {
	return r.serializerFor(r.strict).TypeToJSON(typeName, data)
}

// MarshalInput serializes a map for the named input definition to JSON bytes.
func (r *Runtime) MarshalInput(inputName string, data map[string]any) ([]byte, ValidationErrors) {
	return r.serializerFor(r.strict).InputToJSON(inputName, data)
}

// TypeToMap returns a sanitized map for the named object type (drops
// unknown fields, normalizes nil slices, optionally masks secrets).
func (r *Runtime) TypeToMap(typeName string, data map[string]any) (map[string]any, ValidationErrors) {
	return r.serializerFor(r.strict).TypeToMap(typeName, data)
}

// InputToMap returns a sanitized map for the named input definition.
func (r *Runtime) InputToMap(inputName string, data map[string]any) (map[string]any, ValidationErrors) {
	return r.serializerFor(r.strict).InputToMap(inputName, data)
}

// MaskType returns data with @secret fields zeroed for the named object
// type. Convenience wrapper around the mask package.
func (r *Runtime) MaskType(typeName string, data map[string]any) map[string]any {
	return r.masker.MaskType(typeName, data)
}

// MaskInput returns data with @secret fields zeroed for the named input.
func (r *Runtime) MaskInput(inputName string, data map[string]any) map[string]any {
	return r.masker.MaskInput(inputName, data)
}

// MergeType merges newData into existingData honouring the named object
// type's schema. Secret fields preserve their existing value when newData
// has a zero/empty value at that key.
func (r *Runtime) MergeType(typeName string, newData, existingData map[string]any) map[string]any {
	return r.merger.MergeType(typeName, newData, existingData)
}

// MergeInput merges newData into existingData honouring the named input
// definition's schema.
func (r *Runtime) MergeInput(inputName string, newData, existingData map[string]any) map[string]any {
	return r.merger.MergeInput(inputName, newData, existingData)
}

func (r *Runtime) parserFor(strict bool) *parse.Parser {
	if strict {
		return r.parserStrict
	}
	return r.parser
}

func (r *Runtime) serializerFor(strict bool) *serialize.Serializer {
	if strict {
		return r.serializerStrict
	}
	return r.serializer
}

// Package-level convenience functions for callers that do not need to hold
// a Runtime. Each call constructs an ephemeral Runtime with defaults; for
// services issuing many calls, prefer [New] to reuse state.

// LoadType is the package-level shortcut for [Runtime.LoadType].
func LoadType(schema *ir.Schema, typeName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	return New(schema).LoadType(typeName, jsonBytes)
}

// LoadInput is the package-level shortcut for [Runtime.LoadInput].
func LoadInput(schema *ir.Schema, inputName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	return New(schema).LoadInput(inputName, jsonBytes)
}

// LoadTypeStrict is the package-level shortcut for [Runtime.LoadTypeStrict].
func LoadTypeStrict(schema *ir.Schema, typeName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	return New(schema).LoadTypeStrict(typeName, jsonBytes)
}

// LoadInputStrict is the package-level shortcut for [Runtime.LoadInputStrict].
func LoadInputStrict(schema *ir.Schema, inputName string, jsonBytes []byte) (map[string]any, ValidationErrors) {
	return New(schema).LoadInputStrict(inputName, jsonBytes)
}

// LoadTypeYAML is the package-level shortcut for [Runtime.LoadTypeYAML].
func LoadTypeYAML(schema *ir.Schema, typeName string, yamlBytes []byte) (map[string]any, ValidationErrors) {
	return New(schema).LoadTypeYAML(typeName, yamlBytes)
}

// LoadInputYAML is the package-level shortcut for [Runtime.LoadInputYAML].
func LoadInputYAML(schema *ir.Schema, inputName string, yamlBytes []byte) (map[string]any, ValidationErrors) {
	return New(schema).LoadInputYAML(inputName, yamlBytes)
}

// ValidateType is the package-level shortcut for [Runtime.ValidateType].
func ValidateType(schema *ir.Schema, typeName string, data map[string]any) ValidationErrors {
	return New(schema).ValidateType(typeName, data)
}

// ValidateInput is the package-level shortcut for [Runtime.ValidateInput].
func ValidateInput(schema *ir.Schema, inputName string, data map[string]any) ValidationErrors {
	return New(schema).ValidateInput(inputName, data)
}

// MarshalType is the package-level shortcut for [Runtime.MarshalType].
func MarshalType(schema *ir.Schema, typeName string, data map[string]any) ([]byte, ValidationErrors) {
	return New(schema).MarshalType(typeName, data)
}

// MarshalInput is the package-level shortcut for [Runtime.MarshalInput].
func MarshalInput(schema *ir.Schema, inputName string, data map[string]any) ([]byte, ValidationErrors) {
	return New(schema).MarshalInput(inputName, data)
}

// MaskType is the package-level shortcut for [Runtime.MaskType].
func MaskType(schema *ir.Schema, typeName string, data map[string]any) map[string]any {
	return New(schema).MaskType(typeName, data)
}

// MaskInput is the package-level shortcut for [Runtime.MaskInput].
func MaskInput(schema *ir.Schema, inputName string, data map[string]any) map[string]any {
	return New(schema).MaskInput(inputName, data)
}

// MergeType is the package-level shortcut for [Runtime.MergeType].
func MergeType(schema *ir.Schema, typeName string, newData, existingData map[string]any) map[string]any {
	return New(schema).MergeType(typeName, newData, existingData)
}

// MergeInput is the package-level shortcut for [Runtime.MergeInput].
func MergeInput(schema *ir.Schema, inputName string, newData, existingData map[string]any) map[string]any {
	return New(schema).MergeInput(inputName, newData, existingData)
}
