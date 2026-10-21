package generator

import (
	"github.com/parable-work/superschematic/internal/generator/sitegen"
	"github.com/parable-work/superschematic/internal/registry"
)

// generateSite writes a Site service's typed browser config into its
// package, at the naming file's [implementation_paths] site template under
// the repository root, scaffolding the package once when it is missing,
// and the Bun workspace's root, of which the package is a member (D55). A
// build with no repository root, such as a build into a scratch output
// root alone, writes nothing.
func (r run) generateSite() error {
	if r.Options.RepositoryRoot == "" {
		r.Skip(sitegen.Name)
		return nil
	}
	site, err := sitegen.Of(r.Schema, r.Options.Naming, r.siteSDKs)
	if err != nil {
		return err
	}
	dir := r.Options.Naming.SiteImplementationDir(r.Options.RepositoryRoot, r.Config.Name)
	scaffolded, err := site.Write(dir)
	if err != nil {
		return err
	}
	if scaffolded {
		r.Logf("  + site scaffold of %s written to %s\n", r.Config.Name, dir)
	}
	if err := r.writeTypeScriptWorkspace(); err != nil {
		return err
	}
	r.Done(sitegen.Name, dir)
	return nil
}

// siteSDKs reports whether the config of an API the site calls generates
// a TypeScript SDK, of which the site's config hands out a client (D55,
// amended). An API whose config the build cannot read is taken to have
// one, as a TypeScript server's Deps take it (typeScriptDeps).
func (r run) siteSDKs(service string) bool {
	if r.Options.DependencyConfig == nil {
		return true
	}
	cfg, ok := r.Options.DependencyConfig(service)
	if !ok {
		return true
	}
	outputs, err := registry.ParseOutputs(cfg.Outputs, r.Registry)
	if err != nil {
		return true
	}
	return outputs.SDKEnabled(LangTypeScript)
}
