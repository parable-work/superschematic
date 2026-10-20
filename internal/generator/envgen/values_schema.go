package envgen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/ir"
)

// valuesSchemaFileName is the filename for the generated JSON Schema
// describing a service's env var contract. The file is emitted alongside
// config.go (or src/config.rs) so every service that uses the generated
// loader also has a machine-readable env var manifest.
//
// A deployment's drift check reads these files to verify its Helm values
// files do not set env vars that the schema does not declare.
const valuesSchemaFileName = "values-schema.json"

// valuesSchema is the on-disk format. It is a JSON Schema draft 2020-12
// object describing a single service's env var contract, extended with an
// x-superschematic section that carries superschematic-specific metadata (type names,
// required/default info, scalar primitives, secret flags) in a typed form.
//
// The JSON Schema portion is compatible with generic schema tooling; the
// x-superschematic extension is what the drift check consumes.
type valuesSchema struct {
	Schema               string                      `json:"$schema"`
	Title                string                      `json:"title"`
	Description          string                      `json:"description"`
	Type                 string                      `json:"type"`
	AdditionalProperties bool                        `json:"additionalProperties"`
	Properties           map[string]valuesSchemaProp `json:"properties"`
	// PatternProperties are the variables named by a pattern: the members
	// of a callers field's issuers, keys and callers, under their indexes.
	PatternProperties map[string]valuesSchemaProp `json:"patternProperties,omitempty"`
	Required          []string                    `json:"required,omitempty"`
	XSuperschematic   valuesSchemaExtension       `json:"x-superschematic"`
}

// valuesSchemaProp is a single property in the JSON Schema "properties" map.
type valuesSchemaProp struct {
	Type        string `json:"type"`
	Description string `json:"description,omitempty"`
	Default     any    `json:"default,omitempty"`
	// Pattern and MinLength constrain a string, as a callers field's
	// variables are: a count, a list, an algorithm list, a member that
	// must not be empty.
	Pattern   string `json:"pattern,omitempty"`
	MinLength int    `json:"minLength,omitempty"`
}

// valuesSchemaExtension is the x-superschematic vendor extension, carrying the
// metadata the drift check needs in a form that round-trips Go types
// cleanly (unlike a plain JSON Schema which flattens everything into
// strings).
type valuesSchemaExtension struct {
	SchemaName string               `json:"schemaName"`
	TypeName   string               `json:"typeName"`
	EnvVars    []valuesSchemaEnvVar `json:"envVars"`
}

// valuesSchemaEnvVar describes a single env var entry.
type valuesSchemaEnvVar struct {
	Name       string   `json:"name"`
	IRType     string   `json:"irType"`
	Primitive  string   `json:"primitive"`
	Required   bool     `json:"required"`
	HasDefault bool     `json:"hasDefault"`
	Default    string   `json:"default,omitempty"`
	IsEnum     bool     `json:"isEnum,omitempty"`
	IsScalar   bool     `json:"isScalar,omitempty"`
	EnumValues []string `json:"enumValues,omitempty"`
	// Secret marks a Secret<T> field: a deployment's values bind it through
	// a secret reference and its local tooling delivers it from a secret
	// store, never as a literal.
	Secret      bool   `json:"secret,omitempty"`
	Description string `json:"description,omitempty"`
	// Derived marks a field an edge derives in a stack
	// (docs/stack-model.md, section 3.4) with the edge's kind: sql for the
	// API's database, http for an API it calls. A deployment's values never
	// set it: the platform sets its Variables from the value the edge's
	// connector derives, one per member.
	Derived ir.EdgeKind `json:"derived,omitempty"`
	// Service is the DB or API service a derived field's edge reaches.
	Service string `json:"service,omitempty"`
	// Variables are the environment variables a derived field is read
	// from, one per member its value can have.
	Variables []string `json:"variables,omitempty"`
	// CallersOf names the API whose callers field this is (D37;
	// docs/stack-model.md, section 9.2): in a stack the http edges to the
	// API derive it, and outside one a deployment sets it. Its variables
	// are VariablePatterns' beside Variables, a list being a variable of
	// its length and each entry's members under its index.
	CallersOf string `json:"callersOf,omitempty"`
	// VariablePatterns are the patterns of the variables of a field whose
	// lists make their names, the keys of the schema's patternProperties.
	VariablePatterns []string `json:"variablePatterns,omitempty"`
}

