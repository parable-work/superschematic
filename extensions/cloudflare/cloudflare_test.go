package cloudflare_test

import (
	"bytes"
	"encoding/json"
	"errors"
	"flag"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	ir "github.com/parable-work/superschematic/ir"
	"github.com/parable-work/superschematic/registry"
	"github.com/parable-work/superschematic/stack"
	"github.com/parable-work/superschematic/stack/stacktest"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/cloudflare/schemas"
)

var update = flag.Bool("update", false, "rewrite the golden environment.json files")

// goldenRoot is the output root the golden files live under, laid out as
// a build writes them: stack/<stack>/<environment>/environment.json.
const goldenRoot = "testdata/golden"

// zoneID is the identifier of the fixture's zone, acme.dev.
const zoneID = "0123456789abcdef0123456789abcdef"

func assemble(t *testing.T) *registry.Registry {
	t.Helper()
	reg, err := registry.Assemble(registry.DefaultNaming(), &stacktest.Extension{}, cloudflare.Extension{})
	if err != nil {
		t.Fatalf("assemble: %v", err)
	}
	return reg
}

// shop is the stack/stacktest shop stack with its domains on Cloudflare,
// in zone acme.dev: Staging's records are DNS-only, Production's are
// proxied, and Preview extends Staging with a parameter, so its host's
// name references the parameter. The stack leaves shop-orders' job out,
// which has no records.
func shop() *ir.Stack {
	s := stacktest.WithoutJobSettings(stacktest.Shop())
	for _, env := range s.Environments {
		switch env.Name {
		case "Staging":
			env.DNS = &ir.DNSPlacement{Platform: cloudflare.DNS, Values: map[string]any{"zone": "acme.dev", "zoneId": zoneID}}
		case "Production":
			env.DNS = &ir.DNSPlacement{Platform: cloudflare.DNS, Values: map[string]any{"zone": "acme.dev", "zoneId": zoneID, "proxied": true}}
		}
	}
	return s
}

func resolve(t *testing.T, reg *registry.Registry, s *ir.Stack, env string) *ir.ResolvedEnvironment {
	t.Helper()
	resolved, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.WithoutJobs(stacktest.AcmeShop()), Environment: env})
	if err != nil {
		t.Fatal(err)
	}
	return resolved
}

