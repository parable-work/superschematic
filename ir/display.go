package ir

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// TypeDisplay is what a UI, or an agent, reads to render the instances of
// a type, declared with @display (D48): what to call one and many, which
// field is an instance's title, what a create button says, the glyph shown
// beside its name, which fields summarize it in a list, and how to label
// its Workflow's states and transitions. It adds no field, operation or storage, and no generator
// renders it. The data forms write it as the decorator's argument.
type TypeDisplay struct {
	// Noun names one instance: "Ticket".
	Noun string `json:"noun,omitempty" yaml:"noun,omitempty"`

	// Plural names several: "Tickets".
	Plural string `json:"plural,omitempty" yaml:"plural,omitempty"`

	// TitleField names the field whose value is an instance's title: one
	// of the type's own fields, holding a single text value.
	TitleField string `json:"titleField,omitempty" yaml:"titleField,omitempty"`

	// CreateLabel is what a button that creates an instance says: "New
	// ticket".
	CreateLabel string `json:"createLabel,omitempty" yaml:"createLabel,omitempty"`

	// Icon names the glyph a UI shows beside the type's name (D48,
	// amended): "receipt", a name for an app's icon set to draw, matching
	// DisplayIconName. Which names a set draws is an extension's check.
	Icon string `json:"icon,omitempty" yaml:"icon,omitempty"`

	// SummaryFields name the fields that summarize an instance in a list,
	// in order: the type's own fields, or fields its behaviors add.
	SummaryFields []string `json:"summaryFields,omitempty" yaml:"summaryFields,omitempty"`

	// States labels states of the type's Workflow, keyed by state.
	States map[string]DisplayState `json:"states,omitempty" yaml:"states,omitempty"`

	// Transitions labels transitions of the type's Workflow, keyed by the
	// state a transition leaves, then by the state it enters: a UI that
	// shows an instance's moves reads Transitions[status].
	Transitions map[string]map[string]string `json:"transitions,omitempty" yaml:"transitions,omitempty"`
}

// DisplayState is how a UI shows one Workflow state.
type DisplayState struct {
	// Label names the state: "In review".
	Label string `json:"label,omitempty" yaml:"label,omitempty"`

	// ActiveForm is the present-progressive form a UI shows while an
	// instance is in the state: "Implementing".
	ActiveForm string `json:"activeForm,omitempty" yaml:"activeForm,omitempty"`

	// Tone is what the state means to a reader, which a UI maps onto its
	// own colors: one of [DisplayTones].
	Tone DisplayTone `json:"tone,omitempty" yaml:"tone,omitempty"`
}

// DisplayTone is what a state means to a reader. The set is closed.
type DisplayTone string

// The display tones.
const (
	// DisplayToneMuted is a state nothing happens in: a draft, a backlog.
	DisplayToneMuted DisplayTone = "muted"
	// DisplayToneActive is a state work is under way in.
	DisplayToneActive DisplayTone = "active"
	// DisplayToneSuccess is a state that went well.
	DisplayToneSuccess DisplayTone = "success"
	// DisplayToneWarning is a state that needs attention.
	DisplayToneWarning DisplayTone = "warning"
	// DisplayToneDanger is a state that went badly.
	DisplayToneDanger DisplayTone = "danger"
)

// DisplayTones lists the tones a state may take, in order.
func DisplayTones() []DisplayTone {
	return []DisplayTone{DisplayToneMuted, DisplayToneActive, DisplayToneSuccess, DisplayToneWarning, DisplayToneDanger}
}

// DisplayStateName is a state name a display labels: a letter, then
// letters, digits, `_` and `-`, as a Workflow config's states are.
var DisplayStateName = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_-]*$`)

// DisplayIconName is a type display's icon (D48, amended): a letter or a
// digit, then letters, digits, ".", "_", ":", "/" and "-", as in
// "receipt", "mdi:receipt" or "solid/box". Its classes are ASCII and
// explicit, so the schema-file JSON Schema's pattern, which is this one,
// reads the same in Go's RE2, an ECMAScript engine such as Ajv's and the
// Rust regex crate.
var DisplayIconName = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._:/-]*$`)

