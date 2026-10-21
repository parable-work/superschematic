package stackdeploy

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

// TestBuildsSeeNoCredential: the workspace's install, a site's build and
// a bundle's run the project's code, so the command that runs them gives
// them no credential the deploy holds: not gcp's credentials file or its
// gcloud override, not a Cloudflare token or the state's passphrase, and
// not the variables GitHub Actions mints an identity token with (D47,
// amended).
func TestBuildsSeeNoCredential(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	credentials := []string{
		"GOOGLE_APPLICATION_CREDENTIALS", "CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE", "GOOGLE_CLOUD_PROJECT",
		"CLOUDFLARE_API_TOKEN", "PULUMI_CONFIG_PASSPHRASE", "ACTIONS_ID_TOKEN_REQUEST_TOKEN", "ACTIONS_ID_TOKEN_REQUEST_URL",
	}
	for _, name := range credentials {
		t.Setenv(name, "secret-"+name)
	}
	var out bytes.Buffer
	if err := execCommand(context.Background(), t.TempDir(), &out, "sh", "-c", "env"); err != nil {
		t.Fatal(err)
	}
	for _, name := range credentials {
		if strings.Contains(out.String(), name+"=") {
			t.Errorf("the child reads %s", name)
		}
	}
	if !strings.Contains(out.String(), "PATH=") {
		t.Errorf("the child has no PATH:\n%s", out.String())
	}
}
