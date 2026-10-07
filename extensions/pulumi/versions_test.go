package pulumi_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"

	"github.com/parable-work/superschematic/extensions/pulumi"
)

// TestCLIVersionMatchesSDK keeps the pulumi CLI that CI installs, the
// PULUMI_VERSION pin in tools.env, and the one a generated workflow
// installs, CLIVersion, at the version of the Pulumi Go SDK this module
// requires, so the Automation API drives the CLI it was released with.
func TestCLIVersionMatchesSDK(t *testing.T) {
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
		t.Errorf("tools.env pins the pulumi CLI at %s; go.mod requires the Pulumi SDK at %s", pin[1], sdk[1])
	}
	if pulumi.CLIVersion != string(sdk[1]) {
		t.Errorf("pulumi.CLIVersion is %s; go.mod requires the Pulumi SDK at %s", pulumi.CLIVersion, sdk[1])
	}
}
