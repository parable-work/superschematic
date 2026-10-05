package bindings_test

import (
	"bytes"
	"flag"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/pulumi"
	"github.com/parable-work/superschematic/extensions/pulumi/bindings"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

var (
	update   = flag.Bool("update", false, "rewrite the golden files")
	fixtures = flag.Bool("fixtures", false, "rewrite testdata/outputs from the environments' exports, with made-up values")
)

// shopEnvironment resolves an environment of the acme-shop stack over the
// stack model's acceptance extension.
func shopEnvironment(t *testing.T, name string) *ir.ResolvedEnvironment {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), &stacktest.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Resolve(reg, stack.Input{Stack: stacktest.Shop(), Services: stacktest.AcmeShop(), Environment: name})
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// shopOutputs reads the outputs file of an applied acme-shop environment.
// With -fixtures it first writes one, with a made-up value for every
// output the environment's program exports.
func shopOutputs(t *testing.T, env *ir.ResolvedEnvironment) *bindings.Outputs {
	t.Helper()
	path := filepath.Join("testdata", "outputs", env.Environment, bindings.OutputsFile)
	if *fixtures {
		exports, err := pulumi.Exports(env)
		if err != nil {
			t.Fatal(err)
		}
		resources := map[string]map[string]any{}
		for _, exp := range exports {
			if resources[exp.Resource] == nil {
				resources[exp.Resource] = map[string]any{}
			}
			resources[exp.Resource][exp.Name] = strings.ToLower(env.Environment) + ":" + exp.Key
		}
		data, err := bindings.NewOutputs(env, nil, resources).Marshal()
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -fixtures to write it)", err)
	}
	out, err := bindings.UnmarshalOutputs(data)
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// shopEnvironments are the acme-shop environments: Staging and Production
// applied, and Preview, a parameterized environment with no value.
func shopEnvironments(t *testing.T) []bindings.Environment {
	staging, production := shopEnvironment(t, "Staging"), shopEnvironment(t, "Production")
	return []bindings.Environment{
		{Resolved: staging, Outputs: shopOutputs(t, staging)},
		{Resolved: production, Outputs: shopOutputs(t, production)},
		{Resolved: shopEnvironment(t, "Preview")},
	}
}

// kindsEnvironments hold outputs of every kind: a string, a bool, a
// number, an object, one that is a string in one environment and a number
// in the other, and one no outputs file holds.
func kindsEnvironments() []bindings.Environment {
	env := func(name string) *ir.ResolvedEnvironment {
		return &ir.ResolvedEnvironment{
			Version: ir.ResolvedEnvironmentVersion, Stack: "Kinds", Environment: name, Target: "t",
			Deployables: []*ir.ResolvedDeployable{{
				Name: "worker", Kind: ir.DeployableServer, Platform: "p", ResourceName: "worker",
				Address: ir.Concat{"http://", ir.Output{Resource: "worker.host", Name: "address"}, ":8080"},
			}},
			Resources: &ir.ResourceGraph{Resources: []*ir.Resource{
				{
					ID: "worker.host", Type: "random:index/randomPet:RandomPet", Owners: []string{"worker"},
					Properties: map[string]any{
						"a": ir.Output{Resource: "worker.limits", Name: "enabled"},
						"b": ir.Output{Resource: "worker.limits", Name: "cpu"},
						"c": ir.Output{Resource: "worker.limits", Name: "labels"},
						"d": ir.Output{Resource: "worker.limits", Name: "port"},
						"e": ir.Output{Resource: "worker.limits", Name: "zone"},
					},
				},
				{ID: "worker.limits", Type: "random:index/randomId:RandomId", Owners: []string{"worker"}},
			}},
		}
	}
	one, two := env("One"), env("Two")
	return []bindings.Environment{
		{Resolved: one, Outputs: bindings.NewOutputs(one, nil, map[string]map[string]any{
			"worker.host":   {"address": "one.internal", "id": "one-host"},
			"worker.limits": {"enabled": true, "cpu": 0.5, "labels": map[string]any{"tier": "gold", "n": float64(2)}, "port": "8080", "id": "l1"},
		})},
		{Resolved: two, Outputs: bindings.NewOutputs(two, nil, map[string]map[string]any{
			"worker.host":   {"address": "two.internal"},
			"worker.limits": {"enabled": false, "cpu": float64(2), "port": float64(9090), "id": "l2"},
		})},
	}
}

// TestGenerateGoldens compares both packages of each binding with their
// golden copies under testdata/golden, which TestGoldensCompile builds.
func TestGenerateGoldens(t *testing.T) {
	cases := []struct {
		pkg  string
		envs func(t *testing.T) []bindings.Environment
	}{
		{"shopstack", shopEnvironments},
		{"kinds", func(*testing.T) []bindings.Environment { return kindsEnvironments() }},
	}
	for _, tc := range cases {
		t.Run(tc.pkg, func(t *testing.T) {
			files, err := bindings.Generate(tc.pkg, tc.envs(t))
			if err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{bindings.ValuesFile, bindings.PulumiFile} {
				checkGolden(t, filepath.Join("testdata", "golden", tc.pkg, filepath.FromSlash(name)), files[name])
			}
			dir := t.TempDir()
			if err := bindings.Write(dir, tc.pkg, tc.envs(t)); err != nil {
				t.Fatal(err)
			}
			for _, name := range []string{bindings.ValuesFile, bindings.PulumiFile} {
				got, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(name)))
				if err != nil || !bytes.Equal(got, files[name]) {
					t.Errorf("Write wrote %s unlike Generate: %v", name, err)
				}
			}
		})
	}
}

