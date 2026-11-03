package cli

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/docs"
	"github.com/parable-work/superschematic/internal/agentcatalog"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/packages"
)

// catalogFlags holds one catalog command's flag values.
type catalogFlags struct {
	namingPath string
}

// newCatalogCmd prints a Markdown reference of what the binary knows, for
// whoever authors schemas with it: a person, or a coding agent that has no
// copy of this repository. Each is written from the registry the binary
// assembles, so it lists an extension's scalars, decorators and behaviors
// beside the core's.
func newCatalogCmd(a *app) *cobra.Command {
	flags := &catalogFlags{}
	cmd := &cobra.Command{
		Use:   "catalog " + strings.Join(agentcatalog.Names(), "|"),
		Short: "Print a Markdown reference of the scalars, decorators, behaviors or capabilities this binary knows",
		Long: `catalog prints a Markdown reference written from what this binary links,
its extensions included:

  scalars       every scalar a field can have: what it checks, its SQL type
                and its type in Go, TypeScript, Python and Rust
  decorators    every decorator, with its TypeScript declaration, where it may
                sit and the kinds that may use it, and the type wrappers
  behaviors     every engine behavior, its config, and the fields and
                operations it adds to a type
  capabilities  each thing a backend needs, the declaration that gives it,
                what the build generates and what is left to write

A coding agent reads these in place of documentation that could disagree
with the binary it runs.`,
		Args:      cobra.ExactArgs(1),
		ValidArgs: agentcatalog.Names(),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runCatalog(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default: built-in names), for a binary whose extensions read it")
	return cmd
}

func runCatalog(cmd *cobra.Command, a *app, flags *catalogFlags, name string) error {
	names := naming.Default()
	if flags.namingPath != "" {
		var err error
		if names, err = resolveNaming(flags.namingPath, ""); err != nil {
			return err
		}
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	out, err := agentcatalog.Render(name, reg, agentcatalog.Sources{
		Packages:      packages.Sources,
		CapabilityMap: docs.CapabilityMap,
	})
	if err != nil {
		return fmt.Errorf("catalog: %w", err)
	}
	_, _ = cmd.OutOrStdout().Write(out)
	return nil
}
