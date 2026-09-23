package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/parable-work/superschematic/internal/registry"
)

func TestRunBuildAllHooksNamesTheFailingHookAndStops(t *testing.T) {
	boom := errors.New("creating chart/generated: permission denied")
	var ran []string
	hooks := []registry.BuildAllHook{
		{Name: "first", Run: func(context.Context, registry.BuildAllContext) error {
			ran = append(ran, "first")
			return nil
		}},
		{Name: "mergedValues", Run: func(context.Context, registry.BuildAllContext) error {
			ran = append(ran, "mergedValues")
			return boom
		}},
		{Name: "never", Run: func(context.Context, registry.BuildAllContext) error {
			ran = append(ran, "never")
			return nil
		}},
	}

	err := runBuildAllHooks(context.Background(), hooks, registry.BuildAllContext{})

	if !errors.Is(err, boom) {
		t.Fatalf("error must wrap the hook's error, got %v", err)
	}
	if want := "build-all hook mergedValues: " + boom.Error(); err.Error() != want {
		t.Errorf("error = %q, want %q", err.Error(), want)
	}
	if len(ran) != 2 || ran[0] != "first" || ran[1] != "mergedValues" {
		t.Errorf("hooks ran = %v, want [first mergedValues]", ran)
	}
}

func TestRunBuildAllHooksPassesTheContextThrough(t *testing.T) {
	want := registry.BuildAllContext{ServiceNames: []string{"web-db", "web-api"}, RepoRoot: "/repo", OutputRoot: "/repo/dist"}
	var got registry.BuildAllContext
	hooks := []registry.BuildAllHook{{Name: "capture", Run: func(_ context.Context, bc registry.BuildAllContext) error {
		got = bc
		return nil
	}}}

	if err := runBuildAllHooks(context.Background(), hooks, want); err != nil {
		t.Fatalf("runBuildAllHooks: %v", err)
	}
	if got.RepoRoot != want.RepoRoot || got.OutputRoot != want.OutputRoot || len(got.ServiceNames) != 2 || got.ServiceNames[1] != "web-api" {
		t.Errorf("hook received %+v, want %+v", got, want)
	}
	if err := runBuildAllHooks(context.Background(), nil, want); err != nil {
		t.Errorf("no hooks must be a no-op, got %v", err)
	}
}

// captureHooks is an extension whose only registration is a build-all hook
// that hands every context it gets to seen.
type captureHooks struct {
	seen func(registry.BuildAllContext)
}

func (captureHooks) Name() string { return "capture" }

func (e captureHooks) Register(r *registry.Registry) error {
	return r.RegisterBuildAllHook(registry.BuildAllHook{
		Name:      "capture",
		Extension: "capture",
		Run: func(_ context.Context, bc registry.BuildAllContext) error {
			e.seen(bc)
			return nil
		},
	})
}

// TestBuildAllCommand_HooksSeeEveryServiceWhateverWasBuilt: a hook runs on a
// build that built the service, on one where it was up to date and on one
// where it was restored from the cache, and every time it gets the
// service's directory and output directories. Only the build that loaded
// the service has its IR.
func TestBuildAllCommand_HooksSeeEveryServiceWhateverWasBuilt(t *testing.T) {
	servicesRoot := prepareJSONServicesRoot(t)
	outDir := t.TempDir()
	cacheRoot := t.TempDir()
	goOutput := filepath.Join(outDir, "types", "go", "fixture-db")
	var calls []registry.BuildAllContext
	var goModAtHook []bool
	hooks := captureHooks{seen: func(bc registry.BuildAllContext) {
		calls = append(calls, bc)
		_, err := os.Stat(filepath.Join(goOutput, "go.mod"))
		goModAtHook = append(goModAtHook, err == nil)
	}}
	run := func() string {
		t.Helper()
		out := new(bytes.Buffer)
		root := New(Config{}, hooks)
		root.SetOut(out)
		root.SetErr(new(bytes.Buffer))
		root.SetArgs([]string{"build-all", servicesRoot, "--out", outDir, "--cache", "--cache-root", cacheRoot})
		require.NoError(t, root.Execute())
		return out.String()
	}

	assert.Contains(t, run(), "OK: fixture-db (built, cached)")
	assert.Contains(t, run(), "OK: fixture-db (up to date")
	require.NoError(t, os.RemoveAll(goOutput))
	require.NoError(t, os.MkdirAll(goOutput, 0o755))
	assert.Contains(t, run(), "OK: fixture-db (restored from cache)")

	require.Len(t, calls, 3, "the hook must run on every build-all")
	for i, bc := range calls {
		require.Len(t, bc.Services, 1, "call %d", i)
		service := bc.Services[0]
		assert.Equal(t, "fixture-db", service.Name, "call %d", i)
		assert.Equal(t, "DB", service.Kind, "call %d", i)
		assert.Equal(t, filepath.Join(servicesRoot, "fixture-db-json"), service.Dir, "call %d", i)
		assert.Contains(t, service.OutputDirs, goOutput, "call %d", i)
		for _, dir := range service.OutputDirs {
			assert.DirExists(t, dir, "call %d", i)
		}
		assert.Equal(t, []string{"fixture-db"}, bc.ServiceNames, "call %d", i)
		assert.Equal(t, outDir, bc.OutputRoot, "call %d", i)
		_, loaded := bc.SchemaFor("fixture-db")
		assert.Equal(t, i == 0, loaded, "call %d: only the build that built the service loaded its IR", i)
	}
	assert.Equal(t, []bool{true, true, true}, goModAtHook, "the service's output must be in place when the hook runs")
}
