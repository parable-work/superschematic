package tasks

import (
	"container/heap"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/tasks"
)

// PlanInput is what a plan is computed from.
type PlanInput struct {
	// Registry holds the input resolvers.
	Registry *registry.Registry

	// File is the task file at the commit, and Tree the commit's tree.
	File *tasks.File
	Tree registry.TaskTree

	// Format is the repository's object format, sha1 or sha256. It is
	// part of the key version, so keys of two formats never compare
	// equal.
	Format string

	// Commit is the commit's id.
	Commit string

	// Base is the id of the commit the plan's commit lands on, and
	// BaseFile the task file there. Both are empty for a plan with no
	// base, as a commit that has landed has.
	Base     string
	BaseFile *tasks.File

	// Timings, when set, receives what resolving the inputs cost, by
	// resolver kind.
	Timings *Timings

	// resolved holds each input resolved once: tasks that name one unit
	// read one resolution.
	resolved map[string]*resolvedInput
}

// Timings is what a plan's inputs cost to resolve, by resolver kind.
type Timings struct {
	Kinds map[string]*KindTiming
}

// KindTiming is the cost of one kind's inputs: how many inputs the tasks
// name, how many of those differ and were resolved, the tracked files
// they resolved to, and the time the resolver took.
type KindTiming struct {
	Inputs   int
	Resolved int
	Files    int
	Duration time.Duration
}

// resolvedInput is one input's resolution, or the error it met.
type resolvedInput struct {
	// digest is the input's digest and files the number of tracked files
	// it covers: both are the input's alone, so they are worked out once.
	digest    string
	files     int
	generated []registry.TaskGeneratedPath
	err       error
}

// KeyVersion is the key version of a repository of the format: the key
// definition's version and the format's name.
func KeyVersion(format string) string {
	return fmt.Sprintf("%d:%s", tasks.KeyVersion, format)
}

// Plan computes the plan of a commit: every task's work hash and input
// key, and an order that runs each task after the tasks it uses and the
// tasks it follows.
//
// A task's key covers, in order: the key version; the task's work hash;
// the digest of each of its inputs; and the key of each task it uses, by
// task and output. Every key is known before any task runs, since an
// input is tracked files and an output enters by its task's key. A task
// that is not hermetic has no key.
//
// With a base, a task whose work hash differs from the base's is keyed on
// both hashes and on the inputs of both definitions, so a change that
// edits a task cannot reuse a pass of the task it replaced, and cannot
// pass by narrowing what the task reads. Those keys differ from the keys
// the same tree has with no base: after the change lands, that task and
// each task that uses it runs once more.
func Plan(in PlanInput) (*tasks.Plan, error) {
	if in.Format != FormatSHA1 && in.Format != FormatSHA256 {
		return nil, fmt.Errorf("object format %q is neither sha1 nor sha256", in.Format)
	}
	in.resolved = map[string]*resolvedInput{}
	if in.Timings != nil && in.Timings.Kinds == nil {
		in.Timings.Kinds = map[string]*KindTiming{}
	}
	order, err := Order(in.File)
	if err != nil {
		return nil, err
	}
	plan := &tasks.Plan{
		Version:    tasks.PlanVersion,
		KeyVersion: KeyVersion(in.Format),
		Commit:     in.Commit,
		Base:       in.Base,
		Order:      order,
		Tasks:      make(map[string]tasks.PlanTask, len(order)),
		Sections:   in.File.Sections,
	}
	for _, name := range order {
		task := in.File.Tasks[name]
		work, err := in.File.WorkHash(name)
		if err != nil {
			return nil, err
		}
		planned := tasks.PlanTask{WorkHash: work, Uses: task.Uses, After: task.After, Sections: task.Sections}

		lines := []string{"version " + plan.KeyVersion, "work " + work}
		digests, err := resolveInputs(in, name, task, true)
		if err != nil {
			return nil, err
		}
		planned.Inputs = digests
		for _, input := range digests {
			lines = append(lines, "input "+input.Kind+" "+input.Digest)
		}
		if in.BaseFile != nil {
			if baseTask, ok := in.BaseFile.Tasks[name]; ok {
				baseWork, err := in.BaseFile.WorkHash(name)
				if err != nil {
					return nil, err
				}
				if baseWork != work {
					planned.BaseWorkHash = baseWork
					lines = append(lines, "base-work "+baseWork)
					// The base's definition is read against the commit's
					// tree, which may no longer hold what it names.
					baseDigests, err := resolveInputs(in, name, baseTask, false)
					if err != nil {
						return nil, fmt.Errorf("the base's definition: %w", err)
					}
					for _, input := range baseDigests {
						lines = append(lines, "base-input "+input.Kind+" "+input.Digest)
					}
				}
			}
		}
		if task.Hermetic {
			for _, use := range task.Uses {
				upstream := plan.Tasks[use.Task].Key
				if upstream == nil {
					return nil, fmt.Errorf("task %s is hermetic and uses output %s of task %s, which has no key; its own key would not decide what it read", name, use.Output, use.Task)
				}
				lines = append(lines, "use "+use.Task+" "+use.Output+" "+*upstream)
			}
			sum := sha256.Sum256([]byte("superschematic-task-key\n" + strings.Join(lines, "\n") + "\n"))
			key := hex.EncodeToString(sum[:])
			planned.Key = &key
		}
		plan.Tasks[name] = planned
	}
	return plan, nil
}

