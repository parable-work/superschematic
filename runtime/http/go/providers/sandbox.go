package providers

import (
	"context"
	"time"
)

// The sandbox provider (D159): where an agent run's instance runs (D158).
// The dispatcher starts a fresh instance per run with the run's token, the
// MCP URL and the credential references, and cancels it when the run
// stops; only what the instance wrote through the API outlives it. A
// provider for a container platform, a VM service or a local process
// implements it.

// SandboxLimits are the limits a provider enforces on an instance.
type SandboxLimits struct {
	WallSeconds int `json:"wallSeconds,omitempty"`
	CPUSeconds  int `json:"cpuSeconds,omitempty"`
	MemoryMB    int `json:"memoryMb,omitempty"`
}

// SandboxSpec is what one run's instance gets.
type SandboxSpec struct {
	// RunID is the run the instance serves, which its records name.
	RunID string `json:"runId"`
	// Image is what the instance runs, for a container platform; Command a
	// program, for a local process.
	Image   string   `json:"image,omitempty"`
	Command []string `json:"command,omitempty"`
	// Env are plain settings: the MCP URL, the definition's version.
	Env map[string]string `json:"env,omitempty"`
	// Secrets are secrets the instance reads, the run token among them,
	// which the provider passes without logging.
	Secrets map[string]SecretValue `json:"secrets,omitempty"`
	Limits  *SandboxLimits         `json:"limits,omitempty"`
}

// SandboxInstance is a started instance.
type SandboxInstance struct {
	ID        string    `json:"id"`
	StartedAt time.Time `json:"startedAt"`
}

// SandboxState is where an instance stands.
type SandboxState string

// The sandbox states.
const (
	SandboxStarting  SandboxState = "starting"
	SandboxRunning   SandboxState = "running"
	SandboxSucceeded SandboxState = "succeeded"
	SandboxFailed    SandboxState = "failed"
	SandboxCancelled SandboxState = "cancelled"
)

// SandboxStatus is where an instance stands.
type SandboxStatus struct {
	ID       string       `json:"id"`
	State    SandboxState `json:"state"`
	ExitCode *int         `json:"exitCode,omitempty"`
	Detail   string       `json:"detail,omitempty"`
}

// Amount is an amount a provider charges, as a decimal string.
type Amount struct {
	Value    string `json:"value"`
	Currency string `json:"currency"`
}

// SandboxCost is what an instance cost so far, for the Budget behavior
// (D104).
type SandboxCost struct {
	ID          string  `json:"id"`
	WallSeconds float64 `json:"wallSeconds"`
	CPUSeconds  float64 `json:"cpuSeconds"`
	// Amount is what the platform charges, when it says.
	Amount *Amount `json:"amount,omitempty"`
}

// SandboxProvider is a sandbox provider.
type SandboxProvider interface {
	Start(ctx context.Context, spec SandboxSpec) (SandboxInstance, error)
	// Cancel stops the instance; cancelling one that has stopped does
	// nothing.
	Cancel(ctx context.Context, id string) error
	Status(ctx context.Context, id string) (SandboxStatus, error)
	Cost(ctx context.Context, id string) (SandboxCost, error)
}
