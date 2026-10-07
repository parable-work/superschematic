// Package cigen is the Stack kind's ci generator and the core's CI
// renderer (docs/stack-model.md, section 11.3, D47). A stack opts in with
// outputs.ci in its config, keyed by renderer. The generator resolves every
// environment of the stack, as the stack generator does, asks each
// environment's target how a CI job signs in to it, and hands the result to
// each renderer the config names. It writes the files under
// `<output-root>/ci/<stack>/<renderer>/`, and installs a copy into the
// renderer's directory under the repository root when that directory
// exists.
//
// The build cache keys the generator's output on the stack service's inputs,
// which hold its config and so the renderers' options, and on the binary,
// whose release the workflow installs; it needs no key of its own.
package cigen

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"strings"

	"github.com/parable-work/superschematic/internal/generator/stackgen"
	"github.com/parable-work/superschematic/internal/registry"
	"github.com/parable-work/superschematic/internal/release"
	ir "github.com/parable-work/superschematic/ir"
)

// Name is the generator's name in the Stack kind's pipeline, and the
// outputs key it reads.
const Name = "ci"

// OutDir is where the CI of the stack the service declares is written: a
// stack takes its service's name.
func OutDir(outputRoot, service string) string {
	return filepath.Join(outputRoot, "ci", service)
}

// Enabled reports whether the stack's config names a CI renderer.
func Enabled(c registry.GenerateContext) (bool, string) {
	return len(c.Outputs.CIRenderers()) > 0, ""
}

