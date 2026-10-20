package ir

import (
	"errors"
	"fmt"
	"slices"
	"strings"
	"unicode/utf8"
)

// EnumDef is a format-agnostic representation of an enumeration type.
type EnumDef struct {
	// Name is the enum type name.
	Name string `json:"name" yaml:"name"`

	// Owner identifies the schema that owns this enum definition.
	// Empty means ownership was not set during extraction/merge.
	Owner string `json:"owner,omitempty" yaml:"owner,omitempty"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the enum declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Values lists all enum values in declaration order.
	Values []EnumValueDef `json:"values" yaml:"values"`

	// Origin is [OriginVersionGraph] for an enum a version graph adds;
	// empty for every authored enum.
	Origin string `json:"origin,omitempty" yaml:"origin,omitempty" jsonschema:"-"`
}

// EnumValueDef represents a single value within an enumeration.
type EnumValueDef struct {
	// Name is the enum value name as declared (e.g., "Pending", "Running").
	Name string `json:"name" yaml:"name"`

	// Title is the member's reader-facing label (D48, amended), declared
	// with a JSDoc @title tag on the member in TypeScript or a `title` key
	// in the data forms: "Held for a stock check". Empty means a UI derives
	// one from Name.
	Title string `json:"title,omitempty" yaml:"title,omitempty"`

	// Description is the human-readable description of this value.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for this enum value.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// SerializedAs is the custom serialization value (e.g., "pending").
	// Empty string means the value serializes as its Name.
	SerializedAs string `json:"serializedAs,omitempty" yaml:"serializedAs,omitempty"`
}

// EnumTitlePattern is the rule of an enum member's title (D48, amended),
// as the schema-file JSON Schema writes it, beside "not": {"pattern":
// "\\*/"}: no ASCII control character (U+0000 to U+001F and U+007F, a
// tab included), no U+2028 or U+2029, which TypeScript reads as line
// terminators, and no space at either end. Its classes are explicit, with
// no \s or ".", so Go's RE2, an ECMAScript engine such as Ajv's and the
// Rust regex crate read it alike; U+2028 and U+2029 stand in it as
// themselves, which the JSON Schema's encoder escapes. validateMemberTitle
// holds the IR to the same rule.
const EnumTitlePattern = `^[^\x00-\x1f\x7f` + "\u2028\u2029" + ` ]([^\x00-\x1f\x7f` + "\u2028\u2029" + `]*[^\x00-\x1f\x7f` + "\u2028\u2029" + ` ])?$`

// validateEnums holds every enum member's title to the rules of D48,
// amended, in every authoring form (EnumTitlePattern, and no "*/", so a
// TypeScript JSDoc comment can hold it), and, ignoring case, unlike every
// other member's title in the enum, so a select's options can be told
// apart. The JSON Schema cannot say the last: Schema.Validate, which the
// loader runs on every form, holds it.
func (s *Schema) validateEnums() []error {
	var errs []error
	for _, name := range sortedStringMapKeys(s.Enums) {
		e := s.Enums[name]
		if e == nil {
			continue
		}
		var titled []EnumValueDef
		for _, v := range e.Values {
			if v.Title == "" {
				continue
			}
			if err := validateMemberTitle(v.Title); err != nil {
				errs = append(errs, fmt.Errorf("enum %s member %s: %w", e.Name, v.Name, err))
				continue
			}
			if i := slices.IndexFunc(titled, func(o EnumValueDef) bool { return strings.EqualFold(o.Title, v.Title) }); i >= 0 {
				errs = append(errs, fmt.Errorf("enum %s: members %s and %s share the title %q", e.Name, titled[i].Name, v.Name, v.Title))
				continue
			}
			titled = append(titled, v)
		}
	}
	return errs
}

// titleNever reports whether r may not stand anywhere in a member's
// title: an ASCII control character, U+2028 or U+2029.
func titleNever(r rune) bool {
	return r < 0x20 || r == 0x7f || r == '\u2028' || r == '\u2029'
}

// validateMemberTitle checks one enum member's non-empty title against
// EnumTitlePattern and "*/", with a message for each way it fails.
func validateMemberTitle(title string) error {
	if strings.TrimFunc(title, func(r rune) bool { return r == ' ' || titleNever(r) }) == "" {
		return errors.New("title must be non-blank")
	}
	first, _ := utf8.DecodeRuneInString(title)
	last, _ := utf8.DecodeLastRuneInString(title)
	control := strings.IndexFunc(title, titleNever)
	switch {
	case first == ' ' || first == '\t' || last == ' ' || last == '\t':
		return errors.New("title must not contain surrounding whitespace")
	case strings.ContainsAny(title, "\r\n\u2028\u2029"):
		return errors.New("title must be one line")
	case control >= 0:
		return fmt.Errorf("title must not contain the control character %U", title[control])
	case strings.Contains(title, "*/"):
		return errors.New("title must not contain */")
	}
	return nil
}

// UnionDef is a format-agnostic representation of a union type.
type UnionDef struct {
	// Name is the union type name.
	Name string `json:"name" yaml:"name"`

	// Description is the human-readable description.
	Description string `json:"description,omitempty" yaml:"description,omitempty"`

	// Comment stores the node-attached comment for the union declaration.
	Comment string `json:"comment,omitempty" yaml:"comment,omitempty"`

	// Types lists the names of all member types.
	Types []string `json:"types" yaml:"types"`
}