// ValidateTypeDisplay checks a display's shape, as the JSON Schema of
// @display's argument does, for IR built without a frontend, and returns
// the first problem: at least one member, every text non-blank, an icon
// that is an icon name (DisplayIconName), each summary field named once, a state name per states key with a known tone
// and at least one member, and a state name per transitions key, each
// holding at least one transition. Whether
// the fields and states it names exist is the loader's check, which reads
// the type and its behaviors.
func ValidateTypeDisplay(d *TypeDisplay) error {
	if d == nil {
		return nil
	}
	if d.Noun == "" && d.Plural == "" && d.TitleField == "" && d.CreateLabel == "" && d.Icon == "" &&
		len(d.SummaryFields) == 0 && len(d.States) == 0 && len(d.Transitions) == 0 {
		return errors.New("display declares nothing")
	}
	var errs []error
	text := func(name, value string) {
		if value != "" && strings.TrimSpace(value) == "" {
			errs = append(errs, fmt.Errorf("display %s must be non-blank", name))
		}
	}
	text("noun", d.Noun)
	text("plural", d.Plural)
	text("titleField", d.TitleField)
	text("createLabel", d.CreateLabel)
	if d.Icon != "" && !DisplayIconName.MatchString(d.Icon) {
		errs = append(errs, fmt.Errorf("display icon %q is not an icon name (a letter or a digit, then letters, digits, ., _, :, / and -)", d.Icon))
	}
	if d.SummaryFields != nil && len(d.SummaryFields) == 0 {
		errs = append(errs, errors.New("display summaryFields must list a field"))
	}
	for i, name := range d.SummaryFields {
		if strings.TrimSpace(name) == "" {
			errs = append(errs, fmt.Errorf("display summaryFields[%d] must be non-blank", i))
		} else if slices.Index(d.SummaryFields, name) != i {
			errs = append(errs, fmt.Errorf("display summaryFields lists %s twice", name))
		}
	}
	for _, name := range sortedStringMapKeys(d.States) {
		state := d.States[name]
		if !DisplayStateName.MatchString(name) {
			errs = append(errs, fmt.Errorf("display states key %q is not a state name (a letter, then letters, digits, _ and -)", name))
		}
		if state == (DisplayState{}) {
			errs = append(errs, fmt.Errorf("display state %s declares nothing", name))
		}
		text("state "+name+" label", state.Label)
		text("state "+name+" activeForm", state.ActiveForm)
		if state.Tone != "" && !slices.Contains(DisplayTones(), state.Tone) {
			errs = append(errs, fmt.Errorf("display state %s tone %q is not one of muted, active, success, warning, danger", name, state.Tone))
		}
	}
	for _, from := range sortedStringMapKeys(d.Transitions) {
		if !DisplayStateName.MatchString(from) {
			errs = append(errs, fmt.Errorf("display transitions key %q is not a state name (a letter, then letters, digits, _ and -)", from))
		}
		if len(d.Transitions[from]) == 0 {
			errs = append(errs, fmt.Errorf("display transitions from %s label none", from))
		}
		for _, to := range sortedStringMapKeys(d.Transitions[from]) {
			if !DisplayStateName.MatchString(to) {
				errs = append(errs, fmt.Errorf("display transitions from %s key %q is not a state name (a letter, then letters, digits, _ and -)", from, to))
			}
			if label := d.Transitions[from][to]; strings.TrimSpace(label) == "" {
				errs = append(errs, fmt.Errorf("display transition from %s to %s must be non-blank", from, to))
			}
		}
	}
	if len(errs) > 0 {
		return errs[0]
	}
	return nil
}

// FieldDisplay is how a UI renders a field's control and value, declared
// with @display on the field (D48, amended). The data forms write it under
// the field's display. It adds no storage or validation, and no type, SDK
// or server generator renders it.
type FieldDisplay struct {
	// Multiline asks for a control that holds several lines when true, and
	// one line when false, whatever the field's maxLength; nil leaves it to
	// the UI.
	Multiline *bool `json:"multiline,omitempty" yaml:"multiline,omitempty"`

	// Currency names the currency of the amount in minor units the field
	// holds: a literal ISO 4217 code, or a field of the same type that
	// holds one. nil names none.
	Currency *DisplayCurrency `json:"currency,omitempty" yaml:"currency,omitempty"`
}

