package cli

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/stackdeploy"
)

// `superschematic stack run <environment> <job>` (docs/stack-model.md,
// sections 8.7 and 11.1, D52): one run of a job, outside its schedule. On
// the local target it runs against the environment stack dev runs; on a
// cloud target it runs the deployed job through the target's job runner.

func init() { registerStackCommands(newStackRunCmd) }

// localJobRunner is the local provisioner's run of a job on demand
// (local.Provisioner.RunJob).
type localJobRunner interface {
	RunJob(ctx context.Context, req registry.ProvisionRequest, job string) error
}

func newStackRunCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	cmd := &cobra.Command{
		Use:   "run <environment> <job>",
		Short: "Run a job of an environment once, outside its schedule",
		Long: `Run runs a job of an environment once, outside its schedule
(docs/stack-model.md, section 8.7). <job> is the job's deployable: the API
service's name, a hyphen, and the job's class in kebab case
(shop-orders-ship-orders).

On the local target, run runs the job against the environment stack dev
runs, from another terminal. It reads the environment the last build
resolved and the program stack dev rendered, builds the job's binary from
its entrypoint module at <output-root>/server/<stack>/<job>, and runs it
with the environment's values, secrets and keys, its timeout and its
retries, each line of its output with the job's name in front. It builds
no schema, so a change to the job's implementation is in the run, and a
change to a schema needs stack dev again. It refuses an environment whose
Postgres container or servers do not answer, and a job whose run from stack
dev's schedule goes on; that schedule skips its runs while this one goes
on. Ctrl-C stops the run.

On a cloud target, run runs the deployed job through the target's job
runner: the job the last deploy rolled out, with its image, config,
identity, timeout and retries. It refuses a target that runs no job on
demand, and a run whose last deploy did not roll the job out.

Run exits non-zero when the job's last try fails.

Examples:
  superschematic stack run Dev shop-orders-ship-orders
  superschematic stack run Staging shop-orders-ship-orders
  superschematic stack run Preview shop-orders-ship-orders --param pr=123`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := parseParams(flags.params)
			if err != nil {
				return err
			}
			c, err := openDeployContext(cmd, a, flags, args[0], true)
			if err != nil {
				return err
			}
			defer c.close()
			ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
			defer stop()
			if c.env.Target == local.Target {
				return runLocalJob(ctx, cmd, c, params, args[1])
			}
			return stackdeploy.RunJob(ctx, stackdeploy.RunJobOptions{Options: c.options(cmd, params), Job: args[1]})
		},
	}
	flags.register(cmd, true)
	cmd.Flags().StringVar(&flags.outputRoot, "out", "", "the output root the build wrote, stack dev's --out (default <schemas-root>/dist)")
	return cmd
}

// runLocalJob runs a job once against the local environment stack dev
// runs: the environment the last build resolved, whose program stack dev
// rendered, with the state directory that holds its secrets and keys.
func runLocalJob(ctx context.Context, cmd *cobra.Command, c *deployContext, params map[string]string, job string) error {
	p := c.project
	env, err := p.environment(c.env.Environment)
	if err != nil {
		return fmt.Errorf("%w; stack run runs a job against the environment stack dev runs: start it with superschematic stack dev --environment %s", err, c.env.Environment)
	}
	spec, ok := p.reg.Provisioner(env.Provisioner)
	if !ok {
		return fmt.Errorf("environment %s names provisioner %q, which is not registered", env.Environment, env.Provisioner)
	}
	runner, ok := spec.Provisioner.(localJobRunner)
	if !ok {
		return fmt.Errorf("provisioner %s runs no job on demand", env.Provisioner)
	}
	if s, ok := spec.Provisioner.(outputSetter); ok {
		s.SetOutput(cmd.OutOrStdout())
	}
	return runner.RunJob(ctx, registry.ProvisionRequest{
		Environment: env,
		Parameters:  params,
		Dir:         c.dir,
		OutputRoot:  p.outputRoot,
		Backend:     local.StateBackend(local.StateDir(p.schemasRoot, env.Stack, env.Environment)),
	}, job)
}
