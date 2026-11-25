package apigen

import (
	"strings"

	"github.com/parable-work/superschematic/internal/generator/naming"
)

// keepsUndeclaredKeys reports whether the route of endpoint passes a key
// its input type does not declare on to the type's decoder: the naming
// file says so (go_api.undeclared_input_keys) and the type is open, not a
// closed contract and not one whose keys every router drops.
func keepsUndeclaredKeys(router naming.GoAPIConfig, endpoint EndpointInfo) bool {
	return router.KeepsUndeclaredInputKeys() && !endpoint.InputClosed && !endpoint.InputAllowsUnknownFields
}

// readInputOptions are the bodyargs.ReadOption arguments, each after a
// comma, the route of endpoint passes to bodyargs.ReadInput, as the naming
// file's [go_api] table says. With the table at its defaults they are
// empty, and the route reads its input as every generated server does.
func readInputOptions(router naming.GoAPIConfig, endpoint EndpointInfo) string {
	var opts []string
	if keepsUndeclaredKeys(router, endpoint) {
		opts = append(opts, "bodyargs.KeepUndeclaredKeys()")
	}
	return joinOptions(opts)
}

// readInputNote is the comment line that says how the [go_api] table
// changes the reading of endpoint's input, empty when it does not.
func readInputNote(router naming.GoAPIConfig, endpoint EndpointInfo) string {
	var notes []string
	if keepsUndeclaredKeys(router, endpoint) {
		notes = append(notes, "a key the type does not declare goes on to its decoder")
	}
	if len(notes) == 0 {
		return ""
	}
	return "As the naming file's [go_api] table says, " + strings.Join(notes, "; ") + "."
}

func joinOptions(opts []string) string {
	if len(opts) == 0 {
		return ""
	}
	return ", " + strings.Join(opts, ", ")
}
