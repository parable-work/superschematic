package cli

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/generator/tsutil"
	"github.com/parable-work/superschematic/internal/loader/schemaconfig"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/sentinel"
	"github.com/parable-work/superschematic/internal/stack/local"
	"github.com/parable-work/superschematic/internal/stackinit"
	ir "github.com/parable-work/superschematic/ir"
)

// `stack init` writes a Stack service over a tree that already has API
// services (docs/stack-model.md, section 11.1, D59): the stack's files, a
// local environment, a gcp one when given a project and a region, and the
// ignore rules. It deploys nothing.

func init() { registerStackCommands(newStackInitCmd) }

// cloudTarget is the target of the cloud environment init writes: gcp,
// which the installed binary links (extensions/gcp) and the core alone
// does not. A test names stacktest's fake target in its place.
var cloudTarget = "gcp"

// cloudflareTarget is the target of the cloud environment init writes from
// a Cloudflare account: cloudflare, which the installed binary links
// (extensions/cloudflare, D64). A test names a target of its own.
var cloudflareTarget = "cloudflare"

// stackInitFlags holds `stack init`'s flag values.
type stackInitFlags struct {
	name              string
	gcpProject        string
	region            string
	cloudflareAccount string
	environment       string
	ci                []string
	expose            []string
	force             bool
	namingPath        string
}

func newStackInitCmd(a *app) *cobra.Command {
	flags := &stackInitFlags{}
	cmd := &cobra.Command{
		Use:   "init [<services-root>]",
		Short: "Write a Stack service over the tree's APIs, with a local environment and the ignore rules",
		Long: `init writes a Stack service in a schemas tree that already has API services
(docs/stack-model.md, section 11.1): its package.json when its siblings
have one, tsconfig.json, schema.config.ts and src/stack.schema.ts, written
by the TypeScript schema writer. It deploys nothing and runs nothing.

The stack deploys every API and every Site service of the tree; the DB and
Bucket services they reach come with them. It exposes each API an end user
or a third party calls, one with an operation that is not @requireService,
its own clause or its set's, and each API a site calls. An API whose every
operation is @requireService, or that has none, is internal. --expose
names the APIs to expose in their place, or none.

Its environments are Dev, on the local target that stack dev runs, and,
given a Google Cloud project and region, one on the gcp target, Staging
unless --environment names it, with src/gcp-target.ts typing the target's
values and settings for tsc. Given a Cloudflare account's id instead
(--cloudflare-account), that environment is on the cloudflare target, with
src/cloudflare-target.ts. Only the installed binary links the gcp and
cloudflare targets. --ci github turns on the stack's generated GitHub
Actions workflow (outputs.ci).

It adds to the .gitignore that covers the schemas root, the nearest one at
or above it in its repository, else a new one there: the output root's
contents but its Bun lockfile, /dist/* and !/dist/bun.lock (D51), and
/.superschematic/. A rule that ignores dist itself becomes the rule on its
contents, and where a broader rule still hides the lockfile, !/dist/ takes
the directory back. Running init again adds nothing.

At a terminal it asks for each value no flag gives, with defaults read from
the tree and from gcloud's configuration (project, run/region or
compute/region). Without one, it takes the flags and the defaults: no flag
at all writes a stack named after the tree with Dev alone. A tree with a
Stack service already is refused without --force, which replaces the stack
of the same name, its config and src, or writes another beside it.

Last, it loads the stack and resolves each environment, as the stack
generator does, and prints the commands to run next.

Without a directory, init writes under ./schemas/services.

Examples:
  superschematic stack init
  superschematic stack init --gcp-project acme-staging --region us-east1 --ci github
  superschematic stack init --cloudflare-account 0123456789abcdef0123456789abcdef
  superschematic stack init ./schemas/services --name shop-stack --expose shop-api`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			root := defaultServicesRoot
			if len(args) == 1 {
				root = args[0]
			}
			return runStackInit(cmd, a, flags, root)
		},
	}
	cmd.Flags().StringVar(&flags.name, "name", "", "the stack service's name (default: the first word its services' names share, then -stack, such as shop-stack)")
	cmd.Flags().StringVar(&flags.gcpProject, "gcp-project", "", "the Google Cloud project of a gcp environment")
	cmd.Flags().StringVar(&flags.region, "region", "", "the region of the gcp environment, such as us-east1")
	cmd.Flags().StringVar(&flags.cloudflareAccount, "cloudflare-account", "", "the Cloudflare account of a cloudflare environment: its id, 32 hexadecimal digits from the dashboard")
	cmd.Flags().StringVar(&flags.environment, "environment", "", "the cloud environment's name (default "+stackinit.DefaultCloudEnvironment+")")
	cmd.Flags().StringSliceVar(&flags.ci, "ci", nil, "the CI renderer the stack's config turns on, github for GitHub Actions (outputs.ci)")
	cmd.Flags().StringSliceVar(&flags.expose, "expose", nil, "the API services to expose, comma-separated, in place of those derived from their operations; none exposes none")
	cmd.Flags().BoolVar(&flags.force, "force", false, "write the stack although the tree has a Stack service: replace the one of the same name, or add another")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <services-root>/../superschematic.toml)")
	return cmd
}

