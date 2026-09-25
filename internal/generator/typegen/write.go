package typegen

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"

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
// scalars.go, enums.go, types.go, unions.go, README.md. Files whose content
// would be empty are removed when stale.
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
	}

	if err := codegen.WriteConditionalFilesParallel(conditionalFiles, outputDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	}); err != nil {
		return err
	}

	if err := generateFile(generator, "readme.tmpl", filepath.Join(outputDir, "README.md"), output); err != nil {
		return fmt.Errorf("failed to generate README.md: %w", err)
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
