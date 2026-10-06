// Package bindings writes the typed binding over a stack's outputs
// (docs/stack-model.md, section 6.6): a Go package with one value per
// applied environment and one field per deployable, for scripts and code
// outside the stack, and a second package that reads the same shape in a
// hand-written Pulumi program, over a stack reference to each environment's
// stack. The generator reads each environment's environment.json and the
// outputs file its last apply left, which `superschematic stack outputs
// --out` writes.
package bindings

import (
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack"
)

// The outputs file is the core's (stack.RunOutputs), which `stack outputs`
// writes; the names here forward to it.

// OutputsVersion is the version of the outputs file format.
const OutputsVersion = stack.OutputsVersion

// OutputsFile is the outputs file's name. Beside an environment's
// environment.json, it holds what the environment's last apply exported.
const OutputsFile = stack.OutputsFile

// Outputs is an outputs file: the outputs one run of an environment
// exported, as the provisioner's Outputs reads them. Scripts and CI read
// the file; Go reads the binding generated from it.
type Outputs = stack.RunOutputs

// NewOutputs returns the outputs file of one run of env.
func NewOutputs(env *ir.ResolvedEnvironment, parameters map[string]string, resources map[string]map[string]any) *Outputs {
	return stack.NewOutputs(env, parameters, resources)
}

// UnmarshalOutputs decodes an outputs file, such as the one `stack outputs
// --out` writes, and checks its version.
func UnmarshalOutputs(data []byte) (*Outputs, error) { return stack.UnmarshalOutputs(data) }
