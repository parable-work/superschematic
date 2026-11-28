package cli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/parable-work/superschematic/internal/buildcache"
	"github.com/parable-work/superschematic/internal/buildplan"
	"github.com/parable-work/superschematic/internal/generator/naming"
	"github.com/parable-work/superschematic/internal/generator/tasksgen"
	"github.com/parable-work/superschematic/internal/registry"
	inttasks "github.com/parable-work/superschematic/internal/tasks"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

// tasksFlags holds one tasks command's flag values.
type tasksFlags struct {
	check      bool
	namingPath string
}

// newTasksCmd is the Tasks kind's command (D203): the task file of a
// Tasks service, the plan of a commit, and the check of a schema input's
// static set against what a build read.
func newTasksCmd(a *app) *cobra.Command {
	flags := &tasksFlags{}
	cmd := &cobra.Command{
		Use:   "tasks <tasks-service-dir> [--check]",
		Short: "Write a Tasks service's task file, or check the committed one",
		Long: `tasks renders the task file a Tasks service declares and installs it at the
path the service's config names (outputs.tasks.install), from the
repository root: the file a forge reads at a commit. build-all installs
the same bytes on every run, so this command is for writing one service's
file without a build.

--check writes nothing. It fails when the committed file is missing or is
not what the schema gives, and prints the first line that differs. Run it
in CI, so a change to a task never lands without its file.

The render reads the schema and nothing of the machine, so one schema
gives one file from any checkout.

Examples:
  superschematic tasks ./schemas/services/repo-tasks
  superschematic tasks ./schemas/services/repo-tasks --check
  superschematic tasks plan HEAD --file ci/tasks.json
  superschematic tasks inputs ./schemas/services/repo-tasks`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTasksFile(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().BoolVar(&flags.check, "check", false, "write nothing; fail when the committed task file is not what would be written")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <service-dir>/../../superschematic.toml)")
	cmd.AddCommand(newTasksPlanCmd(a))
	cmd.AddCommand(newTasksInputsCmd(a))
	cmd.AddCommand(newTasksReachCmd(a))
	return cmd
}

// tasksService is a Tasks service a command opened: its schema, its
// config's options and the repository above it.
type tasksService struct {
	version *schemaVersion
	service buildplan.Service
	schema  *ir.Schema
	options tasksgen.Options
	// gitRoot is the repository root above the service, "" for none.
	gitRoot string
}

// openTasksService loads the Tasks service at servicePath.
func openTasksService(a *app, servicePath, namingPath, command string) (*tasksService, error) {
	absService, err := filepath.Abs(servicePath)
	if err != nil {
		return nil, fmt.Errorf("resolving service path: %w", err)
	}
	if info, err := os.Stat(absService); err != nil || !info.IsDir() {
		return nil, fmt.Errorf("service directory not found: %s", servicePath)
	}
	schemasRoot := filepath.Dir(filepath.Dir(absService))
	names, err := resolveNaming(namingPath, schemasRoot)
	if err != nil {
		return nil, err
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return nil, err
	}
	version, err := openSchemaVersion(filepath.Dir(absService), names, reg)
	if err != nil {
		return nil, err
	}
	opened := &tasksService{version: version}
	fail := func(err error) (*tasksService, error) {
		version.close()
		return nil, err
	}
	if opened.service, err = version.serviceAt(absService); err != nil {
		return fail(err)
	}
	if opened.service.Config.Kind != ir.SchemaKindTasks {
		return fail(fmt.Errorf("%s: %s is of kind %s; a %s service declares tasks", command, opened.service.Name, opened.service.Config.Kind, ir.SchemaKindTasks))
	}
	schema, cfg, err := version.load(opened.service.Name)
	if err != nil {
		return fail(err)
	}
	opened.schema = schema
	outputs, err := registry.ParseOutputs(cfg.Outputs, reg)
	if err != nil {
		return fail(err)
	}
	if err := registry.DecodeOutput(outputs, tasksgen.Name, &opened.options); err != nil {
		return fail(err)
	}
	if opened.options.Install != "" {
		if err := tasksgen.CheckInstall(opened.options.Install); err != nil {
			return fail(fmt.Errorf("%s: %s: %w", command, opened.service.Name, err))
		}
	}
	if opened.gitRoot, err = tasksgen.GitRoot(absService); err != nil {
		return fail(err)
	}
	return opened, nil
}

func runTasksFile(cmd *cobra.Command, a *app, flags *tasksFlags, servicePath string) error {
	opened, err := openTasksService(a, servicePath, flags.namingPath, "tasks")
	if err != nil {
		return err
	}
	defer opened.version.close()
	name := opened.service.Name
	rendered, err := tasksgen.Render(opened.version.reg, opened.schema, name, opened.options)
	if err != nil {
		return fmt.Errorf("tasks: %s: %w", name, err)
	}
	var installs []tasksgen.Artifact
	for _, artifact := range rendered {
		if artifact.Install != "" {
			installs = append(installs, artifact)
		}
	}
	switch {
	case len(installs) == 0:
		return fmt.Errorf("tasks: %s names no outputs.tasks.install, the path its task file is committed at; without one a build writes the file under the output root alone", name)
	case opened.gitRoot == "":
		return fmt.Errorf("tasks: no git repository holds %s, so its task file has no place to be committed at", opened.service.Dir)
	}
	out := cmd.OutOrStdout()
	for _, artifact := range installs {
		target := filepath.Join(opened.gitRoot, filepath.FromSlash(artifact.Install))
		if !flags.check {
			changed, err := tasksgen.Install(target, artifact)
			if err != nil {
				return fmt.Errorf("tasks: %s: %w", name, err)
			}
			state := "is current"
			if changed {
				state = "written"
			}
			if _, err := fmt.Fprintf(out, "%s: %s %s\n", name, target, state); err != nil {
				return err
			}
			continue
		}
		have, err := os.ReadFile(target)
		if os.IsNotExist(err) {
			return fmt.Errorf("tasks --check: %s is missing; run `tasks %s` and commit it", artifact.Install, servicePath)
		}
		if err != nil {
			return err
		}
		if !bytes.Equal(have, artifact.Data) {
			return fmt.Errorf("tasks --check: %s is not what %s gives (%s); run `tasks %s` and commit it", artifact.Install, name, firstDifference(have, artifact.Data), servicePath)
		}
		if _, err := fmt.Fprintf(out, "%s: %s is current\n", name, artifact.Install); err != nil {
			return err
		}
	}
	return nil
}

// firstDifference names the first line at which two files differ.
func firstDifference(have, want []byte) string {
	a, b := strings.Split(string(have), "\n"), strings.Split(string(want), "\n")
	for i := 0; i < len(a) || i < len(b); i++ {
		var left, right string
		if i < len(a) {
			left = a[i]
		}
		if i < len(b) {
			right = b[i]
		}
		if left != right {
			return fmt.Sprintf("line %d: committed %q, schema %q", i+1, strings.TrimSpace(left), strings.TrimSpace(right))
		}
	}
	return "the files differ"
}

// tasksPlanFlags holds one tasks plan command's flag values.
type tasksPlanFlags struct {
	file       string
	base       string
	repo       string
	namingPath string
	sandbox    bool
	profile    bool
}

func newTasksPlanCmd(a *app) *cobra.Command {
	flags := &tasksPlanFlags{}
	cmd := &cobra.Command{
		Use:   "plan <commit> --file <path>",
		Short: "Print the plan of a commit: each task with its input key",
		Long: `plan reads the task file a commit holds at --file and the commit's tree,
and prints the plan as JSON: each task with its work hash, its input key
and its edges, in an order that runs every task after the tasks it uses
and the tasks it follows. Two runs with one key did the same work, so a
forge that finds a trusted pass under a task's key need not run it.

A key covers the key version, the task's work fields, the tracked files
each input resolves to, by mode, path and git object id, and the key of
each task whose output the task uses. A task that is not hermetic has no
key.

plan reads the commit through git and nothing else: no working tree, no
index and no generated file, so a dirty checkout changes nothing, and it
runs no build tool. It is the code that must be trusted for keys, so it
ships in the image the task file names.

--base names the commit the plan's commit lands on. A task whose work
fields differ from the base's is then keyed on both definitions and on
the inputs of both, so a change that edits a task cannot reuse a pass of
the task it replaced. Those keys differ from the keys of the same tree
planned with no base: after the change lands, that task and each task
that uses it runs once more.

--sandbox holds the process to a resolver's rules while it resolves: it
starts no program, opens no socket, and opens, makes or removes no file.
A resolver that reads the working tree, runs a build tool or fetches then
fails with "operation not permitted", and the plan fails with it. It
needs Linux on amd64 or arm64 and is refused elsewhere. The filter cannot
be taken off, so the flag is for a process whose whole job is the plan,
as a forge's plan step is. It holds a resolver that slips to its rules;
it is no defence against a binary built to break them.

--profile writes what the plan cost to standard error: the time to read
the commit, and for each resolver kind the inputs the tasks name, how
many of those differ, the tracked files they resolve to and the time.
Tasks that name one unit share one resolution.

Examples:
  superschematic tasks plan HEAD --file ci/tasks.json
  superschematic tasks plan "$MERGE_COMMIT" --base origin/main --file ci/tasks.json
  superschematic tasks plan HEAD --file ci/tasks.json --sandbox --profile`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTasksPlan(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.file, "file", "", "the task file's path from the repository root, as the Tasks service's outputs.tasks.install names it (required)")
	cmd.Flags().StringVar(&flags.base, "base", "", "the commit the plan's commit lands on")
	cmd.Flags().StringVar(&flags.repo, "repo", ".", "a directory of the repository")
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default: the defaults)")
	cmd.Flags().BoolVar(&flags.sandbox, "sandbox", false, "hold the process to a resolver's rules while it resolves: no program, no socket, no file (Linux on amd64 or arm64)")
	cmd.Flags().BoolVar(&flags.profile, "profile", false, "write what the plan cost, by resolver kind, to standard error")
	return cmd
}

func runTasksPlan(cmd *cobra.Command, a *app, flags *tasksPlanFlags, revision string) error {
	if flags.file == "" {
		return fmt.Errorf("tasks plan: --file names the task file's path from the repository root, as ci/%s", tasks.FileName)
	}
	if err := tasksgen.CheckInstall(flags.file); err != nil {
		return fmt.Errorf("tasks plan: --file %q is no task file's path from the repository root", flags.file)
	}
	names := naming.Default()
	if flags.namingPath != "" {
		loaded, err := naming.LoadFile(flags.namingPath)
		if err != nil {
			return err
		}
		names = loaded
	}
	reg, err := a.resolveRegistry(names)
	if err != nil {
		return err
	}
	started := time.Now()
	repo, err := inttasks.OpenRepository(flags.repo)
	if err != nil {
		return fmt.Errorf("tasks plan: %w", err)
	}
	defer func() { _ = repo.Close() }()
	// Units of one workspace read the same manifests: a go.work and each
	// module it uses, a workspace's package.json files. Each is read from
	// git once for the plan.
	repo.CacheBlobs()

	in := inttasks.PlanInput{Registry: reg, Format: repo.Format()}
	if flags.profile {
		in.Timings = &inttasks.Timings{}
	}
	if in.Commit, err = repo.Commit(revision); err != nil {
		return fmt.Errorf("tasks plan: %w", err)
	}
	if in.File, err = readTaskFile(repo, in.Commit, flags.file); err != nil {
		return fmt.Errorf("tasks plan: %w", err)
	}
	if in.Tree, err = repo.Tree(in.Commit); err != nil {
		return fmt.Errorf("tasks plan: %w", err)
	}
	if flags.base != "" {
		if in.Base, err = repo.Commit(flags.base); err != nil {
			return fmt.Errorf("tasks plan: --base: %w", err)
		}
		if in.BaseFile, err = readTaskFile(repo, in.Base, flags.file); err != nil {
			return fmt.Errorf("tasks plan: --base: %w", err)
		}
	}
	read := time.Since(started)
	if flags.sandbox {
		// The sandbox starts no process, so the one that reads blobs
		// starts first. From here on the process resolves, and writes
		// the plan.
		if err := repo.Start(); err != nil {
			return fmt.Errorf("tasks plan: %w", err)
		}
		if err := inttasks.Sandbox(); err != nil {
			return fmt.Errorf("tasks plan: --sandbox: %w", err)
		}
	}
	resolving := time.Now()
	plan, err := inttasks.Plan(in)
	if err != nil {
		if flags.sandbox && errors.Is(err, os.ErrPermission) {
			return fmt.Errorf("tasks plan: %w\nThe sandbox refused it: a resolver reads tracked files through the commit's tree, starts no process and uses no network", err)
		}
		return fmt.Errorf("tasks plan: %w", err)
	}
	if flags.profile {
		writePlanProfile(cmd.ErrOrStderr(), plan, in, read, time.Since(resolving), flags.sandbox)
	}
	data, err := plan.Encode()
	if err != nil {
		return err
	}
	_, err = cmd.OutOrStdout().Write(data)
	return err
}

// writePlanProfile writes what a plan cost.
func writePlanProfile(w io.Writer, plan *tasks.Plan, in inttasks.PlanInput, read, resolved time.Duration, sandboxed bool) {
	round := func(d time.Duration) time.Duration { return d.Round(100 * time.Microsecond) }
	suffix := ""
	if sandboxed {
		suffix = ", in the sandbox"
	}
	_, _ = fmt.Fprintf(w, "tasks plan: %d tasks over %d tracked files%s\n", len(plan.Tasks), len(in.Tree.Entries()), suffix)
	_, _ = fmt.Fprintf(w, "  %-34s %10s\n", "read the task file and the tree", round(read))
	_, _ = fmt.Fprintf(w, "  %-34s %10s\n", "resolve the inputs and key", round(resolved))
	kinds := make([]string, 0, len(in.Timings.Kinds))
	for kind := range in.Timings.Kinds {
		kinds = append(kinds, kind)
	}
	sort.Strings(kinds)
	for _, kind := range kinds {
		t := in.Timings.Kinds[kind]
		_, _ = fmt.Fprintf(w, "    %-12s %5d inputs, %5d resolved, %8d files %10s\n", kind, t.Inputs, t.Resolved, t.Files, round(t.Duration))
	}
}

// readTaskFile reads and decodes the task file a commit holds at path.
func readTaskFile(repo *inttasks.Repository, commit, path string) (*tasks.File, error) {
	data, err := repo.File(commit, path)
	if err != nil {
		return nil, err
	}
	file, err := tasks.Decode(data)
	if err != nil {
		return nil, fmt.Errorf("%s at %s: %w", path, commit, err)
	}
	return file, nil
}

// tasksInputsFlags holds one tasks inputs command's flag values.
type tasksInputsFlags struct {
	namingPath string
}

func newTasksInputsCmd(a *app) *cobra.Command {
	flags := &tasksInputsFlags{}
	cmd := &cobra.Command{
		Use:   "inputs <tasks-service-dir>",
		Short: "Check each schema input's static set against what the last build read",
		Long: `inputs checks the schema inputs of a Tasks service's tasks. A schema input
resolves, from a commit's tracked text alone, to a static set of files:
the service's directory, the directory of each service it names, the
schemas root's shared files and the naming file's [cache] inputs. A plan
keys a task on that set, so the set must hold everything a build of the
service reads.

Run it after build-all. For each schema input it gathers what the last
build read: the services the config names, the services its schema files
reference and the files its documents import, which the build records
under <schemas-root>/dist, and each of those services' own in turn. It
fails and names each service or file the static set at HEAD lacks. The
fix is an input that covers it: another schema input, or a path.

A build changes for no one else: a schemas root with no Tasks service
never runs this check.

Example:
  superschematic build-all ./schemas/services && superschematic tasks inputs ./schemas/services/repo-tasks`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTasksInputs(cmd, a, flags, args[0])
		},
	}
	cmd.Flags().StringVar(&flags.namingPath, "naming", "", "naming config file (default <service-dir>/../../superschematic.toml)")
	return cmd
}

