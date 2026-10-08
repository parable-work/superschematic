package typegen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/profile"
)

// EnumImports returns the Go imports needed by enums.go: dependency modules
// referenced by imported enum aliases.
func (o *ModuleOutput) EnumImports() []ModuleImport {
	used := map[string]bool{}
	for _, e := range o.ImportedEnums {
		used[e.ImportAlias] = true
	}
	return o.filterImports(used)
}

// TypeImports returns the Go imports needed by types.go: dependency modules
// referenced by imported type and union aliases.
func (o *ModuleOutput) TypeImports() []ModuleImport {
	used := map[string]bool{}
	for _, t := range o.ImportedTypes {
		used[t.ImportAlias] = true
	}
	for _, u := range o.ImportedUnions {
		used[u.ImportAlias] = true
	}
	return o.filterImports(used)
}

// WritesScalars reports whether the module has a scalars.go. It carries the
// ValidationError aliases types.go's Validate() references, so it exists
// whenever types.go does; enums.go declares ValidationError only without it.
func (o *ModuleOutput) WritesScalars() bool {
	return len(o.Scalars) > 0 || len(o.Types) > 0 || len(o.ImportedTypes) > 0 || len(o.ImportedUnions) > 0
}

// NeedsRegexp reports whether types.go emits regexp-based pattern checks.
func (o *ModuleOutput) NeedsRegexp() bool {
	for _, typeInfo := range o.Types {
		for _, field := range typeInfo.Fields {
			for _, rule := range append(append([]codegen.ValidationRule(nil), field.Validations...), field.ScalarRules...) {
				if rule.Validator == "pattern" {
					return true
				}
			}
		}
	}
	return false
}

// NeedsRuneCount reports whether types.go emits a length check, which counts
// code points with unicode/utf8 as every other validator does.
func (o *ModuleOutput) NeedsRuneCount() bool {
	for _, typeInfo := range o.Types {
		for _, field := range typeInfo.Fields {
			for _, rule := range append(append([]codegen.ValidationRule(nil), field.Validations...), field.ScalarRules...) {
				if rule.Validator == "minLength" || rule.Validator == "maxLength" {
					return true
				}
			}
		}
	}
	return false
}

// ListFields returns the fields of t whose value is a list, T[] or T[][], in
// field order. UnmarshalJSON refuses a null element in them (D12): a list
// element is never null, and encoding/json would decode one to the
// element type's zero value, which Validate cannot tell from a real one. A
// map whose values are lists is not among them; no validator checks the
// elements of a map value.
func (t TypeInfo) ListFields() []FieldInfo {
	var fields []FieldInfo
	for _, field := range t.Fields {
		if field.IsArray && !field.IsMap {
			fields = append(fields, field)
		}
	}
	return fields
}

// KeptNullJSONFields returns the optional single Generic.JSON fields of t
// that are pointers (*GenericJSON), in field order. JSON null is a value
// there, apart from absent, but encoding/json leaves the pointer nil for
// null as for an absent key, so UnmarshalJSON sets it to the JSON null
// token. An input type's own field is an InputField, which keeps null
// itself.
func (t TypeInfo) KeptNullJSONFields() []FieldInfo {
	var fields []FieldInfo
	for _, field := range t.Fields {
		if keepsJSONNull(field) {
			fields = append(fields, field)
		}
	}
	return fields
}

// keepsJSONNull reports whether field is an optional single value of a
// scalar that holds any JSON value (Generic.JSON) and a pointer to it.
func keepsJSONNull(field FieldInfo) bool {
	return !field.Required && !field.UsesWrapper && !field.IsArray && !field.IsMap &&
		field.IsScalar && field.ScalarInfo != nil && field.ScalarInfo.Traits.IsAnyJSON &&
		strings.HasPrefix(field.GoType, "*")
}

