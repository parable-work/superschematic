package cli

import (
	"context"
	"errors"
	"testing"

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
