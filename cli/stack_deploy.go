package cli

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sqlmigrate"
	"github.com/parable-work/superschematic/internal/stack"
	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// The cloud half of the `stack` commands (docs/stack-model.md, sections
// 7.3, 11.1 and 11.2): bootstrap, secrets set, plan, deploy, destroy and
// outputs. Each resolves one environment of a stack from its schemas, as
// the stack generator does, and hands it to internal/stackdeploy, which
// drives the environment's target and provisioner.

func init() {
	registerStackCommands(
		newStackBootstrapCmd,
		newStackSecretsCmd,
		newStackPlanCmd,
		newStackDeployCmd,
		newStackDestroyCmd,
		newStackOutputsCmd,
	)
}

// newStackSecretsCmd groups `secrets set`.
func newStackSecretsCmd(a *app) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "secrets",
		Short: "Enter the values of an environment's secrets",
		Args:  cobra.NoArgs,
	}
	cmd.AddCommand(newStackSecretsSetCmd(a))
	return cmd
}

// stackFlags are the flags every cloud stack command shares.
type stackFlags struct {
	stackDir   string
	namingPath string
	programDir string
	params     []string
}

// register adds the shared flags; a command that runs one run of an
// environment takes --param too.
func (f *stackFlags) register(cmd *cobra.Command, run bool) {
	cmd.Flags().StringVar(&f.stackDir, "stack", "", "the Stack service directory (default: the working directory when it is one, else the one Stack service under ./schemas/services)")
	cmd.Flags().StringVar(&f.namingPath, "naming", "", "naming config file (default <stack>/../../superschematic.toml)")
	cmd.Flags().StringVar(&f.programDir, "program-dir", "", "render the provisioner's program here (default <schemas-root>/dist/program/<stack>/<environment>)")
	if run {
		cmd.Flags().StringArrayVar(&f.params, "param", nil, "a parameter's value for this run of a parameterized environment: <name>=<value> (repeatable)")
	}
}

// gateFlags are the hazard gate's flags, which plan and deploy share.
type gateFlags struct {
	failOn []string
	allow  []string
}

func (g *gateFlags) register(cmd *cobra.Command) {
	cmd.Flags().StringSliceVar(&g.failOn, "fail-on", []string{"all"}, "hazard classes that stop the deploy unless --allow names each hazard (comma-separated; all for every class, none for no gate)")
	cmd.Flags().StringArrayVar(&g.allow, "allow", nil, "a hazard id to acknowledge, as the plan lists it (repeatable)")
}

func (g *gateFlags) gate() (stackdeploy.Gate, error) {
	if len(g.failOn) == 1 && g.failOn[0] == "none" {
		return stackdeploy.Gate{FailOn: []string{}, Allow: g.allow}, nil
	}
	classes, err := parseHazardClasses(g.failOn)
	if err != nil {
		return stackdeploy.Gate{}, err
	}
	failOn := []string{}
	for _, class := range classes {
		failOn = append(failOn, string(class))
	}
	return stackdeploy.Gate{FailOn: failOn, Allow: g.allow}, nil
}

// deployContext is one stack command's environment, resolved from the
// schemas, with what loaded it. The project is the one stack dev opens
// (openStackProject). The environment is resolved here rather than read
// from the build's environment.json (stackProject.environment), so a plan
// needs no build and never reads a stale environment, and the schemas
// version beside it loads the DB models the migration plans need.
type deployContext struct {
	project  *stackProject
	reg      *registry.Registry
	env      *ir.ResolvedEnvironment
	version  *schemaVersion
	services []stack.Service
	dir      string
	cleanup  []func()
}

func (c *deployContext) close() {
	for i := len(c.cleanup) - 1; i >= 0; i-- {
		c.cleanup[i]()
	}
}

