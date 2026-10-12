package envgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"text/template"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/rustutil"
	"github.com/parable-work/superschematic/ir"
)

// WriteRustConfig writes the generated Rust configuration loader to
// src/config.rs: load_config, the loader of the @envVars type, and, with
// output.EnvConfig, EnvConfig and load_env_config, which join the settings
// with the fields a stack sets for the API (D57). It names the HTTP
// runtime as `crate::runtime`, which the API crate's lib.rs re-exports.
// values-schema.json beside it lists the settings and the derived fields;
// an output with neither, whose EnvConfig holds only the fields that
// follow the environment's edges, writes none, as the TypeScript API's
// build writes none.
func WriteRustConfig(output *ConfigOutput, outputDir string) error {
	srcDir := filepath.Join(outputDir, "src")
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return fmt.Errorf("failed to create output directory %s: %w", srcDir, err)
	}

	target := filepath.Join(srcDir, "config.rs")
	if err := generateRustFile("config.rust.tmpl", target, rustConfig{ConfigOutput: output, Env: rustEnvConfigOf(output)}); err != nil {
		return fmt.Errorf("failed to generate Rust config loader: %w", err)
	}

	if output.TypeName == "" && len(output.Derived) == 0 {
		return nil
	}
	if err := WriteValuesSchema(output, outputDir); err != nil {
		return fmt.Errorf("failed to generate values schema: %w", err)
	}

	return nil
}

func generateRustFile(templateName, outputPath string, data any) error {
	return codegen.GenerateFile(
		codegen.NewFileConfig(templatesFS, templateName, outputPath, data, rustTemplateFuncs()),
	)
}

func rustTemplateFuncs() template.FuncMap {
	return template.FuncMap{
		"toRustFieldName":   toRustFieldName,
		"rustType":          rustType,
		"rustLoaderExpr":    rustLoaderExpr,
		"formatRustStrings": formatRustStrings,
		"usesRustInt":       usesRustInt,
		"usesRustBool":      usesRustBool,
		"usesRustFloat":     usesRustFloat,
		"usesRustEnum":      usesRustEnum,
	}
}

func toRustFieldName(key string) string {
	return strings.ToLower(strings.TrimSpace(key))
}

func rustType(field ConfigField) string {
	if field.IsEnum {
		return "String"
	}

	switch loaderPrimitive(field) {
	case "Int":
		return "i64"
	case "Float":
		return "f64"
	case "Boolean":
		return "bool"
	default:
		return "String"
	}
}

// loaderPrimitive is the primitive a loader parses a non-enum field's text
// as: "Int", "Float", "Boolean" or "String". The Rust and TypeScript loaders
// share it, so both read a value the same way.
func loaderPrimitive(field ConfigField) string {
	if field.IsScalar {
		switch field.ScalarKind {
		case "Float":
			return "Float"
		case "Int":
			return "Int"
		case "Boolean":
			return "Boolean"
		}
	}
	if field.IsScalar && field.ScalarPrimitive != "" {
		return languagePrimitiveLoaderName(field.ScalarPrimitive)
	}
	return languagePrimitiveLoaderName(field.IRType)
}

func rustLoaderExpr(field ConfigField) string {
	keyLiteral := fmt.Sprintf("%q", field.Key)

	if field.IsEnum {
		allowed := formatRustStrings(field.EnumValues)
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_enum(%s, &%s)?", keyLiteral, allowed)
		}
		defaultLiteral := `""`
		if field.HasDefault {
			defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("enum_or_default(%s, &%s, %s)?", keyLiteral, allowed, defaultLiteral)
	}

	switch loaderPrimitive(field) {
	case "Int":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_i64(%s)?", keyLiteral)
		}
		defaultInt := int64(0)
		if field.HasDefault {
			if parsed, err := strconv.ParseInt(field.DefaultValue, 10, 64); err == nil {
				defaultInt = parsed
			}
		}
		return fmt.Sprintf("i64_or_default(%s, %d)?", keyLiteral, defaultInt)
	case "Boolean":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_bool(%s)?", keyLiteral)
		}
		defaultBool := false
		if field.HasDefault {
			if parsed, err := strconv.ParseBool(field.DefaultValue); err == nil {
				defaultBool = parsed
			}
		}
		return fmt.Sprintf("bool_or_default(%s, %t)?", keyLiteral, defaultBool)
	case "Float":
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_f64(%s)?", keyLiteral)
		}
		defaultFloat := float64(0)
		if field.HasDefault {
			if parsed, err := strconv.ParseFloat(field.DefaultValue, 64); err == nil {
				defaultFloat = parsed
			}
		}
		return fmt.Sprintf("f64_or_default(%s, %s)?", keyLiteral, strconv.FormatFloat(defaultFloat, 'g', -1, 64))
	default:
		if field.Required && !field.HasDefault {
			return fmt.Sprintf("require_string(%s)?", keyLiteral)
		}
		defaultLiteral := `""`
		if field.HasDefault {
			defaultLiteral = fmt.Sprintf("%q", field.DefaultValue)
		}
		return fmt.Sprintf("string_or_default(%s, %s)", keyLiteral, defaultLiteral)
	}
}

