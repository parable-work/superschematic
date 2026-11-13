package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/embedded"
	"github.com/parable-work/superschematic/internal/generator"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack/local"
	ir "github.com/parable-work/superschematic/ir"
)

// `stack dev` for an environment that builds as one binary (D116): it
// runs the environment as the one process it is, on this machine, with
// its databases' files under the environment's state directory, laid out
// as the local target lays them out.
//
// It migrates each file as the local target does: it asks the migration
// runner, superschematic-migrate, for the model the file recorded, plans
// from it to the schema's model, since the compiler is here, and applies
// both phases back to back. The chain a release recorded is no use in
// this loop, where a schema changes many times between releases. Then it
// builds the binary as `stack build` builds one that is no release, whose
// carried plans end at the schema's model, which the file is at, so the
// binary's own start finds nothing to do; and runs it until Ctrl-C.

// noAppliedModel is what `superschematic-migrate status --model` says of
// a file no plan has finished on.
const noAppliedModel = "has no applied model"

// devEnvironment picks the environment stack dev runs: the one named, on
// the local target or one that builds as one binary; else the stack's one
// environment on the local target; else its one environment that builds
// as one binary.
func (p *stackProject) devEnvironment(name string) (*ir.ResolvedEnvironment, error) {
	if name != "" {
		env, err := p.environment(name)
		if err != nil {
			return nil, err
		}
		if env.IsBinary() {
			return env, nil
		}
		return p.localEnvironment(name)
	}
	envs, err := p.environments()
	if err != nil {
		return nil, err
	}
	var binaries []*ir.ResolvedEnvironment
	for _, env := range envs {
		switch {
		case env.Target == local.Target:
			return p.localEnvironment("")
		case env.IsBinary():
			binaries = append(binaries, env)
		}
	}
	if len(binaries) == 1 {
		return binaries[0], nil
	}
	return p.localEnvironment("")
}

