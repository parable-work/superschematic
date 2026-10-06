package pulumi_test

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/extensions/pulumi"
	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
)

var update = flag.Bool("update", false, "rewrite the golden files")

// checkGolden compares got with the golden file at path, which -update
// rewrites.
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
		t.Errorf("rendered output differs from %s; run with -update and review the diff:\n%s", path, got)
	}
}

// TestRenderGoldens renders the acme-shop environments the acceptance
// extension resolves, the integration test's environments and the edge
// cases, and compares each Pulumi.yaml with its golden copy.
func TestRenderGoldens(t *testing.T) {
	cases := []struct {
		name     string
		env      func(t *testing.T) *ir.ResolvedEnvironment
		versions map[string]string
	}{
		{name: "shop-staging", env: func(t *testing.T) *ir.ResolvedEnvironment { return shopEnvironment(t, "Staging") }},
		{name: "shop-production", env: func(t *testing.T) *ir.ResolvedEnvironment { return shopEnvironment(t, "Production") }},
		{name: "shop-preview", env: func(t *testing.T) *ir.ResolvedEnvironment { return shopEnvironment(t, "Preview") }},
		{name: "demo-shared", env: func(*testing.T) *ir.ResolvedEnvironment { return sharedEnvironment() },
			versions: map[string]string{"random": randomVersion}},
		{name: "demo-preview", env: func(*testing.T) *ir.ResolvedEnvironment { return previewEnvironment(8, true) },
			versions: map[string]string{"random": randomVersion}},
		{name: "edge-cases", env: func(*testing.T) *ir.ResolvedEnvironment { return casesEnvironment() },
			versions: map[string]string{"gcp": "9.37.1"}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := pulumi.Extension{ProviderVersions: tc.versions}.Provisioner()
			dir := t.TempDir()
			env := tc.env(t)
			if err := p.Render(env, dir); err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(filepath.Join(dir, pulumi.ProgramFile))
			if err != nil {
				t.Fatal(err)
			}
			checkGolden(t, filepath.Join("testdata", "render", tc.name, pulumi.ProgramFile), got)

			// Rendering again gives the same bytes, and leaves other files
			// in the directory alone.
			settings := filepath.Join(dir, "Pulumi.staging.yaml")
			if err := os.WriteFile(settings, []byte("config: {}\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			if err := p.Render(env, dir); err != nil {
				t.Fatal(err)
			}
			again, err := os.ReadFile(filepath.Join(dir, pulumi.ProgramFile))
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, got) {
				t.Error("a second render differs from the first")
			}
			if _, err := os.Stat(settings); err != nil {
				t.Errorf("render removed a stack settings file: %v", err)
			}
		})
	}
}

// TestRenderKeepsParameterValuesOut checks that a parameter's value never
// reaches the program: it is the project's config, set per run.
func TestRenderKeepsParameterValuesOut(t *testing.T) {
	data := render(t, previewEnvironment(8, false))
	for _, want := range []string{"config:\n  pr:\n    type: string", "prefix: api-${pr}-", "pr: ${pr}"} {
		if !strings.Contains(data, want) {
			t.Errorf("program lacks %q:\n%s", want, data)
		}
	}
}

// TestExports lists what the program exports: every node's id, and every
// output the environment references, inherited nodes' included.
func TestExports(t *testing.T) {
	exports, err := pulumi.Exports(casesEnvironment())
	if err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, exp := range exports {
		keys = append(keys, exp.Key)
	}
	want := []string{
		"api.account.email", // a binding and a property
		"api.account.id",
		"api.cert.dnsResourceRecords[0].data", // a DNS record only
		"api.cert.domain",
		"api.cert.id",
		"api.service.id",
		"api.service.uri", // the deployable's address only
	}
	if strings.Join(keys, "\n") != strings.Join(want, "\n") {
		t.Errorf("exports:\n%s\nwant:\n%s", strings.Join(keys, "\n"), strings.Join(want, "\n"))
	}

	exports, err = pulumi.Exports(previewEnvironment(8, false))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]pulumi.Export{}
	for _, exp := range exports {
		got[exp.Key] = exp
	}
	if exp, ok := got["shared.pet.id"]; !ok || (exp != pulumi.Export{Key: "shared.pet.id", Resource: "shared.pet", Name: "id"}) {
		t.Errorf("the inherited pet's id is not exported: %+v", exports)
	}
}

