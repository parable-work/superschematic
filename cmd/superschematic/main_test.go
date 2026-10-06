package main

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parable-work/superschematic/registry"

	"github.com/parable-work/superschematic/extensions/cloudflare"
	"github.com/parable-work/superschematic/extensions/gcp"
	"github.com/parable-work/superschematic/extensions/pulumi"
)

// TestEveryTargetApplies holds the binary to what section 13 of
// docs/stack-model.md promises: it links the official targets and DNS
// platforms, and the provisioner each target names is linked too. The
// pulumi provisioner pins the gcp provider at the release whose schemas the
// gcp target checks its resources against, and the cloudflare provider at
// the release the Cloudflare DNS platform checks its records against, so
// that what resolution validated is what applies.
func TestEveryTargetApplies(t *testing.T) {
	reg, err := registry.Assemble(registry.DefaultNaming(), extensions()...)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := reg.Target(gcp.Target); !ok {
		t.Errorf("the binary does not register the %s target (targets: %v)", gcp.Target, reg.Targets())
	}
	if _, ok := reg.DNSPlatform(cloudflare.DNS); !ok {
		t.Errorf("the binary does not register the %s DNS platform (DNS platforms: %v)", cloudflare.DNS, reg.DNSPlatforms())
	}
	for _, name := range reg.Targets() {
		target, _ := reg.Target(name)
		if target.Provisioner == "" {
			continue
		}
		if _, ok := reg.Provisioner(target.Provisioner); !ok {
			t.Errorf("target %s names provisioner %q, which the binary does not link (provisioners: %v)",
				name, target.Provisioner, reg.Provisioners())
		}
	}

	spec, ok := reg.Provisioner(pulumi.Name)
	if !ok {
		t.Fatalf("the binary does not link the %s provisioner", pulumi.Name)
	}
	p, ok := spec.Provisioner.(*pulumi.Provisioner)
	if !ok {
		t.Fatalf("provisioner %s is a %T, not the pulumi extension's", pulumi.Name, spec.Provisioner)
	}
	if got := p.ProviderVersions["gcp"]; got != gcp.ProviderVersion {
		t.Errorf("pulumi pins the gcp provider at %q; the gcp target's schemas are pulumi-gcp %s", got, gcp.ProviderVersion)
	}
	if got := p.ProviderVersions[cloudflare.Package]; got != cloudflare.ProviderVersion {
		t.Errorf("pulumi pins the %s provider at %q; the Cloudflare DNS platform's schemas are pulumi-cloudflare %s",
			cloudflare.Package, got, cloudflare.ProviderVersion)
	}
}

// TestPulumiSDKMatchesCLIPin keeps the Pulumi Go SDK this binary links at
// PULUMI_VERSION in tools.env, the pulumi CLI that CI installs, as
// extensions/pulumi's own test does for that module: the Automation API
// drives the CLI it was released with.
func TestPulumiSDKMatchesCLIPin(t *testing.T) {
	tools, err := os.ReadFile(filepath.Join("..", "..", "tools.env"))
	if err != nil {
		t.Fatal(err)
	}
	mod, err := os.ReadFile("go.mod")
	if err != nil {
		t.Fatal(err)
	}
	pin := regexp.MustCompile(`(?m)^PULUMI_VERSION=(\S+)$`).FindSubmatch(tools)
	sdk := regexp.MustCompile(`github\.com/pulumi/pulumi/sdk/v3 v(\S+)`).FindSubmatch(mod)
	if pin == nil || sdk == nil {
		t.Fatalf("tools.env PULUMI_VERSION %q, go.mod Pulumi SDK %q", pin, sdk)
	}
	if string(pin[1]) != string(sdk[1]) {
		t.Errorf("tools.env pins the pulumi CLI at %s; the binary links the Pulumi SDK at %s", pin[1], sdk[1])
	}
}
