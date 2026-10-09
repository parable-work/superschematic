package envgen

import (
	"encoding/json"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/ir"
)

// TypeScriptConfigFile is the loader's file name inside the generated
// TypeScript types package. tsgen exports it as the package's "./config"
// subpath.
const TypeScriptConfigFile = "config.ts"

// typeScriptConfig is the template view of one @envVars class.
type typeScriptConfig struct {
	SchemaName string
	TypeName   string
	Fields     []typeScriptField
}

// typeScriptField is one variable: its loaded type and its field spec, both
// rendered in Go so the template stays declarative.
type typeScriptField struct {
	Key         string
	Description string
	LoadedType  string
	Spec        string
}

// WriteTypeScriptConfig writes config.ts, the TypeScript env loader, into a
// generated TypeScript types package directory. It imports the package's own
// types and validators, so tsgen must have written the package first.
func WriteTypeScriptConfig(output *ConfigOutput, typesDir string) error {
	view, err := buildTypeScriptConfig(output)
	if err != nil {
		return err
	}
	target := filepath.Join(typesDir, TypeScriptConfigFile)
	if err := codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, "config.ts.tmpl", target, view, nil),
	); err != nil {
		return fmt.Errorf("failed to generate TypeScript config loader: %w", err)
	}
	return nil
}

func buildTypeScriptConfig(output *ConfigOutput) (typeScriptConfig, error) {
	view := typeScriptConfig{
		SchemaName: output.SchemaName,
		TypeName:   output.TypeName,
		Fields:     make([]typeScriptField, 0, len(output.Fields)),
	}
	for _, field := range output.Fields {
		spec, err := typeScriptFieldSpec(field)
		if err != nil {
			return typeScriptConfig{}, fmt.Errorf("envgen: %s.%s: %w", output.TypeName, field.Key, err)
		}
		view.Fields = append(view.Fields, typeScriptField{
			Key:         field.Key,
			Description: typeScriptDoc(field.Description),
			LoadedType:  typeScriptLoadedType(output.TypeName, field),
			Spec:        spec,
		})
	}
	return view, nil
}

// typeScriptKind is how the loader parses a field's text.
func typeScriptKind(field ConfigField) string {
	if field.IsEnum {
		return "enum"
	}
	switch loaderPrimitive(field) {
	case "Int":
		return "integer"
	case "Float":
		return "float"
	case "Boolean":
		return "boolean"
	default:
		return "string"
	}
}

// typeScriptLoadedType is the field's type after loading. It reads the
// field's own type from the generated interface, so the loader never restates
// a schema type: a default makes it non-null, and Secret<T> wraps it.
func typeScriptLoadedType(typeName string, field ConfigField) string {
	loaded := fmt.Sprintf("NonNullable<%s[%s]>", typeName, jsString(field.Key))
	if field.Secret {
		loaded = "SecretValue<" + loaded + ">"
	}
	if !field.Required && !field.HasDefault {
		loaded += " | null"
	}
	return loaded
}

// typeScriptFieldSpec renders the FieldSpec object literal the loader reads a
// field by. A declared default must parse as the field's kind, or generation
// fails.
func typeScriptFieldSpec(field ConfigField) (string, error) {
	kind := typeScriptKind(field)
	parts := []string{
		"key: " + jsString(field.Key),
		"kind: " + jsString(kind),
		"required: " + strconv.FormatBool(field.Required),
		"secret: " + strconv.FormatBool(field.Secret),
	}
	if kind == "enum" {
		allowed := make([]string, 0, len(field.EnumValues))
		for _, value := range field.EnumValues {
			allowed = append(allowed, jsString(value))
		}
		parts = append(parts, "allowed: ["+strings.Join(allowed, ", ")+"]")
	}
	if field.HasDefault {
		fallback, err := typeScriptDefault(kind, field.DefaultValue)
		if err != nil {
			return "", err
		}
		parts = append(parts, "fallback: "+fallback)
	}
	return "{ " + strings.Join(parts, ", ") + " }", nil
}

