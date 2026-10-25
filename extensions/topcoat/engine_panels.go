package topcoat

import "github.com/parable-work/superschematic/registry"

// enginePanelsModel is what engine_panels.tmpl renders: a panel per
// behavior the crate knows, which an engine page shows on an instance's
// page (engine_panels::panel). The core's are Workflow, Approvals and
// Branches; the commerce behaviors' are in engine_panels_commerce.tmpl.
// Each reads the type's describe document and the instance's record at
// run time, so the model holds nothing of its own: the template reads the
// crate's engine and its pages' prefix.
type enginePanelsModel struct{}

// enginePanelsOf is the engine panels of the crate: one whenever it has
// engine pages, whose instance page calls engine_panels::panel, and nil
// otherwise.
func enginePanelsOf(c registry.GenerateContext, out *crate) (*enginePanelsModel, error) {
	if out.EnginePages == nil {
		return nil, nil
	}
	return &enginePanelsModel{}, nil
}
