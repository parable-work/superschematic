// Package tasksgen is the Tasks kind's generator and its install (D203):
// it writes the task file a Tasks schema declares under the output root,
// and a build-all hook installs it at the path the service's config names,
// where it is committed.
package tasksgen

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
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

// installFile holds, beside the task file under the output root, the path
// the service's config installs it at. The build cache stores and restores
// it with the file, so the hook installs what a restore brought back
// without loading the service.
const installFile = "install"

// OutputSchema is the JSON Schema of outputs.tasks.
var OutputSchema = []byte(`{
	"type": "object",
	"additionalProperties": false,
	"properties": {
		"install": {"type": "string", "minLength": 1}
	}
}`)

// Options is outputs.tasks.
type Options struct {
	// Install is the path the task file is committed at, from the
	// repository root and slash-separated, as ci/tasks.json. Its directory
	// must exist. Unset, the file is written under the output root alone.
	Install string `json:"install,omitempty"`
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

// Generate writes the task file of the schema c holds under OutDir, which
// it empties first, with the install path beside it. It installs nothing:
// a build that restores the service from the cache runs no generator, so
// the install is the hook's, which runs on every build-all.
func Generate(c registry.GenerateContext) error {
	o, err := OptionsOf(c)
	if err != nil {
		return fmt.Errorf("tasks %s: %w", c.Config.Name, err)
	}
	data, err := inttasks.RenderBytes(c.Schema)
	if err != nil {
		return fmt.Errorf("tasks %s: %w", c.Config.Name, err)
	}
	dir := OutDir(c.Options.OutputRoot, c.Config.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("tasks %s: %w", c.Config.Name, err)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("tasks %s: %w", c.Config.Name, err)
	}
	if err := os.WriteFile(filepath.Join(dir, tasks.FileName), data, 0o644); err != nil {
		return fmt.Errorf("tasks %s: %w", c.Config.Name, err)
	}
	if o.Install != "" {
		if err := os.WriteFile(filepath.Join(dir, installFile), []byte(o.Install+"\n"), 0o644); err != nil {
			return fmt.Errorf("tasks %s: %w", c.Config.Name, err)
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
		rel, err := os.ReadFile(filepath.Join(dir, installFile))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return fmt.Errorf("tasks %s: %w", svc.Name, err)
		}
		install := strings.TrimSpace(string(rel))
		if err := CheckInstall(install); err != nil {
			return fmt.Errorf("tasks %s: %w", svc.Name, err)
		}
		data, err := os.ReadFile(filepath.Join(dir, tasks.FileName))
		if err != nil {
			return fmt.Errorf("tasks %s: %w", svc.Name, err)
		}
		root, err := GitRoot(svc.Dir)
		if err != nil {
			return fmt.Errorf("tasks %s: %w", svc.Name, err)
		}
		if root == "" {
			// A source-only build context, such as a Docker stage with
			// the schema tree and no repository: there is no committed
			// file to keep current, and `tasks --check` fails there.
			logf(bc, "  - task file of %s not installed: no repository holds %s\n", svc.Name, svc.Dir)
			continue
		}
		target := filepath.Join(root, filepath.FromSlash(install))
		if other, dup := installed[target]; dup {
			return fmt.Errorf("tasks %s and tasks %s both install their task file at %s; give each service its own outputs.tasks.install", other, svc.Name, install)
		}
		installed[target] = svc.Name
		changed, err := Install(target, data)
		if err != nil {
			return fmt.Errorf("tasks %s: %w", svc.Name, err)
		}
		if changed {
			logf(bc, "  + task file of %s installed at %s\n", svc.Name, target)
		}
	}
	return nil
}

func logf(bc registry.BuildAllContext, format string, args ...any) {
	if bc.Log != nil {
		_, _ = fmt.Fprintf(bc.Log, format, args...)
	}
}

// Install writes data at target and reports whether the file changed. The
// directory must exist: an install never makes one, so a slip in the path
// fails and leaves no stray tree in the repository.
func Install(target string, data []byte) (bool, error) {
	dir := filepath.Dir(target)
	if info, err := os.Stat(dir); err != nil || !info.IsDir() {
		return false, fmt.Errorf("the task file installs at %s, and the directory %s does not exist; create it, or correct outputs.tasks.install", target, dir)
	}
	if have, err := os.ReadFile(target); err == nil && bytes.Equal(have, data) {
		return false, nil
	}
	if err := os.WriteFile(target, data, 0o644); err != nil {
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
