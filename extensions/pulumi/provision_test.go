package pulumi_test

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"

	"github.com/parable-work/superschematic/extensions/pulumi"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

// requirePulumi skips a test that runs the pulumi CLI when the CLI is not
// on PATH. Under SUPERSCHEMATIC_REQUIRE_PULUMI=1, as CI sets it, a missing
// CLI fails the test instead: there a skip is a broken gate.
func requirePulumi(t *testing.T) {
	t.Helper()
	if _, err := exec.LookPath("pulumi"); err != nil {
		if os.Getenv("SUPERSCHEMATIC_REQUIRE_PULUMI") == "1" {
			t.Fatal("SUPERSCHEMATIC_REQUIRE_PULUMI=1, but the pulumi CLI is not on PATH")
		}
		t.Skip("the pulumi CLI is not on PATH")
	}
}

// passphraseBackend is a file:// backend in a temporary directory, with
// the passphrase secrets provider: state that needs no cloud credentials.
func passphraseBackend(t *testing.T) registry.StateBackend {
	return registry.StateBackend{URL: "file://" + t.TempDir(), SecretsProvider: pulumi.PassphraseProvider}
}

// renderRequest renders env into a new directory and returns a request
// over it.
func renderRequest(t *testing.T, p *pulumi.Provisioner, env *ir.ResolvedEnvironment, params map[string]string, backend registry.StateBackend) registry.ProvisionRequest {
	t.Helper()
	req := registry.ProvisionRequest{Environment: env, Parameters: params, Dir: t.TempDir(), Backend: backend}
	if err := p.Render(env, req.Dir); err != nil {
		t.Fatal(err)
	}
	return req
}

