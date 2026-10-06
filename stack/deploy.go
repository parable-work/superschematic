package stack

import (
	"context"

	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// The cloud half of the `stack` commands (docs/stack-model.md, sections
// 7.3, 11.1 and 11.2), for an extension's tests and a command; see
// internal/stackdeploy.
type (
	// Options is what every operation reads; DeployOptions, PlanOptions,
	// BootstrapOptions and SecretsOptions add each one's own.
	Options          = stackdeploy.Options
	DeployOptions    = stackdeploy.DeployOptions
	PlanOptions      = stackdeploy.PlanOptions
	BootstrapOptions = stackdeploy.BootstrapOptions
	SecretsOptions   = stackdeploy.SecretsOptions

	// PlanResult is what a deploy would do; PendingNote names a migration
	// phase a failed deploy left part-way.
	PlanResult  = stackdeploy.PlanResult
	PendingNote = stackdeploy.PendingNote

	// Manifest is a run's deploy manifest, with each database's
	// AppliedSchema and any PendingMigration; Status is where its last
	// deploy got to.
	Manifest         = stackdeploy.Manifest
	AppliedSchema    = stackdeploy.AppliedSchema
	PendingMigration = stackdeploy.PendingMigration
	Status           = stackdeploy.Status

	// DatabasePlan is one DB service's migration plan, with its Hazards;
	// a Planner makes one; Gate holds the hazards a deploy refuses, and
	// HazardsError is its refusal.
	DatabasePlan = stackdeploy.DatabasePlan
	Hazard       = stackdeploy.Hazard
	Planner      = stackdeploy.Planner
	Gate         = stackdeploy.Gate
	HazardsError = stackdeploy.HazardsError

	// Prompter asks a person for a secret value.
	Prompter = stackdeploy.Prompter

	// RunOutputs is an outputs file: what one run exported, which the
	// bindings generator reads.
	RunOutputs = stackdeploy.RunOutputs
)

// The statuses a manifest records.
const (
	StatusDeploying = stackdeploy.StatusDeploying
	StatusDeployed  = stackdeploy.StatusDeployed
	StatusFailed    = stackdeploy.StatusFailed
)

// OutputsVersion is the version of the outputs file format, and
// OutputsFile its name beside environment.json; see internal/stackdeploy.
const (
	OutputsVersion = stackdeploy.OutputsVersion
	OutputsFile    = stackdeploy.OutputsFile
)

// Deploy deploys a run; see internal/stackdeploy.Deploy.
func Deploy(ctx context.Context, o DeployOptions) (*Manifest, error) {
	return stackdeploy.Deploy(ctx, o)
}

// Plan returns what a deploy of a run would do; see
// internal/stackdeploy.Plan.
func Plan(ctx context.Context, o PlanOptions) (*PlanResult, error) { return stackdeploy.Plan(ctx, o) }

// Destroy removes a run's resources and manifest; see
// internal/stackdeploy.Destroy.
func Destroy(ctx context.Context, o Options) error { return stackdeploy.Destroy(ctx, o) }

// Outputs returns a run's outputs file; see internal/stackdeploy.Outputs.
func Outputs(ctx context.Context, o Options) (*RunOutputs, error) { return stackdeploy.Outputs(ctx, o) }

// Bootstrap prepares the cloud project an environment deploys to; see
// internal/stackdeploy.Bootstrap.
func Bootstrap(ctx context.Context, o BootstrapOptions) error { return stackdeploy.Bootstrap(ctx, o) }

// SetSecrets asks for and stores secret values; see
// internal/stackdeploy.SetSecrets.
func SetSecrets(ctx context.Context, o SecretsOptions) ([]string, error) {
	return stackdeploy.SetSecrets(ctx, o)
}

// ParseImages reads `--image <server>=<image>` values; see
// internal/stackdeploy.ParseImages.
func ParseImages(values []string) (map[string]string, error) { return stackdeploy.ParseImages(values) }

// PinImages pins each server's image to a digest; see
// internal/stackdeploy.PinImages.
func PinImages(env *ir.ResolvedEnvironment, images map[string]string) (*ir.ResolvedEnvironment, error) {
	return stackdeploy.PinImages(env, images)
}

// UnmarshalManifest decodes a deploy manifest; see
// internal/stackdeploy.UnmarshalManifest.
func UnmarshalManifest(data []byte) (*Manifest, error) { return stackdeploy.UnmarshalManifest(data) }

// NewOutputs returns the outputs file of one run of env; see
// internal/stackdeploy.NewOutputs.
func NewOutputs(env *ir.ResolvedEnvironment, parameters map[string]string, resources map[string]map[string]any) *RunOutputs {
	return stackdeploy.NewOutputs(env, parameters, resources)
}

// UnmarshalOutputs decodes an outputs file; see
// internal/stackdeploy.UnmarshalOutputs.
func UnmarshalOutputs(data []byte) (*RunOutputs, error) { return stackdeploy.UnmarshalOutputs(data) }
