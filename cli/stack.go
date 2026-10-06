package cli

import "github.com/spf13/cobra"

// newStackCmd groups the `stack` commands (docs/stack-model.md, section
// 11.1). Targets and provisioners plug into them; they add no commands of
// their own. The cloud half registers through addStackDeployCommands.
func newStackCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "stack",
		Short: "Bootstrap, plan, deploy and destroy the environments of a stack",
		Long: `stack runs the environments a Stack service declares (docs/stack-model.md):
each command resolves one environment of the stack, as build does, and
drives its target and provisioner.`,
		Args: cobra.NoArgs,
	}
	addStackDeployCommands(cmd, a)
	return cmd
}
