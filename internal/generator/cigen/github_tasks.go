package cigen

import (
	"bytes"
	"encoding/json"
	"fmt"
	"maps"
	"regexp"
	"slices"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/tasks"
)

// The github renderer over a Tasks service (D203): one GitHub Actions
// workflow, `<service>.yml`, that runs the tasks of the task file. A task
// is a job. Both of its edges, `uses` and `after`, are the job's `needs`.
// A paths output travels from the job that wrote it to each job that uses
// it as an artifact, and a value or an image output as a job output.
//
// The task file says nothing of when a workflow runs, on what runner, or
// from which registry an image is pulled: those are GitHub's, so they are
// the `github` task section, which this file registers and reads.
//
// The workflow runs every task on every run, unless the section asks for
// reuse. With `reuse`, a first job plans the commit (tasks plan --keys),
// and each task that has a key looks for a pass under it before it runs:
// the workflow keeps a pass, with the task's outputs, in GitHub's cache
// under the task's name and its input key. A job that finds one runs
// nothing and hands on the outputs it found, so the jobs that use them
// read what they would have read had it run.
//
// What may read a pass is GitHub's rule and not this file's: a cache
// entry a pull request's run saved is read by that pull request's runs
// alone, and one a run on the default branch saved by every branch. So a
// change cannot leave a pass for the branch it lands on, and a task whose
// definition a pull request edits runs once more after it lands, as the
// plan's base has it. On a pull request the plan is given the commit the
// change lands on, the merge commit's first parent, as its base.

// GitHubTaskSection is the task section the github renderer reads: the
// part of a task file that is GitHub's. The file's part holds the
// workflow's triggers, as GitHub writes `on`, and may hold its name, its
// permissions, the runner every job runs on and the repository the
// tasks' image is pulled from. A task's part may hold its runner, its
// timeout and its condition.
func GitHubTaskSection() registry.TaskSectionSpec {
	return registry.TaskSectionSpec{Name: GitHubRenderer, File: githubTasksFile, Task: githubTasksTask}
}

var githubTasksFile = json.RawMessage(`{
	"type": "object",
	"required": ["on"],
	"additionalProperties": false,
	"properties": {
		"name": {"type": "string", "minLength": 1},
		"on": {"type": "object", "minProperties": 1},
		"permissions": {"type": "object", "additionalProperties": {"enum": ["read", "write", "none"]}},
		"runsOn": {"type": "string", "minLength": 1},
		"imageRepository": {"type": "string", "pattern": "^[a-z0-9][a-z0-9._/:-]*$"},
		"reuse": {
			"type": "object",
			"additionalProperties": false,
			"properties": {
				"command": {"type": "array", "minItems": 1, "items": {"type": "string", "minLength": 1}}
			}
		}
	}
}`)