func typeScriptDefault(kind, value string) (string, error) {
	switch kind {
	case "integer":
		parsed, err := strconv.ParseInt(value, 10, 64)
		if err != nil {
			return "", fmt.Errorf("default %q is not an integer", value)
		}
		return strconv.FormatInt(parsed, 10), nil
	case "float":
		parsed, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return "", fmt.Errorf("default %q is not a number", value)
		}
		return strconv.FormatFloat(parsed, 'g', -1, 64), nil
	case "boolean":
		parsed, err := strconv.ParseBool(value)
		if err != nil {
			return "", fmt.Errorf("default %q is not a boolean", value)
		}
		return strconv.FormatBool(parsed), nil
	default:
		return jsString(value), nil
	}
}

// jsString renders a TypeScript string literal. JSON string syntax is valid
// TypeScript, and encoding/json escapes U+2028 and U+2029.
func jsString(value string) string {
	encoded, err := json.Marshal(value)
	if err != nil {
		// json.Marshal cannot fail for a string.
		panic(err)
	}
	return string(encoded)
}

// typeScriptDoc flattens a schema description into one JSDoc-safe line.
func typeScriptDoc(description string) string {
	flat := strings.Join(strings.Fields(description), " ")
	return strings.ReplaceAll(flat, "*/", "*\\/")
}

// TypeScriptEnvConfigFile is the file of a generated TypeScript API
// package that declares EnvConfig and loadEnvConfig (D51).
const TypeScriptEnvConfigFile = "config.ts"

// typeScriptEnvConfig is the template view of an API's EnvConfig.
type typeScriptEnvConfig struct {
	SchemaName     string
	TypesPackage   string
	RuntimePackage string
	// TypeName is the @envVars type, empty without one.
	TypeName       string
	Derived        []typeScriptDerivedField
	HasDatabase    bool
	HasService     bool
	HasBucket      bool
	Callers        string
	CallersLiteral string
}

// typeScriptDerivedField is a field an edge derives, read by the HTTP
// runtime's Reader into its Type.
type typeScriptDerivedField struct {
	Key        string
	KeyLiteral string
	Reader     string
	Type       string
	Doc        string
}

// WriteTypeScriptEnvConfig writes config.ts into a generated TypeScript API
// package: EnvConfig, the API's @envVars settings (Loaded<Type>, which the
// types package's config.ts loads) joined with a field per edge a stack
// derives for it and its callers field, and loadEnvConfig, which reads
// them all from the environment through the HTTP runtime's stackconfig
// readers (D51; docs/stack-model.md, sections 3.4 and 8.6). Deps holds it.
func WriteTypeScriptEnvConfig(output *ConfigOutput, apiDir string) error {
	n := output.Naming.OrDefault()
	view := typeScriptEnvConfig{
		SchemaName:     output.SchemaName,
		TypesPackage:   n.NpmTypesPackage(output.SchemaName),
		RuntimePackage: n.HTTPRuntimeNpmPackage,
		TypeName:       output.TypeName,
		Callers:        output.CallersField,
		CallersLiteral: jsString(output.CallersField),
	}
	for _, field := range output.Derived {
		derived := typeScriptDerivedField{Key: field.Key, KeyLiteral: jsString(field.Key)}
		if field.Kind == ir.EdgeSQL {
			from := "its one DB-kind dependency"
			if field.From == "authDb" {
				from = "its authDb"
			}
			derived.Reader, derived.Type = "loadDatabase", "Database"
			derived.Doc = fmt.Sprintf("The connection to %s, the API's database: %s. It is read from %s_*.", field.Service, from, field.Key)
			view.HasDatabase = true
		} else if field.Kind == ir.EdgeBucket {
			derived.Reader, derived.Type = "loadBucket", "BucketConnection"
			derived.Doc = fmt.Sprintf("How the server reaches %s, a bucket the API lists. It is read from %s_*.", field.Service, field.Key)
			view.HasBucket = true
		} else {
			derived.Reader, derived.Type = "loadService", "Service"
			derived.Doc = fmt.Sprintf("The endpoint of %s, which the API calls. It is read from %s_*.", field.Service, field.Key)
			view.HasService = true
		}
		view.Derived = append(view.Derived, derived)
	}
	target := filepath.Join(apiDir, TypeScriptEnvConfigFile)
	if err := codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, "env.ts.tmpl", target, view, nil),
	); err != nil {
		return fmt.Errorf("failed to generate TypeScript EnvConfig: %w", err)
	}
	return nil
}
