package ir

import (
	"fmt"
	"slices"
	"strings"
)

// UIControls are the controls @uiControl may name: the controls a schema
// form picks among for a number, an enum, an array of an enum and a string.
var UIControls = []string{
	"slider",
	"number-field",
	"switch",
	"segmented",
	"radio",
	"select",
	"combobox",
	"checkboxes",
	"input",
	"textarea",
}

// ValidateUIControl reports a @uiControl value that is not one of UIControls.
func ValidateUIControl(control string) error {
	if slices.Contains(UIControls, control) {
		return nil
	}
	return fmt.Errorf("unknown control %q; use one of %s", control, strings.Join(UIControls, ", "))
}