func runTasksInputs(cmd *cobra.Command, a *app, flags *tasksInputsFlags, servicePath string) error {
	opened, err := openTasksService(a, servicePath, flags.namingPath, "tasks inputs")
	if err != nil {
		return err
	}
	defer opened.version.close()
	if opened.gitRoot == "" {
		return fmt.Errorf("tasks inputs: no git repository holds %s; a schema input's set is read from a commit", opened.service.Dir)
	}
	file, err := inttasks.Render(opened.schema)
	if err != nil {
		return fmt.Errorf("tasks inputs: %w", err)
	}
	schemasRoot := filepath.Dir(opened.version.servicesRoot)
	rootRel, err := filepath.Rel(opened.gitRoot, schemasRoot)
	if err != nil {
		return err
	}
	rootDir := filepath.ToSlash(rootRel)
	if rootDir == "." {
		rootDir = ""
	}

	repo, err := inttasks.OpenRepository(opened.gitRoot)
	if err != nil {
		return fmt.Errorf("tasks inputs: %w", err)
	}
	defer func() { _ = repo.Close() }()
	commit, err := repo.Commit("HEAD")
	if err != nil {
		return fmt.Errorf("tasks inputs: %w", err)
	}
	tree, err := repo.Tree(commit)
	if err != nil {
		return fmt.Errorf("tasks inputs: %w", err)
	}
	var root *inttasks.SchemaRoot
	for _, candidate := range inttasks.SchemaRoots(tree) {
		if candidate.Dir == rootDir {
			root = &candidate
			break
		}
	}
	if root == nil {
		return fmt.Errorf("tasks inputs: HEAD holds no schema service under %s; commit the schemas root first", path.Join(rootDir, "services"))
	}

	buildcache.SetSchemasRoot(schemasRoot)
	repoRoot := filepath.Dir(schemasRoot)
	var problems []string
	checked := 0
	for _, taskName := range file.Names() {
		for _, input := range file.Tasks[taskName].Inputs {
			if input.Kind != tasks.InputSchema {
				continue
			}
			var spec inttasks.SchemaSpec
			if err := json.Unmarshal(input.Spec, &spec); err != nil {
				return fmt.Errorf("tasks inputs: task %s: %w", taskName, err)
			}
			if spec.Root != "" && strings.TrimSuffix(spec.Root, "/") != rootDir {
				continue
			}
			if _, ok := opened.version.service(spec.Service); !ok {
				problems = append(problems, fmt.Sprintf("task %s: schema input %s: no such service under %s", taskName, spec.Service, opened.version.servicesRoot))
				continue
			}
			set, err := inttasks.SchemaInputSet(tree, *root, spec.Service)
			if err != nil {
				return fmt.Errorf("tasks inputs: task %s: schema input %s: %w", taskName, spec.Service, err)
			}
			checked++
			reads := schemaReadsOf(opened.version.services, repoRoot, spec.Service)
			for _, problem := range reads.outside(set, *root) {
				problems = append(problems, fmt.Sprintf("task %s: schema input %s: %s", taskName, spec.Service, problem))
			}
		}
	}
	if len(problems) > 0 {
		sort.Strings(problems)
		problems = compactStrings(problems)
		return fmt.Errorf("tasks inputs: %s: a build reads what a schema input's static set does not hold, so a plan's key would miss it:\n  %s\nadd an input that covers each: a schema input for the service, or a path", opened.service.Name, strings.Join(problems, "\n  "))
	}
	_, err = fmt.Fprintf(cmd.OutOrStdout(), "%s: %d schema input(s) hold what the last build read\n", opened.service.Name, checked)
	return err
}

