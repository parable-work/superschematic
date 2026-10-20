package ir

import (
	"bytes"
	"encoding/json"
	"reflect"
	"regexp"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// An enum member's title (D48, amended) is omitted when empty, so IR
// without one is the same bytes, and is written right after the member's
// name. Schema.Validate holds every title to its rules in every authoring
// form, each with its own message, and refuses two members one title,
// ignoring case.
func TestEnumMemberTitles(t *testing.T) {
	plain := EnumValueDef{Name: "OnHold", SerializedAs: "on_hold"}
	data, err := json.Marshal(plain)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(data, []byte(`"title"`)) {
		t.Fatalf("a member without a title wrote the key: %s", data)
	}
	titled := EnumValueDef{Name: "OnHold", Title: "Held for a stock check", Comment: "Waiting on stock.", SerializedAs: "on_hold"}
	data, err = json.Marshal(titled)
	if err != nil {
		t.Fatal(err)
	}
	if want := `{"name":"OnHold","title":"Held for a stock check","comment":"Waiting on stock.","serializedAs":"on_hold"}`; string(data) != want {
		t.Fatalf("marshaled member = %s, want %s", data, want)
	}
	text, err := yaml.Marshal(titled)
	if err != nil {
		t.Fatal(err)
	}
	var fromYAML EnumValueDef
	if err := yaml.Unmarshal(text, &fromYAML); err != nil {
		t.Fatal(err)
	}
	if fromYAML != titled {
		t.Fatalf("YAML round trip = %+v, want %+v\n%s", fromYAML, titled, text)
	}

	valid := func() *Schema {
		schema := NewSchema("orders", SchemaKindGeneral)
		schema.Enums["OrderStatus"] = &EnumDef{Name: "OrderStatus", Values: []EnumValueDef{
			{Name: "Placed"},
			{Name: "OnHold", Title: "Held for a stock check"},
			{Name: "Shipped", Title: "Shipped"},
		}}
		return schema
	}
	if errs := valid().Validate(); len(errs) != 0 {
		t.Fatalf("Validate of titled members = %v", errs)
	}
	for _, tc := range []struct {
		name, title, want string
	}{
		{"a blank title", " \t", "enum OrderStatus member OnHold: title must be non-blank"},
		{"a padded title", "Held ", "enum OrderStatus member OnHold: title must not contain surrounding whitespace"},
		{"a title of two lines", "Held for\na stock check", "enum OrderStatus member OnHold: title must be one line"},
		{"a carriage return", "Held for\ra stock check", "enum OrderStatus member OnHold: title must be one line"},
		{"a line separator", "Held for\u2028a stock check", "enum OrderStatus member OnHold: title must be one line"},
		{"a paragraph separator", "Held for\u2029a stock check", "enum OrderStatus member OnHold: title must be one line"},
		{"a leading tab", "\tHeld", "enum OrderStatus member OnHold: title must not contain surrounding whitespace"},
		{"a tab", "Held\tfor a stock check", "enum OrderStatus member OnHold: title must not contain the control character U+0009"},
		{"a NUL", "Held\x00", "enum OrderStatus member OnHold: title must not contain the control character U+0000"},
		{"a DEL", "Held\x7f for a stock check", "enum OrderStatus member OnHold: title must not contain the control character U+007F"},
		{"a comment's end", "Held */ for a stock check", "enum OrderStatus member OnHold: title must not contain */"},
		{"another member's title", "Shipped", `enum OrderStatus: members OnHold and Shipped share the title "Shipped"`},
		{"another member's title in another case", "SHIPPED", `enum OrderStatus: members OnHold and Shipped share the title "Shipped"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			schema := valid()
			schema.Enums["OrderStatus"].Values[1].Title = tc.title
			var got []string
			for _, err := range schema.Validate() {
				got = append(got, err.Error())
			}
			if !reflect.DeepEqual(got, []string{tc.want}) {
				t.Fatalf("Validate = %q, want %q", got, tc.want)
			}
		})
	}

	// Enums are checked in sorted order, each member in declaration order.
	schema := valid()
	schema.Enums["Colour"] = &EnumDef{Name: "Colour", Values: []EnumValueDef{{Name: "Red", Title: " "}}}
	schema.Enums["OrderStatus"].Values[0].Title = "Held for a stock check"
	var got []string
	for _, err := range schema.Validate() {
		got = append(got, err.Error())
	}
	want := []string{
		"enum Colour member Red: title must be non-blank",
		`enum OrderStatus: members Placed and OnHold share the title "Held for a stock check"`,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Validate = %q, want %q", got, want)
	}
}

// TestEnumTitlePatternIsValidatesRule: EnumTitlePattern, which the
// schema-file JSON Schema writes beside "not": {"pattern": "\\*/"}, and
// validateMemberTitle reach one verdict on every title, so the data forms'
// JSON Schema, in any engine, and Schema.Validate hold a title to one
// rule. The pattern's classes are explicit: it holds no \s and no ".",
// which RE2, ECMAScript and Rust read apart, and no character outside
// ASCII but U+2028 and U+2029, which a JSON encoder escapes.
func TestEnumTitlePatternIsValidatesRule(t *testing.T) {
	if strings.Contains(EnumTitlePattern, `\s`) || strings.Contains(strings.ReplaceAll(EnumTitlePattern, `\x`, ""), ".") {
		t.Fatalf("EnumTitlePattern %q reads \\s or \".\"", EnumTitlePattern)
	}
	for _, r := range EnumTitlePattern {
		if r > 0x7e && r != '\u2028' && r != '\u2029' {
			t.Fatalf("EnumTitlePattern holds %U", r)
		}
	}
	pattern := regexp.MustCompile(EnumTitlePattern)
	var runes []rune
	for r := rune(0); r < 0x100; r++ {
		runes = append(runes, r)
	}
	runes = append(runes, '\u1680', '\u2000', '\u200b', '\u2028', '\u2029', '\u202f', '\u3000', '\ufeff', '\uffff', 0x1f600)
	var titles []string
	for _, r := range runes {
		c := string(r)
		titles = append(titles, c, c+"a", "a"+c, "a"+c+"b", c+c, "a "+c, c+" a")
	}
	titles = append(titles, "Held for a stock check", "Picked and packed", "a*/b", "*/", "a*", "/a", "*", "a  b", "\xff", "a\xffb")
	for _, title := range titles {
		want := pattern.MatchString(title) && !strings.Contains(title, "*/")
		if got := validateMemberTitle(title) == nil; got != want {
			t.Errorf("validateMemberTitle(%q) accepts it: %v; the pattern: %v", title, got, want)
		}
	}
}
