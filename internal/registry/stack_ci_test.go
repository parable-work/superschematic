package registry

import (
	"reflect"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
	ir "github.com/parable-work/superschematic/ir"
)

func renderNothing(CIRequest) ([]CIFile, error) { return nil, nil }

// TestRegisterCIRendererRejects covers each refusal of RegisterCIRenderer.
func TestRegisterCIRendererRejects(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec CIRendererSpec
		want string
	}{
		{"no name", CIRendererSpec{Dir: ".github/workflows", Render: renderNothing}, "CI renderer has no name"},
		{"bad name", CIRendererSpec{Name: "GitHub", Dir: ".github/workflows", Render: renderNothing}, `CI renderer name "GitHub" must be lowercase words`},
		{"no Render", CIRendererSpec{Name: "github", Dir: ".github/workflows"}, `CI renderer "github" has no Render function`},
		{"no Dir", CIRendererSpec{Name: "github", Render: renderNothing}, `CI renderer "github" Dir: is empty`},
		{"absolute Dir", CIRendererSpec{Name: "github", Dir: "/etc", Render: renderNothing}, `Dir: "/etc" is not a slash-separated path inside the repository`},
		{"Dir outside", CIRendererSpec{Name: "github", Dir: "../workflows", Render: renderNothing}, `Dir: "../workflows" is not a slash-separated path inside the repository`},
		{"unclean Dir", CIRendererSpec{Name: "github", Dir: ".github//workflows", Render: renderNothing}, "is not a slash-separated path"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := New(naming.Default()).RegisterCIRenderer(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterCIRenderer = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	reg := New(naming.Default())
	spec := CIRendererSpec{Name: "github", Dir: ".github/workflows", Render: renderNothing}
	if err := reg.RegisterCIRenderer(spec); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterCIRenderer(spec); err == nil || !strings.Contains(err.Error(), `CI renderer "github" is already registered`) {
		t.Errorf("duplicate CI renderer: %v", err)
	}
	if got, ok := reg.CIRenderer("github"); !ok || got.Dir != ".github/workflows" {
		t.Errorf("CIRenderer(github) = %+v, %v", got, ok)
	}
	if got := reg.CIRenderers(); !reflect.DeepEqual(got, []string{"github"}) {
		t.Errorf("CIRenderers = %v", got)
	}
	finalizeWithCoreGenerators(t, reg)
	if err := reg.RegisterCIRenderer(CIRendererSpec{Name: "gitlab", Dir: ".gitlab", Render: renderNothing}); err == nil || !strings.Contains(err.Error(), "after Finalize") {
		t.Errorf("CI renderer after Finalize: %v", err)
	}
}

// fixedCI gives every environment one identity per role.
type fixedCI struct{}

func (fixedCI) Identity(env *ir.ResolvedEnvironment, role CIRole) *CIIdentity {
	return &CIIdentity{Kind: "fixed", Fields: map[string]string{"account": env.Environment + "-" + string(role)}}
}

// TestCISeamAndTools: a target's CI seam needs a provisioner and State,
// as the other deploy seams do; a provisioner's tools each have a name and
// a version, once; CIEnvironment reads both.
func TestCISeamAndTools(t *testing.T) {
	for _, tc := range []struct {
		name string
		spec TargetSpec
		want string
	}{
		{"CI without provisioner", TargetSpec{Name: "fake", State: nopState{}, CI: fixedCI{}}, "has State, CI but names no provisioner"},
		{"CI without state", TargetSpec{Name: "fake", Provisioner: "fake", CI: fixedCI{}}, "has Bootstrap, Migrations, Builder, CI or Jobs but no State"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := New(naming.Default()).RegisterTarget(tc.spec)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterTarget = %v, want an error containing %q", err, tc.want)
			}
		})
	}
	for _, tc := range []struct {
		name  string
		tools []CLITool
		want  string
	}{
		{"no version", []CLITool{{Name: "pulumi"}}, `provisioner "fake" has a tool without a name or a version`},
		{"no name", []CLITool{{Version: "1"}}, `provisioner "fake" has a tool without a name or a version`},
		{"twice", []CLITool{{Name: "pulumi", Version: "1"}, {Name: "pulumi", Version: "2"}}, `provisioner "fake" names tool "pulumi" twice`},
	} {
		t.Run("tools "+tc.name, func(t *testing.T) {
			err := New(naming.Default()).RegisterProvisioner(ProvisionerSpec{Name: "fake", Provisioner: nopProvisioner{}, Tools: tc.tools})
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("RegisterProvisioner = %v, want an error containing %q", err, tc.want)
			}
		})
	}

	reg := New(naming.Default())
	pulumi := CLITool{Name: "pulumi", Version: "3.259.0"}
	if err := reg.RegisterProvisioner(ProvisionerSpec{Name: "fake", Provisioner: nopProvisioner{}, Tools: []CLITool{pulumi}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTarget(TargetSpec{Name: "fake", Provisioner: "fake", State: nopState{}, CI: fixedCI{}}); err != nil {
		t.Fatal(err)
	}
	if err := reg.RegisterTarget(TargetSpec{Name: "bare"}); err != nil {
		t.Fatal(err)
	}
	env := &ir.ResolvedEnvironment{Environment: "Staging", Target: "fake", Provisioner: "fake"}
	got := reg.CIEnvironment(env)
	want := CIEnvironment{
		Environment: env,
		HasSeam:     true,
		Planner:     &CIIdentity{Kind: "fixed", Fields: map[string]string{"account": "Staging-planner"}},
		Deployer:    &CIIdentity{Kind: "fixed", Fields: map[string]string{"account": "Staging-deployer"}},
		Tools:       []CLITool{pulumi},
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("CIEnvironment = %+v, want %+v", got, want)
	}
	if got.Identity(CIPlanner) != got.Planner || got.Identity(CIDeployer) != got.Deployer {
		t.Error("Identity does not return the role's identity")
	}
	bare := reg.CIEnvironment(&ir.ResolvedEnvironment{Environment: "Dev", Target: "bare"})
	if bare.HasSeam || bare.Planner != nil || bare.Deployer != nil || bare.Tools != nil {
		t.Errorf("an environment on a target with no CI seam = %+v", bare)
	}
}

// TestParseOutputsReadsCI covers outputs.ci: each key a registered CI
// renderer, each section its options, read strictly, with their defaults.
func TestParseOutputsReadsCI(t *testing.T) {
	reg := coreOutputRegistry(t)
	if err := reg.RegisterGenerator(GeneratorSpec{Name: "ci", Kinds: []string{"Stack"}, OutputKey: "ci", Generate: noopGenerate}); err != nil {
		t.Fatal(err)
	}
	spec := CIRendererSpec{Name: "github", Dir: ".github/workflows", Render: renderNothing}
	if err := reg.RegisterCIRenderer(spec); err != nil {
		t.Fatal(err)
	}
	ci := func(section map[string]any) map[string]any { return map[string]any{"ci": section} }
	outputs, err := ParseOutputs(ci(map[string]any{"github": map[string]any{"branch": "release/v1"}}), reg)
	if err != nil {
		t.Fatal(err)
	}
	if got := outputs.CIRenderers(); !reflect.DeepEqual(got, []string{"github"}) {
		t.Errorf("CIRenderers = %v", got)
	}
	if got := outputs.CI["github"].WithDefaults(spec); got != (CIOptions{Branch: "release/v1", Install: ".github/workflows"}) {
		t.Errorf("options = %+v", got)
	}
	if got := (CIOptions{}).WithDefaults(spec); got != (CIOptions{Branch: "main", Install: ".github/workflows"}) {
		t.Errorf("defaults = %+v", got)
	}
	for _, tc := range []struct {
		name    string
		section map[string]any
		want    string
	}{
		{"unknown renderer", map[string]any{"circle": map[string]any{}}, `outputs.ci names CI renderer "circle", which is not registered (registered CI renderers: github)`},
		{"unknown option", map[string]any{"github": map[string]any{"brnach": "main"}}, `outputs.ci.github: json: unknown field "brnach"`},
		{"a pattern", map[string]any{"github": map[string]any{"branch": "release/*"}}, `outputs.ci.github.branch "release/*" is not a branch name`},
		{"install outside", map[string]any{"github": map[string]any{"install": "../workflows"}}, `outputs.ci.github.install: "../workflows" is not a slash-separated path inside the repository`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := ParseOutputs(ci(tc.section), reg)
			if err == nil || err.Error() != tc.want {
				t.Fatalf("ParseOutputs = %v, want %q", err, tc.want)
			}
		})
	}
}
