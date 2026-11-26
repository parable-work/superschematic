package naming

import (
	"strings"
	"testing"
)

// TestTheGoTableDefaultsToTheCore: a file without it, and a zero Naming
// filled by OrDefault, carry the core's Go output.
func TestTheGoTableDefaultsToTheCore(t *testing.T) {
	n, err := Parse([]byte("npm_scope = \"@x\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]Naming{"a parsed file": n, "a zero Naming": (Naming{}).OrDefault()} {
		if got.Go != defaultGoConfig() {
			t.Errorf("%s: [go] = %+v, want %+v", name, got.Go, defaultGoConfig())
		}
	}
}

// TestParseReadsTheGoTable reads every key.
func TestParseReadsTheGoTable(t *testing.T) {
	n, err := Parse([]byte(`[go]
limit_constants = "on"
optional_zero = "keep"
env_number_scalars = "strconv"
orm_relation_filter_suffix = "Where"
orm_default_selection = "own"
enum_text_methods = "on"
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	want := defaultGoConfig()
	want.LimitConstants = GoOn
	want.OptionalZero = GoOptionalKeep
	want.EnvNumberScalars = GoEnvStrconv
	want.ORMRelationFilterSuffix = "Where"
	want.ORMDefaultSelection = GoSelectOwn
	want.EnumTextMethods = GoOn
	if n.Go != want {
		t.Errorf("[go] = %+v, want %+v", n.Go, want)
	}
	if !n.Go.WritesLimitConstants() || !n.Go.KeepsOptionalZero() || !n.Go.ParsesEnvNumbersWithStrconv() {
		t.Errorf("WritesLimitConstants() %t, KeepsOptionalZero() %t, ParsesEnvNumbersWithStrconv() %t; want all true",
			n.Go.WritesLimitConstants(), n.Go.KeepsOptionalZero(), n.Go.ParsesEnvNumbersWithStrconv())
	}
}

// TestParseRefusesAValueTheGoTableDoesNotTake names the key and the values
// it takes.
func TestParseRefusesAValueTheGoTableDoesNotTake(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"[go]\nlimit_constants = \"yes\"\n", `go.limit_constants is "yes": use "on" or "off"`},
		{"[go]\nenum_text_methods = \"yes\"\n", `go.enum_text_methods is "yes": use "on" or "off"`},
		{"[go]\noptional_zero = \"pointers\"\n", `go.optional_zero is "pointers": use "omit" or "keep"`},
		{"[go]\nenv_number_scalars = \"atoi\"\n", `go.env_number_scalars is "atoi": use "parse" or "strconv"`},
		{"[go]\norm_default_selection = \"none\"\n", `go.orm_default_selection is "none": use "all" or "own"`},
		{"[go]\norm_relation_filter_suffix = \"where\"\n", `go.orm_relation_filter_suffix is "where": use ASCII letters and digits`},
		{"[go]\norm_relation_filter_suffix = \"_Where\"\n", `go.orm_relation_filter_suffix is "_Where"`},
	} {
		_, err := Parse([]byte(tc.file), "superschematic.toml")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to hold %q", tc.file, err, tc.want)
		}
	}
}
