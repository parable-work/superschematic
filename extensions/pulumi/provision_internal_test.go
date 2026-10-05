package pulumi

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/blang/semver"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
)

// noCLI is a PulumiCommand that runs nothing: the settings file is the
// workspace's, and reading or writing it needs no CLI.
type noCLI struct{}

func (noCLI) Run(context.Context, string, io.Reader, []io.Writer, []io.Writer, []string, ...string) (string, string, int, error) {
	return "", "", 1, errors.New("this test runs no pulumi command")
}

func (noCLI) Version() semver.Version { return semver.MustParse("3.259.0") }

// TestKeepSecretsProvider covers the settings file of a stack whose secrets
// provider is a cloud key, which no test can reach without credentials.
func TestKeepSecretsProvider(t *testing.T) {
	const kms = "gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/k"
	ctx := context.Background()
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ProgramFile), []byte("name: shop\nruntime: yaml\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ws, err := auto.NewLocalWorkspace(ctx, auto.WorkDir(dir), auto.Pulumi(noCLI{}))
	if err != nil {
		t.Fatal(err)
	}
	r := &run{ws: ws, name: "staging"}
	path := filepath.Join(dir, "Pulumi.staging.yaml")
	read := func() string {
		t.Helper()
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		return string(data)
	}
	write := func(content string) {
		t.Helper()
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	// The passphrase provider needs no settings file.
	if err := r.keepSecretsProvider(ctx, PassphraseProvider); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(path); !errors.Is(err, os.ErrNotExist) {
		t.Errorf("the passphrase provider wrote %s: %v", path, err)
	}

	// A fresh checkout has no settings file, and gets the request's
	// provider.
	if err := r.keepSecretsProvider(ctx, kms); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "secretsprovider: "+kms) {
		t.Errorf("settings of a fresh checkout:\n%s", got)
	}

	// A file that names a provider keeps it, and its key.
	const named = "secretsprovider: gcpkms://projects/p/locations/l/keyRings/r/cryptoKeys/other\nencryptedkey: a2V5\n"
	write(named)
	if err := r.keepSecretsProvider(ctx, kms); err != nil {
		t.Fatal(err)
	}
	if got := read(); got != named {
		t.Errorf("a settings file that names a provider changed:\n%s", got)
	}

	// A file that names none gets the provider and keeps its config.
	write("config:\n  shop:pr: \"7\"\n")
	if err := r.keepSecretsProvider(ctx, kms); err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "secretsprovider: "+kms) || !strings.Contains(got, `shop:pr: "7"`) {
		t.Errorf("settings with config and no provider:\n%s", got)
	}

	// A file that does not load is an error, and stays as it is.
	write("secretsprovider: [\n")
	if err := r.keepSecretsProvider(ctx, kms); err == nil {
		t.Error("a settings file that does not load was accepted")
	}
	if got := read(); got != "secretsprovider: [\n" {
		t.Errorf("a settings file that does not load was overwritten:\n%s", got)
	}
}
