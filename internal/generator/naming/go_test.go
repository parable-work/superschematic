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

// TestParseReadsTheProjectionCaptureTable reads every key of
// [go.projection_capture], with and without pool. A file without the table
// leaves it off.
func TestParseReadsTheProjectionCaptureTable(t *testing.T) {
	n, err := Parse([]byte("npm_scope = \"@x\"\n"), "test")
	if err != nil {
		t.Fatal(err)
	}
	if n.Go.ProjectionCapture.Enabled() {
		t.Errorf("a file without the table enables it: %+v", n.Go.ProjectionCapture)
	}
	for _, tc := range []struct {
		file string
		want GoProjectionCapture
	}{
		{
			"[go.projection_capture]\nqueries = \"ScopeCaptureQueries\"\nuuid_columns = \"ScopeUUIDColumns\"\nsetting = \"app.account_id\"\npool = \"scope\"\n",
			GoProjectionCapture{Queries: "ScopeCaptureQueries", UUIDColumns: "ScopeUUIDColumns", Setting: "app.account_id", Pool: "scope"},
		},
		{
			"[go]\nlimit_constants = \"on\"\n\n[go.projection_capture]\nqueries = \"CaptureQueries\"\nuuid_columns = \"Capture_UUIDs\"\nsetting = \"app.account_id\"\n",
			GoProjectionCapture{Queries: "CaptureQueries", UUIDColumns: "Capture_UUIDs", Setting: "app.account_id"},
		},
	} {
		n, err := Parse([]byte(tc.file), "test")
		if err != nil {
			t.Fatalf("%q: %v", tc.file, err)
		}
		if n.Go.ProjectionCapture != tc.want || !n.Go.ProjectionCapture.Enabled() {
			t.Errorf("%q: [go.projection_capture] = %+v, want %+v, enabled", tc.file, n.Go.ProjectionCapture, tc.want)
		}
	}
}

// TestParseRefusesAProjectionCaptureTableItCannotUse names the key and
// what it takes: a half-set table, a name that is not an exported Go
// identifier, one name for both functions, a setting or a pool no
// projection reads, and a key the table does not have.
func TestParseRefusesAProjectionCaptureTableItCannotUse(t *testing.T) {
	const full = "queries = \"CaptureQueries\"\nuuid_columns = \"CaptureUUIDColumns\"\nsetting = \"app.account_id\"\n"
	table := func(lines string) string { return "[go.projection_capture]\n" + lines }
	without := func(key string) string {
		var kept []string
		for _, line := range strings.Split(strings.TrimSuffix(full, "\n"), "\n") {
			if !strings.HasPrefix(line, key+" ") {
				kept = append(kept, line)
			}
		}
		return strings.Join(kept, "\n") + "\n"
	}
	for _, tc := range []struct{ file, want string }{
		{table(without("queries")), "go.projection_capture.queries is empty: set queries, uuid_columns and setting together"},
		{table(without("uuid_columns")), "go.projection_capture.uuid_columns is empty"},
		{table(without("setting")), "go.projection_capture.setting is empty"},
		{table("pool = \"scope\"\n"), "go.projection_capture.queries is empty"},
		{table(strings.Replace(full, "\"CaptureQueries\"", "\"captureQueries\"", 1)), `go.projection_capture.queries "captureQueries" is not an exported Go identifier`},
		{table(strings.Replace(full, "\"CaptureUUIDColumns\"", "\"Capture-UUIDs\"", 1)), `go.projection_capture.uuid_columns "Capture-UUIDs" is not an exported Go identifier`},
		{table(strings.Replace(full, "\"CaptureQueries\"", "\"Capture Queries\"", 1)), `go.projection_capture.queries "Capture Queries" is not an exported Go identifier`},
		{table(strings.Replace(full, "\"CaptureUUIDColumns\"", "\"CaptureQueries\"", 1)), `go.projection_capture.queries and uuid_columns are both "CaptureQueries"`},
		{table(strings.Replace(full, "\"app.account_id\"", "\"account_id\"", 1)), `go.projection_capture.setting "account_id" is not a setting a projection reads`},
		{table(strings.Replace(full, "\"app.account_id\"", "\"App.Account\"", 1)), `go.projection_capture.setting "App.Account" is not a setting a projection reads`},
		{table(full + "pool = \"Scope\"\n"), `go.projection_capture.pool "Scope" is not a projection pool`},
		{table(full + "pool = \"scope.notes\"\n"), `go.projection_capture.pool "scope.notes" is not a projection pool`},
		{table(full + "query = \"X\"\n"), "unknown keys: go.projection_capture.query"},
	} {
		_, err := Parse([]byte(tc.file), "superschematic.toml")
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%q: err = %v, want it to hold %q", tc.file, err, tc.want)
		}
	}
}
