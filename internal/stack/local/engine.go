package local

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/internal/generator/projectengine"
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/registry"
)

// The project's engine on the local target (D58). A Go or TypeScript
// entrypoint whose engine database composes a behavior the core's engine
// leaves out, the work queue's or an extension's, opens the engine built
// from the project's engine crate, which the database's build writes at
// engine/rust/<db> under the output root; its module's
// projectengine.LinkFile names the crate and the member it takes. Before it
// builds or runs the entrypoint the provisioner builds that member with
// cargo, as a checkout's Dockerfile does in its engine stage:
//
//   - a Go entrypoint links the ffi member's static archive in the core's
//     place: the build's CGO_LDFLAGS search its directory first;
//   - a TypeScript one's process loads the napi member's addon:
//     projectengine.AddonEnv names its file.
//
// Every member builds into the program's Cargo target directory,
// RustTargetDir, beside the Rust deployables, so cargo rebuilds only what
// changed since the last run, with the Rust release tools.env pins, whose
// archives (superscalar's and the version graph's) a Go binary links
// beside it. Its output is copied into EngineDir, in a directory named
// after its SHA-256: Go's build cache keys a link on CGO_LDFLAGS's text,
// not on an archive's bytes, so a changed archive is a changed flag and
// relinks the binary, and an unchanged one keeps the cache warm.

// EngineDir is the directory, under the program directory, that holds
// each project engine member the provisioner built, as
// engine/<db>/<member>/<digest>/.
const EngineDir = "engine"

// engineArchive is the name the engine's Go binding links its archive by.
const engineArchive = "libsuperschematic_engine.a"

// projectEngine is what the build of an entrypoint, or its process, takes
// of the project's engine crate: the -L flag a Go build puts first in
// CGO_LDFLAGS, or the addon a TypeScript process names with
// projectengine.AddonEnv. Both are empty for an entrypoint whose engine is
// the core's.
type projectEngine struct {
	ldflag string
	addon  string
}

// buildEnv is the environment a Go build of the entrypoint runs in: the
// request's (buildEnv), with CGO_LDFLAGS searching the project's engine
// archive's directory first, before the release's static archives, which
// hold the core's engine archive by the same name.
func (e projectEngine) buildEnv(req registry.ProvisionRequest) []string {
	env := buildEnv(req)
	if e.ldflag == "" {
		return env
	}
	flags, ok := req.BuildEnv[cgoLinkerFlags]
	if !ok {
		flags = os.Getenv(cgoLinkerFlags)
	}
	return append(env, cgoLinkerFlags+"="+strings.TrimSpace(e.ldflag+" "+flags))
}

// processEnv is env with projectengine.AddonEnv naming the addon, when
// there is one.
func (e projectEngine) processEnv(env []string) []string {
	if e.addon == "" {
		return env
	}
	return append(env, projectengine.AddonEnv+"="+e.addon)
}

// cgoLinkerFlags is the variable cgo reads the linker's flags from.
const cgoLinkerFlags = "CGO_LDFLAGS"

// projectEngine builds the member of the project's engine crate the
// entrypoint module of deployable (what it is) takes, when the module has a
// projectengine.LinkFile, and returns what its build or process takes of
// it. It refuses a crate that depends on the engine's crates by version,
// as one built with no [paths] key naming the engine's checkout does: no
// registry serves them.
func (p *Provisioner) projectEngine(ctx context.Context, req registry.ProvisionRequest, module, what, deployable string) (projectEngine, error) {
	link, err := projectengine.ReadLink(module)
	if err != nil || link == nil {
		return projectEngine{}, err
	}
	crate := filepath.Join(module, filepath.FromSlash(link.Crate))
	if err := checkEngineCrate(crate, link.Database); err != nil {
		return projectEngine{}, fmt.Errorf("local: %s %s: %w", what, deployable, err)
	}
	if link.Lockfile != "" {
		if err := seedLockfile(crate, filepath.Join(module, filepath.FromSlash(link.Lockfile))); err != nil {
			return projectEngine{}, fmt.Errorf("local: %s %s: %w", what, deployable, err)
		}
	}
	cargo, err := p.lookPath("cargo")
	if err != nil {
		return projectEngine{}, err
	}
	target, err := filepath.Abs(filepath.Join(req.Dir, RustTargetDir))
	if err != nil {
		return projectEngine{}, err
	}
	manifest := filepath.Join(crate, link.Member, "Cargo.toml")
	p.printf("build %s: cargo build --release of the engine crate of %s, its %s member", deployable, link.Database, link.Member)
	out, err := p.runner().Run(ctx, Command{
		Path: cargo,
		Args: []string{"build", "--release", "--quiet", "--manifest-path", manifest, "--target-dir", target, "--message-format=json-render-diagnostics"},
		Dir:  crate,
		Env:  engineCargoEnv(),
	})
	if err != nil {
		return projectEngine{}, fmt.Errorf("local: build the engine crate of %s for %s %s: %w", link.Database, what, deployable, err)
	}
	kind := "staticlib"
	if link.Member == projectengine.NapiDir {
		kind = "cdylib"
	}
	built, err := cargoArtifact(out, manifest, kind)
	if err != nil {
		return projectEngine{}, fmt.Errorf("local: the engine crate of %s for %s %s: %w", link.Database, what, deployable, err)
	}
	name := engineArchive
	if link.Member == projectengine.NapiDir {
		name = link.Database + "-napi.node"
	}
	staged, err := stage(built, filepath.Join(req.Dir, EngineDir, link.Database, link.Member), name)
	if err != nil {
		return projectEngine{}, fmt.Errorf("local: the engine crate of %s for %s %s: %w", link.Database, what, deployable, err)
	}
	if link.Member == projectengine.NapiDir {
		return projectEngine{addon: staged}, nil
	}
	return projectEngine{ldflag: "-L" + filepath.Dir(staged)}, nil
}

