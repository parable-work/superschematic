package testpaths

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

// emitScript stands in for tsc: it writes a package's files into the
// directory after --outDir slowly, index.js last, as a compiler that takes
// a while does. Each build's files differ from the last's (the stamp), so
// every build moves every file in; EMIT_STAMP fixes the stamp.
const emitScript = `
const { mkdirSync, writeFileSync } = require("node:fs");
const { join } = require("node:path");
const out = process.argv[process.argv.indexOf("--outDir") + 1];
const stamp = process.env.EMIT_STAMP || process.pid + "-" + Date.now();
const pause = (ms) => Atomics.wait(new Int32Array(new SharedArrayBuffer(4)), 0, 0, ms);
mkdirSync(join(out, "sub"), { recursive: true });
for (let i = 0; i < 20; i++) {
  writeFileSync(join(out, "sub", "m" + i + ".js"), "export const m = " + i + "; // " + stamp + "\n");
  pause(2);
}
writeFileSync(join(out, "index.js"), "export * from './sub/m0.js'; // " + stamp + "\n");
`

// TestBuildDistNeverLeavesDistMissing runs two builds of a package at once,
// over and over, through scripts/build-dist.mjs, each build's files
// differing from the last's, while a reader polls for its dist/ files, and
// finds them every time. (It shows no file goes missing; a reader can still
// see one build's file beside another's, which build-dist.mjs's header
// states.) The same harness on the
// build packages ran before (rm -rf dist, then compile into it) catches the
// reader finding them missing, so the reader would see a window if there
// were one.
func TestBuildDistNeverLeavesDistMissing(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: runs Node.js builds for a few seconds")
	}
	node, err := exec.LookPath("node")
	if err != nil {
		RequireOrSkipTS(t, fmt.Sprintf("node not available: %v", err))
	}
	buildDist := filepath.Join(RepoRoot(t), "scripts", "build-dist.mjs")

	// race runs the builds and the reader; failures are the builds' own
	// failures, which the old build has when two run at once (one removes
	// dist/ under the other's compiler).
	race := func(t *testing.T, failures bool, build func(dir string) *exec.Cmd) (misses int64) {
		t.Helper()
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "emit.cjs"), []byte(emitScript), 0o644); err != nil {
			t.Fatal(err)
		}
		first := build(dir)
		if out, err := first.CombinedOutput(); err != nil {
			t.Fatalf("first build: %v\n%s", err, out)
		}
		ctx, stop := context.WithCancel(context.Background())
		var reads atomic.Int64
		var missed atomic.Int64
		readerDone := make(chan struct{})
		go func() {
			defer close(readerDone)
			for ctx.Err() == nil {
				for _, file := range []string{"index.js", filepath.Join("sub", "m0.js"), filepath.Join("sub", "m19.js")} {
					if _, err := os.Stat(filepath.Join(dir, "dist", file)); err != nil {
						missed.Add(1)
					}
					reads.Add(1)
				}
			}
		}()
		var wg sync.WaitGroup
		errs := make(chan error, 2)
		for range 2 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				for range 8 {
					if out, err := build(dir).CombinedOutput(); err != nil && !failures {
						errs <- fmt.Errorf("%v\n%s", err, out)
						return
					}
				}
			}()
		}
		wg.Wait()
		stop()
		<-readerDone
		close(errs)
		for err := range errs {
			t.Fatalf("concurrent build: %v", err)
		}
		if reads.Load() == 0 {
			t.Fatal("the reader read nothing")
		}
		// What the builds leave is one build's output, and no staging.
		var left []string
		_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
			if err == nil && !d.IsDir() {
				rel, _ := filepath.Rel(dir, path)
				left = append(left, filepath.ToSlash(rel))
			}
			return err
		})
		for _, file := range left {
			if strings.HasPrefix(file, ".dist-build-") {
				t.Errorf("a build left its staging behind: %s", file)
			}
		}
		t.Logf("%d reads, %d missing; %d files left", reads.Load(), missed.Load(), len(left))
		return missed.Load()
	}

	t.Run("build-dist", func(t *testing.T) {
		misses := race(t, false, func(dir string) *exec.Cmd {
			cmd := exec.Command(node, buildDist, node, "emit.cjs")
			cmd.Dir = dir
			return cmd
		})
		if misses != 0 {
			t.Fatalf("a reader found dist/ files missing %d times during build-dist builds", misses)
		}
	})
	t.Run("rm-then-compile control", func(t *testing.T) {
		misses := race(t, true, func(dir string) *exec.Cmd {
			cmd := exec.Command("sh", "-c", `rm -rf dist && "$0" emit.cjs --outDir dist`, node)
			cmd.Dir = dir
			return cmd
		})
		if misses == 0 {
			t.Fatal("the reader never caught the old build's missing dist/: it cannot see a window")
		}
	})
}

