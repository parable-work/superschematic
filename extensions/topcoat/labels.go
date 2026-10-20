package topcoat

import (
	"maps"
	"slices"
	"strings"
	"unicode"

	ir "github.com/parable-work/superschematic/ir"
)

// The label rule a form and a display component share: a field is labeled
// by its @docs title, else its name in words; an enum's member by its
// title, else its name in words (D44, amended).

// labelOf is a field's label: its @docs title, or its name in words.
func labelOf(field *ir.FieldDef) string {
	if field.Title != "" {
		return field.Title
	}
	return humanize(field.Name)
}

// enumOptions is each member of enum: the value its JSON holds and its
// label, the member's title, else its name in words. A form's select and
// checkbox group, a filter's, and the label a display component shows
// for a value all take their labels from it.
func enumOptions(enum *ir.EnumDef) []formOption {
	options := make([]formOption, 0, len(enum.Values))
	for _, value := range enum.Values {
		serialized := value.SerializedAs
		if serialized == "" {
			serialized = value.Name
		}
		label := value.Title
		if label == "" {
			label = humanize(value.Name)
		}
		options = append(options, formOption{Value: serialized, Label: label})
	}
	return options
}

// warnSharedLabels logs each enum the crate labels, in a form, an argument
// form or filter, or a display component, two of whose members take one
// label, ignoring case: a select offers the label twice, and a table
// cannot tell the members apart. The IR refuses two members one title,
// but not a title that is another member's name in words, nor two names
// in the same words ({OnHold, Paused /** @title On hold */}). It is a
// warning: the crate builds, and a @title on either member tells them
// apart.
func warnSharedLabels(c *crate, logf func(format string, args ...any)) {
	enums := map[string]*ir.EnumDef{}
	for _, st := range c.FormStructs {
		for _, f := range st.Fields {
			if f.enum != nil {
				enums[f.enum.Name] = f.enum
			}
		}
	}
	for _, form := range c.ArgForms {
		for _, f := range form.Fields {
			if f.enum != nil {
				enums[f.enum.Name] = f.enum
			}
		}
	}
	for _, label := range c.EnumLabels {
		if label.enum != nil {
			enums[label.enum.Name] = label.enum
		}
	}
	for _, name := range slices.Sorted(maps.Keys(enums)) {
		enum := enums[name]
		options := enumOptions(enum)
		for i, option := range options {
			if j := slices.IndexFunc(options[:i], func(o formOption) bool { return strings.EqualFold(o.Label, option.Label) }); j >= 0 {
				logf("  - topcoat: enum %s: members %s and %s share the label %q, so a select offers it twice; a @title tells them apart\n",
					name, enum.Values[j].Name, enum.Values[i].Name, options[j].Label)
			}
		}
	}
}

// humanize is a camelCase or PascalCase name in words, the first
// capitalized: displayName is "Display name". A number that ends a word
// is a word of its own, line1 "Line 1" and addressLine2 "Address line 2",
// unless it follows an acronym or a single letter, which keep it (awsS3Bucket
// is "Aws S3 bucket", userID2 "User ID2", v2 "V2"), or letters follow it
// (md5sum is "Md5sum").
func humanize(name string) string {
	var words []string
	var word []rune
	runes := []rune(name)
	for i, r := range runes {
		boundary := unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1]))
		boundary = boundary || numberEndsWord(runes, i, word)
		if r == '_' || r == '-' || r == ' ' || boundary {
			if len(word) > 0 {
				words = append(words, string(word))
			}
			word = nil
			if r == '_' || r == '-' || r == ' ' {
				continue
			}
		}
		word = append(word, r)
	}
	if len(word) > 0 {
		words = append(words, string(word))
	}
	for i, w := range words {
		if i > 0 && !isAcronym(w) {
			words[i] = strings.ToLower(w)
		}
	}
	text := strings.Join(words, " ")
	if text == "" {
		return name
	}
	first := []rune(text)
	first[0] = unicode.ToUpper(first[0])
	return string(first)
}

func isAcronym(word string) bool {
	return len(word) > 1 && strings.ToUpper(word) == word
}

// numberEndsWord reports whether the digit at runes[i] starts a number that
// is a word of its own after word, the letters before it: word is two
// letters or more and no acronym, and the number ends the name or is
// followed by a separator or a capital, not by a lowercase letter.
func numberEndsWord(runes []rune, i int, word []rune) bool {
	if !unicode.IsDigit(runes[i]) || len(word) < 2 || !unicode.IsLetter(word[len(word)-1]) || isAcronym(string(word)) {
		return false
	}
	for _, r := range word {
		if !unicode.IsLetter(r) {
			return false
		}
	}
	j := i
	for j < len(runes) && unicode.IsDigit(runes[j]) {
		j++
	}
	return j == len(runes) || !unicode.IsLower(runes[j])
}
