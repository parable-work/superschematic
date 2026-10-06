package pulumi_test

import (
	"os"
	"path/filepath"
	"sort"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"
)

// shopEnvironment resolves an environment of the acme-shop stack over the
// stack model's acceptance extension, as its golden environment.json files
// are made.
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

// gcpGoldenRoot is the output root of extensions/gcp's golden
// environment.json files. go.mod's replace directive puts that module
// beside this one, so the path is the module's own.
var gcpGoldenRoot = filepath.Join("..", "gcp", "testdata", "golden")

// gcpEnvironment reads an environment of the shop stack on the gcp target
// as extensions/gcp's TestGolden resolves and checks it: the shop's
// services on Cloud Run and Cloud SQL, every node checked against the
// pinned pulumi-gcp schemas.
func gcpEnvironment(t *testing.T, name string) *ir.ResolvedEnvironment {
	t.Helper()
	data, err := os.ReadFile(stack.EnvironmentPath(gcpGoldenRoot, "Shop", name))
	if err != nil {
		t.Fatal(err)
	}
	env, err := stack.Unmarshal(data)
	if err != nil {
		t.Fatal(err)
	}
	return env
}

// RandomVersion is the random provider the integration test pins.
const randomVersion = "4.21.2"

// The random provider's types, which apply with no cloud credentials.
const (
	typePet    = "random:index/randomPet:RandomPet"
	typeString = "random:index/randomString:RandomString"
	typeID     = "random:index/randomId:RandomId"
)

// sharedEnvironment is the parent of previewEnvironment: one pet, which the
// members share.
func sharedEnvironment() *ir.ResolvedEnvironment {
	pet := &ir.Resource{
		ID:         "shared.pet",
		Type:       typePet,
		Properties: map[string]any{"length": float64(2), "prefix": "demo"},
		Phase:      ir.PhaseInfrastructure,
		Owners:     []string{"shared"},
	}
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       "Demo",
		Environment: "Shared",
		Target:      "random",
		Provisioner: "pulumi",
		Deployables: []*ir.ResolvedDeployable{{
			Name: "shared", Kind: ir.DeployableDatabase, Platform: "random.pet", Dialect: "postgres",
			ResourceName: "shared",
			Address:      ir.Output{Resource: "shared.pet", Name: "id"},
		}},
		Resources: &ir.ResourceGraph{Resources: []*ir.Resource{pet}},
		DeployOrder: []*ir.DeployStep{
			{Step: ir.StepInfrastructure, Resources: []string{"shared.pet"}},
		},
	}
}

// previewEnvironment is a member of a parameterized environment over the
// random provider: it inherits the shared pet, names its own nodes with
// the parameter pr, and applies them in two steps. length sets the
// suffix's length, so a test can change one property; withName adds a
// node only the last step applies.
func previewEnvironment(length int, withName bool) *ir.ResolvedEnvironment {
	resources := []*ir.Resource{
		{
			ID:   "api.id",
			Type: typeID,
			Properties: map[string]any{
				"byteLength": float64(4),
				"prefix":     ir.Concat{"api-", ir.Parameter("pr"), "-"},
				"keepers":    map[string]any{"suffix": ir.Output{Resource: "api.suffix", Name: "result"}},
			},
			DependsOn: []string{"api.suffix"},
			Phase:     ir.PhaseRollout,
			Owners:    []string{"api"},
		},
		{
			ID:   "api.suffix",
			Type: typeString,
			Properties: map[string]any{
				"length":  float64(length),
				"special": false,
				"upper":   false,
				"keepers": map[string]any{
					"pet": ir.Output{Resource: "shared.pet", Name: "id"},
					"pr":  ir.Parameter("pr"),
				},
			},
			DependsOn: []string{"shared.pet"},
			Phase:     ir.PhaseInfrastructure,
			Owners:    []string{"api"},
		},
		{
			ID:        "shared.pet",
			Type:      typePet,
			Phase:     ir.PhaseInfrastructure,
			Inherited: true,
			Owners:    []string{"shared"},
		},
	}
	exposure := &ir.DeployStep{Step: ir.StepExposure}
	if withName {
		resources = append(resources, &ir.Resource{
			ID:         "api.name",
			Type:       typePet,
			Properties: map[string]any{"prefix": ir.Output{Resource: "api.id", Name: "hex"}},
			DependsOn:  []string{"api.id"},
			Phase:      ir.PhaseExposure,
			Owners:     []string{"api"},
		})
		sort.Slice(resources, func(i, j int) bool { return resources[i].ID < resources[j].ID })
		exposure.Resources = []string{"api.name"}
	}
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       "Demo",
		Environment: "Preview",
		Extends:     "Shared",
		Target:      "random",
		Provisioner: "pulumi",
		Parameters:  []string{"pr"},
		Deployables: []*ir.ResolvedDeployable{
			{
				Name: "api", Kind: ir.DeployableServer, Platform: "random.id", Language: "GO",
				Services:     []ir.ServiceRef{{Name: "api", Kind: "API"}},
				ResourceName: ir.Concat{"api-", ir.Parameter("pr")},
				Address:      ir.Concat{"https://", ir.Output{Resource: "api.id", Name: "hex"}, ".example.test"},
			},
			{
				Name: "shared", Kind: ir.DeployableDatabase, Platform: "random.pet", Dialect: "postgres",
				ResourceName: "shared",
				Address:      ir.Output{Resource: "shared.pet", Name: "id"},
			},
		},
		Resources: &ir.ResourceGraph{Parameters: []string{"pr"}, Resources: resources},
		DeployOrder: []*ir.DeployStep{
			{Step: ir.StepInfrastructure, Resources: []string{"api.suffix"}},
			{Step: ir.StepRollout, Wave: 1, Deployables: []string{"api"}, Resources: []string{"api.id"}},
			exposure,
		},
	}
}