// resolveInputs resolves a task's inputs against the commit's tree and
// returns each input's digest. With strict set, a path a manifest names
// and the commit does not hold must be covered by an output the task
// uses, and a path input must cover a tracked file; without it, as for a
// base's definition, an input that finds nothing digests nothing.
func resolveInputs(in PlanInput, name string, task tasks.Task, strict bool) ([]tasks.PlanInput, error) {
	out := make([]tasks.PlanInput, 0, len(task.Inputs))
	for i, input := range task.Inputs {
		resolver, ok := in.Registry.TaskResolver(input.Kind)
		if !ok {
			return nil, fmt.Errorf("task %s: inputs[%d] is of kind %q, which no resolver of this binary resolves (registered: %s)", name, i, input.Kind, strings.Join(in.Registry.TaskResolvers(), ", "))
		}
		if err := in.Registry.CheckTaskInput(input.Kind, input.Spec); err != nil {
			return nil, fmt.Errorf("task %s: inputs[%d]: %w", name, i, err)
		}
		// A resolver is pure, so one kind and one spec resolve once for
		// the plan, however many tasks name them.
		key := input.Kind + "\x00" + string(input.Spec)
		res, seen := in.resolved[key]
		if !seen {
			start := time.Now()
			res = &resolvedInput{}
			resolution, err := resolver.Resolve(registry.TaskResolveRequest{Tree: in.Tree, Task: name, Spec: input.Spec})
			if err == nil {
				res.generated = resolution.Generated
				var lines []string
				if lines, err = entryLines(in.Tree, resolution.Paths); err == nil {
					res.digest, res.files = digest(lines), len(lines)
				}
			}
			res.err = err
			in.resolved[key] = res
			if in.Timings != nil {
				timing := in.timing(input.Kind)
				timing.Resolved++
				timing.Files += res.files
				timing.Duration += time.Since(start)
			}
		}
		if in.Timings != nil {
			in.timing(input.Kind).Inputs++
		}
		if res.err != nil {
			if !strict {
				out = append(out, tasks.PlanInput{Kind: input.Kind, Digest: digest(nil)})
				continue
			}
			return nil, fmt.Errorf("task %s: inputs[%d] (%s): %w", name, i, input.Kind, res.err)
		}
		if strict {
			for _, generated := range res.generated {
				if err := covered(in.File, name, task, generated); err != nil {
					return nil, err
				}
			}
		}
		out = append(out, tasks.PlanInput{Kind: input.Kind, Digest: res.digest, Files: res.files})
	}
	return out, nil
}

// timing returns the timing of a kind.
func (in PlanInput) timing(kind string) *KindTiming {
	if in.Timings.Kinds[kind] == nil {
		in.Timings.Kinds[kind] = &KindTiming{}
	}
	return in.Timings.Kinds[kind]
}

// InputDigest is the digest of an input that resolves to paths in a
// tree: the SHA-256 over one line per tracked file, `<mode> <path>
// <object id>`, each once and sorted.
func InputDigest(tree registry.TaskTree, paths []string) (string, error) {
	lines, err := entryLines(tree, paths)
	if err != nil {
		return "", err
	}
	return digest(lines), nil
}

// entryLines returns one line per tracked file of paths, each once and
// sorted: `<mode> <path> <object id>`. The mode is in the line, so an
// executable bit, a symbolic link and a submodule's commit change the
// digest as a file's bytes do.
func entryLines(tree registry.TaskTree, paths []string) ([]string, error) {
	seen := make(map[string]bool, len(paths))
	lines := make([]string, 0, len(paths))
	for _, p := range paths {
		if seen[p] {
			continue
		}
		seen[p] = true
		entry, ok := tree.Entry(p)
		if !ok {
			return nil, fmt.Errorf("the resolver named %s, which is no tracked file of the commit; a resolver returns tracked files only", p)
		}
		lines = append(lines, entry.Mode+" "+entry.Path+" "+entry.ID)
	}
	sort.Strings(lines)
	return lines, nil
}