func formatRustStrings(values []string) string {
	if len(values) == 0 {
		return "[]"
	}

	quoted := make([]string, 0, len(values))
	for _, value := range values {
		quoted = append(quoted, fmt.Sprintf("%q", value))
	}

	return fmt.Sprintf("[%s]", strings.Join(quoted, ", "))
}

func usesRustInt(fields []ConfigField) bool {
	for _, field := range fields {
		if !field.IsEnum && loaderPrimitive(field) == "Int" {
			return true
		}
	}
	return false
}

func usesRustBool(fields []ConfigField) bool {
	for _, field := range fields {
		if !field.IsEnum && loaderPrimitive(field) == "Boolean" {
			return true
		}
	}
	return false
}

func usesRustFloat(fields []ConfigField) bool {
	for _, field := range fields {
		if !field.IsEnum && loaderPrimitive(field) == "Float" {
			return true
		}
	}
	return false
}

func usesRustEnum(fields []ConfigField) bool {
	for _, field := range fields {
		if field.IsEnum {
			return true
		}
	}
	return false
}

// rustConfig is the template view of src/config.rs: the @envVars loader's
// output, and the EnvConfig that joins it with the derived fields when the
// output has one.
type rustConfig struct {
	*ConfigOutput
	// Env is nil without an EnvConfig.
	Env *rustEnvConfig
}

// rustEnvConfig is the template view of the Rust EnvConfig (D57).
type rustEnvConfig struct {
	// Settings are the Rust fields of the @envVars type, which EnvConfig
	// holds too.
	Settings []string
	// Fields are the fields a stack sets: each derived field, then the
	// callers, CORS and identity config fields.
	Fields []rustEnvField
	// Imports are the use declarations of the runtime's items.
	Imports []string
	// Pattern and Values are the match load_env_config ends with: the
	// pattern of each value loaded, and the variable that holds it, in
	// order. One is matched alone, more as a tuple.
	Pattern []string
	Values  []string
	// Stack reports whether a field is read with a stackconfig reader,
	// whose refusals problems_of collects, and Identity whether one is the
	// identity config, which load_identity reads.
	Stack    bool
	Identity bool
}

// UsesEnv reports whether config.rs reads std::env: the @envVars loader
// does, and so does load_identity.
func (c rustConfig) UsesEnv() bool {
	return c.TypeName != "" || (c.Env != nil && c.Env.Identity)
}

// rustEnvField is a field of EnvConfig a stack sets.
type rustEnvField struct {
	// Name is the Rust field, the config field's name in lower case.
	Name string
	// Type is its Rust type.
	Type string
	// Read is the Rust expression that reads it, of type
	// Result<Type, Vec<String>>.
	Read string
	// Doc is its doc comment, one line per element.
	Doc []string
}

// Inline reports whether the field's read fits on one line, as rustfmt
// lays a call out: its arguments in 60 columns and the line in 100.
func (f rustEnvField) Inline() bool {
	args := "&mut problems, " + f.Read
	return len(args) <= 60 && len("    let "+f.Name+" = keep("+args+");") <= 100
}

