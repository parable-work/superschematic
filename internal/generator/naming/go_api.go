package naming

import (
	"fmt"
	"slices"
	"strings"
)

// GoAPIConfig is the [go_api] table of superschematic.toml: how the
// generated Go API routers answer a request they refuse, and the symbols
// they write beside the routes. Every key defaults to the core's router.
// A project whose clients and hand-written handlers know the answers of
// another router sets these keys, so that the generated router answers
// each request as that router did. The TypeScript and Rust routers do not
// read the table.
type GoAPIConfig struct {
	// ValidationErrorCode is the code of the validation problem the
	// router sends for a path, query or filter value that breaks a rule.
	// Empty (default), the router passes no code and the runtime sends
	// its own.
	ValidationErrorCode string `toml:"validation_error_code"`

	// PathParamError is the detail of the 400 the router sends for a path
	// parameter that does not percent-decode to UTF-8. {name} is the
	// parameter's name. Empty (default), it is DefaultPathParamError.
	PathParamError string `toml:"path_param_error"`

	// ValidatorNames renames the validators of a route's own checks of its
	// path and query values: a bound of a number (min, max), of a string's
	// length (minLength, maxLength), a pattern, and the bounds of a query
	// list (listMin, listMax). Each key is the core's name and its value
	// the name the route sends. A validator it does not list keeps the
	// core's name. The rules of a body, which the input type's Validate
	// checks, are the types generator's, and keep their names.
	ValidatorNames map[string]string `toml:"validator_names"`
}

// RouteValidators are the validators of a route's own checks, which
// ValidatorNames may rename.
var RouteValidators = []string{"min", "max", "minLength", "maxLength", "pattern", "listMin", "listMax"}

// ValidatorNameOf is the name the route sends for the core's validator
// name.
func (c GoAPIConfig) ValidatorNameOf(name string) string {
	if renamed, ok := c.ValidatorNames[name]; ok {
		return renamed
	}
	return name
}

// The placeholder of a [go_api] text and the core's texts.
const (
	GoAPINamePlaceholder  = "{name}"
	DefaultPathParamError = "{name} must be percent-encoded UTF-8"
)

// PathParamErrorOf is the detail of the 400 for the path parameter name
// that does not decode.
func (c GoAPIConfig) PathParamErrorOf(name string) string {
	text := c.PathParamError
	if text == "" {
		text = DefaultPathParamError
	}
	return strings.ReplaceAll(text, GoAPINamePlaceholder, name)
}

// check refuses a value the [go_api] table does not take.
func (c GoAPIConfig) check() error {
	for _, kv := range [][2]string{{"validation_error_code", c.ValidationErrorCode}, {"path_param_error", c.PathParamError}} {
		if err := checkGoAPIText(kv[0], kv[1]); err != nil {
			return err
		}
	}
	for from, to := range c.ValidatorNames {
		if !slices.Contains(RouteValidators, from) {
			return fmt.Errorf("go_api.validator_names renames %q, which no route check names: rename one of %s", from, strings.Join(RouteValidators, ", "))
		}
		if to == "" {
			return fmt.Errorf("go_api.validator_names renames %q to an empty name", from)
		}
		if err := checkGoAPIText("validator_names."+from, to); err != nil {
			return err
		}
	}
	return nil
}

// checkGoAPIText refuses a text the router writes into a Go string
// literal when it holds a line break, a NUL, a quote or a backslash.
func checkGoAPIText(key, value string) error {
	if strings.ContainsAny(value, "\x00\n\r\"\\") {
		return fmt.Errorf("go_api.%s %q holds a line break, a NUL, a quote or a backslash: write one line of plain text", key, value)
	}
	return nil
}
