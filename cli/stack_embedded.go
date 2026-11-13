package cli

import (
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/embedded"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	ir "github.com/parable-work/superschematic/ir"
)

// An environment that builds as one binary (D116): one some of whose
// servers or workers run inside its binary, as the embedded target's do.
// `stack build <environment>` builds the stack, plans the step each
// database's file takes, records it with --release, writes what the
// binary carries into its crate and builds the crate with cargo. Every
// other command refuses the environment: what its target makes is a file
// a person installs, and it rolls nothing out.

// binaryCommands are the stack commands that take an environment that
// builds as one binary.
var binaryCommands = map[string]bool{"build": true}

// refuseBinary refuses, for a command that deploys, an environment that
// builds as one binary, and names the command that builds it.
func refuseBinary(cmd *cobra.Command, env *ir.ResolvedEnvironment) error {
	if !env.IsBinary() || binaryCommands[cmd.Name()] {
		return nil
	}
	return fmt.Errorf("environment %s is on the %s target, which builds one binary a person installs and rolls nothing out, so `stack %s` has nothing to do for it: build the binary with `superschematic stack build %s`",
		env.Environment, env.Target, cmd.Name(), env.Environment)
}

// binaryBuildFlags are `stack build`'s flags for an environment that
// builds as one binary.
type binaryBuildFlags struct {
	release bool
	failOn  []string
	allow   []string
}

func (f *binaryBuildFlags) register(cmd *cobra.Command) {
	cmd.Flags().BoolVar(&f.release, "release", false, "for an environment that builds as one binary: record the plan of each database, from the model the last recorded plan ends at to the schema's, in the Stack service's plans/<environment>/<db>/, to commit")
	cmd.Flags().StringSliceVar(&f.failOn, "fail-on", []string{"all"}, "with --release: hazard classes that stop the release unless --allow names each hazard (comma-separated; all for every class, none for no gate)")
	cmd.Flags().StringArrayVar(&f.allow, "allow", nil, "with --release: a hazard id to acknowledge, as the refusal lists it (repeatable)")
}

// used reports whether a flag of f was given.
func (f *binaryBuildFlags) used(cmd *cobra.Command) bool {
	return cmd.Flags().Changed("release") || cmd.Flags().Changed("fail-on") || cmd.Flags().Changed("allow")
}

// binaryDatabases reads the DB schemas the environment's databases host,
// each with the adoption its database's settings name (D116): the model's
// file, from the Stack service's directory, and the statement that says a
// file built by hand holds it.
func (c *deployContext) binaryDatabases() ([]embedded.Database, error) {
	var out []embedded.Database
	for _, d := range c.env.Deployables {
		if d.Kind != ir.DeployableDatabase {
			continue
		}
		var adoption *embedded.Adoption
		if named, ok := d.Settings["adoption"].(map[string]any); ok {
			model, _ := named["model"].(string)
			check, _ := named["check"].(string)
			if model == "" || check == "" {
				return nil, fmt.Errorf("database %s: its adoption names a model and a check, a statement that answers one row holding 1", d.Name)
			}
			if len(d.Services) != 1 {
				return nil, fmt.Errorf("database %s hosts %d DB schemas and names one adoption: a file is adopted at the model of one schema, so host each schema that has an adoption on a database of its own", d.Name, len(d.Services))
			}
			if !filepath.IsAbs(model) {
				model = filepath.Join(c.project.dir, filepath.FromSlash(model))
			}
			adoption = &embedded.Adoption{ModelPath: model, Check: check}
		}
		for _, svc := range d.Services {
			if d.Dialect != string(sqlmigrate.SQLite) {
				return nil, fmt.Errorf("database %s is a %s database, and environment %s builds as one binary, whose databases are SQLite files (D116)", d.Name, d.Dialect, c.env.Environment)
			}
			out = append(out, embedded.Database{Service: svc.Name, Adoption: adoption})
		}
	}
	return out, nil
}

// buildBinary is `stack build` for an environment that builds as one
// binary. It prints what it does on the command's standard error, and the
// binary's path alone on its standard output.
func (c *deployContext) buildBinary(cmd *cobra.Command, flags *binaryBuildFlags) error {
	if c.prepared != nil {
		return fmt.Errorf("environment %s builds as one binary, which stack build builds from the project's code on this machine: --prepared has nothing to give it", c.env.Environment)
	}
	var failOn []sqlmigrate.HazardClass
	if flags.release && (len(flags.failOn) != 1 || flags.failOn[0] != "none") {
		classes, err := parseHazardClasses(flags.failOn)
		if err != nil {
			return err
		}
		failOn = classes
	}
	if !flags.release && (cmd.Flags().Changed("fail-on") || cmd.Flags().Changed("allow")) {
		return errors.New("--fail-on and --allow gate the plans a release records: pass them with --release")
	}
	log := cmd.ErrOrStderr()
	// The stack's build writes the crate, and brings the workspace's
	// lockfile up to date with it. What it prints is not the binary's path.
	out := cmd.OutOrStdout()
	cmd.SetOut(log)
	err := c.project.build(cmd)
	cmd.SetOut(out)
	if err != nil {
		return err
	}
	databases, err := c.binaryDatabases()
	if err != nil {
		return err
	}
	planned, err := embedded.Plan(embedded.PlanInput{
		StackDir:    c.project.dir,
		Environment: c.env.Environment,
		Databases:   databases,
		Model: func(service string) (*sqlmigrate.Model, error) {
			if err := c.version.requireDialect(service, sqlmigrate.SQLite); err != nil {
				return nil, err
			}
			return c.version.model(service, sqlmigrate.SQLite)
		},
		Reads:   c.version.reads,
		Release: flags.release,
		FailOn:  failOn,
		Allow:   flags.allow,
	})
	if err != nil {
		return err
	}
	writeBinarySteps(log, c.env.Environment, planned, flags.release)
	ctx, stop := interruptible(cmd)
	defer stop()
	built, err := embedded.Build(ctx, embedded.BuildOptions{
		Environment: c.env,
		OutputRoot:  c.project.outputRoot,
		Sources:     c.project.sources(),
		Planned:     planned,
		Release:     flags.release,
		Log:         log,
	})
	if err != nil {
		return err
	}
	_, err = fmt.Fprintln(out, built.Binary)
	return err
}

// writeBinarySteps says what the build planned for each database: the
// plans a release recorded, to commit, or the step a build that is no
// release carries and records nowhere.
func writeBinarySteps(w io.Writer, environment string, planned *embedded.Planned, release bool) {
	carried := map[string]int{}
	for _, d := range planned.Databases {
		carried[d.Service] = len(d.Plans)
	}
	for _, step := range planned.Steps {
		contract := 0
		for _, s := range step.Plan.Steps {
			if s.Phase == sqlmigrate.Contract {
				contract++
			}
		}
		what := fmt.Sprintf("plan %d of %s, %d step(s), %d of them contract", step.Number, step.Service, len(step.Plan.Steps), contract)
		if release {
			_, _ = fmt.Fprintf(w, "environment %s: recorded %s, at %s; commit it\n", environment, what, step.Path)
		} else {
			_, _ = fmt.Fprintf(w, "environment %s: this build carries %s, which no release recorded; record it with stack build %s --release\n", environment, what, environment)
		}
		for _, h := range step.Plan.Hazards() {
			_, _ = fmt.Fprintf(w, "  hazard %s: %s\n", h.ID, h.Reason)
		}
	}
	for _, d := range planned.Databases {
		_, _ = fmt.Fprintf(w, "environment %s: the binary carries %d plan(s) of %s\n", environment, carried[d.Service], d.Service)
	}
}
