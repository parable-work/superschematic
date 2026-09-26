package pygen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
)

// templateData wraps ModuleOutput with rendering-only flags that are derived
// at write time and must not mutate the canonical ModuleOutput struct.
type templateData struct {
	*ModuleOutput

	// NeedsScalarLib is true when scalars.py imports from the scalar library module.
	NeedsScalarLib bool

	// NeedsYAML is true when types.py is generated (uses _safe_load_yaml).
	NeedsYAML bool
}

// WriteTypes writes the generated Python package into outputDir: packaging
// files at the root and the module sources under the module directory.
func WriteTypes(output *ModuleOutput, outputDir string) error {
	hasTypes := len(output.Types) > 0 || len(output.ImportedTypes) > 0
	data := &templateData{
		ModuleOutput:   output,
		NeedsScalarLib: output.HasCustomNormalize || output.HasCustomValidate || output.HasCustomParse || output.HasJSONParse,
		NeedsYAML:      hasTypes,
	}

	moduleDir := filepath.Join(outputDir, output.PythonModuleName)
	if err := cleanOutputDir(outputDir, moduleDir); err != nil {
		return fmt.Errorf("failed to clean output directory: %w", err)
	}
	if err := os.MkdirAll(moduleDir, 0o755); err != nil {
		return fmt.Errorf("failed to create directory: %w", err)
	}

	rootFiles := []codegen.ConditionalFile{
		{Condition: true, Template: "setup.tmpl", Filename: "setup.py"},
		{Condition: true, Template: "pyproject.tmpl", Filename: "pyproject.toml"},
		{Condition: true, Template: "readme.tmpl", Filename: "README.md"},
		{Condition: true, Template: "python_version.tmpl", Filename: ".python-version"},
	}
	if err := codegen.WriteConditionalFiles(rootFiles, outputDir, func(templateName, outputPath string) error {
		return generateFile(templateName, outputPath, data, output)
	}); err != nil {
		return err
	}

	moduleFiles := []codegen.ConditionalFile{
		{Condition: true, Template: "package_init.tmpl", Filename: "__init__.py"},
		{Condition: true, Template: "validation_errors.tmpl", Filename: "validation_errors.py"},
		{Condition: len(output.Scalars) > 0, Template: "scalars.tmpl", Filename: "scalars.py"},
		{Condition: len(output.Enums) > 0, Template: "enums.tmpl", Filename: "enums.py"},
		{Condition: hasTypes, Template: "types.tmpl", Filename: "types.py"},
		{Condition: len(output.Unions) > 0, Template: "unions.tmpl", Filename: "unions.py"},
	}
	if err := codegen.WriteConditionalFiles(moduleFiles, moduleDir, func(templateName, outputPath string) error {
		return generateFile(templateName, outputPath, data, output)
	}); err != nil {
		return err
	}

	if err := os.WriteFile(filepath.Join(moduleDir, "py.typed"), []byte(""), 0o644); err != nil {
		return fmt.Errorf("failed to create py.typed marker: %w", err)
	}

	return nil
}

// cleanOutputDir removes stale generated module files before regeneration so
// renamed or deleted definitions do not leave orphans.
func cleanOutputDir(outputDir, moduleDir string) error {
	entries, err := os.ReadDir(outputDir)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to read output directory: %w", err)
	}
	for _, entry := range entries {
		path := filepath.Join(outputDir, entry.Name())
		if entry.IsDir() && path == moduleDir {
			if err := os.RemoveAll(path); err != nil && !os.IsNotExist(err) {
				return fmt.Errorf("failed to remove directory %s: %w", path, err)
			}
		}
	}
	return nil
}

// generateFile generates a file from an embedded template. Python outputs
// are run through a whitespace normalizer: template control-flow actions
// leave runs of blank lines behind, and there is no guaranteed external
// formatter (ruff) at generation time. Blank lines never affect Python
// block structure, so collapsing them is syntax-safe.
func generateFile(templateName, outputPath string, data interface{}, output *ModuleOutput) error {
	cfg := codegen.NewFileConfig(templatesFS, templateName, outputPath, data, customTemplateFuncs(output))
	if strings.HasSuffix(outputPath, ".py") {
		cfg.FormatOutput = normalizePyWhitespace
	}
	return codegen.GenerateFile(cfg)
}

// normalizePyWhitespace trims trailing whitespace, collapses runs of blank
// lines to a single blank line, and ensures exactly one trailing newline.
func normalizePyWhitespace(src []byte) ([]byte, error) {
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
	for len(out) > 0 && out[0] == "" {
		out = out[1:]
	}

	return []byte(strings.Join(out, "\n") + "\n"), nil
}