// runStackDevBinary runs env, which builds as one binary, until Ctrl-C.
func runStackDevBinary(cmd *cobra.Command, p *stackProject, env *ir.ResolvedEnvironment, flags *stackDevFlags) error {
	out := cmd.OutOrStdout()
	stateDir, err := local.EnsureStateDir(p.schemasRoot, env.Stack, env.Environment)
	if err != nil {
		return err
	}
	version, err := openSchemaVersion(p.servicesRoot, p.names, p.reg)
	if err != nil {
		return err
	}
	defer version.close()
	c := &deployContext{project: p, reg: p.reg, version: version, env: env}
	databases, err := c.binaryDatabases()
	if err != nil {
		return err
	}
	model := func(service string) (*sqlmigrate.Model, error) {
		if err := version.requireDialect(service, sqlmigrate.SQLite); err != nil {
			return nil, err
		}
		return version.model(service, sqlmigrate.SQLite)
	}
	ctx, stop := signal.NotifyContext(cmd.Context(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	files := map[string]string{}
	for _, res := range env.Resources.Resources {
		service, _ := res.Properties["service"].(string)
		file, _ := res.Properties["file"].(string)
		if service != "" && file != "" {
			files[service] = filepath.Join(stateDir, filepath.FromSlash(file))
		}
	}
	plans := filepath.Join(programDir(p.outputRoot, env.Stack, env.Environment), local.MigrationsDir)
	for _, d := range databases {
		file, ok := files[d.Service]
		if !ok {
			return fmt.Errorf("environment %s names no file for database %s", env.Environment, d.Service)
		}
		to, err := model(d.Service)
		if err != nil {
			return fmt.Errorf("the model of %s: %w", d.Service, err)
		}
		if err := devMigrate(ctx, out, d.Service, file, to, filepath.Join(plans, d.Service+".plan.json")); err != nil {
			return err
		}
	}

	planned, err := embedded.Plan(embedded.PlanInput{
		StackDir:    p.dir,
		Environment: env.Environment,
		Databases:   databases,
		Model:       model,
		Reads:       version.reads,
	})
	if err != nil {
		return err
	}
	built, err := embedded.Build(ctx, embedded.BuildOptions{
		Environment: env,
		OutputRoot:  p.outputRoot,
		Sources:     p.sources(),
		Planned:     planned,
		Log:         out,
	})
	if err != nil {
		return err
	}

	port := generator.EmbeddedDefaultPort
	if n, ok := env.Values["port"].(float64); ok {
		port = int(n)
	}
	_, _ = fmt.Fprintf(out, "\nStack %s environment %s is one binary, %s:\n  origin   http://127.0.0.1:%d, each API under %s<service>\n  state    %s\nPress Ctrl-C to stop.\n",
		env.Stack, env.Environment, built.Binary, port, ir.SiteDevAPIPrefix, stateDir)
	run := exec.Command(built.Binary, "--data", stateDir)
	run.Env = append(os.Environ(), "PORT="+strconv.Itoa(port))
	run.Stdout, run.Stderr = out, cmd.ErrOrStderr()
	if err := run.Start(); err != nil {
		return fmt.Errorf("start %s: %w", built.Binary, err)
	}
	done := make(chan error, 1)
	go func() { done <- run.Wait() }()
	var runErr error
	select {
	case runErr = <-done:
		if runErr == nil {
			runErr = fmt.Errorf("%s stopped before Ctrl-C", built.Binary)
		}
	case <-ctx.Done():
		_, _ = fmt.Fprintf(out, "\nStopping stack %s environment %s\n", env.Stack, env.Environment)
		_ = run.Process.Signal(syscall.SIGTERM)
		// The binary gives its requests in flight ten seconds, and exits 0.
		if err := <-done; err != nil {
			runErr = fmt.Errorf("%s: %w", built.Binary, err)
		}
	}
	if flags.removeData {
		for _, file := range files {
			for _, name := range []string{file, file + "-journal", file + "-wal", file + "-shm"} {
				if err := os.Remove(name); err != nil && !errors.Is(err, os.ErrNotExist) {
					runErr = errors.Join(runErr, err)
				}
			}
		}
	}
	return runErr
}

// devMigrate takes the SQLite file of service to the model to, as the
// local target migrates one: from the model the file recorded, which the
// migration runner reads, by a plan written to planPath, both phases back
// to back, since no server of the previous version reads the file.
func devMigrate(ctx context.Context, out io.Writer, service, file string, to *sqlmigrate.Model, planPath string) error {
	runner, err := inspectRunner()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(file), 0o700); err != nil {
		return err
	}
	if f, err := os.OpenFile(file, os.O_RDWR|os.O_CREATE, 0o600); err != nil {
		return err
	} else if err := f.Close(); err != nil {
		return err
	}
	url := local.SQLiteURL(file)
	var from *sqlmigrate.Model
	var stdout, stderr bytes.Buffer
	status := exec.CommandContext(ctx, runner, "status", "--service", service, "--model", "--database-url", url)
	status.Stdout, status.Stderr = &stdout, &stderr
	switch err := status.Run(); {
	case err == nil:
		from = &sqlmigrate.Model{}
		decoder := json.NewDecoder(bytes.NewReader(stdout.Bytes()))
		decoder.DisallowUnknownFields()
		if err := decoder.Decode(from); err != nil {
			return fmt.Errorf("migrate %s: the model %s recorded: %w", service, file, err)
		}
	case strings.Contains(strings.ToLower(stderr.String()), noAppliedModel):
	default:
		return fmt.Errorf("migrate %s: read the model %s recorded: %w: %s", service, file, err, strings.TrimSpace(stderr.String()))
	}
	toHash, err := to.Hash()
	if err != nil {
		return err
	}
	if from != nil {
		if fromHash, err := from.Hash(); err == nil && fromHash == toHash {
			_, _ = fmt.Fprintf(out, "migrate %s: up to date at model %.12s\n", service, toHash)
			return nil
		}
	}
	plan, err := sqlmigrate.Diff(from, to, sqlmigrate.Options{})
	if err != nil {
		return fmt.Errorf("migrate %s: plan: %w", service, err)
	}
	data, err := plan.CanonicalJSON()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(planPath), 0o755); err != nil {
		return err
	}
	if err := os.WriteFile(planPath, append(data, '\n'), 0o644); err != nil {
		return err
	}
	_, _ = fmt.Fprintf(out, "migrate %s: %d steps to model %.12s, expand and contract back to back (%s)\n", service, len(plan.Steps), toHash, planPath)
	for _, h := range plan.Hazards() {
		_, _ = fmt.Fprintf(out, "migrate %s: hazard %s\n", service, h.ID)
	}
	apply := exec.CommandContext(ctx, runner, "apply", "--plan", planPath, "--phase", "all", "--database-url", url)
	apply.Stdout, apply.Stderr = out, out
	if err := apply.Run(); err != nil {
		return fmt.Errorf("migrate %s: %w", service, err)
	}
	return nil
}
