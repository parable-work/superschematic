// Package pulumi is the stack model's first provisioner
// (docs/stack-model.md, section 6.5). It renders an environment's resource
// graph as a Pulumi YAML program and drives the pulumi CLI over it through
// the Automation API: Plan previews, Apply runs `up` for one step of the
// deploy order, Destroy removes the environment's stack, and Outputs reads
// the outputs the program exports. The bindings package writes a typed Go
// package over those outputs (section 6.6).
//
// It is a Go module of its own (section 13), so the Pulumi SDK stays out of
// the core.
package pulumi

import (
	"io"
	"sync"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"

	"github.com/parable-work/superschematic/registry"
)

// Name is the extension's name and the provisioner it registers. A target
// names it in TargetSpec.Provisioner.
const Name = "pulumi"

// CLIVersion is the release of the pulumi CLI the provisioner drives: the
// release of the Pulumi Go SDK this module requires, which the Automation
// API was released with. The provisioner declares it as its tool, so a
// generated CI job installs that release (D47), and tools.env pins CI's at
// it (PULUMI_VERSION).
const CLIVersion = "3.259.0"

// Extension registers the pulumi provisioner. A distribution links it
// beside the targets whose environments it applies, and passes each
// target's pinned provider versions:
//
//	pulumi.Extension{ProviderVersions: map[string]string{"gcp": gcp.ProviderVersion}}
type Extension struct {
	// ProviderVersions pins the plugin version of each provider package,
	// keyed by the package that begins a resource type token ("gcp" for
	// `gcp:cloudrunv2/service:Service`). Render writes it as the
	// `version` option of every node of that package, so the provider that
	// applies a node is the one whose schema the target checked it against
	// (section 6.4). A package it does not name takes the newest installed
	// plugin.
	ProviderVersions map[string]string

	// Env is added to the environment the pulumi CLI runs in, over the
	// process's own: PULUMI_CONFIG_PASSPHRASE for the passphrase secrets
	// provider in tests, or credentials.
	Env map[string]string

	// Progress, when set, receives the CLI's progress output.
	Progress io.Writer
}

// Name is the extension's name.
func (Extension) Name() string { return Name }

// Register adds the pulumi provisioner, which runs the pulumi CLI at
// CLIVersion.
func (e Extension) Register(r *registry.Registry) error {
	return r.RegisterProvisioner(registry.ProvisionerSpec{
		Name:        Name,
		Extension:   Name,
		Provisioner: e.Provisioner(),
		Tools:       []registry.CLITool{{Name: "pulumi", Version: CLIVersion}},
	})
}

// Provisioner returns the provisioner Register adds.
func (e Extension) Provisioner() *Provisioner {
	return &Provisioner{ProviderVersions: e.ProviderVersions, Env: e.Env, Progress: e.Progress}
}

// Provisioner renders resource graphs as Pulumi YAML programs and applies
// them with the pulumi CLI. Its fields are Extension's.
type Provisioner struct {
	ProviderVersions map[string]string
	Env              map[string]string
	Progress         io.Writer

	mu  sync.Mutex
	cmd auto.PulumiCommand
}

var _ registry.Provisioner = (*Provisioner)(nil)
