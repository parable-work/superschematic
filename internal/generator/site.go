package generator

import (
	"github.com/parable-work/superschematic/internal/generator/servergen"
	"github.com/parable-work/superschematic/internal/generator/sitegen"
	"github.com/parable-work/superschematic/internal/generator/tsgen"
)

// generateSite writes a Site service's typed browser config into its
// package, at the naming file's [implementation_paths] site template under
// the repository root, scaffolding the package once when it is missing,
// and the Bun workspace's root, of which the package is a member (D55). A
// build with no repository root, such as a build into a scratch output
// root alone, writes nothing.
//
// In an output root other than the default one (tsgen.Mirrored), whose Bun
// workspace lists the mirrors under impl/ in place of the project's
// packages, it also brings the site's mirror there up to date with the
// package, which the site builds in (D65, amended): the root's install
// links the mirror, and leaves the package's node_modules as the default
// root's install linked them.
func (r run) generateSite() error {
	if r.Options.RepositoryRoot == "" {
		r.Skip(sitegen.Name)
		return nil
	}
	site, err := sitegen.Of(r.Schema, r.Options.Naming)
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
	if tsgen.Mirrored(r.Options.OutputRoot, r.Options.SchemasRoot) {
		m, err := servergen.PlanSiteMirror(r.Options.OutputRoot, tsgen.DefaultOutputRoot(r.Options.SchemasRoot), r.Config.Name, dir)
		if err != nil {
			return err
		}
		if err := servergen.WriteTypeScriptMirror(m); err != nil {
			return err
		}
	}
	if err := r.writeTypeScriptWorkspace(); err != nil {
		return err
	}
	r.Done(sitegen.Name, dir)
	return nil
}
