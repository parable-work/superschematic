package cloudflare_test

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

	"github.com/parable-work/superschematic/extensions/cloudflare"
)

// TestTarget: the extension registers the target beside its DNS platform,
// which is the target's default, with the pulumi provisioner, the pinned
// account token type and every deploy seam built so far; it places a
// server on Workers, a bucket on R2, a database on D1, or on Hyperdrive when
// its DB services list postgres first (D92), a job on Cron, a worker on the
// drainer and a site on Workers' static assets.
func TestTarget(t *testing.T) {
	target := newFixture(t).target(t)
	if target.DNS != cloudflare.DNS || target.Provisioner != "pulumi" {
		t.Errorf("target DNS %q, provisioner %q", target.DNS, target.Provisioner)
	}
	if want := map[ir.DeployableKind]string{
		ir.DeployableServer:   cloudflare.Workers,
		ir.DeployableBucket:   cloudflare.R2,
		ir.DeployableDatabase: cloudflare.D1Platform,
		ir.DeployableJob:      cloudflare.Cron,
		ir.DeployableSite:     cloudflare.Sites,
		ir.DeployableWorker:   cloudflare.Drainer,
	}; !reflect.DeepEqual(target.Platforms, want) {
		t.Errorf("the target places %v, want %v", target.Platforms, want)
	}
	if want := map[string]string{registry.SQLDialectPostgres: cloudflare.HyperdrivePlatform}; !reflect.DeepEqual(target.DatabasePlatforms, want) {
		t.Errorf("the target places a database by its dialect on %v, want %v", target.DatabasePlatforms, want)
	}
	if target.State == nil || target.Secrets == nil || target.Bootstrap == nil || target.CI == nil || target.Bundles == nil ||
		target.PreDestroy == nil || target.Migrations == nil || target.Sites == nil || target.Jobs == nil {
		t.Errorf("the target lacks a seam: %+v", target)
	}
	if _, ok := target.Bundles.(registry.ArtifactFetcher); !ok {
		t.Error("the bundle store fetches nothing back")
	}
	if _, ok := target.Sites.(registry.ArtifactFetcher); !ok {
		t.Error("the site store fetches nothing back")
	}
	var rules []string
	for _, rule := range target.Policies {
		rules = append(rules, rule.Name)
	}
	if !reflect.DeepEqual(rules, []string{cloudflare.PolicyDomainOnCloudflare, cloudflare.PolicyNothingPublic, cloudflare.PolicyBucketsNeverPublic}) {
		t.Errorf("policies = %v", rules)
	}
	reg := newFixture(t).reg
	ok, err := reg.ValidateResource(&ir.Resource{ID: "t", Type: cloudflare.TypeAccountToken, Properties: map[string]any{
		"accountId": accountID, "name": "t", "policies": []any{map[string]any{"effect": "allow", "permissionGroups": []any{map[string]any{"id": "x"}}, "resources": "{}"}},
	}})
	if !ok || err != nil {
		t.Errorf("the account token type is pinned %v: %v", ok, err)
	}
}

// TestValues: an environment enters its account, 32 hexadecimal digits,
// and bootstrap records the workers.dev subdomain and the Secrets Store's
// id beside it; anything else is refused.
func TestValues(t *testing.T) {
	target := newFixture(t).target(t)
	for _, ok := range []map[string]any{
		{"accountId": accountID},
		{"accountId": accountID, "workersSubdomain": "acme", "secretsStoreId": strings.Repeat("ab", 16)},
		{"accountId": accountID, "workersSubdomain": "acme-dev-1"},
	} {
		if err := target.ValidateValues(ok); err != nil {
			t.Errorf("%v: %v", ok, err)
		}
	}
	for _, tc := range []struct {
		values map[string]any
		want   string
	}{
		{map[string]any{}, "accountId"},
		{map[string]any{"accountId": "0123"}, "accountId"},
		{map[string]any{"accountId": strings.ToUpper(accountID)}, "accountId"},
		{map[string]any{"accountId": accountID, "workersSubdomain": "Acme"}, "workersSubdomain"},
		{map[string]any{"accountId": accountID, "workersSubdomain": "-acme"}, "workersSubdomain"},
		{map[string]any{"accountId": accountID, "secretsStoreId": "store"}, "secretsStoreId"},
		{map[string]any{"accountId": accountID, "project": "acme"}, "project"},
	} {
		if err := target.ValidateValues(tc.values); err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%v: %v, want an error naming %s", tc.values, err, tc.want)
		}
	}
}

// TestDNSCredentialsOnTheTarget: on the cloudflare target the run's own
// token carries the zone, so the DNS platform names no credential; beside
// another target it names the zone's token.
func TestDNSCredentialsOnTheTarget(t *testing.T) {
	reg := newFixture(t).reg
	dns, ok := reg.DNSPlatform(cloudflare.DNS)
	if !ok {
		t.Fatal("no cloudflare DNS platform")
	}
	ctx := registry.DNSContext{
		Environment: registry.StackEnvironment{Stack: "edge-stack", Name: "Staging", Target: cloudflare.Target, Domain: "staging.acme.dev"},
		Values:      map[string]any{"zone": "acme.dev", "zoneId": zoneID},
	}
	if got := dns.Credentials(ctx); len(got) != 0 {
		t.Errorf("on the cloudflare target: credentials %+v", got)
	}
	ctx.Environment.Target = stacktest.Target
	if got := dns.Credentials(ctx); len(got) != 1 || got[0].Secret != cloudflare.TokenSecret("edge-stack", "acme.dev") || got[0].Env != cloudflare.TokenEnv {
		t.Errorf("beside the fake target: credentials %+v", got)
	}
}