// casesEnvironment holds the values a program must carry exactly: strings
// that look like other YAML scalars or hold `$`, numbers, nulls, nested
// values, a property path, a concat of an output and a parameter, a pinned
// provider, and outputs only a deployable, a binding or a DNS record
// references.
func casesEnvironment() *ir.ResolvedEnvironment {
	return &ir.ResolvedEnvironment{
		Version:     ir.ResolvedEnvironmentVersion,
		Stack:       "EdgeCases",
		Environment: "PreProd",
		Target:      "gcp",
		Parameters:  []string{"region"},
		Domain:      "cases.example.test",
		DNS: &ir.ResolvedDNS{Platform: "gcp.dns", Records: []*ir.DNSRecord{{
			Name:       "api.cases.example.test",
			Type:       "TXT",
			Value:      ir.Output{Resource: "api.cert", Name: "dnsResourceRecords[0].data"},
			Deployable: "api",
		}}},
		Deployables: []*ir.ResolvedDeployable{{
			Name: "api", Kind: ir.DeployableServer, Platform: "gcp.cloudrun", Language: "GO",
			ResourceName: ir.Concat{"api-", ir.Parameter("region")},
			Address:      ir.Output{Resource: "api.service", Name: "uri"},
			Bindings: []*ir.Binding{{
				Field: "ACCOUNT", Source: ir.BindingDerived,
				Value: ir.Output{Resource: "api.account", Name: "email"},
			}},
		}},
		Resources: &ir.ResourceGraph{Parameters: []string{"region"}, Resources: []*ir.Resource{
			{
				ID:         "api.account",
				Type:       "gcp:serviceaccount/account:Account",
				Properties: map[string]any{"accountId": "api", "project": "acme-cases"},
				Phase:      ir.PhaseInfrastructure,
				Owners:     []string{"api"},
			},
			{
				ID:         "api.cert",
				Type:       "gcp:certificatemanager/dnsAuthorization:DnsAuthorization",
				Properties: map[string]any{"domain": "api.cases.example.test", "name": "api"},
				Phase:      ir.PhaseExposure,
				Owners:     []string{"api"},
			},
			{
				ID:   "api.service",
				Type: "gcp:cloudrunv2/service:Service",
				Properties: map[string]any{
					"name":               ir.Concat{"api-", ir.Parameter("region")},
					"location":           ir.Parameter("region"),
					"deletionProtection": false,
					"description":        "costs $5, reads ${HOME} and $$",
					"labels": map[string]any{
						"looks-like-bool":   "true",
						"looks-like-number": "0123",
						"has-colon":         "a: b",
						"has-hash":          "# not a comment",
						"empty":             "",
					},
					"template": map[string]any{
						"maxInstanceRequestConcurrency": float64(80),
						"timeout":                       "300s",
						"scaling":                       map[string]any{"minInstanceCount": float64(0), "maxInstanceCount": float64(1e6)},
						"cpuFraction":                   0.25,
						"serviceAccount":                ir.Output{Resource: "api.account", Name: "email"},
						"containers": []any{map[string]any{
							"image": "us-east1-docker.pkg.dev/acme-cases/shop/api",
							"args":  []any{"--flag", "multi\nline"},
							"envs": []any{
								map[string]any{"name": "SELF", "value": ir.Concat{"https://api-", ir.Parameter("region"), ".", ir.Output{Resource: "api.cert", Name: "domain"}}},
								map[string]any{"name": "UNSET", "value": nil},
							},
						}},
					},
				},
				DependsOn: []string{"api.account"},
				Phase:     ir.PhaseRollout,
				Owners:    []string{"api"},
			},
		}},
	}
}
