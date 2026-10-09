package gcp_test

import (
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/gcp"
	"github.com/parable-work/superschematic/extensions/gcp/schemas"
)

// TestPin checks that the pin is at ProviderVersion, records a digest for
// each upstream file, and that every pinned type has its file and every
// file is a pinned type's.
func TestPin(t *testing.T) {
	pin, err := schemas.ReadPin()
	if err != nil {
		t.Fatal(err)
	}
	if pin.Version != gcp.ProviderVersion {
		t.Errorf("%s pins %s, but gcp.ProviderVersion is %s", schemas.PinFile, pin.Version, gcp.ProviderVersion)
	}
	for _, src := range pin.Sources {
		if len(src.SHA256) != 64 || !strings.Contains(src.URL, "/v"+pin.Version+"/") {
			t.Errorf("source %s: %s at %s does not pin version %s by digest", src.Name, src.SHA256, src.URL, pin.Version)
		}
	}
	if !sort.StringsAreSorted(pin.Types) {
		t.Errorf("%s lists its types out of order", schemas.PinFile)
	}
	files, err := filepath.Glob("schemas/*.json")
	if err != nil {
		t.Fatal(err)
	}
	want := []string{schemas.PinFile}
	for _, token := range pin.Types {
		s, err := schemas.Load(token)
		if err != nil {
			t.Error(err)
			continue
		}
		if _, err := s.JSONSchema(); err != nil {
			t.Error(err)
		}
		if !strings.HasPrefix(s.Terraform.Type, "google_") {
			t.Errorf("%s has Terraform type %q", token, s.Terraform.Type)
		}
		want = append(want, schemas.FileName(token))
	}
	var got []string
	for _, f := range files {
		got = append(got, filepath.Base(f))
	}
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("schemas/ holds %v, want %v", got, want)
	}
}

// TestRenames spot-checks the Terraform names section 6.4 records: a
// property in snake case, and a list the bridge pluralized, at the top
// level and nested.
func TestRenames(t *testing.T) {
	cases := map[string]map[string]string{
		gcp.TypeService: {
			"invokerIamDisabled":                      "invoker_iam_disabled",
			"traffics":                                "traffic",
			"template.containers.envs":                "env",
			"template.containers.ports.containerPort": "container_port",
			"template.vpcAccess.networkInterfaces":    "network_interfaces",
		},
		gcp.TypeBackendService: {"backends": "backend"},
		gcp.TypeInstance:       {"settings.ipConfiguration.authorizedNetworks": "authorized_networks"},
	}
	terraform := map[string]string{
		gcp.TypeService:        "google_cloud_run_v2_service",
		gcp.TypeBackendService: "google_compute_backend_service",
		gcp.TypeInstance:       "google_sql_database_instance",
	}
	for token, renames := range cases {
		s, err := schemas.Load(token)
		if err != nil {
			t.Fatal(err)
		}
		if s.Terraform.Type != terraform[token] {
			t.Errorf("%s is %s in Terraform, want %s", token, s.Terraform.Type, terraform[token])
		}
		for path, want := range renames {
			if got := s.Terraform.Renames[path]; got != want {
				t.Errorf("%s renames %s to %q, want %q", token, path, got, want)
			}
		}
		if _, ok := s.Terraform.Renames["name"]; ok {
			t.Errorf("%s renames name, which is the same in Terraform", token)
		}
	}
}

// TestNoDeprecatedProperties walks every property every node of the
// golden environments sets, through the pinned schema of its type, and
// refuses one the provider deprecates. Resolution already refused one the
// schema does not have.
func TestNoDeprecatedProperties(t *testing.T) {
	reg := assemble(t)
	var envs []*ir.ResolvedEnvironment
	for _, name := range []string{"Staging", "Production", "Preview"} {
		envs = append(envs, resolve(t, reg, shop(), stacktest.WithoutBuckets(stacktest.AcmeShop()), name))
	}
	checkNoDeprecated(t, "", envs)
}

// checkNoDeprecated walks every property every node of envs sets through
// the pinned schema of its type.
func checkNoDeprecated(t *testing.T, _ string, envs []*ir.ResolvedEnvironment) {
	t.Helper()
	for _, env := range envs {
		name := env.Environment
		for _, res := range env.Resources.Resources {
			s, err := schemas.Load(res.Type)
			if err != nil {
				t.Fatal(err)
			}
			var walk func(path []string, v any)
			walk = func(path []string, v any) {
				if len(path) > 0 {
					p := s.Property(path...)
					if p == nil {
						t.Errorf("%s: %s sets %s, which %s does not have", name, res.ID, strings.Join(path, "."), res.Type)
						return
					}
					if p.DeprecationMessage != "" {
						t.Errorf("%s: %s sets %s, which is deprecated: %s", name, res.ID, strings.Join(path, "."), p.DeprecationMessage)
					}
					if p.Type == "object" {
						return // a map: its keys are not properties
					}
				}
				switch v := v.(type) {
				case map[string]any:
					for key, inner := range v {
						walk(append(slices.Clone(path), key), inner)
					}
				case []any:
					for _, inner := range v {
						walk(path, inner)
					}
				}
			}
			walk(nil, res.Properties)
		}
	}
}
