package topcoat

import "github.com/parable-work/superschematic/registry"

// enginePagesModel is what engine_pages.tmpl renders: the pages of the
// engine database's types under <prefix>/engine (D44, amended), which call
// the engine in-process as the page's caller.
type enginePagesModel struct{}

// enginePagesOf is the engine pages of the crate, nil when it has none: its
// pages are off, it mounts no engine, or pages.engine is false. out holds
// the crate's Pages, Engine and Records, which newCrate builds first. The
// engine console is not built yet, so the crate has none, and the build log
// says so where it would.
func enginePagesOf(c registry.GenerateContext, cfg Config, out *crate) (*enginePagesModel, error) {
	if out.Pages == nil || out.Engine == nil || !cfg.PagesEngine() {
		return nil, nil
	}
	c.Logf("  - topcoat: no engine pages at %s%s: the engine console is not built yet\n", out.Pages.Prefix, EngineMount)
	return nil, nil
}