func compactStrings(sorted []string) []string {
	out := sorted[:0]
	for i, s := range sorted {
		if i == 0 || s != sorted[i-1] {
			out = append(out, s)
		}
	}
	return out
}

// schemaReads is what the last build of a schema service read beyond its
// own directory: the services it reached, each with how, and the files
// its documents imported and its identity handles named, from the parent
// of the schemas root.
type schemaReads struct {
	services map[string]string
	files    map[string]string
}

// schemaReadsOf gathers what the build cache keys the service name on
// (internal/buildcache, InputHasher): the services its config names, the
// services its schema files reference and the files its documents import,
// from the depfiles its last build wrote, and the same of each service it
// reaches. A stack with a generated workflow reads every other stack too.
func schemaReadsOf(services []buildplan.Service, repoRoot, name string) schemaReads {
	byName := make(map[string]buildplan.Service, len(services))
	for _, service := range services {
		byName[service.Name] = service
	}
	reads := schemaReads{services: map[string]string{}, files: map[string]string{}}
	seen := map[string]bool{name: true}
	pending := []string{name}
	reach := func(other, how string) {
		if other == "" || seen[other] {
			return
		}
		seen[other] = true
		reads.services[other] = how
		pending = append(pending, other)
	}
	for len(pending) > 0 {
		current := pending[0]
		pending = pending[1:]
		if cfg := byName[current].Config; cfg != nil {
			named := []string{cfg.AuthDB}
			for _, dep := range cfg.Dependencies {
				named = append(named, dep.Name)
			}
			for _, call := range cfg.Calls {
				named = append(named, call.Name)
			}
			for _, bucket := range cfg.Buckets {
				named = append(named, bucket.Name)
			}
			for _, store := range cfg.MemoryStores {
				named = append(named, store.Name)
			}
			if cfg.RateLimitStore != nil {
				named = append(named, cfg.RateLimitStore.Name)
			}
			for _, other := range named {
				reach(other, "the config of "+current+" names it")
			}
			if _, ci := cfg.Outputs["ci"]; ci && cfg.Kind == ir.SchemaKindStack {
				for _, other := range services {
					if other.Config != nil && other.Config.Kind == ir.SchemaKindStack {
						reach(other.Name, "the workflow of stack "+current+" builds every stack")
					}
				}
			}
		}
		refs := buildcache.ReadSchemaReferences(repoRoot, current)
		for _, ref := range refs.References {
			reach(ref, "a schema file of "+current+" references it")
		}
		for _, rel := range refs.Identities {
			reads.files[rel] = "a schema file of " + current + " names its service"
		}
		for _, rel := range buildcache.ReadAuthoringImports(repoRoot, current) {
			reads.files[rel] = "a document of " + current + " imports it"
		}
	}
	return reads
}