func rustEnvConfigOf(output *ConfigOutput) *rustEnvConfig {
	if !output.EnvConfig {
		return nil
	}
	env := &rustEnvConfig{}
	for _, field := range output.Fields {
		env.Settings = append(env.Settings, toRustFieldName(field.Key))
	}
	stackItems, rootItems := map[string]bool{}, map[string]bool{}
	stack := func(key, reader, typ, doc string) {
		env.Stack = true
		stackItems["self"], stackItems[typ] = true, true
		env.Fields = append(env.Fields, rustEnvField{
			Name: toRustFieldName(key),
			Type: typ,
			Read: fmt.Sprintf("stackconfig::%s(%q).map_err(problems_of)", reader, key),
			Doc:  wrapRustDoc(doc),
		})
	}
	for _, field := range output.Derived {
		switch field.Kind {
		case ir.EdgeSQL:
			from := "its one DB-kind dependency"
			if field.From == "authDb" {
				from = "its authDb"
			}
			stack(field.Key, "load_database", "Database",
				fmt.Sprintf("The connection to %s, the API's database: %s. It is read from %s_*.", field.Service, from, field.Key))
		case ir.EdgeBucket:
			stack(field.Key, "load_bucket", "Bucket",
				fmt.Sprintf("How the server reaches %s, a bucket the API lists. It is read from %s_*.", field.Service, field.Key))
		default:
			stack(field.Key, "load_service", "Service",
				fmt.Sprintf("The endpoint of %s, which the API calls. It is read from %s_*.", field.Service, field.Key))
		}
	}
	if output.CallersField != "" {
		env.Stack = true
		stackItems["self"], rootItems["ServiceAuthConfig"] = true, true
		env.Fields = append(env.Fields, rustEnvField{
			Name: toRustFieldName(output.CallersField),
			Type: "ServiceAuthConfig",
			Read: fmt.Sprintf("stackconfig::load_callers(%q).map_err(problems_of)", output.CallersField),
			Doc:  wrapRustDoc(fmt.Sprintf("What the server verifies a calling service's credential against: the API's callers field, for its service clauses. No issuers means no server calls the API. It is read from %s_*.", output.CallersField)),
		})
	}
	if output.CORSField != "" {
		stack(output.CORSField, "load_cors", "Cors",
			fmt.Sprintf("The origins of the sites that call the API, which its server answers CORS for: none in an environment where no site calls it (D55). It is read from %s_ORIGINS.", output.CORSField))
	}
	if output.IdentityField != "" {
		env.Identity = true
		rootItems["identity"] = true
		env.Fields = append(env.Fields, rustEnvField{
			Name: toRustFieldName(output.IdentityField),
			Type: "identity::Config",
			Read: fmt.Sprintf("load_identity(%q)", output.IdentityField),
			Doc:  wrapRustDoc(fmt.Sprintf("The identity runtime's config (D50), parsed from the JSON in %s: the runtime's defaults when it is unset or empty.", output.IdentityField)),
		})
	}
	for _, use := range []struct {
		path  string
		items map[string]bool
	}{{"crate::runtime::stackconfig", stackItems}, {"crate::runtime", rootItems}} {
		if len(use.items) == 0 {
			continue
		}
		items := make([]string, 0, len(use.items))
		for item := range use.items {
			items = append(items, item)
		}
		env.Imports = append(env.Imports, rustutil.UseLine(use.path, items))
	}

	if output.TypeName != "" {
		if len(env.Settings) > 0 {
			env.Pattern = append(env.Pattern, "Some(settings)")
		} else {
			env.Pattern = append(env.Pattern, "Some(_)")
		}
		env.Values = append(env.Values, "settings")
	}
	for _, field := range env.Fields {
		env.Pattern = append(env.Pattern, "Some("+field.Name+")")
		env.Values = append(env.Values, field.Name)
	}
	return env
}

// rustDocWidth is the width a field's doc comment wraps at, its four
// spaces and `/// ` included.
const rustDocWidth = 80

// wrapRustDoc breaks text into the lines of a field's doc comment.
func wrapRustDoc(text string) []string {
	var lines []string
	line := ""
	for _, word := range strings.Fields(text) {
		if line != "" && 8+len(line)+1+len(word) > rustDocWidth {
			lines = append(lines, line)
			line = ""
		}
		if line != "" {
			line += " "
		}
		line += word
	}
	if line != "" {
		lines = append(lines, line)
	}
	return lines
}
