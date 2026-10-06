package pulumi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/events"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optdestroy"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optpreview"
	"github.com/pulumi/pulumi/sdk/v3/go/auto/optup"
	"github.com/pulumi/pulumi/sdk/v3/go/common/apitype"
	"github.com/pulumi/pulumi/sdk/v3/go/common/resource/plugin"
	"github.com/pulumi/pulumi/sdk/v3/go/common/workspace"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// ErrNoCLI is the error, wrapped, of a run when the pulumi CLI is not on
// PATH. Render needs no CLI.
var ErrNoCLI = errors.New("the pulumi provisioner runs the pulumi CLI, which is not on PATH; " +
	"install it from https://www.pulumi.com/docs/iac/download-install/")

// PassphraseProvider is the secrets provider that encrypts with the
// passphrase in PULUMI_CONFIG_PASSPHRASE. Tests use it with a file://
// backend; an environment the gcp target applies uses a gcpkms:// key.
const PassphraseProvider = "passphrase"

// Plan previews the whole program and returns the change of every node it
// would create, update, replace or delete, sorted by node ID.
func (p *Provisioner) Plan(ctx context.Context, req registry.ProvisionRequest) ([]registry.PlannedChange, error) {
	r, err := p.open(ctx, req, true)
	if err != nil {
		return nil, err
	}
	if err := r.checkParent(ctx); err != nil {
		return nil, err
	}
	ch := make(chan events.EngineEvent)
	collected := make(chan map[string]string)
	go func() {
		changes := map[string]string{}
		for e := range ch {
			if e.ResourcePreEvent == nil {
				continue
			}
			md := e.ResourcePreEvent.Metadata
			action, ok := plannedAction(md.Op)
			if !ok || strings.HasPrefix(md.Type, "pulumi:") {
				continue
			}
			changes[urnName(md.URN)] = action
		}
		collected <- changes
	}()
	opts := []optpreview.Option{optpreview.EventStreams(ch), optpreview.Color("never")}
	if p.Progress != nil {
		opts = append(opts, optpreview.ProgressStreams(p.Progress))
	}
	// A preview that ran has closed ch when it returns. One that failed
	// before it tailed the CLI's events never closes it, so only a
	// successful one waits for the collector.
	if _, err := r.stack.Preview(ctx, opts...); err != nil {
		return nil, fmt.Errorf("pulumi: preview %s: %w", r.name, err)
	}
	changes := <-collected
	out := make([]registry.PlannedChange, 0, len(changes))
	for id, action := range changes {
		out = append(out, registry.PlannedChange{Resource: id, Action: action})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Resource < out[j].Resource })
	return out, nil
}

// plannedAction maps an engine step to a PlannedChange action. A replace
// is one change, whichever of its steps reports it; a step that changes
// nothing reports none.
func plannedAction(op apitype.OpType) (string, bool) {
	switch op {
	case apitype.OpCreate, apitype.OpImport:
		return "create", true
	case apitype.OpUpdate:
		return "update", true
	case apitype.OpReplace, apitype.OpCreateReplacement, apitype.OpDeleteReplaced, apitype.OpImportReplacement:
		return "replace", true
	case apitype.OpDelete:
		return "delete", true
	}
	return "", false
}

// urnName returns the name part of a URN: a node's ID.
func urnName(urn string) string {
	if i := strings.LastIndex(urn, "::"); i >= 0 {
		return urn[i+2:]
	}
	return urn
}

// Apply runs `up` over the nodes of one step of the deploy order, and
// leaves every other node as it is. The last step that holds nodes runs
// `up` over the whole program, which also deletes the nodes the graph no
// longer has. A node the graph no longer has goes earlier only when it
// depends on a node of the step, since an update of a node deletes the
// dependents the program dropped. A step without nodes, such as a migrate
// step, runs nothing.
func (p *Provisioner) Apply(ctx context.Context, req registry.ProvisionRequest, step ir.DeployStep) error {
	if len(step.Resources) == 0 {
		return nil
	}
	r, err := p.open(ctx, req, true)
	if err != nil {
		return err
	}
	if err := r.checkParent(ctx); err != nil {
		return err
	}
	opts := []optup.Option{optup.Color("never")}
	if p.Progress != nil {
		opts = append(opts, optup.ProgressStreams(p.Progress))
	}
	if !isLastStep(req.Environment, step) {
		var targets []string
		for _, id := range step.Resources {
			if r.prog.inherited[id] {
				return fmt.Errorf("pulumi: step %s applies node %s, which environment %s inherits", stepName(step), id, req.Environment.Environment)
			}
			if _, ok := r.prog.keys[id]; !ok {
				return fmt.Errorf("pulumi: step %s applies node %s, which the graph lacks", stepName(step), id)
			}
			// The type part is a wildcard, so the target matches however
			// the engine records the node's type token.
			targets = append(targets, fmt.Sprintf("urn:pulumi:%s::%s::**::%s", r.name, r.prog.project, id))
		}
		orphans, err := r.dependentOrphans(ctx, step.Resources)
		if err != nil {
			return err
		}
		opts = append(opts, optup.Target(append(targets, orphans...)))
	}
	if _, err := r.stack.Up(ctx, opts...); err != nil {
		return fmt.Errorf("pulumi: up %s, step %s: %w", r.name, stepName(step), err)
	}
	return nil
}

