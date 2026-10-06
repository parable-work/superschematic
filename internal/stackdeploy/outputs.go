package stackdeploy

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

// RunOutputs is an outputs file (docs/stack-model.md, section 6.6): the
// outputs one run of an environment exported, as the provisioner's Outputs
// reads them. Outputs returns one and `stack outputs` writes it; scripts
// and CI read the file, and the bindings generator reads it into the Go
// binding.
type RunOutputs struct {
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
func NewOutputs(env *ir.ResolvedEnvironment, parameters map[string]string, resources map[string]map[string]any) *RunOutputs {
	if resources == nil {
		resources = map[string]map[string]any{}
	}
	return &RunOutputs{
		Version:     OutputsVersion,
		Stack:       env.Stack,
		Environment: env.Environment,
		Parameters:  parameters,
		Resources:   resources,
	}
}

// Marshal encodes an outputs file: indented, with sorted keys, so a change
// reads as a diff.
func (o *RunOutputs) Marshal() ([]byte, error) {
	data, err := json.MarshalIndent(o, "", "  ")
	if err != nil {
		return nil, err
	}
	return append(data, '\n'), nil
}

// UnmarshalOutputs decodes an outputs file and checks its version.
func UnmarshalOutputs(data []byte) (*RunOutputs, error) {
	var o RunOutputs
	if err := json.Unmarshal(data, &o); err != nil {
		return nil, fmt.Errorf("outputs file: %w", err)
	}
	if o.Version != OutputsVersion {
		return nil, fmt.Errorf("outputs file has version %d; this build reads version %d", o.Version, OutputsVersion)
	}
	if o.Resources == nil {
		o.Resources = map[string]map[string]any{}
	}
	return &o, nil
}