// outside lists what the reads hold and the static set does not.
func (r schemaReads) outside(set inttasks.SchemaSet, root inttasks.SchemaRoot) []string {
	inSet := make(map[string]bool, len(set.Services))
	for _, service := range set.Services {
		inSet[service] = true
	}
	tracked := make(map[string]bool, len(set.Paths))
	for _, p := range set.Paths {
		tracked[p] = true
	}
	var out []string
	for service, how := range r.services {
		if !inSet[service] {
			out = append(out, fmt.Sprintf("service %s is outside the set (%s)", service, how))
		}
	}
	parent := path.Dir(root.Dir)
	if parent == "." {
		parent = ""
	}
	for rel, how := range r.files {
		file := path.Join(parent, filepath.ToSlash(rel))
		if tracked[file] {
			continue
		}
		// A file of a service the set holds is covered with its
		// directory, tracked or written by the build from tracked files,
		// as a sentinel is.
		covered := false
		for _, service := range set.Services {
			covered = covered || strings.HasPrefix(file, root.ServiceDir(service)+"/")
		}
		if !covered {
			out = append(out, fmt.Sprintf("file %s is outside the set (%s)", file, how))
		}
	}
	return out
}

// tasksReachFlags holds one tasks reach command's flag values.
type tasksReachFlags struct {
	commits int
	rev     string
	json    bool
}