// dependentOrphans returns the URNs of the resources the stack holds that
// the graph no longer has and that depend, directly or through other
// resources, on one of the nodes ids. An update targeted at the nodes
// deletes such a resource, and the CLI refuses to unless it is a target
// too.
func (r *run) dependentOrphans(ctx context.Context, ids []string) ([]string, error) {
	exported, err := r.stack.Export(ctx)
	if err != nil {
		return nil, fmt.Errorf("pulumi: read the state of %s: %w", r.name, err)
	}
	var state struct {
		Resources []struct {
			URN                  string              `json:"urn"`
			Type                 string              `json:"type"`
			Parent               string              `json:"parent"`
			DeletedWith          string              `json:"deletedWith"`
			Dependencies         []string            `json:"dependencies"`
			PropertyDependencies map[string][]string `json:"propertyDependencies"`
		} `json:"resources"`
	}
	if len(exported.Deployment) > 0 {
		if err := json.Unmarshal(exported.Deployment, &state); err != nil {
			return nil, fmt.Errorf("pulumi: read the state of %s: %w", r.name, err)
		}
	}
	dependents := map[string][]string{}
	var queue []string
	for _, res := range state.Resources {
		deps := append([]string{res.Parent, res.DeletedWith}, res.Dependencies...)
		for _, more := range res.PropertyDependencies {
			deps = append(deps, more...)
		}
		for _, dep := range deps {
			if dep != "" {
				dependents[dep] = append(dependents[dep], res.URN)
			}
		}
		if !strings.HasPrefix(res.Type, "pulumi:") && slices.Contains(ids, urnName(res.URN)) {
			queue = append(queue, res.URN)
		}
	}
	types := map[string]string{}
	for _, res := range state.Resources {
		types[res.URN] = res.Type
	}
	seen := map[string]bool{}
	var orphans []string
	for len(queue) > 0 {
		urn := queue[0]
		queue = queue[1:]
		for _, dependent := range dependents[urn] {
			if seen[dependent] {
				continue
			}
			seen[dependent] = true
			queue = append(queue, dependent)
			if _, inGraph := r.prog.keys[urnName(dependent)]; !inGraph && !strings.HasPrefix(types[dependent], "pulumi:") {
				orphans = append(orphans, dependent)
			}
		}
	}
	sort.Strings(orphans)
	return orphans, nil
}

// isLastStep reports whether step is the last step of env's deploy order
// that holds nodes.
func isLastStep(env *ir.ResolvedEnvironment, step ir.DeployStep) bool {
	for i := len(env.DeployOrder) - 1; i >= 0; i-- {
		last := env.DeployOrder[i]
		if len(last.Resources) == 0 {
			continue
		}
		return last.Step == step.Step && last.Wave == step.Wave && last.Migration == step.Migration &&
			slices.Equal(last.Resources, step.Resources)
	}
	return false
}

func stepName(step ir.DeployStep) string {
	if step.Wave > 0 {
		return fmt.Sprintf("%s %d", step.Step, step.Wave)
	}
	return string(step.Step)
}

// Destroy deletes every node of the environment's run and removes its
// stack, settings file included. A run that was never applied has no
// stack, and Destroy does nothing.
func (p *Provisioner) Destroy(ctx context.Context, req registry.ProvisionRequest) error {
	r, err := p.open(ctx, req, false)
	if errors.Is(err, errNoStack) {
		return nil
	}
	if err != nil {
		return err
	}
	opts := []optdestroy.Option{optdestroy.Color("never"), optdestroy.Remove()}
	if p.Progress != nil {
		opts = append(opts, optdestroy.ProgressStreams(p.Progress))
	}
	if _, err := r.stack.Destroy(ctx, opts...); err != nil {
		return fmt.Errorf("pulumi: destroy %s: %w", r.name, err)
	}
	return nil
}

