package local

import (
	"context"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// TestRunSetsPWDToTheDirectory: a command run in a directory reached
// through a symlink, with an environment that carries the provisioner's
// own PWD, reads that directory, as written, from PWD, as a shell's cd
// leaves it, so `go build` resolves a module's relative replaces from it.
func TestRunSetsPWDToTheDirectory(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("the test runs sh")
	}
	real := filepath.Join(t.TempDir(), "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	for name, c := range map[string]Command{
		"an environment": {Env: append(os.Environ(), "PWD=/elsewhere")},
		"no environment": {},
	} {
		t.Run(name, func(t *testing.T) {
			c.Path, c.Args, c.Dir = "/bin/sh", []string{"-c", `printf %s "$PWD"`}, link
			out, err := execRunner{}.Run(context.Background(), c)
			if err != nil {
				t.Fatal(err)
			}
			if string(out) != link {
				t.Errorf("PWD = %q, want %q", out, link)
			}
		})
	}
}