// BuildValuesSchema converts a ConfigOutput into the on-disk values-schema
// representation. It is pure (no I/O) so it is trivially testable.
func BuildValuesSchema(output *ConfigOutput) *valuesSchema {
	if output == nil {
		return nil
	}

	properties := make(map[string]valuesSchemaProp, len(output.Fields))
	envVars := make([]valuesSchemaEnvVar, 0, len(output.Fields))
	var required []string

	for _, field := range output.Fields {
		primitive := fieldPrimitive(field)

		prop := valuesSchemaProp{
			Type:        jsonSchemaType(field, primitive),
			Description: field.Description,
		}
		if field.HasDefault {
			prop.Default = typedDefault(field, primitive, field.DefaultValue)
		}
		properties[field.Key] = prop

		if field.Required && !field.HasDefault {
			required = append(required, field.Key)
		}

		envVars = append(envVars, valuesSchemaEnvVar{
			Name:        field.Key,
			IRType:      field.IRType,
			Primitive:   primitive,
			Required:    field.Required,
			HasDefault:  field.HasDefault,
			Default:     field.DefaultValue,
			IsEnum:      field.IsEnum,
			IsScalar:    field.IsScalar,
			EnumValues:  field.EnumValues,
			Secret:      field.Secret,
			Description: field.Description,
		})
	}

	for _, field := range output.Derived {
		for _, variable := range field.Variables {
			properties[variable] = valuesSchemaProp{
				Type:        "string",
				Description: fmt.Sprintf("Derived: a member of %s, the %s of %s, which the platform sets.", field.Key, derivedNoun(field), field.Service),
			}
		}
		envVars = append(envVars, valuesSchemaEnvVar{
			Name:        field.Key,
			IRType:      field.ContractType(),
			Primitive:   "object",
			Required:    true,
			Derived:     field.Kind,
			Service:     field.Service,
			Variables:   field.Variables,
			Description: fmt.Sprintf("The %s of %s (%s), one variable per member.", derivedNoun(field), field.Service, field.From),
		})
	}

	var patterns map[string]valuesSchemaProp
	if field := output.ListedCallersField; field != "" {
		patterns = map[string]valuesSchemaProp{}
		entry := valuesSchemaEnvVar{
			Name:        field,
			IRType:      "ServiceAuth",
			Primitive:   "object",
			Required:    true,
			CallersOf:   output.SchemaName,
			Description: fmt.Sprintf("The callers field of %s: the issuers whose service credentials its server accepts, and the callers each vouches for, which a @requireService or @allowService operation checks. In a stack the http edges to %s derive it; outside one, set it. A list is a variable holding its length and each entry's members under its index; no issuers refuses every service credential.", output.SchemaName, output.SchemaName),
		}
		for _, v := range callersVariables(field) {
			if v.pattern == "" {
				properties[v.name] = v.prop
				entry.Variables = append(entry.Variables, v.name)
				continue
			}
			patterns[v.pattern] = v.prop
			entry.VariablePatterns = append(entry.VariablePatterns, v.pattern)
		}
		envVars = append(envVars, entry)
	}

	// An API with derived fields and no @envVars type has only EnvConfig.
	title := output.TypeName
	if title == "" {
		title = EnvConfigTypeName
	}

	sort.Strings(required)
	sort.Slice(envVars, func(i, j int) bool {
		return envVars[i].Name < envVars[j].Name
	})

	return &valuesSchema{
		Schema:               "https://json-schema.org/draft/2020-12/schema",
		Title:                title,
		Description:          fmt.Sprintf("Environment variables for %s (generated by superschematic)", output.SchemaName),
		Type:                 "object",
		AdditionalProperties: false,
		Properties:           properties,
		PatternProperties:    patterns,
		Required:             required,
		XSuperschematic: valuesSchemaExtension{
			SchemaName: output.SchemaName,
			TypeName:   title,
			EnvVars:    envVars,
		},
	}
}

