package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader/jsonreader"
	"github.com/parable-work/superschematic/internal/loader/schemafile"
	"github.com/parable-work/superschematic/internal/loader/tsreader"
	"github.com/parable-work/superschematic/internal/loader/yamlreader"
	"github.com/parable-work/superschematic/internal/writer"
)

// formatFlags holds one format command's flag values.
type formatFlags struct {
	to     string
	stdout bool
	force  bool
}

// newFormatCmd converts one schema file between the three authoring formats.
func newFormatCmd() *cobra.Command {
	flags := &formatFlags{}
	cmd := &cobra.Command{
		Use:   "format --to=ts|json|yaml <file>",
		Short: "Convert a schema file between the TypeScript, JSON, and YAML formats",
		Long: `Format converts one schema file to another authoring format through the
Schema IR: the file is read by its format's frontend and written back out by
the target format's writer, preserving definitions and comment metadata.

JSON and YAML files convert standalone. A TypeScript file is read in the
context of its service (the directory holding schema.config.*), since
decorators and types resolve through the compiler; definitions that belong
to the file convert, and service-level definitions that TypeScript cannot
attribute to a file (enums, scalars) ride along with the service's first
schema file.

The converted file is written next to the input as <name>.schema.<format>.
An existing file is not overwritten without --force. Use --stdout to print
the conversion instead of writing it.

Examples:
  psgen format --to=yaml ./src/tenant.schema.json
  psgen format --to=ts ./src/tenant.schema.yaml --stdout
  psgen format --to=json ./src/tenant.schema.ts --force`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runFormat(cmd, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.to, "to", "", "target format: ts, json, or yaml (required)")
	cmd.Flags().BoolVar(&flags.stdout, "stdout", false, "print the conversion instead of writing a file")
	cmd.Flags().BoolVar(&flags.force, "force", false, "overwrite an existing output file")
	_ = cmd.MarkFlagRequired("to")
	return cmd
}

// schemaFileFormat detects a schema file's format from its extension.
func schemaFileFormat(path string) (writer.Format, string, error) {
	for _, candidate := range []struct {
		ext    string
		format writer.Format
	}{
		{".schema.ts", writer.FormatTS},
		{".schema.json", writer.FormatJSON},
		{".schema.yaml", writer.FormatYAML},
		{".schema.yml", writer.FormatYAML},
	} {
		if strings.HasSuffix(path, candidate.ext) {
			return candidate.format, candidate.ext, nil
		}
	}
	return "", "", fmt.Errorf("%s is not a schema file: expected a .schema.{ts,json,yaml} extension", path)
}

func runFormat(cmd *cobra.Command, flags *formatFlags, inputPath string) error {
	target, err := writer.ParseFormat(flags.to)
	if err != nil {
		return err
	}
	if info, err := os.Stat(inputPath); err != nil || info.IsDir() {
		return fmt.Errorf("schema file not found: %s", inputPath)
	}
	inputFormat, inputExt, err := schemaFileFormat(inputPath)
	if err != nil {
		return err
	}
	if inputFormat == target {
		return fmt.Errorf("%s is already in the %s format", inputPath, target)
	}

	// The writer names service packages through naming.Active; the config
	// file sits at the schemas root above the file being converted.
	names, err := naming.Discover(inputPath)
	if err != nil {
		return err
	}
	naming.SetActive(names)

	doc, err := readDocument(inputPath, inputFormat)
	if err != nil {
		return err
	}
	out, err := writer.Write(doc, target)
	if err != nil {
		return fmt.Errorf("converting %s to %s: %w", inputPath, target, err)
	}

	if flags.stdout {
		_, _ = fmt.Fprint(cmd.OutOrStdout(), string(out))
		return nil
	}

	outPath := strings.TrimSuffix(inputPath, inputExt) + target.Extension()
	if _, err := os.Stat(outPath); err == nil && !flags.force {
		return fmt.Errorf("%s already exists; pass --force to overwrite", outPath)
	}
	if err := os.WriteFile(outPath, out, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "Wrote %s\n", outPath)
	return nil
}

// readDocument reads one schema file into its per-file document.
func readDocument(path string, format writer.Format) (*schemafile.Document, error) {
	switch format {
	case writer.FormatJSON:
		return jsonreader.ReadFile(path, filepath.Base(path))
	case writer.FormatYAML:
		return yamlreader.ReadFile(path, filepath.Base(path))
	default:
		return readTSDocument(path)
	}
}

// readTSDocument loads the service containing a TypeScript schema file and
// extracts the document owned by that file. TypeScript files cannot be read
// standalone: decorators, wrappers, and type references resolve through the
// service's compiler program.
func readTSDocument(path string) (*schemafile.Document, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil, err
	}
	root, err := findServiceRoot(filepath.Dir(abs))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	schema, _, err := tsreader.LoadService(root)
	if err != nil {
		return nil, err
	}

	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return nil, err
	}
	base := strings.TrimSuffix(filepath.ToSlash(rel), ".schema.ts") + ".schema"

	docs := writer.SplitSchema(schema)
	doc, ok := docs[base]
	if !ok {
		return nil, fmt.Errorf("%s declares no schema definitions", path)
	}
	return doc, nil
}

// findServiceRoot walks up from a directory to the schema service root: the
// nearest ancestor holding a schema.config.{ts,json,yaml}.
func findServiceRoot(dir string) (string, error) {
	for {
		for _, name := range []string{"schema.config.ts", "schema.config.json", "schema.config.yaml"} {
			if _, err := os.Stat(filepath.Join(dir, name)); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("no enclosing schema service found (missing schema.config.{ts,json,yaml})")
		}
		dir = parent
	}
}