func newTasksReachCmd(_ *app) *cobra.Command {
	flags := &tasksReachFlags{}
	cmd := &cobra.Command{
		Use:   "reach <schemas-root>",
		Short: "Measure how many schema inputs each commit of a schemas root's history changes",
		Long: `reach measures how narrow a schema input is, on the repository's own
history. A schema input resolves to a set that is wider than what one
build of its service reads: the service, each service it names, and the
schemas root's shared files. A set that every edit changes keys every task
that reads schemas on every schema commit, and a forge then reuses none of
them.

It walks the commits that changed the schemas root, back from --rev along
first parents, and measures each against its parent: of the services the
root holds, how many the commit edited, and how many schema inputs'
digests it changed. It prints how many commits changed every schema
input, some of them and none. A commit that changes a file of the root
that belongs to no service, as the naming file or a lockfile, changes
every one.

It reads commits through git and nothing of the working tree, loads no
schema and runs no build.

Examples:
  superschematic tasks reach ./schemas
  superschematic tasks reach ./schemas --commits 500 --json`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return runTasksReach(cmd, flags, args[0])
		},
	}
	cmd.Flags().IntVar(&flags.commits, "commits", 100, "the most commits to measure")
	cmd.Flags().StringVar(&flags.rev, "rev", "HEAD", "the commit to walk back from")
	cmd.Flags().BoolVar(&flags.json, "json", false, "write every measured commit as one JSON document")
	return cmd
}

