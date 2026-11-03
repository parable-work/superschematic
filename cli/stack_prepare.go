package cli

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"sync"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/loader"
	"github.com/parable-work/superschematic/internal/loader/executor"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/stack"
	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

func init() { registerStackCommands(newStackPrepareCmd) }

// newStackPrepareCmd is `stack prepare`: the project code a deploy runs,
// run here with no credentials, into a directory the cloud stack commands
// read with --prepared (D47, amended).
func newStackPrepareCmd(a *app) *cobra.Command {
	var stackDir, namingPath, outputRoot, dir string
	var noBuild bool
	cmd := &cobra.Command{
		Use:   "prepare [environment...]",
		Short: "Run the project code a deploy needs, with no credentials, into a directory stack deploy --prepared reads",
		Long: `prepare runs every piece of the project's code that stack plan, deploy
and destroy would otherwise run beside the target's credentials, and
writes what it made into a directory: each service's sidecar documents,
evaluated with Bun; the build context of each server, job and worker
whose Dockerfile superschematic build-all wrote; each site, built with its
build script after one frozen install of the output root's workspace;
and each bundle, built with bun bundle.ts. A site's or a bundle's files
are kept as the tar stream their digest is of. It reaches no target and
needs no credentials: run it before signing in, or in a shell that holds
none.

stack plan, build, deploy and destroy given the directory with
--prepared read all of it from there and start no Bun, no install and no
build, so a dependency's install script, a build plugin or a sidecar
document never runs where the credentials are. A document whose file or
imports changed since is refused. The generated GitHub workflow runs
prepare in a job with no identity token and hands the directory to the
cloud jobs (docs/stack-model.md, section 11.3).

With no environment named it prepares every environment off the local
target. --no-build writes the documents alone, which a plan or a destroy
needs. --dir is the directory, <output-root>/prepared/<stack> unless set;
an earlier prepare's directory is replaced, and one that holds anything
else is refused. Run superschematic build-all first. A stack with an app
that needs an extension this binary does not link is refused unless
--no-build, as build-all refuses it (D78).`,
		RunE: func(cmd *cobra.Command, args []string) error {
			p, err := openStackProject(cmd, a, stackDir, outputRoot, namingPath)
			if err != nil {
				return err
			}
			version, err := openSchemaVersion(p.servicesRoot, p.names, p.reg)
			if err != nil {
				return err
			}
			defer version.close()
			documents := version.record(p.repositoryRoot())
			// Every service loads, so each document any stack command
			// reads, its planner's readers' included, is recorded.
			for _, service := range version.services {
				if _, _, err := version.load(service.Name); err != nil {
					return err
				}
			}
			_, _, st, services, err := version.stack(p)
			if err != nil {
				return err
			}
			schemasRoot, err := stack.CanonicalSchemasRoot(p.schemasRoot)
			if err != nil {
				return err
			}
			var envs []*ir.ResolvedEnvironment
			for _, e := range st.Environments {
				if len(args) > 0 && !slices.Contains(args, e.Name) {
					continue
				}
				env, err := stack.Resolve(p.reg, stack.Input{Stack: st, Services: services, Environment: e.Name, SchemasRoot: schemasRoot})
				if err != nil {
					return err
				}
				if env.Target == local.Target && len(args) == 0 {
					continue
				}
				envs = append(envs, env)
			}
			for _, name := range args {
				if !stackHasEnvironment(st, name) {
					return fmt.Errorf("stack %s has no environment %s", st.Name, name)
				}
			}
			var src *stackdeploy.Sources
			if !noBuild {
				if err := checkAppExtensions(p.reg, st, services); err != nil {
					return err
				}
				src = p.sources()
			}
			if dir == "" {
				dir = stackdeploy.PreparedDir(p.outputRoot, st.Name)
			}
			prepared, err := stackdeploy.Prepare(cmd.Context(), stackdeploy.PrepareOptions{
				Stack:        st.Name,
				Environments: envs,
				Sources:      src,
				Documents:    documents(),
				Dir:          dir,
				Provenance:   stackdeploy.CIProvenance(),
				Log:          cmd.ErrOrStderr(),
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "prepared %s in %s: %d documents, %d contexts, %d sites, %d bundles\n",
				st.Name, prepared.Dir, len(prepared.Documents), len(prepared.Contexts), len(prepared.Sites), len(prepared.Bundles))
			return err
		},
	}
	cmd.Flags().StringVar(&stackDir, "stack", "", "the Stack service directory (default: the working directory when it is one, else the one Stack service under ./schemas/services)")
	cmd.Flags().StringVar(&namingPath, "naming", "", "naming config file (default <stack>/../../superschematic.toml)")
	cmd.Flags().StringVar(&outputRoot, "out", "", "the output root build-all wrote (default <schemas-root>/dist)")
	cmd.Flags().StringVar(&dir, "dir", "", "write here (default <output-root>/prepared/<stack>)")
	cmd.Flags().BoolVar(&noBuild, "no-build", false, "write the sidecar documents alone: build no context, site or bundle")
	return cmd
}

// record has every sidecar document a module of the version runs recorded
// as a prepared directory keeps it, its sources relative to root, and
// returns what it recorded.
func (v *schemaVersion) record(root string) func() map[string]*stackdeploy.PreparedDocument {
	var mu sync.Mutex
	out := map[string]*stackdeploy.PreparedDocument{}
	v.loadOpts = append(v.loadOpts, loader.WithModuleRunner(func(servicePath, file string) (json.RawMessage, []string, error) {
		res, err := executor.RunDocument(servicePath, file)
		if err != nil {
			return nil, nil, err
		}
		key, err := stackdeploy.DocumentKey(root, servicePath, file)
		if err != nil {
			return nil, nil, err
		}
		doc, err := stackdeploy.RecordDocument(root, res.Document, res.AuthoringImports)
		if err != nil {
			return nil, nil, fmt.Errorf("%s: %w", key, err)
		}
		mu.Lock()
		out[key] = doc
		mu.Unlock()
		return res.Document, res.AuthoringImports, nil
	}))
	return func() map[string]*stackdeploy.PreparedDocument {
		mu.Lock()
		defer mu.Unlock()
		return maps.Clone(out)
	}
}

// replay has every sidecar document a module of the version runs read from
// prepared, whose sources are relative to root, and never run (D47,
// amended).
func (v *schemaVersion) replay(prepared *stackdeploy.Prepared, root string) {
	v.loadOpts = append(v.loadOpts, loader.WithModuleRunner(func(servicePath, file string) (json.RawMessage, []string, error) {
		return prepared.Document(root, servicePath, file)
	}))
}

// stack loads the project's stack service, its declaration, and every
// service the stack reaches.
func (v *schemaVersion) stack(p *stackProject) (*ir.Schema, *schemaconfig.SchemaConfig, *ir.Stack, []stack.Service, error) {
	schema, cfg, err := v.load(p.stack.Name)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	st := ir.StackOf(schema)
	if st == nil {
		return nil, nil, nil, nil, fmt.Errorf("stack %s: no class declares @stack", p.stack.Name)
	}
	gen := registry.GenerateContext{
		Schema:   schema,
		Config:   cfg,
		Registry: p.reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			s, _, err := v.load(name)
			return s, err
		},
		Options: registry.Options{LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			_, c, err := v.load(name)
			return c, err
		}},
	}
	services, err := stackgen.Services(gen, st)
	if err != nil {
		return nil, nil, nil, nil, err
	}
	return schema, cfg, st, services, nil
}
