package topcoat

import (
	"strings"
	"unicode"

	ir "github.com/parable-work/superschematic/ir"
)

// The label rule a form and a display component share: a field is labeled
// by its @docs title, else its name in words; an enum's member by its name
// in words.

// labelOf is a field's label: its @docs title, or its name in words.
func labelOf(field *ir.FieldDef) string {
	if field.Title != "" {
		return field.Title
	}
	return humanize(field.Name)
}

// enumOptions is each member of enum: the value its JSON holds and its
// label, the member's name in words.
func enumOptions(enum *ir.EnumDef) []formOption {
	options := make([]formOption, 0, len(enum.Values))
	for _, value := range enum.Values {
		serialized := value.SerializedAs
		if serialized == "" {
			serialized = value.Name
		}
		options = append(options, formOption{Value: serialized, Label: humanize(value.Name)})
	}
	return options
}

// humanize is a camelCase or PascalCase name in words, the first
// capitalized: displayName is "Display name".
func humanize(name string) string {
	var words []string
	var word []rune
	runes := []rune(name)
	for i, r := range runes {
		boundary := unicode.IsUpper(r) && i > 0 && (unicode.IsLower(runes[i-1]) || i+1 < len(runes) && unicode.IsLower(runes[i+1]))
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
