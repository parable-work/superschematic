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

// panelsBeforeTheConsole, which only the panels' tests set (through
// engine_panels_export_test.go), gives a crate whose pages are on, that
// mounts an engine and authenticates its callers, the engine pages' model
// while engine_pages.go builds none, so that engine_panels.rs is written
// and compiled. It goes once the engine console is built.
var panelsBeforeTheConsole bool

// enginePanelsOf is the engine panels of the crate: one whenever it has
// engine pages, whose instance page calls engine_panels::panel, and nil
// otherwise.
func enginePanelsOf(c registry.GenerateContext, out *crate) (*enginePanelsModel, error) {
	if out.EnginePages == nil && panelsBeforeTheConsole && out.Pages != nil && out.Engine != nil && out.HasAuth {
		out.EnginePages = &enginePagesModel{}
	}
	if out.EnginePages == nil {
		return nil, nil
	}
	return &enginePanelsModel{}, nil
}