// JSONObjectFields returns the fields of t that hold JSON-object scalar
// values (isJSONObjectField), in field order. UnmarshalJSON checks each
// value's JSON with superscalar and notes a required struct value the JSON
// left absent or null (decodeJSONObjects); Validate reports what it found.
func (t TypeInfo) JSONObjectFields() []FieldInfo {
	var fields []FieldInfo
	for _, field := range t.Fields {
		if isJSONObjectField(field) {
			fields = append(fields, field)
		}
	}
	return fields
}

// HasJSONObjectFields reports whether any type in types.go has a field
// JSONObjectFields returns, so the file carries decodeJSONObjects.
func (o *ModuleOutput) HasJSONObjectFields() bool {
	for _, typeInfo := range o.Types {
		if len(typeInfo.JSONObjectFields()) > 0 {
			return true
		}
	}
	return false
}

// HasKeptNullJSONFields reports whether any type in types.go has a field
// KeptNullJSONFields returns, so the file carries nullJSONMembers.
func (o *ModuleOutput) HasKeptNullJSONFields() bool {
	for _, typeInfo := range o.Types {
		if len(typeInfo.KeptNullJSONFields()) > 0 {
			return true
		}
	}
	return false
}

// HasListFields reports whether any type in types.go has a list field, so
// the file carries the null-element check UnmarshalJSON calls.
func (o *ModuleOutput) HasListFields() bool {
	for _, typeInfo := range o.Types {
		if len(typeInfo.ListFields()) > 0 {
			return true
		}
	}
	return false
}

// NeedsJSONValueMissing reports whether types.go's Validate checks a
// required any-JSON field (requiredAnyJSONField) or JSON array field
// (requiredStructuredJSONField), which call the jsonValueMissing helper.
func (o *ModuleOutput) NeedsJSONValueMissing() bool {
	for _, typeInfo := range o.Types {
		for _, field := range typeInfo.Fields {
			if requiredAnyJSONField(field) || requiredStructuredJSONField(field) {
				return true
			}
		}
	}
	return false
}

// ScalarValueCheck is one validate<Symbol>Value function in types.go: the
// scalar's own rules, checked before the scalar core's verdict is taken.
type ScalarValueCheck struct {
	Symbol string
	Name   string
	// Field is one field of the scalar, for validationStringExpr.
	Field FieldInfo
	Rules []codegen.ValidationRule
}

// ScalarValueChecks returns one ScalarValueCheck per scalar whose values
// Validate hands to the scalar and that has rules of its own, in symbol
// order.
func (o *ModuleOutput) ScalarValueChecks() []ScalarValueCheck {
	bySymbol := map[string]ScalarValueCheck{}
	for _, typeInfo := range o.Types {
		for _, field := range typeInfo.Fields {
			if len(field.ScalarRules) == 0 || field.ScalarInfo == nil {
				continue
			}
			symbol := scalarSymbol(field)
			if _, seen := bySymbol[symbol]; seen {
				continue
			}
			bySymbol[symbol] = ScalarValueCheck{Symbol: symbol, Name: field.ScalarInfo.Name, Field: field, Rules: field.ScalarRules}
		}
	}
	checks := make([]ScalarValueCheck, 0, len(bySymbol))
	for _, check := range bySymbol {
		checks = append(checks, check)
	}
	sort.Slice(checks, func(i, j int) bool { return checks[i].Symbol < checks[j].Symbol })
	return checks
}

func (o *ModuleOutput) filterImports(usedAliases map[string]bool) []ModuleImport {
	var imports []ModuleImport
	for _, imp := range o.Imports {
		if usedAliases[imp.Alias] {
			imports = append(imports, imp)
		}
	}
	return imports
}

// WriteTypes writes all generated module files into outputDir: go.mod,
// scalars.go, enums.go, types.go, unions.go, identity.go, README.md and the
// descriptors. Files whose content would be empty are removed when stale.
func WriteTypes(output *ModuleOutput, outputDir string) error {
	return WriteTypesWithProfile(output, outputDir, nil, false)
}

