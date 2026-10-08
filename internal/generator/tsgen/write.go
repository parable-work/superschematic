package tsgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/profile"
)

// cleanOutputDir removes stale generated files and directories before
// regeneration, so renamed or deleted definitions do not leave orphans.
func cleanOutputDir(outputDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read output directory: %w", err)
	}
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".ts") {
			path := filepath.Join(outputDir, entry.Name())
			if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove stale file %s: %w", path, err)
			}
		}
	}

	generatedDirs := []string{
		filepath.Join(outputDir, "types"),
		filepath.Join(outputDir, "validators"),
		filepath.Join(outputDir, "mask"),
		filepath.Join(outputDir, "versiongraph"),
		filepath.Join(outputDir, "dist"),
	}
	for _, dir := range generatedDirs {
		if err := os.RemoveAll(dir); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("failed to remove directory %s: %w", dir, err)
		}
	}

	return nil
}

// createDirs creates all required output directories.
func createDirs(outputDir string) error {
	dirs := []string{
		outputDir,
		filepath.Join(outputDir, "types"),
		filepath.Join(outputDir, "validators", "scalars"),
		filepath.Join(outputDir, "validators", "types"),
		filepath.Join(outputDir, "mask", "types"),
	}
	for _, dir := range dirs {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("failed to create directory %s: %w", dir, err)
		}
	}
	return nil
}

// typeFileName returns the lowercase file stem for a generated per-type file.
func typeFileName(name string) string {
	return strings.ToLower(name)
}

// WriteTypes writes the generated TypeScript package into outputDir using the
// split structure: types/ (pure types), validators/ (per-scalar and
// per-type), and mask/ (secret-masking helpers).
func WriteTypes(output *ModuleOutput, outputDir string) error {
	return WriteTypesWithProfile(output, outputDir, nil, false)
}