var githubTasksTask = json.RawMessage(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"runsOn": {"type": "string", "minLength": 1},
		"timeoutMinutes": {"type": "integer", "minimum": 1},
		"if": {"type": "string", "minLength": 1}
	}
}`)

// githubTasksSection is the section's part of the file.
type githubTasksSection struct {
	Name            string            `json:"name"`
	On              map[string]any    `json:"on"`
	Permissions     map[string]string `json:"permissions"`
	RunsOn          string            `json:"runsOn"`
	ImageRepository string            `json:"imageRepository"`
	// Reuse, when set, has the workflow plan the commit and keep each
	// keyed task's pass under its input key.
	Reuse *githubTasksReuse `json:"reuse"`
}

// githubTasksReuse is the section's `reuse`. Command is the plan command's
// first words: the binary that links the resolvers the task file's inputs
// name, as it is called in the file's image. Unset, it is superschematic.
type githubTasksReuse struct {
	Command []string `json:"command"`
}

// githubTaskSection is the section's part of one task.
type githubTaskSection struct {
	RunsOn         string `json:"runsOn"`
	TimeoutMinutes int    `json:"timeoutMinutes"`
	If             string `json:"if"`
}

// The directories a job's steps hand a task's command, each under the
// runner's temporary directory and named in the job's environment. A
// command writes a value or an image output as the file
// $TASK_OUTPUTS/<output>, and reads one it uses from
// $TASK_USES/<task>/<output>. $TASK_ARTIFACTS holds the archives of paths
// outputs on their way in and out. In a workflow that reuses, a task with
// a key keeps what a pass leaves under $TASK_PASS, which is what the
// cache holds: its value outputs, the archives of its paths outputs, and
// the key it passed under. $TASK_KEY is that key.
const (
	taskOutputsEnv   = "TASK_OUTPUTS"
	taskUsesEnv      = "TASK_USES"
	taskArtifactsEnv = "TASK_ARTIFACTS"
	taskPassEnv      = "TASK_PASS"
	taskKeyEnv       = "TASK_KEY"
)

// passMissed is the condition of a step that runs only when the job found
// no pass of the task's key.
const passMissed = "steps.pass.outputs.cache-hit != 'true'"

// passVersion is the version of what a pass holds in the cache. A change
// to the layout under $TASK_PASS raises it, so no job reads a pass laid
// out another way.
const passVersion = "v1"

// runnerOf is the GitHub-hosted runner of each platform a task may name.
// A task on another platform names its runner in the section.
var runnerOf = map[string]string{
	"":            "ubuntu-latest",
	"linux/amd64": "ubuntu-latest",
	"linux/arm64": "ubuntu-24.04-arm",
}

// serviceID is what GitHub takes as a service container's id.
var serviceID = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_-]*$`)

// renderGitHubTasks writes the workflow of a Tasks service.
func renderGitHubTasks(req registry.CITasksRequest) ([]registry.CIFile, error) {
	file := req.File
	raw, ok := file.Sections[GitHubRenderer]
	if !ok {
		return nil, fmt.Errorf("the workflow's triggers are the %[1]s section's, and @tasks holds none; give @tasks sections.%[1]s.on, written as GitHub writes a workflow's `on`", GitHubRenderer)
	}
	var section githubTasksSection
	if err := strictJSON(raw, &section); err != nil {
		return nil, fmt.Errorf("sections.%s: %w", GitHubRenderer, err)
	}
	if len(section.On) == 0 {
		return nil, fmt.Errorf("sections.%s.on is empty, so the workflow would never run", GitHubRenderer)
	}
	names := make([]string, 0, len(file.Tasks))
	for name := range file.Tasks {
		names = append(names, name)
	}
	sort.Strings(names)

	w := &yamlWriter{}
	w.line(0, fmt.Sprintf("# Code generated by superschematic from Tasks service %s. DO NOT EDIT.", req.Service))
	w.line(0, "#")
	w.comment(0, "Change the service's schema and build it again, which writes this file. Each task of the task file is a job, and a job needs the tasks its task uses and the tasks it follows. A paths output goes from the job that wrote it to each job that uses it as an artifact, and a value or an image output as a job output.")
	w.line(0, "#")
	w.comment(0, fmt.Sprintf("A command writes a value or an image output, one line, to the file $%s/<output>, and reads one it uses from $%s/<task>/<output>.", taskOutputsEnv, taskUsesEnv))
	w.line(0, "#")
	var plan *planJob
	if section.Reuse != nil {
		var err error
		if plan, err = planJobOf(req, section, names); err != nil {
			return nil, err
		}
		w.comment(0, fmt.Sprintf("The job %s plans the commit, and each task that has an input key looks for a pass under it before it runs. A job that finds one runs nothing and hands on the outputs the pass left. A pass is kept in GitHub's cache, which says what may read it: a pull request's runs read their own and the default branch's, and the default branch reads its own. A task that is not hermetic has no key and runs every time.", plan.id))
	} else {
		w.comment(0, "Every run runs every task. With sections.github.reuse the workflow plans the commit and keeps each task's pass under its input key.")
	}
	w.blank()
	name := section.Name
	if name == "" {
		name = req.Service
	}
	w.line(0, "name: "+scalar(name))
	w.blank()
	on, err := yamlBlock(section.On)
	if err != nil {
		return nil, fmt.Errorf("sections.%s.on: %w", GitHubRenderer, err)
	}
	w.line(0, "on:")
	for _, line := range on {
		w.line(1, line)
	}
	w.blank()
	w.line(0, "permissions:")
	if len(section.Permissions) == 0 {
		w.line(1, "contents: read")
	}
	for _, scope := range slices.Sorted(maps.Keys(section.Permissions)) {
		w.line(1, scalar(scope)+": "+section.Permissions[scope])
	}
	w.blank()
	w.line(0, "jobs:")
	if plan != nil {
		plan.write(w)
	}
	for i, name := range names {
		if i > 0 || plan != nil {
			w.blank()
		}
		if err := writeTaskJob(w, req.Service, file, section, plan, name); err != nil {
			return nil, fmt.Errorf("task %s: %w", name, err)
		}
	}
	return []registry.CIFile{{Path: req.Service + ".yml", Data: w.bytes()}}, nil
}

