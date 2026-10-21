package childenv

import (
	"slices"
	"testing"
)

func TestFilterKeepsTheAllowlistAndNoCredential(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"HOME=/home/dev",
		"LC_ALL=C",
		"HTTPS_PROXY=http://proxy:3128",
		"BUN_INSTALL_CACHE_DIR=/cache/bun",
		"GOOGLE_APPLICATION_CREDENTIALS=/work/gha-creds-1.json",
		"CLOUDSDK_AUTH_CREDENTIAL_FILE_OVERRIDE=/work/gha-creds-1.json",
		"CLOUDSDK_CORE_PROJECT=acme",
		"CLOUDFLARE_API_TOKEN=cf",
		"PULUMI_CONFIG_PASSPHRASE=pass",
		"SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE=boot",
		"ACTIONS_ID_TOKEN_REQUEST_TOKEN=oidc",
		"ACTIONS_ID_TOKEN_REQUEST_URL=https://token",
		"ACTIONS_RUNTIME_TOKEN=runtime",
		"AWS_SECRET_ACCESS_KEY=aws",
		"GITHUB_TOKEN=gh",
		"NPM_TOKEN=npm",
		"SOMETHING_ELSE=x",
		"NOEQUALS",
	}
	got := Filter(env)
	want := []string{"PATH=/usr/bin", "HOME=/home/dev", "LC_ALL=C", "HTTPS_PROXY=http://proxy:3128", "BUN_INSTALL_CACHE_DIR=/cache/bun"}
	if !slices.Equal(got, want) {
		t.Errorf("Filter = %q, want %q", got, want)
	}
}

func TestPassVariableAddsNamesButNoCredential(t *testing.T) {
	env := []string{
		"PATH=/usr/bin",
		"NPM_TOKEN=npm",
		"CLOUDFLARE_API_TOKEN=cf",
		"GITHUB_TOKEN=gh",
		"STATE_PASSPHRASE=p",
		PassVariable + "=NPM_TOKEN, CLOUDFLARE_API_TOKEN,GITHUB_TOKEN STATE_PASSPHRASE",
	}
	got := Filter(env)
	want := []string{"PATH=/usr/bin", "NPM_TOKEN=npm"}
	if !slices.Equal(got, want) {
		t.Errorf("Filter = %q, want %q", got, want)
	}
}

func TestDenied(t *testing.T) {
	for _, name := range []string{"GOOGLE_APPLICATION_CREDENTIALS", "google_cloud_project", "CLOUDSDK_CONFIG", "CLOUDFLARE_ACCOUNT_ID", "PULUMI_ACCESS_TOKEN", "AWS_PROFILE", "ACTIONS_CACHE_URL", "GH_TOKEN", "PULUMI_CONFIG_PASSPHRASE", "SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE"} {
		if !Denied(name) {
			t.Errorf("Denied(%s) = false", name)
		}
	}
	for _, name := range []string{"PATH", "NPM_TOKEN", "HOME"} {
		if Denied(name) {
			t.Errorf("Denied(%s) = true", name)
		}
	}
}
