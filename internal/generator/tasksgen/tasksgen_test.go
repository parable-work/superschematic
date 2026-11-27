package tasksgen

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/tasks"
)

// builtService lays out a repository with a Tasks service and its output
// as a build, or a restore from the cache, leaves it: the task file and
// the install path under the output root. It returns the hook's context.
func builtService(t *testing.T, repo bool, services map[string]string) (registry.BuildAllContext, string) {
	t.Helper()
	root := t.TempDir()
	if repo {
		require.NoError(t, os.Mkdir(filepath.Join(root, ".git"), 0o755))
	}
	out := filepath.Join(root, "schemas", "dist")
	bc := registry.BuildAllContext{OutputRoot: out, RepoRoot: root, Log: new(bytes.Buffer)}
	for _, name := range []string{"repo-tasks", "more-tasks", "shop-api"} {
		install, ok := services[name]
		if !ok {
			continue
		}
		dir := filepath.Join(root, "schemas", "services", name)
		require.NoError(t, os.MkdirAll(dir, 0o755))
		kind := string(ir.SchemaKindTasks)
		if name == "shop-api" {
			kind = string(ir.SchemaKindAPI)
		}
		bc.Services = append(bc.Services, registry.BuildAllService{Name: name, Kind: kind, Dir: dir, OutputDirs: []string{OutDir(out, name)}})
		require.NoError(t, os.MkdirAll(OutDir(out, name), 0o755))
		require.NoError(t, os.WriteFile(filepath.Join(OutDir(out, name), tasks.FileName), []byte("{\"tasks\": {}, \"version\": 1}\n"), 0o644))
		if install != "" {
			require.NoError(t, os.WriteFile(filepath.Join(OutDir(out, name), installFile), []byte(install+"\n"), 0o644))
		}
	}
	return bc, root
}

// TestTheHookInstallsWhatTheOutputRootHolds: the hook loads no schema. It
// installs the file the output root holds, so a service the build restored
// from the cache is installed as one it built.
func TestTheHookInstallsWhatTheOutputRootHolds(t *testing.T) {
	bc, root := builtService(t, true, map[string]string{"repo-tasks": "ci/tasks.json", "shop-api": ""})
	require.NoError(t, os.Mkdir(filepath.Join(root, "ci"), 0o755))
	hook := Hook()
	require.Equal(t, HookName, hook.Name)
	require.NoError(t, hook.Run(context.Background(), bc))
	got, err := os.ReadFile(filepath.Join(root, "ci", "tasks.json"))
	require.NoError(t, err)
	require.Equal(t, "{\"tasks\": {}, \"version\": 1}\n", string(got))
	require.Contains(t, bc.Log.(*bytes.Buffer).String(), "task file of repo-tasks installed at")

	// A second run finds the file current, writes nothing and says
	// nothing.
	info, err := os.Stat(filepath.Join(root, "ci", "tasks.json"))
	require.NoError(t, err)
	bc.Log = new(bytes.Buffer)
	require.NoError(t, hook.Run(context.Background(), bc))
	again, err := os.Stat(filepath.Join(root, "ci", "tasks.json"))
	require.NoError(t, err)
	require.Equal(t, info.ModTime(), again.ModTime())
	require.Empty(t, bc.Log.(*bytes.Buffer).String())
}

func TestTheHookFailsOnAnInstallDirectoryThatDoesNotExist(t *testing.T) {
	bc, root := builtService(t, true, map[string]string{"repo-tasks": "ci/tasks.json"})
	err := Hook().Run(context.Background(), bc)
	require.ErrorContains(t, err, "tasks repo-tasks: the task file installs at "+filepath.Join(root, "ci", "tasks.json"))
	require.ErrorContains(t, err, "does not exist; create it, or correct outputs.tasks.install")
	require.NoDirExists(t, filepath.Join(root, "ci"))
}

// A source-only build context has no repository, so no committed file to
// keep current: the hook says so and installs nothing.
func TestTheHookSkipsABuildWithNoRepository(t *testing.T) {
	bc, root := builtService(t, false, map[string]string{"repo-tasks": "ci/tasks.json"})
	require.NoError(t, os.Mkdir(filepath.Join(root, "ci"), 0o755))
	require.NoError(t, Hook().Run(context.Background(), bc))
	require.NoFileExists(t, filepath.Join(root, "ci", "tasks.json"))
	require.Contains(t, bc.Log.(*bytes.Buffer).String(), "task file of repo-tasks not installed: no repository holds")
}

func TestTheHookLeavesAServiceWithNoInstallPath(t *testing.T) {
	bc, _ := builtService(t, true, map[string]string{"repo-tasks": ""})
	require.NoError(t, Hook().Run(context.Background(), bc))
}

func TestTheHookRefusesTwoServicesAtOnePath(t *testing.T) {
	bc, root := builtService(t, true, map[string]string{"repo-tasks": "ci/tasks.json", "more-tasks": "ci/tasks.json"})
	require.NoError(t, os.Mkdir(filepath.Join(root, "ci"), 0o755))
	err := Hook().Run(context.Background(), bc)
	require.ErrorContains(t, err, "tasks repo-tasks and tasks more-tasks both install their task file at ci/tasks.json")
}

func TestCheckInstall(t *testing.T) {
	require.NoError(t, CheckInstall("ci/tasks.json"))
	require.NoError(t, CheckInstall("tasks.json"))
	for install, want := range map[string]string{
		"ci":             "a .json file's",
		"ci/tasks.yaml":  "a .json file's",
		"ci/*.json":      "a .json file's",
		"/ci/tasks.json": "is absolute",
		"../tasks.json":  "leaves the repository",
		`ci\tasks.json`:  "backslash",
	} {
		require.ErrorContains(t, CheckInstall(install), want, install)
	}
}