// The shapes of a callers field's values, as stackconfig.LoadCallers,
// loadCallers and load_callers read them (runtime/http/testdata/
// stackconfig_parity.json): a count is an integer with an optional sign,
// whose range the reader checks; a list is comma-separated, each entry
// something besides spaces; algorithms are a list of RS256, ES256 and
// EdDSA; an index is a decimal without leading zeros, as the readers write
// the names they read.
const (
	callersCount      = `^[+-]?[0-9]+$`
	callersList       = `^[^,]*[^,\s][^,]*(,[^,]*[^,\s][^,]*)*$`
	callersOptional   = `^$|` + callersList
	callersAlgorithms = `^\s*(RS256|ES256|EdDSA)\s*(,\s*(RS256|ES256|EdDSA)\s*)*$`
	callersIndex      = `(0|[1-9][0-9]*)`
)

// callersVariable is a variable of a callers field: its name, or the
// pattern its name matches, and the shape of its value.
type callersVariable struct {
	name    string
	pattern string
	prop    valuesSchemaProp
}

// callersVariables are the variables of the callers field named field,
// as ir.DerivedVariables writes an ir.ServiceAuth and the HTTP runtimes
// read it: the number of issuers, then each issuer's members under its
// index i, each of its keys' under j and each of its callers' under k.
// Every other variable under the field's name is refused by the readers,
// and by the schema, whose additionalProperties is false.
func callersVariables(field string) []callersVariable {
	issuer := "^" + field + "_ISSUERS_" + callersIndex + "_"
	member := func(suffix string, prop valuesSchemaProp) callersVariable {
		return callersVariable{pattern: issuer + suffix + "$", prop: prop}
	}
	text := func(description string, minLength int) valuesSchemaProp {
		return valuesSchemaProp{Type: "string", Description: description, MinLength: minLength}
	}
	shaped := func(description, pattern string) valuesSchemaProp {
		return valuesSchemaProp{Type: "string", Description: description, Pattern: pattern}
	}
	return []callersVariable{
		{name: field + "_ISSUERS", prop: shaped("The number of issuers of the callers field "+field+", from 0 to 1000; 0 refuses every service credential.", callersCount)},
		member("ISSUER", text("The iss an issuer writes.", 1)),
		member("ISSUER_ALIASES", shaped("Other iss values of the issuer, comma-separated, such as Google's accounts.google.com.", callersOptional)),
		member("AUDIENCE", text("The aud a token of the issuer must hold: this server.", 1)),
		member("ALGORITHMS", shaped("The algorithms the issuer signs with, comma-separated: RS256, ES256 or EdDSA.", callersAlgorithms)),
		member("JWKS_URL", text("Where the issuer publishes its keys; an issuer has it or KEYS, not both.", 0)),
		member("KEYS", shaped("The number of the issuer's keys, at least 1, when it lists them rather than JWKS_URL.", callersCount)),
		member("KEYS_"+callersIndex+"_JWK", text("A key of the issuer: a public JWK's JSON, with its kid. A key has JWK or PEM, not both.", 0)),
		member("KEYS_"+callersIndex+"_PEM", text("A key of the issuer: an Ed25519 public key's SubjectPublicKeyInfo PEM, whose kid is its RFC 7638 thumbprint (D63). A key has JWK or PEM, not both.", 0)),
		member("SUBJECT_CLAIM", text("The claim that names the caller; sub when unset.", 0)),
		member("MAX_LIFETIME_SECONDS", shaped("The longest a token of the issuer may live, from its iat to its exp, in whole seconds above 0.", callersCount)),
		member("CALLERS", shaped("The number of the issuer's callers, at least 1.", callersCount)),
		member("CALLERS_"+callersIndex+"_SUBJECT", text("The subject claim's value that names a caller.", 1)),
		member("CALLERS_"+callersIndex+"_DEPLOYABLE", text("The deployable a caller is.", 1)),
		member("CALLERS_"+callersIndex+"_SERVES", shaped("The APIs a caller serves, comma-separated, which an operation's from is checked against.", callersList)),
	}
}

