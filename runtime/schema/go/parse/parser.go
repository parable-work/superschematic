package parse

import (
	"encoding/json"
	"fmt"

	"github.com/parable-work/superschematic/ir"
	"gopkg.in/yaml.v3"
)

// Parser interprets an IR Schema to coerce, normalize, default, and
// scalar-parse runtime data maps. It is independent of validate so callers
// can choose to run parse, validate, or both. See the package doc for the
// per-field operation order.
type Parser struct {
	schema   *ir.Schema
	parseReg *ParseRegistry
	normReg  *NormalizeRegistry
	strict   bool
}

// Option configures a Parser during construction.
type Option func(*Parser)

// WithParseRegistry sets the registry of custom scalar parse functions.
// Defaults to an empty registry; HasCustomParse scalars without a matching
// entry simply skip the parse step.
func WithParseRegistry(r *ParseRegistry) Option {
	return func(p *Parser) {
		p.parseReg = r
	}
}

// WithNormalizeRegistry sets the registry of custom scalar normalize
// functions. Defaults to an empty registry; HasCustomNormalize scalars
// without a matching entry skip the normalize step.
func WithNormalizeRegistry(r *NormalizeRegistry) Option {
	return func(p *Parser) {
		p.normReg = r
	}
}

// WithStrict makes the default FromMap / ParseType / ParseInput entry points
// behave as their Strict variants (reject unknown fields, no coercion).
// Convenience for callers that pass a strict flag through configuration.
func WithStrict(strict bool) Option {
	return func(p *Parser) {
		p.strict = strict
	}
}

// New creates a Parser for the given schema. Both registries default to
// empty; pass [WithParseRegistry] / [WithNormalizeRegistry] with the
// DefaultParseRegistry / DefaultNormalizeRegistry constructors to wire the
// scalar-lib defaults.
func New(schema *ir.Schema, opts ...Option) *Parser {
	p := &Parser{
		schema:   schema,
		parseReg: NewParseRegistry(),
		normReg:  NewNormalizeRegistry(),
	}
	for _, opt := range opts {
		opt(p)
	}
	return p
}

// ParseType parses raw into a result map honouring typeName's TypeDef.
// Unknown fields are kept in lenient mode and rejected with
// {Validator: "unknown_field"} in strict mode (toggled via [WithStrict] or
// [Parser.ParseTypeStrict]).
func (p *Parser) ParseType(typeName string, raw map[string]any) (map[string]any, ValidationErrors) {
	return p.parseTypeWithStrict(typeName, raw, p.strict)
}

// ParseInput parses raw into a result map honouring inputName's TypeDef.
// Same lenient/strict semantics as ParseType.
func (p *Parser) ParseInput(inputName string, raw map[string]any) (map[string]any, ValidationErrors) {
	return p.parseInputWithStrict(inputName, raw, p.strict)
}

// ParseTypeStrict is ParseType with strict=true.
func (p *Parser) ParseTypeStrict(typeName string, raw map[string]any) (map[string]any, ValidationErrors) {
	return p.parseTypeWithStrict(typeName, raw, true)
}

// ParseInputStrict is ParseInput with strict=true.
func (p *Parser) ParseInputStrict(inputName string, raw map[string]any) (map[string]any, ValidationErrors) {
	return p.parseInputWithStrict(inputName, raw, true)
}

// FromMap is a name-agnostic helper that dispatches to ParseInput if a name
// matches an input definition, otherwise to ParseType.
func (p *Parser) FromMap(name string, raw map[string]any) (map[string]any, ValidationErrors) {
	if _, ok := p.schema.Inputs[name]; ok {
		return p.parseInputWithStrict(name, raw, p.strict)
	}
	return p.parseTypeWithStrict(name, raw, p.strict)
}

// FromMapStrict is FromMap with strict=true.
func (p *Parser) FromMapStrict(name string, raw map[string]any) (map[string]any, ValidationErrors) {
	if _, ok := p.schema.Inputs[name]; ok {
		return p.parseInputWithStrict(name, raw, true)
	}
	return p.parseTypeWithStrict(name, raw, true)
}

// ParseTypeJSON unmarshals data as JSON and parses it as typeName.
func (p *Parser) ParseTypeJSON(typeName string, data []byte) (map[string]any, ValidationErrors) {
	raw, errs := unmarshalJSON(data)
	if errs.HasErrors() {
		return nil, errs
	}
	return p.parseTypeWithStrict(typeName, raw, p.strict)
}

// ParseInputJSON unmarshals data as JSON and parses it as inputName.
func (p *Parser) ParseInputJSON(inputName string, data []byte) (map[string]any, ValidationErrors) {
	raw, errs := unmarshalJSON(data)
	if errs.HasErrors() {
		return nil, errs
	}
	return p.parseInputWithStrict(inputName, raw, p.strict)
}

// ParseTypeYAML unmarshals data as YAML and parses it as typeName.
func (p *Parser) ParseTypeYAML(typeName string, data []byte) (map[string]any, ValidationErrors) {
	raw, errs := unmarshalYAML(data)
	if errs.HasErrors() {
		return nil, errs
	}
	return p.parseTypeWithStrict(typeName, raw, p.strict)
}

// ParseInputYAML unmarshals data as YAML and parses it as inputName.
func (p *Parser) ParseInputYAML(inputName string, data []byte) (map[string]any, ValidationErrors) {
	raw, errs := unmarshalYAML(data)
	if errs.HasErrors() {
		return nil, errs
	}
	return p.parseInputWithStrict(inputName, raw, p.strict)
}

func (p *Parser) parseTypeWithStrict(typeName string, raw map[string]any, strict bool) (map[string]any, ValidationErrors) {
	td, ok := p.schema.Types[typeName]
	if !ok {
		errs := NewValidationErrors()
		errs.AddFieldError("", "type", fmt.Sprintf("unknown type %q", typeName))
		return nil, errs
	}
	return p.walk(td, raw, strict)
}

func (p *Parser) parseInputWithStrict(inputName string, raw map[string]any, strict bool) (map[string]any, ValidationErrors) {
	td, ok := p.schema.Inputs[inputName]
	if !ok {
		errs := NewValidationErrors()
		errs.AddFieldError("", "type", fmt.Sprintf("unknown input %q", inputName))
		return nil, errs
	}
	return p.walk(td, raw, strict)
}

func (p *Parser) walk(td *ir.TypeDef, raw map[string]any, strict bool) (map[string]any, ValidationErrors) {
	if raw == nil {
		raw = map[string]any{}
	}
	result := make(map[string]any, len(td.Fields))
	errs := NewValidationErrors()
	p.walkTypeDef(td, raw, result, errs, strict)
	return result, errs
}

func unmarshalJSON(data []byte) (map[string]any, ValidationErrors) {
	errs := NewValidationErrors()
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		errs.AddFieldError("", "json", err.Error())
		return nil, errs
	}
	if raw == nil {
		errs.AddFieldError("", "json", "expected JSON object payload")
		return nil, errs
	}
	return raw, errs
}

func unmarshalYAML(data []byte) (map[string]any, ValidationErrors) {
	errs := NewValidationErrors()
	var raw map[string]any
	if err := yaml.Unmarshal(data, &raw); err != nil {
		errs.AddFieldError("", "yaml", err.Error())
		return nil, errs
	}
	if raw == nil {
		errs.AddFieldError("", "yaml", "expected YAML mapping payload")
		return nil, errs
	}
	return raw, errs
}
