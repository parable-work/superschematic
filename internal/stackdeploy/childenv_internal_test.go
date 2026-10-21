package stackdeploy

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/childenv"
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

// TestBuildsRunInAScratchHome: the install and the builds run with a home
// of their own, so a script looking under the home for the credentials
// gcloud, Pulumi, wrangler or npm keep there finds none of them, and Bun
// still installs a workspace in it.
func TestBuildsRunInAScratchHome(t *testing.T) {
	bun, err := exec.LookPath("bun")
	if err != nil {
		t.Skip("bun is not on PATH")
	}
	real := t.TempDir()
	if err := os.MkdirAll(filepath.Join(real, ".config", "gcloud"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(real, ".config", "gcloud", "application_default_credentials.json"), []byte("{}"), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", real)
	t.Setenv("XDG_CONFIG_HOME", "")
	// Bun's own cache stays where it was, which a real install needs.
	t.Setenv("BUN_INSTALL_CACHE_DIR", filepath.Join(t.TempDir(), "bun-cache"))
	t.Setenv(childenv.PassVariable, "")

	dir := t.TempDir()
	for path, content := range map[string]string{
		"package.json":     `{"name": "workspace", "private": true, "dependencies": {"local-dep": "file:./dep"}}`,
		"dep/package.json": `{"name": "local-dep", "version": "1.0.0", "main": "index.js"}`,
		"dep/index.js":     "module.exports = 1;\n",
	} {
		if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, path)), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, path), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	var out bytes.Buffer
	if err := execCommand(context.Background(), dir, &out, bun, "install"); err != nil {
		t.Fatalf("bun install in a scratch home: %v\n%s", err, out.String())
	}
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "local-dep", "index.js")); err != nil {
		t.Errorf("bun install linked no local-dep: %v\n%s", err, out.String())
	}
	out.Reset()
	script := `const os = require("os"), fs = require("fs"), path = require("path");` +
		`console.log("home=" + os.homedir());` +
		`console.log("gcloud=" + fs.existsSync(path.join(os.homedir(), ".config", "gcloud", "application_default_credentials.json")));`
	if err := execCommand(context.Background(), dir, &out, bun, "-e", script); err != nil {
		t.Fatalf("%v\n%s", err, out.String())
	}
	if strings.Contains(out.String(), "home="+real+"\n") || !strings.Contains(out.String(), "gcloud=false") {
		t.Errorf("the child's home is the user's, or holds gcloud's credentials:\n%s", out.String())
	}
}
