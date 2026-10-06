// Package bindings writes the typed binding over a stack's outputs
// (docs/stack-model.md, section 6.6): a Go package with one value per
// applied environment and one field per deployable, for scripts and code
// outside the stack, and a second package that reads the same shape in a
// hand-written Pulumi program, over a stack reference to each environment's
// stack. The generator reads each environment's environment.json and the
// outputs file its last apply left.
package bindings

import (
	"encoding/json"
	"fmt"

	ir "github.com/parable-work/superschematic/ir"
)

// OutputsVersion is the version of the outputs file format.
const OutputsVersion = 1

// OutputsFile is the outputs file's name. Beside an environment's
// environment.json, it holds what the environment's last apply exported.
const OutputsFile = "outputs.json"

// Outputs is an outputs file: the outputs one run of an environment
// exported, as the provisioner's Outputs reads them. Scripts and CI read
// the file; Go reads the binding generated from it.
type Outputs struct {
	// Version is OutputsVersion.
	Version int `json:"version"`

	// Stack and Environment name the environment the run applied.
	Stack       string `json:"stack"`
	Environment string `json:"environment"`

	// Parameters are the run's parameter values, for a member of a
	// parameterized environment.
	Parameters map[string]string `json:"parameters,omitempty"`

	// Resources are the outputs by node ID and output name. A secret
	// output is never written.
	Resources map[string]map[string]any `json:"resources"`
}

// NewOutputs returns the outputs file of one run of env.
func NewOutputs(env *ir.ResolvedEnvironment, parameters map[string]string, resources map[string]map[string]any) *Outputs {
	if resources == nil {
		resources = map[string]map[string]any{}
	}
	return &Outputs{
		Version:     OutputsVersion,
		Stack:       env.Stack,
		Environment: env.Environment,
		Parameters:  parameters,
		Resources:   resources,
	}
}

// Marshal encodes an outputs file: indented, with sorted keys, so a change
// reads as a diff.
func (o *Outputs) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// UnmarshalOutputs decodes an outputs file.
func UnmarshalOutputs(data []byte) (*Outputs, error) {
	var o Outputs
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("outputs file: %w", err)
	}
	if o.Version != OutputsVersion {
		return nil, fmt.Errorf("outputs file has version %d; this generator reads version %d", o.Version, OutputsVersion)
	}
	if o.Resources == nil {
		o.Resources = map[string]map[string]any{}
	}
	return &o, nil
}
