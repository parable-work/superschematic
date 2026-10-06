package cli

import "github.com/spf13/cobra"

// stackCommands are the constructors of the `stack` group's subcommands
// (docs/stack-model.md, section 11.1). A file of this package adds its own
// with one call, in an init function:
//
//	func init() { registerStackCommands(newStackPlanCmd, newStackDeployCmd) }
//
// Targets and provisioners add no commands: they plug into these.
var stackCommands []func(*app) *cobra.Command

// registerStackCommands adds subcommands to the `stack` group.
func registerStackCommands(constructors ...func(*app) *cobra.Command) {
	stackCommands = append(stackCommands, constructors...)
}

// newStackCmd is the `stack` group: the commands that run and deploy a
// stack's environments.
func newStackCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Run and deploy the environments of a stack",
		Long: `The stack commands run and deploy the environments a Stack service declares
(docs/stack-model.md). Each resolves one environment of the stack and drives
its target and provisioner.`,
		Args: cobra.NoArgs,
	}
	for _, newCmd := range stackCommands {
		cmd.AddCommand(newCmd(a))
	}
	return cmd
}