func render(t *testing.T, env *ir.ResolvedEnvironment) string {
	t.Helper()
	dir := t.TempDir()
	if err := new(pulumi.Provisioner).Render(env, dir); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(filepath.Join(dir, pulumi.ProgramFile))
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// TestRenderRefuses lists the graphs a program cannot carry.
func TestRenderRefuses(t *testing.T) {
	node := func(id, typ string, props map[string]any) *ir.Resource {
		return &ir.Resource{ID: id, Type: typ, Properties: props}
	}
	env := func(params []string, nodes ...*ir.Resource) *ir.ResolvedEnvironment {
		return &ir.ResolvedEnvironment{
			Stack: "Shop", Environment: "Staging", Parameters: params,
			Resources: &ir.ResourceGraph{Parameters: params, Resources: nodes},
		}
	}
	const typ = "random:index/randomPet:RandomPet"
	cases := []struct {
		name string
		env  *ir.ResolvedEnvironment
		want string
	}{
		{"no graph", &ir.ResolvedEnvironment{Stack: "Shop", Environment: "Staging"}, "no resource graph"},
		{"keys collide", env(nil, node("a.b", typ, nil), node("a-b", typ, nil)), "takes the program key a-b"},
		{"key is a parameter", env([]string{"pr"}, node("pr", typ, nil)), "which parameter pr has"},
		{"key is the parent reference", env(nil, node("parent.stack", typ, nil)), "the parent stack reference"},
		{"repeated node", env(nil, node("a", typ, nil), node("a", typ, nil)), "two nodes a"},
		{"glob in an ID", env(nil, node("a*", typ, nil)), "node ID"},
		{"URN separator in an ID", env(nil, node("a::b", typ, nil)), "node ID"},
		{"type is no token", env(nil, node("a", "RandomPet", nil)), "not a Pulumi type token"},
		{"unknown node", env(nil, node("a", typ, map[string]any{"x": ir.Output{Resource: "b", Name: "id"}})), "which the graph lacks"},
		{"unknown dependency", env(nil, &ir.Resource{ID: "a", Type: typ, DependsOn: []string{"b"}}), "depends on node b"},
		{"output is no path", env(nil, node("a", typ, nil), node("b", typ, map[string]any{"x": ir.Output{Resource: "a", Name: "a b"}})), "an output is a property name or path"},
		{"unknown parameter", env(nil, node("a", typ, map[string]any{"x": ir.Parameter("pr")})), "parameter pr"},
		{"function call", env(nil, node("a", typ, map[string]any{"x": map[string]any{"fn::invoke": "f"}})), "function call"},
		{"inherited without a parent", env(nil, &ir.Resource{ID: "a", Type: typ, Inherited: true}), "extends no environment"},
		{"stack makes no project", &ir.ResolvedEnvironment{Stack: "-", Environment: "Staging", Resources: &ir.ResourceGraph{}}, "no Pulumi project"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := new(pulumi.Provisioner).Render(tc.env, t.TempDir())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
		})
	}
}

// TestNames checks the project and stack names a run takes.
func TestNames(t *testing.T) {
	project, err := pulumi.ProjectName("EdgeCases")
	if err != nil || project != "edge-cases" {
		t.Errorf("ProjectName(EdgeCases) = %q, %v", project, err)
	}
	for _, tc := range []struct {
		env    string
		params []string
		values map[string]string
		want   string
		err    string
	}{
		{env: "Staging", want: "staging"},
		{env: "PreProd", want: "pre-prod"},
		{env: "Preview", params: []string{"pr"}, values: map[string]string{"pr": "123"}, want: "preview.pr-123"},
		{env: "Preview", params: []string{"pr", "region"}, values: map[string]string{"pr": "7", "region": "us-east1"}, want: "preview.pr-7.region-us-east1"},
		{env: "Preview", params: []string{"pr"}, err: "takes parameters"},
		{env: "Preview", params: []string{"pr"}, values: map[string]string{"other": "1"}, err: "needs a value for parameter pr"},
		{env: "Preview", params: []string{"pr"}, values: map[string]string{"pr": "a/b"}, err: "a value is letters"},
	} {
		got, err := pulumi.StackName(tc.env, tc.params, tc.values)
		if tc.err != "" {
			if err == nil || !strings.Contains(err.Error(), tc.err) {
				t.Errorf("StackName(%s, %v) = %q, %v; want an error containing %q", tc.env, tc.values, got, err, tc.err)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Errorf("StackName(%s, %v) = %q, %v; want %q", tc.env, tc.values, got, err, tc.want)
		}
	}
}

// TestRegister registers the provisioner under the name a target names.
func TestRegister(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming(), pulumi.Extension{})
	if err != nil {
		t.Fatal(err)
	}
	spec, ok := reg.Provisioner(pulumi.Name)
	if !ok || spec.Extension != pulumi.Name {
		t.Fatalf("provisioner %s is not registered: %+v", pulumi.Name, spec)
	}
	if _, ok := spec.Provisioner.(*pulumi.Provisioner); !ok {
		t.Errorf("provisioner %s is a %T", pulumi.Name, spec.Provisioner)
	}
}
