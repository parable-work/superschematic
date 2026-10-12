package rustgen

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/codegen"
	"github.com/parable-work/superschematic/internal/generator/naming"
)

// WriteTypes writes the generated Rust crate into outputDir: Cargo.toml and
// README.md at the root, module sources under src/.
func WriteTypes(output *ModuleOutput, outputDir string) error {
	srcDir := filepath.Join(outputDir, "src")
	if err := os.RemoveAll(srcDir); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("failed to clean src directory: %w", err)
	}
	if err := os.MkdirAll(srcDir, 0o755); err != nil {
		return fmt.Errorf("failed to create src directory: %w", err)
	}

	hasTypes := len(output.Types) > 0 || len(output.ImportedTypes) > 0

	rootFiles := []codegen.ConditionalFile{
		{Condition: true, Template: "cargo.tmpl", Filename: "Cargo.toml"},
		{Condition: true, Template: "readme.tmpl", Filename: "README.md"},
	}
	if err := codegen.WriteConditionalFiles(rootFiles, outputDir, func(templateName, outputPath string) error {
		return generateFile(templateName, outputPath, output)
	}); err != nil {
		return err
	}
	if err := writeRuntimeSchemas(output.RuntimeSchemas, outputDir); err != nil {
		return err
	}

	srcFiles := []codegen.ConditionalFile{
		{Condition: true, Template: "lib.tmpl", Filename: "lib.rs"},
		{Condition: len(output.Scalars) > 0, Template: "scalars.tmpl", Filename: "scalars.rs"},
		{Condition: len(output.Enums) > 0, Template: "enums.tmpl", Filename: "enums.rs"},
		{Condition: hasTypes, Template: "types.tmpl", Filename: "types.rs"},
		{Condition: len(output.Unions) > 0, Template: "unions.tmpl", Filename: "unions.rs"},
		{Condition: output.Validators != nil, Template: "validators.tmpl", Filename: "validators.rs"},
		{Condition: output.IdentityDescriptor != "", Template: "identity.tmpl", Filename: "identity.rs"},
	}
	if err := codegen.WriteConditionalFiles(srcFiles, srcDir, func(templateName, outputPath string) error {
		return generateFile(templateName, outputPath, output)
	}); err != nil {
		return err
	}
	for _, graph := range output.VersionGraphs {
		data := versionGraphFile{Graph: graph, Naming: output.Naming}
		if err := generateFile("versiongraph.tmpl", filepath.Join(srcDir, versionGraphFileName(graph)), data); err != nil {
			return err
		}
	}
	return nil
}

// versionGraphFile is what versiongraph.tmpl renders: one graph and the
// naming its crate coordinates come from.
type versionGraphFile struct {
	Graph  VersionGraphInfo
	Naming naming.Naming
}

// generateFile generates a file from an embedded template. Rust sources are
// run through a whitespace normalizer: template control flow leaves runs of
// blank lines behind, and rustfmt is not guaranteed to be installed at
// generation time. Blank lines never affect Rust syntax, so collapsing them
// is safe.
func generateFile(templateName, outputPath string, output any) error {
	cfg := codegen.NewFileConfig(templatesFS, templateName, outputPath, output, customTemplateFuncs())
	if strings.HasSuffix(outputPath, ".rs") {
		cfg.FormatOutput = normalizeRustWhitespace
	}
	return codegen.GenerateFile(cfg)
}

// normalizeRustWhitespace trims trailing whitespace, collapses runs of blank
// lines to a single blank line, and ensures exactly one trailing newline.
func normalizeRustWhitespace(src []byte) ([]byte, error) {
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
