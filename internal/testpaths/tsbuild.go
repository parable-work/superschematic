package testpaths

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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

// stampFile records, in a package's node_modules, what its last build
// through BuildTypeScriptPackage read and what it left (see buildOnce); a
// bun install that starts node_modules over drops it.
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

// rulesSources are what the behaviors' core's npm package
// (runtime/rules/typescript) builds its wasm module from, by a path
// relative to the package: the crates scripts/build-wasm.mjs compiles,
// the declarations the behaviors' crate reads in place, and the
// workspace's lock file (D103).
var rulesSources = []string{
	"../../engine/rust/Cargo.lock",
	"../../engine/rust/Cargo.toml",
	"../../engine/rust/rules/Cargo.toml",
	"../../engine/rust/rules/src",
	"../../engine/rust/rules-behaviors/Cargo.toml",
	"../../engine/rust/rules-behaviors/src",
	"../../engine/rust/rules-wasm/Cargo.toml",
	"../../engine/rust/rules-wasm/src",
	"../../engine/rust/behaviors/declarations",
}

// BuildRulesPackage builds the behaviors' core's npm package
// (paths.RulesTypeScript), whose dist/ a generated orm.ts of a table whose
// fields are behaviors imports its core from (D103), as
// BuildTypeScriptPackage does, reading the Rust sources its wasm module is
// built from (rulesSources) among its inputs, so a change to the core or
// to a behavior rebuilds it.
func BuildRulesPackage(t testing.TB, bun string, paths naming.LocalPaths) {
	t.Helper()
	buildPackage(t, bun, paths.RulesTypeScript, buildInputs{sources: rulesSources})
}

// BuildTypeScriptPackage runs `bun install --frozen-lockfile` and `bun run
// build` in dir, a TypeScript package of this checkout other tests read,
// holding a lock on dir that other test processes take too. It does
// nothing when the build would read what the last build here read and the
// package still holds what that build left (buildOnce lists both). So of
// the parallel packages of a `go test ./...` that need it, the first builds
// it and the rest wait for that build and reuse it, and none rebuilds it
// under another's feet. An install that fails (likely offline) skips the
// test, or fails it under SUPERSCHEMATIC_REQUIRE_TS_CHECKS=1; a build that
// fails fails it.
//
// copies names each package of this checkout whose dist/ dir's build
// copies into its node_modules, as the engine's link-local-deps copies the
// HTTP runtime's: a rebuild of one makes dir's next build run. A package
// linked by a symlink needs no entry, and one copied but not named here
// goes stale when it is rebuilt.
func BuildTypeScriptPackage(t testing.TB, bun, dir string, copies ...string) {
	t.Helper()
	buildPackage(t, bun, dir, buildInputs{copies: copies})
}

