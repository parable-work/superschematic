package topcoat

import "github.com/parable-work/superschematic/registry"

// enginePanelsModel is what engine_panels.tmpl renders: a panel per
// behavior the crate knows, which an engine page shows on an instance's
// page (engine_panels::panel), the commerce behaviors' in
// engine_panels_commerce.tmpl.
type enginePanelsModel struct{}

// enginePanelsOf is the engine panels of the crate: one whenever it has
// engine pages, whose instance page calls engine_panels::panel, and nil
// otherwise. No panel is built yet, so every behavior has the engine
// pages' generic panel.
func enginePanelsOf(c registry.GenerateContext, out *crate) (*enginePanelsModel, error) {
	if out.EnginePages == nil {
		return nil, nil
	}
	c.Logf("  - topcoat: no behavior panels for the engine pages: the panels are not built yet, so each behavior has the generic panel\n")
	return &enginePanelsModel{}, nil
}
