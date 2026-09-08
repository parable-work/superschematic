// Package cli is the psgen command line as a library. A binary is one call:
//
//	cli.New(cli.Config{}).Execute()
//
// links the core alone; cli.New(cli.Config{}, ext.Extension{}) links an
// extension. Every command assembles its registry per invocation from the
// naming file it resolves (registry.Assemble with the extensions given
// here), so the same command tree serves a core-only binary and one that
// carries extensions.
//
// Design: docs/extension-model.md section 3.9.
package cli

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/registry"
)

// Config sets what a binary says about itself. The zero value is the psgen
// core.
type Config struct {
	// Name is the command name in usage text. Empty means "psgen".
	Name string
	// Short and Long replace the root command's descriptions when set.
	Short string
	Long  string
}

// CommandProvider is the optional interface an extension implements to
// contribute subcommands. The registry is assembled per command from the
// naming file that command resolves, so subcommands cannot come from it;
// they hang off the extension value itself and are added to the root at
// New.
type CommandProvider interface {
	Commands() []*cobra.Command
}

// app is what every command closes over: the extensions to link when it
// assembles its registry.
type app struct {
	exts []registry.Extension
}

// New builds the root command with build, build-all, json-schema and format,
// plus the subcommands of any extension that implements CommandProvider.
func New(cfg Config, exts ...registry.Extension) *cobra.Command {
	name := cfg.Name
	if name == "" {
		name = "psgen"
	}
	short := cfg.Short
	if short == "" {
		short = "Generate code from TypeScript/JSON/YAML schemas"
	}
	long := cfg.Long
	if long == "" {
		long = fmt.Sprintf(`%s reads schema service directories (schema.config.{ts,json,yaml} plus
src/*.schema.{ts,json,yaml}) into the Schema IR and runs the generators the
schema's kind and the config's outputs block select. Names and paths come from
superschematic.toml at the schemas root; extensions linked into the binary add
kinds, decorators, documents, generators, auth providers and subcommands.`, name)
	}
	a := &app{exts: exts}
	root := &cobra.Command{
		Use:           name,
		Short:         short,
		Long:          long,
		SilenceUsage:  true,
		SilenceErrors: true,
	}
	root.AddCommand(newBuildCmd(a))
	root.AddCommand(newBuildAllCmd(a))
	root.AddCommand(newJSONSchemaCmd(a))
	root.AddCommand(newFormatCmd())
	for _, ext := range exts {
		if provider, ok := ext.(CommandProvider); ok {
			root.AddCommand(provider.Commands()...)
		}
	}
	return root
}

// resolveRegistry assembles the registry a command threads through the
// loader and generators: the core kinds and generators, the extensions the
// binary carries, then Finalize (which rejects a naming file whose
// auth_provider names a provider no linked extension registered).
func (a *app) resolveRegistry(names registry.Naming) (*registry.Registry, error) {
	return registry.Assemble(names, a.exts...)
}