// TestRequestChecks covers the refusals a run makes before it starts the
// CLI, so they need none.
func TestRequestChecks(t *testing.T) {
	p := new(pulumi.Provisioner)
	backend := registry.StateBackend{URL: "file:///nowhere", SecretsProvider: pulumi.PassphraseProvider}
	member := previewEnvironment(8, false)
	stale := renderRequest(t, p, previewEnvironment(8, false), map[string]string{"pr": "1"}, backend)
	stale.Environment = previewEnvironment(9, false)
	cases := []struct {
		name string
		req  registry.ProvisionRequest
		want string
	}{
		{"no environment", registry.ProvisionRequest{Dir: t.TempDir(), Backend: backend}, "no environment"},
		{"no program", registry.ProvisionRequest{Environment: sharedEnvironment(), Dir: t.TempDir(), Backend: backend}, "render environment Shared first"},
		{"stale program", stale, "render it again"},
		{"missing parameter", renderRequest(t, p, member, nil, backend), "takes parameters [pr]"},
		{"bad parameter value", renderRequest(t, p, member, map[string]string{"pr": "a b"}, backend), "a value is letters"},
		{"no backend", renderRequest(t, p, sharedEnvironment(), nil, registry.StateBackend{SecretsProvider: "passphrase"}), "no state backend"},
		{"no secrets provider", renderRequest(t, p, sharedEnvironment(), nil, registry.StateBackend{URL: "file:///nowhere"}), "no secrets provider"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := p.Plan(context.Background(), tc.req)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestNoCLI checks that every run fails with ErrNoCLI when the pulumi CLI
// is not on PATH, and that Render needs no CLI.
func TestNoCLI(t *testing.T) {
	t.Setenv("PATH", t.TempDir())
	p := new(pulumi.Provisioner)
	env := sharedEnvironment()
	req := renderRequest(t, p, env, nil, passphraseBackend(t))
	ctx := context.Background()
	_, planErr := p.Plan(ctx, req)
	_, outErr := p.Outputs(ctx, req)
	for name, err := range map[string]error{
		"plan":    planErr,
		"apply":   p.Apply(ctx, req, *env.DeployOrder[0]),
		"destroy": p.Destroy(ctx, req),
		"outputs": outErr,
	} {
		if !errors.Is(err, pulumi.ErrNoCLI) {
			t.Errorf("%s: got %v, want ErrNoCLI", name, err)
		}
	}
	if !strings.Contains(pulumi.ErrNoCLI.Error(), "not on PATH") {
		t.Errorf("ErrNoCLI says %q", pulumi.ErrNoCLI)
	}
}

// syncBuffer collects the CLI's progress for a failing test's log.
type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

// TestProvisionRandom runs every operation against a file:// backend with
// the random provider, which needs no cloud credentials: a parent
// environment, then a member of a parameterized environment that inherits
// the parent's node, applied a step at a time, changed, read from a fresh
// directory and destroyed.
func TestProvisionRandom(t *testing.T) {
	requirePulumi(t)
	ctx := context.Background()
	progress := &syncBuffer{}
	t.Cleanup(func() {
		if t.Failed() {
			t.Logf("pulumi progress:\n%s", progress.buf.String())
		}
	})
	p := pulumi.Extension{
		ProviderVersions: map[string]string{"random": randomVersion},
		Env:              map[string]string{"PULUMI_CONFIG_PASSPHRASE": "superschematic-test"},
		Progress:         progress,
	}.Provisioner()
	backend := passphraseBackend(t)

	// A member whose parent has no stack yet fails before it runs.
	member := renderRequest(t, p, previewEnvironment(8, true), map[string]string{"pr": "7"}, backend)
	if _, err := p.Plan(ctx, member); err == nil || !strings.Contains(err.Error(), "apply Shared first") {
		t.Fatalf("plan before the parent is applied: got %v", err)
	}

	// The parent: one node in one step, which is the last step.
	shared := renderRequest(t, p, sharedEnvironment(), nil, backend)
	expectPlan(t, p, shared, "shared.pet:create")
	if err := p.Apply(ctx, shared, *shared.Environment.DeployOrder[0]); err != nil {
		t.Fatal(err)
	}
	sharedOut := outputs(t, p, shared)
	pet, _ := sharedOut["shared.pet"]["id"].(string)
	if !strings.HasPrefix(pet, "demo-") {
		t.Fatalf("shared.pet.id is %q", pet)
	}
	expectPlan(t, p, shared)

	// The member plans its own nodes and none of the parent's.
	expectPlan(t, p, member, "api.id:create", "api.name:create", "api.suffix:create")

	// Each step applies its own nodes only.
	steps := member.Environment.DeployOrder
	if err := p.Apply(ctx, member, *steps[0]); err != nil {
		t.Fatal(err)
	}
	out := outputs(t, p, member)
	if _, ok := out["api.id"]; ok {
		t.Errorf("api.id was applied with the infrastructure step: %v", out)
	}
	suffix, _ := out["api.suffix"]["result"].(string)
	if !regexp.MustCompile(`^[a-z0-9]{8}$`).MatchString(suffix) {
		t.Errorf("api.suffix.result is %q", suffix)
	}
	for _, step := range steps[1:] {
		if err := p.Apply(ctx, member, *step); err != nil {
			t.Fatal(err)
		}
	}
	out = outputs(t, p, member)
	// A random ID's hex carries its prefix, which reads the parameter.
	hex, _ := out["api.id"]["hex"].(string)
	if !regexp.MustCompile(`^api-7-[0-9a-f]{8}$`).MatchString(hex) {
		t.Errorf("api.id.hex is %q", hex)
	}
	if id, _ := out["api.id"]["id"].(string); id == "" {
		t.Error("api.id.id is empty")
	}
	if name, _ := out["api.name"]["id"].(string); !strings.HasPrefix(name, hex+"-") {
		t.Errorf("api.name.id is %q, want it prefixed with %s", name, hex)
	}
	if got := out["shared.pet"]["id"]; got != pet {
		t.Errorf("the member reads the inherited pet as %v, want %s", got, pet)
	}
	expectPlan(t, p, member)

	// The state lives in the backend: a fresh directory, as a CI checkout
	// has, plans no change.
	fresh := renderRequest(t, p, member.Environment, member.Parameters, backend)
	expectPlan(t, p, fresh)

	// A changed property replaces its node, and a node the graph no longer
	// has is deleted by the last step that holds nodes.
	changed := renderRequest(t, p, previewEnvironment(10, false), member.Parameters, backend)
	changes := plan(t, p, changed)
	for _, want := range []string{"api.suffix:replace", "api.name:delete"} {
		if !strings.Contains(changes, want) {
			t.Errorf("plan of the change lacks %s:\n%s", want, changes)
		}
	}
	for _, step := range changed.Environment.DeployOrder {
		if err := p.Apply(ctx, changed, *step); err != nil {
			t.Fatal(err)
		}
	}
	out = outputs(t, p, changed)
	if _, ok := out["api.name"]; ok {
		t.Errorf("api.name survived the last step: %v", out)
	}
	if suffix, _ := out["api.suffix"]["result"].(string); len(suffix) != 10 {
		t.Errorf("api.suffix.result is %q after the change to length 10", suffix)
	}
	expectPlan(t, p, changed)

	// Another member is a stack of its own.
	other := renderRequest(t, p, previewEnvironment(10, false), map[string]string{"pr": "8"}, backend)
	expectPlan(t, p, other, "api.id:create", "api.suffix:create")
	if err := p.Destroy(ctx, other); err != nil {
		t.Fatal(err)
	}

	// Destroy removes the member's stack and its settings file; a second
	// destroy finds nothing to do.
	if err := p.Destroy(ctx, changed); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Outputs(ctx, changed); err == nil || !strings.Contains(err.Error(), "apply it first") {
		t.Errorf("outputs after destroy: got %v", err)
	}
	if matches, _ := filepath.Glob(filepath.Join(changed.Dir, "Pulumi.*.yaml")); len(matches) > 0 {
		t.Errorf("destroy left %v", matches)
	}
	if err := p.Destroy(ctx, changed); err != nil {
		t.Errorf("a second destroy: %v", err)
	}
	if err := p.Destroy(ctx, shared); err != nil {
		t.Fatal(err)
	}
}

// plan returns a request's planned changes, one `node:action` per line.
func plan(t *testing.T, p *pulumi.Provisioner, req registry.ProvisionRequest) string {
	t.Helper()
	changes, err := p.Plan(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	var lines []string
	for _, c := range changes {
		lines = append(lines, c.Resource+":"+c.Action)
	}
	return strings.Join(lines, "\n")
}

// expectPlan checks a request's planned changes, sorted by node.
func expectPlan(t *testing.T, p *pulumi.Provisioner, req registry.ProvisionRequest, want ...string) {
	t.Helper()
	if got := plan(t, p, req); got != strings.Join(want, "\n") {
		t.Errorf("plan of %s:\n%s\nwant:\n%s", req.Environment.Environment, got, strings.Join(want, "\n"))
	}
}

func outputs(t *testing.T, p *pulumi.Provisioner, req registry.ProvisionRequest) map[string]map[string]any {
	t.Helper()
	out, err := p.Outputs(context.Background(), req)
	if err != nil {
		t.Fatal(err)
	}
	return out
}