// WriteTypesWithProfile writes all generated module files with shared codegen profiling.
func WriteTypesWithProfile(output *ModuleOutput, outputDir string, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) error {
	if err := os.MkdirAll(outputDir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	generator := codegen.NewFileGenerator(
		templatesFS,
		templateFuncs(),
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)
	if err := generateFile(generator, "module.tmpl", filepath.Join(outputDir, "go.mod"), output); err != nil {
		return fmt.Errorf("failed to generate go.mod: %w", err)
	}

	conditionalFiles := []codegen.ConditionalFile{
		// scalars.go also carries the ValidationErrors aliases types.go's
		// Validate() references, so it must exist whenever types.go does --
		// a schema with types but no scalars previously generated
		// uncompilable Go (the go.mod requires superscalar unconditionally).
		{Condition: output.WritesScalars(), Template: "scalars.tmpl", Filename: "scalars.go"},
		{Condition: len(output.Enums) > 0 || len(output.ImportedEnums) > 0, Template: "enums.tmpl", Filename: "enums.go"},
		{Condition: len(output.Types) > 0 || len(output.ImportedTypes) > 0 || len(output.ImportedUnions) > 0, Template: "types.tmpl", Filename: "types.go"},
		{Condition: len(output.Unions) > 0, Template: "unions.tmpl", Filename: "unions.go"},
		{Condition: output.Identity != nil, Template: "identity.tmpl", Filename: "identity.go"},
	}

	if err := codegen.WriteConditionalFilesParallel(conditionalFiles, outputDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	}); err != nil {
		return err
	}

	if err := generateFile(generator, "readme.tmpl", filepath.Join(outputDir, "README.md"), output); err != nil {
		return fmt.Errorf("failed to generate README.md: %w", err)
	}

	if err := writeDescriptors(output.VersionGraphs, filepath.Join(outputDir, "versiongraph")); err != nil {
		return err
	}
	var identity []DescriptorFile
	if output.Identity != nil {
		identity = append(identity, *output.Identity)
	}
	return writeDescriptors(identity, filepath.Join(outputDir, "identity"))
}

// writeDescriptors writes each descriptor as <dir>/<name>.json and removes
// the directory's other descriptors (each *.json the schema no longer
// writes), so a graph or a User table the schema dropped leaves none
// behind. Any other file stays; the directory goes only when nothing is
// left in it.
func writeDescriptors(descriptors []DescriptorFile, dir string) error {
	keep := map[string]bool{}
	if len(descriptors) > 0 {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create %s: %w", dir, err)
		}
	}
	for _, descriptor := range descriptors {
		name := descriptor.FileName + ".json"
		keep[name] = true
		if err := os.WriteFile(filepath.Join(dir, name), descriptor.JSON, 0o644); err != nil {
			return fmt.Errorf("failed to write %s/%s: %w", filepath.Base(dir), name, err)
		}
	}
	stale, err := filepath.Glob(filepath.Join(dir, "*.json"))
	if err != nil {
		return err
	}
	for _, path := range stale {
		if !keep[filepath.Base(path)] {
			if err := os.Remove(path); err != nil {
				return fmt.Errorf("failed to remove stale %s: %w", path, err)
			}
		}
	}
	if len(descriptors) == 0 {
		if entries, err := os.ReadDir(dir); err == nil && len(entries) == 0 {
			if err := os.Remove(dir); err != nil {
				return fmt.Errorf("failed to remove the empty %s: %w", dir, err)
			}
		}
	}
	return nil
}

// generateFile generates a file from an embedded template, running .go
// outputs through gofmt.
func generateFile(generator *codegen.FileGenerator, templateName, outputPath string, data interface{}) error {
	return generator.GenerateFile(
		codegen.NewGoFileConfig(templatesFS, templateName, outputPath, data, templateFuncs()),
	)
}
