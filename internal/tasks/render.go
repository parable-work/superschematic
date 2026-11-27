package tasks

import (
	"bytes"
	"encoding/json"
	"fmt"
	"sort"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

// Render returns the task file a Tasks schema declares. It is a pure
// function of the schema: it reads no file, no clock and nothing of the
// machine, so one schema gives one file from any checkout.
//
// A task's inputs, edges, outputs and service containers are sets, so
// each is written sorted and the order a schema lists them in changes no
// byte and no key. `run` keeps its order.
func Render(schema *ir.Schema) (*tasks.File, error) {
	decl := ir.TasksOf(schema)
	file := &tasks.File{Version: tasks.FileVersion, Tasks: map[string]tasks.Task{}}
	if decl == nil {
		return file, nil
	}
	file.Image, file.Platform = decl.Image, decl.Platform
	sections, err := canonicalSections(decl.Sections)
	if err != nil {
		return nil, fmt.Errorf("@tasks sections: %w", err)
	}
	file.Sections = sections
	for _, t := range decl.Tasks {
		if _, dup := file.Tasks[t.Name]; dup {
			return nil, fmt.Errorf("two tasks are named %s", t.Name)
		}
		task := tasks.Task{
			Run:      append([]string{}, t.Run...),
			After:    append([]string(nil), t.After...),
			Env:      t.Env,
			Hermetic: t.Hermetic,
		}
		if t.OwnImage {
			task.Image = t.Image
		}
		if t.OwnPlatform {
			task.Platform = t.Platform
		}
		sort.Strings(task.After)
		for _, input := range t.Inputs {
			spec, err := canonicalRaw(input.Spec)
			if err != nil {
				return nil, fmt.Errorf("task %s: input of kind %s: %w", t.Name, input.Kind, err)
			}
			task.Inputs = append(task.Inputs, tasks.Input{Kind: input.Kind, Spec: spec})
		}
		sort.SliceStable(task.Inputs, func(i, j int) bool {
			a, b := task.Inputs[i], task.Inputs[j]
			if a.Kind != b.Kind {
				return a.Kind < b.Kind
			}
			return bytes.Compare(a.Spec, b.Spec) < 0
		})
		for _, use := range t.Uses {
			task.Uses = append(task.Uses, tasks.Use{Task: use.Task, Output: use.Output})
		}
		sort.Slice(task.Uses, func(i, j int) bool {
			a, b := task.Uses[i], task.Uses[j]
			if a.Task != b.Task {
				return a.Task < b.Task
			}
			return a.Output < b.Output
		})
		for _, out := range t.Outputs {
			paths := append([]string(nil), out.Paths...)
			sort.Strings(paths)
			task.Outputs = append(task.Outputs, tasks.Output{Name: out.Name, Kind: string(out.OutputKind()), Paths: paths})
		}
		sort.Slice(task.Outputs, func(i, j int) bool { return task.Outputs[i].Name < task.Outputs[j].Name })
		for _, svc := range t.Services {
			task.Services = append(task.Services, tasks.Service{Name: svc.Name, Image: svc.Image})
		}
		sort.Slice(task.Services, func(i, j int) bool { return task.Services[i].Name < task.Services[j].Name })
		if task.Sections, err = canonicalSections(t.Sections); err != nil {
			return nil, fmt.Errorf("task %s sections: %w", t.Name, err)
		}
		file.Tasks[t.Name] = task
	}
	return file, nil
}

// RenderBytes is Render, encoded as the file is committed.
func RenderBytes(schema *ir.Schema) ([]byte, error) {
	file, err := Render(schema)
	if err != nil {
		return nil, err
	}
	return file.Encode()
}

// canonicalRaw returns a decoded JSON object in canonical JSON, or nil
// for none: an empty object is none, as the data forms leave it out.
func canonicalRaw(value map[string]any) (json.RawMessage, error) {
	if len(value) == 0 {
		return nil, nil
	}
	return tasks.Canonical(value)
}

func canonicalSections(sections map[string]map[string]any) (map[string]json.RawMessage, error) {
	if len(sections) == 0 {
		return nil, nil
	}
	out := make(map[string]json.RawMessage, len(sections))
	for name, part := range sections {
		value, err := tasks.Canonical(map[string]any(part))
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		out[name] = value
	}
	return out, nil
}
