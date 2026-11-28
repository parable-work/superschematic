package tswriter

import (
	"fmt"
	"sort"
	"strconv"
	"strings"

	ir "github.com/parable-work/superschematic/ir"
)

// The Tasks kind's declarations (D203): each @tasks and @task class is
// written with its decorator, the argument the reader evaluates back to
// the declaration. A task named by an edge is its class. A path input
// whose spec is one path is written as that path, as the reader takes it.

// emitTaskDeclarations renders the declarations def carries.
func (e *emitter) emitTaskDeclarations(def *ir.TypeDef) {
	if t := def.Tasks; t != nil {
		var parts []string
		if t.Image != "" {
			parts = append(parts, "image: "+quote(t.Image))
		}
		if t.Platform != "" {
			parts = append(parts, "platform: "+quote(t.Platform))
		}
		if sections := e.sectionsLiteral(t.Sections); sections != "" {
			parts = append(parts, "sections: "+sections)
		}
		fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("tasks"), objectLiteral(parts))
	}
	t := def.Task
	if t == nil {
		return
	}
	var parts []string
	if t.Name != "" {
		parts = append(parts, "name: "+quote(t.Name))
	}
	parts = append(parts, "run: "+stringListLiteral(t.Run))
	if len(t.Inputs) > 0 {
		inputs := make([]string, len(t.Inputs))
		for i, input := range t.Inputs {
			inputs[i] = e.taskInputLiteral(input)
		}
		parts = append(parts, "inputs: ["+strings.Join(inputs, ", ")+"]")
	}
	if len(t.Uses) > 0 {
		uses := make([]string, len(t.Uses))
		for i, use := range t.Uses {
			uses[i] = objectLiteral([]string{"task: " + e.ident(use.Task, "task"), "output: " + quote(use.Output)})
		}
		parts = append(parts, "uses: ["+strings.Join(uses, ", ")+"]")
	}
	if len(t.After) > 0 {
		after := make([]string, len(t.After))
		for i, class := range t.After {
			after[i] = e.ident(class, "task")
		}
		parts = append(parts, "after: ["+strings.Join(after, ", ")+"]")
	}
	if len(t.Outputs) > 0 {
		outputs := make([]string, len(t.Outputs))
		for i, out := range t.Outputs {
			members := []string{"name: " + quote(out.Name)}
			if out.Kind != "" {
				members = append(members, "kind: "+quote(string(out.Kind)))
			}
			if len(out.Paths) > 0 {
				members = append(members, "paths: "+stringListLiteral(out.Paths))
			}
			outputs[i] = objectLiteral(members)
		}
		parts = append(parts, "outputs: ["+strings.Join(outputs, ", ")+"]")
	}
	if len(t.Services) > 0 {
		services := make([]string, len(t.Services))
		for i, svc := range t.Services {
			services[i] = objectLiteral([]string{"name: " + quote(svc.Name), "image: " + quote(svc.Image)})
		}
		parts = append(parts, "services: ["+strings.Join(services, ", ")+"]")
	}
	if len(t.Env) > 0 {
		names := make([]string, 0, len(t.Env))
		for name := range t.Env {
			names = append(names, name)
		}
		sort.Strings(names)
		members := make([]string, len(names))
		for i, name := range names {
			members[i] = propertyName(name) + ": " + quote(t.Env[name])
		}
		parts = append(parts, "env: "+objectLiteral(members))
	}
	if t.Image != "" {
		parts = append(parts, "image: "+quote(t.Image))
	}
	if t.Platform != "" {
		parts = append(parts, "platform: "+quote(t.Platform))
	}
	if t.Hermetic != nil {
		parts = append(parts, "hermetic: "+strconv.FormatBool(*t.Hermetic))
	}
	if sections := e.sectionsLiteral(t.Sections); sections != "" {
		parts = append(parts, "sections: "+sections)
	}
	fmt.Fprintf(&e.body, "@%s(%s)\n", e.use("task"), objectLiteral(parts))
}

// taskInputLiteral renders one input: a path input of one path and no
// exclusion as the path, and any other as its kind and spec.
func (e *emitter) taskInputLiteral(input ir.TaskInput) string {
	spec := input.Spec
	if input.Kind == ir.TaskPathInput && len(spec) == 1 {
		if paths, ok := spec["paths"].([]any); ok && len(paths) == 1 {
			if p, ok := paths[0].(string); ok {
				return quote(p)
			}
		}
	}
	members := []string{"kind: " + quote(input.Kind)}
	if len(spec) > 0 {
		members = append(members, "spec: "+valueLiteral(map[string]any(spec)))
	}
	return objectLiteral(members)
}

// sectionsLiteral renders a declaration's sections, "" for none.
func (e *emitter) sectionsLiteral(sections map[string]map[string]any) string {
	if len(sections) == 0 {
		return ""
	}
	value := make(map[string]any, len(sections))
	for name, part := range sections {
		value[name] = part
	}
	return valueLiteral(value)
}

// taskClassRefs returns the classes def's @task names as values: the
// tasks whose outputs it uses and the tasks it follows.
func taskClassRefs(def *ir.TypeDef) []string {
	if def.Task == nil {
		return nil
	}
	refs := append([]string{}, def.Task.After...)
	for _, use := range def.Task.Uses {
		refs = append(refs, use.Task)
	}
	return refs
}
