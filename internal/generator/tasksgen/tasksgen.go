// Package tasksgen is the Tasks kind's generator and its install (D203):
// it writes the task file a Tasks schema declares under the output root,
// with the workflow of each CI renderer the service's config names, and a
// build-all hook installs each at the path the config gives it, where it
// is committed.
package tasksgen

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/parable-work/superschematic/internal/registry"
	inttasks "github.com/parable-work/superschematic/internal/tasks"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

// Name is the generator's name in the Tasks kind's pipeline, and the
// outputs key it reads.
const Name = "tasks"

// HookName is the name of the build-all hook that installs the file.
const HookName = "tasks-install"

// manifestFile holds, beside the files under the output root, the path
// each installs at. The build cache stores and restores it with them, so
// the hook installs what a restore brought back without loading the
// service.
const manifestFile = "install.json"

// ciDir is where a renderer's files are written under OutDir, each
// renderer in a directory of its name.
const ciDir = "ci"

// OutputSchema is the JSON Schema of outputs.tasks.
var OutputSchema = []byte(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"install": {"type": "string", "minLength": 1},
		"ci": {
			"type": "object",
			"additionalProperties": {
				"type": "object",
				"additionalProperties": false,
				"properties": {
					"install": {"type": "string", "minLength": 1}
				}
			}
		}
	}
}`)

// Options is outputs.tasks.
type Options struct {
	// Install is the path the task file is committed at, from the
	// repository root and slash-separated, as ci/tasks.json. Its directory
	// must exist. Unset, the file is written under the output root alone.
	Install string `json:"install,omitempty"`

	// CI names, by renderer, the CI the tasks run on: `github` writes a
	// GitHub Actions workflow that runs each task as a job.
	CI map[string]CIOptions `json:"ci,omitempty"`
}

// CIOptions is outputs.tasks.ci.<renderer>.
type CIOptions struct {
	// Install is the directory the renderer's files are committed in,
	// from the repository root. Empty is the renderer's own directory,
	// `.github/workflows` for github. It must exist.
	Install string `json:"install,omitempty"`
}

// Artifact is one file a Tasks service's build writes: the task file, or
// a file of a CI renderer.
type Artifact struct {
	// What names the file in a message: "task file", "github workflow".
	What string `json:"what"`
	// File is the file's path under OutDir, slash-separated.
	File string `json:"file"`
	// Install is the path the file is committed at, from the repository
	// root, or "" for a file that is written under the output root alone.
	Install string `json:"install,omitempty"`
	// Option is the config option that gives Install, for a message.
	Option string `json:"option,omitempty"`
	// Data is the file's bytes.
	Data []byte `json:"-"`
}

// OutDir is where a service's task file is written under the output root.
func OutDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "tasks", service)
}

// CheckInstall refuses an install path that is no JSON file's path inside
// the repository.
func CheckInstall(install string) error {
	if err := registry.CheckTaskPath(install); err != nil {
		return fmt.Errorf("outputs.tasks.install %v", err)
	}
	if strings.ContainsAny(install, "*?[") || path.Ext(install) != ".json" {
		return fmt.Errorf("outputs.tasks.install is %q; it is the path the task file is committed at, a .json file's from the repository root, as ci/%s", install, tasks.FileName)
	}
	return nil
}

// checkArtifact refuses a file a manifest or a renderer names that is no
// file's path inside its directory, or whose install path is none inside
// the repository.
func checkArtifact(a Artifact) error {
	if err := registry.CheckTaskPath(a.File); err != nil || strings.ContainsAny(a.File, "*?[") {
		return fmt.Errorf("the %s is written at %q, which is no file's path inside the service's output", a.What, a.File)
	}
	if a.Install == "" {
		return nil
	}
	if err := registry.CheckTaskPath(a.Install); err != nil {
		return fmt.Errorf("%s %v", a.Option, err)
	}
	if strings.ContainsAny(a.Install, "*?[") {
		return fmt.Errorf("%s gives %q, which holds a wildcard; an install names one place", a.Option, a.Install)
	}
	return nil
}

// OptionsOf reads outputs.tasks of a generator's context.
func OptionsOf(c registry.GenerateContext) (Options, error) {
	var o Options
	if err := registry.DecodeOutput(c.Outputs, Name, &o); err != nil {
		return o, err
	}
	if o.Install != "" {
		if err := CheckInstall(o.Install); err != nil {
			return o, err
		}
	}
	return o, nil
}

// Render returns the files of a Tasks service: its task file, and the
// files of each CI renderer outputs.tasks.ci names, in the renderers'
// order by name. It reads the schema and the options and nothing of the
// machine, so one schema gives the same files from any checkout.
func Render(reg *registry.Registry, schema *ir.Schema, service string, o Options) ([]Artifact, error) {
	data, err := inttasks.RenderBytes(schema)
	if err != nil {
		return nil, err
	}
	out := []Artifact{{What: "task file", File: tasks.FileName, Install: o.Install, Option: "outputs.tasks.install", Data: data}}
	if len(o.CI) == 0 {
		return out, nil
	}
	file, err := tasks.Decode(data)
	if err != nil {
		return nil, err
	}
	renderers := make([]string, 0, len(o.CI))
	for name := range o.CI {
		renderers = append(renderers, name)
	}
	sort.Strings(renderers)
	for _, name := range renderers {
		option := "outputs.tasks.ci." + name
		spec, ok := reg.CIRenderer(name)
		if !ok {
			return nil, fmt.Errorf("%s names a CI renderer that is not registered (registered CI renderers: %s)", option, strings.Join(reg.CIRenderers(), ", "))
		}
		if spec.RenderTasks == nil {
			return nil, fmt.Errorf("%s names a CI renderer that renders a stack and no Tasks service", option)
		}
		dir := o.CI[name].Install
		if dir == "" {
			dir = spec.Dir
		}
		files, err := spec.RenderTasks(registry.CITasksRequest{Service: service, File: file})
		if err != nil {
			return nil, fmt.Errorf("CI renderer %s: %w", name, err)
		}
		for _, f := range files {
			a := Artifact{
				What:    name + " workflow",
				File:    path.Join(ciDir, name, f.Path),
				Install: path.Join(dir, f.Path),
				Option:  option + ".install",
				Data:    f.Data,
			}
			if f.Path == "" || path.Clean(f.Path) != f.Path || path.IsAbs(f.Path) || strings.HasPrefix(f.Path, "../") || f.Path == ".." {
				return nil, fmt.Errorf("CI renderer %s writes %q, which is not a slash-separated path inside its directory", name, f.Path)
			}
			if err := checkArtifact(a); err != nil {
				return nil, err
			}
			out = append(out, a)
		}
	}
	return out, nil
}

// Generate writes the files of the schema c holds under OutDir, which it
// empties first, with the manifest of where each installs beside them.
// It installs nothing: a build that restores the service from the cache
// runs no generator, so the install is the hook's, which runs on every
// build-all.
func Generate(c registry.GenerateContext) error {
	fail := func(err error) error { return fmt.Errorf("tasks %s: %w", c.Config.Name, err) }
	o, err := OptionsOf(c)
	if err != nil {
		return fail(err)
	}
	artifacts, err := Render(c.Registry, c.Schema, c.Config.Name, o)
	if err != nil {
		return fail(err)
	}
	dir := OutDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fail(err)
	}
	var installs []Artifact
	for _, a := range artifacts {
		target := filepath.Join(dir, filepath.FromSlash(a.File))
		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return fail(err)
		}
		if err := os.WriteFile(target, a.Data, 0o644); err != nil {
			return fail(err)
		}
		if a.Install != "" {
			installs = append(installs, a)
		}
	}
	if len(installs) > 0 {
		manifest, err := json.MarshalIndent(installs, "", "  ")
		if err != nil {
			return fail(err)
		}
		if err := os.WriteFile(filepath.Join(dir, manifestFile), append(manifest, '\n'), 0o644); err != nil {
			return fail(err)
		}
	}
	c.Done(Name, dir)
	return nil
}

// Hook is the build-all hook that installs each Tasks service's file. It
// runs on every build-all, whether the service was built, restored from
// the cache or found up to date, so the committed file follows the
// schema whichever of the three the build did.
func Hook() registry.BuildAllHook {
	return registry.BuildAllHook{Name: HookName, Run: installAll}
}

func installAll(_ context.Context, bc registry.BuildAllContext) error {
	installed := map[string]string{}
	for _, svc := range bc.Services {
		if svc.Kind != string(ir.SchemaKindTasks) {
			continue
		}
		dir := OutDir(bc.OutputRoot, svc.Name)
		fail := func(err error) error { return fmt.Errorf("tasks %s: %w", svc.Name, err) }
		raw, err := os.ReadFile(filepath.Join(dir, manifestFile))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fail(err)
		}
		var installs []Artifact
		if err := json.Unmarshal(raw, &installs); err != nil {
			return fail(fmt.Errorf("%s: %w", manifestFile, err))
		}
		root, err := GitRoot(svc.Dir)
		if err != nil {
			return fail(err)
		}
		if root == "" {
			// A source-only build context, such as a Docker stage with
			// the schema tree and no repository: there is no committed
			// file to keep current, and `tasks --check` fails there.
			logf(bc, "  - task file of %s not installed: no repository holds %s\n", svc.Name, svc.Dir)
			continue
		}
		for _, a := range installs {
			if a.Install == "" {
				continue
			}
			if err := checkArtifact(a); err != nil {
				return fail(err)
			}
			data, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(a.File)))
			if err != nil {
				return fail(err)
			}
			target := filepath.Join(root, filepath.FromSlash(a.Install))
			if other, dup := installed[target]; dup {
				return fmt.Errorf("tasks %s and tasks %s both install a file at %s; give each service its own %s", other, svc.Name, a.Install, a.Option)
			}
			installed[target] = svc.Name
			a.Data = data
			changed, err := Install(target, a)
			if err != nil {
				return fail(err)
			}
			if changed {
				logf(bc, "  + %s of %s installed at %s\n", a.What, svc.Name, target)
			}
		}
	}
	return nil
}

func logf(bc registry.BuildAllContext, format string, args ...any) {
	if bc.Log != nil {
		_, _ = fmt.Fprintf(bc.Log, format, args...)
	}
}

// Install writes a file's bytes at target and reports whether the file
// changed. The directory must exist: an install never makes one, so a
// slip in the path fails and leaves no stray tree in the repository.
func Install(target string, a Artifact) (bool, error) {
	dir := filepath.Dir(target)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, fmt.Errorf("the %s installs at %s, and the directory %s does not exist; create it, or correct %s", a.What, target, dir, a.Option)
	}
	if have, err := os.ReadFile(target); err == nil && bytes.Equal(have, a.Data) {
		return false, nil
	}
	if err := os.WriteFile(target, a.Data, 0o644); err != nil {
		return false, err
	}
	return true, nil
}

// GitRoot returns the repository root above dir: the nearest ancestor
// that holds .git, a directory in a checkout and a file in a linked
// worktree. It is "" when there is none.
func GitRoot(dir string) (string, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	for {
		if _, statErr := os.Stat(filepath.Join(dir, ".git")); statErr == nil {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