// planJob is the job that plans the commit in a workflow that reuses: it
// runs the plan command in the task file's image and gives each keyed
// task's key as an output of its own, named as the task.
type planJob struct {
	id        string
	runsOn    string
	container string
	keyed     []string
	script    string
}

// planJobOf works out the plan job, and refuses a workflow that cannot
// plan: one whose task file is committed nowhere, or names no image to
// run the plan command from.
func planJobOf(req registry.CITasksRequest, section githubTasksSection, names []string) (*planJob, error) {
	file := req.File
	option := "sections." + GitHubRenderer + ".reuse"
	switch {
	case req.TaskFile == "":
		return nil, fmt.Errorf("%s plans the commit from its committed task file, and the service commits none; give outputs.tasks.install the file's path", option)
	case file.Image == "":
		return nil, fmt.Errorf("%s plans the commit with the plan command, which ships in the image @tasks names, and @tasks names none; give @tasks an image", option)
	case section.ImageRepository == "":
		return nil, fmt.Errorf("%[1]s runs the plan command from image %[2]s, and GitHub pulls an image by its repository and digest; give @tasks sections.%[3]s.imageRepository, the repository that holds it", option, file.Image, GitHubRenderer)
	}
	runsOn := section.RunsOn
	if runsOn == "" {
		hosted, ok := runnerOf[file.Platform]
		if !ok {
			return nil, fmt.Errorf("%s runs the plan command on platform %s, which no GitHub-hosted runner the renderer knows is; name the runner in sections.%s.runsOn", option, file.Platform, GitHubRenderer)
		}
		runsOn = hosted
	}
	command := section.Reuse.Command
	if len(command) == 0 {
		command = []string{"superschematic"}
	}
	for _, word := range command {
		if strings.Contains(word, "${{") {
			return nil, fmt.Errorf("%s.command holds %q, and GitHub would read the ${{ }} as an expression before the command runs", option, word)
		}
	}
	plan := &planJob{id: "plan", runsOn: runsOn, container: section.ImageRepository + "@" + file.Image}
	// The job's id is no task's: a task may be named plan.
	for n := 2; slices.Contains(names, plan.id); n++ {
		plan.id = fmt.Sprintf("plan-%d", n)
	}
	for _, name := range names {
		if file.Tasks[name].Hermetic {
			plan.keyed = append(plan.keyed, name)
		}
	}
	planCommand := shellCommand(append(append([]string{}, command...), "tasks", "plan", "HEAD", "--file", req.TaskFile))
	plan.script = strings.Join([]string{
		// A container's user is seldom the checkout's owner, and git
		// reads no repository it takes for another user's.
		`git config --global --add safe.directory "$GITHUB_WORKSPACE"`,
		// On a pull request HEAD is the merge commit, and its first
		// parent the commit the change lands on. A base that holds no
		// task file gives no definition to hold a task to.
		`if [ "$GITHUB_EVENT_NAME" = pull_request ] && git cat-file -e ` + shellWord("HEAD^1:"+req.TaskFile) + ` 2>/dev/null; then`,
		`  ` + planCommand + ` --base 'HEAD^1' --keys >> "$GITHUB_OUTPUT"`,
		`else`,
		`  ` + planCommand + ` --keys >> "$GITHUB_OUTPUT"`,
		`fi`,
	}, "\n")
	return plan, nil
}