// TestGolden resolves the shop stack in each environment, its records on
// Cloudflare, and checks the environment.json it writes, resource graph
// included, against the golden file. Resolution validated every record
// against the pinned schema of its type on the way.
func TestGolden(t *testing.T) {
	reg := assemble(t)
	s := shop()
	out := t.TempDir()
	for _, env := range s.Environments {
		t.Run(env.Name, func(t *testing.T) {
			resolved := resolve(t, reg, s, env.Name)
			path, err := stack.Write(out, resolved)
			if err != nil {
				t.Fatal(err)
			}
			got, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			golden := stack.EnvironmentPath(goldenRoot, s.Name, env.Name)
			if *update {
				if err := os.MkdirAll(filepath.Dir(golden), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(golden, got, 0o644); err != nil {
					t.Fatal(err)
				}
			}
			want, err := os.ReadFile(golden)
			if err != nil {
				t.Fatalf("%v (run with -update to write it)", err)
			}
			if !bytes.Equal(got, want) {
				t.Errorf("%s differs from %s; run with -update and review the diff", path, golden)
			}
			back, err := stack.Unmarshal(got)
			if err != nil {
				t.Fatal(err)
			}
			again, err := stack.Marshal(back)
			if err != nil {
				t.Fatal(err)
			}
			if !bytes.Equal(again, got) {
				t.Error("environment.json does not round-trip")
			}
		})
	}
}

// TestRecords reads the record each environment writes: DNS-only in
// Staging, proxied with the automatic TTL in Production, and named after
// the parameter in Preview, every one a pinned DnsRecord in the zone.
func TestRecords(t *testing.T) {
	reg := assemble(t)
	for _, tc := range []struct {
		env     string
		name    string
		proxied bool
		ttl     float64
	}{
		{"Staging", `"shop-api.staging.acme.dev"`, false, 300},
		{"Production", `"shop-api.acme.dev"`, true, 1},
		{"Preview", `{"$concat":["shop-api-",{"$parameter":"pr"},".staging.acme.dev"]}`, false, 300},
	} {
		env := resolve(t, reg, shop(), tc.env)
		var records []*ir.Resource
		for _, res := range env.Resources.Resources {
			if slices.Contains(res.Owners, "dns") {
				records = append(records, res)
			}
		}
		if len(records) != 1 || records[0].ID != "dns.shop-api.cname" {
			t.Fatalf("%s: DNS nodes = %v, want dns.shop-api.cname", tc.env, records)
		}
		rec := records[0]
		p := rec.Properties
		if rec.Type != cloudflare.TypeRecord || rec.Phase != ir.PhaseExposure || p["zoneId"] != zoneID || p["type"] != "CNAME" {
			t.Errorf("%s: record = %+v", tc.env, rec)
		}
		if got := mustJSON(t, p["name"]); got != tc.name {
			t.Errorf("%s: name = %s, want %s", tc.env, got, tc.name)
		}
		if p["proxied"] != tc.proxied || p["ttl"] != tc.ttl {
			t.Errorf("%s: proxied %v ttl %v, want %v %v", tc.env, p["proxied"], p["ttl"], tc.proxied, tc.ttl)
		}
		if got := mustJSON(t, p["content"]); got != `{"$output":{"resource":"shop-api.route","name":"target"}}` {
			t.Errorf("%s: content = %s", tc.env, got)
		}
		if !slices.Equal(rec.DependsOn, []string{"shop-api.route"}) {
			t.Errorf("%s: dependsOn = %v", tc.env, rec.DependsOn)
		}
	}
}

// TestCredentials checks the token each environment's records need: one
// secret per stack and zone, which the provider reads from
// CLOUDFLARE_API_TOKEN, and no value anywhere in environment.json.
func TestCredentials(t *testing.T) {
	reg := assemble(t)
	for _, name := range []string{"Staging", "Production", "Preview"} {
		env := resolve(t, reg, shop(), name)
		want := `[{"secret":"shop-stack-cloudflare-dns-acme_dev","env":"CLOUDFLARE_API_TOKEN","description":"a Cloudflare API token with the DNS Edit permission on zone acme.dev, and no other"}]`
		if got := mustJSON(t, env.DNS.Credentials); got != want {
			t.Errorf("%s: credentials = %s, want %s", name, got, want)
		}
	}
	for stack, zone := range map[string]string{"shop-stack-cloudflare-dns-acme_dev": "acme.dev", "shop-stack-cloudflare-dns-a-b_dev": "a-b.dev", "shop-stack-cloudflare-dns-a_b_dev": "a.b.dev"} {
		if got := cloudflare.TokenSecret("shop-stack", zone); got != stack {
			t.Errorf("TokenSecret(shop-stack, %s) = %s, want %s", zone, got, stack)
		}
	}
}

// TestRefusedValues: a missing zone or zone id, an unknown key and a
// malformed value fail resolution with the DNS platform's name and the
// value at fault.
func TestRefusedValues(t *testing.T) {
	reg := assemble(t)
	for _, tc := range []struct {
		name   string
		values map[string]any
		want   string
	}{
		{"no zone", map[string]any{"zoneId": zoneID}, "missing property 'zone'"},
		{"no zone id", map[string]any{"zone": "acme.dev"}, "missing property 'zoneId'"},
		{"unknown key", map[string]any{"zone": "acme.dev", "zoneId": zoneID, "zone_id": zoneID}, "additional properties 'zone_id' not allowed"},
		{"zone id of another shape", map[string]any{"zone": "acme.dev", "zoneId": "acme"}, "zoneId"},
		{"zone with a capital", map[string]any{"zone": "Acme.dev", "zoneId": zoneID}, "zone"},
		{"proxied not a boolean", map[string]any{"zone": "acme.dev", "zoneId": zoneID, "proxied": "yes"}, "proxied"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			s := shop()
			s.Environments[0].DNS.Values = tc.values
			_, err := stack.Resolve(reg, stack.Input{Stack: s, Services: stacktest.WithoutJobs(stacktest.AcmeShop()), Environment: "Staging"})
			var errs *stack.Errors
			if !errors.As(err, &errs) {
				t.Fatalf("Resolve = %v, want a resolution failure", err)
			}
			for _, e := range errs.Of(stack.CodeInvalidValues) {
				if strings.Contains(e.Message, "values for DNS platform cloudflare") && strings.Contains(e.Message, tc.want) {
					return
				}
			}
			t.Errorf("no invalid-values failure for DNS platform cloudflare mentions %q:\n%v", tc.want, errs)
		})
	}
}

// TestDomainOutsideTheZone: a domain the zone does not hold fails before
// any record is written into it.
func TestDomainOutsideTheZone(t *testing.T) {
	s := shop()
	s.Environments[0].DNS.Values["zone"] = "acme.com"
	_, err := stack.Resolve(assemble(t), stack.Input{Stack: s, Services: stacktest.WithoutJobs(stacktest.AcmeShop()), Environment: "Staging"})
	if err == nil || !strings.Contains(err.Error(), "DNS platform cloudflare: environment Staging has domain staging.acme.dev, which is not in zone acme.com") {
		t.Errorf("Resolve = %v, want the domain refused", err)
	}
}

// TestPublicImportsOnly holds the extension to the D10 promise: it imports
// the public packages and the IR, never an internal package of the core.
func TestPublicImportsOnly(t *testing.T) {
	var files []string
	for _, pattern := range []string{"*.go", "schemas/*.go", "internal/tools/providerschemas/*.go"} {
		more, err := filepath.Glob(pattern)
		if err != nil {
			t.Fatal(err)
		}
		files = append(files, more...)
	}
	for _, file := range files {
		if strings.HasSuffix(file, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(token.NewFileSet(), file, nil, parser.ImportsOnly)
		if err != nil {
			t.Fatal(err)
		}
		for _, imp := range f.Imports {
			path, _ := strconv.Unquote(imp.Path.Value)
			if strings.HasPrefix(path, "github.com/parable-work/superschematic/internal") {
				t.Errorf("%s imports %s; an extension uses the public packages only", file, path)
			}
		}
	}
}

// TestProviderVersion keeps ProviderVersion at the pin, which Register
// refuses to load otherwise.
func TestProviderVersion(t *testing.T) {
	pin, err := schemas.ReadPin()
	if err != nil {
		t.Fatal(err)
	}
	if pin.Version != cloudflare.ProviderVersion || pin.Package != cloudflare.Package {
		t.Errorf("%s pins pulumi-%s %s, but the extension applies pulumi-%s %s", schemas.PinFile, pin.Package, pin.Version, cloudflare.Package, cloudflare.ProviderVersion)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	data, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}
