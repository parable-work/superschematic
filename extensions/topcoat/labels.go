package topcoat

import (
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
