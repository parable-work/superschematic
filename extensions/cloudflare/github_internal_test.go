package cloudflare

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// TestGhCLI runs the default GitHub over a stand-in gh on PATH: Ready asks
// whether it is signed in, EnsureEnvironment creates a missing environment
// and keeps one that exists, and SetSecret hands the value on stdin, never
// on the command line. Without gh on PATH, Ready says so.
func TestGhCLI(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the stand-in is a shell script")
	}
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := `#!/bin/sh
echo "$*" >> "` + log + `"
case "$1 $2" in
  "auth status") exit 0 ;;
  "api repos/acme/edge/environments/Staging") exit 0 ;;
  "api repos/acme/edge/environments/Preview") echo "gh: Not Found (HTTP 404)" >&2; exit 1 ;;
  "api --method") exit 0 ;;
  "secret set") echo "stdin: $(cat)" >> "` + log + `"; exit 0 ;;
esac
exit 2
`
	if err := os.WriteFile(filepath.Join(bin, "gh"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	ctx := context.Background()
	gh := ghCLI{}
	if err := gh.Ready(ctx); err != nil {
		t.Fatal(err)
	}
	for env, want := range map[string]bool{"Staging": false, "Preview": true} {
		created, err := gh.EnsureEnvironment(ctx, "acme/edge", env)
		if err != nil || created != want {
			t.Errorf("EnsureEnvironment(%s) = %v, %v; want %v", env, created, err, want)
		}
	}
	if err := gh.SetSecret(ctx, "acme/edge", "Staging", "CLOUDFLARE_API_TOKEN", []byte("the-token")); err != nil {
		t.Fatal(err)
	}
	if err := gh.SetSecret(ctx, "acme/edge", "", "CLOUDFLARE_PLANNER_TOKEN_STAGING", []byte("the-planner")); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(log)
	if err != nil {
		t.Fatal(err)
	}
	calls := string(data)
	for _, want := range []string{
		"auth status\n",
		"api repos/acme/edge/environments/Preview --silent\napi --method PUT repos/acme/edge/environments/Preview --silent\n",
		"secret set CLOUDFLARE_API_TOKEN --repo acme/edge --env Staging\nstdin: the-token\n",
		"secret set CLOUDFLARE_PLANNER_TOKEN_STAGING --repo acme/edge\nstdin: the-planner\n",
	} {
		if !strings.Contains(calls, want) {
			t.Errorf("gh was not called with %q:\n%s", want, calls)
		}
	}
	if strings.Count(calls, "environments/Staging") != 1 {
		t.Errorf("an existing environment was created again:\n%s", calls)
	}

	t.Setenv("PATH", t.TempDir())
	if err := gh.Ready(ctx); err == nil || !strings.Contains(err.Error(), "not on PATH") {
		t.Errorf("Ready without gh: %v", err)
	}
}
