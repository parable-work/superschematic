package stacktest

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// ProgramFile is the file FakeProvisioner.Render writes.
const ProgramFile = "program.json"

// FakeProvisioner is a provisioner that runs nothing. It renders the
// graph as a list of nodes, plans a create per node, and records each
// call.
type FakeProvisioner struct {
	// Fail holds the error Apply returns for a step, keyed by the step's
	// name (`rollout 2`, `exposure`).
	Fail map[string]error

	mu       sync.Mutex
	calls    []string
	rendered *ir.ResolvedEnvironment
	env      map[string]string
}

var _ registry.Provisioner = (*FakeProvisioner)(nil)

// Calls returns the calls made so far, one line each.
func (p *FakeProvisioner) Calls() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return append([]string(nil), p.calls...)
}

// Record adds a call to the log. The target's other fakes record theirs
// here too.
func (p *FakeProvisioner) Record(format string, args ...any) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.calls = append(p.calls, fmt.Sprintf(format, args...))
}

func (p *FakeProvisioner) record(format string, args ...any) { p.Record(format, args...) }

// Rendered returns the environment Render last rendered.
func (p *FakeProvisioner) Rendered() *ir.ResolvedEnvironment {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.rendered
}

// Env returns the credentials the last Plan or Apply was handed.
func (p *FakeProvisioner) Env() map[string]string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.env
}

func (p *FakeProvisioner) keepEnv(req registry.ProvisionRequest) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.env = req.Env
}

// Render writes program.json: one entry per node of the environment's
// graph, with its type and dependencies.
func (p *FakeProvisioner) Render(env *ir.ResolvedEnvironment, dir string) error {
	graph := env.Resources
	type node struct {
		ID        string   `json:"id"`
		Type      string   `json:"type"`
		DependsOn []string `json:"dependsOn,omitempty"`
	}
	program := struct {
		Parameters []string `json:"parameters,omitempty"`
		Nodes      []node   `json:"nodes"`
	}{Parameters: graph.Parameters}
	for _, res := range graph.Resources {
		program.Nodes = append(program.Nodes, node{ID: res.ID, Type: res.Type, DependsOn: res.DependsOn})
	}
	data, err := json.MarshalIndent(program, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	p.record("render %d nodes", len(program.Nodes))
	p.mu.Lock()
	p.rendered = env
	p.mu.Unlock()
	return os.WriteFile(filepath.Join(dir, ProgramFile), append(data, '\n'), 0o644)
}

// Plan plans a create for every node the environment does not inherit.
func (p *FakeProvisioner) Plan(_ context.Context, req registry.ProvisionRequest) ([]registry.PlannedChange, error) {
	if err := checkParameters(req); err != nil {
		return nil, err
	}
	p.keepEnv(req)
	var changes []registry.PlannedChange
	for _, res := range req.Environment.Resources.Resources {
		if !res.Inherited {
			changes = append(changes, registry.PlannedChange{Resource: res.ID, Action: "create"})
		}
	}
	p.record("plan %s: %d changes", req.Environment.Environment, len(changes))
	return changes, nil
}

// Apply records the step it applies.
func (p *FakeProvisioner) Apply(_ context.Context, req registry.ProvisionRequest, step ir.DeployStep) error {
	if err := checkParameters(req); err != nil {
		return err
	}
	name := string(step.Step)
	if step.Wave > 0 {
		name = fmt.Sprintf("%s %d", name, step.Wave)
	}
	p.keepEnv(req)
	p.record("apply %s: %s", name, strings.Join(step.Resources, ", "))
	return p.Fail[name]
}

// Destroy records the call.
func (p *FakeProvisioner) Destroy(_ context.Context, req registry.ProvisionRequest) error {
	p.record("destroy %s", req.Environment.Environment)
	return nil
}

// Outputs returns each node's ID as its `id` output.
func (p *FakeProvisioner) Outputs(_ context.Context, req registry.ProvisionRequest) (map[string]map[string]any, error) {
	out := map[string]map[string]any{}
	for _, res := range req.Environment.Resources.Resources {
		out[res.ID] = map[string]any{"id": res.ID}
	}
	p.record("outputs %s", req.Environment.Environment)
	return out, nil
}

// checkParameters refuses a run that does not supply exactly the
// environment's parameters.
func checkParameters(req registry.ProvisionRequest) error {
	if len(req.Parameters) != len(req.Environment.Parameters) {
		return fmt.Errorf("environment %s takes parameters %v", req.Environment.Environment, req.Environment.Parameters)
	}
	for _, param := range req.Environment.Parameters {
		if _, ok := req.Parameters[param]; !ok {
			return fmt.Errorf("environment %s needs a value for parameter %s", req.Environment.Environment, param)
		}
	}
	return nil
}