// openDeployContext opens the stack, resolves the environment named
// environment, and names the program directory. A local environment runs
// with `stack dev`, so only a command that serves one, secrets set,
// passes allowLocal.
func openDeployContext(cmd *cobra.Command, a *app, flags *stackFlags, environment string, allowLocal bool) (*deployContext, error) {
	p, err := openStackProject(cmd, a, flags.stackDir, "", flags.namingPath)
	if err != nil {
		return nil, err
	}
	version, err := openSchemaVersion(p.servicesRoot, p.names, p.reg)
	if err != nil {
		return nil, err
	}
	c := &deployContext{project: p, reg: p.reg, version: version, cleanup: []func(){version.close}}
	fail := func(err error) (*deployContext, error) {
		c.close()
		return nil, err
	}
	schema, cfg, err := version.load(p.stack.Name)
	if err != nil {
		return fail(err)
	}
	st := ir.StackOf(schema)
	if st == nil {
		return fail(fmt.Errorf("stack %s: no class declares @stack", p.stack.Name))
	}
	gen := registry.GenerateContext{
		Schema:   schema,
		Config:   cfg,
		Registry: p.reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			s, _, err := version.load(name)
			return s, err
		},
		Options: registry.Options{LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			_, c, err := version.load(name)
			return c, err
		}},
	}
	if c.services, err = stackgen.Services(gen, st); err != nil {
		return fail(err)
	}
	if !stackHasEnvironment(st, environment) {
		var known []string
		for _, e := range st.Environments {
			known = append(known, e.Name)
		}
		return fail(fmt.Errorf("stack %s has no environment %s (its environments: %s)", st.Name, environment, strings.Join(known, ", ")))
	}
	if c.env, err = stack.Resolve(p.reg, stack.Input{Stack: st, Services: c.services, Environment: environment}); err != nil {
		return fail(err)
	}
	if c.env.Target == local.Target && !allowLocal {
		return fail(fmt.Errorf("environment %s is on the %s target, which `stack %s` does not deploy: run it with `superschematic stack dev --environment %s`",
			environment, local.Target, cmd.Name(), environment))
	}
	c.dir = flags.programDir
	if c.dir == "" {
		c.dir = programDir(p.outputRoot, st.Name, environment)
	}
	return c, nil
}

func stackHasEnvironment(st *ir.Stack, name string) bool {
	for _, e := range st.Environments {
		if e.Name == name {
			return true
		}
	}
	return false
}

// localSecretsStore is a local environment's secret store over the
// secrets.env stack dev reads (local.SecretsFile, local.WriteSecret). The
// local target registers no SecretStore: the file sits under the schemas
// root, which a registry does not know. So secrets set hands this one to
// the operation in the target's place.
type localSecretsStore struct {
	schemasRoot string
}

var _ registry.SecretStore = localSecretsStore{}

func (c *deployContext) localSecrets() localSecretsStore {
	return localSecretsStore{schemasRoot: c.project.schemasRoot}
}

func (s localSecretsStore) path(env *ir.ResolvedEnvironment) string {
	return filepath.Join(local.StateDir(s.schemasRoot, env.Stack, env.Environment), local.SecretsFile)
}

func (s localSecretsStore) Exists(_ context.Context, env *ir.ResolvedEnvironment, id string) (bool, error) {
	values, err := local.ReadSecrets(s.path(env))
	_, ok := values[id]
	return ok, err
}

