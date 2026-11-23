package naming

import (
	"strings"
	"testing"
)

// TestTheSQLTableDefaultsToTheCore: a file without it, and a zero Naming
// filled by OrDefault, carry the core's DDL.
func TestTheSQLTableDefaultsToTheCore(t *testing.T) {
	want := SQLConfig{
		ColumnChecks:       SQLOn,
		ColumnDefaults:     SQLOn,
		NowDefaults:        SQLNowDeclared,
		StateGuardTerminal: DefaultGuardTerminal,
		StateGuardRefused:  DefaultGuardRefused,
	}
	n, err := Parse([]byte("npm_scope = \"@x\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	for name, got := range map[string]Naming{"a parsed file": n, "a zero Naming": (Naming{}).OrDefault()} {
		if got.SQL != want {
			t.Errorf("%s: [sql] = %+v, want %+v", name, got.SQL, want)
		}
	}
}

// TestParseReadsTheSQLTable reads every key, and a key it leaves out keeps
// its default.
func TestParseReadsTheSQLTable(t *testing.T) {
	n, err := Parse([]byte(`[sql]
column_checks = "off"
column_defaults = "off"
now_defaults = "required"
state_guard_refused = "{column} cannot change from % to %"
`), "test")
	if err != nil {
		t.Fatal(err)
	}
	want := SQLConfig{
		ColumnChecks:       SQLOff,
		ColumnDefaults:     SQLOff,
		NowDefaults:        SQLNowRequired,
		StateGuardTerminal: DefaultGuardTerminal,
		StateGuardRefused:  "{column} cannot change from % to %",
	}
	if n.SQL != want {
		t.Errorf("[sql] = %+v, want %+v", n.SQL, want)
	}
}

// TestParseRefusesAValueTheSQLTableDoesNotTake names the key and the
// values it takes.
func TestParseRefusesAValueTheSQLTableDoesNotTake(t *testing.T) {
	for _, tc := range []struct{ file, want string }{
		{"[sql]\ncolumn_checks = \"no\"\n", `sql.column_checks is "no": use "on" or "off"`},
		{"[sql]\ncolumn_defaults = \"yes\"\n", `sql.column_defaults is "yes": use "on" or "off"`},
		{"[sql]\nnow_defaults = \"always\"\n", `sql.now_defaults is "always": use "declared" or "required"`},
		{"[sql]\nstate_guard_terminal = \"{column} is done at %\"\n", "has 1 % signs: write two"},
		{"[sql]\nstate_guard_refused = \"{column}\\nmoves from % to %\"\n", "holds a line break or a NUL"},
	} {
		_, err := Parse([]byte(tc.file), "superschematic.toml")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to hold %q", tc.file, err, tc.want)
		}
	}
}
