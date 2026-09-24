package typegen

import (
	"fmt"
	"os"
	"path/filepath"

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
			for _, rule := range field.Validations {
				if rule.Validator == "pattern" {
					return true
				}
			}
		}
	}
	return false
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
