package apigen

import (
	"strconv"
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

// readsEmptyBodyAsObject reports whether the route of endpoint reads an
// empty body as {}: the naming file says so
// (go_api.optional_input_empty_body) and the operation does not require
// its input. A multipart route reads no input from an empty JSON body, so
// it passes no option.
func readsEmptyBodyAsObject(router naming.GoAPIConfig, endpoint EndpointInfo) bool {
	return router.ReadsOptionalEmptyBodyAsObject() && !endpoint.InputRequired && !endpoint.HasFileUpload
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
	if router.IgnoresInputTrailingData() {
		opts = append(opts, "bodyargs.FirstValue()")
	}
	if readsEmptyBodyAsObject(router, endpoint) {
		opts = append(opts, "bodyargs.EmptyBodyAsObject()")
	}
	if namesStrictType(router, endpoint) {
		opts = append(opts, strictTypeOption(endpoint))
	}
	return joinOptions(opts)
}

// namesStrictType reports whether the route of endpoint names its
// @strictJSON input type to the runtime (go_api.strict_input_refusals).
func namesStrictType(router naming.GoAPIConfig, endpoint EndpointInfo) bool {
	return router.NamesStrictInputs() && endpoint.InputStrictJSON
}

func strictTypeOption(endpoint EndpointInfo) string {
	return "bodyargs.StrictType(" + strconv.Quote(endpoint.InputType) + ")"
}

// decodeRefusalOptions are the bodyargs.ReadOption arguments, each after a
// comma, the route of endpoint passes to bodyargs.DecodeRefusal.
func decodeRefusalOptions(router naming.GoAPIConfig, endpoint EndpointInfo) string {
	if namesStrictType(router, endpoint) {
		return joinOptions([]string{strictTypeOption(endpoint)})
	}
	return ""
}

// readInputNote is the comment line that says how the [go_api] table
// changes the reading of endpoint's input, empty when it does not.
func readInputNote(router naming.GoAPIConfig, endpoint EndpointInfo) string {
	var notes []string
	if keepsUndeclaredKeys(router, endpoint) {
		notes = append(notes, "a key the type does not declare goes on to its decoder")
	}
	if router.IgnoresInputTrailingData() {
		notes = append(notes, "the first JSON value is read and the rest of the body is not")
	}
	if readsEmptyBodyAsObject(router, endpoint) {
		notes = append(notes, "an empty body is the empty object")
	}
	if namesStrictType(router, endpoint) {
		notes = append(notes, "the refusals of the closed type are its decoder's")
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
