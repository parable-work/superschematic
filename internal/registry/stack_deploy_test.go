package registry

import (
	"context"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

type nopState struct{}

func (nopState) Backend(context.Context, *ir.ResolvedEnvironment) (StateBackend, error) {
	return StateBackend{}, nil
}
func (nopState) ReadManifest(context.Context, Run) ([]byte, error) { return nil, ErrNoManifest }
func (nopState) WriteManifest(context.Context, Run, []byte) error  { return nil }
func (nopState) DeleteManifest(context.Context, Run) error         { return nil }
func (nopState) Bootstrap(context.Context, BootstrapRequest) error { return nil }
func (nopState) Migrate(context.Context, MigrationRequest) error   { return nil }

// TestRegisterTargetDeploySeams covers the refusals of a deploy seam a
// target cannot use.
func TestRegisterTargetDeploySeams(t *testing.T) {
	cases := []struct {
		name string
		spec TargetSpec
		want string
	}{
		{"state without provisioner", TargetSpec{Name: "fake", State: nopState{}}, `target "fake" has State but names no provisioner`},
		{"bootstrap without provisioner", TargetSpec{Name: "fake", State: nopState{}, Bootstrap: nopState{}}, "has State, Bootstrap but names no provisioner"},
		{"bootstrap without state", TargetSpec{Name: "fake", Provisioner: "fake", Bootstrap: nopState{}}, "has Bootstrap or Migrations but no State"},
		{"migrations without state", TargetSpec{Name: "fake", Provisioner: "fake", Migrations: nopState{}}, "has Bootstrap or Migrations but no State"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := New(naming.Default()).RegisterTarget(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterTarget = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	sound := TargetSpec{Name: "fake", Provisioner: "fake", State: nopState{}, Bootstrap: nopState{}, Migrations: nopState{}}
	if err := New(naming.Default()).RegisterTarget(sound); err != nil {
		t.Errorf("a target with every seam: %v", err)
	}
}

// TestRunCheckAndName covers a run's parameter checks and its name.
func TestRunCheckAndName(t *testing.T) {
	plain := &ir.ResolvedEnvironment{Environment: "Staging"}
	member := &ir.ResolvedEnvironment{Environment: "Preview", Parameters: []string{"pr", "region"}}
	for _, tc := range []struct {
		name string
		run  Run
		want string
	}{
		{"no environment", Run{}, "no environment"},
		{"missing value", Run{Environment: member, Parameters: map[string]string{"pr": "1"}}, "takes parameter region"},
		{"bad value", Run{Environment: member, Parameters: map[string]string{"pr": "1 2", "region": "eu"}}, "a value is letters"},
		{"unknown parameter", Run{Environment: plain, Parameters: map[string]string{"pr": "1"}}, "has no parameter pr"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := tc.run.Check(); err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Check = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	run := Run{Environment: member, Parameters: map[string]string{"region": "eu", "pr": "123"}}
	if err := run.Check(); err != nil {
		t.Fatal(err)
	}
	if got := run.Name(); got != "Preview.pr-123.region-eu" {
		t.Errorf("Name = %s", got)
	}
	if got := (Run{Environment: plain}).Name(); got != "Staging" {
		t.Errorf("Name = %s", got)
	}
}
