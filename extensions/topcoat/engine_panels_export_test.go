package topcoat

import "testing"

// PanelsBeforeTheConsole has the crates t builds write and compile the
// engine panels, as the engine pages' would (panelsBeforeTheConsole),
// until t ends.
func PanelsBeforeTheConsole(t testing.TB) {
	panelsBeforeTheConsole = true
	t.Cleanup(func() { panelsBeforeTheConsole = false })
}