// has reports whether the task has a key the plan job gives.
func (p *planJob) has(name string) bool {
	return p != nil && slices.Contains(p.keyed, name)
}

func (p *planJob) write(w *yamlWriter) {
	w.line(1, p.id+":")
	w.line(2, "name: "+scalar(p.id))
	w.line(2, "runs-on: "+scalar(p.runsOn))
	w.line(2, "container: "+scalar(p.container))
	if len(p.keyed) > 0 {
		w.line(2, "outputs:")
		for _, name := range p.keyed {
			w.line(3, name+": ${{ steps.keys.outputs."+name+" }}")
		}
	}
	w.line(2, "steps:")
	// The merge commit and its parents: the plan reads the commit, and on
	// a pull request the commit it lands on.
	step{name: "Check out", uses: &actionCheckout, with: [][2]string{{"fetch-depth", "2"}}}.write(w)
	step{name: "Plan the commit", id: "keys", run: p.script}.write(w)
}

// writeTaskJob writes the job of one task. plan is the workflow's plan
// job, nil for a workflow that reuses nothing.
func writeTaskJob(w *yamlWriter, service string, file *tasks.File, section githubTasksSection, plan *planJob, name string) error {
	task := file.Tasks[name]
	// reuse is set for a task whose pass the workflow keeps: one with a
	// key, in a workflow that plans.
	reuse := plan.has(name)
	var own githubTaskSection
	if raw, ok := task.Sections[GitHubRenderer]; ok {
		if err := strictJSON(raw, &own); err != nil {
			return fmt.Errorf("sections.%s: %w", GitHubRenderer, err)
		}
	}

	// GitHub reads ${{ }} in a workflow before any shell does, so a
	// command or a variable that holds one would not run as written.
	for _, arg := range task.Run {
		if strings.Contains(arg, "${{") {
			return fmt.Errorf("its command holds %q, and GitHub would read the ${{ }} as an expression before the command runs", arg)
		}
	}
	envNames := slices.Sorted(maps.Keys(task.Env))
	for _, key := range envNames {
		switch {
		case key == taskOutputsEnv || key == taskUsesEnv || key == taskArtifactsEnv || key == taskPassEnv || key == taskKeyEnv:
			return fmt.Errorf("its env sets %s, which the workflow sets for every task", key)
		case strings.Contains(task.Env[key], "${{"):
			return fmt.Errorf("its env %s holds %q, and GitHub would read the ${{ }} as an expression", key, task.Env[key])
		}
	}

	runsOn := own.RunsOn
	if runsOn == "" {
		runsOn = section.RunsOn
	}
	if runsOn == "" {
		platform := file.PlatformOf(name)
		hosted, ok := runnerOf[platform]
		if !ok {
			return fmt.Errorf("runs on platform %s, which no GitHub-hosted runner the renderer knows is; name its runner in sections.%s.runsOn", platform, GitHubRenderer)
		}
		runsOn = hosted
	}
	image := file.ImageOf(name)
	if image != "" && section.ImageRepository == "" {
		return fmt.Errorf("runs from image %s, and GitHub pulls an image by its repository and digest; give @tasks sections.%s.imageRepository, the repository that holds it", image, GitHubRenderer)
	}

	// Both edges are the job's needs: GitHub has one kind.
	needs := map[string]bool{}
	for _, use := range task.Uses {
		needs[use.Task] = true
	}
	for _, after := range task.After {
		needs[after] = true
	}
	if reuse {
		needs[plan.id] = true
	}

	w.line(1, name+":")
	w.line(2, "name: "+scalar(name))
	if len(needs) > 0 {
		w.line(2, "needs: ["+strings.Join(slices.Sorted(maps.Keys(needs)), ", ")+"]")
	}
	if own.If != "" {
		w.line(2, "if: "+scalar(own.If))
	}
	w.line(2, "runs-on: "+scalar(runsOn))
	if own.TimeoutMinutes > 0 {
		w.line(2, fmt.Sprintf("timeout-minutes: %d", own.TimeoutMinutes))
	}
	if image != "" {
		w.line(2, "container: "+scalar(section.ImageRepository+"@"+image))
	}
	if len(task.Services) > 0 {
		w.line(2, "services:")
		for _, svc := range task.Services {
			if !serviceID.MatchString(svc.Name) {
				return fmt.Errorf("service container %q has a name GitHub does not take as a service's id: letters, digits, hyphens and underscores, not beginning with a digit or a hyphen", svc.Name)
			}
			w.line(3, svc.Name+":")
			w.line(4, "image: "+scalar(svc.Image))
		}
	}
	var packed, values []tasks.Output
	for _, out := range task.Outputs {
		if out.Kind == tasks.OutputPaths || out.Kind == "" {
			packed = append(packed, out)
		} else {
			values = append(values, out)
		}
	}
	if len(values) > 0 {
		w.line(2, "outputs:")
		for _, out := range values {
			w.line(3, out.Name+": ${{ steps.outputs.outputs."+out.Name+" }}")
		}
	}

	// The steps a pass stands in for run only when the job found none.
	var steps []step
	ran := func(s step) step {
		if reuse {
			s.cond = passMissed
		}
		return s
	}
	// archives is where the archives of the task's own paths outputs are
	// written: with a pass, inside what the cache keeps.
	archives := taskArtifactsEnv
	switch {
	case reuse:
		archives = taskPassEnv
		steps = append(steps,
			step{
				// A key the plan did not give would be every commit's:
				// the job stops before it looks a pass up under none.
				name: "Name the task's directories",
				env:  [][2]string{{taskKeyEnv, "${{ needs." + plan.id + ".outputs." + name + " }}"}},
				run: strings.Join([]string{
					`test -n "$` + taskKeyEnv + `"`,
					`mkdir -p "$RUNNER_TEMP/task/pass/outputs" "$RUNNER_TEMP/task/uses" "$RUNNER_TEMP/task/artifacts"`,
					`{`,
					`  echo "` + taskKeyEnv + `=$` + taskKeyEnv + `"`,
					`  echo "` + taskPassEnv + `=$RUNNER_TEMP/task/pass"`,
					`  echo "` + taskOutputsEnv + `=$RUNNER_TEMP/task/pass/outputs"`,
					`  echo "` + taskUsesEnv + `=$RUNNER_TEMP/task/uses"`,
					`  echo "` + taskArtifactsEnv + `=$RUNNER_TEMP/task/artifacts"`,
					`} >> "$GITHUB_ENV"`,
				}, "\n"),
			},
			step{
				name: "Look for a pass under the task's key",
				id:   "pass",
				uses: &actionCacheRestore,
				with: [][2]string{{"path", "${{ env." + taskPassEnv + " }}"}, {"key", passKey(service, name)}},
			},
			ran(step{name: "Check out", uses: &actionCheckout}))
	case len(task.Uses) > 0 || len(task.Outputs) > 0:
		steps = append(steps, step{name: "Check out", uses: &actionCheckout}, step{
			name: "Name the task's directories",
			run: strings.Join([]string{
				`mkdir -p "$RUNNER_TEMP/task/outputs" "$RUNNER_TEMP/task/uses" "$RUNNER_TEMP/task/artifacts"`,
				`{`,
				`  echo "` + taskOutputsEnv + `=$RUNNER_TEMP/task/outputs"`,
				`  echo "` + taskUsesEnv + `=$RUNNER_TEMP/task/uses"`,
				`  echo "` + taskArtifactsEnv + `=$RUNNER_TEMP/task/artifacts"`,
				`} >> "$GITHUB_ENV"`,
			}, "\n"),
		})
	default:
		steps = append(steps, step{name: "Check out", uses: &actionCheckout})
	}

	// What the task uses, in place before its command starts: the files
	// of a paths output at their paths, and a value as a file.
	var place []string
	var placeEnv [][2]string
	for _, use := range task.Uses {
		upstream, ok := outputOf(file, use)
		if !ok {
			return fmt.Errorf("uses output %s of task %s, which that task does not declare", use.Output, use.Task)
		}
		artifact := use.Task + "." + use.Output
		if upstream.Kind == tasks.OutputPaths || upstream.Kind == "" {
			steps = append(steps, ran(step{
				name: fmt.Sprintf("Download %s of %s", use.Output, use.Task),
				uses: &actionDownload,
				with: [][2]string{{"name", artifact}, {"path", "${{ env." + taskArtifactsEnv + " }}/" + artifact}},
			}))
			place = append(place, fmt.Sprintf(`tar -xf "$%s/%s/%s.tar"`, taskArtifactsEnv, artifact, use.Output))
			continue
		}
		variable := fmt.Sprintf("USE_%d", len(placeEnv)+1)
		placeEnv = append(placeEnv, [2]string{variable, "${{ needs." + use.Task + ".outputs." + use.Output + " }}"})
		place = append(place,
			fmt.Sprintf(`mkdir -p "$%s/%s"`, taskUsesEnv, use.Task),
			fmt.Sprintf(`printf '%%s\n' "$%s" > "$%s/%s/%s"`, variable, taskUsesEnv, use.Task, use.Output))
	}
	if len(place) > 0 {
		steps = append(steps, ran(step{name: "Put what the task uses in place", env: placeEnv, run: strings.Join(place, "\n")}))
	}

	run := step{name: "Run", run: shellCommand(task.Run)}
	for _, key := range envNames {
		run.env = append(run.env, [2]string{key, task.Env[key]})
	}
	steps = append(steps, ran(run))

	for _, out := range packed {
		patterns := make([]string, 0, len(out.Paths))
		for _, pattern := range out.Paths {
			patterns = append(patterns, shellGlob(pattern))
		}
		archive := fmt.Sprintf(`"$%s/%s.tar"`, archives, out.Name)
		// A pattern is the shell's to expand, and one that matches
		// nothing leaves tar no file, which fails the job: a task that
		// declares an output writes it.
		steps = append(steps, ran(step{
			name:  "Pack " + out.Name,
			shell: "bash",
			run:   "shopt -s globstar nullglob dotglob\ntar -cf " + archive + " -- " + strings.Join(patterns, " "),
		}))
	}
	if reuse {
		// What the cache keeps is whole before it is kept: every archive
		// is packed, a value output the command did not write stops the
		// job, and the key the task passed under is beside them.
		var keep []string
		for _, out := range values {
			keep = append(keep, fmt.Sprintf(`test -f "$%s/%s"`, taskOutputsEnv, out.Name))
		}
		keep = append(keep, fmt.Sprintf(`printf '%%s\n' "$%s" > "$%s/key"`, taskKeyEnv, taskPassEnv))
		steps = append(steps,
			ran(step{name: "Record the pass", run: strings.Join(keep, "\n")}),
			ran(step{
				name: "Keep the pass under the task's key",
				uses: &actionCacheSave,
				with: [][2]string{{"path", "${{ env." + taskPassEnv + " }}"}, {"key", passKey(service, name)}},
			}))
	}
	// An output goes on from where it was packed, or from the pass the
	// job found: a job that uses it downloads it either way.
	for _, out := range packed {
		steps = append(steps, step{
			name: "Upload " + out.Name,
			uses: &actionUpload,
			with: [][2]string{
				{"name", name + "." + out.Name},
				{"path", "${{ env." + archives + " }}/" + out.Name + ".tar"},
				{"if-no-files-found", "error"},
			},
		})
	}
	if len(values) > 0 {
		var read []string
		for _, out := range values {
			read = append(read,
				fmt.Sprintf(`value=$(cat "$%s/%s")`, taskOutputsEnv, out.Name),
				fmt.Sprintf(`echo "%s=$value" >> "$GITHUB_OUTPUT"`, out.Name))
		}
		steps = append(steps, step{name: "Read the task's outputs", id: "outputs", run: strings.Join(read, "\n")})
	}
	w.line(2, "steps:")
	for _, s := range steps {
		s.write(w)
	}
	return nil
}