// DisplayCurrency is the currency of a field's amount (D48, amended), in
// D70's two forms: a literal ISO 4217 alphabetic code, which the data forms
// write as the string itself ("GBP"), or a field of the same type that
// holds the code, written as an object ({ field: "currency" }). One of the
// two is set.
type DisplayCurrency struct {
	// Code is a literal currency, three capital letters: "GBP". The loader
	// holds it to an active ISO 4217 code.
	Code string `json:"-" yaml:"-"`

	// Field names a field of the same type, by its name or its JSON key,
	// that holds the currency: a Finance.Currency, or text.
	Field string `json:"field" yaml:"field"`
}

// displayCurrencyCode is the shape of a literal display currency: three
// capital ASCII letters, as Finance.Currency's pattern states it.
var displayCurrencyCode = regexp.MustCompile(`^[A-Z]{3}$`)

// displayCurrencyField is the object form of a display currency.
type displayCurrencyField struct {
	Field string `json:"field" yaml:"field"`
}

// MarshalJSON writes a literal currency as its code and a field's as
// {"field": name}.
func (c DisplayCurrency) MarshalJSON() ([]byte, error) {
	switch {
	case c.Code != "" && c.Field != "":
		return nil, errors.New("display currency is both a code and a field")
	case c.Code != "":
		return json.Marshal(c.Code)
	}
	return json.Marshal(displayCurrencyField{Field: c.Field})
}

// UnmarshalJSON reads a code, a string, or a field, an object with field
// alone.
func (c *DisplayCurrency) UnmarshalJSON(data []byte) error {
	var code string
	if err := json.Unmarshal(data, &code); err == nil {
		*c = DisplayCurrency{Code: code}
		return nil
	}
	var field displayCurrencyField
	decoder := json.NewDecoder(bytes.NewReader(data))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&field); err != nil {
		return fmt.Errorf("display currency is a code or {field}: %w", err)
	}
	*c = DisplayCurrency{Field: field.Field}
	return nil
}

// MarshalYAML writes the currency as MarshalJSON does.
func (c DisplayCurrency) MarshalYAML() (any, error) {
	switch {
	case c.Code != "" && c.Field != "":
		return nil, errors.New("display currency is both a code and a field")
	case c.Code != "":
		return c.Code, nil
	}
	return displayCurrencyField{Field: c.Field}, nil
}

// UnmarshalYAML reads the currency as UnmarshalJSON does.
func (c *DisplayCurrency) UnmarshalYAML(unmarshal func(any) error) error {
	var code string
	if err := unmarshal(&code); err == nil {
		*c = DisplayCurrency{Code: code}
		return nil
	}
	var field map[string]any
	if err := unmarshal(&field); err != nil {
		return fmt.Errorf("display currency is a code or {field}: %w", err)
	}
	*c = DisplayCurrency{}
	for key, value := range field {
		name, ok := value.(string)
		if key != "field" || !ok {
			return fmt.Errorf("display currency is a code or {field}, got the key %q", key)
		}
		c.Field = name
	}
	return nil
}

// ValidateFieldDisplay checks a field display's shape, as the JSON Schema
// of a field's @display argument does, for IR built without a frontend: it
// must declare something, and a currency is three capital letters or a
// non-blank field's name. Whether the field holds text for multiline, or
// an amount for currency, whether a code is an active ISO 4217 one, and
// whether the field a currency names exists, are the loader's checks,
// which read the field's type and its siblings.
func ValidateFieldDisplay(d *FieldDisplay) error {
	if d == nil {
		return nil
	}
	if d.Multiline == nil && d.Currency == nil {
		return errors.New("display declares nothing")
	}
	if c := d.Currency; c != nil {
		switch {
		case c.Code != "" && c.Field != "":
			return errors.New("display currency is both a code and a field")
		case c.Code != "":
			if !displayCurrencyCode.MatchString(c.Code) {
				return fmt.Errorf("display currency %q is not an ISO 4217 code: three capital letters", c.Code)
			}
		case strings.TrimSpace(c.Field) == "":
			return errors.New("display currency must be a code or a non-blank field")
		}
	}
	return nil
}