// Generate renders the CI of the stack c.Schema declares with each renderer
// outputs.ci names, writes it under OutDir, which it empties first, and
// installs it under the repository root. Nothing is written unless every
// environment resolves and every renderer renders.
func Generate(c registry.GenerateContext) error {
	renderers := c.Outputs.CIRenderers()
	if len(renderers) == 0 {
		c.Skip(Name)
		return nil
	}
	st := ir.StackOf(c.Schema)
	if st == nil {
		return fmt.Errorf("stack %s: no class declares @stack", c.Config.Name)
	}
	services, err := stackgen.Services(c, st)
	if err != nil {
		return err
	}
	resolved, err := stackgen.Resolve(c, st, services)
	if err != nil {
		return err
	}
	ciStack, err := stackOf(c, st.Name)
	if err != nil {
		return err
	}
	envs := make([]registry.CIEnvironment, 0, len(resolved))
	for _, env := range resolved {
		envs = append(envs, c.Registry.CIEnvironment(env))
	}
	rel := c.Options.ReleaseInfo()

	type rendered struct {
		name    string
		install string
		files   []registry.CIFile
	}
	var outputs []rendered
	for _, name := range renderers {
		spec, ok := c.Registry.CIRenderer(name)
		if !ok {
			return fmt.Errorf("stack %s: outputs.ci names CI renderer %q, which is not registered (registered CI renderers: %s)", st.Name, name, strings.Join(c.Registry.CIRenderers(), ", "))
		}
		opts := c.Outputs.CI[name].WithDefaults(spec)
		files, err := spec.Render(registry.CIRequest{
			Stack:        ciStack,
			Environments: envs,
			Options:      opts,
			Version:      rel.Version,
			Archives:     archives(rel),
		})
		if err != nil {
			return fmt.Errorf("stack %s: CI renderer %s: %w", st.Name, name, err)
		}
		for _, f := range files {
			if f.Path == "" || strings.Contains(f.Path, `\`) || path.IsAbs(f.Path) || path.Clean(f.Path) != f.Path || f.Path == ".." || strings.HasPrefix(f.Path, "../") {
				return fmt.Errorf("stack %s: CI renderer %s writes %q, which is not a slash-separated path inside its directory", st.Name, name, f.Path)
			}
		}
		outputs = append(outputs, rendered{name: name, install: opts.Install, files: files})
	}

	dir := OutDir(c.Options.OutputRoot, st.Name)
	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("stack %s: %w", st.Name, err)
	}
	for _, out := range outputs {
		if err := writeFiles(filepath.Join(dir, out.name), out.files); err != nil {
			return fmt.Errorf("stack %s: %w", st.Name, err)
		}
	}
	for _, out := range outputs {
		target, err := c.InstallTargetDir(out.install, "CI workflow")
		var missing *registry.NoInstallDirError
		switch {
		case errors.As(err, &missing):
			c.Logf("  - %s CI of stack %s not installed: %s does not exist\n", out.name, st.Name, missing.Dir)
			continue
		case err != nil:
			return fmt.Errorf("stack %s: %w", st.Name, err)
		case target == "":
			c.Logf("  - %s CI of stack %s not installed: no repository holds %s\n", out.name, st.Name, c.Options.ServicePath)
			continue
		}
		if err := writeFiles(target, out.files); err != nil {
			return fmt.Errorf("stack %s: %w", st.Name, err)
		}
		c.Logf("  + %s CI of stack %s installed in %s\n", out.name, st.Name, target)
	}
	c.Done(Name, dir)
	return nil
}

// archives are the release's static archives by platform, with where
// each downloads from.
func archives(r release.Release) map[string]registry.CIArchive {
	out := map[string]registry.CIArchive{}
	for _, platform := range release.Platforms {
		if digest, ok := r.Archive(platform); ok {
			out[platform] = registry.CIArchive{URL: release.DownloadURL(r.Version, release.ArchiveName(r.Version, platform)), SHA256: digest}
		}
	}
	return out
}

// writeFiles writes each file under dir.
func writeFiles(dir string, files []registry.CIFile) error {
	for _, f := range files {
		p := filepath.Join(dir, filepath.FromSlash(f.Path))
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, f.Data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// stackOf locates the stack in the repository a CI job checks out: the
// repository root is the one the install resolves against, or the parent
// of the schemas root outside a repository. The schemas root is the
// parent of the services root, which holds the stack's service, as the CLI
// reads it.
func stackOf(c registry.GenerateContext, name string) (registry.CIStack, error) {
	if c.Options.ServicePath == "" {
		return registry.CIStack{}, fmt.Errorf("stack %s: the ci generator locates the stack from its service path, and the build has none", name)
	}
	dir, err := filepath.Abs(c.Options.ServicePath)
	if err != nil {
		return registry.CIStack{}, err
	}
	schemasRoot := filepath.Dir(filepath.Dir(dir))
	root, err := c.GitRoot()
	if err != nil {
		return registry.CIStack{}, err
	}
	if root == "" {
		root = filepath.Dir(schemasRoot)
	}
	rel := func(p string) (string, error) {
		r, err := filepath.Rel(root, p)
		if err != nil {
			return "", err
		}
		r = filepath.ToSlash(r)
		if r == ".." || strings.HasPrefix(r, "../") {
			return "", fmt.Errorf("stack %s: the schemas root %s lies outside the repository at %s, where a CI job starts", name, schemasRoot, root)
		}
		return r, nil
	}
	out := registry.CIStack{Name: name}
	if out.SchemasRoot, err = rel(schemasRoot); err != nil {
		return registry.CIStack{}, err
	}
	if out.Dir, err = rel(dir); err != nil {
		return registry.CIStack{}, err
	}
	out.ServicesRoot = path.Dir(out.Dir)
	// The workflow's build writes to the CLI's default output root, which
	// the stack commands read.
	out.OutputRoot = path.Join(out.SchemasRoot, "dist")
	out.PackageManager = packageManager(schemasRoot)
	return out, nil
}

// packageManager names the package manager of the schemas root by its
// lockfile: bun for bun.lock or bun.lockb, npm for package-lock.json, and
// none without one.
func packageManager(schemasRoot string) string {
	for _, lock := range []struct{ file, manager string }{
		{"bun.lock", registry.PackageManagerBun},
		{"bun.lockb", registry.PackageManagerBun},
		{"package-lock.json", registry.PackageManagerNPM},
	} {
		if _, err := os.Stat(filepath.Join(schemasRoot, lock.file)); err == nil {
			return lock.manager
		}
	}
	return ""
}
