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

// WorkspaceRootManifest returns the package.json written at the directory
// that holds every generated TypeScript types package (the parent of each
// WriteTypes output directory). Every generated package is a workspace of
// this root, so `bun install` in the root or in any package installs the
// whole tree into one lockfile at the root.
//
// A package depends on the scalar library with a file: spec relative to the
// package, which can point outside the output tree; Bun follows such a path
// only for the root or one of its workspaces. A package depends on a sibling
// types package with workspace:*, not file:../<schema>. With a file: spec on
// a sibling, Bun 1.4.0 re-resolves the tree on every install after the
// first, and Bun 1.4.2 on the install after a manifest gains such a spec;
// that pass reads the scalar path the lockfile stores relative to the root
// as if it were relative to the member, and the install fails.
//
// The root is named <npm_scope>/types-workspace. Every types package name
// ends in -types (Naming.NpmTypesPackage), so the root cannot collide with
// one of its workspaces.
func WorkspaceRootManifest(n naming.Naming) string {
	return fmt.Sprintf(`{
  "name": %q,
  "private": true,
  "workspaces": [
    "*"
  ]
}
`, n.OrDefault().NpmScope+"/types-workspace")
}

// WriteWorkspaceRoot writes WorkspaceRootManifest into typesRoot. The write
// is atomic and skipped when the file already has the same content, so
// schema builds that run in parallel can each call it for their own package.
func WriteWorkspaceRoot(typesRoot string, n naming.Naming) error {
	manifest := WorkspaceRootManifest(n)
	if err := os.MkdirAll(typesRoot, 0o755); err != nil {
		return fmt.Errorf("failed to create types root %s: %w", typesRoot, err)
	}
	target := filepath.Join(typesRoot, "package.json")
	if existing, err := os.ReadFile(target); err == nil && string(existing) == manifest {
		return nil
	}
	tmp, err := os.CreateTemp(typesRoot, ".package.json-*")
	if err != nil {
		return fmt.Errorf("failed to stage workspace manifest: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(manifest); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to write workspace manifest: %w", err)
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to close workspace manifest: %w", err)
	}
	if err := os.Chmod(tmpName, 0o644); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to set workspace manifest mode: %w", err)
	}
	if err := os.Rename(tmpName, target); err != nil {
		_ = os.Remove(tmpName)
		return fmt.Errorf("failed to publish workspace manifest: %w", err)
	}
	return nil
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
				ParserNestedTypes []string
				ParserScalarsUsed []ScalarInfo
				NeedsJSONParse    bool
				Naming            naming.Naming
			}{
				Naming:            output.Naming,
				Type:              t,
				ScalarsUsed:       typeScalarsUsed(t),
				EnumsUsed:         typeEnumNames(t, allEnums),
				LocalEnumsUsed:    typeEnumNames(t, output.Enums),
				ImportedEnumsUsed: typeImportedEnumsUsed(t, output.ImportedTypes),
				EnumDefaultsUsed:  typeEnumDefaultsUsed(t, allEnums),
				ParserNestedTypes: typeParserNestedTypes(t, generatedTypeNames),
				ParserScalarsUsed: typeParserScalarsUsed(t),
				NeedsJSONParse:    typeNeedsJSONParse(t, generatedTypeNames),
			}
			if err := generateFile(generator, "validator_type.tmpl", outPath, data); err != nil {
				return fmt.Errorf("failed to generate %s: %w", outPath, err)
			}
			return nil
		})
	}
	if err := codegen.RunParallel(validatorTypeTasks); err != nil {
		return err
	}

	validatorFiles := []codegen.ConditionalFile{
		{Condition: hasScalars, Template: "validators_scalars_index.tmpl", Filename: filepath.Join("scalars", "index.ts")},
		{Condition: hasEnums, Template: "validator_enums.tmpl", Filename: "enums.ts"},
		{Condition: len(output.Types) > 0, Template: "validators_types_index.tmpl", Filename: filepath.Join("types", "index.ts")},
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
	return codegen.WriteConditionalFilesParallel(maskFiles, maskDir, func(templateName, outputPath string) error {
		return generateFile(generator, templateName, outputPath, output)
	})
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
