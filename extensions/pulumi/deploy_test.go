package pulumi_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/parable-work/superschematic/extensions/pulumi"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
)

// The end-to-end deploy: the core's deploy (stack.Deploy) drives the real
// provisioner over a target of the test's own, whose graph is random
// provider nodes, against a file:// backend. Every seam the target carries
// keeps its state on disk or in memory, so nothing needs a cloud
// credential.

// e2eTarget is the target's name.
const e2eTarget = "random"

// e2eExtension registers the target and the real provisioner.
type e2eExtension struct {
	state      *dirState
	secrets    *memorySecrets
	migrations *recordingRunner
	progress   *syncBuffer
}

func (*e2eExtension) Name() string { return "random-e2e" }

func (e *e2eExtension) Register(r *registry.Registry) error {
	p := pulumi.Extension{
		ProviderVersions: map[string]string{"random": randomVersion},
		Env:              map[string]string{"PULUMI_CONFIG_PASSPHRASE": "superschematic-test"},
		Progress:         e.progress,
	}
	if err := p.Register(r); err != nil {
		return err
	}
	return r.RegisterTarget(registry.TargetSpec{
		Name: e2eTarget, Extension: "random-e2e", Provisioner: pulumi.Name,
		State: e.state, Secrets: e.secrets, Migrations: e.migrations,
	})
}

// dirState keeps the provisioner's state in a file:// backend under dir,
// and each run's manifest beside it.
type dirState struct{ dir string }

// Backend is a directory, as a bucket bootstrap created.
func (s *dirState) Backend(context.Context, *ir.ResolvedEnvironment) (registry.StateBackend, error) {
	dir := filepath.Join(s.dir, "pulumi")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return registry.StateBackend{}, err
	}
	return registry.StateBackend{URL: "file://" + dir, SecretsProvider: pulumi.PassphraseProvider}, nil
}

func (s *dirState) path(run registry.Run) string {
	return filepath.Join(s.dir, "manifests", run.Environment.Stack, run.Name()+".json")
}

func (s *dirState) ReadManifest(_ context.Context, run registry.Run) ([]byte, error) {
	data, err := os.ReadFile(s.path(run))
	if errors.Is(err, os.ErrNotExist) {
		return nil, fmt.Errorf("%s: %w", run.Name(), registry.ErrNoManifest)
	}
	return data, err
}

func (s *dirState) WriteManifest(_ context.Context, run registry.Run, data []byte) error {
	if err := os.MkdirAll(filepath.Dir(s.path(run)), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path(run), data, 0o644)
}

func (s *dirState) DeleteManifest(_ context.Context, run registry.Run) error {
	if err := os.Remove(s.path(run)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	return nil
}

type memorySecrets struct {
	mu     sync.Mutex
	values map[string][]byte
}

func (s *memorySecrets) Exists(_ context.Context, _ *ir.ResolvedEnvironment, id string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	_, ok := s.values[id]
	return ok, nil
}

func (s *memorySecrets) List(context.Context, *ir.ResolvedEnvironment) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var ids []string
	for id := range s.values {
		ids = append(ids, id)
	}
	slices.Sort(ids)
	return ids, nil
}

func (s *memorySecrets) Set(_ context.Context, _ *ir.ResolvedEnvironment, id string, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.values == nil {
		s.values = map[string][]byte{}
	}
	s.values[id] = value
	return nil
}

func (s *memorySecrets) Get(_ context.Context, _ *ir.ResolvedEnvironment, id string) ([]byte, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	value, ok := s.values[id]
	if !ok {
		return nil, fmt.Errorf("%s has no value", id)
	}
	return value, nil
}

// recordingRunner runs no migration: it records each phase, and the
// provisioner's progress gets a marker, so the test reads the order.
type recordingRunner struct {
	mu    sync.Mutex
	calls []string
	out   *syncBuffer
}

func (r *recordingRunner) Migrate(_ context.Context, req registry.MigrationRequest) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	call := fmt.Sprintf("migrate %s %s", req.Phase, req.Database)
	r.calls = append(r.calls, call)
	_, _ = fmt.Fprintf(r.out, "\n=== %s\n", call)
	return nil
}

