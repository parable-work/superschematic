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

	// UndeclaredInputKeys is "refuse" (default) or "keep". "refuse" refuses
	// an input body with a top-level key its type does not declare, as
	// every generated server does (D42). "keep" passes the key on to the
	// type's decoder, which ignores it or, for a key in another case, reads
	// it into the field it matches, as encoding/json does. A closed type,
	// @strictJSON or @denyUnknownFields, is refused under either value, and
	// an @allowUnknownFields type's keys are dropped under either value.
	UndeclaredInputKeys string `toml:"undeclared_input_keys"`

	// InputTrailingData is "refuse" (default) or "ignore". "refuse" reads
	// the whole input body and refuses one with anything after its JSON
	// value. "ignore" reads the first JSON value, as a json.Decoder reads
	// it, and leaves the rest of the body unread: trailing data and a
	// second value are not refused.
	InputTrailingData string `toml:"input_trailing_data"`

	// OptionalInputEmptyBody is "refuse" (default) or "object". "refuse"
	// refuses an empty body, as for any input. "object" reads the empty
	// body of an input the operation does not require as the empty object
	// {}, which the input type then decodes and validates.
	OptionalInputEmptyBody string `toml:"optional_input_empty_body"`

	// StrictInputRefusals is "route" (default) or "decoder". "route"
	// refuses a @strictJSON input's undeclared key with the problem of
	// every generated server and a string an enum lacks as an "enum" field
	// error. "decoder" names the type to the runtime (bodyargs.StrictType),
	// so each refusal is the type's decoder's: the runtime names the type
	// in the refusal of an undeclared key, and a string an enum lacks does
	// not match the declared type.
	StrictInputRefusals string `toml:"strict_input_refusals"`
}

// The values of strict_input_refusals.
const (
	GoAPIRoute   = "route"
	GoAPIDecoder = "decoder"
)

// NamesStrictInputs reports whether strict_input_refusals is "decoder".
func (c GoAPIConfig) NamesStrictInputs() bool { return c.StrictInputRefusals == GoAPIDecoder }

// GoAPIObject is the "object" value of optional_input_empty_body.
const GoAPIObject = "object"

// ReadsOptionalEmptyBodyAsObject reports whether optional_input_empty_body
// is "object".
func (c GoAPIConfig) ReadsOptionalEmptyBodyAsObject() bool {
	return c.OptionalInputEmptyBody == GoAPIObject
}

// The values of go_api.undeclared_input_keys and input_trailing_data.
const (
	GoAPIRefuse = "refuse"
	GoAPIKeep   = "keep"
	GoAPIIgnore = "ignore"
)

// IgnoresInputTrailingData reports whether input_trailing_data is
// "ignore".
func (c GoAPIConfig) IgnoresInputTrailingData() bool { return c.InputTrailingData == GoAPIIgnore }

// KeepsUndeclaredInputKeys reports whether undeclared_input_keys is
// "keep".
func (c GoAPIConfig) KeepsUndeclaredInputKeys() bool { return c.UndeclaredInputKeys == GoAPIKeep }

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
	if err := checkGoAPIChoice("undeclared_input_keys", c.UndeclaredInputKeys, GoAPIRefuse, GoAPIKeep); err != nil {
		return err
	}
	if err := checkGoAPIChoice("input_trailing_data", c.InputTrailingData, GoAPIRefuse, GoAPIIgnore); err != nil {
		return err
	}
	if err := checkGoAPIChoice("optional_input_empty_body", c.OptionalInputEmptyBody, GoAPIRefuse, GoAPIObject); err != nil {
		return err
	}
	if err := checkGoAPIChoice("strict_input_refusals", c.StrictInputRefusals, GoAPIRoute, GoAPIDecoder); err != nil {
		return err
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

// checkGoAPIChoice refuses a value of key other than empty and the values
// it takes.
func checkGoAPIChoice(key, value string, values ...string) error {
	if value == "" || slices.Contains(values, value) {
		return nil
	}
	quoted := make([]string, len(values))
	for i, v := range values {
		quoted[i] = fmt.Sprintf("%q", v)
	}
	return fmt.Errorf("go_api.%s is %q: use %s", key, value, strings.Join(quoted, " or "))
}

// checkGoAPIText refuses a text the router writes into a Go string
// literal when it holds a line break, a NUL, a quote or a backslash.
func checkGoAPIText(key, value string) error {
	if strings.ContainsAny(value, "\x00\n\r\"\\") {
		return fmt.Errorf("go_api.%s %q holds a line break, a NUL, a quote or a backslash: write one line of plain text", key, value)
	}
	return nil
}