// passKey is the cache key a task's pass is kept under: the workflow's
// service, the task and its input key, which the plan job gives.
func passKey(service, name string) string {
	return "tasks-" + passVersion + "-" + service + "-" + name + "-${{ env." + taskKeyEnv + " }}"
}

// outputOf finds the output a use names.
func outputOf(file *tasks.File, use tasks.Use) (tasks.Output, bool) {
	for _, out := range file.Tasks[use.Task].Outputs {
		if out.Name == use.Output {
			return out, true
		}
	}
	return tasks.Output{}, false
}

// strictJSON decodes data into v and refuses a field v does not hold.
func strictJSON(data []byte, v any) error {
	dec := json.NewDecoder(bytes.NewReader(data))
	dec.DisallowUnknownFields()
	return dec.Decode(v)
}

// yamlBlock writes a value as block YAML, two spaces an indent and keys
// sorted, and returns its lines.
func yamlBlock(v any) ([]string, error) {
	var b bytes.Buffer
	enc := yaml.NewEncoder(&b)
	enc.SetIndent(2)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	if err := enc.Close(); err != nil {
		return nil, err
	}
	return strings.Split(strings.TrimRight(b.String(), "\n"), "\n"), nil
}

// shellSafe matches a word a shell reads as itself.
var shellSafe = regexp.MustCompile(`^[A-Za-z0-9_@%+=:,./-]+$`)