// demoEnvironment is a stack whose database is a pet and whose server is
// a random string: its keepers hold the server's image, so a new image
// replaces it, as a new image rolls out a new revision. A greeting pet
// owned by the server is exposure.
func demoEnvironment() *ir.ResolvedEnvironment {
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       "Demo",
		Environment: "Live",
		Target:      e2eTarget,
		Provisioner: pulumi.Name,
		Deployables: []*ir.ResolvedDeployable{
			{
				Name: "api", Kind: ir.DeployableServer, Platform: "random.string", Language: "GO",
				Services:     []ir.ServiceRef{{Name: "api", Kind: "API"}},
				ResourceName: "api",
				Address:      ir.Output{Resource: "api.revision", Name: "result"},
				Bindings:     []*ir.Binding{{Field: "TOKEN", Source: ir.BindingSecret, Secret: "DemoSecrets.TOKEN"}},
			},
			{
				Name: "db", Kind: ir.DeployableDatabase, Platform: "random.pet", Dialect: "postgres",
				Services:     []ir.ServiceRef{{Name: "db", Kind: "DB"}},
				ResourceName: "db",
				Address:      ir.Output{Resource: "db.instance", Name: "id"},
			},
		},
		Secrets: []*ir.StackSecret{{ID: "DemoSecrets.TOKEN", Type: "DemoSecrets", Field: "TOKEN", Readers: []string{"api"}}},
		Resources: &ir.ResourceGraph{Resources: []*ir.Resource{
			{
				ID:   "api.greeting",
				Type: typePet,
				Properties: map[string]any{
					"prefix": ir.Output{Resource: "api.revision", Name: "result"},
				},
				DependsOn: []string{"api.revision"},
				Phase:     ir.PhaseExposure,
				Owners:    []string{"api"},
			},
			{
				ID:   "api.revision",
				Type: typeString,
				Properties: map[string]any{
					"length": float64(8), "special": false, "upper": false,
					"keepers": map[string]any{
						"image":    "registry.test/demo/api",
						"database": ir.Output{Resource: "db.instance", Name: "id"},
					},
				},
				DependsOn: []string{"db.instance"},
				Phase:     ir.PhaseRollout,
				Owners:    []string{"api"},
			},
			{
				ID:         "db.instance",
				Type:       typePet,
				Properties: map[string]any{"length": float64(2), "prefix": "db"},
				Phase:      ir.PhaseInfrastructure,
				Owners:     []string{"db"},
			},
		}},
		DeployOrder: []*ir.DeployStep{
			{Step: ir.StepInfrastructure, Resources: []string{"db.instance"}},
			{Step: ir.StepMigrate, Migration: ir.MigrationExpand, Deployables: []string{"db"}},
			{Step: ir.StepRollout, Wave: 1, Deployables: []string{"api"}, Resources: []string{"api.revision"}},
			{Step: ir.StepMigrate, Migration: ir.MigrationContract, Deployables: []string{"db"}},
			{Step: ir.StepExposure, Resources: []string{"api.greeting"}},
		},
	}
}

// demoPlanner plans db as a plan with a step in each phase, to model
// version n.
func demoPlanner(n int) stack.Planner {
	return func(service, dialect string, from json.RawMessage) (*stack.DatabasePlan, error) {
		return &stack.DatabasePlan{
			Service: service, Dialect: dialect,
			To: fmt.Sprintf("v%d", n), ToModel: json.RawMessage(fmt.Sprintf(`{"v":%d}`, n)),
			Expanded: fmt.Sprintf("v%d-expanded", n), ExpandedModel: json.RawMessage(fmt.Sprintf(`{"v":%d,"expanded":true}`, n)),
			ExpandSteps: 1, ContractSteps: 1,
			Hash: fmt.Sprintf("plan-%d", n), Document: json.RawMessage(fmt.Sprintf(`{"plan":%d}`, n)),
		}, nil
	}
}

func imageDigest(n int) string { return "sha256:" + strings.Repeat(fmt.Sprintf("%x", n), 64) }

