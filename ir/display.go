package ir

import (
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// TypeDisplay is what a UI, or an agent, reads to render the instances of
// a type, declared with @display (D48): what to call one and many, which
// field is an instance's title, what a create button says, which fields
// summarize it in a list, and how to label its Workflow's states and
// transitions. It adds no field, operation or storage, and no generator
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

// ValidateTypeDisplay checks a display's shape, as the JSON Schema of
// @display's argument does, for IR built without a frontend, and returns
// the first problem: at least one member, every text non-blank, each
// summary field named once, a state name per states key with a known tone
// and at least one member, and a state name per transitions key, each
// holding at least one transition. Whether
// the fields and states it names exist is the loader's check, which reads
// the type and its behaviors.
func ValidateTypeDisplay(d *TypeDisplay) error {
	if d == nil {
		return nil
	}
	if d.Noun == "" && d.Plural == "" && d.TitleField == "" && d.CreateLabel == "" &&
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