// TestBuildDistKeepsDistOnFailure: a build whose compiler fails leaves
// dist/ as it was and no staging directory.
func TestBuildDistKeepsDistOnFailure(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		RequireOrSkipTS(t, fmt.Sprintf("node not available: %v", err))
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "dist", "index.js"), "old\n")
	writeFile(t, filepath.Join(dir, "dist", "gone.js"), "old\n")
	cmd := exec.Command(node, filepath.Join(RepoRoot(t), "scripts", "build-dist.mjs"), node, "-e", "process.exit(3)")
	cmd.Dir = dir
	if err := cmd.Run(); err == nil {
		t.Fatal("build-dist succeeded on a failing compiler")
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 || entries[0].Name() != "dist" {
		t.Fatalf("after a failed build the package holds %v, not just dist/", entries)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "dist", "gone.js")); err != nil || string(b) != "old\n" {
		t.Fatalf("dist/gone.js after a failed build: %q, %v", b, err)
	}

	// A build that succeeds removes what it no longer writes.
	writeFile(t, filepath.Join(dir, "emit.cjs"), emitScript)
	cmd = exec.Command(node, filepath.Join(RepoRoot(t), "scripts", "build-dist.mjs"), node, "emit.cjs")
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("build-dist: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "dist", "gone.js")); !errors.Is(err, fs.ErrNotExist) {
		t.Fatalf("dist/gone.js after a build that does not write it: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, "dist", "index.js")); err != nil || !strings.Contains(string(b), "m0.js") {
		t.Fatalf("dist/index.js after the build: %q, %v", b, err)
	}
}

// TestBuildDistLeavesAnIdenticalRebuildAlone: a rebuild with the same
// output leaves dist/'s files as they were, modification times included, so
// a reader sees no change and BuildTypeScriptPackage's stamp stays current;
// and a build sweeps a staging directory a killed build left over an hour
// ago, but not a younger one, which may be another build's.
func TestBuildDistLeavesAnIdenticalRebuildAlone(t *testing.T) {
	node, err := exec.LookPath("node")
	if err != nil {
		RequireOrSkipTS(t, fmt.Sprintf("node not available: %v", err))
	}
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "emit.cjs"), emitScript)
	build := func() {
		t.Helper()
		cmd := exec.Command(node, filepath.Join(RepoRoot(t), "scripts", "build-dist.mjs"), node, "emit.cjs")
		cmd.Dir = dir
		cmd.Env = append(os.Environ(), "EMIT_STAMP=fixed")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build-dist: %v\n%s", err, out)
		}
	}
	build()
	before, err := distDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	old := filepath.Join(dir, ".dist-build-1-1")
	young := filepath.Join(dir, ".dist-build-2-2")
	writeFile(t, filepath.Join(old, "index.js"), "killed\n")
	writeFile(t, filepath.Join(young, "index.js"), "running\n")
	hoursAgo := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(old, hoursAgo, hoursAgo); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	build()
	after, err := distDigest(dir)
	if err != nil {
		t.Fatal(err)
	}
	if before != after {
		t.Error("an identical rebuild changed dist/'s files or their modification times")
	}
	if _, err := os.Stat(old); !errors.Is(err, fs.ErrNotExist) {
		t.Errorf("the build left a staging directory from two hours ago: %v", err)
	}
	if _, err := os.Stat(young); err != nil {
		t.Errorf("the build swept a young staging directory: %v", err)
	}
}

const (
	buildOnceDirEnv = "SUPERSCHEMATIC_TEST_BUILD_ONCE_DIR"
	buildOnceLogEnv = "SUPERSCHEMATIC_TEST_BUILD_ONCE_LOG"
	buildOnceDepEnv = "SUPERSCHEMATIC_TEST_BUILD_ONCE_DEP"
)

