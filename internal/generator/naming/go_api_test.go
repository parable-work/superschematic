package naming

import (
	"reflect"
	"strings"
	"testing"
)

// TestTheGoAPITableDefaultsToTheCore: a file without it, and a zero Naming
// filled by OrDefault, leave every key of [go_api] empty, the core's
// router.
func TestTheGoAPITableDefaultsToTheCore(t *testing.T) {
	n, err := Parse([]byte("npm_scope = \"@x\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]Naming{"a parsed file": n, "a zero Naming": (Naming{}).OrDefault()} {
		if !reflect.DeepEqual(got.GoAPI, GoAPIConfig{}) {
			t.Errorf("%s: [go_api] = %+v, want the zero table", name, got.GoAPI)
		}
	}
}

// TestParseReadsTheGoAPITable reads every key.
func TestParseReadsTheGoAPITable(t *testing.T) {
	n, err := Parse([]byte(`[go_api]
validation_error_code = "VL-1"
path_param_error = "{name} is not a segment"
undeclared_input_keys = "keep"

[go_api.validator_names]
minLength = "min_length"
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	want := GoAPIConfig{
		ValidationErrorCode: "VL-1",
		PathParamError:      "{name} is not a segment",
		ValidatorNames:      map[string]string{"minLength": "min_length"},
		UndeclaredInputKeys: GoAPIKeep,
	}
	if !reflect.DeepEqual(n.GoAPI, want) {
		t.Errorf("[go_api] = %+v, want %+v", n.GoAPI, want)
	}
}

// TestParseRefusesAValueTheGoAPITableDoesNotTake names the key and what it
// takes.
func TestParseRefusesAValueTheGoAPITableDoesNotTake(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"[go_api]\nvalidation_error_code = \"a\\nb\"\n", `go_api.validation_error_code "a\nb" holds a line break`},
		{"[go_api]\nvalidation_error_code = 'a\"b'\n", `go_api.validation_error_code "a\"b" holds a line break, a NUL, a quote or a backslash`},
		{"[go_api]\npath_param_error = 'a\\b'\n", `go_api.path_param_error "a\\b" holds a line break`},
		{"[go_api.validator_names]\nenum = \"one_of\"\n", `go_api.validator_names renames "enum", which no route check names: rename one of min, max, minLength, maxLength, pattern, listMin, listMax`},
		{"[go_api.validator_names]\nmin = \"\"\n", `go_api.validator_names renames "min" to an empty name`},
		{"[go_api]\nundeclared_input_keys = \"drop\"\n", `go_api.undeclared_input_keys is "drop": use "refuse" or "keep"`},
	} {
		_, err := Parse([]byte(tc.file), "superschematic.toml")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to hold %q", tc.file, err, tc.want)
		}
	}
}

// TestValidatorNameOf renames a listed validator and keeps any other name.
func TestValidatorNameOf(t *testing.T) {
	c := GoAPIConfig{ValidatorNames: map[string]string{"listMax": "list_max"}}
	if got := c.ValidatorNameOf("listMax"); got != "list_max" {
		t.Errorf("listMax = %q", got)
	}
	if got := c.ValidatorNameOf("min"); got != "min" {
		t.Errorf("min = %q", got)
	}
}

// TestPathParamErrorOf puts the parameter's name in the text, the core's
// when the key is empty.
func TestPathParamErrorOf(t *testing.T) {
	if got := (GoAPIConfig{}).PathParamErrorOf("id"); got != "id must be percent-encoded UTF-8" {
		t.Errorf("the default = %q", got)
	}
	if got := (GoAPIConfig{PathParamError: "{name} is not a segment"}).PathParamErrorOf("id"); got != "id is not a segment" {
		t.Errorf("a set text = %q", got)
	}
}
