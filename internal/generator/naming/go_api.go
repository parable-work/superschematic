package naming

import (
	"fmt"
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
}

// check refuses a value the [go_api] table does not take.
func (c GoAPIConfig) check() error {
	for _, kv := range [][2]string{{"validation_error_code", c.ValidationErrorCode}} {
		if err := checkGoAPIText(kv[0], kv[1]); err != nil {
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