// WriteTypesWithProfile writes the generated TypeScript package with shared codegen profiling.
func WriteTypesWithProfile(output *ModuleOutput, outputDir string, prof *profile.Profiler, skipFormat bool, phasePrefixes ...string) error {
	if err := cleanOutputDir(outputDir); err != nil {
		return fmt.Errorf("failed to clean output directory: %w", err)
	}
	if err := createDirs(outputDir); err != nil {
		return err
	}

	generator := codegen.NewFileGenerator(
		templatesFS,
		customTemplateFuncs(),
		codegen.WithProfiler(prof, phasePrefixes...),
		codegen.WithSkipFormat(skipFormat),
	)
	typesDir := filepath.Join(outputDir, "types")
	validatorsDir := filepath.Join(outputDir, "validators")
	maskDir := filepath.Join(outputDir, "mask")

	hasScalars := len(output.Scalars) > 0
	hasEnums := len(output.Enums) > 0
	hasTypes := len(output.Types) > 0 || len(output.ImportedTypes) > 0
	hasUnions := len(output.Unions) > 0

	generatedTypeNames := make(map[string]bool, len(output.Types))
	for _, t := range output.Types {
		generatedTypeNames[t.Name] = true
	}
	// A nested object whose type a dependency declares is validated and
	// parsed as a local one is (D14, amended), with the dependency
	// package's validate<Type> and parse<Type>FromJSON.
	nestedTypeNames := make(map[string]bool, len(generatedTypeNames))
	nestedModules := make(map[string]string, len(output.ImportedTypes))
	for name := range generatedTypeNames {
		nestedTypeNames[name] = true
	}
	for _, imported := range output.ImportedTypes {
		if imported.IsObject && imported.HasValidators && !generatedTypeNames[imported.Name] {
			nestedTypeNames[imported.Name] = true
			nestedModules[imported.Name] = imported.ImportPackage + "/validators"
		}
	}
	allEnums := append([]codegen.EnumInfo{}, output.Enums...)
	for _, imported := range output.ImportedTypes {
		if imported.IsEnum {
			allEnums = append(allEnums, codegen.EnumInfo{Name: imported.Name})
		}
	}

	rootFiles := []codegen.ConditionalFile{
		{Condition: true, Template: "package.tmpl", Filename: "package.json"},
		{Condition: true, Template: "tsconfig.tmpl", Filename: "tsconfig.json"},
		{Condition: true, Template: "index.tmpl", Filename: "index.ts"},
		{Condition: true, Template: "readme.tmpl", Filename: "README.md"},
	}
	if err := codegen.WriteConditionalFilesParallel(rootFiles, outputDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	}); err != nil {
		return err
	}

	typesFiles := []codegen.ConditionalFile{
		{Condition: hasScalars, Template: "types_scalars.tmpl", Filename: "scalars.ts"},
		{Condition: hasEnums, Template: "types_enums.tmpl", Filename: "enums.ts"},
		{Condition: hasTypes, Template: "types_types.tmpl", Filename: "types.ts"},
		{Condition: hasUnions, Template: "types_unions.tmpl", Filename: "unions.ts"},
		{Condition: true, Template: "types_index.tmpl", Filename: "index.ts"},
	}
	if err := codegen.WriteConditionalFilesParallel(typesFiles, typesDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	}); err != nil {
		return err
	}

	scalarTasks := make([]func() error, 0, len(output.Scalars))
	for i := range output.Scalars {
		scalar := &output.Scalars[i]
		scalarTasks = append(scalarTasks, func() error {
			outPath := filepath.Join(validatorsDir, "scalars", scalar.Module+".ts")
			data := struct {
				Scalar *ScalarInfo
				Module *ModuleOutput
			}{Scalar: scalar, Module: output}
			if err := generateFile(generator, "validator_scalar.tmpl", outPath, data); err != nil {
				return fmt.Errorf("failed to generate %s: %w", outPath, err)
			}
			return nil
		})
	}
	if err := codegen.RunParallel(scalarTasks); err != nil {
		return err
	}

	validatorTypeTasks := make([]func() error, 0, len(output.Types))
	for i := range output.Types {
		t := &output.Types[i]
		validatorTypeTasks = append(validatorTypeTasks, func() error {
			outPath := filepath.Join(validatorsDir, "types", typeFileName(t.Name)+".ts")
			data := struct {
				Type              *TypeInfo
				ScalarsUsed       []ScalarInfo
				EnumsUsed         []string
				LocalEnumsUsed    []string
				ImportedEnumsUsed []ImportedTypeInfo
				EnumDefaultsUsed  []string
				// ScalarDefaultCasts are the scalar symbols @default
				// literals are cast to.
				ScalarDefaultCasts []string
				ParserNestedTypes  []string
				// ParserNestedImports are where each ParserNestedTypes
				// entry's validator and parser come from: the local
				// type's file, or a dependency package's validators.
				ParserNestedImports []NestedImport
				ParserScalarsUsed   []ScalarInfo
				NeedsJSONParse      bool
				// ValidatesNestedObjects is true when validate<Type>
				// validates a nested object field and reports its errors
				// with addNestedErrors.
				ValidatesNestedObjects bool
				// PrimitiveHelpers are the validators/primitives.ts
				// helpers validate<Type> imports.
				PrimitiveHelpers []string
				Naming           naming.Naming
			}{
				Naming:             output.Naming,
				Type:               t,
				ScalarsUsed:        typeScalarsUsed(t),
				EnumsUsed:          typeEnumNames(t, allEnums),
				LocalEnumsUsed:     typeEnumNames(t, output.Enums),
				ImportedEnumsUsed:  typeImportedEnumsUsed(t, output.ImportedTypes),
				EnumDefaultsUsed:   typeEnumDefaultsUsed(t, allEnums),
				ScalarDefaultCasts: typeScalarDefaultCasts(t),
				ParserNestedTypes:  typeParserNestedTypes(t, nestedTypeNames),
				ParserScalarsUsed:  typeParserScalarsUsed(t),
				NeedsJSONParse:     typeNeedsJSONParse(t, nestedTypeNames),
				PrimitiveHelpers:   typePrimitiveHelpers(t),
			}
			data.ParserNestedImports = nestedImports(data.ParserNestedTypes, nestedModules)
			data.ValidatesNestedObjects = typeValidatesNestedObjects(t, data.ParserNestedTypes)
			if err := generateFile(generator, "validator_type.tmpl", outPath, data); err != nil {
				return fmt.Errorf("failed to generate %s: %w", outPath, err)
			}
			return nil
		})
	}
	if err := codegen.RunParallel(validatorTypeTasks); err != nil {
		return err
	}

	hasPrimitiveHelpers := false
	for i := range output.Types {
		if len(typePrimitiveHelpers(&output.Types[i])) > 0 {
			hasPrimitiveHelpers = true
			break
		}
	}
	validatorFiles := []codegen.ConditionalFile{
		{Condition: hasPrimitiveHelpers, Template: "validator_primitives.tmpl", Filename: "primitives.ts"},
		{Condition: hasScalars, Template: "validators_scalars_index.tmpl", Filename: filepath.Join("scalars", "index.ts")},
		{Condition: hasEnums, Template: "validator_enums.tmpl", Filename: "enums.ts"},
		{Condition: len(output.Types) > 0, Template: "validators_types_index.tmpl", Filename: filepath.Join("types", "index.ts")},
		{Condition: len(output.Types) > 0, Template: "validator_errors.tmpl", Filename: "errors.ts"},
		{Condition: true, Template: "validators_index.tmpl", Filename: "index.ts"},
	}
	if err := codegen.WriteConditionalFilesParallel(validatorFiles, validatorsDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	}); err != nil {
		return err
	}

	maskTypeTasks := make([]func() error, 0, len(output.Types))
	for i := range output.Types {
		t := &output.Types[i]
		maskTypeTasks = append(maskTypeTasks, func() error {
			outPath := filepath.Join(maskDir, "types", typeFileName(t.Name)+".ts")
			data := struct {
				Type            *TypeInfo
				TypeImports     []string
				MaskTypesUsed   []string
				MaskableTypeSet map[string]bool
			}{
				Type:            t,
				TypeImports:     typeMaskTypeImports(t, generatedTypeNames),
				MaskTypesUsed:   typeMaskTypesUsed(t, generatedTypeNames),
				MaskableTypeSet: generatedTypeNames,
			}
			if err := generateFile(generator, "mask_type.tmpl", outPath, data); err != nil {
				return fmt.Errorf("failed to generate %s: %w", outPath, err)
			}
			return nil
		})
	}
	if err := codegen.RunParallel(maskTypeTasks); err != nil {
		return err
	}

	maskFiles := []codegen.ConditionalFile{
		{Condition: len(output.Types) > 0, Template: "mask_types_index.tmpl", Filename: filepath.Join("types", "index.ts")},
		{Condition: true, Template: "mask_index.tmpl", Filename: "index.ts"},
	}
	if err := codegen.WriteConditionalFilesParallel(maskFiles, maskDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	}); err != nil {
		return err
	}

	return writeVersionGraphs(generator, output, filepath.Join(outputDir, "versiongraph"))
}