// buildPackage is BuildTypeScriptPackage with in, the inputs a build reads
// besides the package's own files; it fills in.tools.
func buildPackage(t testing.TB, bun, dir string, in buildInputs) {
	t.Helper()
	installFailed := false
	in.tools = toolVersions(bun)
	_, err := buildOnce(dir, in, func() error {
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

// toolVersions is bun's and node's versions, which run the build: bun the
// install and the script, node the scripts it calls and tsc.
func toolVersions(bun string) string {
	version := func(name string, args ...string) string {
		out, err := exec.Command(name, args...).Output()
		if err != nil {
			return name + ": " + err.Error()
		}
		return name + " " + strings.TrimSpace(string(out))
	}
	return version(bun, "--version") + "\n" + version("node", "--version")
}

// buildInputs is what a build reads besides the package's own files.
type buildInputs struct {
	// tools names the versions of the tools that run the build.
	tools string
	// copies are the packages whose dist/ the build copies in.
	copies []string
	// sources are files and directories outside the package that its build
	// compiles, by a path relative to it, each hashed by its files'
	// contents.
	sources []string
}

// buildOnce runs build under dir's cross-process lock unless dir's stamp
// says it is current, and stamps dir after a build that succeeds. It
// reports whether it ran build.
//
// The stamp holds what the build read: the package's manifest, lock file,
// tsconfigs, src/ and scripts/, the files outside it its build script
// names (scripts/build-dist.mjs), the files of in.sources, in.tools, and
// each of in.copies' dist/.
// And what it left: dist/, by each file's path, size and modification
// time, which another build (make ts, a developer's bun run build) that
// changes it changes, and node_modules' symlinks (link-local-deps'), by
// name, target and whether the target is there, which a bun install that
// drops one changes.
func buildOnce(dir string, in buildInputs, build func() error) (bool, error) {
	dir, err := filepath.Abs(dir)
	if err != nil {
		return false, err
	}
	if resolved, err := filepath.EvalSymlinks(dir); err == nil {
		dir = resolved
	}
	unlock, err := lockDir(dir)
	if err != nil {
		return false, err
	}
	defer unlock()
	inputs, err := inputsDigest(dir, in)
	if err != nil {
		return false, err
	}
	stamp := filepath.Join(dir, "node_modules", stampFile)
	if recorded, err := os.ReadFile(stamp); err == nil {
		if output, err := outputDigest(dir); err == nil && string(recorded) == inputs+"\n"+output+"\n" {
			return false, nil
		}
	}
	// A build that fails part way leaves no stamp to trust.
	_ = os.Remove(stamp)
	if err := build(); err != nil {
		return true, err
	}
	output, err := outputDigest(dir)
	if err != nil {
		return true, fmt.Errorf("after building %s: %w", dir, err)
	}
	if err := os.MkdirAll(filepath.Dir(stamp), 0o755); err != nil {
		return true, err
	}
	return true, os.WriteFile(stamp, []byte(inputs+"\n"+output+"\n"), 0o644)
}

// lockPath is dir's lock file, keyed by its resolved path. It is under the
// user's cache directory, which every process of the user's shares
// whatever its TMPDIR, rather than in dir, whose node_modules a bun install
// may start over under the lock; the temporary directory stands in where
// there is no cache directory.
func lockPath(dir string) (string, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		base = os.TempDir()
	}
	locks := filepath.Join(base, "superschematic", "build-locks")
	if err := os.MkdirAll(locks, 0o755); err != nil {
		return "", err
	}
	sum := sha256.Sum256([]byte(dir))
	return filepath.Join(locks, hex.EncodeToString(sum[:8])+".lock"), nil
}

// inputsDigest hashes what a package's build reads (see buildOnce).
func inputsDigest(dir string, in buildInputs) (string, error) {
	h := sha256.New()
	files := []string{"package.json", "bun.lock"}
	configs, err := filepath.Glob(filepath.Join(dir, "tsconfig*.json"))
	if err != nil {
		return "", err
	}
	for _, c := range configs {
		files = append(files, filepath.Base(c))
	}
	for _, sub := range append([]string{"src", "scripts"}, in.sources...) {
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
	scripts, err := buildScriptFiles(dir)
	if err != nil {
		return "", err
	}
	files = append(files, scripts...)
	slices.Sort(files)
	files = slices.Compact(files)
	for _, rel := range files {
		f, err := os.Open(filepath.Join(dir, rel))
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return "", err
		}
		_, _ = fmt.Fprintf(h, "%s\x00", filepath.ToSlash(rel))
		_, err = io.Copy(h, f)
		_ = f.Close()
		if err != nil {
			return "", err
		}
		h.Write([]byte{0})
	}
	_, _ = fmt.Fprintf(h, "tools\x00%s\x00", in.tools)
	for _, dep := range in.copies {
		digest, err := distDigest(dep)
		if err != nil {
			digest = "missing: " + err.Error()
		}
		_, _ = fmt.Fprintf(h, "copy\x00%s\x00%s\x00", dep, digest)
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

// buildScriptFiles is the script files package.json's build script names
// by a path relative to dir, such as ../../../scripts/build-dist.mjs.
func buildScriptFiles(dir string) ([]string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "package.json"))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var manifest struct {
		Scripts map[string]string `json:"scripts"`
	}
	if err := json.Unmarshal(data, &manifest); err != nil {
		return nil, fmt.Errorf("%s/package.json: %w", dir, err)
	}
	var files []string
	for _, word := range strings.Fields(manifest.Scripts["build"]) {
		word = strings.Trim(word, `"'`)
		switch filepath.Ext(word) {
		case ".mjs", ".cjs", ".js":
		default:
			continue
		}
		if info, err := os.Stat(filepath.Join(dir, word)); err == nil && info.Mode().IsRegular() {
			files = append(files, filepath.Clean(word))
		}
	}
	return files, nil
}

// outputDigest fingerprints what a build leaves (see buildOnce).
func outputDigest(dir string) (string, error) {
	dist, err := distDigest(dir)
	if err != nil {
		return "", err
	}
	return dist + " " + linksDigest(dir), nil
}

// linksDigest fingerprints the symlinks in dir's node_modules, scoped ones
// included: each one's name, target and whether the target is there.
func linksDigest(dir string) string {
	var lines []string
	modules := filepath.Join(dir, "node_modules")
	var scan func(sub string)
	scan = func(sub string) {
		entries, err := os.ReadDir(filepath.Join(modules, sub))
		if err != nil {
			return
		}
		for _, e := range entries {
			name := filepath.Join(sub, e.Name())
			path := filepath.Join(modules, name)
			switch {
			case e.Type()&fs.ModeSymlink != 0:
				target, _ := os.Readlink(path)
				_, err := os.Stat(path)
				lines = append(lines, fmt.Sprintf("%s -> %s %t", filepath.ToSlash(name), target, err == nil))
			case e.IsDir() && sub == "" && strings.HasPrefix(e.Name(), "@"):
				scan(e.Name())
			}
		}
	}
	scan("")
	sum := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(sum[:])
}

// distDigest fingerprints dir's dist/ by each file's path, size and
// modification time. It is an error for dist/ to be missing or empty.
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