// Outputs reads the outputs the program exports (Exports), by node ID and
// output name. A secret output is left out: neither the bindings nor the
// deploy manifest holds a secret. So is an output the stack lacks or holds
// as unknown, which a step not yet applied has not produced.
func (p *Provisioner) Outputs(ctx context.Context, req registry.ProvisionRequest) (map[string]map[string]any, error) {
	r, err := p.open(ctx, req, false)
	if errors.Is(err, errNoStack) {
		return nil, fmt.Errorf("pulumi: environment %s has no stack %s; apply it first", req.Environment.Environment, r.name)
	}
	if err != nil {
		return nil, err
	}
	values, err := r.stack.Outputs(ctx)
	if err != nil {
		return nil, fmt.Errorf("pulumi: outputs of %s: %w", r.name, err)
	}
	out := map[string]map[string]any{}
	for _, exp := range r.prog.exports {
		value, ok := values[exp.Key]
		if !ok || value.Secret || !known(value.Value) {
			continue
		}
		if out[exp.Resource] == nil {
			out[exp.Resource] = map[string]any{}
		}
		out[exp.Resource][exp.Name] = value.Value
	}
	return out, nil
}

// errNoStack is open's error for a run that has no stack yet.
var errNoStack = errors.New("no stack")

// run is one operation on one run's stack.
type run struct {
	ws    auto.Workspace
	stack auto.Stack
	name  string
	prog  *program
}

// open checks req, finds the CLI, opens a local workspace over the
// rendered program and selects the run's stack. With create it creates a
// stack that does not exist yet; without, it returns errNoStack, with
// r.name set.
func (p *Provisioner) open(ctx context.Context, req registry.ProvisionRequest, create bool) (*run, error) {
	r := &run{}
	env := req.Environment
	if env == nil {
		return r, fmt.Errorf("pulumi: the request has no environment")
	}
	var err error
	if r.name, err = StackName(env.Environment, env.Parameters, req.Parameters); err != nil {
		return r, fmt.Errorf("pulumi: %w", err)
	}
	if r.prog, err = newProgram(env, p.ProviderVersions); err != nil {
		return r, err
	}
	if err := p.checkProgram(env, req.Dir); err != nil {
		return r, err
	}
	if req.Backend.URL == "" {
		return r, fmt.Errorf("pulumi: environment %s has no state backend; the target's bootstrap creates one (gs://, or file:// in tests)", env.Environment)
	}
	if req.Backend.SecretsProvider == "" {
		return r, fmt.Errorf("pulumi: environment %s has no secrets provider; the target's bootstrap creates one (gcpkms://, or %s in tests)", env.Environment, PassphraseProvider)
	}
	cmd, err := p.command()
	if err != nil {
		return r, err
	}
	// The CLI runs with the provisioner's environment, then the run's
	// credentials, which the providers read (ProvisionRequest.Env). Both
	// reach the CLI's process only, never a file.
	vars := map[string]string{}
	for k, v := range p.Env {
		vars[k] = v
	}
	for k, v := range req.Env {
		vars[k] = v
	}
	vars["PULUMI_BACKEND_URL"] = req.Backend.URL
	vars["PULUMI_SKIP_UPDATE_CHECK"] = "true"
	r.ws, err = auto.NewLocalWorkspace(ctx,
		auto.WorkDir(req.Dir),
		auto.Pulumi(cmd),
		auto.EnvVars(vars),
		auto.SecretsProvider(req.Backend.SecretsProvider),
	)
	if err != nil {
		return r, fmt.Errorf("pulumi: workspace over %s: %w", req.Dir, err)
	}
	if create {
		r.stack, err = auto.UpsertStack(ctx, r.name, r.ws)
	} else {
		r.stack, err = auto.SelectStack(ctx, r.name, r.ws)
		if auto.IsSelectStack404Error(err) {
			return r, errNoStack
		}
	}
	if err != nil {
		return r, fmt.Errorf("pulumi: stack %s: %w", r.name, err)
	}
	if err := r.keepSecretsProvider(ctx, req.Backend.SecretsProvider); err != nil {
		return r, err
	}
	if len(env.Parameters) > 0 {
		config := auto.ConfigMap{}
		for _, param := range env.Parameters {
			config[param] = auto.ConfigValue{Value: req.Parameters[param]}
		}
		if err := r.stack.SetAllConfig(ctx, config); err != nil {
			return r, fmt.Errorf("pulumi: set the parameters of %s: %w", r.name, err)
		}
	}
	return r, nil
}