// CallersValues is the values-schema.json output of an API whose only
// configuration is its callers field: a Go API with a service clause and
// no @envVars type, database, call or bucket, whose EnvConfig the build
// does not write, since its entrypoint reads the field itself.
func CallersValues(schemaName string) *ConfigOutput {
	return &ConfigOutput{SchemaName: schemaName, Fields: []ConfigField{}, ListedCallersField: ir.CallersField(schemaName)}
}

// derivedNoun names what a derived field holds.
func derivedNoun(field DerivedField) string {
	if field.Kind == ir.EdgeSQL {
		return "database connection"
	}
	if field.Kind == ir.EdgeBucket {
		return "bucket connection"
	}
	return "service endpoint"
}

// fieldPrimitive returns the IR language primitive underlying a field.
// Enums collapse to "string" (they're stored as strings in env vars and
// validated against EnumValues in the drift check); named scalars report
// their declared LanguagePrimitive.
func fieldPrimitive(field ConfigField) string {
	if field.IsEnum {
		return codegen.PrimitiveString
	}
	if field.IsScalar && field.ScalarPrimitive != "" {
		return field.ScalarPrimitive
	}
	if codegen.IsLanguagePrimitive(field.IRType) {
		return field.IRType
	}
	return codegen.PrimitiveString
}

// jsonSchemaType maps a field to its JSON Schema "type" value. Plain number
// fields parse with the integer loader in the generated config.go, so they
// declare "integer"; number-primitive scalars keep "number".
func jsonSchemaType(field ConfigField, primitive string) string {
	switch primitive {
	case codegen.PrimitiveNumber:
		if field.GoType == "int" {
			return "integer"
		}
		return "number"
	case codegen.PrimitiveBoolean:
		return "boolean"
	default:
		return "string"
	}
}

// typedDefault converts the superschematic default (always stored as a string) into
// the appropriate Go type for JSON Schema's "default" field so the emitted
// schema is valid against its declared "type". If parsing fails we fall
// back to the raw string; env vars round-trip through strings anyway.
func typedDefault(field ConfigField, primitive, raw string) any {
	switch primitive {
	case codegen.PrimitiveNumber:
		if field.GoType == "int" {
			if v, err := strconv.ParseInt(raw, 10, 64); err == nil {
				return v
			}
		}
		if v, err := strconv.ParseFloat(raw, 64); err == nil {
			return v
		}
	case codegen.PrimitiveBoolean:
		if v, err := strconv.ParseBool(raw); err == nil {
			return v
		}
	}
	return raw
}

// WriteValuesSchema writes the generated values-schema.json to outputDir.
// This is safe to call alongside WriteConfig / WriteRustConfig; the two
// files do not collide.
func WriteValuesSchema(output *ConfigOutput, outputDir string) error {
	if output == nil {
		return nil
	}

	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", outputDir, err)
	}

	schema := BuildValuesSchema(output)

	buf := &bytes.Buffer{}
	enc := json.NewEncoder(buf)
	enc.SetIndent("", "  ")
	enc.SetEscapeHTML(false)
	if err := enc.Encode(schema); err != nil {
		return fmt.Errorf("failed to encode values schema: %w", err)
	}

	target := filepath.Join(outputDir, valuesSchemaFileName)
	if err := os.WriteFile(target, buf.Bytes(), 0o644); err != nil {
		return fmt.Errorf("failed to write values schema to %s: %w", target, err)
	}

	return nil
}
