package ext

import (
	"fmt"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/registry"
)

// describeCommand is the subcommand the extension contributes through
// cli.CommandProvider. The core assembles its registry per command from the
// naming file each command resolves, so a subcommand that needs the
// registry assembles its own the same way: registry.Assemble with the
// extension. describe prints what the binary knows, which is the first
// thing to check when a schema is rejected.
func describeCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "describe [<schemas-root>]",
		Short: "List the kinds, generators, documents, auth providers and tool invocation policy this binary registers",
		Long: `describe assembles the registry the way build does, with the naming file at
<schemas-root>/superschematic.toml (or the defaults when no root is given),
and prints every kind with its generator pipeline, every document, every
output key, every auth provider and the tool invocation policy.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			names := registry.DefaultNaming()
			if len(args) == 1 {
				loaded, err := registry.LoadNaming(args[0])
				if err != nil {
					return err
				}
				names = loaded
			}
			reg, err := registry.Assemble(names, Extension{})
			if err != nil {
				return err
			}
			out := cmd.OutOrStdout()
			_, _ = fmt.Fprintf(out, "extensions: %s\n", strings.Join(reg.Extensions(), ", "))
			_, _ = fmt.Fprintf(out, "kinds: %s\n", strings.Join(reg.Kinds(), ", "))
			for _, kind := range reg.Kinds() {
				var gens []string
				for _, gen := range reg.Pipeline(kind) {
					gens = append(gens, gen.Name)
				}
				_, _ = fmt.Fprintf(out, "  %s: %s\n", kind, strings.Join(gens, " -> "))
			}
			var docs []string
			for _, doc := range reg.Documents() {
				docs = append(docs, doc.Name+" ("+doc.File+")")
			}
			_, _ = fmt.Fprintf(out, "documents: %s\n", strings.Join(docs, ", "))
			_, _ = fmt.Fprintf(out, "output keys: %s\n", strings.Join(reg.OutputKeys(), ", "))
			_, _ = fmt.Fprintf(out, "auth providers: %s (selected: %s)\n", strings.Join(reg.AuthProviders(), ", "), names.AuthProvider)
			policy := reg.ToolInvocationPolicy()
			_, _ = fmt.Fprintf(out, "tool invocation policy: %s (%s; default %s)\n", policy.Key, strings.Join(policy.Values, ", "), policy.Default)
			return nil
		},
	}
}