// TestPolicyDomainOnCloudflare: a domain on any DNS platform but
// cloudflare, a manual one among them, is refused; no domain and a domain
// on cloudflare pass.
func TestPolicyDomainOnCloudflare(t *testing.T) {
	target := newFixture(t).target(t)
	check := target.Policies[0].Check
	env := staging()
	if got := check(env); got != nil {
		t.Errorf("no domain: %v", got)
	}
	env.Domain = "staging.acme.dev"
	env.DNS = &ir.ResolvedDNS{Platform: cloudflare.DNS}
	if got := check(env); got != nil {
		t.Errorf("a domain on cloudflare: %v", got)
	}
	for _, platform := range []string{ir.ManualDNS, stacktest.DNSPlatform} {
		env.DNS = &ir.ResolvedDNS{Platform: platform}
		got := check(env)
		if len(got) != 1 || !strings.Contains(got[0], "domain staging.acme.dev on DNS platform "+platform) {
			t.Errorf("a domain on %s: %v", platform, got)
		}
	}
}

// TestCIIdentity: a plain environment's deploy job reads the deployer's
// token and the passphrase from the GitHub environment of its name, its
// plan job the planner's and a parameterized environment's preview job the
// preview's from repository secrets; each name ends with the stack's name
// and the environment's (section 18.3).
func TestCIIdentity(t *testing.T) {
	ci := newFixture(t).target(t).CI
	identity := func(token, passphrase, environment string) *registry.CIIdentity {
		fields := map[string]string{"token": token, "passphrase": passphrase}
		if environment != "" {
			fields["environment"] = environment
		}
		return &registry.CIIdentity{Kind: cloudflare.CIIdentityKind, Fields: fields}
	}
	preview := cloudflareEnv("PreviewEu", map[string]any{"accountId": accountID}, "pr")
	for _, tc := range []struct {
		env  *ir.ResolvedEnvironment
		role registry.CIRole
		want *registry.CIIdentity
	}{
		{staging(), registry.CIPlanner, identity("CLOUDFLARE_PLANNER_TOKEN_EDGE_STACK_STAGING", "PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_STAGING", "")},
		{staging(), registry.CIDeployer, identity("CLOUDFLARE_DEPLOYER_TOKEN_EDGE_STACK_STAGING", "PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_STAGING", "Staging")},
		{preview, registry.CIDeployer, identity("CLOUDFLARE_PREVIEW_TOKEN_EDGE_STACK_PREVIEW_EU", "PULUMI_CONFIG_PASSPHRASE_EDGE_STACK_PREVIEW_EU", "")},
		{cloudflareEnv("Staging", nil), registry.CIDeployer, nil},
	} {
		if got := ci.Identity(tc.env, tc.role); !reflect.DeepEqual(got, tc.want) {
			t.Errorf("%s as %s = %+v, want %+v", tc.env.Environment, tc.role, got, tc.want)
		}
	}
}

// TestCIGolden renders the edge stack's workflow with the core's GitHub
// Actions renderer and checks it against the golden file, which -update
// rewrites: Staging plans and deploys, Preview deploys a member per pull
// request, each job's env reads the token and the passphrase from the
// secrets the CI seam names, and no job asks for an identity token.
func TestCIGolden(t *testing.T) {
	reg := newFixture(t).reg
	renderer, ok := reg.CIRenderer("github")
	if !ok {
		t.Fatal("the core registers no github CI renderer")
	}
	preview := cloudflareEnv("Preview", map[string]any{"accountId": accountID}, "pr")
	preview.Extends = "Staging"
	var envs []registry.CIEnvironment
	for _, env := range []*ir.ResolvedEnvironment{staging(), preview} {
		envs = append(envs, reg.CIEnvironment(env))
	}
	files, err := renderer.Render(registry.CIRequest{
		Stack: registry.CIStack{
			Name:           "edge-stack",
			Dir:            "schemas/services/edge-stack",
			ServicesRoot:   "schemas/services",
			SchemasRoot:    "schemas",
			OutputRoot:     "schemas/dist",
			PackageManager: registry.PackageManagerBun,
		},
		Environments: envs,
		Options:      registry.CIOptions{}.WithDefaults(renderer),
		Version:      "1.2.3",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 || files[0].Path != "edge-stack.yml" {
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
	if bytes.Contains(want, []byte("id-token")) {
		t.Error("a job asks for an identity token")
	}
	if bin, err := exec.LookPath("actionlint"); err == nil {
		if out, err := exec.Command(bin, "-no-color", golden).CombinedOutput(); err != nil {
			t.Errorf("actionlint %s: %v\n%s", golden, err, out)
		}
	}
}
