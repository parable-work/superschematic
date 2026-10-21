package childenv

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
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
		"CF_API_TOKEN=cf",
		"WRANGLER_OUTPUT_FILE_PATH=/w",
		"GCLOUD_PROJECT=acme",
		"SSH_AUTH_SOCK=/tmp/agent",
		"KUBECONFIG=/k",
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
	for _, name := range []string{"CF_API_TOKEN", "WRANGLER_LOG", "GCLOUD_PROJECT", "GCP_PROJECT", "SSH_AUTH_SOCK", "DOCKER_AUTH_CONFIG", "KUBECONFIG", "VAULT_TOKEN", "SUPERSCHEMATIC_MIGRATE_TOKEN", "GOOGLE_APPLICATION_CREDENTIALS", "google_cloud_project", "CLOUDSDK_CONFIG", "CLOUDFLARE_ACCOUNT_ID", "PULUMI_ACCESS_TOKEN", "AWS_PROFILE", "ACTIONS_CACHE_URL", "GH_TOKEN", "PULUMI_CONFIG_PASSPHRASE", "SUPERSCHEMATIC_BOOTSTRAP_PASSPHRASE"} {
		if !Denied(name) {
			t.Errorf("Denied(%s) = false", name)
		}
	}
	for _, name := range []string{"PATH", "NPM_TOKEN", "HOME", PassVariable} {
		if Denied(name) {
			t.Errorf("Denied(%s) = true", name)
		}
	}
}

// TestWithHome: a child's home is the scratch directory, and so are the
// configuration and data directories under it, where gcloud, Pulumi,
// wrangler and npm keep their credentials; the caches stay the user's.
func TestWithHome(t *testing.T) {
	env := []string{"PATH=/usr/bin", "HOME=/home/dev", "XDG_CONFIG_HOME=/home/dev/.config", "BUN_INSTALL=/opt/bun"}
	got := WithHome(env, "/scratch", "/home/dev")
	for _, want := range []string{
		"PATH=/usr/bin", "HOME=/scratch", "XDG_CONFIG_HOME=" + filepath.Join("/scratch", ".config"),
		"XDG_DATA_HOME=" + filepath.Join("/scratch", ".local", "share"),
		"XDG_CACHE_HOME=" + filepath.Join("/home/dev", ".cache"),
		"BUN_INSTALL_CACHE_DIR=" + filepath.Join("/opt/bun", "install", "cache"),
	} {
		if !slices.Contains(got, want) {
			t.Errorf("WithHome = %q, lacks %s", got, want)
		}
	}
	for _, kv := range got {
		if strings.HasPrefix(kv, "HOME=/home/dev") || strings.HasPrefix(kv, "XDG_CONFIG_HOME=/home/dev") {
			t.Errorf("WithHome keeps %s", kv)
		}
	}
	kept := WithHome([]string{"XDG_CACHE_HOME=/c", "BUN_INSTALL_CACHE_DIR=/b"}, "/scratch", "/home/dev")
	if !slices.Contains(kept, "XDG_CACHE_HOME=/c") || !slices.Contains(kept, "BUN_INSTALL_CACHE_DIR=/b") {
		t.Errorf("WithHome replaced the caches the user set: %q", kept)
	}
}

// TestEnvironMakesAndRemovesAScratchHome: Environ's child gets a new,
// empty home, which its cleanup removes; naming HOME in PassVariable keeps
// the user's.
func TestEnvironMakesAndRemovesAScratchHome(t *testing.T) {
	real := t.TempDir()
	t.Setenv("HOME", real)
	t.Setenv(PassVariable, "")
	env, cleanup, err := Environ()
	if err != nil {
		t.Fatal(err)
	}
	var home string
	for _, kv := range env {
		if v, ok := strings.CutPrefix(kv, "HOME="); ok {
			home = v
		}
	}
	if home == "" || home == real {
		t.Fatalf("the child's home is %q", home)
	}
	if entries, err := os.ReadDir(home); err != nil || len(entries) != 0 {
		t.Errorf("the scratch home holds %v: %v", entries, err)
	}
	cleanup()
	if _, err := os.Stat(home); !os.IsNotExist(err) {
		t.Errorf("cleanup left %s: %v", home, err)
	}

	t.Setenv(PassVariable, "HOME")
	env, cleanup, err = Environ()
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	if !slices.Contains(env, "HOME="+real) {
		t.Errorf("with HOME passed, the child's environment is %q", env)
	}
}