// TestBuildOnceHelperProcess is the test process TestBuildOnceAcrossProcesses
// starts several of at once: it builds the package in buildOnceDirEnv
// through buildOnce, logging when its build starts and ends. The build
// copies buildOnceDepEnv's dist/ in, as the engine's copies the HTTP
// runtime's, and links node_modules/linked, as link-local-deps links
// superscalar.
func TestBuildOnceHelperProcess(t *testing.T) {
	dir := os.Getenv(buildOnceDirEnv)
	if dir == "" {
		t.Skip("run by TestBuildOnceAcrossProcesses")
	}
	logLine := func(s string) {
		f, err := os.OpenFile(os.Getenv(buildOnceLogEnv), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
		if err != nil {
			t.Fatal(err)
		}
		defer f.Close()
		fmt.Fprintf(f, "%s %d\n", s, os.Getpid())
	}
	in := buildInputs{tools: "bun 1 node 1", copies: []string{os.Getenv(buildOnceDepEnv)}}
	_, err := buildOnce(dir, in, func() error {
		logLine("start")
		link := filepath.Join(dir, "node_modules", "linked")
		if err := os.MkdirAll(filepath.Dir(link), 0o755); err != nil {
			return err
		}
		_ = os.Remove(link)
		if err := os.Symlink(filepath.Join("..", "linked-target"), link); err != nil {
			return err
		}
		// The old build: dist/ gone while it runs.
		if err := os.RemoveAll(filepath.Join(dir, "dist")); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
		if err := os.MkdirAll(filepath.Join(dir, "dist"), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "dist", "index.js"), []byte("built\n"), 0o644); err != nil {
			return err
		}
		logLine("end")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// TestBuildOnceAcrossProcesses starts four test processes at once that each
// build one package through buildOnce: one builds it while the others wait
// on its lock, then find it current and build nothing. Each of these makes
// the next round build once more: a source file changing, the script the
// build script names outside the package changing, another build
// rewriting dist/, a bun install dropping a symlink the build made, and the
// package whose dist/ the build copies being rebuilt.
func TestBuildOnceAcrossProcesses(t *testing.T) {
	if testing.Short() {
		t.Skip("-short: starts test processes")
	}
	root := t.TempDir()
	dir := filepath.Join(root, "pkg")
	dep := filepath.Join(root, "dep")
	writeFile(t, filepath.Join(dir, "package.json"), `{"name": "fixture", "scripts": {"build": "node ../tool.mjs tsc"}}`+"\n")
	writeFile(t, filepath.Join(dir, "src", "index.ts"), "export const a = 1;\n")
	writeFile(t, filepath.Join(dir, "linked-target", "package.json"), "{}\n")
	writeFile(t, filepath.Join(root, "tool.mjs"), "// one\n")
	writeFile(t, filepath.Join(dep, "dist", "index.js"), "dep\n")
	logPath := filepath.Join(t.TempDir(), "builds.log")

	round := func(t *testing.T) []string {
		t.Helper()
		_ = os.Remove(logPath)
		var wg sync.WaitGroup
		errs := make(chan error, 4)
		for range 4 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				cmd := exec.Command(os.Args[0], "-test.run=^TestBuildOnceHelperProcess$", "-test.count=1")
				cmd.Env = append(os.Environ(), buildOnceDirEnv+"="+dir, buildOnceLogEnv+"="+logPath, buildOnceDepEnv+"="+dep)
				if out, err := cmd.CombinedOutput(); err != nil {
					errs <- fmt.Errorf("%v\n%s", err, out)
				}
			}()
		}
		wg.Wait()
		close(errs)
		for err := range errs {
			t.Fatalf("helper process: %v", err)
		}
		data, err := os.ReadFile(logPath)
		if errors.Is(err, fs.ErrNotExist) {
			return nil
		}
		if err != nil {
			t.Fatal(err)
		}
		return strings.Fields(strings.TrimSpace(string(data)))
	}
	builtOnce := func(t *testing.T, why string, log []string) {
		t.Helper()
		// start <pid> end <pid>: one build, never overlapped.
		if len(log) != 4 || log[0] != "start" || log[2] != "end" || log[1] != log[3] {
			t.Fatalf("%s: want one build by one process, got %q", why, log)
		}
	}

	builtOnce(t, "first round", round(t))
	if log := round(t); len(log) != 0 {
		t.Fatalf("a round with nothing changed built: %q", log)
	}

	writeFile(t, filepath.Join(dir, "src", "index.ts"), "export const a = 2;\n")
	builtOnce(t, "after a source changed", round(t))

	// Another build (make ts, a developer's bun run build) rewrote dist/.
	later := time.Now().Add(time.Second)
	if err := os.Chtimes(filepath.Join(dir, "dist", "index.js"), later, later); err != nil {
		t.Fatal(err)
	}
	builtOnce(t, "after dist/ changed", round(t))
	if log := round(t); len(log) != 0 {
		t.Fatalf("a round with nothing changed built: %q", log)
	}

	writeFile(t, filepath.Join(root, "tool.mjs"), "// two\n")
	builtOnce(t, "after the build script's tool changed", round(t))

	// A bun install that dropped link-local-deps' symlink.
	if err := os.Remove(filepath.Join(dir, "node_modules", "linked")); err != nil {
		t.Fatal(err)
	}
	builtOnce(t, "after the symlink went", round(t))
	if _, err := os.Stat(filepath.Join(dir, "node_modules", "linked", "package.json")); err != nil {
		t.Fatalf("the rebuild did not link it again: %v", err)
	}

	// The copied package rebuilt.
	if err := os.Chtimes(filepath.Join(dep, "dist", "index.js"), later.Add(time.Second), later.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	builtOnce(t, "after the copied package was rebuilt", round(t))
	if log := round(t); len(log) != 0 {
		t.Fatalf("a round with nothing changed built: %q", log)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}