// engineCargoEnv is the environment cargo builds a project's engine crate
// in: the Rust release tools.env pins, unless the caller's
// RUSTUP_TOOLCHAIN names one, since a Go binary links the archive beside
// superscalar's and the version graph's, which that release built, and two
// releases' standard libraries do not link into one binary; and on macOS
// the oldest release Go's linker targets, which the SQLite rusqlite
// compiles would otherwise exceed, as scripts/engine-archive.sh sets.
func engineCargoEnv() []string {
	env := os.Environ()
	if os.Getenv("RUSTUP_TOOLCHAIN") == "" {
		env = append(env, "RUSTUP_TOOLCHAIN="+servergen.RustVersion)
	}
	if runtime.GOOS == "darwin" && os.Getenv("MACOSX_DEPLOYMENT_TARGET") == "" {
		env = append(env, "MACOSX_DEPLOYMENT_TARGET=11.0")
	}
	return env
}

// checkEngineCrate refuses a project's engine crate the build did not
// write, or one that depends on the engine by version rather than by path:
// the engine's crates are no registry's, so only a checkout the naming
// file's [paths] engine_rust names builds it.
func checkEngineCrate(crate, database string) error {
	path := filepath.Join(crate, "Cargo.toml")
	var manifest struct {
		Dependencies map[string]any `toml:"dependencies"`
	}
	if _, err := toml.DecodeFile(path, &manifest); errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("no engine crate of %s at %s, which the build of %s writes (D58)", database, crate, database)
	} else if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	dep, ok := manifest.Dependencies[projectengine.EngineCrate].(map[string]any)
	if !ok {
		return fmt.Errorf("%s does not depend on %s", path, projectengine.EngineCrate)
	}
	if _, ok := dep["path"]; !ok {
		return fmt.Errorf("the engine crate of %s depends on %s by version, as a build whose naming file's [paths] engine_rust is unset writes it, and no registry serves the engine's crates; set the [paths] engine keys (engine_rust, engine_behaviors_rust, engine_workqueue_rust, engine_abi_rust, engine_napi_rust) to a superschematic checkout's runtime/engine/rust crates", database, projectengine.EngineCrate)
	}
	return nil
}

// seedLockfile copies the engine workspace's lock file into a crate that
// has none, as the image's engine stage does, so its first build takes the
// versions the engine's crates are tested with. A lock file of the crate's
// own, which cargo keeps up to date, stays.
func seedLockfile(crate, lockfile string) error {
	own := filepath.Join(crate, "Cargo.lock")
	if _, err := os.Stat(own); err == nil {
		return nil
	}
	data, err := os.ReadFile(lockfile)
	if errors.Is(err, fs.ErrNotExist) {
		return nil
	}
	if err != nil {
		return err
	}
	return os.WriteFile(own, data, 0o644)
}

// cargoArtifact is the file of kind (staticlib, cdylib) cargo reports it
// built for the package whose manifest is manifest, in its JSON messages,
// out: the last one, which is the build's.
func cargoArtifact(out []byte, manifest, kind string) (string, error) {
	want, err := filepath.EvalSymlinks(manifest)
	if err != nil {
		return "", err
	}
	suffix := map[string][]string{"staticlib": {".a"}, "cdylib": {".so", ".dylib"}}[kind]
	var found string
	scanner := bufio.NewScanner(bytes.NewReader(out))
	scanner.Buffer(make([]byte, 0, 64*1024), 16*1024*1024)
	for scanner.Scan() {
		var message struct {
			Reason       string   `json:"reason"`
			ManifestPath string   `json:"manifest_path"`
			Filenames    []string `json:"filenames"`
			Target       struct {
				Kind []string `json:"kind"`
			} `json:"target"`
		}
		if json.Unmarshal(scanner.Bytes(), &message) != nil || message.Reason != "compiler-artifact" {
			continue
		}
		if got, err := filepath.EvalSymlinks(message.ManifestPath); err != nil || got != want {
			continue
		}
		if !slices.Contains(message.Target.Kind, kind) {
			continue
		}
		for _, file := range message.Filenames {
			for _, s := range suffix {
				if strings.HasSuffix(file, s) {
					found = file
				}
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return "", err
	}
	if found == "" {
		return "", fmt.Errorf("cargo reported no %s for %s", kind, manifest)
	}
	return found, nil
}

// stage copies built into dir/<its SHA-256's first 16 hex digits>/name,
// unless an earlier run staged the same bytes there, and returns its path.
func stage(built, dir, name string) (string, error) {
	f, err := os.Open(built)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	digest := hex.EncodeToString(h.Sum(nil))[:16]
	dest, err := filepath.Abs(filepath.Join(dir, digest, name))
	if err != nil {
		return "", err
	}
	if _, err := os.Stat(dest); err == nil {
		return dest, nil
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(filepath.Dir(dest), ".stage-*")
	if err != nil {
		return "", err
	}
	if _, err := io.Copy(tmp, f); err != nil {
		_ = tmp.Close()
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := tmp.Close(); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	if err := os.Rename(tmp.Name(), dest); err != nil {
		_ = os.Remove(tmp.Name())
		return "", err
	}
	return dest, nil
}