// shellWord quotes a word so a shell reads it as itself.
func shellWord(word string) string {
	if shellSafe.MatchString(word) {
		return word
	}
	return "'" + strings.ReplaceAll(word, "'", `'\''`) + "'"
}

// shellCommand writes an argument list as the one command a shell runs:
// each argument quoted, so the shell splits, expands and substitutes
// nothing. A task's command starts no shell, and GitHub starts every step
// in one.
func shellCommand(argv []string) string {
	words := make([]string, 0, len(argv))
	for _, arg := range argv {
		words = append(words, shellWord(arg))
	}
	return strings.Join(words, " ")
}

// shellGlob writes a task's pattern for a shell to expand: its `*`, `?`
// and `[...]` as they are and every other run quoted. With bash's
// globstar a `**` segment stands for any number of directories, as it
// does in a pattern.
func shellGlob(pattern string) string {
	var b strings.Builder
	var literal strings.Builder
	flush := func() {
		if literal.Len() > 0 {
			b.WriteString(shellWord(literal.String()))
			literal.Reset()
		}
	}
	for i := 0; i < len(pattern); i++ {
		switch c := pattern[i]; c {
		case '*', '?':
			flush()
			b.WriteByte(c)
		case '[':
			end := strings.IndexByte(pattern[i+1:], ']')
			if end < 0 {
				literal.WriteByte(c)
				continue
			}
			flush()
			b.WriteString(pattern[i : i+end+2])
			i += end + 1
		default:
			literal.WriteByte(c)
		}
	}
	flush()
	return b.String()
}
