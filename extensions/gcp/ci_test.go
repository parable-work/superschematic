package gcp_test

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
)

// projectNumber is Staging's in the CI tests, as bootstrap records it.
const projectNumber = "123456789012"

// ciShop is the shop with Staging's project number recorded, which
// Preview inherits; Production has none yet.
func ciShop() *ir.Stack {
	s := shop()
	s.Environments[0].Values["projectNumber"] = projectNumber
	return s
}

// TestCIIdentity: a CI job signs in to an environment with a project
// number through the provider of the pool bootstrap creates, as the
// role's account; an environment that extends one inherits the number,
// and one without it gets no identity.
func TestCIIdentity(t *testing.T) {
	reg := assemble(t)
	s := ciShop()
	target, ok := reg.Target(gcp.Target)
	if !ok || target.CI == nil {
		t.Fatal("the gcp target has no CI seam")
	}
	identity := func(role, project string) *registry.CIIdentity {
		return &registry.CIIdentity{Kind: gcp.CIIdentityKind, Fields: map[string]string{
			"provider": "projects/" + projectNumber + "/locations/global/workloadIdentityPools/shop-github/providers/github",
			"account":  "shop-" + role + "@" + project + ".iam.gserviceaccount.com",
		}}
	}
	for _, tc := range []struct {
		env  string
		role registry.CIRole
		want *registry.CIIdentity
	}{
		{"Staging", registry.CIPlanner, identity("planner", "acme-staging")},
		{"Staging", registry.CIDeployer, identity("deployer", "acme-staging")},
		{"Preview", registry.CIDeployer, identity("deployer", "acme-staging")},
		{"Production", registry.CIPlanner, nil},
		{"Production", registry.CIDeployer, nil},
	} {
		got := target.CI.Identity(resolve(t, reg, s, stacktest.WithoutBuckets(stacktest.AcmeShop()), tc.env), tc.role)
		if !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s as %s = %+v, want %+v", tc.env, tc.role, got, tc.want)
		}
	}

	// The pool, its provider and the accounts are the ones bootstrap
	// creates.
	boot, err := gcp.BootstrapEnvironment(resolve(t, reg, s, stacktest.WithoutBuckets(stacktest.AcmeShop()), "Staging"), "acme/shop")
	if err != nil {
		t.Fatal(err)
	}
	props := map[string]map[string]any{}
	for _, res := range boot.Resources.Resources {
		props[res.ID] = res.Properties
	}
	for id, want := range map[string][2]string{
		"github":          {"workloadIdentityPoolId", "shop-github"},
		"github.provider": {"workloadIdentityPoolProviderId", "github"},
		"planner":         {"accountId", "shop-planner"},
		"deployer":        {"accountId", "shop-deployer"},
	} {
		if got := props[id][want[0]]; got != want[1] {
			t.Errorf("bootstrap's %s %s = %v, want %s", id, want[0], got, want[1])
		}
	}
}

// TestCIGolden renders the shop's workflow with the core's GitHub Actions
// renderer, through the public registry, and checks it against the golden
// file, which -update rewrites: Staging plans and deploys, Preview deploys
// a member per pull request, and Production names the bootstrap to run.
func TestCIGolden(t *testing.T) {
	reg := assemble(t)
	s := ciShop()
	renderer, ok := reg.CIRenderer("github")
	if !ok {
		t.Fatal("the core registers no github CI renderer")
	}
	var envs []registry.CIEnvironment
	for _, env := range s.Environments {
		envs = append(envs, reg.CIEnvironment(resolve(t, reg, s, stacktest.WithoutBuckets(stacktest.AcmeShop()), env.Name)))
	}
	files, err := renderer.Render(registry.CIRequest{
		Stack: registry.CIStack{
			Name:           s.Name,
			Dir:            "schemas/services/" + s.Name,
			ServicesRoot:   "schemas/services",
			SchemasRoot:    "schemas",
			OutputRoot:     "schemas/dist",
			PackageManager: registry.PackageManagerBun,
		},
		Environments: envs,
		Options:      registry.CIOptions{}.WithDefaults(renderer),
		Version:      "1.2.3",
		Archives: map[string]registry.CIArchive{"linux-x64": {
			URL:    "https://github.com/parable-work/superschematic/releases/download/v1.2.3/superschematic-archives_1.2.3_linux-x64.tar.gz",
			SHA256: strings.Repeat("4", 64),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "Shop.yml" {
		t.Fatalf("files = %+v", files)
	}
	golden := filepath.Join(goldenRoot, "ci", files[0].Path)
	if *update {
		if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(golden, files[0].Data, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(golden)
	if err != nil {
		t.Fatalf("%v (run with -update to write it)", err)
	}
	if !bytes.Equal(files[0].Data, want) {
		t.Errorf("the workflow differs from %s; run with -update and review the diff", golden)
	}
	if bin, err := exec.LookPath("actionlint"); err == nil {
		if out, err := exec.Command(bin, "-no-color", golden).CombinedOutput(); err != nil {
			t.Errorf("actionlint %s: %v\n%s", golden, err, out)
		}
	}
}
