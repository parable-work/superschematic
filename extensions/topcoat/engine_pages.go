package topcoat

import "github.com/parable-work/superschematic/registry"

// enginePagesModel is what engine_pages.tmpl renders: the pages of the
// engine database's types under <prefix>/engine (D44, amended), which call
// the engine in-process as the page's caller.
type enginePagesModel struct {
	// Path is where they are served: the pages' prefix, then /engine.
	Path string
}

// enginePagesOf is the engine pages of the crate, nil when it has none: its
// pages are off, it mounts no engine, or pages.engine is false. out holds
// the crate's Pages and Engine, which newCrate builds first. The build log
// says why a crate whose pages and engine are on has none: no route of its
// API authenticates a caller, and a page acts only as its caller, never as
// the engine's system principal; or the path is taken, by the engine's own
// routes or by a collection of the API's.
func enginePagesOf(c registry.GenerateContext, cfg Config, out *crate) (*enginePagesModel, error) {
	if out.Pages == nil || out.Engine == nil || !cfg.PagesEngine() {
		return nil, nil
	}
	path := out.Pages.Prefix + EngineMount
	if !out.HasAuth {
		c.Logf("  - topcoat: no engine pages at %s: no route of the API authenticates a caller, and a page calls the engine only as its caller, whom the engine's policy admits\n", path)
		return nil, nil
	}
	if under(path, EngineMount) {
		c.Logf("  - topcoat: no engine pages at %s: the path is under %s, where the crate mounts the engine's own routes\n", path, EngineMount)
		return nil, nil
	}
	for _, resource := range out.Pages.Resources {
		if under(resource.Path, path) || under(resource.ItemPath, path) {
			c.Logf("  - topcoat: no engine pages at %s: the API's own collection %s is served there\n", path, resource.Name)
			return nil, nil
		}
	}
	return &enginePagesModel{Path: path}, nil
}
