package cli

import (
	"bytes"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/engineclientgen"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/loader"
	ir "github.com/parable-work/superschematic/ir"
)

// engineClientFlags holds one engine-client command's flag values.
type engineClientFlags struct {
	out        string
	check      bool
	namingPath string
}

// newEngineClientCmd writes the typed wrappers over the engine's client
// for engine schemas (D49). It is a command of its own, not a build
// target: an engine schema is one document with no service config, and a
// module types several schemas that link to each other.
func newEngineClientCmd(a *app) *cobra.Command {
	flags := &engineClientFlags{}
	cmd := &cobra.Command{
		Use:   "engine-client --out <file.ts> <schema file>...",
		Short: "Write a TypeScript module of typed wrappers over the engine's client for engine schemas",
		Long: `engine-client writes one TypeScript module that types the engine's client
(@superschematic/engine/client) for the schemas given: per schema, the
instance with its own fields and its behaviors' fields, the create input
with each behavior's create parameters, the update patch, each operation's
parameters and result, the preconditions and the veto codes, as the
behaviors' configs narrow them (a Workflow's states, Links' names), and a
wrapper whose methods call the client with them.

Each file is one schema document as the engine takes it: kind General, a
name, no imports. A .schema.json or .schema.yaml file is read on its own; a
.schema.ts file is read in the context of its service (the directory
holding schema.config.*), as format reads it. The behaviors' declarations
are this binary's, so an extension's behavior is typed by its declaration.

The module imports the client from the engine's npm package, the naming
key engine_npm_package. Names come from the superschematic.toml found by
walking up from the first file, or --naming.

With --check nothing is written: the command fails when the file is not
what it would write, so CI keeps a committed module current.

Examples:
  superschematic engine-client --out src/notes.client.ts schemas/notes.schema.json
  superschematic engine-client --out src/jobs.client.ts schemas/batches.schema.json schemas/jobs.schema.json schemas/workers.schema.json
  superschematic engine-client --out src/notes.client.ts --check schemas/notes.schema.json`,
		Args: cobra.MinimumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runEngineClient(cmd, a, flags, args)
		},
	}
	cmd.Flags().StringVar(&flags.out, "out", "", "the TypeScript file the module is written to (required)")
	cmd.Flags().BoolVar(&flags.check, "check", false, "write nothing; fail when the file is not what would be written")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default: the superschematic.toml found by walking up from the first schema file)")
	_ = cmd.MarkFlagRequired("out")
	return cmd
}

func runEngineClient(cmd *cobra.Command, a *app, flags *engineClientFlags, paths []string) error {
	var names naming.Naming
	var err error
	if flags.namingPath != "" {
		names, err = naming.LoadFile(flags.namingPath)
	} else {
		names, err = naming.Discover(paths[0])
	}
	if err != nil {
		return err
	}
	naming.SetActive(names)
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	schemas := make([]*ir.Schema, 0, len(paths))
	for _, path := range paths {
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			return fmt.Errorf("schema file not found: %s", path)
		}
		format, _, err := schemaFileFormat(path)
		if err != nil {
			return err
		}
		doc, err := readDocument(path, format, reg)
		if err != nil {
			return err
		}
		schema, err := loader.LoadDocument(doc, filepath.Base(path), loader.WithRegistry(reg), loader.WithNaming(names))
		if err != nil {
			return err
		}
		schemas = append(schemas, schema)
	}
	module, err := engineclientgen.Generate(schemas, engineclientgen.Options{Registry: reg, Naming: names})
	if err != nil {
		return err
	}
	existing, err := os.ReadFile(flags.out)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	if flags.check {
		if err != nil {
			return fmt.Errorf("engine-client: %s is missing; run: %s", flags.out, rerunEngineClient(cmd, flags, paths))
		}
		if !bytes.Equal(existing, module) {
			return fmt.Errorf("engine-client: %s is not what the schemas generate; run: %s", flags.out, rerunEngineClient(cmd, flags, paths))
		}
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "engine-client: %s is current\n", flags.out)
		return nil
	}
	if bytes.Equal(existing, module) {
		_, _ = fmt.Fprintf(cmd.OutOrStdout(), "engine-client: %s is current\n", flags.out)
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(flags.out), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(flags.out, module, 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(cmd.OutOrStdout(), "engine-client: wrote %s (%d schema(s))\n", flags.out, len(schemas))
	return nil
}

func rerunEngineClient(cmd *cobra.Command, flags *engineClientFlags, paths []string) string {
	parts := []string{cmd.Root().Name(), "engine-client", "--out", flags.out}
	if flags.namingPath != "" {
		parts = append(parts, "--naming", flags.namingPath)
	}
	return strings.Join(append(parts, paths...), " ")
}
