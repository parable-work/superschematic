package ir

// ValueRules are the Validate<T, C> rules a field or an argument declares on
// its own value (D12, amended): min and max bound a number, minLength and
// maxLength a text's length in code points, pattern is a regular expression
// a text matches, and listMin and listMax bound a list's item count, the
// outer list of T[][]. The length, pattern and range rules apply to a single
// value and to every element of a list.
//
// FieldDef and ArgumentDef embed it, so its keys sit flat beside theirs in
// the IR's JSON and YAML; the generators' parameter models embed it too, so
// the seven rules and what is derived from them are written once.
type ValueRules struct {
	// ValidateMin is the minimum numeric value allowed (@validateMin). Nil
	// means no minimum.
	ValidateMin *float64 `json:"validateMin,omitempty" yaml:"validateMin,omitempty"`

	// ValidateMax is the maximum numeric value allowed (@validateMax). Nil
	// means no maximum.
	ValidateMax *float64 `json:"validateMax,omitempty" yaml:"validateMax,omitempty"`

	// ValidateMinLength is the minimum character length for string-like
	// values (@validateMinLength). Nil means no minimum.
	ValidateMinLength *int `json:"validateMinLength,omitempty" yaml:"validateMinLength,omitempty"`

	// ValidateMaxLength is the maximum character length for string-like
	// values (@validateMaxLength). Nil means no maximum.
	ValidateMaxLength *int `json:"validateMaxLength,omitempty" yaml:"validateMaxLength,omitempty"`

	// ValidateListMin is the minimum list cardinality (item count)
	// (@validateListMin). Nil means no minimum.
	ValidateListMin *int `json:"validateListMin,omitempty" yaml:"validateListMin,omitempty"`

	// ValidateListMax is the maximum list cardinality (item count)
	// (@validateListMax). Nil means no maximum.
	ValidateListMax *int `json:"validateListMax,omitempty" yaml:"validateListMax,omitempty"`

	// ValidatePattern is the regex pattern a text value matches
	// (@validatePattern). Empty string means no pattern.
	ValidatePattern string `json:"validatePattern,omitempty" yaml:"validatePattern,omitempty"`
}

// Any reports whether any of the rules is set.
func (r ValueRules) Any() bool {
	return r.HasRange() || r.HasText() || r.HasList()
}

// HasRange reports whether min or max bounds a number.
func (r ValueRules) HasRange() bool {
	return r.ValidateMin != nil || r.ValidateMax != nil
}

// HasText reports whether a length or pattern rule checks a text.
func (r ValueRules) HasText() bool {
	return r.ValidateMinLength != nil || r.ValidateMaxLength != nil || r.ValidatePattern != ""
}

// HasLength reports whether minLength or maxLength bounds a text.
func (r ValueRules) HasLength() bool {
	return r.ValidateMinLength != nil || r.ValidateMaxLength != nil
}

// HasList reports whether listMin or listMax bounds a list.
func (r ValueRules) HasList() bool {
	return r.ValidateListMin != nil || r.ValidateListMax != nil
}

// HasValue reports whether a rule checks a single value, or each element
// of a list: a range, length or pattern rule.
func (r ValueRules) HasValue() bool {
	return r.HasRange() || r.HasText()
}

// Element is the rules an element of a list takes: the value rules, without
// the list bounds, which bound the list itself.
func (r ValueRules) Element() ValueRules {
	r.ValidateListMin, r.ValidateListMax = nil, nil
	return r
}

// Names names the rules that are set, in the order Validate's config lists
// them: min, max, minLength, maxLength, listMin, listMax, pattern.
func (r ValueRules) Names() []string {
	var names []string
	for _, rule := range []struct {
		name string
		set  bool
	}{
		{"min", r.ValidateMin != nil},
		{"max", r.ValidateMax != nil},
		{"minLength", r.ValidateMinLength != nil},
		{"maxLength", r.ValidateMaxLength != nil},
		{"listMin", r.ValidateListMin != nil},
		{"listMax", r.ValidateListMax != nil},
		{"pattern", r.ValidatePattern != ""},
	} {
		if rule.set {
			names = append(names, rule.name)
		}
	}
	return names
}