// TestDeployRandom deploys the demo stack twice through the real
// provisioner, a step at a time with the migrations between, then plans,
// reads its outputs and destroys it.
func TestDeployRandom(t *testing.T) {
	requirePulumi(t)
	ctx := context.Background()
	progress := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("pulumi progress:\n%s", progress.buf.String())
		}
	})
	ext := &e2eExtension{
		state:      &dirState{dir: t.TempDir()},
		secrets:    &memorySecrets{},
		migrations: &recordingRunner{out: progress},
		progress:   progress,
	}
	reg, err := registry.Assemble(registry.DefaultNaming(), ext)
	if err != nil {
		t.Fatal(err)
	}
	env := demoEnvironment()
	if err := ext.secrets.Set(ctx, env, "DemoSecrets.TOKEN", []byte("token")); err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	options := stack.Options{Registry: reg, Run: registry.Run{Environment: env}, Dir: dir, Log: progress}
	deploy := func(n int) *stack.Manifest {
		t.Helper()
		m, err := stack.Deploy(ctx, stack.DeployOptions{
			Options: options,
			Images:  map[string]string{"api": "registry.test/demo/api@" + imageDigest(n)},
			Planner: demoPlanner(n),
		})
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	m := deploy(1)
	if m.Status != stack.StatusDeployed || m.Images["api"] != "registry.test/demo/api@"+imageDigest(1) {
		t.Fatalf("manifest: %s, images %v", m.Status, m.Images)
	}
	if got := m.Databases["db"]["db"].Hash; got != "v1" {
		t.Errorf("db holds %s", got)
	}
	if !slices.Equal(ext.migrations.calls, []string{"migrate expand db", "migrate contract db"}) {
		t.Errorf("migrations %v", ext.migrations.calls)
	}
	// The expand phase ran after the infrastructure's update and before
	// the rollout's; the contract phase after the rollout and before
	// exposure.
	log := progress.buf.String()
	order := []string{"db.instance", "=== migrate expand db", "api.revision", "=== migrate contract db", "api.greeting"}
	at := 0
	for _, mark := range order {
		i := strings.Index(log[at:], mark)
		if i < 0 {
			t.Fatalf("the progress has no %q after offset %d, in order %v", mark, at, order)
		}
		at += i + len(mark)
	}

	out, err := stack.Outputs(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	first, _ := out["api.revision"]["result"].(string)
	if len(first) != 8 {
		t.Fatalf("api.revision.result is %q", first)
	}

	// A new image replaces the server's revision.
	plan, err := stack.Plan(ctx, stack.PlanOptions{
		Options: options,
		Images:  map[string]string{"api": "registry.test/demo/api@" + imageDigest(2)},
		Planner: demoPlanner(2),
	})
	if err != nil {
		t.Fatal(err)
	}
	var changes []string
	for _, c := range plan.Changes {
		changes = append(changes, c.Resource+":"+c.Action)
	}
	if !slices.Contains(changes, "api.revision:replace") || len(plan.Databases) != 1 || plan.Databases[0].Hash != "plan-2" {
		t.Errorf("plan: changes %v, databases %+v", changes, plan.Databases)
	}
	deploy(2)
	out, err = stack.Outputs(ctx, options)
	if err != nil {
		t.Fatal(err)
	}
	if second, _ := out["api.revision"]["result"].(string); second == first || len(second) != 8 {
		t.Errorf("api.revision.result went from %q to %q", first, second)
	}

	// The program the deploy rendered names the image by digest.
	program, err := os.ReadFile(filepath.Join(dir, pulumi.ProgramFile))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(program, []byte("registry.test/demo/api@"+imageDigest(2))) {
		t.Errorf("the program does not pin the image:\n%s", program)
	}

	if err := stack.Destroy(ctx, options); err != nil {
		t.Fatal(err)
	}
	if _, err := ext.state.ReadManifest(ctx, options.Run); !errors.Is(err, registry.ErrNoManifest) {
		t.Errorf("destroy left the manifest: %v", err)
	}
}

// TestRequestEnv checks that a run's credentials reach the CLI: the
// provisioner holds no passphrase, and the request's Env carries the
// state's, which no stack opens without.
func TestRequestEnv(t *testing.T) {
	requirePulumi(t)
	for _, name := range []string{"PULUMI_CONFIG_PASSPHRASE", "PULUMI_CONFIG_PASSPHRASE_FILE"} {
		t.Setenv(name, "")
		if err := os.Unsetenv(name); err != nil {
			t.Fatal(err)
		}
	}
	p := pulumi.Extension{ProviderVersions: map[string]string{"random": randomVersion}}.Provisioner()
	req := renderRequest(t, p, sharedEnvironment(), nil, passphraseBackend(t))
	ctx := context.Background()
	if _, err := p.Plan(ctx, req); err == nil {
		t.Fatal("a plan without a passphrase passed")
	}
	req.Env = map[string]string{"PULUMI_CONFIG_PASSPHRASE": "superschematic-test"}
	expectPlan(t, p, req, "shared.pet:create")
	for _, name := range []string{pulumi.ProgramFile, "Pulumi.shared.yaml"} {
		data, err := os.ReadFile(filepath.Join(req.Dir, name))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			t.Fatal(err)
		}
		if bytes.Contains(data, []byte("superschematic-test")) {
			t.Errorf("%s holds the credential", name)
		}
	}
}