// stackInitTree is the tree init writes into, as discovery reads it.
type stackInitTree struct {
	servicesRoot string
	schemasRoot  string
	services     []buildplan.Service
	apis         []buildplan.Service
	sites        []buildplan.Service
	stacks       []buildplan.Service
}

// kinds maps each service's name to its kind.
func (t *stackInitTree) kinds() map[string]ir.SchemaKind {
	out := make(map[string]ir.SchemaKind, len(t.services))
	for _, svc := range t.services {
		out[svc.Name] = svc.Config.Kind
	}
	return out
}

// deployed lists the names of the services the stack deploys, APIs first.
func (t *stackInitTree) deployed() []string {
	var names []string
	for _, svc := range t.apis {
		names = append(names, svc.Name)
	}
	for _, svc := range t.sites {
		names = append(names, svc.Name)
	}
	return names
}

// siteCalls maps each site to the APIs it calls.
func (t *stackInitTree) siteCalls() map[string][]string {
	out := map[string][]string{}
	for _, site := range t.sites {
		for _, call := range site.Config.Calls {
			out[site.Name] = append(out[site.Name], call.Name)
		}
	}
	return out
}

// stackInitValues are what init writes, from the flags and the answers.
type stackInitValues struct {
	name   string
	cloud  *stackinit.Environment
	ci     []string
	expose []string
	// exposeFrom names what gave the expose list in place of the derived
	// one: --expose, or the answer; empty for the derived list.
	exposeFrom string
}