func runTasksReach(cmd *cobra.Command, flags *tasksReachFlags, schemasRoot string) error {
	abs, err := filepath.Abs(schemasRoot)
	if err != nil {
		return err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		abs = resolved
	}
	if info, err := os.Stat(abs); err != nil || !info.IsDir() {
		return fmt.Errorf("tasks reach: schemas root not found: %s", schemasRoot)
	}
	if flags.commits < 1 {
		return fmt.Errorf("tasks reach: --commits is %d; it is the most commits to measure, one or more", flags.commits)
	}
	repo, err := inttasks.OpenRepository(abs)
	if err != nil {
		return fmt.Errorf("tasks reach: %w", err)
	}
	defer func() { _ = repo.Close() }()
	top, err := repo.Root()
	if err != nil {
		return fmt.Errorf("tasks reach: %w", err)
	}
	if resolved, err := filepath.EvalSymlinks(top); err == nil {
		top = resolved
	}
	rel, err := filepath.Rel(top, abs)
	if err != nil {
		return err
	}
	dir := filepath.ToSlash(rel)
	if dir == "." {
		dir = ""
	}
	reach, err := inttasks.SchemaReach(repo, dir, flags.rev, flags.commits)
	if err != nil {
		return fmt.Errorf("tasks reach: %w", err)
	}
	out := cmd.OutOrStdout()
	if flags.json {
		data, err := json.MarshalIndent(reach, "", "  ")
		if err != nil {
			return err
		}
		_, err = out.Write(append(data, '\n'))
		return err
	}
	measured := len(reach.Commits)
	shown := dir
	if shown == "" {
		shown = "."
	}
	if measured == 0 {
		_, err = fmt.Fprintf(out, "%s: no commit back from %s changed it and has a parent that holds it\n", shown, flags.rev)
		return err
	}
	share := func(n int) string { return fmt.Sprintf("%4d  %3.0f%%", n, 100*float64(n)/float64(measured)) }
	var edited, changed, services []int
	shared := 0
	for _, c := range reach.Commits {
		edited, changed, services = append(edited, c.Edited), append(changed, c.Changed), append(services, c.Services)
		if c.Shared {
			shared++
		}
	}
	var b strings.Builder
	fmt.Fprintf(&b, "%s: %d commits that changed it, back from %s", shown, measured, flags.rev)
	if reach.Skipped > 0 {
		fmt.Fprintf(&b, " (%d more left out: no parent, or the root begins there)", reach.Skipped)
	}
	b.WriteString("\n")
	fmt.Fprintf(&b, "  %-42s %s\n", "changed every schema input's digest", share(reach.All()))
	fmt.Fprintf(&b, "  %-42s %s\n", "changed some", share(reach.Some()))
	fmt.Fprintf(&b, "  %-42s %s\n", "changed none", share(reach.None()))
	fmt.Fprintf(&b, "  %-42s %s\n", "changed a file that belongs to no service", share(shared))
	fmt.Fprintf(&b, "  %-42s %4d of %d\n", "median services edited", median(edited), median(services))
	fmt.Fprintf(&b, "  %-42s %4d of %d\n", "median schema inputs changed", median(changed), median(services))
	_, err = io.WriteString(out, b.String())
	return err
}

// median is the middle value of a list, the lower of two.
func median(values []int) int {
	if len(values) == 0 {
		return 0
	}
	sorted := append([]int(nil), values...)
	sort.Ints(sorted)
	return sorted[(len(sorted)-1)/2]
}