// writeVersionGraphs writes each graph's facade, versiongraph/<name>.ts,
// and versiongraph/index.ts, which exports them. A schema without a graph
// gets no versiongraph directory.
func writeVersionGraphs(generator *codegen.FileGenerator, output *ModuleOutput, dir string) error {
	if len(output.VersionGraphs) == 0 {
		return nil
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}
	tasks := make([]func() error, 0, len(output.VersionGraphs)+1)
	for i := range output.VersionGraphs {
		graph := &output.VersionGraphs[i]
		tasks = append(tasks, func() error {
			outPath := filepath.Join(dir, graph.FileName+".ts")
			data := struct {
				Graph  *VersionGraphInfo
				Naming naming.Naming
			}{Graph: graph, Naming: output.Naming}
			if err := generateFile(generator, "versiongraph_graph.tmpl", outPath, data); err != nil {
				return fmt.Errorf("failed to generate %s: %w", outPath, err)
			}
			return nil
		})
	}
	tasks = append(tasks, func() error {
		return generateFile(generator, "versiongraph_index.tmpl", filepath.Join(dir, "index.ts"), output)
	})
	return codegen.RunParallel(tasks)
}

// generateFile generates a file from an embedded template. TypeScript
// outputs are run through a whitespace normalizer: template control-flow
// actions leave runs of blank lines behind, and unlike Go output (gofmt)
// there is no guaranteed external formatter at generation time.
func generateFile(generator *codegen.FileGenerator, templateName, outputPath string, data interface{}) error {
	cfg := codegen.NewFileConfig(templatesFS, templateName, outputPath, data, customTemplateFuncs())
	cfg.OutputDirExists = true
	if strings.HasSuffix(outputPath, ".ts") {
		cfg.FormatOutput = normalizeTSWhitespace
		cfg.FormatProfileKind = "typescript"
	}
	return generator.GenerateFile(cfg)
}

// normalizeTSWhitespace trims trailing whitespace, collapses runs of blank
// lines to a single blank line, and ensures exactly one trailing newline.
func normalizeTSWhitespace(src []byte) ([]byte, error) {
	lines := strings.Split(string(src), "\n")
	var out []string
	blankRun := 0
	for _, line := range lines {
		trimmed := strings.TrimRight(line, " \t")
		if trimmed == "" {
			blankRun++
			if blankRun > 1 {
				continue
			}
		} else {
			blankRun = 0
		}
		out = append(out, trimmed)
	}

	for len(out) > 0 && out[len(out)-1] == "" {
		out = out[:len(out)-1]
	}
	// Drop a leading blank line left behind by template headers.
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}

	return []byte(strings.Join(out, "\n") + "\n"), nil
}
