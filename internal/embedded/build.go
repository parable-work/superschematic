package embedded

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"github.com/BurntSushi/toml"

	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/stackdeploy"
	ir "github.com/parable-work/superschematic/ir"
)

// RunCommand runs name with args in dir, with its output on log, and its
// standard output on out when out is set.
type RunCommand func(ctx context.Context, dir string, out, log io.Writer, name string, args ...string) error

// BuildOptions is one build of an environment's binary.
type BuildOptions struct {
	// Environment is the environment, resolved, which builds as one
	// binary.
	Environment *ir.ResolvedEnvironment

	// OutputRoot is the output root the stack's build wrote the crate
	// into, the root of the Cargo workspace it is a member of.
	OutputRoot string

	// Sources says where the site's package is, and runs its build.
	Sources *stackdeploy.Sources

	// Planned is what the binary carries for its databases, and Release
	// whether a release recorded it.
	Planned *Planned
	Release bool

	// Log receives what the builds print.
	Log io.Writer

	// Run runs cargo; nil runs it with os/exec.
	Run RunCommand
}

// Built is a binary a build made.
type Built struct {
	// Binary is the binary's path, and Crate the crate's directory.
	Binary string
	Crate  string

	// SiteFiles counts the files of the site the binary carries.
	SiteFiles int
}

// Build builds the binary of an environment (D116): it runs the site's
// build, as every target does, writes what the binary carries into the
// crate the stack's build wrote, each database's plans and the site's
// files, and builds the crate with `cargo build --release --locked` in
// the output root's workspace.
func Build(ctx context.Context, o BuildOptions) (*Built, error) {
	env := o.Environment
	log := o.Log
	if log == nil {
		log = io.Discard
	}
	dir := servergen.EmbeddedDir(o.OutputRoot, env.Stack, env.Environment)
	manifest := filepath.Join(dir, servergen.RustManifestFile)
	var crate struct {
		Package struct {
			Name string `toml:"name"`
		} `toml:"package"`
	}
	if _, err := toml.DecodeFile(manifest, &crate); errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("environment %s builds as one binary, and %s holds no crate of it: build the stack first, which writes it", env.Environment, dir)
	} else if err != nil {
		return nil, fmt.Errorf("the crate of environment %s: %w", env.Environment, err)
	}
	carried := servergen.EmbeddedCarried{Stack: env.Stack, Environment: env.Environment, Release: o.Release}
	if o.Planned != nil {
		carried.Databases = o.Planned.Databases
	}
	for _, d := range env.Deployables {
		if d.Kind != ir.DeployableSite {
			continue
		}
		if o.Sources == nil {
			return nil, fmt.Errorf("site %s: the build names no repository root, under which its package lies", d.Name)
		}
		files, err := stackdeploy.BuildSite(ctx, o.Sources, d, log)
		if err != nil {
			return nil, err
		}
		for _, name := range slices.Sorted(maps.Keys(files.Sums)) {
			content, err := os.ReadFile(filepath.Join(files.Dir, filepath.FromSlash(name)))
			if err != nil {
				return nil, fmt.Errorf("site %s: %w", d.Name, err)
			}
			carried.Site = append(carried.Site, servergen.EmbeddedCarriedFile{
				Path:        name,
				ContentType: servergen.SiteContentType(name),
				Digest:      files.Sums[name],
				Content:     content,
			})
		}
	}
	if err := servergen.WriteEmbeddedCarried(dir, carried); err != nil {
		return nil, err
	}
	run := o.Run
	if run == nil {
		run = execCommand
	}
	_, _ = fmt.Fprintf(log, "environment %s: cargo build --release --locked -p %s in %s\n", env.Environment, crate.Package.Name, o.OutputRoot)
	if err := run(ctx, o.OutputRoot, nil, log, "cargo", "build", "--release", "--locked", "-p", crate.Package.Name); err != nil {
		return nil, fmt.Errorf("build the binary of environment %s: %w; a lockfile that is not current is brought up to date by a build of every service (superschematic build-all), and committed", env.Environment, err)
	}
	target, err := targetDirectory(ctx, run, o.OutputRoot, log)
	if err != nil {
		return nil, err
	}
	return &Built{
		Binary:    filepath.Join(target, "release", servergen.EmbeddedBinary(env.Stack, env.Environment)),
		Crate:     dir,
		SiteFiles: len(carried.Site),
	}, nil
}

// targetDirectory asks cargo where the workspace at dir builds into.
func targetDirectory(ctx context.Context, run RunCommand, dir string, log io.Writer) (string, error) {
	var out bytes.Buffer
	if err := run(ctx, dir, &out, log, "cargo", "metadata", "--format-version", "1", "--no-deps", "--locked"); err != nil {
		return "", fmt.Errorf("ask cargo where %s builds into: %w", dir, err)
	}
	var metadata struct {
		TargetDirectory string `json:"target_directory"`
	}
	if err := json.Unmarshal(out.Bytes(), &metadata); err != nil || metadata.TargetDirectory == "" {
		return "", fmt.Errorf("cargo metadata in %s names no target directory: %v", dir, err)
	}
	return metadata.TargetDirectory, nil
}

// execCommand is RunCommand with os/exec.
func execCommand(ctx context.Context, dir string, out, log io.Writer, name string, args ...string) error {
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Dir, cmd.Stdout, cmd.Stderr = dir, log, log
	if out != nil {
		cmd.Stdout = out
	}
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%s %s in %s: %w", name, strings.Join(args, " "), dir, err)
	}
	return nil
}