func runStackInit(cmd *cobra.Command, a *app, flags *stackInitFlags, servicesRoot string) error {
	servicesRoot, err := filepath.Abs(servicesRoot)
	if err != nil {
		return err
	}
	if info, err := os.Stat(servicesRoot); err != nil || !info.IsDir() {
		return fmt.Errorf("services root not found: %s; name the directory that holds the schema services", servicesRoot)
	}
	tree := &stackInitTree{servicesRoot: servicesRoot, schemasRoot: filepath.Dir(servicesRoot)}
	names, err := resolveNaming(flags.namingPath, tree.schemasRoot)
	if err != nil {
		return err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	// The stack's schema imports each service's handle, and a config may
	// import a sibling's (D34).
	if err := buildplan.EnsureSentinels(servicesRoot, reg, cmd.OutOrStdout()); err != nil {
		return err
	}
	if tree.services, err = buildplan.DiscoverWith(servicesRoot, filepath.Join(tree.schemasRoot, "dist"), reg); err != nil {
		return err
	}
	for _, svc := range tree.services {
		switch svc.Config.Kind {
		case ir.SchemaKindAPI:
			tree.apis = append(tree.apis, svc)
		case ir.SchemaKindSite:
			tree.sites = append(tree.sites, svc)
		case ir.SchemaKindStack:
			tree.stacks = append(tree.stacks, svc)
		}
	}
	byName := func(a, b buildplan.Service) int { return strings.Compare(a.Name, b.Name) }
	slices.SortFunc(tree.apis, byName)
	slices.SortFunc(tree.sites, byName)
	if len(tree.apis) == 0 {
		return fmt.Errorf("no API service under %s: a stack deploys a tree's APIs, so write one first", servicesRoot)
	}
	if len(tree.stacks) > 0 && !flags.force {
		var have []string
		for _, svc := range tree.stacks {
			have = append(have, fmt.Sprintf("%s (%s)", svc.Name, displayPath(svc.Dir)))
		}
		return fmt.Errorf("the tree has a Stack service already, %s; pass --force to replace the one --name names or to write another beside it", strings.Join(have, ", "))
	}
	_, cloudLinked := reg.Target(cloudTarget)
	gcpFlags := flags.gcpProject != "" || flags.region != ""
	if flags.cloudflareAccount != "" && gcpFlags {
		return errors.New("stack init writes one cloud environment: give --gcp-project and --region, or --cloudflare-account")
	}
	if (gcpFlags || flags.environment != "" && flags.cloudflareAccount == "") && !cloudLinked {
		return fmt.Errorf("a gcp environment needs the %s target, which this binary does not link (its targets: %s); the installed superschematic links it", cloudTarget, strings.Join(reg.Targets(), ", "))
	}
	if _, ok := reg.Target(cloudflareTarget); flags.cloudflareAccount != "" && !ok {
		return fmt.Errorf("a cloudflare environment needs the %s target, which this binary does not link (its targets: %s); the installed superschematic links it", cloudflareTarget, strings.Join(reg.Targets(), ", "))
	}
	for _, renderer := range flags.ci {
		if _, ok := reg.CIRenderer(renderer); !ok {
			return fmt.Errorf("--ci %s: no CI renderer of that name (registered: %s)", renderer, strings.Join(reg.CIRenderers(), ", "))
		}
	}
	sibling, err := stackSibling(tree)
	if err != nil {
		return err
	}

	prompter := newLinePrompter(cmd)
	var gcloud func() gcloudDefaults
	if prompter != nil && cloudLinked && flags.cloudflareAccount == "" && (flags.gcpProject == "" || flags.region == "") {
		gcloud = startGcloudDefaults(cmd.Context())
	}

	// Which APIs end users call decides what is exposed, so init reads
	// each API's operations.
	version, err := openSchemaVersion(servicesRoot, names, reg)
	if err != nil {
		return err
	}
	defer version.close()
	apis := map[string]*ir.Schema{}
	for _, svc := range tree.apis {
		schema, _, err := version.load(svc.Name)
		if err != nil {
			return fmt.Errorf("stack init reads each API's operations to decide what to expose: %w", err)
		}
		apis[svc.Name] = schema
	}
	derived, reasons := stackinit.ExposeFor(apis, tree.siteCalls())

	values, err := stackInitAnswers(cmd, flags, tree, names, derived, prompter, gcloud, reg)
	if err != nil {
		return err
	}
	if values.exposeFrom != "" {
		for _, svc := range tree.apis {
			r := stackinit.Exposure{Exposed: slices.Contains(values.expose, svc.Name)}
			if r.Exposed {
				r.Reason = values.exposeFrom + " names it"
			} else {
				r.Reason = values.exposeFrom + " leaves it out"
			}
			reasons[svc.Name] = r
		}
	}

	dir := filepath.Join(servicesRoot, values.name)
	if err := stackDirProblem(tree, dir, flags.force); err != nil {
		return err
	}
	if err := clearStackDir(tree, dir); err != nil {
		return err
	}
	kinds := tree.kinds()
	plan := &stackinit.Plan{
		Name:        values.name,
		Deploy:      stackinit.Refs(tree.deployed(), kinds),
		Expose:      stackinit.Refs(values.expose, kinds),
		ExposeNamed: values.exposeFrom != "",
		CI:          values.ci,
		Environments: []stackinit.Environment{{
			Name:    stackinit.LocalEnvironment,
			Target:  local.Target,
			Comment: "This machine: superschematic stack dev runs each database and bucket in a\ncontainer and each server as a process, and derives every connection.",
		}},
	}
	if values.cloud != nil {
		plan.Environments = append(plan.Environments, *values.cloud)
	}
	files, err := stackinit.Render(plan, sibling, names, reg)
	if err != nil {
		return err
	}
	for _, f := range files {
		path := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(path, f.Content, 0o644); err != nil {
			return err
		}
	}
	edit, err := stackinit.EnsureIgnoreRules(tree.schemasRoot)
	if err != nil {
		return err
	}

	out := cmd.OutOrStdout()
	printStackInitSummary(out, tree, plan, files, dir, reasons, edit)
	// A stack that does not resolve is written all the same: what it
	// lacks, such as a required config field's value, only a person
	// knows.
	if err := checkWrittenStack(version, plan.Name, dir, reg); err != nil {
		_, _ = fmt.Fprintf(out, "\nThe stack does not resolve yet. Set what each problem names in %s; a build of the stack checks it again:\n%s\n",
			displayPath(filepath.Join(dir, filepath.FromSlash(stackinit.SchemaFile))), indent(err.Error()))
	}
	printStackInitNext(out, tree, plan, dir)
	return nil
}

// indent indents each line of s by two spaces.
func indent(s string) string {
	return "  " + strings.ReplaceAll(strings.TrimRight(s, "\n"), "\n", "\n  ")
}

// stackSibling reads the API service whose tsconfig.json the stack's
// copies, the first by name, and refuses a tree whose deployed services
// have no handle for the stack's schema to import.
func stackSibling(tree *stackInitTree) (stackinit.Sibling, error) {
	for _, svc := range append(slices.Clone(tree.apis), tree.sites...) {
		if _, err := os.Stat(filepath.Join(svc.Dir, filepath.FromSlash(sentinel.GeneratedFile))); err != nil {
			return stackinit.Sibling{}, fmt.Errorf("stack init writes the stack in TypeScript, which imports each service's handle, and %s has none: give it a tsconfig.json, as a TypeScript service has, so the build writes %s", svc.Name, sentinel.GeneratedFile)
		}
	}
	for _, svc := range tree.apis {
		data, err := os.ReadFile(filepath.Join(svc.Dir, stackinit.TSConfig))
		if err != nil {
			continue
		}
		_, err = os.Stat(filepath.Join(svc.Dir, stackinit.PackageJSON))
		return stackinit.Sibling{TSConfig: data, HasPackageJSON: err == nil}, nil
	}
	return stackinit.Sibling{}, errors.New("no API service has a tsconfig.json for the stack's to follow")
}

// stackDirProblem refuses a directory init may not write the stack into:
// another kind of service, a Stack service without --force, or a
// directory with files in it without --force.
func stackDirProblem(tree *stackInitTree, dir string, force bool) error {
	if svc, ok := tree.serviceAt(dir); ok {
		switch {
		case svc.Config.Kind != ir.SchemaKindStack:
			return fmt.Errorf("%s is the %s service %s; name the stack another way", displayPath(dir), svc.Config.Kind, svc.Name)
		case !force:
			return fmt.Errorf("the Stack service %s exists; pass --force to replace it", svc.Name)
		}
		return nil
	}
	entries, err := os.ReadDir(dir)
	switch {
	case errors.Is(err, os.ErrNotExist):
		return nil
	case err != nil:
		return err
	case len(entries) > 0 && !force:
		return fmt.Errorf("%s exists and is no schema service; pass --force to write the stack into it", displayPath(dir))
	}
	return nil
}

// clearStackDir empties the Stack service --force replaces at dir: its
// configs, in any form, and its src, so no schema file of the old stack
// is read with the new one.
func clearStackDir(tree *stackInitTree, dir string) error {
	if _, ok := tree.serviceAt(dir); !ok {
		return nil
	}
	for _, name := range []string{"schema.config.ts", "schema.config.json", "schema.config.yaml"} {
		if err := os.Remove(filepath.Join(dir, name)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}
	return os.RemoveAll(filepath.Join(dir, "src"))
}

// serviceAt returns the service of the tree at dir.
func (t *stackInitTree) serviceAt(dir string) (buildplan.Service, bool) {
	for _, svc := range t.services {
		if filepath.Clean(svc.Dir) == filepath.Clean(dir) {
			return svc, true
		}
	}
	return buildplan.Service{}, false
}

// checkWrittenStack loads the stack init wrote and resolves each of its
// environments, as the stack generator does, over the services version
// already loaded.
func checkWrittenStack(version *schemaVersion, name, dir string, reg *registry.Registry) error {
	cfg, err := buildplan.ReadConfig(dir, reg)
	if err != nil {
		return err
	}
	// A stack --force replaced is in discovery's list already, its old
	// config with it.
	version.services = slices.DeleteFunc(version.services, func(svc buildplan.Service) bool { return svc.Name == name })
	version.services = append(version.services, buildplan.Service{Name: name, Dir: dir, Config: cfg})
	schema, config, err := version.load(name)
	if err != nil {
		return err
	}
	st := ir.StackOf(schema)
	if st == nil {
		return fmt.Errorf("stack %s: no class declares @stack", name)
	}
	gen := registry.GenerateContext{
		Schema:   schema,
		Config:   config,
		Registry: reg,
		LoadDependency: func(name string) (*ir.Schema, error) {
			s, _, err := version.load(name)
			return s, err
		},
		Options: registry.Options{LoadDependencyConfig: func(name string) (*schemaconfig.SchemaConfig, error) {
			_, c, err := version.load(name)
			return c, err
		}},
	}
	services, err := stackgen.Services(gen, st)
	if err != nil {
		return err
	}
	_, err = stackgen.Resolve(gen, st, services)
	return err
}

// printStackInitSummary says what init wrote and why the stack holds what
// it does.
func printStackInitSummary(w io.Writer, tree *stackInitTree, plan *stackinit.Plan, files []stackinit.File, dir string, reasons map[string]stackinit.Exposure, edit *stackinit.IgnoreEdit) {
	var b strings.Builder
	fmt.Fprintf(&b, "Wrote the Stack service %s in %s:\n", plan.Name, displayPath(dir))
	for _, f := range files {
		fmt.Fprintf(&b, "  %s\n", f.Path)
	}
	fmt.Fprintf(&b, "\nIt deploys %s; the databases and buckets they reach come with them.\n", joinNames(tree.deployed()))
	width := 0
	for _, svc := range tree.apis {
		width = max(width, len(svc.Name))
	}
	for _, svc := range tree.apis {
		r := reasons[svc.Name]
		state := "internal"
		if r.Exposed {
			state = "exposed"
		}
		fmt.Fprintf(&b, "  %-*s  %s: %s\n", width, svc.Name, state, r.Reason)
	}
	if len(tree.sites) > 0 {
		fmt.Fprintf(&b, "  A site is always exposed: %s.\n", joinNames(siteNames(tree)))
	}
	b.WriteString("\nEnvironments:\n")
	for _, env := range plan.Environments {
		fmt.Fprintf(&b, "  %s on %s", env.Name, env.Target)
		if project, ok := env.Values["project"]; ok {
			fmt.Fprintf(&b, ", project %v in %v", project, env.Values["region"])
		}
		if account, ok := env.Values["accountId"]; ok {
			fmt.Fprintf(&b, ", account %v", account)
		}
		b.WriteString("\n")
	}
	if len(plan.CI) > 0 {
		fmt.Fprintf(&b, "\nCI: outputs.ci writes the %s workflow on each build of the stack.\n", strings.Join(plan.CI, ", "))
	}
	file := displayPath(edit.File)
	switch {
	case edit.Changed():
		var changes []string
		for _, from := range sortedStrings(edit.Replaced) {
			changes = append(changes, fmt.Sprintf("rewrote %s as %s", from, edit.Replaced[from]))
		}
		if len(edit.Added) > 0 {
			changes = append(changes, "added "+strings.Join(edit.Added, ", "))
		}
		verb := "In"
		if edit.Created {
			verb = "Created"
		}
		fmt.Fprintf(&b, "\n%s %s: %s.\n", verb, file, strings.Join(changes, "; "))
	default:
		fmt.Fprintf(&b, "\n%s has the ignore rules already.\n", file)
	}
	if edit.StillIgnored != "" {
		fmt.Fprintf(&b, "git still ignores the output root's bun.lock, by %s; change that rule so the project can commit it (D51).\n", edit.StillIgnored)
	}
	_, _ = io.WriteString(w, b.String())
}

// printStackInitNext prints the commands to run next, naming the stack
// directory when the stack commands would not find it on their own.
func printStackInitNext(w io.Writer, tree *stackInitTree, plan *stackinit.Plan, dir string) {
	// The stack commands take the working directory when it is a Stack
	// service, else the one Stack service under ./schemas/services.
	found := true
	if cwd, err := os.Getwd(); err == nil {
		alone := len(tree.stacks) == 0 || (len(tree.stacks) == 1 && filepath.Clean(tree.stacks[0].Dir) == filepath.Clean(dir))
		found = filepath.Clean(dir) == filepath.Clean(cwd) ||
			(filepath.Clean(tree.servicesRoot) == filepath.Join(cwd, defaultServicesRoot) && alone)
	}
	devDir, stackFlag := "", ""
	if !found {
		devDir = " " + displayPath(dir)
		stackFlag = " --stack " + displayPath(dir)
	}
	var b strings.Builder
	b.WriteString("\nNext:\n")
	fmt.Fprintf(&b, "  superschematic stack dev%s --environment %s\n", devDir, stackinit.LocalEnvironment)
	for _, env := range plan.Environments {
		if env.Target != local.Target {
			fmt.Fprintf(&b, "  superschematic stack bootstrap %s%s\n", env.Name, stackFlag)
		}
	}
	_, _ = io.WriteString(w, b.String())
}

func siteNames(tree *stackInitTree) []string {
	var out []string
	for _, svc := range tree.sites {
		out = append(out, svc.Name)
	}
	return out
}

// displayPath is path relative to the working directory, when it lies
// under it.
func displayPath(path string) string {
	cwd, err := os.Getwd()
	if err != nil {
		return path
	}
	rel, err := filepath.Rel(cwd, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	if rel == "." {
		return "."
	}
	return "./" + filepath.ToSlash(rel)
}

// joinNames joins names as a sentence lists them.
func joinNames(names []string) string {
	switch len(names) {
	case 0:
		return "nothing"
	case 1:
		return names[0]
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

func sortedStrings(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for key := range m {
		keys = append(keys, key)
	}
	slices.Sort(keys)
	return keys
}

// --- the values ---

// stackInitAnswers takes each value from its flag, asks for the rest at a
// terminal, and otherwise takes the defaults. derived is the expose list
// the APIs' operations give.
func stackInitAnswers(cmd *cobra.Command, flags *stackInitFlags, tree *stackInitTree, names naming.Naming, derived []string, p linePrompter, gcloud func() gcloudDefaults, reg *registry.Registry) (*stackInitValues, error) {
	ask := &asker{p: p}
	values := &stackInitValues{ci: flags.ci}
	handles := make([]string, 0, len(tree.services))
	for _, name := range tree.deployed() {
		handles = append(handles, tsutil.ToClassName(name))
	}
	if gcloud == nil {
		gcloud = func() gcloudDefaults { return gcloudDefaults{} }
	}

	// The name.
	checkName := func(name string) error {
		if err := stackinit.CheckName(name); err != nil {
			return err
		}
		return stackDirProblem(tree, filepath.Join(tree.servicesRoot, name), flags.force)
	}
	values.name = flags.name
	if values.name == "" {
		def := stackinit.DefaultName(tree.deployed(), names, tree.schemasRoot)
		if p == nil {
			values.name = def
		} else {
			var err error
			if values.name, err = ask.text("Stack service name", def, checkName); err != nil {
				return nil, err
			}
		}
	}
	if err := checkName(values.name); err != nil {
		return nil, err
	}

	// The gcp environment, or else the cloudflare one.
	if target, ok := reg.Target(cloudTarget); ok && flags.cloudflareAccount == "" {
		cloud, err := askCloudEnvironment(ask, flags, target, handles, gcloud)
		if err != nil {
			return nil, err
		}
		values.cloud = cloud
	}
	if target, ok := reg.Target(cloudflareTarget); ok && values.cloud == nil {
		cloud, err := askCloudflareEnvironment(ask, flags, target, handles)
		if err != nil {
			return nil, err
		}
		values.cloud = cloud
	}

	// The CI.
	if !cmd.Flags().Changed("ci") && p != nil {
		if _, ok := reg.CIRenderer("github"); ok {
			yes, err := ask.yesNo("Write a GitHub Actions workflow for the stack (outputs.ci)", false)
			if err != nil {
				return nil, err
			}
			if yes {
				values.ci = []string{"github"}
			}
		}
	}

	// What is exposed.
	deploy := stackinit.Refs(tree.deployed(), tree.kinds())
	check := func(list []string) error { return stackinit.CheckExpose(list, deploy, tree.siteCalls()) }
	values.expose = derived
	switch {
	case cmd.Flags().Changed("expose"):
		values.expose, values.exposeFrom = parseExpose(flags.expose), "--expose"
	case p != nil:
		def := "none"
		if len(derived) > 0 {
			def = strings.Join(derived, ", ")
		}
		answer, err := ask.text("APIs to expose, comma-separated, or none", def, func(s string) error {
			return check(parseExpose(strings.Split(s, ",")))
		})
		if err != nil {
			return nil, err
		}
		values.expose = parseExpose(strings.Split(answer, ","))
		if !slices.Equal(values.expose, derived) {
			values.exposeFrom = "the answer"
		}
	}
	if err := check(values.expose); err != nil {
		return nil, err
	}
	return values, nil
}

// parseExpose reads an expose list: names, or none for no API.
func parseExpose(list []string) []string {
	out := []string{}
	for _, item := range list {
		item = strings.TrimSpace(item)
		if item == "" || item == "none" || slices.Contains(out, item) {
			continue
		}
		out = append(out, item)
	}
	slices.Sort(out)
	return out
}

// askCloudEnvironment takes the gcp environment from the flags, asks for
// what they leave out at a terminal, and returns nil for none.
func askCloudEnvironment(ask *asker, flags *stackInitFlags, target registry.TargetSpec, handles []string, gcloud func() gcloudDefaults) (*stackinit.Environment, error) {
	project, region, name := flags.gcpProject, flags.region, flags.environment
	checkName := checkCloudEnvironmentName(handles)
	if ask.p == nil {
		switch {
		case project == "" && region == "" && name == "":
			return nil, nil
		case project == "" || region == "":
			return nil, errors.New("a gcp environment needs --gcp-project and --region together")
		}
	} else if project == "" && region == "" && name == "" {
		defaults := gcloud()
		yes, err := ask.yesNo("Add an environment on Google Cloud (the gcp target)", defaults.project != "")
		if err != nil || !yes {
			return nil, err
		}
	}
	if name == "" {
		name = stackinit.DefaultCloudEnvironment
		if ask.p != nil && flags.environment == "" {
			var err error
			if name, err = ask.text("Its environment name", name, checkName); err != nil {
				return nil, err
			}
		}
	}
	if err := checkName(name); err != nil {
		return nil, err
	}
	check := func(project, region string) error {
		if err := target.ValidateValues(map[string]any{"project": project, "region": region}); err != nil {
			return fmt.Errorf("the gcp environment's project %q and region %q: %w", project, region, err)
		}
		return nil
	}
	for ask.p != nil && (project == "" || region == "") {
		defaults := gcloud()
		var err error
		if project == "" {
			if project, err = ask.text("Google Cloud project", defaults.project, nonEmpty); err != nil {
				return nil, err
			}
		}
		if region == "" {
			if region, err = ask.text("Region", defaults.region, nonEmpty); err != nil {
				return nil, err
			}
		}
		if err := check(project, region); err != nil {
			ask.say(err.Error())
			project, region = flags.gcpProject, flags.region
		}
	}
	if err := check(project, region); err != nil {
		return nil, err
	}
	return &stackinit.Environment{
		Name:    name,
		Target:  cloudTarget,
		Values:  map[string]any{"project": project, "region": region},
		Comment: fmt.Sprintf("Google Cloud: superschematic stack bootstrap %s prepares the project once,\nthen superschematic stack deploy %s deploys to it.", name, name),
	}, nil
}

// checkCloudEnvironmentName refuses a cloud environment's name that is no
// class name, or that the local environment or a service's handle has.
func checkCloudEnvironmentName(handles []string) func(string) error {
	return func(n string) error {
		if err := stackinit.CheckEnvironment(n); err != nil {
			return err
		}
		if n == stackinit.LocalEnvironment || slices.Contains(handles, n) {
			return fmt.Errorf("environment name %s is taken: the local environment or a service's handle has it", n)
		}
		return nil
	}
}

// askCloudflareEnvironment takes the cloudflare environment from the
// flags, asks for what they leave out at a terminal, and returns nil for
// none (D64). Its one value is the account's id; bootstrap records the
// rest.
func askCloudflareEnvironment(ask *asker, flags *stackInitFlags, target registry.TargetSpec, handles []string) (*stackinit.Environment, error) {
	account, name := flags.cloudflareAccount, flags.environment
	checkName := checkCloudEnvironmentName(handles)
	check := func(account string) error {
		if err := target.ValidateValues(map[string]any{"accountId": account}); err != nil {
			return fmt.Errorf("the cloudflare environment's account %q: %w; the account's id is 32 hexadecimal digits, on the dashboard", account, err)
		}
		return nil
	}
	if account == "" {
		if ask.p == nil {
			return nil, nil
		}
		yes, err := ask.yesNo("Add an environment on Cloudflare (the cloudflare target)", false)
		if err != nil || !yes {
			return nil, err
		}
	}
	if name == "" {
		name = stackinit.DefaultCloudEnvironment
		if ask.p != nil {
			var err error
			if name, err = ask.text("Its environment name", name, checkName); err != nil {
				return nil, err
			}
		}
	}
	if err := checkName(name); err != nil {
		return nil, err
	}
	if account == "" {
		var err error
		if account, err = ask.text("Cloudflare account id", "", check); err != nil {
			return nil, err
		}
	}
	if err := check(account); err != nil {
		return nil, err
	}
	return &stackinit.Environment{
		Name:    name,
		Target:  cloudflareTarget,
		Values:  map[string]any{"accountId": account},
		Comment: fmt.Sprintf("Cloudflare: superschematic stack bootstrap %s prepares the account once,\nthen superschematic stack deploy %s deploys to it.", name, name),
	}, nil
}

func nonEmpty(s string) error {
	if s == "" {
		return errors.New("a value is needed")
	}
	return nil
}

// --- the terminal ---

// linePrompter asks a person for a line at a terminal, echoing what they
// type: the CLI's reads the command's stdin; tests answer from a script.
type linePrompter interface {
	// Line shows prompt and returns the line typed, without its line
	// ending. io.EOF is an error: nothing more will be typed.
	Line(prompt string) (string, error)
	// Say prints a line of its own.
	Say(line string)
}

// newLinePrompter makes the prompter stack init asks with, or nil when
// stdin is no terminal; a test replaces it.
var newLinePrompter = func(cmd *cobra.Command) linePrompter {
	if !isTerminal(cmd.InOrStdin()) {
		return nil
	}
	return &ttyLinePrompter{reader: bufio.NewReader(cmd.InOrStdin()), out: cmd.ErrOrStderr()}
}

type ttyLinePrompter struct {
	reader *bufio.Reader
	out    io.Writer
}

func (p *ttyLinePrompter) Line(prompt string) (string, error) {
	if _, err := io.WriteString(p.out, prompt); err != nil {
		return "", err
	}
	line, err := p.reader.ReadString('\n')
	if errors.Is(err, io.EOF) && line == "" {
		_, _ = io.WriteString(p.out, "\n")
		return "", io.EOF
	}
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	return strings.TrimRight(line, "\r\n"), nil
}

func (p *ttyLinePrompter) Say(line string) { _, _ = fmt.Fprintln(p.out, line) }

// asker asks questions with defaults over a linePrompter.
type asker struct{ p linePrompter }

// text asks a question whose default an empty line takes, until check
// passes.
func (a *asker) text(question, def string, check func(string) error) (string, error) {
	prompt := question + ": "
	if def != "" {
		prompt = fmt.Sprintf("%s [%s]: ", question, def)
	}
	for {
		line, err := a.p.Line(prompt)
		if err != nil {
			return "", fmt.Errorf("%s: %w", question, err)
		}
		answer := strings.TrimSpace(line)
		if answer == "" {
			answer = def
		}
		if err := check(answer); err != nil {
			a.say(err.Error())
			continue
		}
		return answer, nil
	}
}

// yesNo asks a yes-or-no question.
func (a *asker) yesNo(question string, def bool) (bool, error) {
	hint := "y/N"
	if def {
		hint = "Y/n"
	}
	for {
		line, err := a.p.Line(fmt.Sprintf("%s? [%s]: ", question, hint))
		if err != nil {
			return false, fmt.Errorf("%s: %w", question, err)
		}
		switch strings.ToLower(strings.TrimSpace(line)) {
		case "":
			return def, nil
		case "y", "yes":
			return true, nil
		case "n", "no":
			return false, nil
		}
		a.say("Answer y or n.")
	}
}

func (a *asker) say(line string) {
	if a.p != nil {
		a.p.Say(line)
	}
}

// --- gcloud ---

// gcloudDefaults are the project and region gcloud's active configuration
// names, each empty when it names none.
type gcloudDefaults struct {
	project string
	region  string
}

// gcloudTimeout bounds each read of gcloud's configuration.
var gcloudTimeout = 10 * time.Second

// readGcloudConfig reads one property of gcloud's active configuration, or
// "" when gcloud is missing, names none, or does not answer in time. It
// gives gcloud no stdin and turns its prompts off, so it cannot wait on a
// person. A test replaces it.
var readGcloudConfig = func(ctx context.Context, property string) string {
	path, err := exec.LookPath("gcloud")
	if err != nil {
		return ""
	}
	ctx, cancel := context.WithTimeout(ctx, gcloudTimeout)
	defer cancel()
	cmd := exec.CommandContext(ctx, path, "config", "get", property)
	cmd.Stdin = nil
	cmd.Env = append(os.Environ(), "CLOUDSDK_CORE_DISABLE_PROMPTS=1")
	cmd.WaitDelay = time.Second
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	value := strings.TrimSpace(string(out))
	if value == "(unset)" {
		return ""
	}
	return value
}

// startGcloudDefaults reads gcloud's project and region in the background,
// so init loads the tree meanwhile, and returns what waits for them. The
// region is run/region, Cloud Run's, else compute/region.
func startGcloudDefaults(ctx context.Context) func() gcloudDefaults {
	properties := []string{"project", "run/region", "compute/region"}
	values := make([]string, len(properties))
	var wg sync.WaitGroup
	for i, property := range properties {
		wg.Go(func() { values[i] = readGcloudConfig(ctx, property) })
	}
	return sync.OnceValue(func() gcloudDefaults {
		wg.Wait()
		region := values[1]
		if region == "" {
			region = values[2]
		}
		return gcloudDefaults{project: values[0], region: region}
	})
}