func (s localSecretsStore) List(_ context.Context, env *ir.ResolvedEnvironment) ([]string, error) {
	values, err := local.ReadSecrets(s.path(env))
	if err != nil {
		return nil, err
	}
	var ids []string
	for _, secret := range env.Secrets {
		if _, ok := values[secret.ID]; ok {
			ids = append(ids, secret.ID)
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s localSecretsStore) Set(_ context.Context, env *ir.ResolvedEnvironment, id string, value []byte) error {
	if _, err := local.EnsureStateDir(s.schemasRoot, env.Stack, env.Environment); err != nil {
		return err
	}
	return local.WriteSecret(s.path(env), id, string(value))
}

func (s localSecretsStore) Get(_ context.Context, env *ir.ResolvedEnvironment, id string) ([]byte, error) {
	values, err := local.ReadSecrets(s.path(env))
	if err != nil {
		return nil, err
	}
	value, ok := values[id]
	if !ok {
		return nil, fmt.Errorf("secret %s has no value", id)
	}
	return []byte(value), nil
}

// parseParams reads --param values.
func parseParams(values []string) (map[string]string, error) {
	params := map[string]string{}
	for _, value := range values {
		name, v, ok := strings.Cut(value, "=")
		if !ok || name == "" {
			return nil, fmt.Errorf("--param %q: want <name>=<value>", value)
		}
		if _, dup := params[name]; dup {
			return nil, fmt.Errorf("--param names %s twice", name)
		}
		params[name] = v
	}
	return params, nil
}

// options returns the operations' options over the run --param names.
func (c *deployContext) options(cmd *cobra.Command, params map[string]string) stackdeploy.Options {
	return stackdeploy.Options{
		Registry: c.reg,
		Run:      registry.Run{Environment: c.env, Parameters: params},
		Dir:      c.dir,
		Log:      cmd.ErrOrStderr(),
	}
}

// digests returns the IR digest of each service the stack reaches:
// `sha256:` and the hex SHA-256 of the IR's canonical JSON.
func (c *deployContext) digests() (map[string]string, error) {
	out := map[string]string{}
	for _, svc := range c.services {
		schema, _, err := c.version.load(svc.Name)
		if err != nil {
			return nil, err
		}
		raw, err := json.Marshal(schema)
		if err != nil {
			return nil, err
		}
		canonical, err := ir.CanonicalJSON(raw)
		if err != nil {
			return nil, err
		}
		sum := sha256.Sum256(canonical)
		out[svc.Name] = "sha256:" + hex.EncodeToString(sum[:])
	}
	return out, nil
}

// planner plans a DB service's migration from a recorded model to the
// schema now, with the readers of the schemas root as the readers after
// the rollout. The recorded model has no services beside it, so there
// are no readers before it (D27).
func (c *deployContext) planner() stackdeploy.Planner {
	return func(service, dialect string, from json.RawMessage) (*stackdeploy.DatabasePlan, error) {
		d := sqlmigrate.Dialect(dialect)
		if err := c.version.requireDialect(service, d); err != nil {
			return nil, err
		}
		to, err := c.version.model(service, d)
		if err != nil {
			return nil, err
		}
		var fromModel *sqlmigrate.Model
		if len(from) > 0 {
			fromModel = &sqlmigrate.Model{}
			decoder := json.NewDecoder(bytes.NewReader(from))
			decoder.DisallowUnknownFields()
			if err := decoder.Decode(fromModel); err != nil {
				return nil, fmt.Errorf("the model the deploy manifest records for %s: %w", service, err)
			}
		}
		reads, err := c.version.reads(service, to)
		if err != nil {
			return nil, err
		}
		plan, err := sqlmigrate.Diff(fromModel, to, sqlmigrate.Options{ReadersAfter: reads})
		if err != nil {
			return nil, err
		}
		return stackdeploy.PlanOf(plan)
	}
}

// --- bootstrap ---

func newStackBootstrapCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	var repository string
	cmd := &cobra.Command{
		Use:   "bootstrap <environment>",
		Short: "Prepare the cloud project an environment deploys to",
		Long: `bootstrap prepares the cloud project an environment deploys to, with an
owner's credentials (application default credentials on gcp), and is safe
to run again. The environment's target does the work: on gcp it enables
the APIs, creates the state bucket and its KMS key, applies the Artifact
Registry repository, the deployer and planner accounts and Workload
Identity Federation for the GitHub repository the git remote names, and
creates the secret of each platform credential the environment needs.
Then it asks for each credential that has no value, without echoing it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openDeployContext(cmd, a, flags, args[0], false)
			if err != nil {
				return err
			}
			defer c.close()
			if !cmd.Flags().Changed("repository") {
				repository = gitHubRepository(cmd.Context(), filepath.Dir(c.version.servicesRoot))
			}
			return stackdeploy.Bootstrap(cmd.Context(), stackdeploy.BootstrapOptions{
				Options:    c.options(cmd, nil),
				Repository: repository,
				Prompter:   terminalPrompter(cmd),
			})
		},
	}
	flags.register(cmd, false)
	cmd.Flags().StringVar(&repository, "repository", "", "the GitHub repository the CI runs in, owner/name (default: read from the git remote origin)")
	return cmd
}

// gitHubRepositoryPattern reads owner/name from a GitHub remote URL, over
// HTTPS or SSH.
var gitHubRepositoryPattern = regexp.MustCompile(`^(?:https://(?:[^@/]+@)?github\.com/|git@github\.com:|ssh://git@github\.com/)([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+?)(?:\.git)?/?$`)

// gitHubRepository returns the GitHub repository the git remote origin of
// dir names, or "" when there is none.
func gitHubRepository(ctx context.Context, dir string) string {
	out, err := exec.CommandContext(ctx, "git", "-C", dir, "remote", "get-url", "origin").Output()
	if err != nil {
		return ""
	}
	return parseGitHubRemote(strings.TrimSpace(string(out)))
}

func parseGitHubRemote(url string) string {
	if m := gitHubRepositoryPattern.FindStringSubmatch(url); m != nil {
		return m[1]
	}
	return ""
}

// --- secrets set ---

func newStackSecretsSetCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	cmd := &cobra.Command{
		Use:   "set <environment> [Type.FIELD]",
		Short: "Enter the value of each secret of an environment that has none, or of the one named",
		Long: `set asks for the value of every secret of the environment that has none,
and every platform credential, without echoing it, and stores each in the
target's secret store (Secret Manager on gcp). Name one secret, by the
type that declares it and its field (PaymentsSecrets.STRIPE_KEY), to set
it whether or not it has a value. A value is never written to a file.

An application secret's storage is created by the deploy's infrastructure
step, so on a fresh environment run stack deploy first: it stops for the
values it lacks, and asks for them when it runs at a terminal.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			c, err := openDeployContext(cmd, a, flags, args[0], true)
			if err != nil {
				return err
			}
			defer c.close()
			var store registry.SecretStore
			if c.env.Target == local.Target {
				store = c.localSecrets()
			}
			only := ""
			if len(args) == 2 {
				only = args[1]
			}
			prompter := terminalPrompter(cmd)
			if prompter == nil {
				return errors.New("stack secrets set asks for each value at a terminal, and stdin is not one")
			}
			stored, err := stackdeploy.SetSecrets(cmd.Context(), stackdeploy.SecretsOptions{
				Options:  c.options(cmd, nil),
				Only:     only,
				Prompter: prompter,
				Store:    store,
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "stored %d secret value(s) of %s\n", len(stored), args[0])
			return err
		},
	}
	flags.register(cmd, false)
	return cmd
}

// --- plan ---

func newStackPlanCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	gate := &gateFlags{}
	var images []string
	var out, format string
	cmd := &cobra.Command{
		Use:   "plan <environment>",
		Short: "Show what a deploy of an environment would change, and its migration plans",
		Long: `plan shows what stack deploy would do, and changes nothing: the
provisioner's plan of every resource, with each server's image the deploy
manifest records or --image names pinned, and the migration plan of each
database from the schema the manifest records (D27). It also lists the
secrets with no value, the servers with no image yet, and, for a domain
no DNS platform holds, the records to create by hand. The read-only
planner account runs it in CI.

With --fail-on, which is every hazard class by default, it exits 1 after
printing when a plan has a hazard no --allow names. --out writes the plan
as JSON; stack deploy --expect checks that it runs the same migration
plans.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if format != "text" && format != "json" {
				return fmt.Errorf("--format %q: want text or json", format)
			}
			params, err := parseParams(flags.params)
			if err != nil {
				return err
			}
			imgs, err := stackdeploy.ParseImages(images)
			if err != nil {
				return err
			}
			g, err := gate.gate()
			if err != nil {
				return err
			}
			c, err := openDeployContext(cmd, a, flags, args[0], false)
			if err != nil {
				return err
			}
			defer c.close()
			result, err := stackdeploy.Plan(cmd.Context(), stackdeploy.PlanOptions{
				Options: c.options(cmd, params),
				Images:  imgs,
				Planner: c.planner(),
				Gate:    g,
			})
			if err != nil {
				return err
			}
			data, err := json.MarshalIndent(result, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
			if out != "" {
				if err := os.WriteFile(out, data, 0o644); err != nil {
					return fmt.Errorf("writing --out: %w", err)
				}
			}
			w := cmd.OutOrStdout()
			if format == "json" {
				_, err = w.Write(data)
			} else {
				err = writePlanText(w, args[0], result)
			}
			if err != nil {
				return err
			}
			if len(result.Unallowed) > 0 {
				return &stackdeploy.HazardsError{Hazards: result.Unallowed}
			}
			return nil
		},
	}
	flags.register(cmd, true)
	gate.register(cmd)
	cmd.Flags().StringArrayVar(&images, "image", nil, "a server's image: <server>=<repository>@sha256:<digest> (repeatable)")
	cmd.Flags().StringVar(&out, "out", "", "write the plan as JSON to this file, for stack deploy --expect")
	cmd.Flags().StringVar(&format, "format", "text", "print the plan as text or json")
	return cmd
}

// writePlanText prints a plan for a person.
func writePlanText(w io.Writer, environment string, r *stackdeploy.PlanResult) error {
	var b strings.Builder
	fmt.Fprintf(&b, "Plan of %s\n\nResources: %d change(s)\n", r.Run, len(r.Changes))
	for _, c := range r.Changes {
		fmt.Fprintf(&b, "  %-8s %s\n", c.Action, c.Resource)
	}
	if len(r.Pending) > 0 {
		b.WriteString("\nMigration phases a failed deploy left part-way, which the deploy finishes first:\n")
		for _, p := range r.Pending {
			fmt.Fprintf(&b, "  %s on %s: the %s phase of plan %s\n", p.Service, p.Database, p.Phase, p.Plan)
		}
	}
	for _, p := range r.Databases {
		fmt.Fprintf(&b, "\nMigration of %s on database %s: %d expand, %d contract step(s), plan %s\n", p.Service, p.Database, p.ExpandSteps, p.ContractSteps, p.Hash)
		if p.ExpandSteps+p.ContractSteps > 0 && p.Text != "" {
			for _, line := range strings.Split(strings.TrimRight(p.Text, "\n"), "\n") {
				fmt.Fprintf(&b, "  %s\n", line)
			}
		}
	}
	if len(r.Unallowed) > 0 {
		b.WriteString("\nHazards no --allow acknowledges:\n")
		for _, h := range r.Unallowed {
			fmt.Fprintf(&b, "  %s\n    %s\n", h.ID, h.Reason)
		}
	}
	if len(r.Unpinned) > 0 {
		fmt.Fprintf(&b, "\nServers with no image yet, planned at their repository: %s (deploy them with --image)\n", strings.Join(r.Unpinned, ", "))
	}
	if len(r.MissingSecrets) > 0 {
		fmt.Fprintf(&b, "\nSecrets with no value: %s (stack secrets set %s)\n", strings.Join(r.MissingSecrets, ", "), environment)
	}
	if len(r.ManualRecords) > 0 {
		b.WriteString("\nNo DNS platform holds the domain; create these records by hand:\n")
		for _, rec := range r.ManualRecords {
			name, _ := json.Marshal(rec.Name)
			value, _ := json.Marshal(rec.Value)
			fmt.Fprintf(&b, "  %s %s %s\n", name, rec.Type, value)
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// --- deploy ---

func newStackDeployCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	gate := &gateFlags{}
	var images []string
	var expect string
	cmd := &cobra.Command{
		Use:   "deploy <environment>",
		Short: "Deploy an environment in deploy order",
		Long: `deploy applies an environment in the order its resolution gives
(docs/stack-model.md, section 5.3): the infrastructure; the expand phase
of each database's migration, planned from the schema the deploy manifest
records; the servers, callees first, each wave once the platform reports
it ready; the contract phases; exposure. Then it writes the deploy
manifest to the target's state: the resolved environment, the IR digest of
each service, the image of each server and each database's schema.

Each server's image is given by digest with --image, or kept from the
manifest. Every secret needs a value before the first step after
infrastructure; at a terminal, deploy asks for each one missing. A
migration hazard of a --fail-on class stops the deploy unless --allow
names it, and --expect refuses migration plans other than the ones stack
plan --out wrote.

A rollout that fails runs no contract step: the manifest records the
schema between the phases, and the next deploy plans from it.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := parseParams(flags.params)
			if err != nil {
				return err
			}
			imgs, err := stackdeploy.ParseImages(images)
			if err != nil {
				return err
			}
			g, err := gate.gate()
			if err != nil {
				return err
			}
			var expected map[string]string
			if expect != "" {
				data, err := os.ReadFile(expect)
				if err != nil {
					return fmt.Errorf("--expect: %w", err)
				}
				var shown stackdeploy.PlanResult
				if err := json.Unmarshal(data, &shown); err != nil {
					return fmt.Errorf("--expect %s: not a plan stack plan --out wrote: %w", expect, err)
				}
				expected = shown.Expected()
			}
			c, err := openDeployContext(cmd, a, flags, args[0], false)
			if err != nil {
				return err
			}
			defer c.close()
			digests, err := c.digests()
			if err != nil {
				return err
			}
			m, err := stackdeploy.Deploy(cmd.Context(), stackdeploy.DeployOptions{
				Options:  c.options(cmd, params),
				Images:   imgs,
				Planner:  c.planner(),
				Services: digests,
				Gate:     g,
				Expected: expected,
				Prompter: terminalPrompter(cmd),
			})
			if err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "deployed %s\n", m.Run)
			return err
		},
	}
	flags.register(cmd, true)
	gate.register(cmd)
	cmd.Flags().StringArrayVar(&images, "image", nil, "a server's image: <server>=<repository>@sha256:<digest> (repeatable)")
	cmd.Flags().StringVar(&expect, "expect", "", "a plan stack plan --out wrote: refuse migration plans other than its")
	return cmd
}

// --- destroy ---

func newStackDestroyCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	var yes bool
	cmd := &cobra.Command{
		Use:   "destroy <environment>",
		Short: "Remove every resource of a run of an environment, and its deploy manifest",
		Long: `destroy removes every resource the provisioner applied for the run, and
its deploy manifest. It asks for the run's name at a terminal, unless
--yes. A database's data goes with it, unless its platform protects it
(deletion protection on a production Cloud SQL instance).`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := parseParams(flags.params)
			if err != nil {
				return err
			}
			c, err := openDeployContext(cmd, a, flags, args[0], false)
			if err != nil {
				return err
			}
			defer c.close()
			o := c.options(cmd, params)
			if err := o.Run.Check(); err != nil {
				return err
			}
			if !yes {
				if err := confirmDestroy(cmd, o.Run.Name()); err != nil {
					return err
				}
			}
			if err := stackdeploy.Destroy(cmd.Context(), o); err != nil {
				return err
			}
			_, err = fmt.Fprintf(cmd.OutOrStdout(), "destroyed %s\n", o.Run.Name())
			return err
		},
	}
	flags.register(cmd, true)
	cmd.Flags().BoolVar(&yes, "yes", false, "destroy without asking")
	return cmd
}

// confirmDestroy asks for the run's name at a terminal.
func confirmDestroy(cmd *cobra.Command, run string) error {
	if !isTerminal(cmd.InOrStdin()) {
		return fmt.Errorf("destroy asks for the run's name at a terminal, and stdin is not one: pass --yes to destroy %s", run)
	}
	if _, err := fmt.Fprintf(cmd.ErrOrStderr(), "Type %s to destroy every resource of it: ", run); err != nil {
		return err
	}
	line, err := bufio.NewReader(cmd.InOrStdin()).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return err
	}
	if strings.TrimSpace(line) != run {
		return errors.New("destroy: the name did not match; nothing was destroyed")
	}
	return nil
}

// --- outputs ---

func newStackOutputsCmd(a *app) *cobra.Command {
	flags := &stackFlags{}
	var out string
	cmd := &cobra.Command{
		Use:   "outputs <environment>",
		Short: "Print the outputs file of a run's applied resources",
		Long: `outputs prints the run's outputs file, the outputs.json the bindings
generator reads (docs/stack-model.md, section 6.6): a JSON object with the
format's version, the stack, the environment, the run's parameter values
and, under "resources", the outputs the provisioner reads from the run's
applied resources, by node ID and output name, leaving out secret ones.
--out writes the same file to a path in place of stdout.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			params, err := parseParams(flags.params)
			if err != nil {
				return err
			}
			c, err := openDeployContext(cmd, a, flags, args[0], false)
			if err != nil {
				return err
			}
			defer c.close()
			outputs, err := stackdeploy.Outputs(cmd.Context(), c.options(cmd, params))
			if err != nil {
				return err
			}
			data, err := outputs.Marshal()
			if err != nil {
				return err
			}
			if out != "" {
				return os.WriteFile(out, data, 0o644)
			}
			_, err = cmd.OutOrStdout().Write(data)
			return err
		},
	}
	flags.register(cmd, true)
	cmd.Flags().StringVar(&out, "out", "", "write the outputs file to this path in place of stdout")
	return cmd
}

// --- the terminal ---

// newPrompter makes the prompter a command asks for secret values with;
// a test replaces it.
var newPrompter = func(cmd *cobra.Command) stackdeploy.Prompter {
	in, ok := cmd.InOrStdin().(*os.File)
	if !ok || !isTerminal(in) {
		return nil
	}
	return &ttyPrompter{in: in, reader: bufio.NewReader(in), out: cmd.ErrOrStderr()}
}

// terminalPrompter returns the prompter over the command's terminal, or
// nil when stdin is not one.
func terminalPrompter(cmd *cobra.Command) stackdeploy.Prompter { return newPrompter(cmd) }

// isTerminal reports whether r is a terminal: a character device stty
// can read the settings of, which /dev/null is not.
func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	if !ok || runtime.GOOS == "windows" {
		return false
	}
	info, err := f.Stat()
	if err != nil || info.Mode()&os.ModeCharDevice == 0 {
		return false
	}
	cmd := exec.Command("stty", "-g")
	cmd.Stdin = f
	return cmd.Run() == nil
}

// ttyPrompter reads a line from the terminal with its echo turned off.
type ttyPrompter struct {
	in     *os.File
	reader *bufio.Reader
	out    io.Writer
}

func (p *ttyPrompter) Secret(prompt string) ([]byte, error) {
	if _, err := io.WriteString(p.out, prompt); err != nil {
		return nil, err
	}
	restore, err := hideInput(p.in)
	if err != nil {
		return nil, err
	}
	line, err := p.reader.ReadString('\n')
	restore()
	_, _ = io.WriteString(p.out, "\n")
	if err != nil && !errors.Is(err, io.EOF) {
		return nil, err
	}
	return []byte(strings.TrimRight(line, "\r\n")), nil
}

// hideInput turns the terminal's echo off with stty, and returns what
// turns it back on.
func hideInput(in *os.File) (restore func(), err error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New("hidden input needs stty, which Windows lacks: enter secrets from a Unix terminal")
	}
	stty := func(arg string) error {
		cmd := exec.Command("stty", arg)
		cmd.Stdin = in
		return cmd.Run()
	}
	if err := stty("-echo"); err != nil {
		return nil, fmt.Errorf("turn the terminal's echo off: %w", err)
	}
	return func() { _ = stty("echo") }, nil
}