// digest is the hex SHA-256 of the lines, each ended by a newline.
func digest(lines []string) string {
	h := sha256.New()
	for _, line := range lines {
		_, _ = h.Write([]byte(line))
		_, _ = h.Write([]byte{'\n'})
	}
	return hex.EncodeToString(h.Sum(nil))
}

// covered checks that a path a manifest names and the commit does not
// hold comes from an output the task uses. It names the task whose output
// covers the path when the edge is all that is missing.
func covered(file *tasks.File, name string, task tasks.Task, generated registry.TaskGeneratedPath) error {
	covers := func(taskName, output string) bool {
		for _, out := range file.Tasks[taskName].Outputs {
			if out.Name != output || out.Kind != tasks.OutputPaths {
				continue
			}
			for _, pattern := range out.Paths {
				if Covers(pattern, generated.Path) {
					return true
				}
			}
		}
		return false
	}
	for _, use := range task.Uses {
		if covers(use.Task, use.Output) {
			return nil
		}
	}
	for _, other := range file.Names() {
		if other == name {
			continue
		}
		for _, out := range file.Tasks[other].Outputs {
			if covers(other, out.Name) {
				return fmt.Errorf("task %s: %s names %s, which no tracked file of the commit holds. Output %s of task %s covers it: add `uses: [{ task: %s, output: %q }]`, so the key covers what wrote it", name, generated.From, generated.Path, out.Name, other, other, out.Name)
			}
		}
	}
	return fmt.Errorf("task %s: %s names %s, which no tracked file of the commit holds and no task's output covers. A file a build writes is read through uses: declare it as an output of the task that writes it", name, generated.From, generated.Path)
}

// Order returns the file's tasks so that each comes after the tasks it
// uses and the tasks it follows, and by name where the edges leave a
// choice. It refuses an edge to no task and a cycle, which the Tasks
// kind's verification refuses in a schema and a file edited by hand may
// still hold.
func Order(file *tasks.File) ([]string, error) {
	names := file.Names()
	waiting := make(map[string]int, len(names))
	dependents := map[string][]string{}
	for _, name := range names {
		task := file.Tasks[name]
		seen := map[string]bool{}
		edges := append([]string{}, task.After...)
		for _, use := range task.Uses {
			edges = append(edges, use.Task)
			if _, ok := file.Tasks[use.Task]; ok && !hasOutput(file.Tasks[use.Task], use.Output) {
				return nil, fmt.Errorf("task %s uses output %s of task %s, which declares no such output", name, use.Output, use.Task)
			}
		}
		for _, other := range edges {
			if _, ok := file.Tasks[other]; !ok {
				return nil, fmt.Errorf("task %s names task %s, which the task file does not hold", name, other)
			}
			if other == name {
				return nil, fmt.Errorf("task %s names itself", name)
			}
			if seen[other] {
				continue
			}
			seen[other] = true
			waiting[name]++
			dependents[other] = append(dependents[other], name)
		}
	}
	// The ready tasks are a heap by name: the first of them is taken
	// each time, and a file of many tasks is not sorted once per task.
	ready := &nameHeap{}
	for _, name := range names {
		if waiting[name] == 0 {
			heap.Push(ready, name)
		}
	}
	order := make([]string, 0, len(names))
	for ready.Len() > 0 {
		name := heap.Pop(ready).(string)
		order = append(order, name)
		for _, dependent := range dependents[name] {
			if waiting[dependent]--; waiting[dependent] == 0 {
				heap.Push(ready, dependent)
			}
		}
	}
	if len(order) != len(names) {
		var stuck []string
		for _, name := range names {
			if waiting[name] > 0 {
				stuck = append(stuck, name)
			}
		}
		return nil, fmt.Errorf("the tasks %s form a cycle through uses and after, so none of them can start first", strings.Join(stuck, ", "))
	}
	return order, nil
}

// nameHeap is a heap of task names, the first by name on top.
type nameHeap []string

func (h nameHeap) Len() int           { return len(h) }
func (h nameHeap) Less(i, j int) bool { return h[i] < h[j] }
func (h nameHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *nameHeap) Push(x any)        { *h = append(*h, x.(string)) }
func (h *nameHeap) Pop() any {
	old := *h
	name := old[len(old)-1]
	*h = old[:len(old)-1]
	return name
}

func hasOutput(task tasks.Task, name string) bool {
	for _, out := range task.Outputs {
		if out.Name == name {
			return true
		}
	}
	return false
}
