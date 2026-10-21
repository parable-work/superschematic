package testpaths

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// The rule for tests that need a runtime package of this checkout built
// (CONTRIBUTING.md, "Shared build outputs"): `go test ./...` runs each
// package's tests in its own process, in parallel, and they share the
// checkout's runtime/http/typescript (and the engine's package and addon).
// A test that ran `bun install` and `bun run build` there itself rebuilt
// dist/ and node_modules under another package's test reading them. Build
// such a package only through BuildTypeScriptPackage or BuildHTTPRuntime;
// never run its build or install directly from a test.

// stampFile records, in a package's node_modules, the inputs its last
// build through BuildTypeScriptPackage read and the dist/ it wrote; a bun
// install that starts node_modules over drops it.
const stampFile = ".superschematic-build-stamp"

// BuildHTTPRuntime builds the HTTP runtime's TypeScript package, whose
// dist/ a generated workspace installs the runtime from, as
// BuildTypeScriptPackage does, after checking superscalar's TypeScript
// binding, which its build links, is built.
func BuildHTTPRuntime(t testing.TB, bun string, paths naming.LocalPaths) {
	t.Helper()
	if _, err := os.Stat(filepath.Join(paths.ScalarTypeScript, "dist")); err != nil {
		RequireOrSkipTS(t, fmt.Sprintf("superscalar TypeScript binding not built: %v", err))
	}
	BuildTypeScriptPackage(t, bun, paths.HTTPRuntimeTypeScript)
}

// BuildTypeScriptPackage runs `bun install --frozen-lockfile` and `bun run
// build` in dir, a TypeScript package of this checkout other tests read,
// holding a lock on dir that other test processes take too, and does
// nothing when the package's sources, manifest and lock file are what its
// last build here read and its dist/ is still what that build wrote. So of
// the parallel packages of a `go test ./...` that need it, the first builds
// it and the rest wait for that build and reuse it, and none rebuilds it
// under another's feet. An install that fails (likely offline) skips the
// test, or fails it under SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1; a build that
// fails fails it.
func BuildTypeScriptPackage(t testing.TB, bun, dir string) {
	t.Helper()
	installFailed := false
	_, err := buildOnce(dir, func() error {
		install := exec.Command(bun, "install", "--frozen-lockfile")
		install.Dir = dir
		if out, err := install.CombinedOutput(); err != nil {
			installFailed = true
			return fmt.Errorf("bun install --frozen-lockfile in %s (likely offline): %v\n%s", dir, err, out)
		}
		build := exec.Command(bun, "run", "build")
		build.Dir = dir
		if out, err := build.CombinedOutput(); err != nil {
			return fmt.Errorf("bun run build in %s: %v\n%s", dir, err, out)
		}
		return nil
	})
	switch {
	case err == nil:
	case installFailed:
		RequireOrSkipTS(t, err.Error())
	default:
		t.Fatal(err)
	}
}

// buildOnce runs build under dir's cross-process lock unless dir's stamp
// says it is current, and stamps dir after a build that succeeds. It
// reports whether it ran build.
func buildOnce(dir string, build func() error) (bool, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return false, err
	}
	defer unlock()
	inputs, err := inputsDigest(dir)
	if err != nil {
		return false, err
	}
	stamp := filepath.Join(dir, "node_modules", stampFile)
	if recorded, err := os.ReadFile(stamp); err == nil {
		if output, err := distDigest(dir); err == nil && string(recorded) == inputs+"\n"+output+"\n" {
			return false, nil
		}
	}
	// A build that fails part way leaves no stamp to trust.
	_ = os.Remove(stamp)
	if err := build(); err != nil {
		return true, err
	}
	output, err := distDigest(dir)
	if err != nil {
		return true, fmt.Errorf("after building %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		return true, err
	}
	return true, os.WriteFile(stamp, []byte(inputs+"\n"+output+"\n"), 0o644)
}

// lockPath is dir's lock file: in the temporary directory, which every
// test process of a run shares, rather than in dir, whose node_modules a
// bun install may start over under the lock.
func lockPath(dir string) string {
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(os.TempDir(), "superschematic-build-"+hex.EncodeToString(sum[:8])+".lock")
}

// inputsDigest hashes what a package's build reads from it: its manifest,
// lock file, tsconfigs, and everything under src/ and scripts/.
func inputsDigest(dir string) (string, error) {
	h := sha256.New()
	files := []string{"package.json", "bun.lock"}
	configs, err := filepath.Glob(filepath.Join(dir, "tsconfig*.json"))
	if err != nil {
		return "", err
	}
	for _, c := range configs {
		files = append(files, filepath.Base(c))
	}
	for _, sub := range []string{"src", "scripts"} {
		err := filepath.WalkDir(filepath.Join(dir, sub), func(path string, d fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) && path == filepath.Join(dir, sub) {
				return filepath.SkipDir
			}
			if err != nil || d.IsDir() {
				return err
			}
			rel, err := filepath.Rel(dir, path)
			files = append(files, rel)
			return err
		})
		if err != nil {
			return "", err
		}
	}
	slices.Sort(files)
	for _, rel := range files {
		f, err := os.Open(filepath.Join(dir, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		_, err = io.Copy(h, f)
		f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// distDigest fingerprints dir's dist/ by each file's path, size and
// modification time, which any other build of the package (make ts, a
// developer's bun run build) changes. It is an error for dist/ to be
// missing or empty.
func distDigest(dir string) (string, error) {
	var lines []string
	root := filepath.Join(dir, "dist")
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return err
		}
		info, err := d.Info()
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, path)
		if err != nil {
			return err
		}
		lines = append(lines, fmt.Sprintf("%s %d %d", filepath.ToSlash(rel), info.Size(), info.ModTime().UnixNano()))
		return nil
	})
	if err != nil {
		return "", err
	}
	if len(lines) == 0 {
		return "", fmt.Errorf("%s is empty", root)
	}
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:]), nil
}