// command finds the pulumi CLI on PATH and checks its version, once per
// provisioner.
func (p *Provisioner) command() (auto.PulumiCommand, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.cmd != nil {
		return p.cmd, nil
	}
	if _, err := exec.LookPath("pulumi"); err != nil {
		return nil, ErrNoCLI
	}
	cmd, err := auto.NewPulumiCommand(nil)
	if err != nil {
		return nil, fmt.Errorf("pulumi: %w", err)
	}
	p.cmd = cmd
	return cmd, nil
}

// checkProgram refuses a directory whose Pulumi.yaml is not the one Render
// writes for env, so a run never applies a stale program.
func (p *Provisioner) checkProgram(env *ir.ResolvedEnvironment, dir string) error {
	if dir == "" {
		return fmt.Errorf("pulumi: the request names no program directory")
	}
	want, err := renderProgram(env, p.ProviderVersions)
	if err != nil {
		return err
	}
	got, err := os.ReadFile(filepath.Join(dir, ProgramFile))
	if errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("pulumi: %s has no %s; render environment %s first", dir, ProgramFile, env.Environment)
	}
	if err != nil {
		return err
	}
	if !bytes.Equal(got, want) {
		return fmt.Errorf("pulumi: %s is not the program environment %s renders; render it again", filepath.Join(dir, ProgramFile), env.Environment)
	}
	return nil
}

// keepSecretsProvider writes the request's secrets provider into the
// stack's settings file when the file lacks one. The CLI keeps the file
// beside the program, and a fresh checkout has none: without the
// provider, a stack in a file:// or gs:// backend would fall back to the
// passphrase provider. A settings file that names a provider is left as it
// is.
func (r *run) keepSecretsProvider(ctx context.Context, provider string) error {
	if provider == PassphraseProvider {
		return nil
	}
	settings := &workspace.ProjectStack{}
	path := filepath.Join(r.ws.WorkDir(), "Pulumi."+r.name+".yaml")
	if _, err := os.Stat(path); err == nil {
		if settings, err = r.ws.StackSettings(ctx, r.name); err != nil {
			return fmt.Errorf("pulumi: settings of %s: %w", r.name, err)
		}
	}
	if settings.SecretsProvider != "" {
		return nil
	}
	settings.SecretsProvider = provider
	if err := r.ws.SaveStackSettings(ctx, r.name, settings); err != nil {
		return fmt.Errorf("pulumi: settings of %s: %w", r.name, err)
	}
	return nil
}

// checkParent checks, for a member of a parameterized environment, that
// the parent environment's stack exports every output the member reads
// from it. A stack reference reads a missing output as null, which would
// reach a property unnoticed.
func (r *run) checkParent(ctx context.Context) error {
	if r.prog.parent == "" {
		return nil
	}
	env := r.prog.env
	outputs, err := r.ws.StackOutputs(ctx, r.prog.parent)
	if err != nil {
		return fmt.Errorf("pulumi: environment %s inherits nodes from environment %s, whose stack %s cannot be read; apply %s first: %w",
			env.Environment, env.Extends, r.prog.parent, env.Extends, err)
	}
	var missing []string
	for _, exp := range r.prog.exports {
		if !r.prog.inherited[exp.Resource] {
			continue
		}
		if value, ok := outputs[exp.Key]; !ok || !known(value.Value) {
			missing = append(missing, exp.Key)
		}
	}
	if len(missing) > 0 {
		return fmt.Errorf("pulumi: environment %s reads %s from the stack %s of environment %s, which does not export it; apply %s first",
			env.Environment, strings.Join(missing, ", "), r.prog.parent, env.Extends, env.Extends)
	}
	return nil
}

// known reports whether an output value is set and holds no unknown: a
// node a targeted update has not created yet exports the CLI's unknown
// placeholder.
func known(v any) bool {
	switch v := v.(type) {
	case nil:
		return false
	case string:
		return v != plugin.UnknownStringValue
	case map[string]any:
		for _, inner := range v {
			if inner != nil && !known(inner) {
				return false
			}
		}
	case []any:
		for _, inner := range v {
			if inner != nil && !known(inner) {
				return false
			}
		}
	}
	return true
}