func checkGolden(t *testing.T, path string, got []byte) {
	t.Helper()
	if *update {
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, got, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(got, want) {
		t.Errorf("generated output differs from %s; run with -update and review the diff:\n%s", path, got)
	}
}

// TestGoldensCompile builds the golden packages and a program that uses
// them as code outside the stack would, vets them, and runs the program's
// plain half, which prints values the binding read from the outputs files.
func TestGoldensCompile(t *testing.T) {
	if _, err := exec.LookPath("go"); err != nil {
		t.Skip("no go command")
	}
	vet := exec.Command("go", "vet", "./testdata/golden/...", "./testdata/use")
	if out, err := vet.CombinedOutput(); err != nil {
		t.Fatalf("go vet: %v\n%s", err, out)
	}
	run := exec.Command("go", "run", "./testdata/use")
	out, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("go run: %v\n%s", err, out)
	}
	want := strings.Join([]string{
		"shop-api staging:shop-api.service.url staging:shop-api.account.email",
		"Production production:shop-db.instance.connectionName",
		"Staging staging:shop-db.instance.connectionName",
		"http://one.internal:8080 true 0.5 gold",
		"",
	}, "\n")
	if string(out) != want {
		t.Errorf("the program printed:\n%s\nwant:\n%s", out, want)
	}
}

// TestOutputsFile round-trips an outputs file and refuses another version.
func TestOutputsFile(t *testing.T) {
	env := shopEnvironment(t, "Preview")
	out := bindings.NewOutputs(env, map[string]string{"pr": "7"}, map[string]map[string]any{"shop-api.service": {"url": "https://x"}})
	data, err := out.Marshal()
	if err != nil {
		t.Fatal(err)
	}
	back, err := bindings.UnmarshalOutputs(data)
	if err != nil {
		t.Fatal(err)
	}
	again, err := back.Marshal()
	if err != nil || !bytes.Equal(again, data) {
		t.Errorf("outputs file does not round-trip: %v\n%s\n%s", err, data, again)
	}
	if back.Stack != "Shop" || back.Environment != "Preview" || back.Parameters["pr"] != "7" {
		t.Errorf("read back %+v", back)
	}
	if _, err := bindings.UnmarshalOutputs([]byte(`{"version": 2, "resources": {}}`)); err == nil || !strings.Contains(err.Error(), "version 2") {
		t.Errorf("version 2: got %v", err)
	}
}

// TestGenerateRefuses lists the bindings the generator refuses.
func TestGenerateRefuses(t *testing.T) {
	staging, preview := shopEnvironment(t, "Staging"), shopEnvironment(t, "Preview")
	other := shopEnvironment(t, "Production")
	other.Stack = "Other"
	clash := kindsEnvironments()[0].Resolved
	clash.Resources.Resources = append(clash.Resources.Resources,
		&ir.Resource{ID: "worker.name", Type: "random:index/randomPet:RandomPet", Owners: []string{"worker"}})
	cases := []struct {
		name string
		pkg  string
		envs []bindings.Environment
		want string
	}{
		{"bad package", "Shop", []bindings.Environment{{Resolved: staging}}, "lowercase letters"},
		{"no environments", "shop", nil, "no environments"},
		{"two stacks", "shop", []bindings.Environment{{Resolved: staging}, {Resolved: other}}, "a binding covers one stack"},
		{"repeated environment", "shop", []bindings.Environment{{Resolved: staging}, {Resolved: staging}}, "appears twice"},
		{"outputs of a parameterized environment", "shop", []bindings.Environment{{Resolved: preview, Outputs: bindings.NewOutputs(preview, map[string]string{"pr": "1"}, nil)}}, "no value of its own"},
		{"outputs of another environment", "shop", []bindings.Environment{{Resolved: staging, Outputs: bindings.NewOutputs(other, nil, nil)}}, "were given for environment"},
		{"node takes the name field", "kinds", []bindings.Environment{{Resolved: clash}}, "both field Name"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := bindings.Generate(tc.pkg, tc.envs)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}
